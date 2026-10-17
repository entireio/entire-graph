package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// TestAgentQueryColdAndWarmReportTheSameCoverage pins a cache hit to the payload its cold miss
// printed, byte for byte apart from the timing line.
//
// The two are served by different preselection: cold, a small repository indexes every file (or
// builds the complete snapshot and derives the query's view from it); warm, the preindexed graph
// lets Git's exact tree grep pick the matching files. The coverage diagnostics used to follow the
// selection, so a file that failed to parse was reported cold and silently dropped on the hit
// whenever the warm grep did not match it: `Coverage: degraded (... 2 partial failures)` became
// `... 1 partial failure`, or no Coverage line at all.
//
// Two sizes, because the cold path differs: below the indexing pool the cold query indexes every
// file directly, and above it the cold query builds the complete snapshot and derives a selective
// view, which used to report the selective view's diagnostics on BOTH runs.
func TestAgentQueryColdAndWarmReportTheSameCoverage(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		fillers int
	}{
		{name: "every file fits the cold indexing pool", fillers: 0},
		{name: "cold query builds the complete snapshot and derives", fillers: 130},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repo := t.TempDir()
			initDoctorRepo(t, repo, "git@github.com:example/coverage.git")
			write(t, repo, "go.mod", "module example.com/coverage\n\ngo 1.22\n")
			write(t, repo, "pkg/widget.go", `package pkg

// RenderWidget renders the widget frame.
func RenderWidget(name string) string {
	return "widget:" + name
}
`)
			write(t, repo, "pkg/widget_test.go", `package pkg

import "testing"

func TestRenderWidget(t *testing.T) {
	if RenderWidget("a") != "widget:a" {
		t.Fatal("bad")
	}
}
`)
			// A parse failure the query's terms never match, so the warm grep never selects it.
			write(t, repo, "pkg/broken.go", "package pkg\n\nfunc Broken( {\n")
			for index := 0; index < testCase.fillers; index++ {
				write(t, repo, fmt.Sprintf("filler/f%03d.go", index),
					fmt.Sprintf("package filler\n\nfunc Unrelated%03d() int { return %d }\n", index, index))
			}
			git(t, repo, "add", "-A")
			git(t, repo, "commit", "-q", "-m", "initial")

			cacheDir := t.TempDir()
			query := func() (string, string) {
				t.Helper()
				var out bytes.Buffer
				if err := Run(t.Context(), Options{
					Version: "0.1.0",
					Env:     EntireEnv{RepoRoot: repo, PluginDataDir: cacheDir},
					Stdout:  &out,
				}, []string{
					"query", "--format", "agent", "--query", "render widget frame",
					"--repo", repo, "--head", "--cache-dir", cacheDir,
				}); err != nil {
					t.Fatalf("query: %v\n%s", err, out.String())
				}
				header, body, _ := strings.Cut(out.String(), "\n")
				return header, body
			}
			coldHeader, cold := query()
			warmHeader, warm := query()
			hotHeader, hot := query()
			if !strings.Contains(coldHeader, "cache-miss") || !strings.Contains(warmHeader, "cache-hit") ||
				!strings.Contains(hotHeader, "cache-hit") {
				t.Fatalf("headers = %q / %q / %q, want a miss then two hits", coldHeader, warmHeader, hotHeader)
			}
			if !strings.Contains(cold, "Coverage: degraded") || !strings.Contains(cold, "E_PARSE_ERROR: pkg/broken.go") {
				t.Fatalf("cold payload does not report the repository's parse failure:\n%s", cold)
			}
			if warm != cold {
				t.Fatalf("warm payload differs from cold:\n--- cold\n%s\n--- warm\n%s", cold, warm)
			}
			if hot != cold {
				t.Fatalf("second warm payload differs from cold:\n--- cold\n%s\n--- hot\n%s", cold, hot)
			}
		})
	}
}
