package sem

import (
	"path/filepath"
	"sort"
	"strings"
)

// Covering test
// =============
//
// A snippet says what the code DOES. It does not say what the code is supposed to do, and on a
// defect that turns on a contract — "return false when the extension is unknown", "reply with a
// null ARRAY, not a null bulk string" — the difference between those two is the difference
// between a fix and a plausible-looking wrong patch.
//
// Measured over 295 agentic SWE-bench instances, the instances where a test file happened to
// land in the top ten resolved at 50.9%, against 42.1% where none did. The cleanest single
// tool-assisted win in the run was an instance where the covering test came back at rank 2 and
// its two assertions stated the expected behaviour outright; the agent read them and wrote the
// conditional fix instead of the unconditional one. That was luck: the ranker DEMOTES test
// files, deliberately and correctly, because a test is not a fix site.
//
// This block does not undo that. It is a separate, labelled section that names ONE test — the
// test that exercises the top hit — and prints enough of it to read the contract off. The entry
// is never in the primary list, never a candidate fix site, and never competes with the ranking.
//
// Evidence, strongest first:
//
//	edge        an inbound CALLS/TESTS edge from a symbol in a test file: the graph resolved
//	            that this test executes this code
//	mirror      the test lives in the anchor's mirror test file — `foo_test.go` beside `foo.go`,
//	            `src/test/java/**/FooTest.java` against `src/main/java/**/Foo.java`,
//	            `__tests__/foo.ts`, `test_foo.py`, `foo_spec.rb` — matched by comparing file
//	            STEMS with the test affix stripped, so no per-language path table is needed
//	name/body   the test's own name or body names the anchor
//
// A mirror file alone is not enough to pick a test METHOD out of it, so a mirror candidate still
// has to name the anchor somewhere. Nothing is guessed: when no candidate clears the bar the
// section is simply absent.
const (
	// searchCoveringTestLimit is how many tests the block names. One. The block answers "what
	// is this supposed to do", and a second test rarely says something the first did not while
	// always costing another body.
	searchCoveringTestLimit = 1

	// searchCoveringTestGrowthBytes is the block's whole serialized allowance, in the same
	// currency the rest of search is budgeted in: bytes replayed on every later agent turn.
	//
	// It is sized off the FLOOR, not off a wish. One entry costs ~330 bytes of schema before a
	// single line of source in a Java repo (`gson/src/test/java/com/google/gson/GsonBuilderTest
	// .java` is 54 bytes of path, and the qualified name is another 44), so an allowance of 384
	// measured as "no covering test on any Java or Kotlin instance" — the block silently did not
	// exist wherever paths are long. 448 clears the floor everywhere and buys three or four lines
	// of body where paths are short, which is what the measured win looked like (two setup lines
	// and an assertion).
	searchCoveringTestGrowthBytes = 448

	// searchCoveringTestMaxLines caps the window even when the budget would allow more. Past
	// this a "test" is a fixture-driven harness whose body states no contract.
	searchCoveringTestMaxLines = 12

	// searchCoveringTestCandidateLimit bounds how many candidates are scored. A widely used
	// helper has hundreds of test callers; past this bound the block is the wrong tool and
	// `entire-graph neighbors --relation CALLS --direction in` is the right one.
	searchCoveringTestCandidateLimit = 24

	// searchCoveringTestFileReads bounds files hydrated to score bodies and cut the window: the
	// mention checks below, plus the winner's own file.
	searchCoveringTestFileReads = 6

	// searchCoveringTestMentionChecks bounds how many candidate BODIES are read to look for the
	// anchor's name. Reading a file is the expensive part of this block, and it only ever changes
	// the outcome for a candidate the graph resolved no edge for.
	searchCoveringTestMentionChecks = 4

	// searchCoveringTestNameScanLimit bounds the name-convention scan over test-file symbols. It
	// is a work guard, not a quality knob: past this many symbols the repo's test suite is large
	// enough that the edge and mirror routes have already had every chance.
	searchCoveringTestNameScanLimit = 200000

	// searchCoveringTestSignal marks the entry in the payload's own signal list, so the reason
	// survives every serialization the schema already has.
	searchCoveringTestSignal = "covering-test"
)

// searchCoveringTest is the one test the block names.
type searchCoveringTest struct {
	symbol SymbolRecord
	// line is where the agent should look: the first assertion in the body, or the
	// declaration when the body asserts nothing recognizable.
	line int
	// score orders candidates. See searchCoveringTestScore for what it is made of.
	score float64
}

// searchTestAffixes are the name fragments that mark a file as the test of another file. They
// are stripped from a candidate's stem before it is compared with the anchor's, which is what
// makes mirror detection language-agnostic: `foo_test.go`, `FooTest.java`, `foo.spec.ts`,
// `test_foo.py`, `foo_spec.rb` and `FooTests.cs` all reduce to `foo`.
var searchTestAffixes = []string{
	"_test", "-test", ".test", "test", "_tests", "-tests", ".tests", "tests",
	"_spec", "-spec", ".spec", "spec", "_specs", "-specs", ".specs", "specs",
	"it", "_it", "-it",
}

// searchAssertionMarkers are the substrings that mark a line as stating an expectation. The set
// is deliberately the vocabulary shared by mainstream test frameworks and nothing wider: a
// marker that also matches ordinary code would point the window at the wrong line.
var searchAssertionMarkers = []string{
	"assert", "expect", "should", "verify(", "require.", "require(",
	"t.error", "t.fatal", "throws", "raises", "must_", ".to.", ".tobe", "ok(",
}

// selectSearchCoveringTest picks the test that covers an anchor, or reports that none does.
func selectSearchCoveringTest(
	results []SearchResult,
	anchor SymbolRecord,
	q searchQuery,
	relations []RelationRecord,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	cache *searchRelatedFileCache,
) (searchCoveringTest, bool) {
	if anchor.ID == "" || anchor.Name == "" {
		return searchCoveringTest{}, false
	}
	// A top hit that is ITSELF a test needs no covering test: the caller is already reading
	// one, and the block would name the thing it is looking at.
	if searchTestArtifactPath(searchLowerPath(anchor.FilePath)) {
		return searchCoveringTest{}, false
	}
	// A caller who ASKED for tests already gets them ranked at full strength — the additive
	// test demotion is switched off for exactly these words — so the block would spend its
	// bytes re-stating the primary list. The gate is the same one the ranker uses, so the two
	// can never disagree about what "asked for tests" means.
	if searchQuerySupplied(q, "test", "tests", "testing", "spec", "specs", "fixture", "fixtures") {
		return searchCoveringTest{}, false
	}
	candidates := searchCoveringTestCandidates(anchor, relations, symbolsByID, symbolsByFile)
	if len(candidates) == 0 {
		return searchCoveringTest{}, false
	}
	scored := make([]searchCoveringTest, 0, len(candidates))
	// File hydration is the expensive part of this block, so it is spent in one place only: on
	// the candidates that would otherwise be REJECTED, where reading the body is the difference
	// between an entry and no entry. A candidate the graph already resolved as calling the
	// anchor needs no body evidence — the edge is the evidence — and its focus line is read once,
	// for the winner, after the ordering is decided.
	mentionChecks := 0
	for _, candidate := range candidates {
		mirror := searchCoveringTestMirror(anchor.FilePath, candidate.symbol.FilePath)
		named := searchNameMentions(candidate.symbol.Name, anchor.Name)
		mentions := false
		if !candidate.edge && !named && mirror && mentionChecks < searchCoveringTestMentionChecks {
			mentionChecks++
			if lines, ok := cache.get(candidate.symbol.FilePath); ok {
				mentions = searchCoveringTestBodyMentions(lines, candidate.symbol, anchor.Name)
			}
		}
		score, ok := searchCoveringTestScore(candidate, mirror, named, mentions)
		if !ok {
			continue
		}
		scored = append(scored, searchCoveringTest{
			symbol: candidate.symbol, line: candidate.symbol.StartLine, score: score,
		})
	}
	if len(scored) == 0 {
		return searchCoveringTest{}, false
	}
	sort.SliceStable(scored, func(left, right int) bool {
		if scored[left].score != scored[right].score {
			return scored[left].score > scored[right].score
		}
		if scored[left].symbol.FilePath != scored[right].symbol.FilePath {
			return scored[left].symbol.FilePath < scored[right].symbol.FilePath
		}
		return scored[left].symbol.ID < scored[right].symbol.ID
	})
	best := scored[0]
	lines, readable := cache.get(best.symbol.FilePath)
	if !readable {
		return searchCoveringTest{}, false
	}
	best.line = searchCoveringTestFocusLine(lines, best.symbol)
	// A test the payload already prints is not worth a slot: the caller can read it where it is.
	if searchCoveringTestAlreadySurfaced(results, best) {
		return searchCoveringTest{}, false
	}
	return best, true
}

// searchCoveringTestCandidate is a test symbol under consideration, plus whether the graph
// resolved an edge from it to the anchor.
type searchCoveringTestCandidate struct {
	symbol     SymbolRecord
	edge       bool
	confidence float64
}

// searchCoveringTestCandidates collects test symbols that could be the covering test: everything
// the graph resolved as calling or testing the anchor, plus the callables declared in the
// anchor's mirror test file.
func searchCoveringTestCandidates(
	anchor SymbolRecord,
	relations []RelationRecord,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
) []searchCoveringTestCandidate {
	seen := map[string]int{}
	var candidates []searchCoveringTestCandidate
	admit := func(symbol SymbolRecord, edge bool, confidence float64) {
		if symbol.ID == "" || symbol.ID == anchor.ID || !searchEnclosableSymbolKind(symbol.Kind) {
			return
		}
		if symbol.StartLine <= 0 || symbol.EndLine < symbol.StartLine {
			return
		}
		if !searchTestArtifactPath(searchLowerPath(symbol.FilePath)) &&
			!searchTestNameShaped(symbol.Name) {
			return
		}
		if index, found := seen[symbol.ID]; found {
			if edge && !candidates[index].edge {
				candidates[index].edge = true
				candidates[index].confidence = confidence
			}
			return
		}
		if len(candidates) >= searchCoveringTestCandidateLimit {
			return
		}
		seen[symbol.ID] = len(candidates)
		candidates = append(candidates, searchCoveringTestCandidate{
			symbol: symbol, edge: edge, confidence: confidence,
		})
	}
	for index := range relations {
		relation := &relations[index]
		if relation.ToID != anchor.ID {
			continue
		}
		if !searchRelatedCallRelation(relation.Type) && relation.Type != "TESTS" {
			continue
		}
		if caller, known := symbolsByID[relation.FromID]; known {
			admit(caller, true, relation.Confidence)
		}
	}
	for _, filePath := range searchMirrorTestFiles(anchor.FilePath, symbolsByFile) {
		for _, symbol := range symbolsByFile[filePath] {
			admit(symbol, false, 0)
		}
	}
	// Third route: a test whose own NAME names the anchor, wherever it lives. `TestRouteRedirect
	// FixedPath` covers `redirectFixedPath` from `routes_test.go`, which is neither the mirror of
	// `gin.go` nor a resolved caller — the convention IS the evidence, and it is the route that
	// reaches languages whose call resolution does not see into test harnesses.
	for _, filePath := range searchNamedTestFiles(anchor, symbolsByFile) {
		for _, symbol := range symbolsByFile[filePath] {
			if searchNameMentions(symbol.Name, anchor.Name) {
				admit(symbol, false, 0)
			}
		}
	}
	return candidates
}

// searchNamedTestFiles returns the test files holding a symbol whose name names the anchor,
// sorted. The scan is over test files only and is bounded, and the inner test is a case-folded
// substring check that allocates nothing until it matches — the cheap half of searchNameMentions
// — so the cost is a walk of the symbol table, not a normalization of it.
func searchNamedTestFiles(anchor SymbolRecord, symbolsByFile map[string][]SymbolRecord) []string {
	if len(anchor.Name) < 4 {
		return nil
	}
	var files []string
	examined := 0
	for filePath, symbols := range symbolsByFile {
		if examined >= searchCoveringTestNameScanLimit {
			break
		}
		if filePath == anchor.FilePath || !searchTestArtifactPath(searchLowerPath(filePath)) {
			continue
		}
		for _, symbol := range symbols {
			examined++
			if !searchEnclosableSymbolKind(symbol.Kind) || len(symbol.Name) < len(anchor.Name) {
				continue
			}
			if !searchNameMentions(symbol.Name, anchor.Name) {
				continue
			}
			files = append(files, filePath)
			break
		}
	}
	sort.Strings(files)
	return files
}

// searchMirrorTestFiles returns the indexed test files whose stem is the anchor file's stem with
// a test affix — `foo_test.go` for `foo.go`, `FooTest.java` for `Foo.java`, `foo.spec.ts` for
// `foo.ts`, `test_foo.py` for `foo.py`. Comparing STEMS is what makes this work across
// languages and across layouts (same directory, `src/test/java` mirror, `__tests__/`, `spec/`)
// without a per-language table of path rewrites.
//
// Paths come back sorted: the caller admits candidates up to a cap, and a map's iteration order
// would make WHICH candidates it sees differ between two runs of the same query.
func searchMirrorTestFiles(sourcePath string, symbolsByFile map[string][]SymbolRecord) []string {
	stem := searchFileStem(sourcePath)
	if stem == "" {
		return nil
	}
	var mirrors []string
	for filePath, symbols := range symbolsByFile {
		if filePath == sourcePath || len(symbols) == 0 {
			continue
		}
		if searchCoveringTestMirror(sourcePath, filePath) {
			mirrors = append(mirrors, filePath)
		}
	}
	sort.Strings(mirrors)
	return mirrors
}

// searchFileStem is a path's basename, lowercased, with every extension removed, so
// `Foo.spec.ts` and `foo_test.go` reduce to comparable text.
func searchFileStem(filePath string) string {
	base := strings.ToLower(filepath.Base(filepath.ToSlash(filePath)))
	for {
		ext := filepath.Ext(base)
		if ext == "" || ext == base {
			break
		}
		base = strings.TrimSuffix(base, ext)
	}
	return base
}

// searchStripTestAffix removes one leading or trailing test affix from a stem and reports
// whether it removed anything. `footest` -> `foo`, `test_foo` -> `foo`, `foo` -> not stripped.
func searchStripTestAffix(stem string) (string, bool) {
	if stem == "" {
		return "", false
	}
	longest, found := "", false
	for _, affix := range searchTestAffixes {
		if trimmed, ok := strings.CutSuffix(stem, affix); ok && trimmed != "" {
			if !found || len(affix) > len(longest) {
				longest, found = affix, true
			}
		}
	}
	if found {
		return strings.Trim(strings.TrimSuffix(stem, longest), "_-."), true
	}
	for _, affix := range searchTestAffixes {
		if trimmed, ok := strings.CutPrefix(stem, affix); ok && trimmed != "" {
			return strings.Trim(trimmed, "_-."), true
		}
	}
	return stem, false
}

// searchTestNameShaped reports whether a symbol's own NAME marks it as a test, which is how a
// test declared inside the file it tests (Rust's `#[cfg(test)] mod tests`, a Python module's
// `def test_foo`) is admitted even though its path carries no test marker.
func searchTestNameShaped(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, "test") || strings.HasSuffix(lower, "test") ||
		strings.HasPrefix(lower, "it_") || strings.HasSuffix(lower, "_spec") ||
		strings.HasPrefix(lower, "should")
}

// searchCoveringTestScore rates a candidate, or rejects it.
//
// The bar: a candidate must either be a graph-resolved caller of the anchor, or NAME the anchor
// (in its own name or its body). "It lives in the mirror file" is evidence about the FILE, not
// about which of its twenty methods covers this symbol, so on its own it admits nothing.
func searchCoveringTestScore(
	candidate searchCoveringTestCandidate,
	mirror, named, mentions bool,
) (float64, bool) {
	if !candidate.edge && !named && !mentions {
		return 0, false
	}
	score := 0.0
	if mirror {
		score += 4
	}
	if named {
		score += 3
	}
	if candidate.edge {
		score += 2 + candidate.confidence
	}
	if mentions {
		score += 1
	}
	// A shorter body states its contract more directly and costs fewer bytes to show. The tilt
	// is small on purpose: it separates equals, it does not outvote evidence.
	if span := candidate.symbol.EndLine - candidate.symbol.StartLine + 1; span > 0 {
		score += 1 / float64(span)
	}
	return score, true
}

// searchCoveringTestMirror reports whether a candidate file is the anchor file's mirror test
// file: same stem once the test affix is stripped (`foo_test.go`, `FooTest.java`, `foo.spec.ts`,
// `test_foo.py`), or the SAME stem inside a test tree (`tests/unit/bitops.tcl` for
// `src/bitops.c`, `src/routing/tests/mod.rs` for `src/routing/mod.rs`) — a layout where the
// directory carries the affix the filename does not.
func searchCoveringTestMirror(sourcePath, candidatePath string) bool {
	stem := searchFileStem(sourcePath)
	if stem == "" {
		return false
	}
	candidateStem := searchFileStem(candidatePath)
	if candidateStem == stem {
		return searchTestArtifactPath(searchLowerPath(candidatePath))
	}
	candidate, stripped := searchStripTestAffix(candidateStem)
	return stripped && candidate == stem
}

// searchNameMentions reports whether one identifier names another: `testRegisterTypeAdapterFor
// CoreType` names `registerTypeAdapter`, and `test_shift_timezone` names `shiftTimezone`.
//
// The case-folded substring test runs first and allocates nothing, which matters because this is
// called once per candidate symbol while scanning a repo's test files. The
// separator-insensitive comparison — the one that costs two normalized copies — runs only when a
// separator is actually present to explain a miss.
func searchNameMentions(outer, inner string) bool {
	if len(inner) < 4 || len(outer) < len(inner) {
		return false
	}
	if containsFoldASCII(outer, inner) {
		return true
	}
	if !searchNameHasSeparator(outer) && !searchNameHasSeparator(inner) {
		return false
	}
	needle := searchAlphanumericLower(inner)
	if len(needle) < 4 {
		return false
	}
	return strings.Contains(searchAlphanumericLower(outer), needle)
}

func searchNameHasSeparator(value string) bool {
	return strings.ContainsAny(value, "_-.$")
}

// containsFoldASCII reports whether needle occurs in haystack, ignoring ASCII case, without
// allocating.
func containsFoldASCII(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for offset := 0; offset+len(needle) <= len(haystack); offset++ {
		match := true
		for index := 0; index < len(needle); index++ {
			if lowerASCII(haystack[offset+index]) != lowerASCII(needle[index]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func searchAlphanumericLower(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			builder.WriteByte(character)
		case character >= 'A' && character <= 'Z':
			builder.WriteByte(character + ('a' - 'A'))
		}
	}
	return builder.String()
}

// searchCoveringTestBodyMentions reports whether a candidate's body names the anchor.
func searchCoveringTestBodyMentions(lines []string, symbol SymbolRecord, name string) bool {
	start, end := clampRegion(symbol.StartLine, symbol.EndLine, len(lines))
	if start == 0 {
		return false
	}
	for offset := start; offset <= end; offset++ {
		if containsIdentifierUsage(lines[offset-1], name) {
			return true
		}
	}
	return false
}

// searchCoveringTestFocusLine picks the line the window is built around: the LAST line of the
// body that states an expectation, which is the line that says what the code is supposed to do.
//
// Last, not first. A test's conclusion is at its end, and its first assertion is very often
// setup or teardown — measured: the first `require.` in a Go test was `defer func() {
// require.NoError(t, h.Close()) }()`, and a window centred there showed the agent a teardown.
// Lines inside a deferred or registered cleanup are skipped for the same reason.
//
// Falls back to the declaration when nothing in the body is recognizable as an assertion.
func searchCoveringTestFocusLine(lines []string, symbol SymbolRecord) int {
	start, end := clampRegion(symbol.StartLine, symbol.EndLine, len(lines))
	if start == 0 {
		return symbol.StartLine
	}
	focus := 0
	deferDepth := 0
	for offset := start; offset <= end; offset++ {
		line := lines[offset-1]
		trimmed := strings.TrimSpace(line)
		if deferDepth > 0 {
			deferDepth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
			if deferDepth < 0 {
				deferDepth = 0
			}
			continue
		}
		if searchCleanupLine(trimmed) {
			deferDepth = maxInt(0, strings.Count(trimmed, "{")-strings.Count(trimmed, "}"))
			continue
		}
		lower := strings.ToLower(trimmed)
		for _, marker := range searchAssertionMarkers {
			if strings.Contains(lower, marker) {
				focus = offset
				break
			}
		}
	}
	if focus == 0 {
		return start
	}
	return focus
}

// searchCleanupLine reports whether a line opens deferred or registered teardown. What such a
// block asserts is that the fixture shut down, never what the code under test should do.
func searchCleanupLine(trimmed string) bool {
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "defer ") || strings.HasPrefix(lower, "defer(") ||
		strings.Contains(lower, "cleanup(") || strings.Contains(lower, "aftereach(") ||
		strings.Contains(lower, "teardown")
}

// searchCoveringTestAlreadySurfaced reports whether the payload already prints the covering
// test's focus line, or already carries the symbol itself.
func searchCoveringTestAlreadySurfaced(results []SearchResult, test searchCoveringTest) bool {
	for _, result := range results {
		if result.SymbolID != "" && result.SymbolID == test.symbol.ID {
			return true
		}
		if result.FilePath != test.symbol.FilePath {
			continue
		}
		if result.SnippetStartLine <= test.line && result.SnippetEndLine >= test.line {
			return true
		}
	}
	return false
}

// searchCoveringTestResult renders the block's entry: a location plus the widest window of the
// test's own source, verbatim, that the remaining allowance can hold — centred on the assertion
// so an entry that can only afford three lines spends them on the expectation rather than on
// the setup above it.
//
// Returns false when not even one line fits, which is how "a test exists but the budget is
// exhausted" ends as an absent section rather than as a truncated claim.
func searchCoveringTestResult(test searchCoveringTest, lines []string, budget int) (SearchResult, bool) {
	start, end := clampRegion(test.symbol.StartLine, test.symbol.EndLine, len(lines))
	if start == 0 {
		return SearchResult{}, false
	}
	focus := test.line
	if focus < start || focus > end {
		focus = start
	}
	build := func(from, to int) SearchResult {
		result := SearchResult{
			FilePath:         test.symbol.FilePath,
			StartLine:        start,
			EndLine:          end,
			FocusLine:        focus,
			SnippetStartLine: from,
			SnippetEndLine:   to,
			Kind:             test.symbol.Kind,
			Signals:          []string{searchRelatedSignalPrefix + searchCoveringTestSignal},
			Section:          searchSectionCoveringTest,
			Snippet:          strings.Join(lines[from-1:to], "\n"),
		}
		// One name field, not two. A Java qualified name is ~45 bytes and the plain name is
		// contained in it; the bytes the entry does not spend on saying its name twice are lines
		// of the test body it can afford to show instead.
		if test.symbol.QualifiedName != "" {
			result.QualifiedName = test.symbol.QualifiedName
		} else {
			result.SymbolName = test.symbol.Name
		}
		return result
	}
	limit := minInt(searchCoveringTestMaxLines, end-start+1)
	for span := limit; span >= 1; span-- {
		// Widest window first, and among windows of one width the most balanced one around
		// the assertion: the setup that makes an assertion readable is the line above it.
		from := focus - (span-1)/2
		if from < start {
			from = start
		}
		if from+span-1 > end {
			from = end - span + 1
		}
		candidate := build(from, from+span-1)
		if budget <= 0 || serializedSearchResultBytes([]SearchResult{candidate}) <= budget {
			return candidate, true
		}
	}
	return SearchResult{}, false
}

func searchLowerPath(filePath string) string {
	return "/" + strings.ToLower(filepath.ToSlash(filePath))
}
