package sem

import (
	"fmt"
	"strings"
	"testing"
)

// A snapshot or golden file is a machine-written recording of expected output. It belongs in the
// section that carries its name — docs-and-fixtures — and not in the PRIMARY list an agent reads as
// its set of candidate fix sites, where it also anchors related-site expansion on a neighbourhood
// that is not the change's.
func TestSearchDocsSectionClaimsFixtureArtifacts(t *testing.T) {
	t.Parallel()
	q := buildSearchQuery("rule SIM201 reports the wrong message")
	for _, path := range []string{
		"crates/ruff/src/snapshots/rules__SIM201.py.snap",
		"internal/pkg/testdata/report.golden",
		"tests/__snapshots__/render.ambr",
	} {
		if !searchDocsSectionPath(q, path) {
			t.Fatalf("%s was left in the primary fix-site list", path)
		}
	}
	// Asking for the artifact keeps it primary, exactly as every other class prior behaves.
	if searchDocsSectionPath(buildSearchQuery("update the snapshot"), "tests/__snapshots__/render.ambr") {
		t.Fatal("a query that asked for snapshots had its snapshot labelled away")
	}
	// A real source file is untouched.
	if searchDocsSectionPath(q, "crates/ruff/src/rules/ast_unary_op.rs") {
		t.Fatal("a source file was labelled into docs-and-fixtures")
	}
}

// "expected X, got Y" is the most common sentence in a bug report. Treating "expected" as a request
// FOR fixtures switched the fixture prior off on exactly the ordinary defect queries it exists to
// correct, so snapshots kept full strength and could outrank the implementation again.
func TestSearchFixturePriorSurvivesTheWordExpected(t *testing.T) {
	t.Parallel()
	path := "tests/__snapshots__/render.ambr"
	bug := buildSearchQuery("expected a 200 response but the handler returns 500")
	if prior := searchFileClassPrior(bug, path); prior != searchSecondaryClassPrior {
		t.Fatalf("fixture prior on a bug query = %v, want %v", prior, searchSecondaryClassPrior)
	}
	// Naming the artifact still switches the prior off.
	asked := buildSearchQuery("regenerate the snapshot for the renderer")
	if prior := searchFileClassPrior(asked, path); prior != 1 {
		t.Fatalf("fixture prior when the query asked for snapshots = %v, want 1", prior)
	}
}

// The prose basenames are matched as a PREFIX, and a prefix of a filename is not a filename:
// `license_check.go`, `history_store.py` and `readme_parser.ts` are ordinary executable sources.
// Classifying them as documentation halved their score AND — because NonProgramTextPath consumes
// the same classification — declared them unable to hold a relation.
func TestSearchDocClassLeavesSourceNamedLikeProseAlone(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"internal/policy/license_check.go",
		"app/store/history_store.py",
		"src/docs-tools/readme_parser.ts",
		"cmd/newsfeed.go",
		"pkg/authors_index.rb",
	} {
		if class := classifySearchFile(path); class != searchFileClassSource {
			t.Fatalf("%s classified as %q, want source", path, class)
		}
		if NonProgramTextPath(path) {
			t.Fatalf("%s was declared non-program-text, so its relations were discarded", path)
		}
	}
	// The files the rule was written for keep their classification.
	for _, path := range []string{
		"README", "LICENSE", "LICENSE-APACHE", "COPYING", "AUTHORS", "NEWS",
		"README.md", "CHANGELOG.md", "docs/guide.rst",
	} {
		if class := classifySearchFile(path); class != searchFileClassDoc {
			t.Fatalf("%s classified as %q, want doc", path, class)
		}
	}
}

// A serialized artifact can also carry a canonical documentation name. Keep its data taxonomy,
// while allowing either explicit intent to retain full ranking strength and a primary-list slot.
func TestSearchDataClassHonorsDocumentationNamedArtifacts(t *testing.T) {
	t.Parallel()
	generic := buildSearchQuery("renderer returns the wrong status")
	for _, test := range []struct {
		path      string
		docQuery  string
		dataQuery string
	}{
		{"README.json", "update the README", "update the JSON metadata"},
		{"CHANGELOG.xml", "add a changelog entry", "update the XML data"},
		{"README.yaml", "refresh the README", "update the YAML metadata"},
	} {
		t.Run(test.path, func(t *testing.T) {
			if class := classifySearchFile(test.path); class != searchFileClassData {
				t.Fatalf("class = %q, want data", class)
			}
			if !NonProgramTextPath(test.path) {
				t.Fatal("serialized artifact was treated as program text")
			}
			for _, query := range []string{test.docQuery, test.dataQuery} {
				q := buildSearchQuery(query)
				if prior := searchFileClassPrior(q, test.path); prior != 1 {
					t.Errorf("prior on %q = %v, want 1", query, prior)
				}
				if searchDocsSectionPath(q, test.path) {
					t.Errorf("%q moved the requested artifact out of the primary list", query)
				}
			}
			if prior := searchFileClassPrior(generic, test.path); prior != searchNonSourceClassPrior {
				t.Errorf("generic-query prior = %v, want %v", prior, searchNonSourceClassPrior)
			}
			if !searchDocsSectionPath(generic, test.path) {
				t.Fatal("generic query left the serialized artifact in the primary list")
			}
		})
	}
}

// A callable that opens no block is a declaration only when it has no body at all. Kotlin and C#
// write executable bodies as expressions, and demoting those by the reference-declaration prior
// pushes the real fix site down the ranking.
func TestSearchReferenceDeclarationKeepsExpressionBodies(t *testing.T) {
	t.Parallel()
	callable := func(snippet string) SearchResult {
		return SearchResult{
			Kind: "function", Snippet: snippet,
			SymbolStartLine: 10, SymbolEndLine: 10,
			SnippetStartLine: 10, SnippetEndLine: 10,
		}
	}
	for _, snippet := range []string{
		"    fun formatAmount(value: Int) = value.toString()",
		"    public int Area() => Width * Height;",
		"    fun retries(limit: Int = 3) = limit * 2",
		"  def total: Int = items.sum",
	} {
		if searchReferenceDeclaration(callable(snippet)) {
			t.Fatalf("expression body demoted as a reference declaration: %q", snippet)
		}
	}
	// Real declarations are unchanged, including the C++ special members and a default argument.
	for _, snippet := range []string{
		"    virtual void flush() = 0;",
		"    Buffer(const Buffer &) = delete;",
		"    public function previous($fallback = false);",
		"void redisSetCpuAffinity(const char *cpulist);",
	} {
		if !searchReferenceDeclaration(callable(snippet)) {
			t.Fatalf("declaration lost its prior: %q", snippet)
		}
	}
}

// `other than` is a list cue only as the ordered adjacent phrase. Testing the two words
// independently read a singular comparison as a request for many parents and cut the protected
// baseline head from half of top-k to a third.
func TestProseParentHeadCountRequiresTheOtherThanPhrase(t *testing.T) {
	t.Parallel()
	single := buildSearchQuery("why is this slower than the other implementation")
	if got, want := proseParentHeadCount(single, 12), 6; got != want {
		t.Fatalf("head count on a singular comparison = %d, want %d", got, want)
	}
	list := buildSearchQuery("which sessions other than the migration one touched billing")
	if got, want := proseParentHeadCount(list, 12), 4; got != want {
		t.Fatalf("head count on an `other than` list = %d, want %d", got, want)
	}
}

// The `what/where are` cue read the query's LAST word as its plural evidence, so a singular
// question whose trailing prepositional phrase happens to be plural sacrificed baseline slots.
func TestProseParentHeadCountRequiresAPluralFrameAfterAre(t *testing.T) {
	t.Parallel()
	single := buildSearchQuery("what are we doing about deployments")
	if got, want := proseParentHeadCount(single, 12), 6; got != want {
		t.Fatalf("head count on a singular question = %d, want %d", got, want)
	}
	for _, query := range []string{
		"what are the open questions",
		"what are the remaining blockers",
		"where are the migrations",
	} {
		if got, want := proseParentHeadCount(buildSearchQuery(query), 12), 4; got != want {
			t.Fatalf("head count on %q = %d, want %d", query, got, want)
		}
	}
}

// A compound query term must not draw coverage from the prose word it NEGATES: `NotEncrypted`
// matching evidence `encrypted` promotes a session stating the opposite of what was asked.
func TestSafeProseEdgeCompoundRejectsNegatedTerms(t *testing.T) {
	t.Parallel()
	for _, pair := range [][2]string{
		{"notencrypted", "encrypted"},
		{"notavailable", "available"},
		{"noredirects", "redirects"},
	} {
		if safeProseInflectionMatch(pair[0], pair[1]) {
			t.Fatalf("%q was credited with coverage from its negation %q", pair[0], pair[1])
		}
	}
	// A non-opposing compound still resolves, so the rule stays a negation guard and not a ban.
	if !safeProseInflectionMatch("reencrypted", "encrypted") {
		t.Fatal("a non-negating compound lost its edge-compound coverage")
	}
}

// Multi-resolution promotion is funded from the SAME ceiling as the declaration card and the
// signature-type block, and SearchResponse.Validate checks their SUM. Spending the whole ceiling on
// promotion made the combined payload overshoot, and the response was rejected by its own
// validator — the search command failed instead of returning results.
func TestExpandProseResolutionReservesFundedBlockBytes(t *testing.T) {
	t.Parallel()
	results := []SearchResult{{
		Rank: 1, Score: 9, FilePath: "notes/session.md", Language: "Markdown",
		StartLine: 1, EndLine: 4, FocusLine: 2, SnippetStartLine: 1, SnippetEndLine: 4,
		Snippet: "amber lantern orchard", Signals: []string{proseParentRetrievalSignal},
		Passages: []SearchPassage{
			{StartLine: 40, EndLine: 44, FocusLine: 41, Snippet: strings.Repeat("amber ledger vessel ", 8)},
			{StartLine: 80, EndLine: 84, FocusLine: 81, Snippet: strings.Repeat("orchard ridge marker ", 8)},
		},
	}}
	budget := serializedSearchResultBytes(expandProseResolution(results, 3, 0, 0))
	reserved := 200
	expanded := expandProseResolution(results, 3, budget, reserved)
	if funded := serializedSearchResultBytes(expanded) + reserved; funded > budget {
		t.Fatalf("promotion spent %d of a %d ceiling that already owed %d to funded blocks",
			funded, budget, reserved)
	}
	// With nothing reserved the whole ceiling is still spendable, so the reservation is a
	// correction and not a new, permanent tax on promotion.
	if got := len(expandProseResolution(results, 3, budget, 0)); got != 3 {
		t.Fatalf("unreserved promotion returned %d results, want 3", got)
	}
}

// The unit of retrieval for prose is the SECTION. A markdown heading is parsed as a one-LINE
// `section` symbol, so asking whether a region carries a symbol of its own is true only for the
// few regions that matched a heading LINE and false for every match in a section BODY. Keying on
// it collapsed all ordinary body matches of a document into a single file unit, silently reducing
// section resolution to the file resolution it was written to replace.
func TestSearchRepositoryRanksProseSectionBodiesAsUnits(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	for index := 0; index < 5; index++ {
		var body strings.Builder
		body.WriteString("# Session log\n\n")
		for section := 0; section < 6; section++ {
			fmt.Fprintf(&body, "## turn %d\n\n", section)
			fmt.Fprintf(&body, "the amber lantern orchard ledger vessel appeared in turn %d.\n\n", section)
			for filler := 0; filler < 30; filler++ {
				fmt.Fprintf(&body, "filler line %d-%d about unrelated matters entirely.\n", section, filler)
			}
			body.WriteString("\n")
		}
		write(t, repo, fmt.Sprintf("sessions/session-%02d.md", index), body.String())
	}

	response, err := SearchRepository(
		t.Context(), repo, "test", "amber lantern orchard ledger vessel", SearchOptions{
			Worktree: true, Profile: ProfileSyntaxOnly, TopK: 20,
			MaxIndexedFiles: 32, MaxContextBytes: 400000,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("invalid response: %v", err)
	}
	ranked := map[string]int{}
	for _, result := range response.Results {
		if hasSearchSignal(result, proseResolutionSignal) {
			continue // a promoted passage, not a ranked unit
		}
		ranked[result.FilePath]++
	}
	multi := 0
	for _, count := range ranked {
		if count > 1 {
			multi++
		}
	}
	if multi == 0 {
		t.Fatalf("every document contributed at most one RANKED unit, so section bodies were "+
			"collapsed back onto the file: %v", ranked)
	}
}

// A document with no headings has no sections to split it into, so it keeps the file as its unit —
// and a markdown CODE FENCE, which is also a one-line symbol carrying an ID, must not become a
// retrievable unit of its own.
func TestProseSectionUnitsIgnoreCodeFenceSymbols(t *testing.T) {
	t.Parallel()
	fence := SymbolRecord{ID: "fence-1", Kind: "code_fence", FilePath: "notes/plain.md", StartLine: 4, EndLine: 4}
	candidates := []searchCandidate{{
		result: SearchResult{
			FilePath: "notes/plain.md", StartLine: 4, EndLine: 6, FocusLine: 4, SymbolID: fence.ID,
		},
	}}
	attachProseSectionUnits(candidates, map[string][]SymbolRecord{"notes/plain.md": {fence}})
	if key := proseParentKey(candidates[0], true); key != "notes/plain.md" {
		t.Fatalf("a code fence became its own unit: key = %q", key)
	}

	heading := SymbolRecord{ID: "h-1", Kind: "section", FilePath: "notes/headed.md", StartLine: 10, EndLine: 10}
	headed := []searchCandidate{
		{result: SearchResult{FilePath: "notes/headed.md", StartLine: 2, EndLine: 3, FocusLine: 2}},
		{result: SearchResult{FilePath: "notes/headed.md", StartLine: 20, EndLine: 24, FocusLine: 21}},
	}
	attachProseSectionUnits(headed, map[string][]SymbolRecord{"notes/headed.md": {heading}})
	preamble, section := proseParentKey(headed[0], true), proseParentKey(headed[1], true)
	if preamble == section {
		t.Fatalf("the preamble and the first section shared one unit: %q", preamble)
	}
	if section != "notes/headed.md#10" {
		t.Fatalf("body region keyed %q, want the heading it falls under", section)
	}
}

// `--enclosure-context-lines` is the caller asking to SEE the margin around the body. The
// "already complete" short-circuit ran before the padding, so the flag was silently ignored
// whenever rank 1's ranked snippet already spanned a short callable — which a short callable makes
// the common case.
func TestPlanSearchEnclosuresPadsAnAlreadyCompleteBody(t *testing.T) {
	t.Parallel()
	lines, symbol := enclosureTestFile(200, 40, 44, "function")
	reader := enclosureTestReader(lines)
	byID := map[string]SymbolRecord{symbol.ID: symbol}
	// The ranked snippet already spans the whole 5-line callable.
	result := SearchResult{
		FilePath: "pkg/file.go", StartLine: 40, EndLine: 44, FocusLine: 42,
		SnippetStartLine: 40, SnippetEndLine: 44, SymbolID: symbol.ID,
	}

	got := planSearchEnclosures([]SearchResult{result}, byID, nil, reader, 0, 10, 0, 0, 0, false)
	if !got[0].available() {
		t.Fatal("--enclosure-context-lines produced no enclosure for a body the snippet already covered")
	}
	if got[0].start != 30 || got[0].end != 54 {
		t.Fatalf("padded enclosure = %d-%d, want 30-54 (symbol 40-44 padded by 10)", got[0].start, got[0].end)
	}
	if got[0].symbol.Name != symbol.Name {
		t.Fatalf("padding renamed the symbol to %q", got[0].symbol.Name)
	}

	// Without a margin the short-circuit still holds: there is nothing to gain, and no bytes are
	// spent. This is what keeps the default payload byte-identical.
	if plain := planSearchEnclosures([]SearchResult{result}, byID, nil, reader, 0, 0, 0, 0, 0, false); plain[0].available() {
		t.Fatalf("an unpadded complete body was upgraded anyway: %+v", plain[0])
	}
}

// The docs-and-fixtures label is ~28 bytes on every result it touches, and the labelling pass runs
// after the fitter and the allocator have spent the ceiling. Those bytes therefore landed OUTSIDE
// the budget, and SearchResponse.Validate rejected the whole response — the search command failed
// for a caller who had asked for a SMALLER answer. It is the byte-budget half of the two findings
// above: both of them label or rank MORE prose and fixture results, so both make it reachable more
// often.
func TestSearchRepositorySectionLabelsFitTheByteBudget(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	for document := 0; document < 6; document++ {
		var body strings.Builder
		fmt.Fprintf(&body, "# Ledger notes %d\n\n", document)
		for section := 0; section < 6; section++ {
			fmt.Fprintf(&body, "## amber section %d\n\n", section)
			fmt.Fprintf(&body, "the amber lantern orchard ledger vessel in note %d turn %d.\n\n",
				document, section)
			for filler := 0; filler < 24; filler++ {
				fmt.Fprintf(&body, "filler %d-%d unrelated.\n", section, filler)
			}
			body.WriteString("\n")
		}
		write(t, repo, fmt.Sprintf("notes/note-%02d.md", document), body.String())
	}
	write(t, repo, "pkg/ledger.go", "package pkg\n\n"+
		"// AmberLedger holds the orchard ledger vessel state.\n"+
		"type AmberLedger struct {\n\tLantern string\n\tVessel  int\n}\n\n"+
		"// AmberLanternOrchardLedgerVessel returns the amber lantern orchard ledger vessel.\n"+
		"func AmberLanternOrchardLedgerVessel(l AmberLedger) AmberLedger {\n\tl.Vessel++\n\treturn l\n}\n")

	const ceiling = 6000
	for _, documentResolution := range []bool{false, true} {
		response, err := SearchRepository(
			t.Context(), repo, "test", "amber lantern orchard ledger vessel", SearchOptions{
				Worktree: true, Profile: ProfileFast, TopK: 20, MaxIndexedFiles: 64,
				MaxContextBytes: ceiling, DocumentResolution: documentResolution,
			},
		)
		if err != nil {
			t.Fatalf("document-resolution=%v: %v", documentResolution, err)
		}
		if err := response.Validate(); err != nil {
			t.Fatalf("document-resolution=%v: %v", documentResolution, err)
		}
		if response.Stats.ResultBytes > ceiling {
			t.Fatalf("document-resolution=%v: result_bytes %d over the %d ceiling",
				documentResolution, response.Stats.ResultBytes, ceiling)
		}
		labelled := 0
		for _, result := range response.Results {
			if result.Section == SearchSectionDocs {
				labelled++
			}
		}
		if labelled == 0 {
			t.Fatalf("document-resolution=%v: nothing was labelled, so the ceiling was never "+
				"under pressure from the label", documentResolution)
		}
	}
}

// The `=` scan starts after the callable's PARAMETER list, and the first parenthesised group in a
// snippet is not always it: an annotation or attribute writes its own argument list first, so
// `@JvmName("f") fun f(x: Int = 3);` left the DEFAULT ARGUMENT in the scanned tail and the
// declaration was read as an expression body, losing the reference-declaration demotion the rule
// exists to apply.
func TestSearchReferenceDeclarationSkipsAnnotationArgumentLists(t *testing.T) {
	t.Parallel()
	callable := func(snippet string) SearchResult {
		return SearchResult{
			Kind: "function", Snippet: snippet,
			SymbolStartLine: 10, SymbolEndLine: 10,
			SnippetStartLine: 10, SnippetEndLine: 10,
		}
	}
	// Declarations whose default argument sits behind an annotation's own parentheses.
	for _, snippet := range []string{
		"    @JvmName(\"f\") fun f(x: Int = 3);",
		"    [Obsolete(\"use Area2\")] public int Area(int scale = 2);",
		"    @Deprecated(since = \"2.0\") void flush(bool force = true);",
		// A generic type parameter's DEFAULT precedes the parameter list entirely.
		"template <class T = int> void reset();",
	} {
		if !searchReferenceDeclaration(callable(snippet)) {
			t.Fatalf("declaration lost its prior: %q", snippet)
		}
	}
	// An expression body behind the same annotation is still an implementation.
	for _, snippet := range []string{
		"    @JvmName(\"fmt\") fun formatAmount(value: Int) = value.toString()",
		"    [Pure] public int Area() => Width * Height;",
		"    @JvmName(\"r\") fun retries(limit: Int = 3) = limit * 2",
	} {
		if searchReferenceDeclaration(callable(snippet)) {
			t.Fatalf("expression body demoted as a reference declaration: %q", snippet)
		}
	}
}

// `expected` was dropped from the fixture intent terms because "expected X, got Y" is the most
// common sentence in a bug report. The converse still has to hold: a caller who asks to UPDATE an
// expected-output artifact is naming the edit target, and demoting snapshots out of the primary
// list hides it.
func TestSearchFixturePriorAnswersAnExpectedArtifactRequest(t *testing.T) {
	t.Parallel()
	path := "tests/__snapshots__/render.ambr"
	for _, query := range []string{
		"update the expected output for the parser",
		"regenerate the expected results for the renderer",
		"rewrite the expected files after the format change",
	} {
		if prior := searchFileClassPrior(buildSearchQuery(query), path); prior != 1 {
			t.Fatalf("fixture prior on %q = %v, want 1", query, prior)
		}
		if searchDocsSectionPath(buildSearchQuery(query), path) {
			t.Fatalf("%q had its requested artifact labelled out of the primary list", query)
		}
	}
	// A defect report keeps the prior on, including one that happens to say "expected output".
	for _, query := range []string{
		"expected a 200 response but the handler returns 500",
		"expected output 42 but the parser returns 43",
		"the expected output is wrong for nested nodes",
	} {
		if prior := searchFileClassPrior(buildSearchQuery(query), path); prior != searchSecondaryClassPrior {
			t.Fatalf("fixture prior on the bug query %q = %v, want %v",
				query, prior, searchSecondaryClassPrior)
		}
	}
}

// The plural frame after `are` was read through a fixed three-token window, so a plural head
// carried by several modifiers — "what are the currently known open blockers" — fell outside it and
// the query lost its multi-parent retrieval. The frame is bounded by what ENDS a noun phrase, not
// by a token count.
func TestProseParentHeadCountReadsAWiderPluralFrame(t *testing.T) {
	t.Parallel()
	for _, query := range []string{
		"what are the currently known open blockers",
		"what are the most recently reported flaky tests",
		"where are the remaining unmigrated legacy fixtures",
	} {
		if got, want := proseParentHeadCount(buildSearchQuery(query), 12), 4; got != want {
			t.Fatalf("head count on the plural frame %q = %d, want %d", query, got, want)
		}
	}
	// Singular questions still decline: a pronoun ends the frame, and so does a preposition or a
	// verb that carries the sentence past the noun phrase.
	for _, query := range []string{
		"what are we doing about deployments",
		"what are the difference between the two approaches",
		"what are the impact of failing builds",
	} {
		if got, want := proseParentHeadCount(buildSearchQuery(query), 12), 6; got != want {
			t.Fatalf("head count on the singular question %q = %d, want %d", query, got, want)
		}
	}
}

// Both intent fixes make MORE results reachable in the primary list — a fixture that keeps full
// strength on an artifact request, and prose parents behind a wider plural frame — and the docs
// label, the declaration card and the signature block are all funded from ONE ceiling that
// SearchResponse.Validate checks the sum of. So the widened intents are priced end to end: the
// response must still fit the budget the caller asked for, and the requested artifact must
// actually be in the primary list, which is what makes the byte measurement non-vacuous.
func TestSearchRepositoryFitsTheBudgetOnTheWidenedIntents(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	for document := 0; document < 5; document++ {
		var body strings.Builder
		fmt.Fprintf(&body, "# Ledger renderer notes %d\n\n", document)
		for section := 0; section < 5; section++ {
			fmt.Fprintf(&body, "## amber section %d\n\n", section)
			fmt.Fprintf(&body, "the currently known open blockers for the ledger renderer "+
				"output in note %d turn %d.\n\n", document, section)
			for filler := 0; filler < 20; filler++ {
				fmt.Fprintf(&body, "filler %d-%d unrelated.\n", section, filler)
			}
			body.WriteString("\n")
		}
		write(t, repo, fmt.Sprintf("notes/note-%02d.md", document), body.String())
	}
	for snapshot := 0; snapshot < 4; snapshot++ {
		write(t, repo, fmt.Sprintf("tests/__snapshots__/ledger-%02d.ambr", snapshot),
			fmt.Sprintf("# name: test_ledger_renderer_output[%d]\n  'the ledger renderer "+
				"output for the currently known open blockers'\n---\n", snapshot))
	}
	write(t, repo, "pkg/ledger.go", "package pkg\n\n"+
		"// LedgerRenderer renders the ledger renderer output.\n"+
		"type LedgerRenderer struct{ Output string }\n\n"+
		"// RenderLedgerOutput renders the expected ledger renderer output.\n"+
		"func RenderLedgerOutput(r LedgerRenderer) string {\n\treturn r.Output\n}\n")

	const ceiling = 6000
	for _, query := range []string{
		"update the expected output for the ledger renderer",
		"what are the currently known open blockers for the ledger renderer",
	} {
		response, err := SearchRepository(t.Context(), repo, "test", query, SearchOptions{
			Worktree: true, Profile: ProfileFast, TopK: 20, MaxIndexedFiles: 64,
			MaxContextBytes: ceiling,
		})
		if err != nil {
			t.Fatalf("%q: %v", query, err)
		}
		if err := response.Validate(); err != nil {
			t.Fatalf("%q: the widened intent overshot its own validator: %v", query, err)
		}
		if response.Stats.ResultBytes > ceiling {
			t.Fatalf("%q: result_bytes %d over the %d ceiling",
				query, response.Stats.ResultBytes, ceiling)
		}
	}

	// The artifact request must actually reach the primary list, or the budget above was
	// measured on a payload the change never touched.
	response, err := SearchRepository(t.Context(), repo, "test",
		"update the expected output for the ledger renderer", SearchOptions{
			Worktree: true, Profile: ProfileFast, TopK: 20, MaxIndexedFiles: 64,
			MaxContextBytes: ceiling,
		})
	if err != nil {
		t.Fatal(err)
	}
	primary := 0
	for _, result := range response.Results {
		if strings.HasSuffix(result.FilePath, ".ambr") && result.Section != SearchSectionDocs {
			primary++
		}
	}
	if primary == 0 {
		t.Fatal("the requested expected-output artifact was demoted out of the primary list")
	}
}

// The two halves of the artifact-request rule have to be about the SAME phrase. `expected output`
// names an artifact and `update` edits one, but "update the parser because the expected output is
// wrong" is a defect report whose edit target is the parser: the verb governs `the parser`, and
// the artifact phrase is the evidence, not the request. Reading the two halves as mere
// co-occurrence switched the fixture demotion off on exactly that sentence and let snapshots rank
// above the implementation the caller explicitly asked for.
func TestSearchFixturePriorRequiresTheEditVerbToGovernTheArtifact(t *testing.T) {
	t.Parallel()
	path := "tests/__snapshots__/render.ambr"
	// The verb governs something else: an edit request for the implementation, with the artifact
	// named only as the symptom. The demotion must stay ON.
	for _, query := range []string{
		"update the parser because the expected output is wrong",
		"regenerate the token stream, the expected results disagree",
		"rewrite the serializer since the expected output no longer matches",
	} {
		q := buildSearchQuery(query)
		if searchFixtureArtifactRequest(q) {
			t.Fatalf("%q was read as a request to edit the artifact", query)
		}
		if prior := searchFileClassPrior(q, path); prior != searchSecondaryClassPrior {
			t.Fatalf("fixture prior on %q = %v, want %v", query, prior, searchSecondaryClassPrior)
		}
	}
	// The verb governs the artifact phrase itself, across the modifiers a noun phrase may carry.
	// The demotion must stay OFF, which is the half the rule exists for.
	for _, query := range []string{
		"update the expected output for the parser",
		"regenerate all of the expected results for the renderer",
		"rewrite the stale expected files after the format change",
		"bless expected output",
	} {
		q := buildSearchQuery(query)
		if !searchFixtureArtifactRequest(q) {
			t.Fatalf("%q was not read as a request to edit the artifact", query)
		}
		if prior := searchFileClassPrior(q, path); prior != 1 {
			t.Fatalf("fixture prior on %q = %v, want 1", query, prior)
		}
	}
}

// A `--deep` search fuses sparse windows into a ranking that already contains prose-parent
// regions, and a sparse window over a markdown document is prose text like any other. Containment
// was decided from the RETRIEVAL SIGNAL rather than from the file, so a sparse window was never
// checked against the region above it and the payload printed the same lines twice — the cost paid
// again on every later turn that replays the payload.
func TestSearchDeepDoesNotPrintOneProseWindowTwice(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	for document := 0; document < 6; document++ {
		var body strings.Builder
		fmt.Fprintf(&body, "# ledger renderer notes %d\n\n", document)
		for section := 0; section < 6; section++ {
			fmt.Fprintf(&body, "## amber ledger section %d-%d\n\n", document, section)
			for paragraph := 0; paragraph < 8; paragraph++ {
				fmt.Fprintf(&body, "the ledger renderer output amber blocker paragraph %d in "+
					"section %d of note %d.\n\n", paragraph, section, document)
				for filler := 0; filler < 6; filler++ {
					fmt.Fprintf(&body, "filler %d-%d-%d text about unrelated matters.\n",
						section, paragraph, filler)
				}
				body.WriteString("\n")
			}
		}
		write(t, repo, fmt.Sprintf("notes/note-%02d.md", document), body.String())
	}
	response, err := SearchRepository(t.Context(), repo, "test",
		"ledger renderer output amber blocker", SearchOptions{
			Worktree: true,
			Profile:  ProfileSyntaxOnly,
			TopK:     12,
			Deep:     true,
		})
	if err != nil {
		t.Fatal(err)
	}
	if response.Stats.SparseCandidates == 0 {
		t.Fatal("the deep search built no sparse candidates, so it cannot exercise fusion")
	}
	sparse := false
	for _, result := range response.Results {
		if containsString(result.Signals, "sparse-region") {
			sparse = true
			break
		}
	}
	if !sparse {
		t.Fatal("no sparse window reached the payload, so containment was never exercised")
	}
	for index, result := range response.Results {
		if result.SnippetStartLine <= 0 || result.SnippetEndLine < result.SnippetStartLine {
			continue
		}
		for _, prior := range response.Results[:index] {
			if prior.FilePath != result.FilePath || prior.SnippetStartLine <= 0 {
				continue
			}
			if prior.SnippetStartLine <= result.SnippetStartLine &&
				result.SnippetEndLine <= prior.SnippetEndLine {
				t.Fatalf("%s lines %d-%d were printed again inside the higher-ranked %d-%d",
					result.FilePath, result.SnippetStartLine, result.SnippetEndLine,
					prior.SnippetStartLine, prior.SnippetEndLine)
			}
		}
	}
}

// Widening the containment rule from the RETRIEVAL SIGNAL to the FILE has to widen it to prose
// only. A contained CODE region is a different statement about the same lines — an inner function
// inside the outer one that encloses it — and one region per unit already bounds how many of those
// a file can contribute, so dropping it would take a distinct fix site out of the payload.
func TestDropContainedResultsKeepsContainedCodeRegions(t *testing.T) {
	t.Parallel()
	code := []SearchResult{
		{FilePath: "internal/ledger/render.go", Rank: 1, SnippetStartLine: 10, SnippetEndLine: 60},
		{FilePath: "internal/ledger/render.go", Rank: 2, SnippetStartLine: 20, SnippetEndLine: 28},
	}
	if kept := dropContainedProseResults(code); len(kept) != 2 {
		t.Fatalf("a nested code region was dropped as a duplicate: %#v", kept)
	}
	// The same shape in a document IS a duplicate: the enclosing window already printed it.
	prose := []SearchResult{
		{FilePath: "notes/design.md", Rank: 1, SnippetStartLine: 1, SnippetEndLine: 80},
		{FilePath: "notes/design.md", Rank: 2, SnippetStartLine: 5, SnippetEndLine: 40},
	}
	kept := dropContainedProseResults(prose)
	if len(kept) != 1 {
		t.Fatalf("the contained prose window survived: %#v", kept)
	}
	if kept[0].SnippetStartLine != 1 || kept[0].SnippetEndLine != 80 {
		t.Fatalf("containment kept the wrong window: %#v", kept[0])
	}
}
