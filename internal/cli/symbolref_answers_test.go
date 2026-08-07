package cli

import (
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// answerFixtureSymbols is the measured shape of both failures in one inventory: a name declared twice
// (lombok-3486) and a name the caller will spell differently (phpspreadsheet-3570).
func answerFixtureSymbols() []sem.SymbolRecord {
	return []sem.SymbolRecord{
		{ID: "ecl", Kind: "method", Name: "generateBuilderAbstractClass",
			QualifiedName: "HandleSuperBuilder.generateBuilderAbstractClass",
			FilePath:      "src/core/lombok/eclipse/handlers/HandleSuperBuilder.java", StartLine: 476, EndLine: 504},
		{ID: "jav", Kind: "method", Name: "generateBuilderAbstractClass",
			QualifiedName: "HandleSuperBuilder.generateBuilderAbstractClass",
			FilePath:      "src/core/lombok/javac/handlers/HandleSuperBuilder.java", StartLine: 454, EndLine: 480},
		{ID: "flat", Kind: "method", Name: "flattenSingleValue",
			QualifiedName: "Functions.flattenSingleValue",
			FilePath:      "src/PhpSpreadsheet/Calculation/Functions.php", StartLine: 619, EndLine: 626},
		{ID: "single", Kind: "class", Name: "Single", QualifiedName: "Single",
			FilePath: "src/PhpSpreadsheet/Calculation/Financial/CashFlow/Single.php", StartLine: 9, EndLine: 48},
		{ID: "deletion", Kind: "function", Name: "deletion", QualifiedName: "deletion",
			FilePath: "pkg/edit.go", StartLine: 3, EndLine: 9},
	}
}

// TestResolveFocusSymbolsOrFuzzyNeverAnswersEmpty is FIX B. Each case is a spelling an agent actually
// used, and the ladder has to reach the right symbol without inventing matches for a short fragment.
func TestResolveFocusSymbolsOrFuzzyNeverAnswersEmpty(t *testing.T) {
	t.Parallel()
	symbols := answerFixtureSymbols()
	for _, testCase := range []struct {
		name      string
		query     string
		wantFirst string
		wantFuzzy bool
		wantTier  symbolMatchTier
		wantNone  bool
	}{
		{name: "an exact match is not fuzzy", query: "flattenSingleValue",
			wantFirst: "flat", wantFuzzy: false, wantTier: symbolMatchExact},
		{
			// resolveFocusSymbols already compares names with EqualFold, so pure case difference is an
			// EXACT match and never reaches the ladder. Pinned so the rung above it is not mistaken for
			// the thing that makes case work.
			name: "case only is already exact", query: "flattensinglevalue",
			wantFirst: "flat", wantFuzzy: false, wantTier: symbolMatchExact,
		},
		{name: "snake_case for a camelCase name — the measured phpspreadsheet miss",
			query: "flatten_single_value", wantFirst: "flat", wantFuzzy: true,
			wantTier: symbolMatchSeparatorInsensitive},
		{name: "the qualifier the caller did not know about",
			query: "Functions::flattenSingleValue", wantFirst: "flat", wantFuzzy: true,
			wantTier: symbolMatchSeparatorInsensitive},
		{name: "a dropped qualifier still reaches the member",
			query: "handleSuperBuilder.generate_builder_abstract_class", wantFirst: "ecl",
			wantFuzzy: true, wantTier: symbolMatchSeparatorInsensitive},
		{
			// The standing def invariant: a short fragment must never resolve to a longer name.
			name: "a four-letter fragment matches nothing", query: "dele", wantNone: true,
		},
		{name: "a name nothing shares a token with matches nothing",
			query: "zzzznotpresent", wantNone: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			ref := parseSymbolRef(testCase.query, "", 0, "", "", nil)
			matches, tier, fuzzy := resolveFocusSymbolsOrFuzzy(symbols, ref, symbolFuzzyCandidateLimit)
			if testCase.wantNone {
				if len(matches) != 0 {
					t.Fatalf("query %q matched %d symbols, want none (first %s)",
						testCase.query, len(matches), matches[0].ID)
				}
				return
			}
			if len(matches) == 0 {
				t.Fatalf("query %q returned nothing — FIX B must never answer empty", testCase.query)
			}
			if matches[0].ID != testCase.wantFirst {
				t.Fatalf("first match = %s, want %s (order is the answer for a fuzzy result)",
					matches[0].ID, testCase.wantFirst)
			}
			if fuzzy != testCase.wantFuzzy || tier != testCase.wantTier {
				t.Fatalf("fuzzy=%v tier=%s, want %v/%s", fuzzy, tier.label(),
					testCase.wantFuzzy, testCase.wantTier.label())
			}
			if len(matches) > symbolFuzzyCandidateLimit {
				t.Fatalf("returned %d candidates, over the limit of %d",
					len(matches), symbolFuzzyCandidateLimit)
			}
		})
	}
}

// TestFuzzyResolutionHonoursTheCallersNarrowing pins that --file/--kind still apply: a fuzzy NAME plus
// an exact file is a much better answer than a fuzzy name alone.
func TestFuzzyResolutionHonoursTheCallersNarrowing(t *testing.T) {
	t.Parallel()
	symbols := answerFixtureSymbols()
	ref := parseSymbolRef("generate_builder_abstract_class",
		"src/core/lombok/javac/handlers/HandleSuperBuilder.java", 0, "", "", nil)
	matches, _, fuzzy := resolveFocusSymbolsOrFuzzy(symbols, ref, symbolFuzzyCandidateLimit)
	if !fuzzy || len(matches) != 1 || matches[0].ID != "jav" {
		t.Fatalf("fuzzy=%v matches=%d, want exactly the javac definition", fuzzy, len(matches))
	}
	kinded := parseSymbolRef("flatten_single_value", "", 0, "class", "", nil)
	if matches, _, _ := resolveFocusSymbolsOrFuzzy(symbols, kinded, symbolFuzzyCandidateLimit); len(matches) > 0 &&
		matches[0].Kind != "class" {
		t.Fatalf("--kind class was ignored: got %s", matches[0].Kind)
	}
}

// TestSymbolIdentifierTokensSplitsEverySpelling pins the tokenizer the ladder is built on.
func TestSymbolIdentifierTokensSplitsEverySpelling(t *testing.T) {
	t.Parallel()
	want := map[string]bool{"flatten": true, "single": true, "value": true}
	for _, spelling := range []string{
		"flattenSingleValue", "flatten_single_value", "FLATTEN_SINGLE_VALUE",
		"flatten-single-value", "Flatten::Single::Value", "flatten.single.value",
	} {
		got := symbolIdentifierTokens(spelling)
		if len(got) != len(want) {
			t.Fatalf("%q -> %v, want %v", spelling, got, want)
		}
		for token := range want {
			if !got[token] {
				t.Fatalf("%q -> %v, missing %q", spelling, got, token)
			}
		}
	}
	// A capital run ends a word at its last capital: HTTPServer is http + server, not h+t+t+p+server.
	if got := symbolIdentifierTokens("HTTPServer"); !got["http"] || !got["server"] || len(got) != 2 {
		t.Fatalf("HTTPServer -> %v, want {http, server}", got)
	}
}

// TestWriteDisambiguationListingAnswersInsteadOfRefusing is FIX A at the renderer: every definition,
// every selector, source for the top ones, and NO instruction to re-run.
func TestWriteDisambiguationListingAnswersInsteadOfRefusing(t *testing.T) {
	t.Parallel()
	definitions := []neighborEndpoint{
		{Name: "generateBuilderAbstractClass", QualifiedName: "HandleSuperBuilder.generateBuilderAbstractClass",
			Kind: "method", FilePath: "eclipse/HandleSuperBuilder.java", StartLine: 476},
		{Name: "generateBuilderAbstractClass", QualifiedName: "HandleSuperBuilder.generateBuilderAbstractClass",
			Kind: "method", FilePath: "javac/HandleSuperBuilder.java", StartLine: 454},
	}
	bodies := []symbolMatchBody{
		{Name: "HandleSuperBuilder.generateBuilderAbstractClass", Kind: "method",
			FilePath: "eclipse/HandleSuperBuilder.java", StartLine: 476, EndLine: 478,
			Source: "private EclipseNode generateBuilderAbstractClass() {\n\treturn null;\n}"},
	}
	var out strings.Builder
	writeDisambiguationListing(&out, "HandleSuperBuilder.generateBuilderAbstractClass", 2, definitions, bodies)
	rendered := out.String()
	// The refusal is gone. This is the measured +$2.22 instruction.
	for _, forbidden := range []string{"rerun", "Ambiguous symbol"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("listing still refuses (%q):\n%s", forbidden, rendered)
		}
	}
	for _, want := range []string{
		"matches 2 definitions", "eclipse/HandleSuperBuilder.java:476", "javac/HandleSuperBuilder.java:454",
		"--symbol HandleSuperBuilder.generateBuilderAbstractClass --file eclipse/HandleSuperBuilder.java --line 476",
		"private EclipseNode generateBuilderAbstractClass() {",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("listing is missing %q:\n%s", want, rendered)
		}
	}
}

// TestWriteFuzzyMatchListingSaysHowItMatched pins FIX B's labelling. Silently returning a different
// symbol than the one asked for would be worse than the empty answer it replaced.
func TestWriteFuzzyMatchListingSaysHowItMatched(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	writeFuzzyMatchListing(&out, "flatten_single_value", symbolMatchSeparatorInsensitive,
		[]neighborEndpoint{{Name: "flattenSingleValue", QualifiedName: "Functions.flattenSingleValue",
			Kind: "method", FilePath: "Functions.php", StartLine: 619}},
		[]symbolMatchBody{{Name: "Functions.flattenSingleValue", FilePath: "Functions.php",
			StartLine: 619, EndLine: 620, Source: "public static function flattenSingleValue($value = '')\n{"}})
	rendered := out.String()
	for _, want := range []string{
		`No exact match for "flatten_single_value"`, "separator-insensitive match",
		"Functions.php:619", "public static function flattenSingleValue",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("fuzzy listing is missing %q:\n%s", want, rendered)
		}
	}
	// Round-trip the label so a response carrying it renders the same rung it was resolved at.
	if got := symbolMatchTierFromLabel(symbolMatchSeparatorInsensitive.label()); got != symbolMatchSeparatorInsensitive {
		t.Fatalf("label round-trip gave %s", got.label())
	}
	if fuzzyKindLabel(false, symbolMatchSubstring) != "" {
		t.Fatal("an exact match reported a fuzzy rung")
	}
}
