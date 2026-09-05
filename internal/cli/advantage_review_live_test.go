package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"runtime"
	"slices"
	"testing"

	"github.com/entireio/entire-graph/internal/compiler"
	"github.com/entireio/entire-graph/internal/sem"
)

// Independently authored review-F2 correctness fixture, with promoted-method
// lookup and an alias. Static coverage is recorded, not assumed absent: the
// regression requires actual compiler evidence to reach ordinary query paths.
func TestLiveReviewCompilerOrdinaryQueries(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("ENTIRE_GRAPH_COMPILER_LIVE") != "1" {
		t.Skip("explicit pinned Linux correctness run")
	}
	repo := t.TempDir()
	sources := map[string]string{
		"go.mod":    "module fixture.local/review\n\ngo 1.24\n",
		"target.go": "package p\ntype Core struct{}\nfunc (Core) Pulse() {}\ntype Envelope struct { Core }\ntype Alias = Envelope\n",
		"entry.go":  "package p\nfunc ReviewEntry(value Alias) { value.Pulse() }\nfunc ReviewOuter(value Alias) { ReviewEntry(value) }\n",
	}
	for path, body := range sources {
		write(t, repo, path, body)
	}
	raw := map[string]json.RawMessage{}
	t.Cleanup(func() {
		if path := os.Getenv("ENTIRE_GRAPH_REVIEW_LIVE_OUTPUT"); path != "" {
			encoded, err := json.MarshalIndent(map[string]any{"fixture_origin": "hand-authored interim review F2 promoted-method and alias correctness fixture", "sources": sources, "responses": raw, "failed": t.Failed()}, "", "  ")
			if err == nil {
				err = os.WriteFile(path, append(encoded, '\n'), 0600)
			}
			if err != nil {
				t.Errorf("retain review correctness evidence: %v", err)
			}
		}
	})
	base := []string{"--repo", repo, "--profile", "full", "--format", "json", "--no-cache"}
	live := []string{"--compiler", "go", "--require-compiler", "--gopls", "/opt/graph-tools/gopls", "--gopls-sha256", "2b4652d6ac42a22942f63735d9c7e44e9dfbc1dade5d4fd09c0d4eb8fa3539b1", "--go-toolchain", "/usr/local/go", "--compiler-launcher", "/usr/bin/bwrap"}
	run := func(stage, verb string, enabled bool, extra ...string) []byte {
		command := append([]string{verb}, base...)
		if enabled {
			command = append(command, live...)
		}
		command = append(command, extra...)
		var out bytes.Buffer
		err := Run(context.Background(), Options{Stdout: &out}, command)
		if json.Valid(out.Bytes()) {
			raw[stage] = append([]byte(nil), out.Bytes()...)
		}
		if err != nil {
			t.Fatalf("%s: %v; %s", stage, err, out.String())
		}
		return out.Bytes()
	}
	var static impactResponse
	if err := json.Unmarshal(run("static-impact", "impact", false, "--symbol", "Pulse", "--depth", "2"), &static); err != nil {
		t.Fatal(err)
	}
	for _, depth := range []string{"1", "2"} {
		var impact impactResponse
		if err := json.Unmarshal(run("compiler-impact-"+depth, "impact", true, "--symbol", "Pulse", "--depth", depth), &impact); err != nil {
			t.Fatal(err)
		}
		if impact.Compiler == nil || impact.Compiler.Report.Status != "complete" || impact.Focus == nil {
			t.Fatalf("compiler impact coverage: %+v", impact.Compiler)
		}
		callerID := ""
		for _, call := range impact.Compiler.Calls {
			if call.Evidence.Category == compiler.DirectDeclaration && call.Evidence.TargetSymbolID == impact.Focus.ID && call.Evidence.Caller.Path == "entry.go" {
				callerID = call.SourceSymbolID
			}
		}
		if callerID == "" {
			t.Fatal("real compiler did not confirm promoted alias invocation")
		}
		direct := false
		outer := false
		for _, caller := range impact.Callers.Entries {
			if caller.Endpoint.ID == callerID && caller.Depth == 1 {
				direct = true
			}
			if caller.Endpoint.Name == "ReviewOuter" && caller.Depth == 2 {
				outer = true
			}
		}
		if !direct || (depth == "2" && !outer) {
			t.Fatalf("compiler shallow callers missing: %+v", impact.Callers)
		}
	}
	for _, query := range []string{"ReviewEntry", "Pulse"} {
		var response sem.SearchResponse
		if err := json.Unmarshal(run("compiler-search-"+query, "search", true, "--query", query, "--index-all-files", "--top-k", "20", "--max-context-bytes", "0"), &response); err != nil {
			t.Fatal(err)
		}
		if response.Compiler == nil || response.Compiler.Report.Status != "complete" || response.Stats.Ranking != nil {
			t.Fatal("ordinary search compiler coverage/ranking changed")
		}
		signal := "graph:callers"
		if query == "ReviewEntry" {
			signal = "graph:calls"
		}
		found := false
		for _, result := range response.Results {
			if result.SymbolName == "Pulse" && slices.Contains(result.Signals, signal) {
				found = true
			}
		}
		if !found {
			t.Fatalf("ordinary search did not expose Pulse %s: %+v", signal, response.Results)
		}
	}
	t.Logf("static direct callers=%d; static transitive callers=%d (retained as observed coverage, not a missing-edge claim)", static.Callers.Direct, static.Callers.Transitive)
}
