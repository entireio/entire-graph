package sem

import (
	"path/filepath"
	"sort"
	"strings"
)

// Related sites
// =============
//
// A ranked list answers "where is it?". It does not answer "where ELSE does this change have to
// land?", and a multi-site fix turns entirely on the second question.
//
// Measured on 31 agentic SWE-bench instances where a no-tool baseline fixed the bug and the
// graph-assisted agent did not DESPITE both finding the right file, the dominant failure was a
// patch applied to one site of several: three sibling methods 24 and 40 lines apart in the same
// file (only the first was patched), the same guard in three sibling classes (only one class
// surfaced), a second hunk inside the top hit's own caller (never surfaced). In every case the
// missing site was ONE graph hop from a hit that DID surface, and in every case it was absent
// from the ranking — because ranking asks a relevance question and this is a structural one.
// Bare `file:line` locators in the tail of the ranking cannot answer it either: they are what
// the agent is left with, and they point at whatever else scored, not at the rest of this change.
//
// The block answers it directly, from relations the graph already holds:
//
//	caller     an incoming CALLS/ASYNC_CALLS edge, reported at the CALL SITE — the line that has
//	           to change when a callee's contract does
//	sibling    a member that stands in the same place as the anchor: an OVERRIDES/IMPLEMENTS edge
//	           to the same supertype member, a same-named member of a sibling or derived
//	           container, or — when the anchor's declaration unit is small enough that "the rest
//	           of this unit" is a specific answer — the members declared beside it
//	near-dupe  a near-duplicate body: the graph's own SIMILAR_TO clone edges, plus the anchor's
//	           own file, where a clone family is normally written as adjacent members
//
// It is not a second ranking and never competes with the first one. Entries are locators, they
// are filtered by the ranker's OWN priors so the block cannot recommend a file the ranking has
// already ruled out as a fix site, and they are funded out of the tail of the ranking rather
// than out of new bytes: see mergeSearchRelatedSites.
//
// Deliberately NOT included: outgoing CALLS. A callee is a plausible second site, but adding it
// as a fourth kind costs a slot in every rotation and the cases it reaches (the next stage of a
// pipeline, declared beside its caller) are already reached as co-members of the same unit.
const (
	// searchRelatedSiteLimit bounds the block. Four is one slot per kind plus one, so the
	// commonest multi-site shape — a two-member clone family, or a caller alongside its
	// callee's twin — fits whole while no single kind can monopolize the block.
	searchRelatedSiteLimit = 4

	// searchRelatedCandidateLimit bounds how many candidates of one kind are considered, and
	// with them how many extra files may be hydrated to order siblings. Past this the anchor is
	// a repo-wide interface with dozens of implementations, the block is the wrong tool for it,
	// and `entire-graph impact` is the right one.
	searchRelatedCandidateLimit = 12

	// searchRelatedUnitMemberLimit is how small a declaration unit — a class, a trait, a file's
	// top level — has to be for "the members declared beside the anchor" to be a specific
	// answer rather than a lottery. A 6-method value class or a 4-function Go file names the
	// change's other site; a 150-method utility class or a 60-function module names nothing, so
	// past this bound the co-member route switches off entirely and the anchor keeps only the
	// routes that carry real evidence (an edge, a shared name, a duplicated body).
	searchRelatedUnitMemberLimit = 12

	// maxSearchRelatedScopedRelations bounds the prefiltered relation slice. It is a memory
	// guard for a supertype with thousands of implementors, not a quality knob: the candidate
	// limits above already decide what can reach the block.
	maxSearchRelatedScopedRelations = 4096
)

// searchRelatedKind is why a site is related: it is reported verbatim to the caller, because
// "the same change probably also belongs here" means something different for a call site than it
// does for a copy-pasted body.
type searchRelatedKind string

const (
	searchRelatedCaller   searchRelatedKind = "caller"
	searchRelatedSibling  searchRelatedKind = "sibling"
	searchRelatedNearDupe searchRelatedKind = "near-dupe"
)

// Evidence tiers inside the sibling kind. A shared name across an inheritance relation is a
// statement by the code that two members do the same job; being declared in the same small unit
// is only a statement that they belong together.
const (
	searchRelatedEvidenceCoMember = 1
	searchRelatedEvidenceNamed    = 2
)

// searchRelatedSite is one place the anchor's change is likely to have to land too.
type searchRelatedSite struct {
	kind   searchRelatedKind
	symbol SymbolRecord
	// line is where the agent should look: the call site for a caller, the declaration for a
	// sibling or a near-duplicate body.
	line int
	// evidence tiers candidates inside one kind (see the constants above).
	evidence int
	// strength is kind-specific evidence: edge confidence for a caller, body similarity to the
	// anchor for a sibling or a near-duplicate. It orders candidates WITHIN a kind and is never
	// compared across kinds, where it measures different things.
	strength float64
	// sameFile marks a candidate in the anchor's own file. Clone families and variant sets are
	// normally written as adjacent members, so a same-file candidate is the likelier second site
	// even when a cross-file twin scores a shade higher.
	sameFile bool
	// proximity is the distance in lines from the anchor's declaration, used only to break ties
	// among same-file candidates that similarity cannot separate.
	proximity int
}

// searchRelatedCallRelation reports whether a relation type is "X's code runs Y", which is the
// only kind of inbound edge whose source line a change to Y can force to change.
func searchRelatedCallRelation(relation string) bool {
	switch relation {
	case "CALLS", "ASYNC_CALLS":
		return true
	}
	return false
}

// searchRelatedOverrideRelation reports whether a relation type links a member to the supertype
// member it stands in for. Two members overriding the SAME supertype member are the sibling
// implementations of one contract, which is why this is the sibling relation and EXTENDS is not.
func searchRelatedOverrideRelation(relation string) bool {
	switch relation {
	case "OVERRIDES", "IMPLEMENTS":
		return true
	}
	return false
}

// searchRelatedInheritRelation reports whether a relation type links two CONTAINERS by
// inheritance. It is the fallback route to siblings for languages whose provider emits
// inheritance between types but no member-level OVERRIDES edges.
func searchRelatedInheritRelation(relation string) bool {
	switch relation {
	case "EXTENDS", "INHERITS", "IMPLEMENTS":
		return true
	}
	return false
}

// searchRelatedSiteEditable reports whether a fix could land in a candidate's file, reusing both
// of the RANKER's own priors: the multiplicative file-class prior (docs, vendored, generated,
// data, examples) and the additive test-artifact demotion. Reuse is the point — a block that
// invented its own taxonomy could recommend a file the ranking had already ruled out.
//
// Here the priors are a FILTER, not a tilt, which is the one place this block deliberately
// differs from the ranking. The ranking demotes such a file but keeps it, because it may be the
// only match there is; this block has four slots and exists to name the OTHER places the change
// lands, so a slot spent on a unit test that merely calls the anchor is a slot spent on a file
// no patch will touch. (Measured: without the filter, a Java anchor's four slots went entirely
// to `src/test/java/**` callers.) A query that asks for tests, docs or config switches the
// corresponding prior off, exactly as it does for ranking.
func searchRelatedSiteEditable(q searchQuery, filePath string) bool {
	if searchFileClassPrior(q, filePath) < 1 {
		return false
	}
	lower := "/" + strings.ToLower(filepath.ToSlash(filePath))
	return !searchTestArtifactPath(lower) || searchQuerySupplied(q,
		"test", "tests", "testing", "spec", "specs", "fixture", "fixtures",
	)
}

// searchRelatedFileCache hydrates file content once per search through the shared reader, so
// ordering candidates by body similarity re-reads nothing the ranker already read.
type searchRelatedFileCache struct {
	read  contentReader
	lines map[string][]string
	files int
	limit int
}

func newSearchRelatedFileCache(read contentReader, limit int) *searchRelatedFileCache {
	return &searchRelatedFileCache{read: read, lines: map[string][]string{}, limit: limit}
}

// get returns a file's lines, or false when the file is unreadable, binary, or beyond the
// cache's hydration allowance. The allowance is what keeps the block's IO bounded on a symbol
// with dozens of implementations.
func (cache *searchRelatedFileCache) get(path string) ([]string, bool) {
	if cache == nil || cache.read == nil {
		return nil, false
	}
	if lines, cached := cache.lines[path]; cached {
		return lines, lines != nil
	}
	if cache.limit > 0 && cache.files >= cache.limit {
		return nil, false
	}
	cache.files++
	content, ok := cache.read(path)
	if !ok || strings.IndexByte(content, 0) >= 0 {
		cache.lines[path] = nil
		return nil, false
	}
	lines := strings.Split(content, "\n")
	cache.lines[path] = lines
	return lines, true
}

// searchRelatedBodyTokens returns the near-duplicate token set of a symbol's own source, or nil
// when the body is too small for a similarity comparison to mean anything. It reuses the
// near-duplicate collapse's normalization and size floor so "near-duplicate" means exactly one
// thing in this package.
func searchRelatedBodyTokens(lines []string, symbol SymbolRecord) map[string]struct{} {
	if len(lines) == 0 || symbol.StartLine <= 0 {
		return nil
	}
	if symbol.EndLine-symbol.StartLine+1 > defaultSearchEnclosureMaxLines {
		return nil
	}
	text := normalizeSearchDuplicateText(symbolBlockFromLines(lines, symbol))
	if len(text) < searchNearDuplicateMinChars {
		return nil
	}
	return searchDuplicateTokens(text)
}

// searchRelatedBodySimilarity estimates how alike two bodies are, as exact token-set Jaccard
// over files the anchor's neighbourhood already needed. The graph's repo-wide SIMILAR_TO edges
// use MinHash over token SHINGLES, which systematically under-detects short bodies (a three-line
// method whose only difference is its own name loses ~30% of its shingles and falls below the
// clone bar), so a same-file clone family of small methods — the commonest multi-site fix there
// is — has no edge to surface. The BAR is the same in both places: similarThreshold. Only the
// estimator differs, and it is affordable here because the scope is one anchor's neighbourhood.
func searchRelatedBodySimilarity(anchorTokens map[string]struct{}, lines []string, symbol SymbolRecord) (float64, bool) {
	if len(anchorTokens) == 0 {
		return 0, false
	}
	tokens := searchRelatedBodyTokens(lines, symbol)
	if len(tokens) == 0 {
		return 0, false
	}
	score := searchTokenJaccard(anchorTokens, tokens)
	return score, score >= similarThreshold
}

// searchRelatedAnchors picks the symbols the block is about: the head of the primary list.
//
// Not just rank 1. The same measurement that sized the complete-body head (search_enclosure.go)
// found the top hit is the file the agent edits 35% of the time and one of the first three 46%
// of the time, so a block computed only from rank 1 describes the wrong neighbourhood on most
// searches. The head is also exactly the depth an agent reads before deciding, which makes it
// the honest scope: the block covers the neighbourhood of what the caller is going to read.
//
// Docs-and-fixtures hits are skipped — they are not fix sites, so their neighbourhood is not the
// neighbourhood of the change — and each distinct symbol anchors at most once.
func searchRelatedAnchors(
	results []SearchResult,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	limit int,
) []SymbolRecord {
	if limit <= 0 {
		return nil
	}
	seen := make(map[string]bool, limit)
	anchors := make([]SymbolRecord, 0, limit)
	for _, result := range results {
		if len(anchors) == limit {
			break
		}
		if result.Section != searchSectionPrimary {
			continue
		}
		symbol, ok := enclosingCallableForResult(result, symbolsByID, symbolsByFile)
		if !ok || symbol.ID == "" || seen[symbol.ID] {
			continue
		}
		seen[symbol.ID] = true
		anchors = append(anchors, symbol)
	}
	return anchors
}

// scopeSearchRelatedRelations prefilters the snapshot's relations down to the ones that can
// possibly produce a related site, in two passes over the full slice regardless of how many
// anchors there are: first everything touching an anchor or an anchor's container, then — for
// the supertype members and supertypes those reveal — everything else that stands in the same
// place. Per-anchor selection then runs over a slice of tens of records instead of the whole
// graph.
func scopeSearchRelatedRelations(anchors []SymbolRecord, relations []RelationRecord) []RelationRecord {
	ids := make(map[string]bool, len(anchors))
	containers := make(map[string]bool, len(anchors))
	for _, anchor := range anchors {
		ids[anchor.ID] = true
		if anchor.ContainerID != "" {
			containers[anchor.ContainerID] = true
		}
	}
	var scoped []RelationRecord
	supers := map[string]bool{}
	for index := range relations {
		relation := &relations[index]
		if !ids[relation.FromID] && !ids[relation.ToID] &&
			!containers[relation.FromID] && !containers[relation.ToID] {
			continue
		}
		if len(scoped) < maxSearchRelatedScopedRelations {
			scoped = append(scoped, *relation)
		}
		if searchRelatedOverrideRelation(relation.Type) && ids[relation.FromID] {
			supers[relation.ToID] = true
		}
		if searchRelatedInheritRelation(relation.Type) && containers[relation.FromID] {
			supers[relation.ToID] = true
		}
	}
	if len(supers) == 0 {
		return scoped
	}
	for index := range relations {
		relation := &relations[index]
		if len(scoped) >= maxSearchRelatedScopedRelations {
			break
		}
		if !supers[relation.ToID] || ids[relation.FromID] || containers[relation.FromID] {
			continue
		}
		if !searchRelatedOverrideRelation(relation.Type) && !searchRelatedInheritRelation(relation.Type) {
			continue
		}
		scoped = append(scoped, *relation)
	}
	return scoped
}

// selectSearchRelatedSites picks the related-site block for the head of a ranking. It is a pure
// read of the snapshot's relations plus the head's own neighbourhood of files; it builds no
// index and adds no analysis pass to the search.
func selectSearchRelatedSites(
	results []SearchResult,
	anchors []SymbolRecord,
	q searchQuery,
	relations []RelationRecord,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	read contentReader,
	limit int,
) []searchRelatedSite {
	if len(anchors) == 0 || limit <= 0 {
		return nil
	}
	scoped := scopeSearchRelatedRelations(anchors, relations)
	cache := newSearchRelatedFileCache(read, searchRelatedCandidateLimit)
	var callers, siblings, dupes []searchRelatedSite
	anchored := make(map[string]bool, len(anchors))
	for _, anchor := range anchors {
		anchored[anchor.ID] = true
	}
	for _, anchor := range anchors {
		var anchorTokens map[string]struct{}
		if lines, ok := cache.get(anchor.FilePath); ok {
			anchorTokens = searchRelatedBodyTokens(lines, anchor)
		}
		callers = append(callers, searchRelatedCallerSites(anchor, q, scoped, symbolsByID)...)
		siblings = append(siblings, searchRelatedSiblingSites(
			anchor, q, scoped, symbolsByID, symbolsByFile, cache, anchorTokens,
		)...)
		dupes = append(dupes, searchRelatedNearDuplicateSites(
			anchor, q, scoped, symbolsByID, symbolsByFile, cache, anchorTokens,
		)...)
	}
	// Candidates the payload ALREADY points at are removed here, before slots are handed out
	// rather than while entries are rendered. Filtering later would let a candidate the caller
	// can already see consume a slot and take the block's fourth entry with it — measured: an
	// anchor's clone twin was already ranked, so its slot was spent on nothing and the twin of
	// the NEXT anchor (a gold fix site) never made the block.
	drop := func(sites []searchRelatedSite) []searchRelatedSite {
		out := sites[:0]
		for _, site := range sites {
			if anchored[site.symbol.ID] {
				continue
			}
			if searchRelatedSiteAlreadySurfaced(results, site, site.line) {
				continue
			}
			out = append(out, site)
		}
		return out
	}
	// Draw order across kinds, strongest claim on the change first: a near-duplicate body needs
	// the IDENTICAL edit (that is what makes it a duplicate), a sibling implementation usually
	// needs the same edit expressed in its own terms, and a caller only needs to be adjusted to
	// a changed contract. The rotation still guarantees every kind a slot before any kind gets a
	// second one; this only decides who gets the block's spare slot.
	return interleaveSearchRelatedSites(limit, drop(dupes), drop(siblings), drop(callers))
}

// searchRelatedCallerSites collects one anchor's immediate callers, reported at the call site
// rather than at the caller's declaration: the line an agent has to look at when a callee's
// contract changes is the line that calls it.
func searchRelatedCallerSites(
	anchor SymbolRecord,
	q searchQuery,
	relations []RelationRecord,
	symbolsByID map[string]SymbolRecord,
) []searchRelatedSite {
	var sites []searchRelatedSite
	for index := range relations {
		relation := &relations[index]
		if relation.ToID != anchor.ID || !searchRelatedCallRelation(relation.Type) {
			continue
		}
		caller, known := symbolsByID[relation.FromID]
		if !known || caller.ID == anchor.ID || !searchRelatedSiteEditable(q, caller.FilePath) {
			continue
		}
		sites = append(sites, searchRelatedSite{
			kind:     searchRelatedCaller,
			symbol:   caller,
			line:     searchRelatedCallSiteLine(*relation, caller),
			strength: relation.Confidence,
		})
	}
	return orderSearchRelatedSites(anchor, sites)
}

// searchRelatedCallSiteLine resolves the call site from the relation's own evidence, falling back
// to the caller's declaration when the provider recorded none.
func searchRelatedCallSiteLine(relation RelationRecord, caller SymbolRecord) int {
	for _, evidence := range relation.Evidence {
		if evidence.StartLine <= 0 {
			continue
		}
		if evidence.FilePath != "" && evidence.FilePath != caller.FilePath {
			continue
		}
		if caller.StartLine > 0 && caller.EndLine >= caller.StartLine &&
			(evidence.StartLine < caller.StartLine || evidence.StartLine > caller.EndLine) {
			continue
		}
		return evidence.StartLine
	}
	return caller.StartLine
}

// searchRelatedSiblingSites collects members that stand in the same place as the anchor.
//
// Three routes, in decreasing directness of what the graph states:
//
//  1. member level — the anchor OVERRIDES/IMPLEMENTS some supertype member, so everything else
//     overriding that same member is a sibling implementation of one contract; and anything that
//     overrides the ANCHOR is a derived implementation of it.
//  2. container level — for providers that emit inheritance between types but no member-level
//     edges: the anchor's container's supertypes, their other subtypes, and the container's own
//     subtypes each contribute their same-named member.
//  3. co-members — the members declared beside the anchor in its own unit, admitted only when
//     that unit is small enough for "the rest of it" to be a specific answer.
func searchRelatedSiblingSites(
	anchor SymbolRecord,
	q searchQuery,
	relations []RelationRecord,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	cache *searchRelatedFileCache,
	anchorTokens map[string]struct{},
) []searchRelatedSite {
	if anchor.Name == "" {
		return nil
	}
	supermembers := map[string]bool{}
	supertypes := map[string]bool{}
	containers := map[string]bool{}
	var sites []searchRelatedSite
	add := func(symbol SymbolRecord, evidence int) {
		if symbol.ID == "" || symbol.ID == anchor.ID || !searchEnclosableSymbolKind(symbol.Kind) {
			return
		}
		if !searchRelatedSiteEditable(q, symbol.FilePath) {
			return
		}
		sites = append(sites, searchRelatedSite{
			kind:     searchRelatedSibling,
			symbol:   symbol,
			line:     symbol.StartLine,
			evidence: evidence,
		})
	}
	for index := range relations {
		relation := &relations[index]
		switch {
		case searchRelatedOverrideRelation(relation.Type) && relation.FromID == anchor.ID:
			supermembers[relation.ToID] = true
		case searchRelatedOverrideRelation(relation.Type) && relation.ToID == anchor.ID:
			if symbol, known := symbolsByID[relation.FromID]; known {
				add(symbol, searchRelatedEvidenceNamed)
			}
		case anchor.ContainerID != "" && searchRelatedInheritRelation(relation.Type):
			if relation.FromID == anchor.ContainerID {
				supertypes[relation.ToID] = true
				containers[relation.ToID] = true
			} else if relation.ToID == anchor.ContainerID {
				containers[relation.FromID] = true
			}
		}
	}
	if len(supermembers) > 0 || len(supertypes) > 0 {
		for index := range relations {
			relation := &relations[index]
			if searchRelatedOverrideRelation(relation.Type) && supermembers[relation.ToID] &&
				relation.FromID != anchor.ID {
				if symbol, known := symbolsByID[relation.FromID]; known {
					add(symbol, searchRelatedEvidenceNamed)
				}
				continue
			}
			if searchRelatedInheritRelation(relation.Type) && supertypes[relation.ToID] &&
				relation.FromID != anchor.ContainerID {
				containers[relation.FromID] = true
			}
		}
	}
	for _, containerID := range sortedSearchRelatedIDs(containers) {
		container, known := symbolsByID[containerID]
		if !known {
			continue
		}
		for _, member := range symbolsByFile[container.FilePath] {
			if member.ContainerID != containerID || member.Name != anchor.Name {
				continue
			}
			add(member, searchRelatedEvidenceNamed)
		}
	}
	// The supertype member the anchor stands in for is itself a site: a contract change lands on
	// the default implementation as often as on the overrides.
	for _, memberID := range sortedSearchRelatedIDs(supermembers) {
		if symbol, known := symbolsByID[memberID]; known {
			add(symbol, searchRelatedEvidenceNamed)
		}
	}
	for _, member := range searchRelatedUnitCoMembers(anchor, symbolsByFile) {
		add(member, searchRelatedEvidenceCoMember)
	}
	sites = orderSearchRelatedSites(anchor, sites)
	return scoreSearchRelatedSimilarity(sites, cache, anchorTokens)
}

// searchRelatedUnitCoMembers returns the callables declared beside the anchor in its own
// declaration unit — the same container, or the same file's top level when the anchor has no
// container — or nothing at all when that unit has more members than
// searchRelatedUnitMemberLimit.
func searchRelatedUnitCoMembers(anchor SymbolRecord, symbolsByFile map[string][]SymbolRecord) []SymbolRecord {
	var members []SymbolRecord
	for _, member := range symbolsByFile[anchor.FilePath] {
		if member.ContainerID != anchor.ContainerID || !searchEnclosableSymbolKind(member.Kind) {
			continue
		}
		if member.ID == anchor.ID {
			continue
		}
		members = append(members, member)
		if len(members) > searchRelatedUnitMemberLimit {
			return nil
		}
	}
	return members
}

// searchRelatedNearDuplicateSites collects near-duplicate bodies: the graph's own SIMILAR_TO
// clone edges (repo-wide), plus the anchor's own file, where clone families are normally written
// as adjacent members and are usually too short for the repo-wide estimator to catch.
func searchRelatedNearDuplicateSites(
	anchor SymbolRecord,
	q searchQuery,
	relations []RelationRecord,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	cache *searchRelatedFileCache,
	anchorTokens map[string]struct{},
) []searchRelatedSite {
	var sites []searchRelatedSite
	add := func(symbol SymbolRecord, strength float64) {
		if symbol.ID == "" || symbol.ID == anchor.ID || !searchEnclosableSymbolKind(symbol.Kind) {
			return
		}
		if !searchRelatedSiteEditable(q, symbol.FilePath) {
			return
		}
		sites = append(sites, searchRelatedSite{
			kind:     searchRelatedNearDupe,
			symbol:   symbol,
			line:     symbol.StartLine,
			strength: strength,
		})
	}
	for index := range relations {
		relation := &relations[index]
		if relation.Type != "SIMILAR_TO" {
			continue
		}
		twin := ""
		switch anchor.ID {
		case relation.FromID:
			twin = relation.ToID
		case relation.ToID:
			twin = relation.FromID
		default:
			continue
		}
		if symbol, known := symbolsByID[twin]; known {
			add(symbol, relation.Confidence)
		}
	}
	if len(anchorTokens) > 0 {
		if lines, ok := cache.get(anchor.FilePath); ok {
			for _, member := range symbolsByFile[anchor.FilePath] {
				if member.ID == anchor.ID || !searchEnclosableSymbolKind(member.Kind) {
					continue
				}
				if score, similar := searchRelatedBodySimilarity(anchorTokens, lines, member); similar {
					add(member, score)
				}
			}
		}
	}
	return orderSearchRelatedSites(anchor, sites)
}

// scoreSearchRelatedSimilarity fills in body similarity to the anchor for candidates that have
// none yet and re-orders them. A sibling that closely resembles the body being changed is far
// more likely to need the same change than one that merely shares its name or its unit, so
// similarity — where it is affordable to measure — is the ordering signal, and the relation
// itself is only the admission test.
func scoreSearchRelatedSimilarity(
	sites []searchRelatedSite,
	cache *searchRelatedFileCache,
	anchorTokens map[string]struct{},
) []searchRelatedSite {
	if len(anchorTokens) == 0 || len(sites) == 0 {
		return sites
	}
	for index := range sites {
		if sites[index].strength > 0 {
			continue
		}
		lines, ok := cache.get(sites[index].symbol.FilePath)
		if !ok {
			continue
		}
		if score, _ := searchRelatedBodySimilarity(anchorTokens, lines, sites[index].symbol); score > 0 {
			sites[index].strength = score
		}
	}
	sort.SliceStable(sites, func(left, right int) bool {
		return searchRelatedSiteLess(sites[left], sites[right])
	})
	return sites
}

// orderSearchRelatedSites deduplicates one kind's candidates for one anchor by symbol and puts
// them in the order the block will spend its slots in.
func orderSearchRelatedSites(anchor SymbolRecord, sites []searchRelatedSite) []searchRelatedSite {
	if len(sites) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(sites))
	unique := make([]searchRelatedSite, 0, len(sites))
	for _, site := range sites {
		if seen[site.symbol.ID] {
			continue
		}
		seen[site.symbol.ID] = true
		site.sameFile = site.symbol.FilePath == anchor.FilePath
		site.proximity = site.symbol.StartLine - anchor.StartLine
		if site.proximity < 0 {
			site.proximity = -site.proximity
		}
		unique = append(unique, site)
	}
	sort.SliceStable(unique, func(left, right int) bool {
		return searchRelatedSiteLess(unique[left], unique[right])
	})
	if len(unique) > searchRelatedCandidateLimit {
		unique = unique[:searchRelatedCandidateLimit]
	}
	return unique
}

func searchRelatedSiteLess(left, right searchRelatedSite) bool {
	if left.evidence != right.evidence {
		return left.evidence > right.evidence
	}
	if left.sameFile != right.sameFile {
		return left.sameFile
	}
	if left.strength != right.strength {
		return left.strength > right.strength
	}
	if left.proximity != right.proximity {
		return left.proximity < right.proximity
	}
	if left.symbol.FilePath != right.symbol.FilePath {
		return left.symbol.FilePath < right.symbol.FilePath
	}
	if left.line != right.line {
		return left.line < right.line
	}
	return left.symbol.ID < right.symbol.ID
}

// interleaveSearchRelatedSites spends the block's slots by rotating through the kinds rather than
// by draining them in order. A symbol with twelve callers would otherwise fill the whole block
// with call sites and hide the one copy-pasted twin that also has to change, so every kind that
// has a candidate is guaranteed a slot before any kind gets a second one.
func interleaveSearchRelatedSites(limit int, kinds ...[]searchRelatedSite) []searchRelatedSite {
	if limit <= 0 {
		return nil
	}
	cursors := make([]int, len(kinds))
	seen := map[string]bool{}
	out := make([]searchRelatedSite, 0, limit)
	for progress := true; progress && len(out) < limit; {
		progress = false
		for index := range kinds {
			if len(out) >= limit {
				break
			}
			for cursors[index] < len(kinds[index]) {
				site := kinds[index][cursors[index]]
				cursors[index]++
				if seen[site.symbol.ID] {
					continue
				}
				seen[site.symbol.ID] = true
				out = append(out, site)
				progress = true
				break
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sortedSearchRelatedIDs(ids map[string]bool) []string {
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	if len(out) > searchRelatedCandidateLimit {
		out = out[:searchRelatedCandidateLimit]
	}
	return out
}

// searchRelatedResult renders one related site as a payload entry: a locator carrying the
// symbol's name, its true span, and the one line the agent should look at, verbatim from the
// file, so the block can be acted on without a second call.
//
// It is deliberately the CHEAPEST entry the schema allows. Everything a locator does not need is
// left out — no symbol id (a stable id is ~110 bytes in Java and the name is what a follow-up
// query takes), no language, and only one of the two name fields. The block is funded out of the
// tail of the ranking, so every byte an entry does not spend is a ranked hit that survives.
func searchRelatedResult(site searchRelatedSite, lines []string) (SearchResult, bool) {
	start, end := clampRegion(site.symbol.StartLine, site.symbol.EndLine, len(lines))
	if start == 0 {
		return SearchResult{}, false
	}
	focus := site.line
	if focus < start || focus > end {
		focus = start
	}
	result := SearchResult{
		FilePath:         site.symbol.FilePath,
		StartLine:        start,
		EndLine:          end,
		FocusLine:        focus,
		SnippetStartLine: focus,
		SnippetEndLine:   focus,
		Kind:             site.symbol.Kind,
		Signals:          []string{searchRelatedSignalPrefix + string(site.kind)},
		Section:          searchSectionRelated,
		Snippet:          lines[focus-1],
	}
	if site.symbol.QualifiedName != "" {
		result.QualifiedName = site.symbol.QualifiedName
	} else {
		result.SymbolName = site.symbol.Name
	}
	return result, true
}

// searchRelatedSiteAlreadySurfaced reports whether the ranked payload already points the agent at
// this site — same symbol, or a snippet that already prints the line. Re-listing a hit the caller
// can already see would spend a slot on nothing.
func searchRelatedSiteAlreadySurfaced(results []SearchResult, site searchRelatedSite, line int) bool {
	for _, result := range results {
		if result.SymbolID != "" && result.SymbolID == site.symbol.ID {
			return true
		}
		if result.FilePath != site.symbol.FilePath {
			continue
		}
		if result.SnippetStartLine <= line && result.SnippetEndLine >= line {
			return true
		}
	}
	return false
}

// mergeSearchRelatedSites attaches the related-site block to a ranked payload, paid for out of
// the tail of the ranking.
//
// Contract:
//   - The payload never grows: the merged result is at most as large, serialized, as the ranking
//     it replaces (and never past hardBudget). Related-site entries are cheap enough that a tail
//     locator normally funds one outright.
//   - A related site costs at most ONE ranked hit. Without that cap a block of four could pay for
//     itself by dropping five or six tail hits, which trades away recall the ranking had already
//     earned; with it, the block can only ever be an exchange of like for like.
//   - The head is never displaced. searchEnclosureHeadRanks is where the allocator already stops
//     spending on bodies because that is as deep as an agent reads before deciding; the same line
//     is where displacement stops.
//   - The most sites wins, then the fewest displaced hits: a plan that keeps a ranked hit for
//     free is strictly better than one that drops it for nothing.
//   - Ranks are renumbered so the payload keeps its 1..N invariant.
func mergeSearchRelatedSites(
	results []SearchResult,
	sites []searchRelatedSite,
	read contentReader,
	hardBudget int,
) ([]SearchResult, int) {
	if len(results) == 0 || len(sites) == 0 {
		return results, 0
	}
	baseline := serializedSearchResultBytes(results)
	budget := baseline
	if hardBudget > 0 && hardBudget < budget {
		budget = hardBudget
	}
	cache := newSearchRelatedFileCache(read, searchRelatedCandidateLimit)
	entries := make([]SearchResult, 0, len(sites))
	for _, site := range sites {
		lines, ok := cache.get(site.symbol.FilePath)
		if !ok {
			continue
		}
		entry, ok := searchRelatedResult(site, lines)
		if !ok {
			continue
		}
		if searchRelatedSiteAlreadySurfaced(results, site, entry.FocusLine) {
			continue
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return results, 0
	}
	floor := minInt(len(results), searchEnclosureHeadRanks)
	order := searchRelatedDisplacementOrder(results, floor)
	for count := len(entries); count >= 1; count-- {
		for drop := 0; drop <= minInt(count, len(order)); drop++ {
			merged := searchRelatedMergedPlan(results, entries[:count], order[:drop])
			if serializedSearchResultBytes(merged) <= budget {
				return merged, count
			}
		}
	}
	return results, 0
}

// searchRelatedMergedPlan builds one merged payload: the ranking minus the displaced indices,
// then the block, renumbered 1..N.
func searchRelatedMergedPlan(results, entries []SearchResult, displaced []int) []SearchResult {
	dropped := make(map[int]bool, len(displaced))
	for _, index := range displaced {
		dropped[index] = true
	}
	merged := make([]SearchResult, 0, len(results)-len(dropped)+len(entries))
	for index, result := range results {
		if !dropped[index] {
			merged = append(merged, result)
		}
	}
	merged = append(merged, entries...)
	for index := range merged {
		merged[index].Rank = index + 1
	}
	return merged
}

// searchRelatedDisplacementOrder returns the ranked hits that may be displaced to fund the block:
// below the protected head, lowest-ranked first, and ONLY hits whose file the payload still shows
// somewhere else.
//
// What the tail is worth to an agent is the FILES it names. A second or third fragment of a file
// the payload already shows adds a line number; the last mention of a file is the agent's only
// route to it. Search already spends budget on exactly that distinction (MaxRegionsPerFile,
// selectDiverseCandidates), so the block is not allowed to buy structural coverage with file
// coverage — the last mention of a file is not for sale at any price, and a block that cannot be
// funded out of redundant fragments is simply smaller. (Measured: without this rule one instance
// lost the only mention of its gold file, which sat at rank 9 behind a duplicate fragment of an
// already-shown file at rank 7; with it, file-level recall across the probe is unchanged.)
func searchRelatedDisplacementOrder(results []SearchResult, floor int) []int {
	counts := make(map[string]int, len(results))
	for _, result := range results {
		counts[result.FilePath]++
	}
	order := make([]int, 0, len(results))
	for index := len(results) - 1; index >= floor; index-- {
		if counts[results[index].FilePath] <= 1 {
			continue
		}
		counts[results[index].FilePath]--
		order = append(order, index)
	}
	return order
}
