package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/compiler"
	"github.com/entireio/entire-graph/internal/sem"
)

// Independently authored combination fixture from plan section 8. Each stage
// uses the production CLI dispatcher, with actual pinned gopls and sandbox.
func TestLiveAdvantageCombinationsAndNextQueryFreshness(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("ENTIRE_GRAPH_COMPILER_LIVE") != "1" {
		t.Skip("explicit pinned Linux combination run")
	}
	repo, cache := t.TempDir(), t.TempDir()
	core := func(value string) string {
		return "package p\ntype Worker interface { Work() }\ntype One struct{}\nfunc (One) Work() {}\ntype Two struct{}\nfunc (Two) Work() {}\nfunc Shared() string { return \"" + value + "\" }\n"
	}
	chain := "package p\nfunc Tier1() { Shared() }\nfunc Tier2() { Tier1() }\nfunc Tier3() { Tier2() }\nfunc Dispatch(w Worker) { w.Work() }\n"
	write(t, repo, "go.mod", "module fixture.local/combinations\n\ngo 1.24\n")
	write(t, repo, "chain.go", chain)
	args := []string{"--repo", repo, "--cache-dir", cache, "--extraction-cache", "on", "--compiler", "go", "--require-compiler", "--gopls", "/opt/graph-tools/gopls", "--gopls-sha256", "2b4652d6ac42a22942f63735d9c7e44e9dfbc1dade5d4fd09c0d4eb8fa3539b1", "--go-toolchain", "/usr/local/go", "--compiler-launcher", "/usr/bin/bwrap", "--profile", "full", "--format", "json"}
	raw := map[string]json.RawMessage{}
	sources := map[string]map[string]string{}
	t.Cleanup(func() {
		if path := os.Getenv("ENTIRE_GRAPH_ADVANTAGE_LIVE_OUTPUT"); path != "" {
			b, err := json.MarshalIndent(map[string]any{"fixture_origin": "hand-authored plan section 8 combination contract", "sources_by_stage": sources, "responses": raw, "failed": t.Failed()}, "", "  ")
			if err == nil {
				err = os.WriteFile(path, append(b, '\n'), 0600)
			}
			if err != nil {
				t.Errorf("retain live evidence: %v", err)
			}
		}
	})
	current := ""
	run := func(stage, verb string, extra ...string) []byte {
		sources[stage] = map[string]string{"core.go": core(current), "chain.go": chain, "core.go.sha256": compiler.ContentDigest(core(current)), "chain.go.sha256": compiler.ContentDigest(chain)}
		command := append([]string{verb}, args...)
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
	checkCompiler := func(stage string, overlay *sem.CompilerOverlay) {
		if overlay == nil || overlay.Report.Status != "complete" {
			t.Fatalf("%s compiler status %#v", stage, overlay)
		}
		candidate, direct := 0, 0
		for _, call := range overlay.Calls {
			switch call.Evidence.Category {
			case compiler.ImplementationCandidate:
				candidate++
			case compiler.DirectDeclaration:
				direct++
			}
			for _, site := range []compiler.Site{call.Evidence.Caller, call.Evidence.Target} {
				body := chain
				if site.Path == "core.go" {
					body = core(current)
				}
				if site.Digest != compiler.ContentDigest(body) {
					t.Fatalf("%s stale compiler evidence %s", stage, site.Path)
				}
			}
		}
		if candidate != 2 || direct < 4 {
			t.Fatalf("%s lost declared/candidate states direct=%d candidate=%d", stage, direct, candidate)
		}
	}
	previousContext := ""
	for index, value := range []string{"before", "before", "after!"} {
		current = value
		write(t, repo, "core.go", core(current))
		stage := []string{"search-cold", "search-reused", "search-edited"}[index]
		var result sem.SearchResponse
		if err := json.Unmarshal(run(stage, "search", "--query", "Shared", "--ranking", "experimental-graph", "--index-all-files", "--top-k", "8"), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Results) == 0 || !strings.Contains(result.Results[0].Snippet, value) {
			t.Fatalf("%s source not current", stage)
		}
		if result.OperationInputs == nil || result.OperationInputs.ID == "" {
			t.Fatalf("%s missing captured input provenance", stage)
		}
		if result.Stats.IndexCacheHit || result.Stats.Extraction == nil || result.Stats.Ranking == nil {
			t.Fatalf("%s lost cache/ranking status", stage)
		}
		if index == 1 && (result.Stats.Extraction.FilesParsed != 0 || result.Stats.Extraction.FilesReused < 2) {
			t.Fatalf("%s did not reuse unchanged eligible files %#v", stage, result.Stats.Extraction)
		}
		if index == 2 && (result.Stats.Extraction.FilesParsed != 1 || result.Stats.Extraction.FilesReused < 1) {
			t.Fatalf("%s changed-file extraction %#v", stage, result.Stats.Extraction)
		}
		checkCompiler(stage, result.Compiler)
		if index == 2 && result.Compiler.Report.ContextID == previousContext {
			t.Fatal("source edit kept compiler context")
		}
		previousContext = result.Compiler.Report.ContextID
	}
	focusID := ""
	previousContext = ""
	for index, value := range []string{"after!", "third!"} {
		current = value
		write(t, repo, "core.go", core(current))
		stage := []string{"impact-reused", "impact-edited"}[index]
		var result impactResponse
		if err := json.Unmarshal(run(stage, "impact", "--symbol", "Shared", "--depth", "all"), &result); err != nil {
			t.Fatal(err)
		}
		if result.IndexCacheHit || result.Traversal == nil || result.Focus == nil {
			t.Fatalf("%s lost traversal", stage)
		}
		checkCompiler(stage, result.Compiler)
		deep := false
		for _, hit := range result.Traversal.Results {
			for _, path := range hit.Paths {
				if len(path.Steps) >= 3 {
					deep = true
				}
			}
		}
		if !deep {
			t.Fatalf("%s failed three-hop chain %#v", stage, result.Traversal)
		}
		if index == 1 && (result.Focus.ID != focusID || result.Compiler.Report.ContextID == previousContext) {
			t.Fatal("edit changed stable identity or retained stale compiler context")
		}
		focusID = result.Focus.ID
		previousContext = result.Compiler.Report.ContextID
	}
}
