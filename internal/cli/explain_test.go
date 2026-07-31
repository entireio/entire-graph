package cli

import (
	"strings"
	"testing"
)

// The extractor is the part that decides whether this command is useful at all: a build failure that
// names a symbol it cannot find is the case, and every language spells it differently. Each line here
// is real compiler/runner output, not a paraphrase.
func TestExplainCandidateNamesAcrossLanguages(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		text string
		want []string
	}{
		{"go undefined", "./x.go:12:2: undefined: loadTestFiles", []string{"loadTestFiles"}},
		{"go no field", "./x.go:9:5: p.cfg undefined (type *Parser has no field or method cfg)", []string{"cfg"}},
		{"go unused", "./x.go:14:2: declared and not used: leftover", []string{"leftover"}},
		{"rust cannot find", "error[E0425]: cannot find function `simplify_ring` in this scope", []string{"simplify_ring"}},
		{"rust no method", "error[E0599]: no method named `is_point_in_line` found", []string{"is_point_in_line"}},
		{"java symbol", "Foo.java:8: error: cannot find symbol\n  symbol:   method refreshCache()", []string{"refreshCache"}},
		{"ts property", "src/a.ts(4,7): error TS2339: Property 'brokenLinks' does not exist on type 'Cfg'.", []string{"brokenLinks"}},
		{"js not defined", "ReferenceError: resolveAsset is not defined", []string{"resolveAsset"}},
		{"python attribute", "AttributeError: 'Sheet' object has no attribute 'calcFormula'", []string{"calcFormula"}},
		{"php undefined method", "Error: Call to undefined method RuleSet::getRuleNames()", []string{"getRuleNames"}},
		{"ruby undefined method", "NoMethodError: undefined method `redundant_freeze' for nil", []string{"redundant_freeze"}},
		{"go arity", "./x.go:3:9: not enough arguments in call to parser.loadFiles", []string{"loadFiles"}},
		{"qualified reduces to last segment", "undefined: configs.LoadConfigDir", []string{"LoadConfigDir"}},
		{"prose is not a symbol", "All tests passed. 42 examples, 0 failures", nil},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := explainCandidateNames(testCase.text, 8)
			if len(got) != len(testCase.want) {
				t.Fatalf("names: got %v want %v", got, testCase.want)
			}
			for i := range got {
				if got[i] != testCase.want[i] {
					t.Fatalf("names: got %v want %v", got, testCase.want)
				}
			}
		})
	}
}

// Order carries the root cause: a build reports the failure that caused the cascade first, so the
// extractor must not sort or dedupe away that ordering.
func TestExplainCandidateNamesKeepsFirstSeenOrderAndDedupes(t *testing.T) {
	t.Parallel()
	text := strings.Join([]string{
		"./a.go:1:1: undefined: alpha",
		"./b.go:2:2: undefined: beta",
		"./c.go:3:3: undefined: alpha",
	}, "\n")
	got := explainCandidateNames(text, 8)
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("got %v want [alpha beta]", got)
	}
}

func TestExplainCandidateNamesRespectsLimit(t *testing.T) {
	t.Parallel()
	var lines []string
	for _, n := range []string{"a1", "b2", "c3", "d4", "e5"} {
		lines = append(lines, "undefined: sym"+n)
	}
	if got := explainCandidateNames(strings.Join(lines, "\n"), 2); len(got) != 2 {
		t.Fatalf("limit ignored: got %v", got)
	}
}

// An unresolved name must be REPORTED, not dropped: "this repository does not define it" is the
// answer when the agent has invented a method, and silence would read as "no information".
func TestRenderExplainReportsUnresolvedNames(t *testing.T) {
	t.Parallel()
	out := string(RenderExplain(ExplainResponse{Scanned: 2, Symbols: []ExplainSymbol{
		{Query: "realOne", Name: "realOne", Kind: "function", FilePath: "a.go", StartLine: 3, EndLine: 9, Resolved: true},
		{Query: "invented"},
	}}, 2048))
	if !strings.Contains(out, ExplainHeader) {
		t.Fatalf("missing header: %q", out)
	}
	if !strings.Contains(out, "a.go:3-9 function realOne") {
		t.Fatalf("missing resolved entry: %q", out)
	}
	if !strings.Contains(out, "not defined in this repository: invented") {
		t.Fatalf("unresolved name not reported: %q", out)
	}
}

// Nothing extractable must render nothing at all — a header over an empty list would assert that the
// build named symbols when it did not.
func TestRenderExplainSilentWhenNothingResolved(t *testing.T) {
	t.Parallel()
	if got := RenderExplain(ExplainResponse{}, 2048); got != nil {
		t.Fatalf("expected silence, got %q", got)
	}
}
