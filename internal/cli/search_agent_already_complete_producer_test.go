package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestAlreadyCompleteProducerEmitsExactCertifiedBody(t *testing.T) {
	t.Parallel()
	repo := hugeUnitRepo(t)
	content, err := os.ReadFile(filepath.Join(repo, "small/retry.go"))
	if err != nil {
		t.Fatal(err)
	}
	wantBody := strings.Join(strings.Split(string(content), "\n")[1:6], "\n")
	for _, budget := range []int{4096, 8192, 24576} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			query := []string{"query", "--repo", repo, "--query", "reports whether the retry budget is spent", "--no-cache", "--max-context-bytes", fmt.Sprint(budget)}
			var jsonOut bytes.Buffer
			if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &jsonOut}, append(query, "--format", "json")); err != nil {
				t.Fatal(err)
			}
			var response sem.SearchResponse
			if err := json.Unmarshal(jsonOut.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, result := range response.Results {
				if result.FilePath != "small/retry.go" || result.SymbolName != "RetryBudgetExhausted" {
					continue
				}
				found = true
				if result.SymbolID == "" || result.Kind != "function" || result.SymbolStartLine != 4 || result.SymbolEndLine != 6 || result.SnippetStartLine != 2 || result.SnippetEndLine != 6 || result.Snippet != wantBody {
					t.Fatalf("target identity, bounds or exact source changed: %+v", result)
				}
				if !searchResultNeedsNoFollowUpRead(result) {
					t.Fatalf("whole target is not certified: %v", result.Signals)
				}
			}
			if !found {
				t.Fatal("whole target not returned")
			}
			if response.Stats.CompleteSymbols != 1 {
				t.Fatalf("complete-symbol count=%d, want exactly the small callable", response.Stats.CompleteSymbols)
			}
			var out bytes.Buffer
			if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, append(query, "--format", "agent")); err != nil {
				t.Fatal(err)
			}
			if out.Len() > budget {
				t.Fatalf("agent bytes=%d exceed budget=%d", out.Len(), budget)
			}
			// Require the marker and the exact body on THIS target's block, not an
			// unrelated complete result somewhere else in the payload.
			markedTarget := false
			for _, line := range strings.Split(out.String(), "\n") {
				if strings.Contains(line, "small/retry.go:2-6 RetryBudgetExhausted ") && strings.Contains(line, completeMarker) && strings.Contains(out.String(), line+"\n"+wantBody+"\n") {
					markedTarget = true
				}
			}
			if !markedTarget {
				t.Fatalf("target's complete body/marker missing:\n%s", out.String())
			}
		})
	}
}

func TestAlreadyCompleteProducerKeepsTransformedBodyMarkerGuard(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "control.go", "package control\n\n// PreserveControl returns a raw control byte.\nfunc PreserveControl() string {\n\treturn `A\x7fB`\n}\n")
	args := []string{"query", "--repo", repo, "--query", "PreserveControl returns raw control byte", "--no-cache"}
	var jsonOut bytes.Buffer
	if err := Run(t.Context(), Options{Version: "audit", Env: EntireEnv{RepoRoot: repo}, Stdout: &jsonOut}, append(args, "--format", "json")); err != nil {
		t.Fatal(err)
	}
	var response sem.SearchResponse
	if err := json.Unmarshal(jsonOut.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) == 0 || response.Results[0].SymbolName != "PreserveControl" || !searchResultNeedsNoFollowUpRead(response.Results[0]) || !strings.ContainsRune(response.Results[0].Snippet, '\x7f') {
		t.Fatalf("fixture did not produce a structurally complete raw body: %+v", response.Results)
	}
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "audit", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, append(args, "--format", "agent")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), completeMarker) || strings.ContainsRune(out.String(), '\x7f') || !strings.Contains(out.String(), `A\x7fB`) {
		t.Fatalf("transformed body claimed completeness or lost escaping:\n%s", out.String())
	}
}

func TestAlreadyCompleteCertificationLeavesPublicNoHitResponseValid(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "present.go", "package present\nfunc Present() bool { return true }\n")
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "audit", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
		"query", "--repo", repo, "--query", "ZzxxyyAbsentIdentifierNeverOccurs", "--format", "json", "--no-cache",
	}); err != nil {
		t.Fatal(err)
	}
	var response sem.SearchResponse
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Results == nil || len(response.Results) != 0 || response.Stats.ResultBytes != 2 || response.Stats.CompleteSymbols != 0 {
		t.Fatalf("no-hit response changed shape/accounting: %s", out.String())
	}
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
}
