package sem

import (
	"strings"
	"testing"
)

func TestSearchSignatureParameterCountDropsReceivers(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		member    string
		signature string
		want      int
		wantKnown bool
	}{
		{name: "java no-arg", member: "toString", signature: "public String toString()", want: 0, wantKnown: true},
		{name: "java one arg", member: "equals", signature: "public boolean equals(Object other)", want: 1, wantKnown: true},
		{
			// Go puts the receiver first, so the parameter list is the SECOND group.
			name: "go method with a receiver", member: "String",
			signature: "func (o Ops) String() string", want: 0, wantKnown: true,
		},
		{name: "python receiver", member: "__repr__", signature: "def __repr__(self)", want: 0, wantKnown: true},
		{
			name: "rust formatter", member: "fmt",
			signature: "fn fmt(&self, f: &mut Formatter) -> Result", want: 1, wantKnown: true,
		},
		{
			name: "a generic argument is one argument", member: "set",
			signature: "public void set(Map<String, List<Integer>> value)",
			want:      1, wantKnown: true,
		},
		{
			name: "two arguments", member: "compute",
			signature: "double compute(double lhs, double rhs)", want: 2, wantKnown: true,
		},
		{name: "no parameter list to count", member: "Level", signature: "type Level int", wantKnown: false},
		{name: "unbalanced", member: "broken", signature: "func broken(a int", wantKnown: false},
		{
			// A name the signature does not spell falls back to the first group, which is what a
			// signature without a name looks like anyway.
			name: "an unnamed member falls back to the first group", member: "",
			signature: "(a int, b int)", want: 2, wantKnown: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, known := searchSignatureParameterCount(testCase.signature, testCase.member)
			if known != testCase.wantKnown {
				t.Fatalf("known = %v, want %v", known, testCase.wantKnown)
			}
			if known && got != testCase.want {
				t.Fatalf("parameters = %d, want %d", got, testCase.want)
			}
		})
	}
}

func TestSearchAccessorShapeRequiresAWordBoundary(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name         string
		symbol       string
		wantProperty string
		wantArity    int
		wantOK       bool
	}{
		{name: "getter", symbol: "getTotal", wantProperty: "Total", wantArity: 0, wantOK: true},
		{name: "setter", symbol: "setTotal", wantProperty: "Total", wantArity: 1, wantOK: true},
		{name: "predicate", symbol: "isEmpty", wantProperty: "Empty", wantArity: 0, wantOK: true},
		{name: "snake case getter", symbol: "get_total", wantProperty: "total", wantArity: 0, wantOK: true},
		{name: "a word starting with the prefix is not an accessor", symbol: "getter"},
		{name: "settle is not a setter", symbol: "settle"},
		{name: "the bare prefix is not an accessor", symbol: "get"},
		{name: "an unrelated name", symbol: "compute"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			property, arity, ok := searchAccessorShape(testCase.symbol)
			if ok != testCase.wantOK {
				t.Fatalf("ok = %v, want %v (property %q)", ok, testCase.wantOK, property)
			}
			if !ok {
				return
			}
			if property != testCase.wantProperty || arity != testCase.wantArity {
				t.Fatalf("shape = %q/%d, want %q/%d", property, arity, testCase.wantProperty, testCase.wantArity)
			}
		})
	}
}

func TestSearchBoilerplateSymbolNeedsBothNameAndShape(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		result SearchResult
		query  string
		want   bool
	}{
		{
			name: "java toString",
			result: SearchResult{Kind: "method", SymbolName: "toString",
				Signature: "public String toString()", StartLine: 10, EndLine: 14},
			query: "average aggregator name is wrong", want: true,
		},
		{
			name: "go Stringer",
			result: SearchResult{Kind: "method", SymbolName: "String",
				Signature: "func (l Level) String() string", StartLine: 10, EndLine: 12},
			query: "level naming is wrong", want: true,
		},
		{
			name: "rust Display formatter",
			result: SearchResult{Kind: "method", SymbolName: "fmt",
				Signature: "fn fmt(&self, f: &mut Formatter) -> Result", StartLine: 10, EndLine: 12},
			query: "kind naming is wrong", want: true,
		},
		{
			// Same name, wrong arity: this is a method that happens to share the protocol's name.
			name: "a toString taking an argument is not the protocol member",
			result: SearchResult{Kind: "method", SymbolName: "toString",
				Signature: "public String toString(Formatter f)", StartLine: 10, EndLine: 40},
			query: "average aggregator name is wrong", want: false,
		},
		{
			name: "trivial getter",
			result: SearchResult{Kind: "method", SymbolName: "getTotal",
				Signature: "public double getTotal()", StartLine: 10, EndLine: 12},
			query: "aggregation sum is wrong", want: true,
		},
		{
			// The shape rule, not the naming rule: a long accessor is doing something.
			name: "a long getter is a fix site",
			result: SearchResult{Kind: "method", SymbolName: "getTotal",
				Signature: "public double getTotal()", StartLine: 10, EndLine: 60},
			query: "aggregation sum is wrong", want: false,
		},
		{
			name: "the query names the property the accessor exposes",
			result: SearchResult{Kind: "method", SymbolName: "getTotal",
				Signature: "public double getTotal()", StartLine: 10, EndLine: 12},
			query: "the total is wrong", want: false,
		},
		{
			name: "a container is never boilerplate",
			result: SearchResult{Kind: "class", SymbolName: "toString",
				Signature: "class toString", StartLine: 1, EndLine: 40},
			query: "aggregation naming", want: false,
		},
		{
			name: "compareTo is real logic",
			result: SearchResult{Kind: "method", SymbolName: "compareTo",
				Signature: "public int compareTo(Ops other)", StartLine: 10, EndLine: 14},
			query: "operator ordering is wrong", want: false,
		},
		{
			name: "a go Error method is real logic",
			result: SearchResult{Kind: "method", SymbolName: "Error",
				Signature: "func (e parseError) Error() string", StartLine: 10, EndLine: 12},
			query: "the parse message is wrong", want: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := searchBoilerplateSymbol(testCase.result, buildSearchQuery(testCase.query))
			if got != testCase.want {
				t.Fatalf("boilerplate = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestApplySearchBoilerplatePriorIsRevokedByAskingAboutIt(t *testing.T) {
	t.Parallel()
	toString := SearchResult{Kind: "method", SymbolName: "toString",
		Signature: "public String toString()", StartLine: 10, EndLine: 14}
	for _, testCase := range []struct {
		name       string
		query      string
		wantScore  float64
		wantSignal bool
	}{
		{
			name: "a domain query deranks it", query: "average aggregation field names",
			wantScore: 10 * searchBoilerplatePrior, wantSignal: true,
		},
		{
			name: "a query about rendering does not", query: "toString drops the leading zero",
			wantScore: 10,
		},
		{
			name: "a query about formatting does not", query: "the debug formatting of an aggregator",
			wantScore: 10,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			candidates := []searchCandidate{{result: toString, score: 10}}
			applySearchBoilerplatePrior(candidates, buildSearchQuery(testCase.query))
			if candidates[0].score != testCase.wantScore {
				t.Fatalf("score = %v, want %v", candidates[0].score, testCase.wantScore)
			}
			hasSignal := false
			for _, signal := range candidates[0].result.Signals {
				if signal == searchBoilerplateSignal {
					hasSignal = true
				}
			}
			if hasSignal != testCase.wantSignal {
				t.Fatalf("signal present = %v, want %v", hasSignal, testCase.wantSignal)
			}
		})
	}
}

func TestSearchDeranksBoilerplateBelowTheRealFixSiteEndToEnd(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	// The measured magnet, reproduced: every type in the package has a `String()` whose body is a
	// list of the domain's field names, so all of them match any query that names the domain — while
	// none of them can be the fix. One file holds the code that actually computes it.
	for _, name := range []string{"sum", "min", "max"} {
		writeFile(t, repo, name+"agg.go", `package agg

import "fmt"

type `+name+`Agg struct {
	quantileValue float64
}

func (a `+name+`Agg) String() string {
	return fmt.Sprintf("`+name+`Agg{quantileValue=%v}", a.quantileValue)
}
`)
	}
	writeFile(t, repo, "quantile.go", `package agg

func computeQuantileValue(samples []float64, fraction float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	index := int(fraction * float64(len(samples)))
	if index >= len(samples) {
		index = len(samples) - 1
	}
	return samples[index]
}
`)
	response, err := SearchRepository(t.Context(), repo, "test-version",
		"computeQuantileValue returns the wrong quantileValue",
		SearchOptions{Worktree: true, Profile: ProfileFull, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) == 0 {
		t.Fatal("no results")
	}
	if top := response.Results[0]; top.SymbolName == "String" {
		t.Fatalf("a protocol member outranked the implementation: %s:%d %s",
			top.FilePath, top.StartLine, top.SymbolName)
	}
	deranked, stringers := 0, 0
	for _, result := range response.Results {
		if result.SymbolName == "String" {
			stringers++
		}
		for _, signal := range result.Signals {
			if signal == searchBoilerplateSignal {
				deranked++
			}
		}
	}
	if stringers == 0 {
		t.Fatal("no String() in the payload, so this repository does not exercise the prior")
	}
	if deranked != stringers {
		t.Fatalf("%d of %d String() members carry the derank signal", deranked, stringers)
	}
}

func TestSearchSplitTopLevelIgnoresNestedCommas(t *testing.T) {
	t.Parallel()
	got := searchSplitTopLevel("Map<String, Integer> a, List<int[]> b")
	if len(got) != 2 || !strings.Contains(got[0], "Map<String, Integer>") {
		t.Fatalf("split = %q", got)
	}
}

// A rule-configuration array is a DATA TABLE, not behaviour. Measured on php-cs-fixer-7523: two
// `getRules()` bodies tied at rank 1-2 (score 37.1786 each) and pushed the gold tokenizer file off
// the list; the agent then blind-swept it for 77,718 B.
func TestLiteralTableBodyIsTreatedAsBoilerplate(t *testing.T) {
	t.Parallel()
	table := SearchResult{
		SymbolName: "PSR12Set.getRules", Kind: "method",
		Snippet: "return [\n" +
			"    'binary_operator_spaces' => true,\n" +
			"    'blank_line_after_opening_tag' => true,\n" +
			"    'compact_nullable_typehint' => true,\n" +
			"    'declare_equal_normalize' => ['space' => 'none'],\n" +
			"    'lowercase_cast' => true,\n" +
			"    'no_blank_lines_after_class_opening' => true,\n" +
			"    'no_leading_import_slash' => true,\n" +
			"];",
	}
	if !searchLiteralTableBody(table) {
		t.Fatal("a dense branch-free literal table must be detected")
	}
	// A body with control flow is a decision, never a table — this is where fixes live.
	decision := SearchResult{
		SymbolName: "Analyzer.isBinaryOperator", Kind: "method",
		Snippet: "if ($token->isGivenKind(T_STRING)) {\n" +
			"    return false,\n" +
			"}\n" +
			"foreach ($tokens as $index => $token) {\n" +
			"    $prev = $tokens->getPrevMeaningfulToken($index),\n" +
			"    $next = $tokens->getNextMeaningfulToken($index),\n" +
			"    $result = $this->check($prev, $next),\n" +
			"}\n" +
			"return $result;",
	}
	if searchLiteralTableBody(decision) {
		t.Fatal("a body containing control flow must NOT be treated as a table")
	}
	// Too short to be a table: a two-entry return is an ordinary early return.
	short := SearchResult{SymbolName: "x", Kind: "method", Snippet: "return [\n  'a' => 1,\n  'b' => 2,\n];"}
	if searchLiteralTableBody(short) {
		t.Fatal("a short literal must not be treated as a table")
	}
}
