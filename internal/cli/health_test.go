package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestHealthReportsAndCacheLifecycle(t *testing.T) {
	for _, tc := range []struct {
		total, failures int
		status          string
	}{
		{20, 0, "ok"}, {21, 1, "ok"}, {20, 1, "degraded"}, {1, 1, "unsafe"},
	} {
		t.Run(fmt.Sprintf("%d_of_%d", tc.failures, tc.total), func(t *testing.T) {
			repo, cache := t.TempDir(), t.TempDir()
			git(t, repo, "init")
			git(t, repo, "config", "user.name", "Test")
			git(t, repo, "config", "user.email", "test@example.com")
			for i := 0; i < tc.total; i++ {
				content := "def retained():\n    return 1\n"
				if i < tc.failures {
					content += "def broken(:\n"
				}
				write(t, repo, fmt.Sprintf("source%d.py", i), content)
			}
			write(t, repo, "notes.md", "# documentation\n")
			git(t, repo, "add", ".")
			git(t, repo, "commit", "-m", "fixture")
			run := func(args ...string) (string, string) {
				t.Helper()
				var out, stderr bytes.Buffer
				if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo, PluginDataDir: cache}, Stdout: &out, Stderr: &stderr}, args); err != nil {
					t.Fatal(err)
				}
				return out.String(), stderr.String()
			}
			decode := func(text string) healthResponse {
				t.Helper()
				var r healthResponse
				if err := json.Unmarshal([]byte(text), &r); err != nil {
					t.Fatal(err)
				}
				return r
			}
			text, progress := run("health", "--json")
			first := decode(text)
			h := first.Completeness.Health
			if h.Status != tc.status || first.Counts.CompletenessLevel != h.Status || h.SourceFiles != tc.total || h.FlaggedFiles != tc.failures || h.ThresholdPercentage != 5 {
				t.Fatalf("health = %+v", h)
			}
			if first.IndexCacheHit || first.CacheFreshness != "built" || first.Profile != "full" || first.Commit == "" || !strings.Contains(progress, "Building committed HEAD index") {
				t.Fatalf("cold provenance/progress incorrect: %+v %q", first, progress)
			}
			if len(first.PartialFailures) != tc.failures {
				t.Fatalf("diagnostics lost: %+v", first.PartialFailures)
			}
			if tc.failures > 0 && (!strings.Contains(first.PartialFailures[0].Detail, "line") || first.Categories["E_PARSE_ERROR"].Files != tc.failures) {
				t.Fatal("locations or category counts missing")
			}
			text, progress = run("health", "--json")
			warm := decode(text)
			if !warm.IndexCacheHit || warm.CacheFreshness != "matching_index" || progress != "" || !reflect.DeepEqual(warm.Completeness, first.Completeness) {
				t.Fatal("health did not reuse index")
			}
			// Dirty source is not included in committed HEAD health.
			write(t, repo, "uncommitted.py", "def broken(:\n")
			text, _ = run("health", "--refresh", "--json")
			fresh := decode(text)
			if fresh.IndexCacheHit || fresh.CacheFreshness != "rebuilt" || !reflect.DeepEqual(fresh.Completeness, first.Completeness) {
				t.Fatal("refresh failed or read uncommitted source")
			}
			text, _ = run("index", "--format", "json")
			var index indexResponse
			if err := json.Unmarshal([]byte(text), &index); err != nil {
				t.Fatal(err)
			}
			if !index.IndexCacheHit || !reflect.DeepEqual(index.Completeness, first.Completeness) {
				t.Fatal("index/health cache or accounting differ")
			}
			if tc.total == 21 && tc.failures == 1 {
				text, _ = run("search", "--head", "--profile", "full", "--index-all-files", "--query", "retained", "--format", "json", "--max-context-bytes", "0")
				var search sem.SearchResponse
				if err := json.Unmarshal([]byte(text), &search); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(search.Completeness.Health, h) || len(search.PartialFailures) == 0 {
					t.Fatal("search health/diagnostics differ from index")
				}
				text, _ = run("neighbors", "--head", "--symbol", "retained", "--file", "source1.py", "--format", "text")
				if !strings.Contains(text, "E_PARSE_ERROR") || !strings.Contains(text, "source0.py") {
					t.Fatal("below-threshold relation query hid relevant diagnostics")
				}
			}
			text, _ = run("health")
			for _, want := range []string{"Graph health: " + tc.status, "Source files:", "degradation threshold: 5%", "Revision:", "profile: full", "Cache freshness: matching_index", "Python:", "do not establish that source code is invalid"} {
				if !strings.Contains(text, want) {
					t.Fatalf("text missing %q: %s", want, text)
				}
			}
			if tc.failures > 0 && (!strings.Contains(text, "source0.py") || !strings.Contains(text, "line")) {
				t.Fatal("text lost diagnostic location")
			}
		})
	}
}

func TestHealthSeparatesSkipsAndEscapesDiagnostics(t *testing.T) {
	index := indexResponse{PartialFailures: []sem.PartialFailure{
		{FilePath: "bundle.js", Code: "E_MINIFIED", Detail: "policy skip"},
		{FilePath: "bad\nfile.py", Code: "E_PARSE_ERROR", Detail: "at line 2 column 3\nforged line"},
		{FilePath: "bad\nfile.py", Code: "E_PARSE_DEPTH_EXCEEDED"},
	}}
	var out bytes.Buffer
	if err := writeHealth(&out, index, false, false); err != nil {
		t.Fatal(err)
	}
	var r healthResponse
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.PartialFailures) != 3 || len(r.IntentionalSkips) != 1 || !r.Categories["E_MINIFIED"].IntentionalSkip {
		t.Fatal("skip diagnostics lost or misclassified")
	}
	out.Reset()
	if err := writeHealth(&out, index, true, false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Contains(text, "bad\nfile") || strings.Contains(text, "\nforged") || strings.Count(text, "bundle.js") != 1 {
		t.Fatalf("unsafe or duplicated text: %q", text)
	}
	if !strings.Contains(text, "Intentional skips:\n  bundle.js") {
		t.Fatal("skip not separately listed")
	}
}

func TestHealthRejectsUnsupportedFlags(t *testing.T) {
	for _, args := range [][]string{{"--worktree"}, {"--report", "report.md"}, {"--force"}, {"--repo"}, {"--profile", "invalid"}, {"--format", "ndjson"}} {
		if err := Run(t.Context(), Options{}, append([]string{"health"}, args...)); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

func TestQueryDiagnosticsSurviveOKRepositoryHealth(t *testing.T) {
	snapshot := polyglotSnapshot(1, 0)
	snapshot.Header.Stats.CompletenessLevel = "ok"
	var out bytes.Buffer
	writeScopedCompletenessBlock(&out, buildCompletenessScope(snapshot, "Rust"), snapshot.Header.Warnings, snapshot.Header.PartialFailures, snapshot.Header.Stats)
	if !strings.Contains(out.String(), "E_PARSE_ERROR") || !strings.Contains(out.String(), "broken_a.rs") {
		t.Fatal("ok suppressed relevant query warning")
	}
	full, compact := agentSearchDiagnostics(sem.SearchResponse{PartialFailures: snapshot.Header.PartialFailures, Completeness: snapshot.Header.Completeness})
	if !bytes.Contains(full, []byte("E_PARSE_ERROR")) || !bytes.Contains(compact, []byte("F1")) {
		t.Fatal("search dropped affected-file warning")
	}
}
