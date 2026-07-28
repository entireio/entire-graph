package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIndexReportIsDeterministicAndSummarizesTheSnapshot covers the one property a
// generated report has to have to be worth anything: the same tree renders the
// same bytes, so the file can be committed, diffed, and reviewed. It also pins
// that the report is a projection of the snapshot's own summary — the counts it
// prints have to match what the graph found, not a second pass over the source.
func TestIndexReportIsDeterministicAndSummarizesTheSnapshot(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", `package auth

func ValidateToken(token string) bool { return Helper(token) }

func Helper(token string) bool { return token != "" }
`)
	write(t, repo, "svc.py", "def handler():\n    return True\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	render := func(name string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		if err := Run(t.Context(), Options{
			Version: "test-version",
			Env:     EntireEnv{RepoRoot: repo, PluginDataDir: cacheDir},
			Stdout:  &bytes.Buffer{},
		}, []string{"index", "--repo", repo, "--format", "json", "--report", path}); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}

	first := render("GRAPH_REPORT.md")
	// The second run is served from the cache written by the first, so this also
	// pins that a cached snapshot renders identically to a freshly built one.
	if second := render("GRAPH_REPORT-again.md"); first != second {
		t.Fatalf("report is not deterministic for one tree:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	for _, want := range []string{
		"# Graph report", "- Completeness: **ok**", "## Languages",
		"| Go | semantic | 1 | 2 |", "| Python | semantic | 1 | 1 |",
		"## Symbol kinds", "| function | 3 |", "## Relations", "| CALLS | 1 |",
		"## Top files by symbol count", "| auth.go | 2 |",
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("report missing %q:\n%s", want, first)
		}
	}
	// Nothing outside the snapshot may reach the file, or two runs of one tree
	// would diff on someone else's machine.
	if strings.Contains(first, repo) {
		t.Fatalf("report leaked an absolute machine path:\n%s", first)
	}
}
