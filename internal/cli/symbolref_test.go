package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// ambiguitySnapshot holds the three shapes of same-name definition that the old
// "--file, and if needed a qualified --symbol" advice could not separate:
//
//   - two definitions in the SAME file with the SAME qualified name, on different lines
//   - two definitions at the SAME location differing only by kind
//   - definitions in different files (the case --file always handled)
func ambiguitySnapshot() sem.ProviderSnapshot {
	return sem.ProviderSnapshot{
		Files: []sem.FileRecord{
			{ID: "f-scanner", Path: "internal/scanner.c", Language: "C"},
			{ID: "f-other", Path: "internal/other.c", Language: "C"},
			{ID: "f-provider", Path: "internal/provider.go", Language: "Go"},
		},
		Symbols: []sem.SymbolRecord{
			{ID: "state-late", Name: "State", QualifiedName: "State", Kind: "type",
				FilePath: "internal/scanner.c", StartLine: 712, EndLine: 717, Language: "C"},
			{ID: "state-early", Name: "State", QualifiedName: "State", Kind: "type",
				FilePath: "internal/scanner.c", StartLine: 562, EndLine: 569, Language: "C"},
			{ID: "state-other", Name: "State", QualifiedName: "State", Kind: "type",
				FilePath: "internal/other.c", StartLine: 10, EndLine: 12, Language: "C"},
			// A DIFFERENTLY named record sharing a line with state-early. It is not part of
			// any "State" listing, but it is why a location-only selector is unsafe: dropping
			// the name from the selector widens the query onto this record.
			{ID: "anon-field", Name: "done", QualifiedName: "done", Kind: "struct",
				FilePath: "internal/scanner.c", StartLine: 562, EndLine: 569, Language: "C"},
			{ID: "handler-fn", Name: "handler", QualifiedName: "handler", Kind: "function",
				FilePath: "internal/provider.go", StartLine: 40, EndLine: 48, Language: "Go"},
			{ID: "handler-tool", Name: "handler", QualifiedName: "handler", Kind: "tool",
				FilePath: "internal/provider.go", StartLine: 40, EndLine: 48, Language: "Go"},
			{ID: "caller", Name: "Caller", QualifiedName: "Caller", Kind: "function",
				FilePath: "internal/provider.go", StartLine: 1, EndLine: 5, Language: "Go"},
		},
		Relations: []sem.RelationRecord{
			{FromID: "caller", ToID: "state-early", Type: "CALLS", Resolution: "exact"},
		},
	}
}

func TestParseSymbolRef(t *testing.T) {
	t.Parallel()
	paths := []string{"internal/scanner.c", "src/domains/combobox/Combobox.tsx"}
	cases := []struct {
		name     string
		symbol   string
		file     string
		line     int
		kind     string
		wantName string
		wantFile string
		wantLine int
	}{
		{
			name: "plain name is unchanged", symbol: "State",
			wantName: "State",
		},
		{
			name: "file and line shorthand", symbol: "internal/scanner.c:712",
			wantName: "", wantFile: "internal/scanner.c", wantLine: 712,
		},
		{
			name: "basename suffix resolves against the snapshot", symbol: "Combobox.tsx:12",
			wantName: "", wantFile: "src/domains/combobox/Combobox.tsx", wantLine: 12,
		},
		{
			name: "leading ./ is tolerated", symbol: "./internal/scanner.c:5",
			wantName: "", wantFile: "internal/scanner.c", wantLine: 5,
		},
		{
			name: "c++ scoped name is not a location", symbol: "Lexer::State",
			wantName: "Lexer::State",
		},
		{
			name: "ruby scoped name is not a location", symbol: "Foo::Bar",
			wantName: "Foo::Bar",
		},
		{
			name: "unknown path with a line stays a name", symbol: "not/a/file.c:99",
			wantName: "not/a/file.c:99",
		},
		{
			name: "trailing colon stays a name", symbol: "State:",
			wantName: "State:",
		},
		{
			name: "zero line stays a name", symbol: "internal/scanner.c:0",
			wantName: "internal/scanner.c:0",
		},
		{
			name:   "explicit flags win over the shorthand",
			symbol: "internal/scanner.c:712", file: "internal/other.c", line: 10,
			wantName: "", wantFile: "internal/other.c", wantLine: 10,
		},
		{
			name: "explicit name plus line", symbol: "State", line: 562,
			wantName: "State", wantLine: 562,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			ref := parseSymbolRef(testCase.symbol, testCase.file, testCase.line, testCase.kind, "", paths)
			if ref.Name != testCase.wantName || ref.File != testCase.wantFile || ref.Line != testCase.wantLine {
				t.Fatalf("ref = %+v, want name=%q file=%q line=%d",
					ref, testCase.wantName, testCase.wantFile, testCase.wantLine)
			}
		})
	}
}

func TestResolveFocusSymbolsEscapeHatches(t *testing.T) {
	t.Parallel()
	snapshot := ambiguitySnapshot()
	paths := snapshotFilePaths(snapshot)
	cases := []struct {
		name    string
		symbol  string
		file    string
		line    int
		kind    string
		wantIDs []string
	}{
		{
			name: "bare name is ambiguous across files and lines", symbol: "State",
			wantIDs: []string{"state-early", "state-late", "state-other"},
		},
		{
			name:   "file alone cannot separate two same-file definitions",
			symbol: "State", file: "internal/scanner.c",
			wantIDs: []string{"state-early", "state-late"},
		},
		{
			name:   "line separates two same-file same-qualified-name definitions",
			symbol: "State", file: "internal/scanner.c", line: 712,
			wantIDs: []string{"state-late"},
		},
		{
			name: "line alone separates them too", symbol: "State", line: 562,
			wantIDs: []string{"state-early"},
		},
		{
			// The shorthand is positional, so it selects EVERY definition on that line.
			// That is why the printed selector always carries the name (see
			// disambiguationSelectors).
			name: "file:line shorthand selects everything on the line", symbol: "internal/scanner.c:562",
			wantIDs: []string{"state-early", "anon-field"},
		},
		{
			name: "file:line shorthand plus a kind narrows it", symbol: "internal/scanner.c:562", kind: "type",
			wantIDs: []string{"state-early"},
		},
		{
			name:   "kind separates two records at one location",
			symbol: "handler", file: "internal/provider.go", line: 40, kind: "tool",
			wantIDs: []string{"handler-tool"},
		},
		{
			name:   "kind is case insensitive",
			symbol: "handler", file: "internal/provider.go", line: 40, kind: "FUNCTION",
			wantIDs: []string{"handler-fn"},
		},
		{
			name:   "a line inside a body selects the innermost definition",
			symbol: "State", file: "internal/scanner.c", line: 565,
			wantIDs: []string{"state-early"},
		},
		{
			name: "a line matching nothing yields nothing", symbol: "State", line: 9999,
			wantIDs: nil,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			ref := parseSymbolRef(testCase.symbol, testCase.file, testCase.line, testCase.kind, "", paths)
			matches := resolveFocusSymbols(snapshot.Symbols, ref)
			got := make([]string, 0, len(matches))
			for _, match := range matches {
				got = append(got, match.ID)
			}
			// resolveFocusSymbols preserves snapshot order; compare as a set.
			if len(got) != len(testCase.wantIDs) {
				t.Fatalf("matches = %v, want %v", got, testCase.wantIDs)
			}
			for _, want := range testCase.wantIDs {
				found := false
				for _, id := range got {
					if id == want {
						found = true
					}
				}
				if !found {
					t.Fatalf("matches = %v, want %v", got, testCase.wantIDs)
				}
			}
		})
	}
}

// TestDisambiguationSelectorsAreUniqueAndUsable is the guarantee the old error message
// lacked: every selector printed must (a) be unique in the listing and (b) actually resolve
// to exactly the definition it was printed for when fed back through the flag parser.
func TestDisambiguationSelectorsAreUniqueAndUsable(t *testing.T) {
	t.Parallel()
	snapshot := ambiguitySnapshot()
	paths := snapshotFilePaths(snapshot)
	for _, query := range []string{"State", "handler"} {
		ref := parseSymbolRef(query, "", 0, "", "", paths)
		matches := resolveFocusSymbols(snapshot.Symbols, ref)
		if len(matches) < 2 {
			t.Fatalf("%q was not ambiguous in the fixture", query)
		}
		definitions := make([]neighborEndpoint, 0, len(matches))
		for _, match := range matches {
			definitions = append(definitions, endpointForSymbol(match))
		}
		selectors := disambiguationSelectors(definitions)
		seen := map[string]bool{}
		for index, selector := range selectors {
			if selector == "" {
				t.Fatalf("%q definition %d got no selector", query, index)
			}
			if seen[selector] {
				t.Fatalf("%q selector %q is not unique", query, selector)
			}
			seen[selector] = true

			flags, err := parseImpactFlags(strings.Fields(selector))
			if err != nil {
				t.Fatalf("selector %q is not parseable: %v", selector, err)
			}
			resolved := resolveFocusSymbols(snapshot.Symbols,
				parseSymbolRef(flags.Symbol, flags.File, flags.Line, flags.Kind, "", paths))
			if len(resolved) != 1 {
				t.Fatalf("selector %q resolved to %d definitions, want 1", selector, len(resolved))
			}
			if resolved[0].ID != definitions[index].ID {
				t.Fatalf("selector %q resolved to %q, want %q",
					selector, resolved[0].ID, definitions[index].ID)
			}
		}
	}
}

// TestAmbiguityRemedyEndToEnd walks the whole loop a caller walks: ambiguous query ->
// listing -> copy a printed selector -> resolved focus with real sections.
func TestAmbiguityRemedyEndToEnd(t *testing.T) {
	t.Parallel()
	snapshot := ambiguitySnapshot()
	ambiguous := buildImpactResponseOnDisk(snapshot, impactFlags{Symbol: "State", Depth: 2, Limit: 15})
	if !ambiguous.DisambiguationRequired || ambiguous.FocusMatchesTotal != 3 {
		t.Fatalf("ambiguous response = %#v", ambiguous)
	}
	var listing bytes.Buffer
	writeImpactText(&listing, ambiguous)
	text := listing.String()
	if !strings.Contains(text, "--symbol State --file internal/scanner.c --line 562") ||
		!strings.Contains(text, "--symbol State --file internal/scanner.c --line 712") {
		t.Fatalf("listing did not offer a per-definition selector:\n%s", text)
	}

	flags, err := parseImpactFlags(append(
		strings.Fields("--symbol State --file internal/scanner.c --line 562"),
		"--depth", "2", "--limit", "15"))
	if err != nil {
		t.Fatal(err)
	}
	resolved := buildImpactResponseOnDisk(snapshot, flags)
	if resolved.DisambiguationRequired {
		t.Fatalf("printed selector did not disambiguate: %#v", resolved)
	}
	if resolved.Focus == nil || resolved.Focus.ID != "state-early" {
		t.Fatalf("focus = %#v", resolved.Focus)
	}
	if resolved.Callers.Total != 1 || resolved.Callers.Entries[0].Endpoint.ID != "caller" {
		t.Fatalf("resolved callers = %#v", resolved.Callers)
	}
}

func TestNeighborsAcceptsSameEscapeHatches(t *testing.T) {
	t.Parallel()
	snapshot := ambiguitySnapshot()
	flags, err := parseNeighborFlags([]string{
		"--symbol", "handler", "--file", "internal/provider.go", "--line", "40", "--kind", "tool",
		"--relation", "CALLS", "--limit", "20", "--depth", "1", "--direction", "both",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := buildNeighborResponseOnDisk(snapshot, flags)
	if response.DisambiguationRequired || len(response.Matches) != 1 {
		t.Fatalf("neighbors did not disambiguate: %#v", response)
	}
	if response.Matches[0].Symbol.ID != "handler-tool" {
		t.Fatalf("focus = %#v", response.Matches[0].Symbol)
	}
	if response.Line != 40 || response.File != "internal/provider.go" {
		t.Fatalf("response did not echo the address: file=%q line=%d", response.File, response.Line)
	}
}

func TestNoFocusMatchMessageOffersAWorkingNextStep(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		query string
		file  string
		line  int
		want  []string
	}{
		{
			name: "bare name", query: "Missing",
			want: []string{`No symbols matched "Missing"`, "entire graph search"},
		},
		{
			name: "narrowed by file", query: "Missing", file: "a.go",
			want: []string{"in a.go", "Drop --file"},
		},
		{
			name: "narrowed by line", query: "Missing", line: 7,
			want: []string{"at line 7", "Drop --line"},
		},
		{
			name: "narrowed by both", query: "Missing", file: "a.go", line: 7,
			want: []string{"in a.go at line 7", "Drop --line"},
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			writeNoFocusMatch(&out, testCase.query, testCase.file, testCase.line)
			text := out.String()
			for _, want := range testCase.want {
				if !strings.Contains(text, want) {
					t.Fatalf("message %q does not contain %q", text, want)
				}
			}
			// The old message suggested --file, which cannot turn zero matches into one.
			if strings.Contains(text, "Add --file to disambiguate") {
				t.Fatalf("message still suggests an impossible remedy: %q", text)
			}
		})
	}
}
