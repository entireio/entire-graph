package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

// The guide tells agents to run `def --max-context-bytes 4096 <Name>`. Before this was pinned, only the
// declaration CARD honoured that number: the source bodies printed after it were unbounded, so an
// ambiguous or fuzzy name came back with up to four bodies of up to 400 lines each (measured 23 KB for
// `def State` on the cli repo), and `--format json` ignored the flag entirely (29 KB). Every case below
// first proves that the fixture overflows without a budget, so a green run cannot be a vacuous one.

// defBudgetRepo has five same-named functions with long bodies (ambiguous `Run`, and fuzzy `RUN`) and one
// type with many long-signature members (`Widget`, the JSON overflow).
func defBudgetRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	for _, pkg := range []string{"p1", "p2", "p3", "p4", "p5"} {
		var body strings.Builder
		fmt.Fprintf(&body, "package %s\n\nfunc Run() int {\n", pkg)
		for line := range 150 {
			fmt.Fprintf(&body, "\tpadding%03d := %d // a line long enough that a body costs real bytes\n\t_ = padding%03d\n", line, line, line)
		}
		body.WriteString("\treturn 0\n}\n")
		write(t, repo, pkg+"/run.go", body.String())
	}
	var widget strings.Builder
	widget.WriteString("package widget\n\ntype Widget struct {\n")
	for field := range 40 {
		fmt.Fprintf(&widget, "\tFieldNumber%02d map[string][]map[string]*WidgetConfigurationEntry\n", field)
	}
	widget.WriteString("}\n\ntype WidgetConfigurationEntry struct{}\n\n")
	for method := range 30 {
		fmt.Fprintf(&widget, "func (w *Widget) MethodNumber%02d(argumentOne string, argumentTwo map[string][]int) (map[string]int, error) {\n\treturn nil, nil\n}\n\n", method)
	}
	write(t, repo, "widget/widget.go", widget.String())
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

func runDefForBudget(t *testing.T, repo, cacheDir string, args ...string) string {
	t.Helper()
	stdout, _ := runVerb(t, repo, cacheDir, append([]string{"def", "--head"}, args...))
	return stdout
}

func TestDefHonoursTheByteBudgetOnEveryOutputPath(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	repo := defBudgetRepo(t)
	cacheDir := t.TempDir()
	cases := []struct {
		name   string
		budget int
		args   []string
		// want is text the budgeted answer must still carry: the identity, and the count of what it left out.
		want []string
	}{
		{"text ambiguous", 2048, []string{"Run"}, []string{"p1/run.go:3", "not shown within --max-context-bytes 2048"}},
		// `run_` misses exactly and lands on the fuzzy ladder (separator-insensitive), which returns bodies.
		{"text fuzzy", 2048, []string{"run_"}, []string{`No exact match for "run_"`, "p1/run.go:3"}},
		{"text single long body", 2048, []string{"Run", "--file", "p1/run.go"}, []string{"p1/run.go:3", "rerun with --file p1/run.go --from"}},
		{"text multi-name", 2048, []string{"Run", "Widget", "run_"}, []string{"== Run ==", "== Widget ==", "== run_ =="}},
		{"json ambiguous", 1200, []string{"Run", "--format", "json"}, []string{`"budget_omitted"`, `"declarations_total":5`}},
		{"json fuzzy", 1200, []string{"run_", "--format", "json"}, []string{`"budget_omitted"`, `"fuzzy_match_kind"`}},
		{"json members", 2048, []string{"Widget", "--format", "json"}, []string{`"budget_omitted"`, `"methods_total":30`}},
		{"json multi-name", 3000, []string{"Run", "Widget", "run_", "--format", "json"}, []string{`"budget_omitted"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unbounded := runDefForBudget(t, repo, cacheDir, append(append([]string{}, tc.args...), "--max-context-bytes", "0")...)
			if len(unbounded) <= tc.budget {
				t.Fatalf("fixture does not overflow: %d bytes unbounded, budget %d", len(unbounded), tc.budget)
			}
			got := runDefForBudget(t, repo, cacheDir, append(append([]string{}, tc.args...), "--max-context-bytes", fmt.Sprint(tc.budget))...)
			if len(got) > tc.budget {
				t.Fatalf("%d bytes at --max-context-bytes %d (unbounded %d):\n%s", len(got), tc.budget, len(unbounded), got)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("budgeted answer lost %q:\n%s", want, got)
				}
			}
			if strings.Contains(strings.Join(tc.args, " "), "json") {
				assertDefJSONStream(t, got)
			}
		})
	}
}

// TestDefBudgetHoldsAcrossBudgets sweeps budgets, so the reserve arithmetic is held at every size, including
// budgets smaller than any note.
func TestDefBudgetHoldsAcrossBudgets(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	repo := defBudgetRepo(t)
	cacheDir := t.TempDir()
	for _, budget := range []int{40, 200, 700, 1500, 3000, 5000} {
		for _, args := range [][]string{{"Run"}, {"run_"}, {"Run", "--file", "p2/run.go"}, {"Run", "Widget"}} {
			got := runDefForBudget(t, repo, cacheDir, append(append([]string{}, args...), "--max-context-bytes", fmt.Sprint(budget))...)
			if len(got) > budget {
				t.Errorf("def %v: %d bytes at budget %d", args, len(got), budget)
			}
		}
	}
}

func assertDefJSONStream(t *testing.T, stream string) {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(stream))
	documents := 0
	for {
		var response defResponse
		err := decoder.Decode(&response)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("budgeted JSON is not a valid document stream: %v\n%s", err, stream)
		}
		documents++
		if response.BudgetOmitted != nil {
			if !response.Truncated {
				t.Errorf("budget_omitted without truncated: %+v", response)
			}
			if !strings.Contains(response.BudgetOmitted.Note, "narrow with --file") {
				t.Errorf("omission note does not say how to narrow: %q", response.BudgetOmitted.Note)
			}
		}
	}
	if documents == 0 {
		t.Fatalf("no JSON documents in %q", stream)
	}
}

// TestDefJSONRefusesABudgetItCannotMeet pins the floor: below the smallest valid document the answer is
// an error, never a byte-cut JSON document that no consumer can parse.
func TestDefJSONRefusesABudgetItCannotMeet(t *testing.T) {
	t.Parallel()
	response := buildDefResponse(defFixtureSnapshot(), defFlags{Symbol: "Edit", MemberLimit: defaultDefMemberLimit})
	if _, err := fitDefJSON(response, 50, 50); err == nil || !strings.Contains(err.Error(), "smallest JSON answer") {
		t.Fatalf("err = %v", err)
	}
	full, err := fitDefJSON(response, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(full, []byte("budget_omitted")) {
		t.Fatalf("an unbounded answer reported an omission:\n%s", full)
	}
}

func TestDefNameBudgetSplitsTheTotal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ budget, names, want int }{
		{4096, 1, 4096}, {4096, 3, 1365}, {0, 3, 0}, {2, 5, 1},
	} {
		if got := defNameBudget(tc.budget, tc.names); got != tc.want {
			t.Errorf("defNameBudget(%d, %d) = %d, want %d", tc.budget, tc.names, got, tc.want)
		}
	}
}
