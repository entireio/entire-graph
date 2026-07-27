package sem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/entireio/entire-graph/internal/gitutil"
)

const (
	// Benchmark-calibrated: the graphmark suites (official SWE-bench Multilingual 300 among
	// them) measured top-k 10 with 6-line snippets as the best agent config; the old defaults
	// (20 / 40) produced ~15KB search outputs that get re-read every subsequent turn.
	defaultSearchTopK              = 10
	defaultSearchContextLines      = 8
	defaultSearchMaxRegionLines    = 80
	defaultSearchMaxSnippetLines   = 6
	defaultSearchMaxRegionsPerFile = 3
	defaultSearchMaxIndexedFiles   = 96
	deepSearchMaxIndexedFiles      = 256
	searchIndexedFilesPerResult    = 3
	maxSearchQueryTerms            = 48
	minGitGrepPreselectionFiles    = 10_000
	sparseSearchChunkStrideLines   = 60
	sparseSearchRRFConstant        = 60
	sparseSearchFileRankWeight     = 2
	deepSearchSparseNumerator      = 5
	deepSearchSparseDenominator    = 8
	deepSearchSemanticHeadDivisor  = 32
	maxDeepSearchSemanticHead      = 10
	maxSparseSearchQueryTerms      = 64
	maxSparseSearchFileBytes       = 2 * 1024 * 1024
	maxSearchContentCacheBytes     = 32 * 1024 * 1024
	maxSearchContentCacheFileBytes = 2 * 1024 * 1024
	searchDiversityRelevanceRatio  = 0.75
	maxSearchGitGrepPatterns       = 4096
)

// SearchOptions controls local issue-to-code retrieval. Search reads the same
// HEAD/worktree view and ignore rules as provider snapshots.
type SearchOptions struct {
	Worktree          bool
	IgnoreFiles       []string
	IncludeFiles      []string
	Profile           Profile
	TopK              int
	ContextLines      int
	MaxRegionLines    int
	MaxSnippetLines   int
	MaxRegionsPerFile int
	MaxParseBytes     int
	CacheDir          string
	DisableCache      bool
	MaxIndexedFiles   int
	IndexAllFiles     bool
	MaxContextBytes   int
	// Deep enables the exhaustive sparse (BM25 chunk) retrieval pass and its
	// reciprocal-rank fusion with the semantic ranking.
	//
	// It used to be switched on IMPLICITLY by `TopK > 10`, which made every observable
	// property of a search discontinuous at that boundary: the git-tree-grep preselector
	// was disabled, every file in the repo was read, the indexed-file pool shrank, region
	// deduplication stopped and the reported score stopped being a relevance score. Asking
	// for one more result is not a request to change retrieval strategy, so the strategy is
	// now an explicit choice and top-k only changes how many rows come back.
	Deep bool
}

// SearchResult is a ranked source region suitable for direct agent context.
type SearchResult struct {
	Rank             int      `json:"rank"`
	Score            float64  `json:"score"`
	FilePath         string   `json:"file_path"`
	StartLine        int      `json:"start_line"`
	EndLine          int      `json:"end_line"`
	FocusLine        int      `json:"focus_line"`
	SnippetStartLine int      `json:"snippet_start_line"`
	SnippetEndLine   int      `json:"snippet_end_line"`
	Language         string   `json:"language,omitempty"`
	Kind             string   `json:"kind,omitempty"`
	SymbolID         string   `json:"symbol_id,omitempty"`
	SymbolName       string   `json:"symbol_name,omitempty"`
	QualifiedName    string   `json:"qualified_name,omitempty"`
	Signature        string   `json:"signature,omitempty"`
	Signals          []string `json:"signals"`
	// Section groups a result for presentation. Empty (omitted) means the primary list of
	// candidate fix sites; see search_section.go for the other values and why the grouping is
	// a label rather than a filter.
	Section string `json:"section,omitempty"`
	Snippet string `json:"snippet"`
}

type SearchStats struct {
	FilesScanned                   int    `json:"files_scanned"`
	PreselectionBackend            string `json:"preselection_backend,omitempty"`
	PreselectionPasses             int    `json:"preselection_passes,omitempty"`
	PreselectionFilesExamined      int    `json:"preselection_files_examined,omitempty"`
	UsagePreselectionBackend       string `json:"identifier_usage_preselection_backend,omitempty"`
	UsagePreselectionPasses        int    `json:"identifier_usage_preselection_passes,omitempty"`
	UsagePreselectionFilesExamined int    `json:"identifier_usage_preselection_files_examined,omitempty"`
	// Content-read counters report blobs hydrated into the Go process. Git's
	// own immutable-tree scans are represented by the backend/pass/examined
	// counters above; their internal byte IO is deliberately not estimated.
	FilesContentRead   int   `json:"files_content_read_during_preselection"`
	QueryFilesRead     int   `json:"files_content_read_during_query"`
	QueryBytesRead     int64 `json:"bytes_content_read_during_query"`
	UsageFilesRead     int   `json:"files_content_read_for_identifier_usage,omitempty"`
	UsageBytesRead     int64 `json:"bytes_content_read_for_identifier_usage,omitempty"`
	FilesIndexed       int   `json:"files_indexed"`
	SymbolsConsidered  int   `json:"symbols_considered"`
	LexicalCandidates  int   `json:"lexical_candidates"`
	GraphCandidates    int   `json:"graph_candidates"`
	IdentifierUsages   int   `json:"identifier_usage_candidates,omitempty"`
	NeighborCandidates int   `json:"same_container_neighbor_candidates,omitempty"`
	BridgeCandidates   int   `json:"same_file_bridge_candidates,omitempty"`
	SparseCandidates   int   `json:"sparse_candidates"`
	SparseFilesRead    int   `json:"sparse_files_content_read"`
	CandidatesSelected int   `json:"candidates_selected"`
	ResultBytes        int   `json:"result_bytes"`
	ContextBudgetBytes int   `json:"context_budget_bytes,omitempty"`
	ResultsDropped     int   `json:"results_dropped_by_budget,omitempty"`
	// SnippetsTruncated counts results carrying LESS source than the ranker produced for
	// them, whether the byte fitter compacted them or the snippet allocator demoted them to
	// a locator. A result grown to a complete symbol body is not truncated.
	SnippetsTruncated int `json:"snippets_truncated_by_budget,omitempty"`
	// CompleteSymbols counts results returned as the complete body of their enclosing
	// symbol; LocatorSnippets counts tail results demoted to a locator window to pay for
	// them. Together they report how the byte budget was allocated (schema 1.x additive).
	CompleteSymbols int `json:"complete_symbol_snippets,omitempty"`
	LocatorSnippets int `json:"locator_snippets,omitempty"`
	// RelatedSites counts entries in the related-site block: the other places the top hit's
	// change usually has to land (callers, sibling implementations, near-duplicate bodies).
	// They are funded out of the tail of the ranking, so this count also says how much of the
	// ranking's tail was worth less than a structural neighbour of the top hit.
	RelatedSites int `json:"related_sites,omitempty"`
	// ContainerMapBytes is what the container map cost. It is reported separately from
	// ResultBytes because the map is NOT funded out of the ranking: a payload that spent its
	// budget on complete head bodies must not lose one to buy the map, so the map is additive
	// and its cost is stated rather than hidden.
	ContainerMapBytes int   `json:"container_map_bytes,omitempty"`
	IndexCacheHit     bool  `json:"index_cache_hit"`
	IndexLatencyMS    int64 `json:"index_latency_ms"`
	QueryLatencyMS    int64 `json:"query_latency_ms"`
	TotalLatencyMS    int64 `json:"total_latency_ms"`
	// SearchLatencyMS is retained as the backwards-compatible name for total
	// retrieval latency. New consumers should use TotalLatencyMS and the
	// separate preselection, index, and query phases.
	SearchLatencyMS    int64 `json:"search_latency_ms"`
	PreselectLatencyMS int64 `json:"preselect_latency_ms"`
}

type SearchResponse struct {
	Query    string         `json:"query"`
	RepoRoot string         `json:"repo_root"`
	Commit   string         `json:"commit,omitempty"`
	Tree     string         `json:"tree,omitempty"`
	Profile  string         `json:"profile"`
	Results  []SearchResult `json:"results"`
	// ContainerMap maps the top hit's enclosing container — the file's extent and the
	// container's members with their line ranges — so a range read can be sized from this one
	// response instead of by reading the whole file. Nil when the payload has no ranked code
	// hit whose container the graph knows. See search_container_map.go.
	ContainerMap    *SearchContainerMap `json:"container_map,omitempty"`
	Stats           SearchStats         `json:"stats"`
	Warnings        []ProviderWarning   `json:"warnings"`
	PartialFailures []PartialFailure    `json:"partial_failures"`
	Completeness    CompletenessReport  `json:"completeness"`
}

type searchQuery struct {
	rawLower string
	terms    []string
	termSet  map[string]bool
	weights  map[string]float64
	// words holds only the terms the caller actually WROTE as words: the maximal
	// alphanumeric runs of the raw query. It is deliberately not the term set —
	// `terms` also contains camelCase/compound fragments and morphological variants
	// mined out of identifiers, which are evidence for matching but not for intent.
	// See searchQuerySupplied.
	words map[string]bool
	// wordSequence is the same words in the order the caller wrote them, duplicates kept.
	// Reconstructing the identifier a caller typed ("update market sizes" ->
	// updateMarketSizes) is order-dependent, so the set above cannot do it.
	wordSequence []string
	// matchableWords is wordSequence without the words that cannot contribute a match to
	// any document in this corpus — words that never became search terms (stop words) and
	// terms no indexed file contains. Scoring reads the query through this sequence so an
	// unmatchable word cannot revoke a bonus the matchable ones earned.
	// Populated by withCorpusPresence once document frequencies are known.
	matchableWords []string
	// identifierTokens holds the compaction of every raw token the caller wrote in
	// identifier shape (mixed case, or containing `_` `.` `/` `:`). Such a token IS the
	// caller naming a symbol, whatever else the query says around it.
	identifierTokens map[string]bool
}

type searchCandidate struct {
	result     SearchResult
	termCounts map[string]int
	docLength  int
	baseScore  float64
	score      float64
}

type searchContentReadTracker struct {
	read  contentReader
	files int
	bytes int64
}

func (tracker *searchContentReadTracker) Read(path string) (string, bool) {
	content, ok := tracker.read(path)
	if ok {
		tracker.files++
		tracker.bytes += int64(len(content))
	}
	return content, ok
}

func cachedContentReader(read contentReader) contentReader {
	type entry struct {
		content string
		ok      bool
	}
	cache := map[string]entry{}
	cachedBytes := 0
	return func(path string) (string, bool) {
		if cached, exists := cache[path]; exists {
			return cached.content, cached.ok
		}
		content, ok := read(path)
		if len(content) <= maxSearchContentCacheFileBytes && cachedBytes+len(content) <= maxSearchContentCacheBytes {
			cache[path] = entry{content: content, ok: ok}
			cachedBytes += len(content)
		}
		return content, ok
	}
}

var searchWordPattern = regexp.MustCompile(`[[:alnum:]_./:+#-]+`)
var sparseSearchWordPattern = regexp.MustCompile(`[[:alpha:]][[:alnum:]_]*|[[:digit:]]+`)

var searchStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true,
	"at": true, "be": true, "but": true, "by": true, "can": true, "change": true,
	"code": true, "did": true, "does": true, "for": true, "from": true, "how": true,
	"i": true, "in": true, "into": true, "is": true, "it": true,
	"make": true, "not": true, "of": true, "on": true, "or": true, "our": true,
	// Politeness is addressed to the tool, never to the corpus: a word like these carries
	// no retrieval intent, so indexing it can only add noise. This is a refinement, not the
	// mechanism — see matchesExactSymbolForm and scoreSearchCandidates, which make ANY
	// unmatchable word inert, listed or not.
	"kindly": true, "please": true, "pls": true, "thanks": true,
	"should": true, "support": true, "that": true, "the": true,
	"this": true, "to": true, "use": true, "uses": true, "using": true,
	"we": true, "what": true, "when": true, "where": true, "which": true, "while": true,
	"with": true, "without": true,
}

var sparseSearchStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true,
	"at": true, "be": true, "been": true, "but": true, "by": true,
	"can": true, "could": true, "do": true, "does": true, "for": true,
	"from": true, "had": true, "has": true, "have": true, "how": true,
	"i": true, "if": true, "in": true, "into": true, "is": true,
	"it": true, "its": true, "may": true, "not": true, "of": true,
	"on": true, "or": true, "our": true, "should": true, "that": true,
	"the": true, "their": true, "then": true, "this": true, "to": true,
	"was": true, "we": true, "were": true, "what": true, "when": true,
	"where": true, "which": true, "why": true, "will": true, "with": true,
	"would": true, "you": true, "your": true,
}

// SearchRepository performs local hybrid lexical/semantic retrieval. It uses
// no qrels, hosted models, embeddings, or network access.
func SearchRepository(ctx context.Context, repo, providerVersion, query string, options SearchOptions) (SearchResponse, error) {
	q := buildSearchQuery(query)
	if len(q.terms) == 0 {
		return SearchResponse{}, errors.New("search query has no meaningful terms")
	}
	if options.TopK <= 0 {
		options.TopK = defaultSearchTopK
	}
	if options.ContextLines < 0 {
		return SearchResponse{}, errors.New("search context lines cannot be negative")
	}
	if options.ContextLines == 0 {
		options.ContextLines = defaultSearchContextLines
	}
	if options.MaxRegionLines <= 0 {
		options.MaxRegionLines = defaultSearchMaxRegionLines
	}
	if options.MaxSnippetLines <= 0 {
		options.MaxSnippetLines = defaultSearchMaxSnippetLines
	}
	if options.MaxRegionsPerFile <= 0 {
		options.MaxRegionsPerFile = defaultSearchMaxRegionsPerFile
	}
	if options.MaxIndexedFiles <= 0 {
		options.MaxIndexedFiles = defaultSearchIndexedFiles(options.TopK)
	}
	if options.Profile == "" {
		options.Profile = ProfileSyntaxOnly
	}
	sparseQuery := buildSparseSearchQuery(query)
	searchStarted := time.Now()
	baseSnapshotOptions := ProviderSnapshotOptions{
		NoNetwork:     true,
		Worktree:      options.Worktree,
		IgnoreFiles:   options.IgnoreFiles,
		IncludeFiles:  options.IncludeFiles,
		MaxParseBytes: options.MaxParseBytes,
		Profile:       options.Profile,
	}
	var preindexedSnapshot ProviderSnapshot
	preindexCacheHit := false
	indexStarted := time.Now()
	if !options.Worktree && !options.DisableCache {
		var err error
		preindexedSnapshot, preindexCacheHit, err = loadCachedCompleteSearchSnapshot(
			ctx, repo, providerVersion, baseSnapshotOptions, options.CacheDir,
		)
		if err != nil {
			return SearchResponse{}, err
		}
	}
	preindexLoadLatency := time.Since(indexStarted)
	preselectStarted := time.Now()
	selection, err := preselectSearchFiles(
		ctx, repo, q, sparseQuery, options, preindexedSnapshot, preindexCacheHit,
	)
	if err != nil {
		return SearchResponse{}, err
	}
	preselectLatency := time.Since(preselectStarted)
	selectedFiles := selection.files
	if len(selectedFiles) == 0 {
		// A no-hit query still reports the health of an already-preindexed HEAD
		// graph. Do not build a cold graph merely to return no results, but do
		// preserve cached partial failures/completeness and cache-hit provenance.
		cachedSnapshot, cacheHit := preindexedSnapshot, preindexCacheHit
		// Tree, not commit, is what determines whether the graph is still valid:
		// two different commits sharing a tree parse identically, so only a tree
		// change mid-search is a real race worth rejecting.
		if cacheHit && selection.commit != "" && cachedSnapshot.Header.Tree != selection.tree {
			return SearchResponse{}, errors.New("repository HEAD changed during search; retry against a stable commit")
		}
		totalLatency := time.Since(searchStarted).Milliseconds()
		repoRoot, commit, tree := selection.repoRoot, selection.commit, selection.tree
		warnings := selection.warnings
		partialFailures := []PartialFailure{}
		completeness := CompletenessReport{Languages: map[string]LanguageCompleteness{}, Relations: map[string]int{}}
		symbolsConsidered := 0
		if cacheHit {
			repoRoot = cachedSnapshot.Header.RepoRoot
			commit = cachedSnapshot.Header.Commit
			tree = cachedSnapshot.Header.Tree
			warnings = cachedSnapshot.Header.Warnings
			partialFailures = cachedSnapshot.Header.PartialFailures
			if partialFailures == nil {
				partialFailures = []PartialFailure{}
			}
			completeness = cachedSnapshot.Header.Completeness
			symbolsConsidered = len(cachedSnapshot.Symbols)
		}
		return SearchResponse{
			Query:    query,
			RepoRoot: repoRoot,
			Commit:   commit,
			Tree:     tree,
			Profile:  string(options.Profile),
			Results:  []SearchResult{},
			Stats: SearchStats{
				FilesScanned:              selection.filesScanned,
				PreselectionBackend:       selection.preselectionBackend,
				PreselectionPasses:        selection.preselectionPasses,
				PreselectionFilesExamined: selection.preselectionFilesExamined,
				FilesContentRead:          selection.filesContentRead,
				FilesIndexed:              0,
				SymbolsConsidered:         symbolsConsidered,
				ResultBytes:               serializedSearchResultBytes([]SearchResult{}),
				ContextBudgetBytes:        options.MaxContextBytes,
				IndexCacheHit:             cacheHit,
				IndexLatencyMS:            preindexLoadLatency.Milliseconds(),
				PreselectLatencyMS:        preselectLatency.Milliseconds(),
				TotalLatencyMS:            totalLatency,
				SearchLatencyMS:           totalLatency,
			},
			Warnings:        warnings,
			PartialFailures: partialFailures,
			Completeness:    completeness,
		}, nil
	}

	// The provider graph and the lexical query scope are independent. A full
	// graph gives relation expansion repository-wide context, while only the
	// preselected files need content hydration for this query.
	onlyFiles := selectedFiles
	if options.IndexAllFiles || len(selectedFiles) == selection.filesScanned {
		// Canonicalize complete snapshots to the query-independent cache key so
		// `graph index` and all-files search share one durable artifact.
		onlyFiles = nil
	}
	snapshotOptions := baseSnapshotOptions
	snapshotOptions.OnlyFiles = onlyFiles
	snapshot, cacheHit := preindexedSnapshot, preindexCacheHit
	indexLatency := preindexLoadLatency
	if !cacheHit {
		indexStarted = time.Now()
		snapshot, cacheHit, err = loadOrBuildSearchGraphSnapshot(ctx, repo, providerVersion, snapshotOptions, options.CacheDir, options.DisableCache)
		if err != nil {
			return SearchResponse{}, err
		}
		indexLatency += time.Since(indexStarted)
	}
	// See the analogous no-hit guard above: tree identity is what matters here.
	if selection.commit != "" && snapshot.Header.Tree != selection.tree {
		return SearchResponse{}, errors.New("repository HEAD changed during search; retry against a stable commit")
	}
	queryStarted := time.Now()
	useHead := !options.Worktree && snapshot.Header.Commit != ""
	read, closeSource, err := openSearchContentReader(
		ctx, repo, snapshot.Header.Commit, useHead, options.IgnoreFiles, options.IncludeFiles,
	)
	if err != nil {
		return SearchResponse{}, err
	}
	if closeSource != nil {
		defer closeSource()
	}
	queryReads := &searchContentReadTracker{read: read}
	read = cachedContentReader(queryReads.Read)

	symbolsByFile := make(map[string][]SymbolRecord)
	symbolsByID := make(map[string]SymbolRecord, len(snapshot.Symbols))
	for _, symbol := range snapshot.Symbols {
		symbolsByFile[symbol.FilePath] = append(symbolsByFile[symbol.FilePath], symbol)
		symbolsByID[symbol.ID] = symbol
	}
	for filePath := range symbolsByFile {
		sort.Slice(symbolsByFile[filePath], func(i, j int) bool {
			left, right := symbolsByFile[filePath][i], symbolsByFile[filePath][j]
			if left.StartLine != right.StartLine {
				return left.StartLine < right.StartLine
			}
			return left.EndLine < right.EndLine
		})
	}

	fileLanguages := make(map[string]string, len(snapshot.Files))
	for _, file := range snapshot.Files {
		fileLanguages[file.Path] = file.Language
	}

	fileDF := make(map[string]int, len(q.terms))
	sparseDF := selection.sparseDF
	if sparseDF == nil {
		sparseDF = make(map[string]int, len(sparseQuery.terms))
	}
	sparseDocumentCount := selection.sparseDocumentCount
	sparseDocumentLength := selection.sparseDocumentLength
	var candidates []searchCandidate
	sparseCandidates := append([]searchCandidate(nil), selection.sparseCandidates...)
	// Corpus statistics come first, in their own pass. Scoring has to know which query
	// words this repository can match at all before a single candidate is built: the
	// all-or-nothing bonuses (exact-symbol, all-query-terms) are otherwise judged against
	// words nothing contains, which silently penalizes the documents that matched the rest
	// of the query. The content reader is memoized, so the second pass normally re-reads
	// nothing; a selection larger than the content cache can fall back to disk for the
	// overflow, which rereadFiles/rereadBytes below keep out of the read telemetry.
	indexableFiles := make([]string, 0, len(selectedFiles))
	for _, filePath := range selectedFiles {
		if err := ctx.Err(); err != nil {
			return SearchResponse{}, err
		}
		content, ok := read(filePath)
		if !ok || strings.IndexByte(content, 0) >= 0 {
			continue
		}
		indexableFiles = append(indexableFiles, filePath)
		lowerContent := strings.ToLower(content)
		lowerPath := strings.ToLower(filepath.ToSlash(filePath))
		for _, term := range q.terms {
			if strings.Contains(lowerContent, term) || strings.Contains(lowerPath, term) {
				fileDF[term]++
			}
		}
	}
	q = q.withCorpusPresence(fileDF)
	statisticsPassFiles, statisticsPassBytes := queryReads.files, queryReads.bytes
	for _, filePath := range indexableFiles {
		if err := ctx.Err(); err != nil {
			return SearchResponse{}, err
		}
		content, ok := read(filePath)
		if !ok {
			continue
		}
		lines := strings.Split(content, "\n")
		candidates = append(candidates, candidatesForFile(
			q, filePath, fileLanguages[filePath], lines, symbolsByFile[filePath], options,
		)...)
	}
	// Re-reading a file the content cache had to drop pulls in the same file, not another
	// one, so it must not inflate what the query is reported to have read.
	rereadFiles := queryReads.files - statisticsPassFiles
	rereadBytes := queryReads.bytes - statisticsPassBytes
	sparseFilesRead := selection.sparseFilesContentRead
	if options.Deep && len(sparseQuery.terms) > 0 {
		for _, filePath := range selection.sparseFiles {
			if selection.sparsePrecomputedFiles[filePath] {
				continue
			}
			if err := ctx.Err(); err != nil {
				return SearchResponse{}, err
			}
			content, ok := read(filePath)
			sparseFilesRead++
			if !ok || len(content) > maxSparseSearchFileBytes || strings.IndexByte(content, 0) >= 0 {
				continue
			}
			lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
			if len(lines) == 1 && lines[0] == "" {
				continue
			}
			language := ""
			if spec, detected := languageForContent(filePath, content); detected {
				language = spec.language
			}
			fileSparse, documentCount, documentLength := sparseCandidatesForFile(
				sparseQuery, filePath, language, lines, options,
			)
			sparseCandidates = append(sparseCandidates, fileSparse...)
			sparseDocumentCount += documentCount
			sparseDocumentLength += documentLength
			for _, candidate := range fileSparse {
				for term := range candidate.termCounts {
					sparseDF[term]++
				}
			}
		}
		if len(selection.sparseFiles) > 0 && len(selection.sparseFiles) < selection.filesScanned {
			sparseDocumentCount = scaleSearchCorpusValue(
				sparseDocumentCount, selection.filesScanned, len(selection.sparseFiles),
			)
			sparseDocumentLength = scaleSearchCorpusValue(
				sparseDocumentLength, selection.filesScanned, len(selection.sparseFiles),
			)
		}
	}

	stats := SearchStats{
		FilesScanned:              selection.filesScanned,
		PreselectionBackend:       selection.preselectionBackend,
		PreselectionPasses:        selection.preselectionPasses,
		PreselectionFilesExamined: selection.preselectionFilesExamined,
		FilesContentRead:          selection.filesContentRead,
		FilesIndexed:              len(selectedFiles),
		SymbolsConsidered:         len(snapshot.Symbols),
		LexicalCandidates:         len(candidates),
		SparseCandidates:          len(sparseCandidates),
		SparseFilesRead:           sparseFilesRead,
		IndexCacheHit:             cacheHit,
		IndexLatencyMS:            indexLatency.Milliseconds(),
		PreselectLatencyMS:        preselectLatency.Milliseconds(),
	}
	// Graph centrality: inbound CALLS/IMPLEMENTS degree per symbol. Implementation code is called
	// by many sites; leaf/trait-dispatch/trivial methods (and most test helpers) are not. Ranking
	// with this signal lets the real fix site outrank a flood of common-named leaves — the graph
	// already resolves the callers, search just wasn't using them (field feedback: search behaved
	// like keyword BM25 with graph_candidates:0, ranking tests/leaves over the implementation).
	// Requires CALLS in the snapshot (profile fast/full); empty at syntax-only, so the bonus no-ops.
	inboundDegree := make(map[string]int)
	for _, rel := range snapshot.Relations {
		switch rel.Type {
		case "CALLS", "ASYNC_CALLS", "IMPLEMENTS", "OVERRIDES":
			if rel.ToID != "" {
				inboundDegree[rel.ToID]++
			}
		}
	}
	scoreSearchCandidates(candidates, q, fileDF, maxInt(1, len(selectedFiles)), inboundDegree)
	sortSearchCandidates(candidates)

	graphCandidates := expandGraphCandidates(candidates, q, snapshot.Relations, symbolsByID, read, fileLanguages, options)
	stats.GraphCandidates = len(graphCandidates)
	candidates = append(candidates, graphCandidates...)
	sortSearchCandidates(candidates)
	expansionSeeds := append([]searchCandidate(nil), candidates...)
	usageFilesBefore := queryReads.files
	usageBytesBefore := queryReads.bytes
	usagePreselection := searchScanTelemetry{}
	identifierUsages := expandIdentifierUsageCandidates(
		ctx, expansionSeeds, q, symbolsByID, symbolsByFile, read, fileLanguages, options,
		committedUsageFileSelector(
			repo, selection.commit, useHead, fileLanguages, options.Deep, selection.filesScanned, &usagePreselection,
		),
	)
	usageFilesRead := queryReads.files - usageFilesBefore
	usageBytesRead := queryReads.bytes - usageBytesBefore
	if err := ctx.Err(); err != nil {
		return SearchResponse{}, err
	}
	stats.IdentifierUsages = len(identifierUsages)
	stats.UsagePreselectionBackend = usagePreselection.backend
	stats.UsagePreselectionPasses = usagePreselection.passes
	stats.UsagePreselectionFilesExamined = usagePreselection.filesExamined
	neighbors := expandSameContainerNeighborCandidates(ctx, expansionSeeds, q, symbolsByID, symbolsByFile, read, fileLanguages, options)
	if err := ctx.Err(); err != nil {
		return SearchResponse{}, err
	}
	stats.NeighborCandidates = len(neighbors)
	bridges := expandSameFileBridgeCandidates(ctx, expansionSeeds, q, symbolsByFile, read, fileLanguages, options)
	if err := ctx.Err(); err != nil {
		return SearchResponse{}, err
	}
	stats.BridgeCandidates = len(bridges)
	candidates = append(candidates, identifierUsages...)
	candidates = append(candidates, neighbors...)
	candidates = append(candidates, bridges...)
	candidates = dedupeSemanticMirrorCandidates(candidates, q, symbolsByID)
	// File-class prior then near-duplicate collapse, in that order: the prior
	// decides WHICH copy of a duplicated document survives, the collapse frees
	// the budget the other copies would have spent. Both run before selection so
	// the reclaimed result slots go to genuinely different code.
	applySearchFileClassPrior(candidates, q)
	sortSearchCandidates(candidates)
	candidates = collapseNearDuplicateCandidates(candidates)
	semantic := selectDiverseCandidates(candidates, options.TopK, options.MaxRegionsPerFile)
	selected := semantic
	if len(sparseCandidates) > 0 {
		attachSparseCandidateSymbols(sparseCandidates, symbolsByFile)
		scoreSparseCandidates(sparseCandidates, sparseQuery, sparseDF, sparseDocumentCount, sparseDocumentLength)
		applySearchFileClassPrior(sparseCandidates, q)
		sortSearchCandidates(sparseCandidates)
		sparseCandidates = dedupeSemanticMirrorCandidates(sparseCandidates, q, symbolsByID)
		sortSearchCandidates(sparseCandidates)
		sparseCandidates = collapseNearDuplicateCandidates(sparseCandidates)
		// Fusion decides the ORDER. The reported score stays a relevance score on the
		// semantic scale (sparse rows are rescaled onto it by rescaleFusedCandidateScores),
		// because the score is the only signal a caller has for "is this a real hit or is
		// the repo simply missing what I asked for". Overwriting it with 1/rank — which the
		// deep path used to do — destroyed exactly that signal: every payload came back
		// looking like 1, 0.5, 0.333 whether the top hit was perfect or worthless.
		selected = rescaleFusedCandidateScores(
			selectHybridCandidates(semantic, sparseCandidates, options.TopK),
			semantic, sparseCandidates,
		)
	}
	sparseHydrationReads := hydrateSparseCandidates(selected, read)
	stats.SparseFilesRead += sparseHydrationReads
	results := make([]SearchResult, 0, len(selected))
	for i := range selected {
		selected[i].result.Rank = i + 1
		selected[i].result.Score = math.Round(selected[i].score*10000) / 10000
		if selected[i].result.Signals == nil {
			selected[i].result.Signals = []string{}
		}
		results = append(results, selected[i].result)
	}
	ranked := append([]SearchResult(nil), results...)
	results, resultBytes, dropped, _ := fitSearchResultsToBudget(results, q, options.MaxContextBytes)
	// The fitter decides HOW MANY results fit; the allocator decides how the bytes they are
	// allowed are spent. It runs second, on the already-seated results, so a hit whose
	// complete enclosing symbol fits is returned whole — removing the follow-up file read
	// an agent would otherwise pay a whole turn for — funded, when the budget is tight, by
	// demoting the tail of the ranking to locators.
	enclosures := planSearchEnclosures(results, symbolsByID, symbolsByFile, read, defaultSearchEnclosureMaxLines)
	results, completeSymbols, locators := allocateSearchSnippets(
		results, enclosures, options.MaxContextBytes, searchEnclosureGrowthBytes,
		searchEnclosureHeadRanks, minInt(searchEnclosureTailSnippetLines, options.MaxSnippetLines),
	)
	stats.CompleteSymbols = completeSymbols
	stats.LocatorSnippets = locators
	// Truncation is measured against the ranking, by rank, so it has to be read off the
	// allocator's output BEFORE the related-site block renumbers the payload.
	truncated := countBudgetTruncatedResults(ranked, results)
	// Sectioning first: it decides which hit anchors the related-site block, because a
	// docs-and-fixtures hit at rank 1 is not a fix site and its neighbourhood is not the
	// neighbourhood of the change.
	results = assignSearchSections(results, q)
	if anchors := searchRelatedAnchors(results, symbolsByID, symbolsByFile, searchEnclosureHeadRanks); len(anchors) > 0 {
		sites := selectSearchRelatedSites(
			results, anchors, q, snapshot.Relations, symbolsByID, symbolsByFile, read, searchRelatedSiteLimit,
		)
		results, stats.RelatedSites = mergeSearchRelatedSites(results, sites, read, options.MaxContextBytes)
	}
	// The map is built LAST, from the payload that is actually going out: its anchor must be
	// the hit the caller will read, after sectioning and the related-site merge have decided
	// which result that is.
	containerMap := fitSearchContainerMap(
		buildSearchContainerMap(results, symbolsByID, symbolsByFile, read),
		searchContainerMapMaxBytes,
	)
	if containerMap != nil {
		stats.ContainerMapBytes = len(RenderSearchContainerMap(containerMap, false))
	}
	stats.CandidatesSelected = len(results)
	resultBytes = serializedSearchResultBytes(results)
	stats.ResultBytes = resultBytes
	stats.ContextBudgetBytes = options.MaxContextBytes
	stats.ResultsDropped = dropped
	stats.SnippetsTruncated = truncated
	// Query and usage counters are disjoint physical reads. Identifier lookups
	// already satisfied by the shared content cache add zero usage bytes.
	stats.QueryFilesRead = queryReads.files - usageFilesRead - rereadFiles
	stats.QueryBytesRead = queryReads.bytes - usageBytesRead - rereadBytes
	stats.UsageFilesRead = usageFilesRead
	stats.UsageBytesRead = usageBytesRead
	stats.QueryLatencyMS = time.Since(queryStarted).Milliseconds()
	stats.TotalLatencyMS = time.Since(searchStarted).Milliseconds()
	stats.SearchLatencyMS = stats.TotalLatencyMS
	if results == nil {
		results = []SearchResult{}
	}
	partialFailures := snapshot.Header.PartialFailures
	if partialFailures == nil {
		partialFailures = []PartialFailure{}
	}
	return SearchResponse{
		Query:           query,
		RepoRoot:        snapshot.Header.RepoRoot,
		Commit:          snapshot.Header.Commit,
		Tree:            snapshot.Header.Tree,
		Profile:         string(options.Profile),
		Results:         results,
		ContainerMap:    containerMap,
		Stats:           stats,
		Warnings:        snapshot.Header.Warnings,
		PartialFailures: partialFailures,
		Completeness:    snapshot.Header.Completeness,
	}, nil
}

func openSearchContentReader(
	ctx context.Context,
	repo, commit string,
	useHead bool,
	ignoreFiles, includeFiles []string,
) (contentReader, func() error, error) {
	if useHead {
		batch, err := gitutil.NewBatchFileReader(ctx, repo, commit)
		if err != nil {
			return nil, nil, err
		}
		read := func(path string) (string, bool) {
			if strings.Contains(path, "\n") {
				content, ok, err := gitutil.ShowFile(ctx, repo, commit, path)
				return content, ok && err == nil
			}
			content, ok, err := batch.ReadFile(path)
			return content, ok && err == nil
		}
		return read, batch.Close, nil
	}
	_, read, _, closeSource, err := openSource(ctx, repo, "", ignoreFiles, includeFiles)
	return read, closeSource, err
}

func defaultSearchIndexedFiles(topK int) int {
	// Shallow interactive searches keep the original cold-start bound. Deeper
	// rankings need a wider file pool or preselection becomes the recall limit
	// before TopK and per-file diversity can take effect.
	return minInt(
		deepSearchMaxIndexedFiles,
		maxInt(
			defaultSearchMaxIndexedFiles,
			minInt(topK, deepSearchMaxIndexedFiles)*searchIndexedFilesPerResult,
		),
	)
}

func fitSearchResultsToBudget(results []SearchResult, q searchQuery, budget int) ([]SearchResult, int, int, int) {
	originalCount := len(results)
	if budget <= 0 || len(results) == 0 {
		return results, serializedSearchResultBytes(results), 0, 0
	}

	for count := len(results); count > 0; count-- {
		// The per-result share is a CAP as well as a floor: no single result may eat the
		// budget, which is what keeps a huge signature or region from starving the rest of
		// the ranking. Deliberate whole-body snippets are added afterwards, by
		// allocateSearchSnippets, which is bounded separately.
		//
		// The per-result floor is a fixed 256 bytes, not budget/count: below that a result
		// degrades to a single truncated line and stops being usable, so the fitter drops
		// whole results (reported in results_dropped_by_budget) rather than keeping a
		// larger number of unusable ones.
		perResult := maxInt(256, (budget-2-(count-1))/count)
		compacted := make([]SearchResult, count)
		truncated := 0
		for index := range compacted {
			compacted[index], _ = compactSearchResultToBytes(results[index], q, perResult)
			if compacted[index].Snippet != results[index].Snippet || compacted[index].Signature != results[index].Signature {
				truncated++
			}
		}
		resultBytes := serializedSearchResultBytes(compacted)
		if resultBytes <= budget {
			return compacted, resultBytes, originalCount - count, truncated
		}
	}
	return []SearchResult{}, serializedSearchResultBytes([]SearchResult{}), originalCount, 0
}

func compactSearchResultToBytes(result SearchResult, q searchQuery, budget int) (SearchResult, int) {
	if size := serializedSearchResultBytes(result); size <= budget {
		return result, size
	}
	result.Signature = truncateSearchText(result.Signature, 192, q)
	if size := serializedSearchResultBytes(result); size <= budget {
		return result, size
	}

	lines := strings.Split(result.Snippet, "\n")
	focus := result.FocusLine - result.SnippetStartLine
	if focus < 0 || focus >= len(lines) {
		focus = len(lines) / 2
	}
	bestSpan, bestBalance := 0, len(lines)+1
	var best SearchResult
	bestSize := 0
	for left := 0; left <= focus; left++ {
		for right := focus; right < len(lines); right++ {
			candidate := result
			candidate.SnippetStartLine = result.SnippetStartLine + left
			candidate.SnippetEndLine = result.SnippetStartLine + right
			candidate.Snippet = strings.Join(lines[left:right+1], "\n")
			size := serializedSearchResultBytes(candidate)
			if size > budget {
				continue
			}
			span := right - left + 1
			balance := (focus - left) - (right - focus)
			if balance < 0 {
				balance = -balance
			}
			if span > bestSpan || (span == bestSpan && balance < bestBalance) {
				best, bestSize = candidate, size
				bestSpan, bestBalance = span, balance
			}
		}
	}
	if bestSpan > 0 {
		return best, bestSize
	}
	candidate := result
	candidate.SnippetStartLine = result.SnippetStartLine + focus
	candidate.SnippetEndLine = candidate.SnippetStartLine
	candidate.Snippet = truncateSearchText(lines[focus], maxInt(16, budget/3), q)
	return candidate, serializedSearchResultBytes(candidate)
}

func truncateSearchText(value string, maxBytes int, q searchQuery) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	center := len(value) / 2
	lower := strings.ToLower(value)
	for _, term := range q.terms {
		if index := strings.Index(lower, term); index >= 0 {
			center = index + len(term)/2
			break
		}
	}
	window := maxBytes - len("... ") - len(" ...")
	if window <= 0 {
		return ""
	}
	start := maxInt(0, center-window/2)
	end := minInt(len(value), start+window)
	start = maxInt(0, end-window)
	for start < end && !utf8RuneStart(value[start]) {
		start++
	}
	for end > start && end < len(value) && !utf8RuneStart(value[end]) {
		end--
	}
	if start >= end {
		return ""
	}
	prefix, suffix := "", ""
	if start > 0 {
		prefix = "... "
	}
	if end < len(value) {
		suffix = " ..."
	}
	return prefix + value[start:end] + suffix
}

func utf8RuneStart(value byte) bool {
	return value&0xc0 != 0x80
}

func serializedSearchResultBytes(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(encoded)
}

type searchFileCandidate struct {
	path  string
	score float64
	// matchedWeight is the query weight this file matched, kept so the coverage share of
	// the file score can be normalized AFTER the scan — by then the terms no file matched
	// are known and can be left out of the denominator. See applySearchFileCoverage.
	matchedWeight float64
}

// applySearchFileCoverage adds the query-coverage share of the preselection score once the
// scan is done. The denominator is the weight of the terms at least one file matched, not of
// everything the caller typed: a term the corpus does not contain would otherwise shrink the
// coverage share of every file and re-order them against their path evidence, which is how a
// single nonsense word used to change which files got indexed at all.
func applySearchFileCoverage(files []searchFileCandidate, q searchQuery, matchedTerms []bool) {
	queryWeight := 0.0
	for index, term := range q.terms {
		if index < len(matchedTerms) && matchedTerms[index] {
			queryWeight += q.weights[term]
		}
	}
	if queryWeight <= 0 {
		return
	}
	for index := range files {
		files[index].score += 4 * files[index].matchedWeight / queryWeight
	}
}

type searchFileSelection struct {
	files                     []string
	sparseFiles               []string
	sparseCandidates          []searchCandidate
	sparseDF                  map[string]int
	sparsePrecomputedFiles    map[string]bool
	sparseDocumentCount       int
	sparseDocumentLength      int
	sparseFilesContentRead    int
	repoRoot                  string
	commit                    string
	tree                      string
	warnings                  []ProviderWarning
	filesScanned              int
	filesContentRead          int
	preselectionBackend       string
	preselectionPasses        int
	preselectionFilesExamined int
}

type searchScanTelemetry struct {
	backend       string
	passes        int
	filesExamined int
}

func preselectSearchFiles(
	ctx context.Context,
	repo string,
	q, sparseQuery searchQuery,
	options SearchOptions,
	preindexedSnapshot ProviderSnapshot,
	preindexCacheHit bool,
) (searchFileSelection, error) {
	source, err := prepareSource(ctx, repo, ProviderSnapshotOptions{
		NoNetwork:    true,
		Worktree:     options.Worktree,
		IgnoreFiles:  options.IgnoreFiles,
		IncludeFiles: options.IncludeFiles,
	})
	if err != nil {
		return searchFileSelection{}, err
	}
	if source.close != nil {
		defer source.close()
	}
	selection := searchFileSelection{
		repoRoot:                  source.absRepo,
		commit:                    source.commit,
		tree:                      source.tree,
		warnings:                  append([]ProviderWarning{}, source.warnings...),
		filesScanned:              len(source.paths),
		preselectionBackend:       "inventory",
		preselectionPasses:        1,
		preselectionFilesExamined: len(source.paths),
		sparseFiles:               append([]string(nil), source.paths...),
		sparseDF:                  make(map[string]int, len(sparseQuery.terms)),
		sparsePrecomputedFiles:    make(map[string]bool),
	}
	// Interactive committed-tree search can ask Git's optimized object-store
	// scanner for every eligible content match in one fixed-string batch. Keep
	// every matched/path-evidenced file: MaxIndexedFiles is a cold selective-
	// indexing guard, not a retrieval recall cap once the graph is preindexed.
	// Deeper sparse search retains the exhaustive path until corpus statistics
	// can be persisted with the preindex.
	//
	// Tree identity is what makes the preindexed graph exact for source's
	// current HEAD; the grep below runs against source.commit directly, so a
	// same-tree preindex built at a different commit is still an exact match.
	exactFullPreindex := preindexCacheHit &&
		preindexedSnapshot.Header.Tree == source.tree
	grepPatterns, grepSafe := searchGitGrepPatterns(q.terms)
	if exactFullPreindex && !options.Worktree && !options.Deep && grepSafe {
		matches, grepErr := gitutil.GrepTreePaths(ctx, source.absRepo, source.commit, grepPatterns)
		if grepErr == nil {
			selection.files = committedSearchFiles(source.paths, matches, q)
			selection.sparseFiles = append([]string(nil), selection.files...)
			selection.preselectionBackend = "git-tree-grep"
			selection.preselectionPasses = 1
			selection.preselectionFilesExamined = len(source.paths)
			return selection, nil
		}
	}
	if options.IndexAllFiles || len(source.paths) <= options.MaxIndexedFiles {
		selection.files = append([]string(nil), source.paths...)
		return selection, nil
	}
	// Which terms any file matches is not known until the scan is done, so the coverage
	// share of every file score is added afterwards, over the matched terms only
	// (applySearchFileCoverage). Until then a file carries its matched weight unnormalized.
	matchedAnywhere := make([]bool, len(q.terms))
	matcher := newSearchTermMatcher(q.terms)
	scanPaths := source.paths
	usedGitIndexPreselection := false
	if shouldUseGitGrepPreselection(options.Worktree, len(source.paths)) {
		matches, grepErr := gitutil.GrepIndexMatches(ctx, source.absRepo, searchGitGrepPreselectionPatterns(q), 32)
		tracked, trackedErr := gitutil.ListIndexFiles(ctx, source.absRepo)
		if grepErr == nil && trackedErr == nil {
			usedGitIndexPreselection = true
			allowed := make(map[string]bool, len(source.paths))
			for _, filePath := range source.paths {
				allowed[filePath] = true
			}
			trackedSet := make(map[string]bool, len(tracked))
			for _, filePath := range tracked {
				trackedSet[filePath] = true
			}
			termMatches := make(map[string][]bool)
			for _, match := range matches {
				if !allowed[match.Path] {
					continue
				}
				seen := termMatches[match.Path]
				if seen == nil {
					seen = make([]bool, len(q.terms))
				}
				for index, matched := range matcher.match(match.Text) {
					seen[index] = seen[index] || matched
				}
				termMatches[match.Path] = seen
			}
			provisional := make([]searchFileCandidate, 0, len(termMatches)+16)
			grepMatchedAnywhere := make([]bool, len(q.terms))
			for filePath, seen := range termMatches {
				matchedWeight := 0.0
				for index, matched := range seen {
					if matched {
						matchedWeight += q.weights[q.terms[index]]
						grepMatchedAnywhere[index] = true
					}
				}
				pathScore := pathSearchScore(q, filePath)
				provisional = append(provisional, searchFileCandidate{
					path:          filePath,
					score:         2*pathScore + matchedWeight + searchPathPrior(q, filePath),
					matchedWeight: matchedWeight,
				})
			}
			untracked := make([]string, 0, 16)
			for _, filePath := range source.paths {
				if !trackedSet[filePath] {
					untracked = append(untracked, filePath)
					continue
				}
				if _, exists := termMatches[filePath]; !exists {
					if pathScore := pathSearchScore(q, filePath); pathScore > 0 {
						provisional = append(provisional, searchFileCandidate{
							path:  filePath,
							score: 2*pathScore + searchPathPrior(q, filePath),
						})
					}
				}
			}
			applySearchFileCoverage(provisional, q, grepMatchedAnywhere)
			sort.Slice(provisional, func(i, j int) bool {
				if provisional[i].score != provisional[j].score {
					return provisional[i].score > provisional[j].score
				}
				return provisional[i].path < provisional[j].path
			})
			poolLimit := len(provisional)
			if threshold := (len(provisional)-1)/4 + 1; options.MaxIndexedFiles < threshold {
				poolLimit = options.MaxIndexedFiles * 4
			}
			scanPaths = make([]string, 0, poolLimit+len(untracked))
			selection.sparseFiles = make([]string, 0, len(provisional)+len(untracked))
			for _, candidate := range provisional {
				selection.sparseFiles = append(selection.sparseFiles, candidate.path)
			}
			selection.sparseFiles = append(selection.sparseFiles, untracked...)
			for _, candidate := range provisional[:poolLimit] {
				scanPaths = append(scanPaths, candidate.path)
			}
			scanPaths = append(scanPaths, untracked...)
			if len(scanPaths) == 0 {
				scanPaths = source.paths
			}
		}
	}
	var sparseMu sync.Mutex
	var contentReadMu sync.Mutex
	var matchedMu sync.Mutex
	contentReads := 0
	scoreFile := func(filePath string) (searchFileCandidate, bool) {
		if err := ctx.Err(); err != nil {
			return searchFileCandidate{}, false
		}
		content, ok := source.read(filePath)
		if ok {
			contentReadMu.Lock()
			contentReads++
			contentReadMu.Unlock()
		}
		if !ok || strings.IndexByte(content, 0) >= 0 {
			return searchFileCandidate{}, false
		}
		if options.Deep && len(sparseQuery.terms) > 0 && len(content) <= maxSparseSearchFileBytes {
			lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
			if len(lines) > 1 || lines[0] != "" {
				language := ""
				if spec, detected := languageForContent(filePath, content); detected {
					language = spec.language
				}
				fileSparse, documentCount, documentLength := sparseCandidatesForFile(
					sparseQuery, filePath, language, lines, options,
				)
				sparseMu.Lock()
				selection.sparseCandidates = append(selection.sparseCandidates, fileSparse...)
				selection.sparseDocumentCount += documentCount
				selection.sparseDocumentLength += documentLength
				selection.sparseFilesContentRead++
				selection.sparsePrecomputedFiles[filePath] = true
				for _, candidate := range fileSparse {
					for term := range candidate.termCounts {
						selection.sparseDF[term]++
					}
				}
				sparseMu.Unlock()
			}
		}
		pathScore := pathSearchScore(q, filePath)
		matchedWeight := 0.0
		matched := matcher.match(content)
		for index, hit := range matched {
			if hit {
				matchedWeight += q.weights[q.terms[index]]
			}
		}
		if pathScore == 0 && matchedWeight == 0 {
			return searchFileCandidate{}, false
		}
		matchedMu.Lock()
		for index, hit := range matched {
			matchedAnywhere[index] = matchedAnywhere[index] || hit
		}
		matchedMu.Unlock()
		return searchFileCandidate{
			path:          filePath,
			score:         2*pathScore + matchedWeight + searchPathPrior(q, filePath),
			matchedWeight: matchedWeight,
		}, true
	}
	workers := 1
	if options.Worktree {
		workers = minInt(8, maxInt(1, runtime.GOMAXPROCS(0)))
	}
	files := scoreSearchPaths(ctx, scanPaths, workers, scoreFile)
	if err := ctx.Err(); err != nil {
		return searchFileSelection{}, err
	}
	applySearchFileCoverage(files, q, matchedAnywhere)
	sort.Slice(files, func(i, j int) bool {
		if files[i].score != files[j].score {
			return files[i].score > files[j].score
		}
		return files[i].path < files[j].path
	})
	if len(files) > options.MaxIndexedFiles {
		files = files[:options.MaxIndexedFiles]
	}
	selected := make([]string, len(files))
	for i, file := range files {
		selected[i] = file.path
	}
	selection.files = selected
	selection.filesContentRead = contentReads
	selection.preselectionBackend = "go-content"
	selection.preselectionPasses = 1
	selection.preselectionFilesExamined = len(scanPaths)
	if usedGitIndexPreselection {
		selection.preselectionBackend = "git-index-grep+go-content"
		selection.preselectionPasses++
		selection.preselectionFilesExamined += len(source.paths)
	}
	return selection, nil
}

func searchTermsSafeForGitGrep(terms []string) bool {
	for _, term := range terms {
		for index := 0; index < len(term); index++ {
			if term[index] < 0x20 || term[index] > 0x7e {
				return false
			}
		}
	}
	return true
}

var (
	searchASCIILowerAlternativesOnce sync.Once
	searchASCIILowerAlternatives     [128][]rune
)

// searchGitGrepPatterns makes Git's byte-oriented fixed-string preselection a
// conservative superset of Go's strings.ToLower matching. Some non-ASCII
// uppercase runes lower to ASCII (for example Turkish dotted İ -> i), while
// Git's -i behavior for those runes varies by platform and locale. Generate
// exact UTF-8 pattern alternatives from this Go runtime's Unicode tables. If
// the cartesian expansion would be excessive, return unsafe so the caller
// falls back to exhaustive content scoring without changing recall.
func searchGitGrepPatterns(terms []string) ([]string, bool) {
	if !searchTermsSafeForGitGrep(terms) {
		return nil, false
	}
	searchASCIILowerAlternativesOnce.Do(func() {
		for value := rune(128); value <= unicode.MaxRune; value++ {
			lower := unicode.ToLower(value)
			if lower >= 0 && lower < 128 {
				searchASCIILowerAlternatives[lower] = append(searchASCIILowerAlternatives[lower], value)
			}
		}
	})
	patterns := make([]string, 0, len(terms))
	seen := make(map[string]bool, len(terms))
	for _, term := range terms {
		variants := []string{""}
		for index := 0; index < len(term); index++ {
			alternatives := searchASCIILowerAlternatives[term[index]]
			if len(alternatives) == 0 {
				for variantIndex := range variants {
					variants[variantIndex] += term[index : index+1]
				}
				continue
			}
			if len(variants)*(len(alternatives)+1) > maxSearchGitGrepPatterns-len(patterns) {
				return nil, false
			}
			expanded := make([]string, 0, len(variants)*(len(alternatives)+1))
			for _, prefix := range variants {
				expanded = append(expanded, prefix+term[index:index+1])
				for _, alternative := range alternatives {
					expanded = append(expanded, prefix+string(alternative))
				}
			}
			variants = expanded
		}
		for _, variant := range variants {
			if seen[variant] {
				continue
			}
			seen[variant] = true
			patterns = append(patterns, variant)
			if len(patterns) > maxSearchGitGrepPatterns {
				return nil, false
			}
		}
	}
	return patterns, true
}

func committedSearchFiles(paths, matches []string, q searchQuery) []string {
	allowed := make(map[string]bool, len(paths))
	for _, filePath := range paths {
		allowed[filePath] = true
	}
	matched := make(map[string]bool, len(matches))
	for _, match := range matches {
		if allowed[match] {
			matched[match] = true
		}
	}
	files := make([]searchFileCandidate, 0, len(matched)+16)
	for _, filePath := range paths {
		pathScore := pathSearchScore(q, filePath)
		if !matched[filePath] && pathScore == 0 {
			continue
		}
		files = append(files, searchFileCandidate{
			path:  filePath,
			score: 2*pathScore + searchPathPrior(q, filePath),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].score != files[j].score {
			return files[i].score > files[j].score
		}
		return canonicalSearchPathLess(files[i].path, files[j].path)
	})
	selected := make([]string, len(files))
	for index, file := range files {
		selected[index] = file.path
	}
	return selected
}

func scoreSearchPaths(
	ctx context.Context,
	paths []string,
	workers int,
	score func(string) (searchFileCandidate, bool),
) []searchFileCandidate {
	initialCapacity := minInt(len(paths), 1024)
	if workers <= 1 {
		files := make([]searchFileCandidate, 0, initialCapacity)
		for _, filePath := range paths {
			if ctx.Err() != nil {
				break
			}
			if candidate, ok := score(filePath); ok {
				files = append(files, candidate)
			}
		}
		return files
	}

	jobs := make(chan string)
	results := make(chan searchFileCandidate)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for filePath := range jobs {
				if ctx.Err() != nil {
					return
				}
				if candidate, ok := score(filePath); ok {
					select {
					case results <- candidate:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, filePath := range paths {
			select {
			case jobs <- filePath:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wait.Wait()
		close(results)
	}()
	files := make([]searchFileCandidate, 0, initialCapacity)
	for candidate := range results {
		files = append(files, candidate)
	}
	return files
}

func shouldUseGitGrepPreselection(worktree bool, fileCount int) bool {
	return worktree && fileCount >= minGitGrepPreselectionFiles
}

func searchPreselectionPatterns(q searchQuery) []string {
	const (
		maxPatterns            = 12
		maxPrimaryPatterns     = 8
		maxInitialCodePatterns = 4
		maxOriginalPatterns    = 4
		maxMorphPatterns       = 2
		maxFragmentTerms       = 2
	)
	patterns := make([]string, 0, maxPatterns)
	seen := make(map[string]bool, maxPatterns)
	appendTier := func(limit int, include func(float64) bool) {
		if limit <= 0 {
			return
		}
		added := 0
		for _, term := range q.terms {
			if seen[term] || !include(q.weights[term]) {
				continue
			}
			patterns = append(patterns, term)
			seen[term] = true
			added++
			if added == limit || len(patterns) == maxPatterns {
				return
			}
		}
	}
	appendTier(maxInitialCodePatterns, func(weight float64) bool {
		return weight >= 1.25
	})
	appendTier(maxOriginalPatterns, func(weight float64) bool {
		return weight == 1
	})
	appendTier(maxPrimaryPatterns-len(patterns), func(weight float64) bool {
		return weight >= 1.25 || weight == 1
	})
	appendTier(maxMorphPatterns, func(weight float64) bool {
		return weight < 1
	})
	appendTier(maxFragmentTerms, func(weight float64) bool {
		return weight > 1 && weight < 1.25
	})
	appendTier(maxPatterns-len(patterns), func(weight float64) bool {
		return weight >= 1.25 || weight == 1
	})
	return patterns
}

func searchGitGrepPreselectionPatterns(q searchQuery) []string {
	patterns := searchPreselectionPatterns(q)
	// Each additional fixed-string pattern makes Git scan and emit more of a
	// large index. Preserve one derived morphological/fragment fallback rather
	// than letting direct terms consume the entire bounded pattern budget.
	if len(patterns) <= 6 {
		return patterns
	}
	direct := make([]string, 0, 6)
	derived := make([]string, 0, 1)
	for _, pattern := range patterns {
		if q.weights[pattern] >= 1 {
			direct = append(direct, pattern)
		} else {
			derived = append(derived, pattern)
		}
	}
	limit := 6
	if len(derived) > 0 {
		limit--
	}
	if len(direct) > limit {
		direct = direct[:limit]
	}
	if len(derived) > 0 {
		direct = append(direct, derived[0])
	}
	return direct
}

func candidatesForFile(q searchQuery, filePath, language string, lines []string, symbols []SymbolRecord, options SearchOptions) []searchCandidate {
	var out []searchCandidate
	covered := make([]bool, len(lines)+1)
	for _, symbol := range symbols {
		start, end := clampRegion(symbol.StartLine, symbol.EndLine, len(lines))
		if start == 0 {
			continue
		}
		searchStart := precedingDocumentationStart(lines, start, options.ContextLines)
		for line := searchStart; line <= end; line++ {
			covered[line] = true
		}
		if end-searchStart+1 <= options.MaxRegionLines {
			if candidate, ok := makeSearchCandidate(q, filePath, language, lines, searchStart, end, symbol, options.MaxSnippetLines); ok {
				out = append(out, candidate)
			}
			continue
		}
		regions := matchingLineRegions(q, lines, searchStart, end, options.ContextLines, options.MaxRegionLines)
		for _, region := range regions {
			attributed := attributeSearchRegion(q, lines, region[0], region[1], symbol, symbols)
			if candidate, ok := makeSearchCandidate(q, filePath, language, lines, region[0], region[1], attributed, options.MaxSnippetLines); ok {
				out = append(out, candidate)
			}
		}
		if len(regions) == 0 {
			focusedEnd := minInt(end, searchStart+options.MaxRegionLines-1)
			if candidate, ok := makeSearchCandidate(q, filePath, language, lines, searchStart, focusedEnd, symbol, options.MaxSnippetLines); ok {
				out = append(out, candidate)
			}
		}
	}

	var uncoveredHits []int
	for index, line := range lines {
		lineNumber := index + 1
		if !covered[lineNumber] && textMatchesSearchQuery(q, line) {
			uncoveredHits = append(uncoveredHits, lineNumber)
		}
	}
	for _, region := range uncoveredRegionsAroundHits(uncoveredHits, covered, len(lines), options.ContextLines, options.MaxRegionLines) {
		if candidate, ok := makeSearchCandidate(q, filePath, language, lines, region[0], region[1], SymbolRecord{}, options.MaxSnippetLines); ok {
			out = append(out, candidate)
		}
	}

	if len(out) == 0 && textMatchesSearchQuery(q, filepath.ToSlash(filePath)) {
		end := minInt(len(lines), maxInt(1, options.ContextLines*2+1))
		// A path can match only through weak derived fragments (compound
		// identifier parts or morphological variants) that pathSearchScore
		// does not treat as evidence. Such files produce no valid candidate
		// and must be skipped, not emitted as zero-value results.
		if candidate, ok := makeSearchCandidate(q, filePath, language, lines, 1, end, SymbolRecord{}, options.MaxSnippetLines); ok {
			candidate.baseScore += pathSearchScore(q, filePath)
			candidate.result.Signals = appendUnique(candidate.result.Signals, "path")
			out = append(out, candidate)
		}
	}
	return out
}

// attributeSearchRegion decides which symbol a matching region INSIDE a larger symbol should
// be credited to.
//
// A symbol too large to return whole is split into the regions that matched the query, and
// every one of those regions inherited the enclosing symbol's identity — including its
// name-match score. For a container that is wrong twice over: a 3,000-line class scores its
// name bonus at every matching line in the file, so an arbitrary region of `class Calculation`
// outranks the small method that actually implements the reported behaviour; and the result
// then reports `class Calculation` as the thing the agent is looking at.
//
// The correction is narrow, and it is a statement about evidence rather than a tuning knob: a
// region that does not even contain the declaration of the symbol it is named after did not
// match that name, so it is credited to the smallest CALLABLE inside the container that
// contains it. Regions that do include the declaration, and containers with no callable at
// that line (field blocks, constant tables), are left exactly as they were.
func attributeSearchRegion(
	q searchQuery, lines []string, start, end int, symbol SymbolRecord, symbols []SymbolRecord,
) SymbolRecord {
	if symbol.ID == "" || searchEnclosableSymbolKind(symbol.Kind) || start <= symbol.StartLine {
		return symbol
	}
	inner, ok := smallestSearchSymbolContainingLineWhere(
		symbols, searchFocusLine(q, lines, start, end),
		func(candidate SymbolRecord) bool {
			return searchEnclosableSymbolKind(candidate.Kind) &&
				candidate.StartLine >= symbol.StartLine && candidate.EndLine <= symbol.EndLine
		},
	)
	if !ok {
		return symbol
	}
	return inner
}

// sparseCandidatesForFile complements syntax-aware regions with fixed-width
// lexical windows. Syntax regions are precise but can hide the relevant part
// of a large symbol or prose-heavy file; overlapping windows preserve those
// locations for deeper rankings without changing the interactive search path.
func sparseCandidatesForFile(
	sparseQuery searchQuery,
	filePath, language string,
	lines []string,
	options SearchOptions,
) ([]searchCandidate, int, int) {
	if len(lines) == 0 {
		return nil, 0, 0
	}
	chunkLines := maxInt(1, options.MaxRegionLines)
	stride := minInt(sparseSearchChunkStrideLines, chunkLines)
	documentCount := 0
	documentLength := 0
	var out []searchCandidate
	for start := 1; start <= len(lines); start += stride {
		end := minInt(len(lines), start+chunkLines-1)
		text := filepath.ToSlash(filePath) + "\n" + strings.Join(lines[start-1:end], "\n")
		counts, length := sparseSearchTermCounts(text, sparseQuery.termSet)
		documentCount++
		documentLength += maxInt(1, length)
		if len(counts) > 0 {
			focus := sparseSearchFocusLine(sparseQuery, lines, start, end)
			snippetStart, snippetEnd := focusedSnippetRegion(start, end, focus, options.MaxSnippetLines)
			out = append(out, searchCandidate{
				result: SearchResult{
					FilePath:         filePath,
					StartLine:        start,
					EndLine:          end,
					FocusLine:        focus,
					SnippetStartLine: snippetStart,
					SnippetEndLine:   snippetEnd,
					Language:         language,
					Signals:          []string{"sparse-region"},
					Snippet:          "",
				},
				termCounts: counts,
				docLength:  length,
			})
		}
		if end == len(lines) {
			break
		}
	}
	return out, documentCount, documentLength
}

func sparseSearchFocusLine(q searchQuery, lines []string, start, end int) int {
	bestLine := start
	bestMatches := -1
	for line := start; line <= end; line++ {
		counts, _ := sparseSearchTermCounts(lines[line-1], q.termSet)
		matches := 0
		for _, count := range counts {
			matches += count
		}
		if matches > bestMatches {
			bestLine = line
			bestMatches = matches
		}
	}
	return bestLine
}

// attachSparseCandidateSymbols restores exact semantic identity for sparse
// windows whose best matching line is inside a parsed symbol. Sparse regions
// are built during file preselection, before the provider snapshot is
// available, so they otherwise cannot participate in semantic mirror dedup.
func attachSparseCandidateSymbols(candidates []searchCandidate, symbolsByFile map[string][]SymbolRecord) {
	for index := range candidates {
		candidate := &candidates[index]
		if candidate.result.SymbolID != "" {
			continue
		}
		symbol, ok := smallestSearchSymbolContainingLine(
			symbolsByFile[candidate.result.FilePath], candidate.result.FocusLine,
		)
		if !ok {
			continue
		}
		if candidate.result.Language == "" {
			candidate.result.Language = symbol.Language
		}
		candidate.result.Kind = symbol.Kind
		candidate.result.SymbolID = symbol.ID
		candidate.result.SymbolName = symbol.Name
		candidate.result.QualifiedName = symbol.QualifiedName
		candidate.result.Signature = symbol.Signature
	}
}

func smallestSearchSymbolContainingLine(symbols []SymbolRecord, line int) (SymbolRecord, bool) {
	return smallestSearchSymbolContainingLineWhere(symbols, line, nil)
}

// smallestSearchSymbolContainingLineWhere is smallestSearchSymbolContainingLine restricted to
// the symbols a caller accepts (a nil filter accepts every symbol). It lets snippet allocation
// ask for the smallest enclosing CALLABLE while ranking keeps asking for the smallest symbol
// of any kind.
func smallestSearchSymbolContainingLineWhere(
	symbols []SymbolRecord, line int, keep func(SymbolRecord) bool,
) (SymbolRecord, bool) {
	bestSpan := int(^uint(0) >> 1)
	var best SymbolRecord
	found := false
	for _, symbol := range symbols {
		if symbol.ID == "" || line < symbol.StartLine || line > symbol.EndLine {
			continue
		}
		if keep != nil && !keep(symbol) {
			continue
		}
		span := symbol.EndLine - symbol.StartLine
		if !found || span < bestSpan || (span == bestSpan && symbol.StartLine > best.StartLine) {
			best = symbol
			bestSpan = span
			found = true
		}
	}
	return best, found
}

func hydrateSparseCandidates(candidates []searchCandidate, read contentReader) int {
	cache := make(map[string][]string)
	missing := make(map[string]bool)
	reads := 0
	for index := range candidates {
		candidate := &candidates[index]
		isSparse := false
		for _, signal := range candidate.result.Signals {
			if signal == "sparse-region" {
				isSparse = true
				break
			}
		}
		if candidate.result.Snippet != "" || !isSparse {
			continue
		}
		filePath := candidate.result.FilePath
		lines, cached := cache[filePath]
		if !cached && !missing[filePath] {
			content, ok := read(filePath)
			reads++
			if !ok || strings.IndexByte(content, 0) >= 0 {
				missing[filePath] = true
				continue
			}
			lines = strings.Split(strings.TrimSuffix(content, "\n"), "\n")
			cache[filePath] = lines
			if candidate.result.Language == "" {
				if spec, detected := languageForContent(filePath, content); detected {
					candidate.result.Language = spec.language
				}
			}
		}
		if len(lines) == 0 {
			continue
		}
		start, end := clampRegion(candidate.result.SnippetStartLine, candidate.result.SnippetEndLine, len(lines))
		if start > 0 {
			candidate.result.Snippet = strings.Join(lines[start-1:end], "\n")
		}
	}
	return reads
}

func scaleSearchCorpusValue(value, totalFiles, observedFiles int) int {
	if value <= 0 || totalFiles <= observedFiles || observedFiles <= 0 {
		return value
	}
	maxValue := int(^uint(0) >> 1)
	quotient, remainder := value/observedFiles, value%observedFiles
	if quotient > maxValue/totalFiles {
		return maxValue
	}
	scaled := quotient * totalFiles
	if remainder == 0 {
		return scaled
	}
	if remainder > (maxValue-scaled)/totalFiles {
		return maxValue
	}
	product := remainder * totalFiles
	extra := product / observedFiles
	if product%observedFiles != 0 {
		extra++
	}
	return scaled + extra
}

func scoreSparseCandidates(
	candidates []searchCandidate,
	q searchQuery,
	documentFrequency map[string]int,
	documentCount, documentLength int,
) {
	if len(candidates) == 0 || documentCount <= 0 {
		return
	}
	averageLength := float64(maxInt(1, documentLength)) / float64(documentCount)
	for i := range candidates {
		candidate := &candidates[i]
		candidate.score += searchPathPrior(q, candidate.result.FilePath)
		// cmm parity: score a sparse window on its symbol identity (name / qualified-name)
		// and path tokens, not only body-BM25 + path-prior. This is what lets a fix file whose
		// domain nouns live in its path/identifiers surface as a genuine ranked hit instead of
		// relevance-free RRF filler (measured whiffs: fmt core.h on_zero, druid *PostAggregator.java).
		// attachSparseCandidateSymbols now runs before this, so the symbol fields are populated.
		candidate.score += pathSearchScore(q, candidate.result.FilePath)
		if candidate.result.SymbolID != "" {
			nameScore, nameSignals := symbolSearchScore(q, SymbolRecord{
				Name:          candidate.result.SymbolName,
				QualifiedName: candidate.result.QualifiedName,
				Signature:     candidate.result.Signature,
				Kind:          candidate.result.Kind,
			})
			candidate.score += nameScore
			candidate.result.Signals = appendUnique(candidate.result.Signals, nameSignals...)
		}
		for _, term := range q.terms {
			frequency := candidate.termCounts[term]
			if frequency == 0 {
				continue
			}
			df := documentFrequency[term]
			inverse := math.Log(1 + (float64(documentCount-df)+0.5)/(float64(df)+0.5))
			denominator := float64(frequency) + 1.2*(0.25+0.75*float64(maxInt(1, candidate.docLength))/averageLength)
			candidate.score += q.weights[term] * inverse * (float64(frequency) * 2.2 / denominator)
		}
	}
}

func uncoveredRegionsAroundHits(hits []int, covered []bool, lineCount, context, maxLines int) [][2]int {
	baseRegions := regionsAroundHits(hits, 1, lineCount, context, maxLines)
	hitSet := make(map[int]bool, len(hits))
	for _, hit := range hits {
		hitSet[hit] = true
	}
	var out [][2]int
	for _, region := range baseRegions {
		runStart := 0
		runHasHit := false
		flush := func(end int) {
			if runStart > 0 && runHasHit {
				out = append(out, [2]int{runStart, end})
			}
			runStart = 0
			runHasHit = false
		}
		for line := region[0]; line <= region[1]; line++ {
			if line < len(covered) && covered[line] {
				flush(line - 1)
				continue
			}
			if runStart == 0 {
				runStart = line
			}
			runHasHit = runHasHit || hitSet[line]
		}
		flush(region[1])
	}
	return out
}

func precedingDocumentationStart(lines []string, symbolStart, limit int) int {
	start := symbolStart
	if limit <= 0 {
		return start
	}
	sawComment := false
	for line := symbolStart - 1; line >= 1 && symbolStart-line <= limit; line-- {
		trimmed := strings.TrimSpace(lines[line-1])
		isComment := strings.HasPrefix(trimmed, "//") ||
			strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, "/*") ||
			strings.HasPrefix(trimmed, "*") ||
			strings.HasPrefix(trimmed, "--")
		if isComment {
			sawComment = true
			start = line
			continue
		}
		if trimmed == "" && sawComment {
			start = line
			continue
		}
		break
	}
	return start
}

func makeSearchCandidate(q searchQuery, filePath, language string, lines []string, start, end int, symbol SymbolRecord, maxSnippetLines int) (searchCandidate, bool) {
	start, end = clampRegion(start, end, len(lines))
	if start == 0 {
		return searchCandidate{}, false
	}
	regionText := strings.Join(lines[start-1:end], "\n")
	counts, length := searchTermCounts(regionText, q.termSet)
	focus := searchFocusLine(q, lines, start, end)
	if symbol.ID != "" {
		focus = maxInt(focus, symbol.StartLine)
	}
	// symbol.StartLine can fall outside [start, end] when the caller passed a truncated
	// window (e.g. a same-container-neighbor candidate clipped to a tiny --max-snippet-lines).
	// Clamp so FocusLine always satisfies the SearchResponse.Validate invariant
	// (StartLine <= FocusLine <= EndLine); otherwise the whole response is rejected with
	// "invalid search result at rank N" (reproducible with --max-snippet-lines 1).
	focus = minInt(maxInt(focus, start), end)
	snippetStart, snippetEnd := focusedSnippetRegion(start, end, focus, maxSnippetLines)
	snippet := strings.Join(lines[snippetStart-1:snippetEnd], "\n")
	pathScore := pathSearchScore(q, filePath)
	base := pathScore
	signals := []string{}
	if pathScore > 0 {
		signals = append(signals, "path")
	}
	if len(counts) > 0 {
		signals = append(signals, "body")
	}
	if symbol.ID != "" {
		nameScore, nameSignals := symbolSearchScore(q, symbol)
		base += nameScore
		signals = append(signals, nameSignals...)
	}
	if len(counts) == 0 && pathScore == 0 && len(signals) == 0 {
		return searchCandidate{}, false
	}
	base += searchPathPrior(q, filePath)
	return searchCandidate{
		result: SearchResult{
			FilePath:         filePath,
			StartLine:        start,
			EndLine:          end,
			FocusLine:        focus,
			SnippetStartLine: snippetStart,
			SnippetEndLine:   snippetEnd,
			Language:         language,
			Kind:             symbol.Kind,
			SymbolID:         symbol.ID,
			SymbolName:       symbol.Name,
			QualifiedName:    symbol.QualifiedName,
			Signature:        symbol.Signature,
			Signals:          appendUnique(nil, signals...),
			Snippet:          snippet,
		},
		termCounts: counts,
		docLength:  length,
		baseScore:  base,
	}, true
}

func scoreSearchCandidates(candidates []searchCandidate, q searchQuery, fileDF map[string]int, fileCount int, inboundDegree map[string]int) {
	if len(candidates) == 0 {
		return
	}
	averageLength := 0.0
	for _, candidate := range candidates {
		averageLength += float64(maxInt(1, candidate.docLength))
	}
	averageLength /= float64(len(candidates))
	queryWeight := 0.0
	idf := make(map[string]float64, len(q.terms))
	for _, term := range q.terms {
		df := fileDF[term]
		weight := math.Log(1+(float64(fileCount-df)+0.5)/(float64(df)+0.5)) * q.weights[term]
		idf[term] = weight
		if df == 0 {
			// No indexed file contains this term, so no candidate can ever match it —
			// and, being maximally rare, it would carry the LARGEST idf of the query.
			// Charging it to the query weight would put full coverage out of reach and
			// tax every document that matched the rest of the query, purely because the
			// caller wrote one word the repository has never heard of. An unmatched term
			// contributes zero (BM25), never a penalty.
			continue
		}
		queryWeight += weight
	}
	for i := range candidates {
		candidate := &candidates[i]
		bm25 := 0.0
		coveredWeight := 0.0
		codeTokenBonus := 0.0
		for _, term := range q.terms {
			frequency := candidate.termCounts[term]
			if frequency == 0 {
				continue
			}
			weight := idf[term]
			coveredWeight += weight
			if q.weights[term] >= 2 {
				codeTokenBonus += 6 * q.weights[term]
			}
			denominator := float64(frequency) + 1.2*(0.25+0.75*float64(maxInt(1, candidate.docLength))/averageLength)
			bm25 += weight * (float64(frequency) * 2.2 / denominator)
		}
		coverage := 0.0
		if queryWeight > 0 {
			coverage = coveredWeight / queryWeight
		}
		// Centrality: reward well-connected implementation symbols (many inbound callers/impls) so
		// they outrank leaf/trait-dispatch/trivial methods that merely share a common query term.
		// log-scaled + capped so it tilts ties toward the implementation without overriding a strong
		// direct match. Only meaningful when the candidate matched the query at all (bm25>0 or a
		// name/base signal), so it can't drag in unrelated high-degree symbols.
		centrality := 0.0
		if len(inboundDegree) > 0 && candidate.result.SymbolID != "" && (bm25 > 0 || candidate.baseScore > 0) {
			if deg := inboundDegree[candidate.result.SymbolID]; deg > 0 {
				centrality = minFloat64(6, 1.6*math.Log(1+float64(deg)))
			}
		}
		candidate.score = candidate.baseScore + bm25 + 7*coverage + minFloat64(24, codeTokenBonus) + centrality
		if codeTokenBonus > 0 {
			candidate.result.Signals = appendUnique(candidate.result.Signals, "exact-code-token")
		}
		if coverage == 1 {
			candidate.score += 2
			candidate.result.Signals = appendUnique(candidate.result.Signals, "all-query-terms")
		}
	}
}

func minFloat64(left, right float64) float64 {
	if left < right {
		return left
	}
	return right
}

func maxFloat64(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}

func derivedSearchScore(parent, proposed float64) float64 {
	return minFloat64(parent-0.01, proposed)
}

func expandGraphCandidates(seeds []searchCandidate, q searchQuery, relations []RelationRecord, symbolsByID map[string]SymbolRecord, read contentReader, languages map[string]string, options SearchOptions) []searchCandidate {
	if len(seeds) == 0 || len(relations) == 0 {
		return nil
	}
	// SearchRepository normalizes options before calling here, but direct
	// callers (e.g. tests) may pass hand-built options. Mirror that
	// normalization so a non-positive limit cannot shrink end below start
	// and panic on the snippet slice below.
	if options.MaxRegionLines <= 0 {
		options.MaxRegionLines = defaultSearchMaxRegionLines
	}
	if options.MaxSnippetLines <= 0 {
		options.MaxSnippetLines = defaultSearchMaxSnippetLines
	}
	seedScores := map[string]float64{}
	for _, candidate := range seeds {
		if len(seedScores) >= 10 {
			break
		}
		if candidate.result.SymbolID != "" && candidate.score > seedScores[candidate.result.SymbolID] {
			seedScores[candidate.result.SymbolID] = candidate.score
		}
	}
	if len(seedScores) == 0 {
		return nil
	}
	best := map[string]searchCandidate{}
	contentCache := map[string][]string{}
	for _, relation := range relations {
		if relation.Confidence < 0.7 || !searchExpansionRelation(relation.Type) {
			continue
		}
		pairs := [][3]string{{relation.FromID, relation.ToID, "outgoing"}, {relation.ToID, relation.FromID, "incoming"}}
		for _, pair := range pairs {
			seedScore, ok := seedScores[pair[0]]
			if !ok {
				continue
			}
			symbol, ok := symbolsByID[pair[1]]
			if !ok || symbol.ID == pair[0] {
				continue
			}
			lines, ok := contentCache[symbol.FilePath]
			if !ok {
				content, readable := read(symbol.FilePath)
				if !readable {
					continue
				}
				lines = strings.Split(content, "\n")
				contentCache[symbol.FilePath] = lines
			}
			start, end := clampRegion(symbol.StartLine, symbol.EndLine, len(lines))
			if start == 0 {
				continue
			}
			if end-start+1 > options.MaxRegionLines {
				end = minInt(len(lines), start+options.MaxRegionLines-1)
			}
			snippetStart, snippetEnd := focusedSnippetRegion(start, end, start, options.MaxSnippetLines)
			candidate := searchCandidate{
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
					Snippet:          strings.Join(lines[snippetStart-1:snippetEnd], "\n"),
				},
			}
			// A high-confidence, type-resolved CALLS/CONSTRUCTS edge from a strongly-matched seed
			// is very likely the collaborator that implements the query's behaviour — the vocab-gap
			// bridge: the query matches the caller by name while the fix lives in the differently-
			// named callee (e.g. rule "binary_operator_spaces" -> BinaryOperatorSpacesFixer ->CALLS->
			// TokensAnalyzer.getLastTokenIndexOfArrowFunction, the gold). The default 0.28 seed
			// fraction buries such a callee ~7 pts down, below prose/tests; score it just below the
			// seed so it enters the top-K. derivedSearchScore's parent-0.01 cap keeps it under the
			// caller, and best[]/dedup/diversity selection bound the added candidates.
			// Vocab-gap bridge: boost a high-confidence CALLS/CONSTRUCTS callee to just below its
			// caller ONLY when the callee is itself textually plausible for the query (its name shares
			// a query term). This promotes a buried-but-relevant collaborator — e.g. the query
			// "...arrow function..." reaches TokensAnalyzer.getLastTokenIndexOfArrowFunction (shares
			// "arrow"/"function") from the caller BinaryOperatorSpacesFixer — while NOT surfacing the
			// arbitrary callees of a strong direct hit (e.g. sympy's Point.__new__ callees don't match
			// "imaginary/coordinates", so they stay unboosted and add no exploration noise).
			seedFraction := 0.28
			if relation.Confidence >= 0.8 &&
				(relation.Type == "CALLS" || relation.Type == "ASYNC_CALLS" || relation.Type == "CONSTRUCTS") &&
				searchSymbolNameMatchesQueryTerm(q, symbol) {
				seedFraction = 0.85
			}
			candidate.score = derivedSearchScore(
				seedScore,
				seedFraction*seedScore+relation.Confidence+searchPathPrior(q, symbol.FilePath),
			)
			candidate.baseScore = candidate.score
			candidate.result.Signals = appendUnique(candidate.result.Signals, "graph:"+strings.ToLower(relation.Type), "graph:"+pair[2])
			if previous, exists := best[symbol.ID]; !exists || candidate.score > previous.score {
				best[symbol.ID] = candidate
			}
		}
	}
	out := make([]searchCandidate, 0, len(best))
	for _, candidate := range best {
		out = append(out, candidate)
	}
	sortSearchCandidates(out)
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

// searchSymbolNameMatchesQueryTerm reports whether a symbol's name/qualified-name shares a
// (non-trivial) query term — used to gate the graph-expansion boost to textually-plausible callees.
func searchSymbolNameMatchesQueryTerm(q searchQuery, symbol SymbolRecord) bool {
	name := strings.ToLower(symbol.Name)
	qualified := strings.ToLower(symbol.QualifiedName)
	for _, term := range q.terms {
		if len(term) < 3 {
			continue
		}
		if strings.Contains(name, term) || strings.Contains(qualified, term) {
			return true
		}
	}
	return false
}

func searchExpansionRelation(relation string) bool {
	switch relation {
	case "CALLS", "CONSTRUCTS", "ASYNC_CALLS", "IMPORTS", "EXTENDS", "INHERITS", "IMPLEMENTS", "OVERRIDES", "USES_TYPE", "TESTS", "CONFIGURES",
		// SIMILAR_TO is a near-duplicate body (MinHash >= 0.82). Copy-pasted code is the
		// classic reason a fix has to be applied in more than one place, so the twin of a
		// strong hit is exactly the second site an agent would otherwise discover only
		// after its patch failed review. The relation is emitted at profile fast/full only.
		"SIMILAR_TO":
		return true
	default:
		return false
	}
}

type usageFileSelector func(context.Context, []string) ([]string, bool)

func committedUsageFileSelector(
	repo string,
	treeish string,
	useHead bool,
	languages map[string]string,
	deep bool,
	filesExamined int,
	telemetry *searchScanTelemetry,
) usageFileSelector {
	if !useHead || deep {
		return nil
	}
	return func(ctx context.Context, identifiers []string) ([]string, bool) {
		if !searchTermsSafeForGitGrep(identifiers) {
			return nil, false
		}
		matches, err := gitutil.GrepTreePaths(ctx, repo, treeish, identifiers)
		if err != nil {
			return nil, false
		}
		if telemetry != nil {
			telemetry.backend = "git-tree-grep"
			telemetry.passes = 1
			telemetry.filesExamined = filesExamined
		}
		seen := make(map[string]bool, len(matches))
		files := make([]string, 0, len(matches))
		for _, match := range matches {
			if _, eligible := languages[match]; !eligible || seen[match] {
				continue
			}
			seen[match] = true
			files = append(files, match)
		}
		// The expansion routine performs the final stable sort. Sorting there
		// applies the same query-aware source/test/docs/generated prior to both
		// Git postings and the exhaustive correctness fallback.
		return files, true
	}
}

// expandIdentifierUsageCandidates complements typed graph edges with precise
// lexical use sites for strong compound-identifier seeds. Constants, exported
// values, callbacks, and other non-call references often have no relation edge,
// but their consumers are exactly the context a coding agent needs after finding
// a definition.
func expandIdentifierUsageCandidates(
	ctx context.Context,
	seeds []searchCandidate,
	q searchQuery,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	read contentReader,
	languages map[string]string,
	options SearchOptions,
	selectors ...usageFileSelector,
) []searchCandidate {
	type usageSeed struct {
		symbol SymbolRecord
		score  float64
	}
	const maxSeeds = 20
	const maxUsagesPerSeed = 3
	const maxRawUsagesPerSeed = 64
	var selectedSeeds []usageSeed
	seenSymbols := map[string]bool{}
	if len(seeds) == 0 || seeds[0].score <= 0 {
		return nil
	}
	bestScore := seeds[0].score
	for _, candidate := range seeds {
		if len(selectedSeeds) == maxSeeds {
			break
		}
		if candidate.score < 0.35*bestScore {
			continue
		}
		symbol, ok := symbolsByID[candidate.result.SymbolID]
		if !ok || seenSymbols[symbol.ID] || !expandableUsageIdentifier(symbol.Name) {
			continue
		}
		seenSymbols[symbol.ID] = true
		selectedSeeds = append(selectedSeeds, usageSeed{symbol: symbol, score: candidate.score})
	}
	if len(selectedSeeds) == 0 {
		return nil
	}

	filePaths := make([]string, 0, len(languages))
	for filePath := range languages {
		filePaths = append(filePaths, filePath)
	}
	if len(selectors) > 0 && selectors[0] != nil {
		names := make([]string, len(selectedSeeds))
		for index, seed := range selectedSeeds {
			names[index] = seed.symbol.Name
		}
		if selected, ok := selectors[0](ctx, names); ok {
			filePaths = selected
		}
	}
	sort.Slice(filePaths, func(i, j int) bool {
		leftScore := searchPathPrior(q, filePaths[i]) + pathSearchScore(q, filePaths[i])
		rightScore := searchPathPrior(q, filePaths[j]) + pathSearchScore(q, filePaths[j])
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		return canonicalSearchPathLess(filePaths[i], filePaths[j])
	})
	type indexedUsage struct {
		candidate searchCandidate
		seedIndex int
	}
	rawUsageCounts := make([]int, len(selectedSeeds))
	seenLocations := map[string]bool{}
	var discovered []indexedUsage
	usageContext := minInt(options.ContextLines, 2)
	for _, filePath := range filePaths {
		if ctx.Err() != nil {
			return nil
		}
		allSaturated := true
		for seedIndex := range selectedSeeds {
			if rawUsageCounts[seedIndex] < maxRawUsagesPerSeed {
				allSaturated = false
				break
			}
		}
		if allSaturated {
			break
		}
		content, ok := read(filePath)
		if !ok || strings.IndexByte(content, 0) >= 0 {
			continue
		}
		lines := strings.Split(content, "\n")
		for seedIndex, seed := range selectedSeeds {
			if rawUsageCounts[seedIndex] == maxRawUsagesPerSeed {
				continue
			}
			if !strings.Contains(content, seed.symbol.Name) {
				continue
			}
			definitionStart := seed.symbol.StartLine
			if filePath == seed.symbol.FilePath {
				definitionStart = precedingDocumentationStart(lines, seed.symbol.StartLine, options.ContextLines)
			}
			for lineIndex, line := range lines {
				lineNumber := lineIndex + 1
				if !containsIdentifierUsage(line, seed.symbol.Name) || identifierUsageIsImport(line) ||
					(filePath == seed.symbol.FilePath && lineNumber >= definitionStart && lineNumber <= seed.symbol.EndLine) {
					continue
				}
				locationKey := fmt.Sprintf("%s:%d:%s", filePath, lineNumber, seed.symbol.ID)
				if seenLocations[locationKey] {
					continue
				}
				start := maxInt(1, lineNumber-usageContext)
				end := minInt(len(lines), lineNumber+usageContext)
				container := enclosingSearchSymbol(symbolsByFile[filePath], lineNumber)
				candidate, ok := makeSearchCandidate(q, filePath, languages[filePath], lines, start, end, container, options.MaxSnippetLines)
				if !ok {
					continue
				}
				candidate.result.FocusLine = lineNumber
				candidate.result.Signals = appendUnique(candidate.result.Signals, "symbol-usage")
				candidate.score = derivedSearchScore(seed.score, 0.9*seed.score+searchPathPrior(q, filePath))
				candidate.baseScore = candidate.score
				seenLocations[locationKey] = true
				rawUsageCounts[seedIndex]++
				discovered = append(discovered, indexedUsage{candidate: candidate, seedIndex: seedIndex})
				if rawUsageCounts[seedIndex] == maxRawUsagesPerSeed {
					break
				}
			}
		}
	}
	sort.SliceStable(discovered, func(i, j int) bool {
		if discovered[i].candidate.score != discovered[j].candidate.score {
			return discovered[i].candidate.score > discovered[j].candidate.score
		}
		leftPath := discovered[i].candidate.result.FilePath
		rightPath := discovered[j].candidate.result.FilePath
		if leftPath != rightPath {
			return canonicalSearchPathLess(leftPath, rightPath)
		}
		return searchCandidateLess(discovered[i].candidate, discovered[j].candidate)
	})
	selectedPerSeed := make([]int, len(selectedSeeds))
	out := make([]searchCandidate, 0, minInt(32, len(discovered)))
	for _, usage := range discovered {
		if selectedPerSeed[usage.seedIndex] == maxUsagesPerSeed {
			continue
		}
		selectedPerSeed[usage.seedIndex]++
		out = append(out, usage.candidate)
		if len(out) == 32 {
			break
		}
	}
	return out
}

func identifierUsageIsImport(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "import{") ||
		strings.HasPrefix(trimmed, "export {") || strings.HasPrefix(trimmed, "export{")
}

func expandableUsageIdentifier(name string) bool {
	if len(name) < 6 {
		return false
	}
	hasLetter, hasLower, hasUpper, hasSeparator := false, false, false, false
	for _, character := range name {
		switch {
		case unicode.IsLower(character):
			hasLetter = true
			hasLower = true
		case unicode.IsUpper(character):
			hasLetter = true
			hasUpper = true
		case unicode.IsLetter(character):
			hasLetter = true
		case character == '_' || character == '$':
			hasSeparator = true
		case unicode.IsDigit(character):
		default:
			return false
		}
	}
	return (hasLower && hasUpper) || (hasLetter && hasSeparator)
}

func containsIdentifierUsage(line, name string) bool {
	for offset := 0; offset <= len(line)-len(name); {
		index := strings.Index(line[offset:], name)
		if index < 0 {
			return false
		}
		index += offset
		leftBoundary := index == 0 || !searchIdentifierByte(line[index-1])
		right := index + len(name)
		rightBoundary := right == len(line) || !searchIdentifierByte(line[right])
		if leftBoundary && rightBoundary {
			return true
		}
		offset = index + len(name)
	}
	return false
}

func searchIdentifierByte(value byte) bool {
	return value == '_' || value == '$' ||
		(value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') ||
		(value >= '0' && value <= '9')
}

func enclosingSearchSymbol(symbols []SymbolRecord, line int) SymbolRecord {
	var best SymbolRecord
	for _, symbol := range symbols {
		if line < symbol.StartLine || line > symbol.EndLine {
			continue
		}
		if best.ID == "" || symbol.EndLine-symbol.StartLine < best.EndLine-best.StartLine {
			best = symbol
		}
	}
	return best
}

// expandSameContainerNeighborCandidates follows strong callable results to the
// immediately preceding and following callable in the same lexical container.
// Lifecycle code is commonly split into small adjacent stages without an
// explicit call edge (for example tick -> dispatch -> run); the transition is
// often more useful to an agent than another weak match from an unrelated file.
func expandSameContainerNeighborCandidates(
	ctx context.Context,
	seeds []searchCandidate,
	q searchQuery,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	read contentReader,
	languages map[string]string,
	options SearchOptions,
) []searchCandidate {
	if len(seeds) == 0 {
		return nil
	}
	limit := minInt(len(seeds), maxInt(30, options.TopK*2))
	bestScore := seeds[0].score
	if bestScore <= 0 {
		return nil
	}
	seedSymbolIDs := map[string]bool{}
	for _, seed := range seeds[:limit] {
		if ctx.Err() != nil {
			return nil
		}
		seedSymbolIDs[seed.result.SymbolID] = true
	}
	seen := map[string]bool{}
	var out []searchCandidate
	for _, seed := range seeds[:limit] {
		if ctx.Err() != nil {
			return nil
		}
		if seed.score < 0.35*bestScore || !searchFlowSymbolKind(seed.result.Kind) {
			continue
		}
		symbol, ok := symbolsByID[seed.result.SymbolID]
		if !ok {
			continue
		}
		siblings := sameContainerFlowSymbols(symbolsByFile[symbol.FilePath], symbol.ContainerID)
		index := -1
		for i := range siblings {
			if siblings[i].ID == symbol.ID {
				index = i
				break
			}
		}
		if index < 0 {
			continue
		}
		content, ok := read(symbol.FilePath)
		if !ok || strings.IndexByte(content, 0) >= 0 {
			continue
		}
		lines := strings.Split(content, "\n")
		for _, neighborIndex := range []int{index - 1, index + 1} {
			if neighborIndex < 0 || neighborIndex >= len(siblings) {
				continue
			}
			neighbor := siblings[neighborIndex]
			if seedSymbolIDs[neighbor.ID] {
				continue
			}
			start, end := neighbor.StartLine, neighbor.EndLine
			if neighborIndex < index {
				end = minInt(symbol.StartLine-1, start+options.MaxSnippetLines-1)
			} else {
				start = maxInt(symbol.EndLine+1, neighbor.StartLine-options.ContextLines)
				end = minInt(neighbor.EndLine, start+options.MaxSnippetLines-1)
			}
			if start < 1 || start > end || start > len(lines) || end-start+1 > options.MaxSnippetLines {
				continue
			}
			gap := maxInt(0, maxInt(neighbor.StartLine-symbol.EndLine-1, symbol.StartLine-neighbor.EndLine-1))
			if gap > options.MaxSnippetLines {
				continue
			}
			key := fmt.Sprintf("%s:%d:%d", symbol.FilePath, start, end)
			if seen[key] {
				continue
			}
			candidate, ok := makeSearchCandidate(q, symbol.FilePath, languages[symbol.FilePath], lines, start, end, neighbor, options.MaxSnippetLines)
			if !ok {
				// Adjacency is itself the relevance signal; the neighboring stage
				// need not repeat narrative terms from the original query.
				candidate, ok = makeSearchCandidate(buildSearchQuery(neighbor.Name), symbol.FilePath, languages[symbol.FilePath], lines, start, end, neighbor, options.MaxSnippetLines)
				if !ok {
					continue
				}
				candidate.result.Signals = nil
			}
			// neighbor.StartLine is the meaningful focus point, but under a small
			// options.MaxSnippetLines the emitted region above can be truncated to a window
			// that no longer contains it. Clamp into the candidate's own region so FocusLine
			// never escapes [StartLine, EndLine] and trips SearchResponse.Validate.
			candidate.result.FocusLine = minInt(maxInt(neighbor.StartLine, candidate.result.StartLine), candidate.result.EndLine)
			candidate.result.Signals = appendUnique(candidate.result.Signals, "same-container-neighbor")
			candidate.score = derivedSearchScore(seed.score, 0.85*seed.score)
			candidate.baseScore = candidate.score
			seen[key] = true
			out = append(out, candidate)
		}
	}
	sortSearchCandidates(out)
	if len(out) > 24 {
		out = out[:24]
	}
	return out
}

func sameContainerFlowSymbols(symbols []SymbolRecord, containerID string) []SymbolRecord {
	var out []SymbolRecord
	for _, symbol := range symbols {
		if symbol.ContainerID == containerID && searchFlowSymbolKind(symbol.Kind) {
			out = append(out, symbol)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartLine != out[j].StartLine {
			return out[i].StartLine < out[j].StartLine
		}
		return out[i].EndLine < out[j].EndLine
	})
	return out
}

// expandSameFileBridgeCandidates exposes short omitted spans between two
// independently strong regions. These bridges frequently contain the glue
// call or state transition that explains how neighboring lifecycle stages are
// connected.
func expandSameFileBridgeCandidates(
	ctx context.Context,
	candidates []searchCandidate,
	q searchQuery,
	symbolsByFile map[string][]SymbolRecord,
	read contentReader,
	languages map[string]string,
	options SearchOptions,
) []searchCandidate {
	if len(candidates) < 2 {
		return nil
	}
	limit := minInt(len(candidates), maxInt(40, options.TopK*2))
	byFile := map[string][]searchCandidate{}
	bestScore := candidates[0].score
	if bestScore <= 0 {
		return nil
	}
	for _, candidate := range candidates[:limit] {
		if candidate.score < 0.3*bestScore || !searchFlowSymbolKind(candidate.result.Kind) || candidate.result.SymbolID == "" {
			continue
		}
		byFile[candidate.result.FilePath] = append(byFile[candidate.result.FilePath], candidate)
	}
	filePaths := make([]string, 0, len(byFile))
	for filePath := range byFile {
		filePaths = append(filePaths, filePath)
	}
	sort.Strings(filePaths)
	var out []searchCandidate
	seenBridges := map[string]bool{}
	for _, filePath := range filePaths {
		if ctx.Err() != nil {
			return nil
		}
		regions := byFile[filePath]
		sort.Slice(regions, func(i, j int) bool {
			return searchCandidateLess(regions[i], regions[j])
		})
		content, ok := read(filePath)
		if !ok || strings.IndexByte(content, 0) >= 0 {
			continue
		}
		lines := strings.Split(content, "\n")
		for leftIndex := 0; leftIndex+1 < len(regions); leftIndex++ {
			for rightIndex := leftIndex + 1; rightIndex < len(regions); rightIndex++ {
				left, right := regions[leftIndex], regions[rightIndex]
				gapStart, gapEnd := left.result.EndLine+1, right.result.StartLine-1
				if gapStart > gapEnd {
					continue
				}
				if gapEnd-gapStart+1 > options.MaxSnippetLines {
					break
				}
				// Reserve at least half the snippet for the downstream operation.
				// A long omitted span must not consume the entire result and end one
				// line before the consumer that makes the bridge actionable.
				start := maxInt(gapStart, right.result.StartLine-options.MaxSnippetLines/2)
				end := minInt(right.result.EndLine, start+options.MaxSnippetLines-1)
				leftSymbol, leftOK := searchSymbolByID(symbolsByFile[filePath], left.result.SymbolID)
				rightSymbol, rightOK := searchSymbolByID(symbolsByFile[filePath], right.result.SymbolID)
				if !leftOK || !rightOK || leftSymbol.ContainerID != rightSymbol.ContainerID {
					continue
				}
				bridgeKey := fmt.Sprintf("%s:%d:%d", filePath, start, end)
				if seenBridges[bridgeKey] {
					continue
				}
				focus := searchFocusLine(q, lines, start, end)
				container := enclosingSearchSymbol(symbolsByFile[filePath], focus)
				candidate, ok := makeSearchCandidate(q, filePath, languages[filePath], lines, start, end, container, options.MaxSnippetLines)
				if !ok {
					continue
				}
				candidate.result.Signals = appendUnique(candidate.result.Signals, "same-file-bridge")
				candidate.score = derivedSearchScore(maxFloat64(left.score, right.score), 0.45*(left.score+right.score))
				candidate.baseScore = candidate.score
				seenBridges[bridgeKey] = true
				out = append(out, candidate)
			}
		}
	}
	sortSearchCandidates(out)
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

func searchSymbolByID(symbols []SymbolRecord, id string) (SymbolRecord, bool) {
	for _, symbol := range symbols {
		if symbol.ID == id {
			return symbol, true
		}
	}
	return SymbolRecord{}, false
}

func searchFlowSymbolKind(kind string) bool {
	switch kind {
	case "function", "method", "constructor", "destructor", "closure":
		return true
	default:
		return false
	}
}

// selectHybridCandidates uses reciprocal-rank fusion to put sparse windows
// from semantically strong files first, then reserves the rest of a deep
// ranking for distinct files found by the syntax-aware search. The split keeps
// region coverage from collapsing into repeated chunks from a few files while
// retaining enough sparse depth to find relevant locations inside large files.
func selectHybridCandidates(semantic, sparse []searchCandidate, topK int) []searchCandidate {
	if topK <= 0 {
		return nil
	}
	if len(semantic) == 0 {
		selected := append([]searchCandidate(nil), sparse[:minInt(topK, len(sparse))]...)
		for index := range selected {
			selected[index].score = 1 / float64(sparseSearchRRFConstant+index+1)
		}
		return selected
	}
	if len(sparse) == 0 {
		return append([]searchCandidate(nil), semantic[:minInt(topK, len(semantic))]...)
	}

	semanticFileRank := make(map[string]int, len(semantic))
	for index, candidate := range semantic {
		if _, exists := semanticFileRank[candidate.result.FilePath]; !exists {
			semanticFileRank[candidate.result.FilePath] = index + 1
		}
	}
	// RRF combines rankings at the requested retrieval depth. Allowing the
	// entire sparse tail to participate lets weak chunks from a highly ranked
	// file leapfrog genuinely strong top-K sparse regions.
	fusedSparse := append([]searchCandidate(nil), sparse[:minInt(topK, len(sparse))]...)
	for index := range fusedSparse {
		sparseRank := index + 1
		fusedSparse[index].score = 1 / float64(sparseSearchRRFConstant+sparseRank)
		if fileRank, exists := semanticFileRank[fusedSparse[index].result.FilePath]; exists {
			fusedSparse[index].score += sparseSearchFileRankWeight / float64(sparseSearchRRFConstant+fileRank)
		}
	}
	sortSearchCandidates(fusedSparse)

	semanticHead := minInt(
		len(semantic),
		minInt(maxDeepSearchSemanticHead, maxInt(1, topK/deepSearchSemanticHeadDivisor)),
	)
	sparseTarget := minInt(len(fusedSparse), topK*deepSearchSparseNumerator/deepSearchSparseDenominator)
	selected := make([]searchCandidate, 0, minInt(topK, len(semantic)+len(fusedSparse)))
	// Every append goes through the region-dedup guard, INCLUDING the two head blocks.
	// The head blocks used to append unchecked, so a region the semantic ranking and the
	// sparse ranking both found was seated twice — observed as one symbol holding ranks 1,
	// 2 and 3 of an 11-row payload that contained only 9 distinct locations.
	appendUnique := func(candidates []searchCandidate, limit int) {
		for _, candidate := range candidates {
			if limit >= 0 && len(selected) >= limit {
				return
			}
			if len(selected) >= topK {
				return
			}
			if containsSearchCandidate(selected, candidate) {
				continue
			}
			selected = append(selected, candidate)
		}
	}
	appendUnique(semantic[:semanticHead], -1)
	appendUnique(fusedSparse, minInt(topK, len(selected)+sparseTarget))

	// Prefer one representative for every additional semantic file. This is a
	// coverage reserve, so cross-modality region overlap is intentionally not a
	// reason to discard a sparse window.
	seenFiles := make(map[string]bool, len(selected))
	for _, candidate := range selected {
		seenFiles[candidate.result.FilePath] = true
	}
	for _, candidate := range semantic[semanticHead:] {
		if len(selected) == topK {
			break
		}
		if seenFiles[candidate.result.FilePath] || containsSearchCandidate(selected, candidate) {
			continue
		}
		seenFiles[candidate.result.FilePath] = true
		selected = append(selected, candidate)
	}
	appendUnique(semantic[semanticHead:], -1)
	appendUnique(fusedSparse[minInt(sparseTarget, len(fusedSparse)):], -1)
	return selected
}

func containsSearchCandidate(candidates []searchCandidate, target searchCandidate) bool {
	for _, candidate := range candidates {
		if candidate.result.FilePath == target.result.FilePath &&
			candidate.result.StartLine == target.result.StartLine &&
			candidate.result.EndLine == target.result.EndLine {
			return true
		}
	}
	return false
}

// searchCandidateLocationKey identifies the source region a candidate occupies. Two
// candidates with the same key are the same region reached by different retrieval
// modalities, never two answers.
func searchCandidateLocationKey(candidate searchCandidate) string {
	return fmt.Sprintf("%s\x00%d\x00%d", candidate.result.FilePath, candidate.result.StartLine, candidate.result.EndLine)
}

// rescaleFusedCandidateScores restores a relevance score on a fused ranking.
//
// Reciprocal-rank fusion works on ranks, so the intermediate scores it computes are rank
// artifacts (1/(k+rank)) with no relation to how well anything matched. Reporting those —
// or, worse, a bare 1/rank — leaves a caller unable to tell a strong hit from a repo that
// simply does not contain what was asked for. Each selected row therefore gets its OWN
// retrieval score back: the semantic score when the region was retrieved semantically,
// otherwise its BM25 score linearly rescaled so the sparse block's maximum lines up with
// the semantic maximum. Ordering is untouched — only the reported number changes.
func rescaleFusedCandidateScores(selected, semantic, sparse []searchCandidate) []searchCandidate {
	if len(selected) == 0 {
		return selected
	}
	semanticScores, maxSemantic := bestSearchCandidateScores(semantic)
	sparseScores, maxSparse := bestSearchCandidateScores(sparse)
	scale := 1.0
	if maxSparse > 0 && maxSemantic > 0 {
		scale = maxSemantic / maxSparse
	}
	for index := range selected {
		key := searchCandidateLocationKey(selected[index])
		if score, ok := semanticScores[key]; ok {
			selected[index].score = score
			continue
		}
		if score, ok := sparseScores[key]; ok {
			selected[index].score = score * scale
		}
	}
	return selected
}

func bestSearchCandidateScores(candidates []searchCandidate) (map[string]float64, float64) {
	scores := make(map[string]float64, len(candidates))
	best := 0.0
	for _, candidate := range candidates {
		key := searchCandidateLocationKey(candidate)
		if existing, exists := scores[key]; !exists || candidate.score > existing {
			scores[key] = candidate.score
		}
		if candidate.score > best {
			best = candidate.score
		}
	}
	return scores, best
}

func selectDiverseCandidates(candidates []searchCandidate, topK, maxPerFile int) []searchCandidate {
	remaining := append([]searchCandidate(nil), candidates...)
	selected := make([]searchCandidate, 0, minInt(topK, len(remaining)))
	perFile := map[string]int{}
	perSymbolName := map[string]int{}
	perBaseName := map[string]int{}
	distinctFileTarget := minInt(topK, (topK+1)/2)
	for len(selected) < topK {
		// Search is primarily a discovery operation for coding agents. Give the
		// agent one strong location from each candidate file before spending the
		// remaining result budget on additional regions in files it has already
		// seen. Without this first pass, a few large or repetitive files can fill
		// the context window even when other relevant implementation files ranked
		// close behind them. The reserve is relevance-bounded: an exact usage or
		// second requested symbol must not be buried under weak matches merely
		// because it shares a file with an earlier result.
		fileLimit := maxPerFile
		if len(perFile) < distinctFileTarget && shouldReserveUnseenSearchFile(
			remaining, selected, perFile, perSymbolName, perBaseName, maxPerFile,
		) {
			fileLimit = 1
		}
		bestIndex := -1
		bestAdjusted := -math.MaxFloat64
		for i := range remaining {
			candidate := remaining[i]
			if perFile[candidate.result.FilePath] >= fileLimit || overlapsSelected(candidate, selected) {
				continue
			}
			adjusted := adjustedSearchCandidateScore(candidate, perFile, perSymbolName, perBaseName)
			if adjusted > bestAdjusted || (adjusted == bestAdjusted && searchCandidateLess(candidate, remaining[bestIndex])) {
				bestIndex = i
				bestAdjusted = adjusted
			}
		}
		if bestIndex < 0 {
			break
		}
		selected = append(selected, remaining[bestIndex])
		chosen := remaining[bestIndex].result
		perFile[chosen.FilePath]++
		symbolKey := strings.ToLower(chosen.QualifiedName)
		if symbolKey == "" {
			symbolKey = strings.ToLower(chosen.SymbolName)
		}
		if symbolKey != "" {
			perSymbolName[symbolKey]++
		}
		perBaseName[strings.ToLower(filepath.Base(chosen.FilePath))]++
		remaining = append(remaining[:bestIndex], remaining[bestIndex+1:]...)
	}
	return selected
}

func shouldReserveUnseenSearchFile(
	candidates, selected []searchCandidate,
	perFile, perSymbolName, perBaseName map[string]int,
	maxPerFile int,
) bool {
	bestAny := -math.MaxFloat64
	bestUnseen := -math.MaxFloat64
	bestRelationship := -math.MaxFloat64
	for _, candidate := range candidates {
		if perFile[candidate.result.FilePath] >= maxPerFile || overlapsSelected(candidate, selected) {
			continue
		}
		adjusted := adjustedSearchCandidateScore(candidate, perFile, perSymbolName, perBaseName)
		if adjusted > bestAny {
			bestAny = adjusted
		}
		if perFile[candidate.result.FilePath] > 0 && searchRelationshipCandidate(candidate) && adjusted > bestRelationship {
			bestRelationship = adjusted
		}
		if perFile[candidate.result.FilePath] == 0 && adjusted > bestUnseen {
			bestUnseen = adjusted
		}
	}
	if bestUnseen == -math.MaxFloat64 {
		return false
	}
	if bestRelationship != -math.MaxFloat64 && bestRelationship >= 0.65*bestUnseen {
		return false
	}
	if bestAny <= 0 {
		return true
	}
	return bestUnseen >= searchDiversityRelevanceRatio*bestAny
}

func searchRelationshipCandidate(candidate searchCandidate) bool {
	for _, signal := range candidate.result.Signals {
		switch signal {
		case "symbol-usage", "same-container-neighbor", "same-file-bridge":
			return true
		}
	}
	return false
}

func adjustedSearchCandidateScore(
	candidate searchCandidate,
	perFile, perSymbolName, perBaseName map[string]int,
) float64 {
	symbolKey := strings.ToLower(candidate.result.QualifiedName)
	if symbolKey == "" {
		symbolKey = strings.ToLower(candidate.result.SymbolName)
	}
	symbolPenalty := 0.0
	if symbolKey != "" {
		symbolPenalty = 4 * float64(perSymbolName[symbolKey])
	}
	baseKey := strings.ToLower(filepath.Base(candidate.result.FilePath))
	return candidate.score -
		0.8*float64(perFile[candidate.result.FilePath]) -
		symbolPenalty -
		2*float64(perBaseName[baseKey])
}

func overlapsSelected(candidate searchCandidate, selected []searchCandidate) bool {
	for _, existing := range selected {
		if candidate.result.FilePath != existing.result.FilePath {
			continue
		}
		intersection := minInt(candidate.result.EndLine, existing.result.EndLine) - maxInt(candidate.result.StartLine, existing.result.StartLine) + 1
		if intersection <= 0 {
			continue
		}
		shorter := minInt(candidate.result.EndLine-candidate.result.StartLine+1, existing.result.EndLine-existing.result.StartLine+1)
		if float64(intersection)/float64(shorter) >= 0.6 {
			return true
		}
	}
	return false
}

func sortSearchCandidates(candidates []searchCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return searchCandidateLess(candidates[i], candidates[j])
	})
}

func searchCandidateLess(left, right searchCandidate) bool {
	if left.result.FilePath != right.result.FilePath {
		return left.result.FilePath < right.result.FilePath
	}
	if left.result.StartLine != right.result.StartLine {
		return left.result.StartLine < right.result.StartLine
	}
	return left.result.EndLine < right.result.EndLine
}

func matchingLineRegions(q searchQuery, lines []string, start, end, context, maxLines int) [][2]int {
	var hits []int
	for line := start; line <= end; line++ {
		if textMatchesSearchQuery(q, lines[line-1]) {
			hits = append(hits, line)
		}
	}
	return regionsAroundHits(hits, start, end, context, maxLines)
}

func regionsAroundHits(hits []int, lower, upper, context, maxLines int) [][2]int {
	if len(hits) == 0 {
		return nil
	}
	var regions [][2]int
	groupStart := hits[0]
	groupEnd := hits[0]
	flush := func() {
		start := maxInt(lower, groupStart-context)
		end := minInt(upper, groupEnd+context)
		if end-start+1 > maxLines {
			end = start + maxLines - 1
		}
		regions = append(regions, [2]int{start, end})
	}
	for _, hit := range hits[1:] {
		if hit-groupEnd <= context*2+1 && hit-groupStart < maxLines {
			groupEnd = hit
			continue
		}
		flush()
		groupStart, groupEnd = hit, hit
	}
	flush()
	return regions
}

func buildSearchQuery(query string) searchQuery {
	weights := map[string]float64{}
	add := func(term string, weight float64) {
		if len(term) < 2 || searchStopWords[term] {
			return
		}
		hasAlphanumeric := false
		for _, character := range term {
			if unicode.IsLetter(character) || unicode.IsDigit(character) {
				hasAlphanumeric = true
				break
			}
		}
		if !hasAlphanumeric {
			return
		}
		if weight > weights[term] {
			weights[term] = weight
		}
	}
	// Case folding has to be score-neutral. codeLikeSearchToken reads consecutive capitals
	// as an identifier signal (SCREAMING_SNAKE constants, acronyms), which made a SHOUTED
	// prose query score the same symbol far above the identical lowercase query. When every
	// letter-bearing word of a multi-word query is uppercase, the capitals distinguish
	// nothing — they are emphasis, not identifier structure — so fold the query before
	// reading code-likeness out of it. A single-word query (`ENOENT`), any mixed-case query,
	// and separator-bearing tokens (`MAX_SIZE` stays code-like after folding) are untouched.
	rawTokens := searchWordPattern.FindAllString(query, -1)
	if searchQueryIsShouted(rawTokens) {
		for index := range rawTokens {
			rawTokens[index] = strings.ToLower(rawTokens[index])
		}
	}
	identifierTokens := map[string]bool{}
	for _, raw := range rawTokens {
		codeLike := codeLikeSearchToken(raw)
		if identifierShapedToken(raw) {
			if compacted := compactSearchIdentifier(raw); compacted != "" {
				identifierTokens[compacted] = true
			}
		}
		for index, term := range searchTokenVariants(raw) {
			weight := 1.0
			if codeLike && index == 0 {
				weight = codeLikeSearchWeight(raw)
			} else if codeLike {
				weight = 1.1
			}
			add(term, weight)
		}
	}
	originalTerms := make([]string, 0, len(weights))
	for term := range weights {
		originalTerms = append(originalTerms, term)
	}
	for _, term := range originalTerms {
		for _, related := range morphologicalSearchTerms(term) {
			add(related, 0.55)
		}
	}
	termSet := make(map[string]bool, len(weights))
	terms := make([]string, 0, len(weights))
	for term := range weights {
		termSet[term] = true
		terms = append(terms, term)
	}
	sort.Slice(terms, func(i, j int) bool {
		if weights[terms[i]] != weights[terms[j]] {
			return weights[terms[i]] > weights[terms[j]]
		}
		if len(terms[i]) != len(terms[j]) {
			return len(terms[i]) > len(terms[j])
		}
		return terms[i] < terms[j]
	})
	if len(terms) > maxSearchQueryTerms {
		terms = terms[:maxSearchQueryTerms]
		termSet = make(map[string]bool, len(terms))
		trimmedWeights := make(map[string]float64, len(terms))
		for _, term := range terms {
			termSet[term] = true
			trimmedWeights[term] = weights[term]
		}
		weights = trimmedWeights
	}
	rawLower := strings.ToLower(strings.TrimSpace(query))
	wordSequence := searchQueryWordSequence(rawLower)
	return searchQuery{
		rawLower:         rawLower,
		terms:            terms,
		termSet:          termSet,
		weights:          weights,
		words:            searchQueryWords(rawLower),
		wordSequence:     wordSequence,
		matchableWords:   matchableQueryWords(wordSequence, termSet, nil),
		identifierTokens: identifierTokens,
	}
}

// identifierShapedToken reports whether a token was written the way code spells names: it
// carries a separator (`_ . / : $ + #`) or a case hump inside the word (fooBar, NonNull).
// It is deliberately stricter than codeLikeSearchToken, which also accepts any run of
// capitals — issue prose is full of shouted words (BUG, TODO, API, JSON) that are not the
// caller naming a symbol, and only this stricter shape may claim an exact name match.
func identifierShapedToken(raw string) bool {
	trimmed := strings.Trim(raw, "./:-")
	if strings.ContainsAny(trimmed, "_./:$+#") {
		return true
	}
	humped, lowercase := false, false
	for index, character := range trimmed {
		switch {
		case unicode.IsUpper(character):
			humped = humped || index > 0
		case unicode.IsLower(character):
			lowercase = true
		}
	}
	return humped && lowercase
}

// searchQueryIsShouted reports whether the capitals in a query carry no information: at
// least two letter-bearing words and not one lowercase letter among them. One all-caps word
// is an identifier a caller typed; a whole all-caps sentence is emphasis.
func searchQueryIsShouted(tokens []string) bool {
	lettered := 0
	for _, token := range tokens {
		hasLetter := false
		for _, character := range token {
			if !unicode.IsLetter(character) {
				continue
			}
			hasLetter = true
			if unicode.IsLower(character) {
				return false
			}
		}
		if hasLetter {
			lettered++
		}
	}
	return lettered >= 2
}

// searchQueryUnmatchableWord returns the predicate "this query word cannot contribute a
// match to ANY document in the corpus": either it never became a search term (a stop word,
// or too short to index), or it is a term no indexed file contains. Such a word is inert
// evidence, and inert evidence may not cost a document points it already earned.
// documentFrequency may be nil before corpus statistics exist, in which case only the
// never-became-a-term half of the test applies.
func searchQueryUnmatchableWord(termSet map[string]bool, documentFrequency map[string]int) func(string) bool {
	return func(word string) bool {
		if !termSet[word] {
			return true
		}
		if documentFrequency == nil {
			return false
		}
		return documentFrequency[word] == 0
	}
}

// matchableQueryWords drops from the written word order every word that cannot contribute a
// match to any document, so the phrase the caller meant survives the noise words around it.
func matchableQueryWords(wordSequence []string, termSet map[string]bool, documentFrequency map[string]int) []string {
	unmatchable := searchQueryUnmatchableWord(termSet, documentFrequency)
	matchable := make([]string, 0, len(wordSequence))
	for _, word := range wordSequence {
		if unmatchable(word) {
			continue
		}
		matchable = append(matchable, word)
	}
	return matchable
}

// withCorpusPresence returns a copy of q that knows which of its words the corpus can
// actually match, so scoring stops charging documents for words nothing contains.
// documentFrequency is keyed by term over the indexed file set.
func (q searchQuery) withCorpusPresence(documentFrequency map[string]int) searchQuery {
	q.matchableWords = matchableQueryWords(q.wordSequence, q.termSet, documentFrequency)
	return q
}

func buildSparseSearchQuery(query string) searchQuery {
	terms := make([]string, 0, maxSparseSearchQueryTerms)
	termSet := make(map[string]bool, maxSparseSearchQueryTerms)
	weights := make(map[string]float64, maxSparseSearchQueryTerms)
	for _, term := range sparseSearchTokens(query) {
		if len(term) < 2 || sparseSearchStopWords[term] || termSet[term] {
			continue
		}
		termSet[term] = true
		weights[term] = 1
		terms = append(terms, term)
		if len(terms) == maxSparseSearchQueryTerms {
			break
		}
	}
	rawLower := strings.ToLower(strings.TrimSpace(query))
	wordSequence := searchQueryWordSequence(rawLower)
	return searchQuery{
		rawLower:       rawLower,
		terms:          terms,
		termSet:        termSet,
		weights:        weights,
		words:          searchQueryWords(rawLower),
		wordSequence:   wordSequence,
		matchableWords: matchableQueryWords(wordSequence, termSet, nil),
	}
}

func codeLikeSearchWeight(raw string) float64 {
	trimmed := strings.Trim(raw, "./:+-")
	letters := 0
	allUpper := true
	for _, character := range trimmed {
		if !unicode.IsLetter(character) {
			continue
		}
		letters++
		if unicode.IsLower(character) {
			allUpper = false
		}
	}
	if allUpper && letters > 0 && letters <= 4 {
		return 1.4
	}
	return 2.5
}

func codeLikeSearchToken(raw string) bool {
	trimmed := strings.Trim(raw, "./:-")
	if strings.ContainsAny(trimmed, "_./:$+#") || strings.HasPrefix(raw, "--") {
		return true
	}
	uppercase := 0
	lowercase := 0
	for index, character := range trimmed {
		if unicode.IsUpper(character) {
			uppercase++
			if index > 0 {
				return true
			}
		} else if unicode.IsLower(character) {
			lowercase++
		}
	}
	return uppercase >= 2 && lowercase > 0
}

func morphologicalSearchTerms(term string) []string {
	var out []string
	switch {
	case strings.HasSuffix(term, "ization") && len(term) > len("ization")+2:
		out = append(out, strings.TrimSuffix(term, "ization")+"ize")
	case strings.HasSuffix(term, "ification") && len(term) > len("ification")+2:
		out = append(out, strings.TrimSuffix(term, "ification")+"ify")
	case strings.HasSuffix(term, "ing") && len(term) > 5:
		stem := strings.TrimSuffix(term, "ing")
		out = append(out, stem, stem+"e")
	case strings.HasSuffix(term, "ed") && len(term) > 4:
		stem := strings.TrimSuffix(term, "ed")
		out = append(out, stem, stem+"e")
	}
	if strings.HasSuffix(term, "s") && !strings.HasSuffix(term, "ss") && len(term) > 4 {
		out = append(out, strings.TrimSuffix(term, "s"))
	}
	return appendUnique(nil, out...)
}

func searchTokenVariants(raw string) []string {
	raw = strings.Trim(raw, "./:-")
	if raw == "" {
		return nil
	}
	variants := []string{strings.ToLower(raw)}
	for _, segment := range strings.FieldsFunc(raw, func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsDigit(character)
	}) {
		variants = append(variants, strings.ToLower(segment))
	}
	var current []rune
	runes := []rune(raw)
	flush := func() {
		if len(current) > 0 {
			variants = append(variants, strings.ToLower(string(current)))
			current = nil
		}
	}
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if len(current) > 0 && unicode.IsUpper(r) {
			previous := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(previous) || unicode.IsDigit(previous) || (unicode.IsUpper(previous) && nextLower) {
				flush()
			}
		}
		current = append(current, r)
	}
	flush()
	return appendUnique(nil, variants...)
}

func sparseSearchTokens(text string) []string {
	var out []string
	for _, raw := range sparseSearchWordPattern.FindAllString(text, -1) {
		var current []rune
		currentDigit := false
		flush := func() {
			if len(current) > 0 {
				out = append(out, strings.ToLower(string(current)))
				current = nil
			}
		}
		runes := []rune(raw)
		for index, character := range runes {
			if character == '_' {
				flush()
				continue
			}
			isDigit := unicode.IsDigit(character)
			if len(current) > 0 && isDigit != currentDigit {
				flush()
			}
			if len(current) > 0 && !isDigit && unicode.IsUpper(character) {
				previous := runes[index-1]
				nextLower := index+1 < len(runes) && unicode.IsLower(runes[index+1])
				if unicode.IsLower(previous) || (unicode.IsUpper(previous) && nextLower) {
					flush()
				}
			}
			currentDigit = isDigit
			current = append(current, character)
		}
		flush()
	}
	return out
}

func sparseSearchTermCounts(text string, queryTerms map[string]bool) (map[string]int, int) {
	counts := map[string]int{}
	tokens := sparseSearchTokens(text)
	for _, token := range tokens {
		if queryTerms[token] {
			counts[token]++
		}
	}
	return counts, len(tokens)
}

func searchTermCounts(text string, queryTerms map[string]bool) (map[string]int, int) {
	counts := map[string]int{}
	length := 0
	for _, raw := range searchWordPattern.FindAllString(text, -1) {
		for _, token := range searchTokenVariants(raw) {
			if len(token) < 2 {
				continue
			}
			length++
			if queryTerms[token] {
				counts[token]++
			}
		}
	}
	return counts, length
}

func textMatchesSearchQuery(q searchQuery, text string) bool {
	lower := strings.ToLower(text)
	for _, term := range q.terms {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

func searchFocusLine(q searchQuery, lines []string, start, end int) int {
	bestLine := start
	bestMatches := 0.0
	for line := start; line <= end; line++ {
		lower := strings.ToLower(lines[line-1])
		matches := 0.0
		for _, term := range q.terms {
			if strings.Contains(lower, term) {
				matches += q.weights[term]
			}
		}
		if matches > bestMatches {
			bestLine = line
			bestMatches = matches
		}
	}
	return bestLine
}

func focusedSnippetRegion(start, end, focus, maxLines int) (int, int) {
	if maxLines <= 0 || end-start+1 <= maxLines {
		return start, end
	}
	half := maxLines / 2
	snippetStart := maxInt(start, focus-half)
	snippetEnd := minInt(end, snippetStart+maxLines-1)
	if snippetEnd-snippetStart+1 < maxLines {
		snippetStart = maxInt(start, snippetEnd-maxLines+1)
	}
	return snippetStart, snippetEnd
}

func pathSearchScore(q searchQuery, filePath string) float64 {
	lower := strings.ToLower(filepath.ToSlash(filePath))
	base := strings.ToLower(filepath.Base(filePath))
	score := 0.0
	for _, term := range q.terms {
		weight := q.weights[term]
		if weight != 1 && weight < 1.25 {
			continue
		}
		if strings.Contains(base, term) {
			score += 2.5 * weight
		} else if strings.Contains(lower, term) {
			score += 1.25 * weight
		}
	}
	return score
}

func searchPathPrior(q searchQuery, filePath string) float64 {
	lower := "/" + strings.ToLower(filepath.ToSlash(filePath))
	score := 0.0
	if strings.Contains(lower, "/src/") || strings.Contains(lower, "/include/") || strings.Contains(lower, "/lib/") || strings.Contains(lower, "/packages/") {
		score += 0.5
	}
	if strings.Contains(lower, "/.github/workflows/") && !searchQuerySupplied(q, "workflow", "pipeline", "ci", "action") {
		score -= 6
	}
	// .github/ metadata (ISSUE_TEMPLATE, PULL_REQUEST_TEMPLATE, CONTRIBUTING, FUNDING, etc.)
	// is NEVER a code fix site, yet issue-template prose matches the problem statement's own
	// filled-in fields (version/expected/actual boilerplate) and body-BM25-outranks the real
	// source (measured: briannesbitt .github/ISSUE_TEMPLATE.md ranked #1 over src/Carbon/*).
	// Hard-demote all non-workflow .github/ paths so template prose can never top the list.
	if strings.Contains(lower, "/.github/") && !strings.Contains(lower, "/.github/workflows/") {
		score -= 12
	}
	if strings.Contains(lower, "/dependencies/") || strings.Contains(lower, "/third_party/") || strings.Contains(lower, "/third-party/") {
		score -= 5
	}
	// "regression"/"regressions" are deliberately NOT test-intent words. In issue prose
	// "a regression" names behaviour that used to work, not a request for the regression
	// suite; a caller who does want tests writes "test"/"spec"/"fixture", and the phrase
	// "regression test" already contains "test". Keeping them here let the commonest word
	// in a bug report switch off the test demotion for the whole query.
	if searchTestArtifactPath(lower) && !searchQuerySupplied(q,
		"test", "tests", "testing", "spec", "specs", "fixture", "fixtures",
	) {
		// Strong demotion (was -1.5 — far too weak): a test file that exercises the buggy
		// function matches the issue's exact code tokens (function name + behaviour keywords),
		// so it out-scores the real source at ~26-47 while the implementation sits at ~13.
		// A bug-fix task never edits a test, so tests are pure exploration noise. -12 reliably
		// sinks the test below the editable source (measured: briannesbitt RoundTest.php crowded
		// out src/Carbon/Traits/Rounding.php; php-cs-fixer's 13 test files buried the fix). Not
		// excluded outright — a test still surfaces if it is the only match.
		score -= 12
	}
	if searchDocumentationArtifactPath(lower) && !searchQuerySupplied(q,
		"doc", "docs", "documentation", "readme", "readmes", "guide", "guides", "example", "examples",
	) {
		// Strong demotion: docs describe features in the issue's own vocabulary and
		// otherwise out-score the implementing code on a body-text match. A "fix the
		// bug" query wants code, not the manual. (Was -1.25 — too weak to flip a ~9.9
		// doc match below the ~5-8 code match; -6 reliably ranks code first.)
		score -= 6
	}
	if searchGeneratedArtifactPath(lower) && !searchQuerySupplied(q,
		"generated", "generator", "generators", "codegen", "codegens", "build", "builds", "dist", "bundle", "bundles",
		"amalgamation", "amalgamated", "single", "onefile",
	) {
		// Strong demotion (was -2): an amalgamation scores as high as the real source
		// (it IS the source, concatenated) so -2 leaves it competitive; -6 ranks the
		// editable per-file source first so the agent's edit actually sticks.
		score -= 6
	}
	return score
}

func searchTestArtifactPath(lower string) bool {
	return strings.Contains(lower, "/test/") || strings.Contains(lower, "/tests/") ||
		strings.Contains(lower, "/testdata/") || strings.Contains(lower, "/fixtures/") ||
		strings.Contains(lower, "/__tests__/") || strings.Contains(lower, "/spec/") ||
		strings.Contains(lower, ".test.") || strings.Contains(lower, ".spec.") ||
		strings.Contains(lower, "_test.") || strings.Contains(lower, "_spec.") ||
		strings.HasSuffix(lower, "_test.go") || strings.HasSuffix(lower, ".test")
}

// searchDocumentationArtifactPath reports whether a path is prose documentation.
// The taxonomy lives in search_file_class.go so the additive prior here and the
// multiplicative file-class prior applied at ranking time can never disagree
// about what "documentation" means (segment-aware, so `versioned_docs/` and
// `website/` count, and .mdx counts alongside .md/.rst/.adoc/.txt).
func searchDocumentationArtifactPath(lower string) bool {
	segments := searchPathSegments(lower)
	dirs := segments
	if len(dirs) > 0 {
		dirs = dirs[:len(dirs)-1]
	}
	return searchDocumentationClassPath(lower, filepath.Base(lower), dirs)
}

func searchGeneratedArtifactPath(lower string) bool {
	base := filepath.Base(lower)
	// Amalgamated / single-header / concatenated build artifacts: a generated COPY of
	// the real per-file source (e.g. nlohmann's single_include/nlohmann/json.hpp is a
	// concatenation of include/nlohmann/**). They match the query as well as the real
	// source AND duplicate it, so an agent that edits the amalgamation makes a change
	// that gets overwritten by the next codegen — always steer to the real source.
	if strings.Contains(lower, "/single_include/") || strings.Contains(lower, "/single-include/") ||
		strings.Contains(lower, "amalgamat") || strings.Contains(lower, "/onefile/") ||
		strings.Contains(lower, "/amalgamation/") {
		return true
	}
	return strings.Contains(lower, "/generated/") || strings.Contains(lower, "/dist/") ||
		strings.Contains(lower, "/build/") || strings.Contains(lower, "/gen/") ||
		strings.Contains(base, ".generated.") || strings.Contains(base, "_generated.") ||
		strings.HasPrefix(base, "generated_") || strings.HasSuffix(base, ".min.js")
}

// searchQuerySupplied reports whether the caller ASKED FOR a class of file, which is how
// every relevance prior below 1 is switched back off ("update the docs for…", "fix the
// example", "add a test").
//
// Intent is read from the words the caller wrote, never from fragments mined out of an
// identifier they merely mentioned. The distinction is not cosmetic: the tokenizer splits
// `NamedByteArrayTest.java` into `named`/`byte`/`array`/`test` at weight 1.1, so under a
// plain weight lookup an issue that merely names a reproduction file switched OFF the -12
// test-artifact demotion for the whole query — and test fixtures, which restate the issue in
// its own vocabulary, took the top of the ranking away from the source being fixed
// (measured: a lombok issue quoting `NamedByteArrayTest.java` ranked a `test/transform/
// resource/before/*.java` fixture first). Mentioning an identifier is not a request.
func searchQuerySupplied(q searchQuery, terms ...string) bool {
	words := q.words
	if words == nil {
		// Hand-built queries (tests, direct API callers) carry no precomputed word set.
		// Derive it rather than silently answering "no intent" for every class.
		words = searchQueryWords(q.rawLower)
	}
	for _, term := range terms {
		if words[term] {
			return true
		}
	}
	return false
}

// searchQueryWords splits a raw query into the words a human wrote: maximal alphanumeric
// runs. Any separator a writer types — space, `/`, `.`, `_`, `-`, punctuation — ends a word,
// so `docs/api.md` contributes `docs`, while `NamedByteArrayTest` contributes only itself.
func searchQueryWords(rawLower string) map[string]bool {
	words := map[string]bool{}
	for _, word := range searchQueryWordSequence(rawLower) {
		words[word] = true
	}
	return words
}

// searchQueryWordSequence is searchQueryWords in written order, duplicates kept.
func searchQueryWordSequence(rawLower string) []string {
	var words []string
	current := make([]rune, 0, 16)
	flush := func() {
		if len(current) > 0 {
			words = append(words, string(current))
			current = current[:0]
		}
	}
	for _, character := range rawLower {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			current = append(current, unicode.ToLower(character))
			continue
		}
		flush()
	}
	flush()
	return words
}

func symbolSearchScore(q searchQuery, symbol SymbolRecord) (float64, []string) {
	name := strings.ToLower(symbol.Name)
	qualified := strings.ToLower(symbol.QualifiedName)
	signature := strings.ToLower(symbol.Signature)
	compactName := compactSearchIdentifier(name)
	score := 0.0
	switch symbol.Kind {
	case "function", "method":
		score += 2
	case "class", "interface", "struct", "trait", "type", "enum", "record", "object", "protocol":
		score += 0.75
	case "block", "workflow", "document":
		score -= 1.5
	case "section":
		score -= 0.5
	}
	var signals []string
	if matchesExactSymbolForm(q, compactName) {
		score += exactSymbolBonus(q, symbol)
		signals = append(signals, "exact-symbol")
	}
	for _, term := range q.terms {
		weight := q.weights[term]
		switch {
		case name == term:
			score += 6 * weight
			signals = append(signals, "symbol-name")
		case strings.Contains(name, term) || strings.Contains(qualified, term):
			score += 3 * weight
			signals = append(signals, "symbol-name")
		case strings.Contains(signature, term):
			score += 1.5 * weight
			signals = append(signals, "signature")
		}
	}
	return score, appendUnique(nil, signals...)
}

// exactSymbolBonus weights an exact name match by what the named thing is.
//
// A container — a class, an interface, an enum, a module-level constant — is a NOUN issue
// prose is full of, and reading it as the edit site crowds the head of the ranking with
// declarations. Measured: "GsonBuilder should throw when registering an adapter for Object or
// JsonElement" named three types, and at the flat bonus the classes GsonBuilder, JsonElement
// and the constant JSON_ELEMENT took ranks 1-3 from the method the patch actually changes.
// A callable, by contrast, is where a fix lands, so it keeps the full bonus. When the caller
// asks for the container by itself — the whole query is that one name — it is the answer, and
// the full bonus applies again.
//
// A CONSTRUCTOR is the one callable that belongs in the container tier. It is named after its
// own type (Java `GsonBuilder.GsonBuilder`, C++ `Foo::Foo`), so an exact match on it is a
// type-name match wearing a callable's clothes — the very thing the container tier exists to
// hold back. At the callable bonus it outranks the type it constructs (14 vs 6) and buries real
// methods: measured on 43 Java instances, `JsonElement.JsonElement` and `GsonBuilder.GsonBuilder`
// took ranks 1-2 from GsonBuilder.registerTypeAdapter on a query that named both types.
func exactSymbolBonus(q searchQuery, symbol SymbolRecord) float64 {
	switch symbol.Kind {
	case "class", "interface", "struct", "trait", "type", "enum", "record", "object", "protocol",
		"annotation", "const", "constant", "variable", "field", "property",
		"module", "namespace", "package":
		if len(q.wordSequence) > 1 {
			return 6
		}
	default:
		if namedAfterOwnContainer(symbol) && len(q.wordSequence) > 1 {
			// The type this constructor is named after is itself a candidate in the same
			// file, and it earns this bonus. Paying it twice for one name is what lifts the
			// constructor over the methods a fix lands in — a method whose own name is a
			// single word (`replace`) cannot earn the bonus at all inside a multi-word
			// query, so the duplicate is the whole margin. Spend the name once, on the type.
			return 0
		}
	}
	return 14
}

// namedAfterOwnContainer reports whether a member's own name repeats the name of the type that
// contains it — the constructor shape, in every language that spells constructors that way. It
// compares the last two segments of the qualified name (`GsonBuilder.GsonBuilder`,
// `Mode.FAST.FAST`) so a plain method that merely shares a name with some OTHER type is
// unaffected.
func namedAfterOwnContainer(symbol SymbolRecord) bool {
	qualified := symbol.QualifiedName
	cut := strings.LastIndex(qualified, ".")
	if cut <= 0 {
		return false
	}
	member := qualified[cut+1:]
	container := qualified[:cut]
	if inner := strings.LastIndex(container, "."); inner >= 0 {
		container = container[inner+1:]
	}
	return member != "" && member == container
}

// matchesExactSymbolForm reports whether the query spells this symbol out by name.
//
// It used to compare the symbol against the compaction of the WHOLE query, which made the
// bonus all-or-nothing over every token the caller typed: appending one more word — a
// politeness ("please"), a word the repository has never heard of ("xyzzy"), an article
// ("update THE market sizes") — revoked a bonus the symbol had already earned from the words
// that did match. That is the opposite of how a term-scoring model must behave: a term the
// document cannot match contributes zero, never a penalty.
//
// So the test is now local to the words that name the symbol. The bonus is earned when the
// symbol's name is spelled out by
//   - a contiguous run of two or more query words, in written order ("update market sizes"
//     inside a longer sentence, and "get the value" for a name really spelled get_the_value);
//   - the same over the matchable words only, so stop words and words nothing in the corpus
//     contains cannot break the run apart ("update THE market sizes PLEASE");
//   - one token the caller wrote in identifier shape, whatever surrounds it
//     ("buildGroupIndex please");
//   - or a single-word query equal to the name, which is the original whole-query rule.
//
// Every clause only ADDS a way to earn the bonus, so no word can take one away.
func matchesExactSymbolForm(q searchQuery, compactName string) bool {
	if compactName == "" {
		return false
	}
	if q.identifierTokens[compactName] {
		return true
	}
	if len(q.wordSequence) == 1 {
		return q.wordSequence[0] == compactName
	}
	return spellsCompactIdentifier(q.wordSequence, compactName) ||
		spellsCompactIdentifier(q.matchableWords, compactName)
}

// spellsCompactIdentifier reports whether two or more consecutive words of the sequence
// concatenate to exactly compact. Word boundaries are what keep this honest: a run must
// consume whole words, so "buildGroupIndexEntry" (one word) never spells buildGroupIndex.
func spellsCompactIdentifier(words []string, compact string) bool {
	for start := 0; start+1 < len(words); start++ {
		if !strings.HasPrefix(compact, words[start]) {
			continue
		}
		length := len(words[start])
		for end := start + 1; end < len(words); end++ {
			if length+len(words[end]) > len(compact) ||
				compact[length:length+len(words[end])] != words[end] {
				break
			}
			length += len(words[end])
			if length == len(compact) {
				return true
			}
		}
	}
	return false
}

func compactSearchIdentifier(value string) string {
	var out strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out.WriteRune(unicode.ToLower(r))
		}
	}
	return out.String()
}

func clampRegion(start, end, lineCount int) (int, int) {
	if lineCount <= 0 {
		return 0, 0
	}
	start = maxInt(1, start)
	end = minInt(lineCount, maxInt(start, end))
	if start > lineCount {
		return 0, 0
	}
	return start, end
}

func appendUnique(values []string, additions ...string) []string {
	seen := make(map[string]bool, len(values)+len(additions))
	for _, value := range values {
		seen[value] = true
	}
	for _, value := range additions {
		if value != "" && !seen[value] {
			seen[value] = true
			values = append(values, value)
		}
	}
	return values
}

func (response SearchResponse) Validate() error {
	if actual := serializedSearchResultBytes(response.Results); response.Stats.ResultBytes != actual {
		return fmt.Errorf("search result byte accounting mismatch: %d != %d", response.Stats.ResultBytes, actual)
	}
	if response.Stats.ContextBudgetBytes > 0 && response.Stats.ResultBytes > response.Stats.ContextBudgetBytes {
		return fmt.Errorf("search result context exceeds byte budget: %d > %d", response.Stats.ResultBytes, response.Stats.ContextBudgetBytes)
	}
	for index, result := range response.Results {
		if result.Rank != index+1 {
			return fmt.Errorf("search result rank %d at index %d", result.Rank, index)
		}
		if result.FilePath == "" || result.StartLine < 1 || result.EndLine < result.StartLine || result.FocusLine < result.StartLine || result.FocusLine > result.EndLine || result.SnippetStartLine < result.StartLine || result.SnippetEndLine > result.EndLine || result.SnippetEndLine < result.SnippetStartLine {
			return fmt.Errorf("invalid search result at rank %d", result.Rank)
		}
	}
	return nil
}
