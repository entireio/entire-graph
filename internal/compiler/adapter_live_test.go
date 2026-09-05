package compiler

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func liveConfig(t *testing.T) Config {
	t.Helper()
	if runtime.GOOS != "linux" || os.Getenv("ENTIRE_GRAPH_COMPILER_LIVE") != "1" {
		t.Skip("explicit Linux isolated live fixture run")
	}
	return Config{ServerPath: "/opt/graph-tools/gopls", ServerSHA256: "2b4652d6ac42a22942f63735d9c7e44e9dfbc1dade5d4fd09c0d4eb8fa3539b1", ToolchainRoot: "/usr/local/go", BubblewrapPath: "/usr/bin/bwrap"}
}
func TestLiveCompilerDirectAndCandidate(t *testing.T) {
	config := liveConfig(t)
	body := `package p
 type Worker interface { Work() }
 type One struct{}
 func (One) Work() {}
 type Two struct{}
 func (Two) Work() {}
 type Embedded struct { One }
 func Generic[T any](v T) T { return v }
 func Caller(w Worker) { var e Embedded; e.Work(); w.Work(); Generic(1) }
 `
	files := map[string]string{"go.mod": "module fixture.local/live\n\ngo 1.24\n", "a.go": body}
	first := strings.Index(body, "e.Work()") + 2
	iface := strings.Index(body, "w.Work()") + 2
	generic := strings.LastIndex(body, "Generic(1)")
	report := Analyze(context.Background(), config, files, []Query{{Path: "a.go", Offset: first}, {Path: "a.go", Offset: iface}, {Path: "a.go", Offset: iface, Implementation: true}, {Path: "a.go", Offset: generic}})
	if report.Status != "complete" {
		t.Fatalf("report %#v", report)
	}
	if len(report.Answers) != 4 {
		t.Fatalf("answers %#v", report.Answers)
	}
	for _, answer := range report.Answers {
		var expected []int
		switch {
		case answer.Query.Implementation:
			expected = []int{strings.Index(body, "Work() {}"), strings.LastIndex(body, "Work() {}")}
		case answer.Query.Offset == first:
			expected = []int{strings.Index(body, "Work() {}")}
		case answer.Query.Offset == iface:
			expected = []int{strings.Index(body, "Work() }")}
		case answer.Query.Offset == generic:
			expected = []int{strings.Index(body, "Generic[T")}
		}
		if len(answer.Targets) != len(expected) {
			t.Fatalf("targets %#v expected %v", answer, expected)
		}
		for i, target := range answer.Targets {
			file, start, _, err := MapLocation(files, target)
			if err != nil || file != "a.go" || start != expected[i] {
				t.Fatalf("wrong target %#v start=%d expected=%d err=%v", answer, start, expected[i], err)
			}
		}
	}
	changed := map[string]string{"go.mod": files["go.mod"], "a.go": strings.Replace(body, "type Two struct{}", "type Two struct{ Value int }", 1)}
	next := Analyze(context.Background(), config, changed, []Query{{Path: "a.go", Offset: first}})
	if next.ContextID == report.ContextID || next.Status != "complete" {
		t.Fatalf("dependency edit not rebound %#v", next)
	}
}
func TestLiveCompilerMissingDependenciesAndCancellation(t *testing.T) {
	config := liveConfig(t)
	files := map[string]string{"go.mod": "module fixture.local/offline\n\ngo 1.24\nrequire missing.invalid/dependency v1.2.3\n", "a.go": "package p\nimport _ \"missing.invalid/dependency\"\n"}
	report := Analyze(context.Background(), config, files, nil)
	if report.Status != "unavailable" || len(report.Diagnostics) == 0 || report.Diagnostics[0].Code != "compiler_dependency_unavailable" {
		t.Fatalf("missing dependency %#v", report)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	report = Analyze(ctx, config, map[string]string{"go.mod": "module fixture.local/cancel\n\ngo 1.24\n"}, nil)
	if report.Status == "complete" || time.Since(start) > 3*time.Second {
		t.Fatalf("cancellation %#v", report)
	}
}
func TestLiveCompilerWorkspaceAliases(t *testing.T) {
	config := liveConfig(t)
	files := map[string]string{
		"go.work":        "go 1.24\nuse (\n ./app\n ./library\n)\n",
		"app/go.mod":     "module fixture.local/app\n\ngo 1.24\n",
		"library/go.mod": "module fixture.local/library\n\ngo 1.24\n",
		"library/lib.go": "package library\nfunc Target() {}\n",
		"app/app.go":     "package app\nimport alias \"fixture.local/library\"\nfunc Caller() { alias.Target() }\n",
	}
	report := Analyze(context.Background(), config, files, []Query{{Path: "app/app.go", Offset: strings.Index(files["app/app.go"], "Target()")}})
	if report.Status != "complete" || len(report.Answers) != 1 || len(report.Answers[0].Targets) != 1 {
		t.Fatalf("workspace %#v", report)
	}
	file, start, _, err := MapLocation(files, report.Answers[0].Targets[0])
	if err != nil || file != "library/lib.go" || start != strings.Index(files[file], "Target") {
		t.Fatalf("workspace target %s:%d %v", file, start, err)
	}
}

// Authored from P2.3/P2-C: the caller stays byte-identical while tags select
// declarations in distinct captured files. Function-value definition is a
// variable declaration, never proof of the closure's runtime target.
func TestLiveCompilerTagsReplacementAndClosure(t *testing.T) {
	config := liveConfig(t)
	files := map[string]string{
		"go.mod":           "module fixture.local/app\n\ngo 1.24\nrequire fixture.local/lib v0.0.0\nreplace fixture.local/lib => ./lib\n",
		"lib/go.mod":       "module fixture.local/lib\n\ngo 1.24\n",
		"lib/default.go":   "//go:build !alternate\n\npackage lib\nfunc Target() {}\n",
		"lib/alternate.go": "//go:build alternate\n\npackage lib\nfunc Target() {}\n",
		"app.go":           "package app\nimport alias \"fixture.local/lib\"\nfunc Caller() { alias.Target(); callback := func() {}; callback() }\n",
	}
	offset := strings.Index(files["app.go"], "Target()")
	query := Query{Path: "app.go", Offset: offset}
	before := Analyze(context.Background(), config, files, []Query{query})
	config.Tags = []string{"alternate"}
	after := Analyze(context.Background(), config, files, []Query{query})
	for i, report := range []Report{before, after} {
		expected := []string{"lib/default.go", "lib/alternate.go"}[i]
		if report.Status != "complete" || len(report.Answers) != 1 || len(report.Answers[0].Targets) != 1 {
			t.Fatalf("tag arm %d: %#v", i, report)
		}
		name, _, _, err := MapLocation(files, report.Answers[0].Targets[0])
		if err != nil || name != expected {
			t.Fatalf("tag arm %d target %s %v", i, name, err)
		}
	}
	if before.ContextID == after.ContextID {
		t.Fatal("tag configuration did not invalidate overlay")
	}
	closure := Analyze(context.Background(), config, files, []Query{{Path: "app.go", Offset: strings.LastIndex(files["app.go"], "callback()")}})
	if closure.Status != "complete" || len(closure.Answers) != 1 || len(closure.Answers[0].Targets) != 1 {
		t.Fatalf("closure %#v", closure)
	}
	name, start, _, err := MapLocation(files, closure.Answers[0].Targets[0])
	if err != nil || name != "app.go" || start != strings.Index(files["app.go"], "callback :=") {
		t.Fatalf("function value target invented: %s:%d %v", name, start, err)
	}
	files["go.mod"] = strings.Replace(files["go.mod"], "./lib", "../escaped", 1)
	escaped := Analyze(context.Background(), config, files, []Query{query})
	if escaped.Status != "unavailable" {
		t.Fatalf("escaped replacement %#v", escaped)
	}
}

// Independently authored P2-C invalidation fixtures. The caller's bytes and
// query token remain fixed while a transitive input changes.
func TestLiveCompilerSignatureAndWorkspaceInvalidation(t *testing.T) {
	config := liveConfig(t)
	files := map[string]string{
		"go.work":        "go 1.24\nuse (\n ./app\n ./library\n)\n",
		"app/go.mod":     "module fixture.local/app\n\ngo 1.24\n",
		"library/go.mod": "module fixture.local/library\n\ngo 1.24\n",
		"library/lib.go": "package library\nfunc Target(x int) {}\n",
		"app/app.go":     "package app\nimport \"fixture.local/library\"\nfunc Caller() { library.Target(1) }\n",
	}
	query := Query{Path: "app/app.go", Offset: strings.Index(files["app/app.go"], "Target(1)")}
	before := Analyze(context.Background(), config, files, []Query{query})
	if before.Status != "complete" || len(before.Answers) != 1 {
		t.Fatalf("baseline %#v", before)
	}
	files["library/lib.go"] = "package library\nfunc Target(x int64) {}\n"
	after := Analyze(context.Background(), config, files, []Query{query})
	if after.Status != "complete" || len(after.Answers) != 1 || len(after.Answers[0].Targets) != 1 || after.ContextID == before.ContextID {
		t.Fatalf("signature edit not rebound %#v", after)
	}
	path, start, _, err := MapLocation(files, after.Answers[0].Targets[0])
	if err != nil || path != "library/lib.go" || start != strings.Index(files[path], "Target") {
		t.Fatalf("signature target %s:%d %v", path, start, err)
	}
	files["go.work"] = "go 1.24\nuse ./app\n"
	excluded := Analyze(context.Background(), config, files, []Query{query})
	if excluded.Status != "unavailable" || len(excluded.Answers) != 0 || excluded.ContextID == after.ContextID {
		t.Fatalf("excluded dependency reused old overlay %#v", excluded)
	}
	if len(excluded.Diagnostics) == 0 || excluded.Diagnostics[0].Code != "compiler_dependency_unavailable" {
		t.Fatalf("missing explicit workspace fallback %#v", excluded)
	}
}
