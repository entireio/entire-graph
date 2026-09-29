package sem

// THE OPT-IN SEMANTIC CHANNEL.
//
// Lexical ranking cannot find a function whose body and name share no word with the question. That
// is the agent-phrased query ("where do we give up on a slow upstream"), and it is measured, not
// hypothetical: on an exposed-DEV set of 40 agent-phrased queries the lexical ranking put the
// target in its top ten 14 times; local embeddings of function symbols did it 36 times. The same
// study froze the fusion rule used below — INTERLEAVE, EMBEDDING FIRST, canonical dedupe — and
// checked the controls that rule could have broken: verbatim doc queries and exact-identifier
// queries kept the lexical rank-1 hit at position <= 2.
//
// Everything here is OFF unless the caller configures an embedding endpoint AND a model, and the
// channel is FAIL-OPEN: every way it can fail (no index for this tree, endpoint down, slow, wrong
// shape, wrong model, wrong dimension) returns the lexical answer unchanged plus one warning. It
// never runs in worktree mode, for the same reason the snapshot cache refuses it: a dirty tree has
// no durable identity to key an index on.
//
// The index is a derivative of ONE committed tree, built explicitly (`index --semantic`), and it
// is keyed by that tree, the model name and the text-recipe version. It stores only symbol
// locations and vectors — the ranking still renders source through the normal read path, so a
// file the corpus policy excludes can never be nominated (see planSemanticNominations).

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// semanticRecipeVersion names the TEXT each symbol was embedded from. Changing the recipe
	// changes every vector's meaning, so it is part of the index key: an index built by an older
	// recipe is simply never found, rather than silently compared against a query it was not
	// built for.
	semanticRecipeVersion = 1
	semanticCacheFamily   = "semantic"
	// semanticCacheVersion is v2 since the index began recording the Corpus and Profile it was
	// built from. A v1 index has neither, so a search can no longer prove it describes its corpus;
	// reading one as "no-index" was the silent version of this bump.
	semanticCacheVersion = "v2"
	// semanticTopK is how many nearest symbols the channel contributes. It also bounds nomination:
	// at most this many files are nominated. On the cold selective path every nomination is spent
	// INSIDE MaxIndexedFiles, so the channel never raises the cold parse budget; on the warm
	// preindexed path nominations cost no parse and are purely additive (see
	// planSemanticNominations).
	semanticTopK = 10
	// semanticQueryTimeout bounds the one network round trip a search can make. A local embedder
	// answers a single short query in tens of milliseconds; anything slower is a down or loaded
	// server, and the lexical answer is worth more than waiting for it.
	semanticQueryTimeout       = 3 * time.Second
	semanticBuildBatchSize     = 32
	semanticBuildBatchTimeout  = 120 * time.Second
	semanticMaxDocumentBytes   = 1500
	semanticMaxDocCommentLines = 40
	semanticDocumentPrefix     = "search_document: "
	semanticQueryPrefix        = "search_query: "
	// Response ceilings. A query answer is one vector; a build batch is semanticBuildBatchSize of
	// them. Both are far above any real embedding width, and exist so a misbehaving endpoint cannot
	// make a search allocate without bound.
	semanticMaxQueryResponseBytes = 4 << 20
	semanticMaxBuildResponseBytes = 256 << 20
	semanticMaxDimension          = 1 << 16
	semanticSignal                = "semantic:embedding"
	// semanticOnlySignal is the PROVENANCE of a row the channel synthesized: no lexical candidate
	// existed for its symbol. It is recorded, never inferred from scores — a cosine can be 0 or
	// negative, and a lexical score can be 0 — so SearchResult.SemanticOnly reads this and nothing
	// else. It rides in `signals`, so a JSON consumer can tell the two row kinds apart too.
	semanticOnlySignal = "semantic:only"
	// semanticUnitNormTolerance is how far a stored or freshly normalised row's squared norm may
	// sit from 1. float32 rounding of a unit vector stays orders of magnitude inside it.
	semanticUnitNormTolerance = 1e-3
)

// Semantic status values reported in SearchStats.SemanticStatus. An UNCONFIGURED channel reports
// nothing at all (the field is omitted), which is what keeps the default payload byte-identical.
const (
	SemanticStatusUsed        = "used"
	SemanticStatusOffFlag     = "off:flag"
	SemanticStatusOffWorktree = "off:worktree"
	semanticUnavailablePrefix = "unavailable:"
)

// SemanticConfig configures the opt-in semantic channel. The zero value (and a nil pointer) is
// "not configured": the channel is enabled only when BOTH Endpoint and Model are set.
type SemanticConfig struct {
	// Endpoint is an Ollama-compatible base URL (http://localhost:11434). It must name localhost or
	// a literal loopback address; anything else leaves the channel unavailable. The channel POSTs
	// {"model","input":[...]} to <Endpoint>/api/embed and reads {"embeddings":[[...]]}.
	Endpoint string
	Model    string
	// Disabled turns a configured channel off for one call (`--no-semantic`). It is reported as
	// off:flag rather than omitted, so a payload says the channel was deliberately skipped.
	Disabled bool
	// QueryTimeout overrides semanticQueryTimeout; 0 means the default. A test seam more than a
	// knob — the hang test must not wait three real seconds.
	QueryTimeout time.Duration
}

func (config *SemanticConfig) configured() bool {
	return config != nil && strings.TrimSpace(config.Endpoint) != "" && strings.TrimSpace(config.Model) != ""
}

// ReplayIdentity is the channel's EFFECTIVE configuration as an opaque identity, for callers that
// persist a rendered payload and must not replay it under a different configuration (the CLI's
// search session echo). A payload rendered with the channel on carries its rows and its stats line;
// one rendered with it off (--no-semantic) carries an off:flag line; an unconfigured one carries
// neither — so all three are distinct identities, and the endpoint and model are part of "on".
//
// Unconfigured is "", so an unconfigured caller's identity is unchanged by the channel's existence.
// The endpoint is hashed, never echoed: the identity is written to a state file, and an endpoint
// URL can carry credentials.
func (config *SemanticConfig) ReplayIdentity() string {
	if !config.configured() {
		return ""
	}
	if config.Disabled {
		return "off"
	}
	endpoint := sha256.Sum256([]byte(strings.TrimSpace(config.Endpoint)))
	model := sha256.Sum256([]byte(strings.TrimSpace(config.Model)))
	return "on;model=" + hex.EncodeToString(model[:8]) + ";endpoint=" + hex.EncodeToString(endpoint[:8])
}

// semanticUnavailable is a fail-open reason. It is an error type so the embedding client can say
// WHY it failed in the status vocabulary directly, instead of the caller re-deriving it from text.
type semanticUnavailable struct {
	reason string
	detail string
}

func (err *semanticUnavailable) Error() string {
	if err.detail == "" {
		return err.reason
	}
	return err.reason + ": " + err.detail
}

func unavailable(reason, detail string) *semanticUnavailable {
	return &semanticUnavailable{reason: reason, detail: detail}
}

// semanticOutcome is what the channel did for one search. The wrapper in SearchRepository stamps
// it into the stats in ONE place, so the no-hit return and the ranked return cannot disagree.
type semanticOutcome struct {
	status    string
	detail    string
	hits      []semanticHit
	nominated int
	evicted   int
	seated    int
}

type semanticHit struct {
	FilePath  string
	StartLine int
	Name      string
	Score     float64
}

// applySemanticOutcome records the channel's status, the nomination budget it spent and — only
// when a configured channel could not run — one warning. It is the single point where the channel
// touches the response envelope, and it touches nothing when the channel is not configured.
func applySemanticOutcome(response *SearchResponse, outcome *semanticOutcome) {
	if outcome == nil || outcome.status == "" {
		return
	}
	response.Stats.SemanticStatus = outcome.status
	response.Stats.SemanticNominatedFiles = outcome.nominated
	response.Stats.SemanticEvictedFiles = outcome.evicted
	response.Stats.SemanticResults = outcome.seated
	if strings.HasPrefix(outcome.status, semanticUnavailablePrefix) {
		detail := "semantic channel is configured but was not used for this search (" +
			strings.TrimPrefix(outcome.status, semanticUnavailablePrefix) + ")"
		if outcome.detail != "" {
			detail += ": " + outcome.detail
		}
		detail += "; results are lexical only"
		response.Warnings = append(response.Warnings, ProviderWarning{
			Code:                 "W_SEMANTIC_UNAVAILABLE",
			Severity:             "warning",
			EffectOnCompleteness: "semantic retrieval skipped; ranking is lexical only",
			Detail:               detail,
		})
	}
}

// resolveSemanticChannel decides whether the channel runs for this search and, if it does, returns
// the nearest indexed symbols to the query. Every CHANNEL failure is a status, never an error: the
// caller continues with the lexical answer. The one exception is the CALLER's cancellation: a
// canceled or expired parent context is returned as an error, because "the caller stopped asking"
// is not "the embedder was unavailable" and must not come back as a successful lexical answer.
func resolveSemanticChannel(
	ctx context.Context, config *SemanticConfig, options SearchOptions, tree string,
	corpusFor func(profile string) string, query string,
) (semanticOutcome, error) {
	outcome := resolveSemanticChannelOutcome(ctx, config, options, tree, corpusFor, query)
	if err := ctx.Err(); err != nil {
		return semanticOutcome{}, err
	}
	return outcome, nil
}

func resolveSemanticChannelOutcome(
	ctx context.Context, config *SemanticConfig, options SearchOptions, tree string,
	corpusFor func(profile string) string, query string,
) semanticOutcome {
	if !config.configured() {
		return semanticOutcome{}
	}
	if config.Disabled {
		return semanticOutcome{status: SemanticStatusOffFlag}
	}
	// Policy, not failure: a worktree has no durable identity to key an index on, so the channel
	// is never consulted there. It is reported as off, not unavailable, and carries no warning —
	// `search` defaults to worktree mode, so a warning here would fire on every default call.
	if options.Worktree {
		return semanticOutcome{status: SemanticStatusOffWorktree}
	}
	fail := func(err error) semanticOutcome {
		var reason *semanticUnavailable
		if errors.As(err, &reason) {
			return semanticOutcome{status: semanticUnavailablePrefix + reason.reason, detail: reason.detail}
		}
		// An unclassified error's text is not ours to vouch for (it can carry a path or a URL),
		// so only the stable code travels into the payload.
		return semanticOutcome{status: semanticUnavailablePrefix + "error"}
	}
	if tree == "" {
		return fail(unavailable("no-commit", "the search is not reading a committed tree"))
	}
	if options.DisableCache || options.CacheDir == "" {
		return fail(unavailable("cache-disabled", "the semantic index lives in the cache directory"))
	}
	index, err := loadSemanticIndexContext(ctx, options.CacheDir, tree, config.Model)
	if err != nil {
		return fail(err)
	}
	// The index must describe THIS search's corpus, not just its tree. corpusFor answers with the
	// digest of the prepared committed snapshot this search's own ignore/include and parse policy
	// produces at the profile the index was built from, or "" when no such prepared snapshot
	// exists. Without one nothing proves the index was built from the same corpus, and an index
	// built under a different policy is as good as absent.
	corpus := ""
	if corpusFor != nil {
		corpus = corpusFor(index.Profile)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if corpus == "" {
		return fail(unavailable("no-index",
			"no prepared index for this search's corpus; run `entire graph index --semantic` with the same settings"))
	}
	if index.Corpus != corpus {
		return fail(unavailable("no-index",
			"the semantic index was built from a different corpus; rerun `entire graph index --semantic` with this search's settings"))
	}
	timeout := config.QueryTimeout
	if timeout <= 0 {
		timeout = semanticQueryTimeout
	}
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	vectors, err := semanticEmbed(queryCtx, config, []string{semanticQueryPrefix + query}, semanticMaxQueryResponseBytes)
	if err != nil {
		return fail(err)
	}
	if len(vectors[0]) != index.Dimension {
		return fail(unavailable("dimension-mismatch",
			fmt.Sprintf("query embedding has %d dimensions, index has %d", len(vectors[0]), index.Dimension)))
	}
	hits, err := index.nearest(ctx, vectors[0], semanticTopK)
	if err != nil {
		return fail(err)
	}
	return semanticOutcome{status: SemanticStatusUsed, hits: hits}
}

// semanticNominationPlan is what the channel adds to, and on the cold path takes from, the file
// set a search parses. The lexical selection it was planned against is NOT rewritten: idf and
// query-word presence are always computed from the pre-nomination selection, and a nominated file
// never produces a lexical row (see searchRepository). Warm, that makes the lexical ranking exactly
// the unconfigured one; cold, the only difference is the evicted files (see evicted).
type semanticNominationPlan struct {
	// nominated are the hit files outside the lexical selection whose symbols are loaded so the
	// channel's rows can be rendered. They never produce lexical candidates.
	nominated []string
	// evicted are lexical files that yielded their parse slot to a nomination. Non-empty only on the
	// COLD selective path; their lexical rows are lost, and with their symbols their share of BM25's
	// average length and any call edges they carried — the one treatment cost the channel has,
	// disclosed as stats.semantic_evicted_files. Their content still counts toward idf and presence.
	evicted map[string]bool
}

// planSemanticNominations decides which of the channel's hit files are loaded beside the lexical
// selection, and — on the cold path only — which lexical files give up their parse slot for them.
//
// WARM (additive): the search's own complete snapshot is preindexed, so a nominated file costs no
// parse — its symbols are derived from the snapshot already in memory. Nominations are then purely
// additive: no lexical file yields, and MaxIndexedFiles (a cold parsing guard, not a recall cap —
// the warm git-tree-grep path deliberately keeps every matched file) is not consulted.
//
// COLD (hard cap): every nominated file is a parse, so nominations are spent INSIDE the caller's
// --max-indexed-files: lexical selection plus nominations hold at most max(maxFiles,
// len(selected)) files. Free slots are spent first, then the lexical TAIL yields file by file. Two
// lexical files never yield: the lexical head (the file of the best lexical evidence, so the
// lexical rank-1 hit stays renderable) and any file a hit itself points into (it serves both
// channels).
//
// Either way at most `budget` files (semanticTopK) are nominated. Only a file already in the corpus
// (allFiles: the tree after every ignore rule and include/ignore flag) can be nominated: the index
// stores paths from the tree it was built on, and that tree's policy is not necessarily this
// search's, so admitting a path the corpus excluded would let an index built before a .graphignore
// line reintroduce the file it removes.
func planSemanticNominations(selected, corpus []string, hits []semanticHit, budget, maxFiles int, additive bool) semanticNominationPlan {
	if len(hits) == 0 || budget <= 0 {
		return semanticNominationPlan{}
	}
	inCorpus := make(map[string]bool, len(corpus))
	for _, path := range corpus {
		inCorpus[path] = true
	}
	present := make(map[string]bool, len(selected))
	for _, path := range selected {
		present[path] = true
	}
	hitFiles := make(map[string]bool, len(hits))
	var nominations []string
	for _, hit := range hits {
		hitFiles[hit.FilePath] = true
		if len(nominations) >= budget || present[hit.FilePath] || !inCorpus[hit.FilePath] {
			continue
		}
		nominations = append(nominations, hit.FilePath)
		present[hit.FilePath] = true
	}
	if len(nominations) == 0 {
		return semanticNominationPlan{}
	}
	if additive {
		return semanticNominationPlan{nominated: nominations}
	}
	limit := max(maxFiles, len(selected))
	free := max(0, limit-len(selected))
	// Which lexical files may yield, from the tail forward.
	var yieldable []string
	for index := len(selected) - 1; index >= 1; index-- {
		if !hitFiles[selected[index]] {
			yieldable = append(yieldable, selected[index])
		}
	}
	count := min(len(nominations), free+len(yieldable))
	if count == 0 {
		return semanticNominationPlan{}
	}
	plan := semanticNominationPlan{nominated: nominations[:count:count]}
	for _, path := range yieldable[:max(0, count-free)] {
		if plan.evicted == nil {
			plan.evicted = map[string]bool{}
		}
		plan.evicted[path] = true
	}
	return plan
}

// withoutEvicted is the lexical selection minus the files the plan evicted, in selection order. It
// never writes into selected's backing array, which is shared with the preselection state.
func (plan semanticNominationPlan) withoutEvicted(selected []string) []string {
	if len(plan.evicted) == 0 {
		return selected
	}
	out := make([]string, 0, len(selected)-len(plan.evicted))
	for _, path := range selected {
		if !plan.evicted[path] {
			out = append(out, path)
		}
	}
	return out
}

// joinSemanticNominations adds the nominated files' symbols, call edges, languages and partial
// failures to the maps and snapshot the ranking renders from. searchRepository calls it only once
// the lexical ranking is final, so nothing here can reach a lexical score. The nominated files are
// disjoint from the lexical snapshot's (planSemanticNominations never nominates a selected file),
// and the lexical snapshot's slices are never appended to in place: they can be a cached
// snapshot's backing arrays.
func joinSemanticNominations(
	nomination ProviderSnapshot, snapshot *ProviderSnapshot,
	symbolsByFile map[string][]SymbolRecord, symbolsByID map[string]SymbolRecord, languages map[string]string,
) {
	touched := map[string]bool{}
	for _, symbol := range nomination.Symbols {
		if _, seen := symbolsByID[symbol.ID]; seen && symbol.ID != "" {
			continue
		}
		symbolsByFile[symbol.FilePath] = append(symbolsByFile[symbol.FilePath], symbol)
		symbolsByID[symbol.ID] = symbol
		touched[symbol.FilePath] = true
	}
	for filePath := range touched {
		sort.Slice(symbolsByFile[filePath], func(i, j int) bool {
			left, right := symbolsByFile[filePath][i], symbolsByFile[filePath][j]
			if left.StartLine != right.StartLine {
				return left.StartLine < right.StartLine
			}
			return left.EndLine < right.EndLine
		})
	}
	for _, file := range nomination.Files {
		if _, ok := languages[file.Path]; !ok {
			languages[file.Path] = file.Language
		}
	}
	if len(nomination.Relations) > 0 {
		snapshot.Relations = append(slices.Clip(snapshot.Relations), nomination.Relations...)
	}
	if len(nomination.Header.PartialFailures) > 0 {
		snapshot.Header.PartialFailures = append(
			slices.Clip(snapshot.Header.PartialFailures), nomination.Header.PartialFailures...,
		)
	}
}

// semanticCandidates turns the channel's hits into ranking candidates. A hit that the lexical pool
// already produced reuses that candidate, so the row keeps its lexical evidence and snippet focus;
// otherwise the symbol is rendered the way a graph-expansion neighbour is. A hit whose symbol is
// not in this snapshot is dropped — the index is tree-keyed, so that only happens when the file
// failed to parse or was not selected, and in both cases there is nothing to render.
func semanticCandidates(
	hits []semanticHit, pool []searchCandidate, symbolsByFile map[string][]SymbolRecord,
	read contentReader, languages map[string]string, options SearchOptions,
) []searchCandidate {
	if len(hits) == 0 {
		return nil
	}
	maxRegionLines := options.MaxRegionLines
	if maxRegionLines <= 0 {
		maxRegionLines = defaultSearchMaxRegionLines
	}
	maxSnippetLines := options.MaxSnippetLines
	if maxSnippetLines <= 0 {
		maxSnippetLines = defaultSearchMaxSnippetLines
	}
	bestLexical := map[semanticKey]searchCandidate{}
	for _, candidate := range pool {
		key := semanticCandidateKey(candidate, symbolsByFile)
		if previous, ok := bestLexical[key]; !ok || candidate.score > previous.score {
			bestLexical[key] = candidate
		}
	}
	out := make([]searchCandidate, 0, len(hits))
	for _, hit := range hits {
		symbol, ok := semanticSymbolFor(symbolsByFile[hit.FilePath], hit)
		if !ok {
			continue
		}
		key := semanticSymbolKey(symbol)
		if lexical, ok := bestLexical[key]; ok {
			lexical.result.Signals = appendUnique(append([]string(nil), lexical.result.Signals...), semanticSignal)
			lexical.result.SemanticScore = hit.Score
			out = append(out, lexical)
			continue
		}
		content, readable := read(symbol.FilePath)
		if !readable {
			continue
		}
		lines := strings.Split(content, "\n")
		start, end := clampRegion(symbol.StartLine, symbol.EndLine, len(lines))
		if start == 0 {
			continue
		}
		if end-start+1 > maxRegionLines {
			end = minInt(len(lines), start+maxRegionLines-1)
		}
		snippetStart, snippetEnd := focusedSnippetRegion(start, end, start, maxSnippetLines)
		out = append(out, searchCandidate{
			result: SearchResult{
				FilePath:         symbol.FilePath,
				StartLine:        start,
				EndLine:          end,
				FocusLine:        start,
				SnippetStartLine: snippetStart,
				SnippetEndLine:   snippetEnd,
				Language:         languages[symbol.FilePath],
				Kind:             symbol.Kind,
				SymbolID:         symbol.ID,
				SymbolName:       symbol.Name,
				QualifiedName:    symbol.QualifiedName,
				Signature:        symbol.Signature,
				SymbolStartLine:  symbol.StartLine,
				SymbolEndLine:    symbol.EndLine,
				SemanticScore:    hit.Score,
				Signals:          []string{semanticSignal},
				Snippet:          strings.Join(lines[snippetStart-1:snippetEnd], "\n"),
			},
			aliases: append([]string(nil), symbol.Aliases...),
			// No lexical score exists for this row. The cosine rides here only until fusion,
			// which moves it into result.SemanticScore and leaves the relevance score at 0.
			score:        hit.Score,
			semanticOnly: true,
		})
	}
	return out
}

// SemanticOnly reports whether the semantic channel synthesized this row, read from its recorded
// provenance (the semantic:only signal), never from its scores: its cosine may be 0 or negative,
// and its Score 0 means "not measured", not "worthless".
func (result SearchResult) SemanticOnly() bool {
	return slices.Contains(result.Signals, semanticOnlySignal)
}

func semanticSymbolFor(symbols []SymbolRecord, hit semanticHit) (SymbolRecord, bool) {
	for _, symbol := range symbols {
		if symbol.StartLine == hit.StartLine && symbol.Name == hit.Name && semanticIndexableKind(symbol.Kind) {
			return symbol, true
		}
	}
	return SymbolRecord{}, false
}

// semanticKey is the canonical identity fusion deduplicates on: one row per SYMBOL. Two regions
// of the same function are the same answer to "where is this", but two symbols that merely start
// on the same line (`function a() {} function b() {}`, a class and its first method) are not, so
// the identity is the symbol's ID; the file and a line stand in only for a row that carries no
// symbol at all.
type semanticKey struct {
	file string
	id   string
	line int
}

func semanticSymbolKey(symbol SymbolRecord) semanticKey {
	if symbol.ID != "" {
		return semanticKey{file: symbol.FilePath, id: symbol.ID}
	}
	return semanticKey{file: symbol.FilePath, line: symbol.StartLine}
}

func semanticCandidateKey(candidate searchCandidate, symbolsByFile map[string][]SymbolRecord) semanticKey {
	result := candidate.result
	if result.SymbolID != "" {
		return semanticKey{file: result.FilePath, id: result.SymbolID}
	}
	if symbol, ok := smallestSearchSymbolContainingLine(symbolsByFile[result.FilePath], result.FocusLine); ok {
		return semanticSymbolKey(symbol)
	}
	if result.SymbolStartLine > 0 {
		return semanticKey{file: result.FilePath, line: result.SymbolStartLine}
	}
	return semanticKey{file: result.FilePath, line: result.StartLine}
}

// fuseSemanticCandidates is the frozen fusion rule: interleave EMBEDDING FIRST (e1, g1, e2, g2, ...),
// drop any row whose canonical key already appeared, and cut to topK.
//
// Fusion decides ORDER only; it rewrites no relevance score. Cosine and lexical relevance are two
// scales that do not compare, and the downstream readers of Score (the confidence assessment, the
// full-unit gap test) are calibrated on the lexical one. So a lexical row keeps its measured score
// wherever fusion seats it — rank 2 may legitimately score higher than rank 1, exactly as after
// promoteFixSiteOverLeadingTest — and a row the channel synthesized carries its cosine in
// result.SemanticScore and a relevance score of 0 ("no lexical evidence"), which
// AssessSearchConfidence and the renderers recognise via SearchResult.SemanticOnly.
func fuseSemanticCandidates(
	lexical, semantic []searchCandidate, topK int, keyOf func(searchCandidate) semanticKey,
) ([]searchCandidate, int) {
	if len(semantic) == 0 {
		return lexical, 0
	}
	type fusedRow struct {
		candidate searchCandidate
		semantic  bool
	}
	seen := map[semanticKey]int{}
	var fused []fusedRow
	add := func(candidate searchCandidate, fromSemantic bool) {
		key := keyOf(candidate)
		if position, ok := seen[key]; ok {
			// The lexical side already seated this symbol. The channel matched it too, so the
			// seated row records that provenance (signal and cosine) without moving or rescoring.
			if fromSemantic && !fused[position].semantic {
				seated := &fused[position].candidate
				seated.result.Signals = appendUnique(append([]string(nil), seated.result.Signals...), semanticSignal)
				seated.result.SemanticScore = candidate.result.SemanticScore
			}
			return
		}
		if len(fused) >= topK {
			return
		}
		seen[key] = len(fused)
		fused = append(fused, fusedRow{candidate: candidate, semantic: fromSemantic})
	}
	for index := 0; index < max(len(lexical), len(semantic)); index++ {
		if index < len(semantic) {
			add(semantic[index], true)
		}
		if index < len(lexical) {
			add(lexical[index], false)
		}
	}
	out := make([]searchCandidate, len(fused))
	seated := 0
	for index, row := range fused {
		out[index] = row.candidate
		if !row.semantic {
			continue
		}
		seated++
		if !row.candidate.semanticOnly {
			continue
		}
		if out[index].result.SemanticScore == 0 {
			out[index].result.SemanticScore = row.candidate.score
		}
		// Provenance and the unmeasured score are set together, here, so SemanticOnly never has to
		// infer one from the other.
		out[index].result.Signals = appendUnique(append([]string(nil), out[index].result.Signals...), semanticOnlySignal)
		out[index].score = 0
	}
	return out, seated
}

// stampSemanticLexicalRanks records, on every fused row the lexical ranking produced, that row's
// PRE-FUSION lexical rank (its 1-based position in `lexical`), keyed by the same canonical identity
// fusion deduplicates on — so a lexical row the channel also nominated, and that fusion therefore
// seated at the embedding's position, still carries its lexical rank. Rows the channel synthesized
// keep 0. The context blocks' displacement pool reads it: see searchRelatedDisplacementOrder.
func stampSemanticLexicalRanks(
	fused, lexical []searchCandidate, keyOf func(searchCandidate) semanticKey,
) {
	ranks := make(map[semanticKey]int, len(lexical))
	for index, candidate := range lexical {
		key := keyOf(candidate)
		if _, seen := ranks[key]; !seen {
			ranks[key] = index + 1
		}
	}
	for index := range fused {
		fused[index].result.lexicalRank = ranks[keyOf(fused[index])]
	}
}

// countSemanticSeated counts the delivered primary rows the channel placed or matched: every row
// carrying its signal, including a lexical row the channel also nominated.
func countSemanticSeated(selected []searchCandidate) int {
	seated := 0
	for _, candidate := range selected {
		for _, signal := range candidate.result.Signals {
			if signal == semanticSignal {
				seated++
				break
			}
		}
	}
	return seated
}

// --- index -------------------------------------------------------------------------------------

type semanticIndexSymbol struct {
	FilePath  string `json:"file_path"`
	StartLine int    `json:"start_line"`
	Name      string `json:"name"`
}

// semanticIndexFile is the persisted index. Vectors is one row-major little-endian float32 blob
// (Count x Dimension), L2-normalised at build time so a dot product IS the cosine.
type semanticIndexFile struct {
	Recipe int    `json:"recipe_version"`
	Model  string `json:"model"`
	Tree   string `json:"tree"`
	// Corpus is semanticCorpusDigest of the prepared snapshot the index embedded. The tree alone
	// does not fix WHICH symbols were embedded — ignore/include rules, .graphignore, parse and
	// file limits all shape the snapshot — so an index is reused, and consulted by a search,
	// only for the exact corpus it was built from.
	Corpus string `json:"corpus"`
	// Profile is the snapshot profile the corpus was prepared at, so a search can find the
	// prepared snapshot its own policy yields at that profile and compare corpora.
	Profile   string                `json:"profile"`
	Dimension int                   `json:"dimension"`
	Count     int                   `json:"count"`
	Symbols   []semanticIndexSymbol `json:"symbols"`
	Vectors   []byte                `json:"vectors"`

	decoded []float32
}

// semanticIndexKey is tree + model + recipe. The dimension is part of the index's identity too,
// but it is only known once the model has answered, so it is VALIDATED on load and against every
// query vector rather than addressed: a query of a different width is an unusable index, reported
// as dimension-mismatch.
func semanticIndexKey(tree, model string) string {
	hash := sha256.New()
	writeCacheKeyString(hash, "family", semanticCacheFamily)
	writeCacheKeyString(hash, "tree", tree)
	writeCacheKeyString(hash, "model", model)
	writeCacheKeyString(hash, "recipe", fmt.Sprint(semanticRecipeVersion))
	return hex.EncodeToString(hash.Sum(nil))
}

func semanticIndexEntry(cacheDir, tree, model string) (cacheEntry, error) {
	return newCacheEntry(cacheDir, semanticCacheFamily, semanticCacheVersion, semanticIndexKey(tree, model))
}

func loadSemanticIndex(cacheDir, tree, model string) (*semanticIndexFile, error) {
	return loadSemanticIndexContext(context.Background(), cacheDir, tree, model)
}

// loadSemanticIndexContext reads and validates the index, stopping when ctx is done: the decode
// reads through a context-checking reader and the validation walk checks ctx per row block, so a
// large or hostile entry cannot hold a canceled search.
func loadSemanticIndexContext(ctx context.Context, cacheDir, tree, model string) (*semanticIndexFile, error) {
	entry, err := semanticIndexEntry(cacheDir, tree, model)
	if err != nil {
		return nil, unavailable("no-index", "the semantic cache entry cannot be addressed")
	}
	file, err := entry.open()
	if err != nil {
		return nil, unavailable("no-index", "run `entire graph index --semantic` on this commit")
	}
	defer file.Close()
	var index semanticIndexFile
	// The cache file is not trusted input: its decode and validation errors can quote strings it
	// holds, so the payload gets a stable sentence, never the error text.
	if err := decodeSemanticIndex(semanticContextReader{ctx: ctx, reader: file}, &index); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, unavailable("invalid-index", "the semantic index could not be decoded; rebuild it")
	}
	if err := index.validateContext(ctx, tree, model); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, unavailable("invalid-index", "the semantic index failed validation; rebuild it")
	}
	return &index, nil
}

// semanticMaxIndexBytes bounds the DECOMPRESSED index a search will read. It is far above any
// real index (a million 1024-wide vectors is ~5.5 GB base64 — larger repositories are a known
// limit) and exists so a corrupt or hostile cache entry cannot inflate without bound.
const semanticMaxIndexBytes = 4 << 30

func decodeSemanticIndex(reader io.Reader, index *semanticIndexFile) error {
	unzipped, err := gzip.NewReader(reader)
	if err != nil {
		return err
	}
	defer unzipped.Close()
	decoder := json.NewDecoder(io.LimitReader(unzipped, semanticMaxIndexBytes))
	if err := decoder.Decode(index); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("semantic index has trailing data")
	}
	return nil
}

// semanticContextReader fails a read once its context is done.
type semanticContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader semanticContextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

// semanticScanCheckRows is how many rows a scan walks between context checks.
const semanticScanCheckRows = 4096

// validate is the load-time contract: the header must name this tree, model and recipe, and the
// cardinality and dimension must agree with the blob. Anything else is an unusable index.
func (index *semanticIndexFile) validate(tree, model string) error {
	return index.validateContext(context.Background(), tree, model)
}

func (index *semanticIndexFile) validateContext(ctx context.Context, tree, model string) error {
	switch {
	case index.Recipe != semanticRecipeVersion:
		return fmt.Errorf("recipe version %d, want %d", index.Recipe, semanticRecipeVersion)
	case index.Tree != tree:
		return fmt.Errorf("index names tree %q, search is on %q", index.Tree, tree)
	case index.Model != model:
		return fmt.Errorf("index names model %q, configured model is %q", index.Model, model)
	case index.Dimension <= 0 || index.Dimension > semanticMaxDimension:
		return fmt.Errorf("dimension %d out of range", index.Dimension)
	case index.Count != len(index.Symbols):
		return fmt.Errorf("count %d but %d symbols", index.Count, len(index.Symbols))
	case len(index.Vectors) != index.Count*index.Dimension*4:
		return fmt.Errorf("vector blob is %d bytes, want %d", len(index.Vectors), index.Count*index.Dimension*4)
	}
	index.decoded = make([]float32, index.Count*index.Dimension)
	for i := range index.decoded {
		value := math.Float32frombits(binary.LittleEndian.Uint32(index.Vectors[i*4:]))
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("vector blob holds a non-finite value")
		}
		index.decoded[i] = value
	}
	// Every row must be the unit vector the builder wrote: nearest reads a dot product AS a
	// cosine, which is true only of unit rows. A finite row of arbitrary magnitude (a damaged or
	// forged entry, or an all-zero row) would otherwise rank as a confident, meaningless hit.
	for row := 0; row < index.Count; row++ {
		if row%semanticScanCheckRows == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		var sum float64
		for _, value := range index.decoded[row*index.Dimension : (row+1)*index.Dimension] {
			sum += float64(value) * float64(value)
		}
		if math.IsNaN(sum) || math.IsInf(sum, 0) || math.Abs(sum-1) > semanticUnitNormTolerance {
			return fmt.Errorf("vector row %d is not unit length", row)
		}
	}
	return nil
}

// nearest returns the k indexed symbols with the highest cosine to query (already normalised).
// Ties keep index order, which is (file, start line) order, so the answer is deterministic.
func (index *semanticIndexFile) nearest(ctx context.Context, query []float32, k int) ([]semanticHit, error) {
	type scored struct {
		index int
		score float64
	}
	scores := make([]scored, index.Count)
	for row := 0; row < index.Count; row++ {
		if row%semanticScanCheckRows == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		vector := index.decoded[row*index.Dimension : (row+1)*index.Dimension]
		var dot float64
		for i, value := range vector {
			dot += float64(value) * float64(query[i])
		}
		scores[row] = scored{index: row, score: dot}
	}
	sort.SliceStable(scores, func(i, j int) bool { return scores[i].score > scores[j].score })
	if len(scores) > k {
		scores = scores[:k]
	}
	hits := make([]semanticHit, 0, len(scores))
	for _, entry := range scores {
		symbol := index.Symbols[entry.index]
		hits = append(hits, semanticHit{
			FilePath: symbol.FilePath, StartLine: symbol.StartLine, Name: symbol.Name,
			Score: math.Round(entry.score*10000) / 10000,
		})
	}
	return hits, nil
}

// SemanticIndexReport describes one `index --semantic` run.
type SemanticIndexReport struct {
	Status    string `json:"status"`
	Model     string `json:"model"`
	Tree      string `json:"tree"`
	Recipe    int    `json:"recipe_version"`
	Dimension int    `json:"dimension"`
	Symbols   int    `json:"symbols"`
	Batches   int    `json:"batches"`
	LatencyMS int64  `json:"latency_ms"`
}

// BuildSemanticIndex embeds every function and method of a COMMITTED snapshot and persists the
// index beside the snapshot cache. It refuses a worktree snapshot for the reason the snapshot cache
// does: there is no durable identity to key it on. An existing valid index for the same tree,
// model and recipe is reused unless force is set.
//
// Unlike the query path this is not fail-open: the caller asked for the artifact explicitly, so an
// unreachable endpoint or a malformed answer is an error.
func BuildSemanticIndex(
	ctx context.Context, repo string, snapshot ProviderSnapshot, cacheDir string, config SemanticConfig, force bool,
) (SemanticIndexReport, error) {
	started := time.Now()
	if !config.configured() {
		return SemanticIndexReport{}, errors.New("semantic index requires an embedding endpoint and model")
	}
	if cacheDir == "" {
		return SemanticIndexReport{}, errors.New("semantic index requires a cache directory")
	}
	commit, tree := snapshot.Header.Commit, snapshot.Header.Tree
	if commit == "" || tree == "" {
		return SemanticIndexReport{}, errors.New("semantic index requires a committed HEAD snapshot")
	}
	report := SemanticIndexReport{Model: config.Model, Tree: tree, Recipe: semanticRecipeVersion}
	corpus := semanticCorpusDigest(snapshot.Symbols)
	if !force {
		if existing, err := loadSemanticIndexContext(ctx, cacheDir, tree, config.Model); err == nil && existing.Corpus == corpus {
			report.Status = "reused"
			report.Dimension = existing.Dimension
			report.Symbols = existing.Count
			report.LatencyMS = time.Since(started).Milliseconds()
			return report, nil
		}
	}
	symbols := semanticEligibleSymbols(snapshot.Symbols)
	if err := ctx.Err(); err != nil {
		return SemanticIndexReport{}, err
	}
	// Blob reads at the snapshot's own commit, so the text embedded is the text of the tree the
	// index is keyed on, whatever the worktree holds now.
	read, closeRead, err := openSearchContentReader(ctx, repo, commit, true, nil, nil, 0)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return SemanticIndexReport{}, ctxErr
		}
		return SemanticIndexReport{}, err
	}
	if closeRead != nil {
		defer closeRead()
	}
	linesByFile := map[string][]string{}
	texts := make([]string, 0, len(symbols))
	kept := make([]semanticIndexSymbol, 0, len(symbols))
	for _, symbol := range symbols {
		// A read that fails because the build was canceled must not be mistaken for an unreadable
		// file and skipped: that would publish a partial (or empty) index as complete.
		if err := ctx.Err(); err != nil {
			return SemanticIndexReport{}, err
		}
		lines, ok := linesByFile[symbol.FilePath]
		if !ok {
			content, readable := read(symbol.FilePath)
			if err := ctx.Err(); err != nil {
				return SemanticIndexReport{}, err
			}
			if readable {
				lines = strings.Split(content, "\n")
			}
			linesByFile[symbol.FilePath] = lines
		}
		if lines == nil {
			continue
		}
		texts = append(texts, semanticDocumentText(symbol, lines))
		kept = append(kept, semanticIndexSymbol{FilePath: symbol.FilePath, StartLine: symbol.StartLine, Name: symbol.Name})
	}
	index := semanticIndexFile{
		Recipe: semanticRecipeVersion, Model: config.Model, Tree: tree, Corpus: corpus, Profile: snapshot.Header.Profile,
		Count: len(kept), Symbols: kept,
	}
	var blob bytes.Buffer
	for start := 0; start < len(texts); start += semanticBuildBatchSize {
		end := min(start+semanticBuildBatchSize, len(texts))
		batchCtx, cancel := context.WithTimeout(ctx, semanticBuildBatchTimeout)
		vectors, err := semanticEmbed(batchCtx, &config, texts[start:end], semanticMaxBuildResponseBytes)
		cancel()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return SemanticIndexReport{}, fmt.Errorf("embed batch %d: %w", report.Batches+1, ctxErr)
		}
		if err != nil {
			return SemanticIndexReport{}, fmt.Errorf("embed batch %d: %w", report.Batches+1, err)
		}
		report.Batches++
		for _, vector := range vectors {
			if index.Dimension == 0 {
				index.Dimension = len(vector)
			}
			if len(vector) != index.Dimension {
				return SemanticIndexReport{}, fmt.Errorf("embedding dimension changed mid-build: %d then %d", index.Dimension, len(vector))
			}
			for _, value := range vector {
				var word [4]byte
				binary.LittleEndian.PutUint32(word[:], math.Float32bits(value))
				blob.Write(word[:])
			}
		}
	}
	if index.Count == 0 {
		// An empty index is still a valid answer for this tree ("nothing to embed"), but a
		// zero dimension cannot be validated; record the smallest legal one.
		index.Dimension = 1
	}
	index.Vectors = blob.Bytes()
	// Round-trip the invariants the loader enforces BEFORE writing, so a build can never persist
	// an artifact every later search would reject.
	if err := index.validate(tree, config.Model); err != nil {
		return SemanticIndexReport{}, fmt.Errorf("semantic index failed its own validation: %w", err)
	}
	entry, err := semanticIndexEntry(cacheDir, tree, config.Model)
	if err != nil {
		return SemanticIndexReport{}, err
	}
	// cacheEntry.write is the atomic path: a unique O_EXCL temp in the entry's directory, then a
	// rename over the entry, with the temp removed on every failure. The context is checked
	// immediately before the rename commits, so a build canceled at any point up to publication
	// leaves the previous valid generation in place and reports the cancellation.
	if err := entry.writeCommitting("semantic", index, ctx.Err); err != nil {
		return SemanticIndexReport{}, fmt.Errorf("persist semantic index: %w", err)
	}
	report.Status = "built"
	report.Dimension = index.Dimension
	report.Symbols = index.Count
	report.LatencyMS = time.Since(started).Milliseconds()
	return report, nil
}

// semanticEligibleSymbols is the ordered list of symbols a build embeds: every function and
// method with a start line, in (file, start line, name) order.
func semanticEligibleSymbols(all []SymbolRecord) []SymbolRecord {
	symbols := make([]SymbolRecord, 0, len(all))
	for _, symbol := range all {
		if semanticIndexableKind(symbol.Kind) && symbol.StartLine > 0 {
			symbols = append(symbols, symbol)
		}
	}
	sort.SliceStable(symbols, func(i, j int) bool {
		if symbols[i].FilePath != symbols[j].FilePath {
			return symbols[i].FilePath < symbols[j].FilePath
		}
		if symbols[i].StartLine != symbols[j].StartLine {
			return symbols[i].StartLine < symbols[j].StartLine
		}
		return symbols[i].Name < symbols[j].Name
	})
	return symbols
}

// semanticCorpusDigest fingerprints the corpus a build embeds: the recipe version and every
// eligible symbol's location, name, kind and signature (the recipe's inputs besides the file text,
// which the tree already fixes). Two snapshots of one tree prepared under different ignore/include
// or parse policies hold different symbols and so get different digests; the same prepared
// snapshot always gets the same one.
func semanticCorpusDigest(all []SymbolRecord) string {
	hash := sha256.New()
	writeCacheKeyString(hash, "recipe", fmt.Sprint(semanticRecipeVersion))
	for _, symbol := range semanticEligibleSymbols(all) {
		writeCacheKeyString(hash, "file", symbol.FilePath)
		writeCacheKeyString(hash, "start", fmt.Sprint(symbol.StartLine))
		writeCacheKeyString(hash, "end", fmt.Sprint(symbol.EndLine))
		writeCacheKeyString(hash, "name", symbol.Name)
		writeCacheKeyString(hash, "kind", symbol.Kind)
		writeCacheKeyString(hash, "signature", symbol.Signature)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func semanticIndexableKind(kind string) bool {
	return kind == "function" || kind == "method"
}

// --- text recipe (semanticRecipeVersion 1) -----------------------------------------------------

// semanticDocumentText is recipe 1: "search_document: <split name words>. <doc comment> <signature>",
// cut to semanticMaxDocumentBytes. The name words carry the identifier, the doc comment carries the
// intent in prose (what an agent's question is phrased in), and the signature carries the types.
func semanticDocumentText(symbol SymbolRecord, lines []string) string {
	var builder strings.Builder
	builder.WriteString(semanticDocumentPrefix)
	builder.WriteString(strings.Join(splitSemanticNameWords(symbol.Name), " "))
	builder.WriteString(".")
	if doc := semanticLeadingComment(lines, symbol.StartLine); doc != "" {
		builder.WriteString(" ")
		builder.WriteString(doc)
	}
	signature := strings.TrimSpace(symbol.Signature)
	if signature == "" && symbol.StartLine >= 1 && symbol.StartLine <= len(lines) {
		signature = strings.TrimSpace(lines[symbol.StartLine-1])
	}
	if signature != "" {
		builder.WriteString(" ")
		builder.WriteString(collapseSemanticSpace(signature))
	}
	return truncateSemanticText(builder.String(), semanticMaxDocumentBytes)
}

// splitSemanticNameWords splits camelCase, PascalCase, snake_case and acronym runs into lowercase
// words: parseHTTPResponse_v2 -> parse http response v2. Digits stay attached to their word
// (v2, sha256), which is how they are written in prose too.
func splitSemanticNameWords(name string) []string {
	var words []string
	var current []rune
	flush := func() {
		if len(current) > 0 {
			words = append(words, strings.ToLower(string(current)))
			current = current[:0]
		}
	}
	runes := []rune(name)
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if len(current) > 0 {
			previous := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			switch {
			case unicode.IsUpper(r) && unicode.IsLower(previous):
				flush()
			case unicode.IsUpper(r) && unicode.IsUpper(previous) && nextLower:
				flush()
			}
		}
		current = append(current, r)
	}
	flush()
	return words
}

// semanticLeadingComment is the comment block directly above a declaration, with markers stripped.
// Attribute/decorator lines between the comment and the declaration are skipped, a blank line ends
// the block, and the walk is bounded.
func semanticLeadingComment(lines []string, startLine int) string {
	var collected []string
	for line := startLine - 1; line >= 1 && startLine-line <= semanticMaxDocCommentLines; line-- {
		text := strings.TrimSpace(lines[line-1])
		if text == "" {
			break
		}
		if strings.HasPrefix(text, "@") || strings.HasPrefix(text, "#[") || strings.HasPrefix(text, "[") {
			continue
		}
		stripped, ok := stripSemanticCommentMarker(text)
		if !ok {
			break
		}
		if stripped != "" {
			collected = append(collected, stripped)
		}
	}
	for left, right := 0, len(collected)-1; left < right; left, right = left+1, right-1 {
		collected[left], collected[right] = collected[right], collected[left]
	}
	return collapseSemanticSpace(strings.Join(collected, " "))
}

func stripSemanticCommentMarker(text string) (string, bool) {
	for _, marker := range []string{"///", "//!", "//", "/**", "/*", "*/", "#", "--", ";;", "*"} {
		if strings.HasPrefix(text, marker) {
			text = strings.TrimPrefix(text, marker)
			text = strings.TrimSuffix(strings.TrimSpace(text), "*/")
			return strings.TrimSpace(text), true
		}
	}
	return "", false
}

func collapseSemanticSpace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func truncateSemanticText(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// --- embedding client --------------------------------------------------------------------------

type semanticEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type semanticEmbedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float64 `json:"embeddings"`
}

// semanticHTTPClient never follows a redirect: the endpoint is the one the operator named, and a
// 3xx is reported as the HTTP status it is rather than silently sending the request elsewhere.
//
// It is also LOCAL-ONLY at connection time, not just at URL-parse time. The request carries source
// text and the query, and the promise is that they go to a daemon on this machine. So the
// transport ignores every proxy setting (Proxy is nil — an ambient HTTP(S)_PROXY or a PAC-style
// environment cannot reroute it), and its dialer resolves nothing: "localhost" is mapped to the
// loopback literals directly and any other name or non-loopback address is refused before a socket
// is opened. semanticEndpointURL enforces the same rule up front so a refused endpoint never even
// builds a request.
var semanticHTTPClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           semanticLoopbackDial,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: semanticBuildBatchTimeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	},
}

// errSemanticNotLoopback is the dialer's refusal of a non-loopback destination.
var errSemanticNotLoopback = errors.New("semantic endpoint is not a loopback address")

// semanticLoopbackHost reports whether host is one the channel may talk to: the name "localhost"
// or a LITERAL loopback address (127.0.0.0/8, ::1). A hostname that merely resolves to loopback is
// refused, because what it resolves to is decided outside this process.
func semanticLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// semanticLoopbackDial dials only loopback literals and never consults a resolver.
func semanticLoopbackDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errSemanticNotLoopback
	}
	var targets []string
	switch {
	case strings.EqualFold(host, "localhost"):
		targets = []string{"127.0.0.1", "::1"}
	case semanticLoopbackHost(host):
		targets = []string{host}
	default:
		return nil, errSemanticNotLoopback
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	var lastErr error
	for _, target := range targets {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(target, port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// semanticEmbed calls <Endpoint>/api/embed and returns one L2-normalised vector per input. Every
// failure is a *semanticUnavailable naming the fail-open reason.
func semanticEmbed(ctx context.Context, config *SemanticConfig, inputs []string, maxResponseBytes int64) ([][]float32, error) {
	base, err := url.Parse(strings.TrimSpace(config.Endpoint))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, unavailable("endpoint-invalid", "want an http(s) base URL such as http://localhost:11434")
	}
	if !semanticLoopbackHost(base.Hostname()) {
		return nil, unavailable("endpoint-not-loopback",
			"the semantic endpoint must be localhost or a literal loopback address (127.0.0.0/8, ::1)")
	}
	// Credential-bearing URL state is refused before any request, not redacted after one: a
	// local embedder needs none of it, and a transport error would otherwise quote it back.
	if base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return nil, unavailable("endpoint-invalid", "the endpoint URL must not carry userinfo, a query or a fragment")
	}
	endpoint := base.JoinPath("api", "embed").String()
	body, err := json.Marshal(semanticEmbedRequest{Model: config.Model, Input: inputs})
	if err != nil {
		return nil, unavailable("error", "the embedding request could not be encoded")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, unavailable("endpoint-invalid", "the endpoint URL cannot carry a request")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := semanticHTTPClient.Do(request)
	if err != nil {
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return nil, unavailable("timeout", "")
		}
		// The transport's error text quotes the request URL; the code alone is the diagnostic.
		return nil, unavailable("endpoint", "the embedding endpoint could not be reached")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return nil, unavailable(fmt.Sprintf("http-%d", response.StatusCode), "")
	}
	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return nil, unavailable("timeout", "")
		}
		return nil, unavailable("endpoint", "the embedding response could not be read")
	}
	if int64(len(raw)) > maxResponseBytes {
		return nil, unavailable("bad-response", "response exceeds the size ceiling")
	}
	var decoded semanticEmbedResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, unavailable("bad-response", "the response is not an embedding answer")
	}
	// The answered model name is service-controlled text; it is compared, never quoted.
	if decoded.Model != "" && !sameSemanticModel(decoded.Model, config.Model) {
		return nil, unavailable("model-mismatch", "the endpoint answered with a different model")
	}
	if len(decoded.Embeddings) != len(inputs) {
		return nil, unavailable("bad-response",
			fmt.Sprintf("%d embeddings for %d inputs", len(decoded.Embeddings), len(inputs)))
	}
	out := make([][]float32, len(decoded.Embeddings))
	for row, vector := range decoded.Embeddings {
		if len(vector) == 0 || len(vector) > semanticMaxDimension || len(vector) != len(decoded.Embeddings[0]) {
			return nil, unavailable("bad-response", "embeddings have inconsistent or empty dimensions")
		}
		normalised, err := normaliseSemanticVector(vector)
		if err != nil {
			return nil, err
		}
		out[row] = normalised
	}
	return out, nil
}

// normaliseSemanticVector L2-normalises one embedding without overflowing. Summing raw squares is
// NOT safe even when every component is finite: [1e308, 0] squares to +Inf, the division by
// sqrt(+Inf) then turns every component into 0, and an all-zero "unit" vector is accepted as a
// meaningless tie with every row. So the vector is first scaled by its largest magnitude — every
// scaled component lies in [-1, 1], their squares sum to at most the dimension — and every output
// component is checked again, so nothing non-finite or degenerate can be returned.
func normaliseSemanticVector(vector []float64) ([]float32, error) {
	var largest float64
	for _, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, unavailable("bad-response", "embedding holds a non-finite value")
		}
		largest = math.Max(largest, math.Abs(value))
	}
	if largest == 0 {
		return nil, unavailable("bad-response", "embedding has zero norm")
	}
	var sum float64
	for _, value := range vector {
		scaled := value / largest
		sum += scaled * scaled
	}
	norm := math.Sqrt(sum)
	if math.IsNaN(norm) || math.IsInf(norm, 0) || norm <= 0 {
		return nil, unavailable("bad-response", "embedding norm is not a positive finite number")
	}
	normalised := make([]float32, len(vector))
	var check float64
	for i, value := range vector {
		component := float32((value / largest) / norm)
		if math.IsNaN(float64(component)) || math.IsInf(float64(component), 0) {
			return nil, unavailable("bad-response", "embedding does not normalise to a finite vector")
		}
		normalised[i] = component
		check += float64(component) * float64(component)
	}
	if math.Abs(check-1) > semanticUnitNormTolerance {
		return nil, unavailable("bad-response", "embedding does not normalise to a unit vector")
	}
	return normalised, nil
}

// sameSemanticModel treats Ollama's implicit ":latest" tag as the untagged name, so a model
// configured as "nomic-embed-text" is not reported as a mismatch when the server echoes
// "nomic-embed-text:latest".
func sameSemanticModel(answered, configured string) bool {
	return strings.TrimSuffix(answered, ":latest") == strings.TrimSuffix(configured, ":latest")
}
