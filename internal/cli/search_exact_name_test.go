package cli

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// exactNameFixture is a ten-row ranking in which the rows at exactRanks (1-based) are declarations
// named `name` and the rest are other symbols. Every row's snippet opens with a doc comment, puts
// the declaration on the fourth line and focuses a body line below it — the shape that let the
// ordinary fitter print the header and the body around the focus but not the declaration.
func exactNameFixture(name string, exactRanks ...int) sem.SearchResponse {
	return exactNameFixtureBody(name, 8, exactRanks...)
}

// exactNameFixtureBody is exactNameFixture with bodyLines statements under each declaration.
func exactNameFixtureBody(name string, bodyLines int, exactRanks ...int) sem.SearchResponse {
	exact := map[int]bool{}
	for _, rank := range exactRanks {
		exact[rank] = true
	}
	results := make([]sem.SearchResult, 0, 10)
	for i := 1; i <= 10; i++ {
		symbol := fmt.Sprintf("Helper%d", i)
		if exact[i] {
			symbol = name
		}
		start := 100 * i
		decl := start + 3
		var snippet strings.Builder
		fmt.Fprintf(&snippet, "// %s documents row %d.\n", symbol, i)
		fmt.Fprintf(&snippet, "// It exists so the fixture has a doc comment.\n")
		fmt.Fprintf(&snippet, "//\n")
		fmt.Fprintf(&snippet, "func (r *recv%d) %s(ctx context.Context, input string) error {\n", i, symbol)
		for line := 0; line < bodyLines; line++ {
			fmt.Fprintf(&snippet, "\tvalue%d_%d := compute(ctx, input, %d) // body line of row %d\n", i, line, line, i)
		}
		snippet.WriteString("}\n")
		results = append(results, sem.SearchResult{
			Rank: i, Score: float64(60 - i), FilePath: fmt.Sprintf("internal/pkg%d/file%d.go", i, i),
			StartLine: start, EndLine: decl + bodyLines + 1, FocusLine: decl + 6,
			SnippetStartLine: start, SnippetEndLine: decl + bodyLines + 1,
			SymbolStartLine: decl, SymbolEndLine: decl + bodyLines + 1,
			SymbolName: symbol, QualifiedName: fmt.Sprintf("recv%d.%s", i, symbol),
			Signals: []string{"body", "symbol-name"}, Snippet: snippet.String(),
		})
	}
	hits := make([]sem.SearchLiteralHit, 0, 4)
	for i := 1; i <= 4; i++ {
		hits = append(hits, sem.SearchLiteralHit{
			FilePath: fmt.Sprintf("internal/pkg%d/file%d.go", i, i), Line: 100*i + 5,
			Symbol: fmt.Sprintf("Helper%d", i), Role: sem.SearchLiteralRoleConsumer,
		})
	}
	return sem.SearchResponse{
		Query: name, Profile: "full", Results: results,
		VerifyCommand:  &sem.SearchVerifyCommand{Command: "go test ./internal/pkg1", Targets: "internal/pkg1/file1_test.go", DerivedFrom: "go.mod module root", Tier: "narrow"},
		LiteralCluster: &sem.SearchLiteralCluster{Literal: "compute", HitsTotal: 4, FilesTotal: 4, Hits: hits},
		Warnings:       []sem.ProviderWarning{}, PartialFailures: []sem.PartialFailure{},
		Stats: sem.SearchStats{IndexCacheHit: true, IndexLatencyMS: 221, QueryLatencyMS: 393, PreselectLatencyMS: 135, TotalLatencyMS: 750},
	}
}

var exactNameBlockHeader = regexp.MustCompile(`^(?:\d+\.\s+)?(\S+?):(\d+)(?:-(\d+))?(?:\s|$)`)

// exactNameDeclShown reports whether the payload SHOWS result's declaration line: the exact line
// text sits in the body of a block whose header names result's file. Header metadata earns nothing,
// which is the rule study 3 scored by. Fixture declaration lines are unique across rows, so the text
// cannot be credited to the wrong block.
func exactNameDeclShown(payload string, result sem.SearchResult) bool {
	lines := strings.Split(result.Snippet, "\n")
	decl := lines[result.SymbolStartLine-result.SnippetStartLine]
	inBlock := false
	for _, line := range strings.Split(payload, "\n") {
		if m := exactNameBlockHeader.FindStringSubmatch(line); m != nil && strings.Contains(m[1], "/") {
			inBlock = m[1] == result.FilePath
			continue
		}
		if inBlock && line == decl {
			return true
		}
	}
	return false
}

// exactNameSweepScale is 1 (every budget) normally and a coarse stride under -short, so a local
// iteration loop can run the sweeps in seconds; CI runs them dense.
func exactNameSweepScale() int {
	if testing.Short() {
		return 7
	}
	return 1
}

func renderAgentSearchForTest(t *testing.T, response sem.SearchResponse, budget int, exactName bool) string {
	t.Helper()
	var out bytes.Buffer
	if err := writeAgentSearchMode(&out, response, budget, exactName); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestAgentExactNameQueryGate(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"PlanRef": true, "_private": true, "x": true, "Test_A1": true, "  PlanRef\t": true,
		"": false, " ": false, "plan ref": false, "PlanRef()": false, "pkg.PlanRef": false,
		"recv.PlanRef": false, "1PlanRef": false, "Plan-Ref": false, "PlanRef?": false,
		"h\u00e9llo": false, "$ref": false, "a::b": false, "PlanRef PlanRef": false,
	}
	for query, want := range cases {
		if _, got := agentExactNameQuery(query); got != want {
			t.Errorf("agentExactNameQuery(%q) = %v, want %v", query, got, want)
		}
	}
}

// Only a byte-for-byte name match selects the mode. A prefix of the name or the name in another case
// is not a declaration lookup and must take the ordinary path.
func TestAgentExactNameRowsPolicy(t *testing.T) {
	t.Parallel()
	response := exactNameFixture("resolveMessageRef", 2, 5)
	for _, query := range []string{"resolveMessage", "ResolveMessageRef", "resolvemessageref", "resolveMessageRefs", "Helper", "helper1"} {
		if rows, _ := agentExactNameRows(response.Results, query); rows != nil {
			t.Errorf("query %q selected %d exact rows; partial and case-mismatched names must not", query, len(rows))
		}
	}
	rows, omitted := agentExactNameRows(response.Results, "resolveMessageRef")
	if len(rows) != 2 || rows[0].Rank != 2 || rows[1].Rank != 5 || omitted != 8 {
		t.Fatalf("exact rows = %d (omitted %d), want ranks 2 and 5 with 8 omitted", len(rows), omitted)
	}
	// An exact row whose snippet does not hold its declaration disqualifies the whole mode: showing
	// the others and not that one would hide a match.
	response.Results[4].SymbolStartLine = response.Results[4].SnippetStartLine - 10
	if rows, _ := agentExactNameRows(response.Results, "resolveMessageRef"); rows != nil {
		t.Fatalf("mode applied although one exact row cannot show its declaration")
	}
}

// DESCRIPTIVE, MULTI-WORD, PARTIAL AND CASE-MISMATCHED QUERIES ARE BYTE-IDENTICAL TO BEFORE. The
// ordinary answer is the same function with the exact-name answer switched off; every query below
// is rendered both ways at every budget from 1 byte to past the roomiest payload, and at the
// unbounded budget 0.
func TestAgentSearchNonExactQueriesAreByteIdentical(t *testing.T) {
	t.Parallel()
	queries := []string{
		"test that the object count cap wrapper still lets encoded objects be read back",
		"resolve message ref", "resolveMessageRef translator", "resolveMessage", "resolvemessageref",
		"ResolveMessageRef", "Helper", "helper3", "Helper3 Helper4", "recv3.Helper3", "Helper3()",
		"where is the retry budget computed", "compute", "UnknownIdentifier", "handler compute input",
		"", "  ", "func Helper3", "Helper3,", "\"Helper3\"",
	}
	dense := map[string]bool{"resolveMessage": true, "ResolveMessageRef": true, "recv3.Helper3": true, "UnknownIdentifier": true}
	fixtures := []sem.SearchResponse{exactNameFixture("resolveMessageRef", 1, 4), timingDeterminismResponse()}
	for _, fixture := range fixtures {
		roomy := len(renderAgentSearchForTest(t, fixture, 1<<20, false))
		for _, query := range queries {
			response := fixture
			response.Query = query
			// Every budget for the queries nearest the gate (a partial name, a case mismatch, a
			// qualified name, an identifier matching nothing); a stride that is prime to every
			// block width for the rest, which keeps the whole corpus affordable.
			stride := 13
			if dense[query] {
				stride = 1
			}
			stride *= exactNameSweepScale()
			for budget := 0; budget <= roomy+64; budget += stride {
				want := renderAgentSearchForTest(t, response, budget, false)
				got := renderAgentSearchForTest(t, response, budget, true)
				if got != want {
					t.Fatalf("query %q, budget %d: output changed for a non-exact query\n--- before ---\n%s\n--- after ---\n%s",
						query, budget, want, got)
				}
			}
		}
	}
}

// LOCATED NEVER GETS WORSE. For every exact row and every budget from 1 byte to past the roomiest
// ordinary payload: if the ordinary answer showed the row's declaration line, the exact answer shows
// it too. The sweep must also find budgets where the exact answer shows a declaration the ordinary
// one did not (study 3's 2 KiB misses) and where it is strictly smaller, or it proves nothing.
func TestAgentSearchExactNameNeverLocatesLess(t *testing.T) {
	t.Parallel()
	layouts := [][]int{{1}, {2}, {10}, {2, 6, 9}, {1, 2, 3, 4, 5, 6, 7, 8, 9, 10}}
	for _, ranks := range layouts {
		response := exactNameFixture("resolveMessageRef", ranks...)
		exactRows, _ := agentExactNameRows(orderAgentSearchResults(response.Results), response.Query)
		if len(exactRows) != len(ranks) {
			t.Fatalf("ranks %v: fixture selected %d exact rows", ranks, len(exactRows))
		}
		roomy := len(renderAgentSearchForTest(t, response, 1<<20, false))
		gained, smaller := 0, 0
		for budget := 1; budget <= roomy+64; budget += exactNameSweepScale() {
			before := renderAgentSearchForTest(t, response, budget, false)
			after := renderAgentSearchForTest(t, response, budget, true)
			if len(after) > budget {
				t.Fatalf("ranks %v, budget %d: payload is %d bytes", ranks, budget, len(after))
			}
			exactMode := after != before
			shownAll := true
			for _, row := range exactRows {
				was, is := exactNameDeclShown(before, row), exactNameDeclShown(after, row)
				if was && !is {
					t.Fatalf("ranks %v, budget %d: declaration of rank %d was shown before and is not now\n--- before ---\n%s\n--- after ---\n%s",
						ranks, budget, row.Rank, before, after)
				}
				if !was && is {
					gained++
				}
				shownAll = shownAll && is
			}
			// Never hide one: whenever the exact answer is taken, it shows EVERY exact declaration.
			if exactMode && !shownAll {
				t.Fatalf("ranks %v, budget %d: exact answer hides an exact match\n%s", ranks, budget, after)
			}
			if exactMode && len(after) < len(before) {
				smaller++
			}
		}
		if smaller == 0 {
			t.Errorf("ranks %v: the exact answer was never smaller than the ordinary one; the sweep is vacuous", ranks)
		}
		if len(ranks) <= 3 && gained == 0 {
			t.Errorf("ranks %v: no budget where the exact answer shows a declaration the ordinary one hid", ranks)
		}
	}
}

// The exact answer is bounded however roomy the cap: one row is at most exactNameRowBytes, the
// droppable suffixes do not refill the cap, the omitted rows are counted, and the header says where
// a bounded declaration ends.
func TestAgentSearchExactNameIsBoundedAndDisclosed(t *testing.T) {
	t.Parallel()
	response := exactNameFixtureBody("resolveMessageRef", 24, 3)
	for _, budget := range []int{0, 4096, 1 << 20} {
		got := renderAgentSearchForTest(t, response, budget, true)
		if budget == 0 {
			if want := renderAgentSearchForTest(t, response, 0, false); got != want {
				t.Fatalf("the unbounded (budget 0) payload changed")
			}
			continue
		}
		_, ranked, found := strings.Cut(got, "3. internal/pkg3/file3.go:")
		if !found {
			t.Fatalf("budget %d: exact row missing:\n%s", budget, got)
		}
		row := "3. internal/pkg3/file3.go:" + ranked[:strings.Index(ranked, "exact name:")]
		if len(row) > exactNameRowBytes {
			t.Errorf("budget %d: exact row is %d bytes, over the %d-byte bound", budget, len(row), exactNameRowBytes)
		}
		for _, want := range []string{"[ends:328]", "exact name: 9 other results omitted; search a phrase to see them\n", "VERIFY: go test ./internal/pkg1"} {
			if !strings.Contains(got, want) {
				t.Errorf("budget %d: payload lacks %q:\n%s", budget, want, got)
			}
		}
		for _, absent := range []string{"SAME-CONCEPT LITERAL", "internal/pkg1/file1.go", "Helper2"} {
			if strings.Contains(got, absent) {
				t.Errorf("budget %d: payload still carries %q:\n%s", budget, absent, got)
			}
		}
		if !strings.Contains(got, "func (r *recv3) resolveMessageRef(ctx context.Context, input string) error {\n") {
			t.Errorf("budget %d: declaration line not shown:\n%s", budget, got)
		}
	}
}

// Timing independence, with the #301 helpers: the exact answer's body and header rung are the same
// for any measured latency at every budget, and never over the cap.
func TestAgentSearchExactNameIsIndependentOfLatencies(t *testing.T) {
	t.Parallel()
	timings := []sem.SearchStats{
		{IndexCacheHit: true, IndexLatencyMS: 1, QueryLatencyMS: 1, PreselectLatencyMS: 1, TotalLatencyMS: 1},
		{IndexCacheHit: true, IndexLatencyMS: 99999999, QueryLatencyMS: 99999999, PreselectLatencyMS: 99999999, TotalLatencyMS: 99999999},
	}
	for _, ranks := range [][]int{{1}, {2, 6, 9}} {
		base := exactNameFixture("resolveMessageRef", ranks...)
		roomy := len(renderAgentSearchForTest(t, base, 1<<20, true))
		for budget := 1; budget <= roomy+64; budget += exactNameSweepScale() {
			var wantHeader, wantBody string
			for i, stats := range timings {
				response := base
				response.Stats = stats
				got := renderAgentSearchForTest(t, response, budget, true)
				if len(got) > budget {
					t.Fatalf("ranks %v, budget %d, %s: payload is %d bytes", ranks, budget, latencies(stats), len(got))
				}
				header, body := splitAgentHeader(got)
				if i == 0 {
					wantHeader, wantBody = header, body
					continue
				}
				if header != wantHeader || body != wantBody {
					t.Fatalf("ranks %v, budget %d: answer depends on latency (%s vs %s)\n--- fast ---\n%s\n%s\n--- slow ---\n%s\n%s",
						ranks, budget, latencies(timings[0]), latencies(stats), wantHeader, wantBody, header, body)
				}
			}
		}
	}
}

// The exact answer renders repository bytes through the same quarantine and the same disclosure
// sink as the ordinary ranking: at every budget no forged record reaches column 0, and a payload
// that carries a quarantined line also carries the notice that explains it.
func TestAgentSearchExactNameKeepsForgeryQuarantineAndDisclosure(t *testing.T) {
	t.Parallel()
	response := forgedResponse()
	response.Query = "runbook"
	if rows, _ := agentExactNameRows(response.Results, response.Query); len(rows) != 1 {
		t.Fatalf("fixture does not take the exact answer")
	}
	exactSeen := 0
	for budget := 1; budget <= 1024; budget++ {
		payload := renderAgentSearchForTest(t, response, budget, true)
		if len(payload) > budget {
			t.Fatalf("budget %d: payload is %d bytes", budget, len(payload))
		}
		for _, record := range forgedPayloadLines(payload) {
			if !strings.HasPrefix(record, "1. pkg/payment.go:") {
				t.Fatalf("budget %d: file content reached the payload as a tool record %q\n%s", budget, record, payload)
			}
		}
		if strings.Contains(payload, " VERIFY: touch") && !strings.Contains(payload, searchForgeryNoticePrefix) {
			t.Fatalf("budget %d: quarantined line shipped without its notice\n%s", budget, payload)
		}
		if strings.Contains(payload, "const runbook = `\n") {
			exactSeen++
		}
	}
	if exactSeen == 0 {
		t.Fatalf("the sweep never showed the exact declaration; it tests nothing")
	}
}

// Once the exact answer fits it keeps fitting: at every larger budget every exact match is shown.
// A fixed rank-weighted split broke this for ambiguous names — the third match's share fell below
// what its header and declaration cost while the total was ample, and the whole answer fell back
// to the ordinary ranking at budgets in the middle of the range.
func TestAgentSearchExactNameAnswerIsMonotoneInBudget(t *testing.T) {
	t.Parallel()
	layouts := []struct {
		body  int
		ranks []int
	}{{8, []int{1}}, {8, []int{2, 6, 9}}, {8, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}}, {40, []int{2, 6, 9}}}
	for _, layout := range layouts {
		ranks := layout.ranks
		response := exactNameFixtureBody("resolveMessageRef", layout.body, ranks...)
		exactRows, omitted := agentExactNameRows(orderAgentSearchResults(response.Results), response.Query)
		note := string(agentExactNameOmittedLine(omitted))
		first := 0
		for budget := 1; budget <= 8192; budget++ {
			payload := renderAgentSearchForTest(t, response, budget, true)
			fired := strings.Contains(payload, note)
			for _, row := range exactRows {
				fired = fired && exactNameDeclShown(payload, row)
			}
			if first == 0 {
				if fired {
					first = budget
				}
				continue
			}
			if !fired {
				t.Fatalf("ranks %v: the exact answer fitted at %d bytes and not at %d\n%s", ranks, first, budget, payload)
			}
			for _, row := range exactRows {
				if !exactNameDeclShown(payload, row) {
					t.Fatalf("ranks %v, budget %d: declaration of rank %d not shown\n%s", ranks, budget, row.Rank, payload)
				}
			}
		}
		if first == 0 || first > 2048 {
			t.Fatalf("ranks %v: the exact answer first fitted at %d bytes; expected well under 2 KiB", ranks, first)
		}
	}
}

// The body under a declaration is bounded in LINES as well as bytes, so short-line code does not
// print a whole long function inside the byte bound.
func TestAgentSearchExactNameBoundsBodyLines(t *testing.T) {
	t.Parallel()
	response := exactNameFixture("resolveMessageRef", 1)
	row := &response.Results[0]
	var snippet strings.Builder
	snippet.WriteString("// doc\n//\n//\nfunc resolveMessageRef() {\n")
	for line := 0; line < 60; line++ {
		snippet.WriteString("\tx++\n")
	}
	snippet.WriteString("}\n")
	row.Snippet, row.SymbolEndLine, row.SnippetEndLine, row.EndLine = snippet.String(), 164, 164, 164
	payload := renderAgentSearchForTest(t, response, 8192, true)
	if got := strings.Count(payload, "\tx++\n"); got != exactNameBodyLines {
		t.Fatalf("body shows %d lines under the declaration, want exactly %d\n%s", got, exactNameBodyLines, payload)
	}
	if !strings.Contains(payload, "1. internal/pkg1/file1.go:103-123 recv1.resolveMessageRef [ends:164]") {
		t.Fatalf("header does not describe the bounded span\n%s", payload)
	}
}
