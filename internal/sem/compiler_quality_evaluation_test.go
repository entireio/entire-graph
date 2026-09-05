package sem

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/compiler"
)

type qualityCounts struct {
	Required  int      `json:"required"`
	Returned  int      `json:"returned"`
	TP        int      `json:"true_positive"`
	FP        int      `json:"false_positive"`
	Missed    int      `json:"missed"`
	Precision *float64 `json:"precision"`
	Recall    *float64 `json:"recall"`
}

func qualityScore(required, observed []string) qualityCounts {
	r, o := map[string]bool{}, map[string]bool{}
	for _, v := range required {
		r[v] = true
	}
	for _, v := range observed {
		o[v] = true
	}
	c := qualityCounts{Required: len(r), Returned: len(o)}
	for v := range o {
		if r[v] {
			c.TP++
		} else {
			c.FP++
		}
	}
	c.Missed = c.Required - c.TP
	if c.Required > 0 {
		v := float64(c.TP) / float64(c.Required)
		c.Recall = &v
	}
	if c.Returned > 0 {
		v := float64(c.TP) / float64(c.Returned)
		c.Precision = &v
	}
	return c
}
func TestLiveCompilerQualityEvaluationV1(t *testing.T) {
	if os.Getenv("ENTIRE_GRAPH_COMPILER_LIVE") != "1" {
		t.Skip("explicit frozen Linux quality evaluation")
	}
	files := map[string]string{
		"go.mod":             "module fixture.local/quality\n\ngo 1.24\n",
		"library/library.go": "package library\nfunc Target() {}\n",
		"main.go": `package quality
import renamed "fixture.local/quality/library"
type Worker interface { Work() }
type One struct{}
func (One) Work() {}
func (*One) Pointer() {}
type Two struct{}
func (Two) Work() {}
type Embedded struct { One }
func Generic[T any](value T) T { return value }
func Imported() { renamed.Target() }
func Promoted() { var e Embedded; e.Work() }
func GenericCall() { Generic[int](1) }
func Expression() { One.Work(One{}) }
func PointerCall() { var o One; o.Pointer() }
func Dispatch(w Worker) { w.Work() }
func Dynamic(f func()) { f() }
`,
	}
	type label struct{ Category, Caller, RequiredAnchor, Token, TargetFile string }
	labels := []label{
		{"aliased_import", "Imported", "func Target", "Target", "library/library.go"},
		{"promoted_method", "Promoted", "func (One) Work", "Work", "main.go"},
		{"generic_call", "GenericCall", "func Generic[", "Generic", "main.go"},
		{"method_expression", "Expression", "func (One) Work", "Work", "main.go"},
		{"pointer_receiver", "PointerCall", "func (*One) Pointer", "Pointer", "main.go"},
		{"interface_declaration", "Dispatch", "type Worker interface { Work", "Work", "main.go"},
		{"dynamic_parameter", "Dynamic", "", "", ""},
	}
	repo := t.TempDir()
	for name, body := range files {
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "quality-v1", ProviderSnapshotOptions{Worktree: true, Profile: "full"})
	if err != nil {
		t.Fatal(err)
	}
	declarations, calls := compilerTokens(files, snapshot.Symbols)
	symbolNames := map[string]string{}
	for _, s := range snapshot.Symbols {
		symbolNames[s.ID] = s.Name
	}
	key := func(path string, start, end int) string {
		b, _ := json.Marshal([]any{path, start, end})
		return string(b)
	}
	declarationKeys := map[string]string{}
	for _, d := range declarations {
		declarationKeys[d.symbol] = key(d.path, d.start, d.end)
	}
	expected := func(path, anchor, name string) string {
		start := strings.Index(files[path], anchor)
		if start < 0 {
			t.Fatalf("bad frozen label %s", anchor)
		}
		start += strings.LastIndex(anchor, name)
		return key(path, start, start+len(name))
	}
	queries := []compiler.Query{}
	for _, c := range calls {
		queries = append(queries, compiler.Query{Path: c.path, Offset: c.start, IncludeCandidates: true})
	}
	report := compiler.Analyze(context.Background(), compiler.Config{ServerPath: "/opt/graph-tools/gopls", ServerSHA256: "2b4652d6ac42a22942f63735d9c7e44e9dfbc1dade5d4fd09c0d4eb8fa3539b1", ToolchainRoot: "/usr/local/go", BubblewrapPath: "/usr/bin/bwrap"}, files, queries)
	overlay := reconcileCompiler(&snapshot, files, declarations, calls, report)
	type row struct {
		Category       string        `json:"category"`
		Caller         string        `json:"caller"`
		Required       []string      `json:"required"`
		Static         []string      `json:"static"`
		Compiler       []string      `json:"compiler"`
		StaticCounts   qualityCounts `json:"static_counts"`
		CompilerCounts qualityCounts `json:"compiler_counts"`
	}
	rows := []row{}
	bad := false
	for _, l := range labels {
		r := row{Category: l.Category, Caller: l.Caller, Required: []string{}, Static: []string{}, Compiler: []string{}}
		if l.RequiredAnchor != "" {
			r.Required = append(r.Required, expected(l.TargetFile, l.RequiredAnchor, l.Token))
		}
		caller := ""
		count := 0
		for _, c := range calls {
			if symbolNames[c.symbol] == l.Caller {
				caller = c.symbol
				count++
			}
		}
		if count != 1 {
			t.Fatalf("frozen caller must contain one call: %s=%d", l.Caller, count)
		}
		for _, rel := range snapshot.Relations {
			if rel.FromID == caller && (rel.Type == "CALLS" || rel.Type == "ASYNC_CALLS" || rel.Type == "CONSTRUCTS") {
				if k := declarationKeys[rel.ToID]; k != "" {
					r.Static = append(r.Static, k)
				}
			}
		}
		for _, item := range overlay.Calls {
			if item.SourceSymbolID == caller && item.Evidence.Category == compiler.DirectDeclaration {
				site := item.Evidence.Target
				r.Compiler = append(r.Compiler, key(site.Path, site.StartByte, site.EndByte))
			}
		}
		sort.Strings(r.Static)
		sort.Strings(r.Compiler)
		r.StaticCounts = qualityScore(r.Required, r.Static)
		r.CompilerCounts = qualityScore(r.Required, r.Compiler)
		if r.CompilerCounts.FP > 0 || r.CompilerCounts.Missed > 0 {
			bad = true
		}
		rows = append(rows, r)
	}
	candidates := row{Category: "interface_candidates", Caller: "Dispatch", Required: []string{expected("main.go", "func (One) Work", "Work"), expected("main.go", "func (Two) Work", "Work")}, Static: []string{}, Compiler: []string{}}
	for _, item := range overlay.Calls {
		if symbolNames[item.SourceSymbolID] == "Dispatch" && item.Evidence.Category == compiler.ImplementationCandidate {
			site := item.Evidence.Target
			candidates.Compiler = append(candidates.Compiler, key(site.Path, site.StartByte, site.EndByte))
		}
	}
	sort.Strings(candidates.Compiler)
	candidates.StaticCounts = qualityScore(candidates.Required, candidates.Static)
	candidates.CompilerCounts = qualityScore(candidates.Required, candidates.Compiler)
	if candidates.CompilerCounts.FP > 0 || candidates.CompilerCounts.Missed > 0 {
		bad = true
	}
	rows = append(rows, candidates)
	hashes := map[string]string{}
	for p, b := range files {
		hashes[p] = compiler.ContentDigest(b)
	}
	result := map[string]any{"manifest": "compiler-quality-v1", "label_origin": "hand-derived contract fixtures; compiler-checked, not independently human-adjudicated", "source": files, "static_relations": snapshot.Relations, "overlay_calls": overlay.Calls, "source_sha256": hashes, "rows": rows, "report": overlay.Report, "compiler_contract_pass": !bad}
	bytes, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out := os.Getenv("ENTIRE_GRAPH_COMPILER_QUALITY_OUTPUT")
	if out != "" {
		if err := os.WriteFile(out, append(bytes, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Log(string(bytes))
	}
	if bad {
		t.Fatal("compiler quality contract failure; frozen results retained")
	}
}
