package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// The agent coverage line follows completeness.health.status on every path that
// renders it: a working-tree query, a cold --head query, a warm --head query,
// and a query that matches nothing. The fixtures sit on both sides of the
// inclusive 5% threshold (DegradationThresholdPercent) and add an intentional
// skip, which health leaves out of the flagged count.
func TestAgentCoverageLineFollowsHealthOnEveryPath(t *testing.T) {
	cases := []struct {
		name   string
		good   int
		broken int
		skip   bool
		level  string
	}{
		// 1 of 25 source files flagged: 4%, under the threshold.
		{"one failure in twenty-five source files", 24, 1, false, "notice"},
		// 1 of 20: exactly 5%, and the threshold is inclusive.
		{"one failure in twenty source files", 19, 1, false, "degraded"},
		// 2 of 20: 10%.
		{"two failures in twenty source files", 18, 2, false, "degraded"},
		// An intentional skip is listed and counted, but is not a flagged file.
		{"only an intentionally skipped bundle", 24, 0, true, "notice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			git(t, repo, "init", "-q")
			git(t, repo, "config", "user.name", "Coverage Health Test")
			git(t, repo, "config", "user.email", "coverage-health@example.invalid")
			for index := 0; index < tc.good; index++ {
				write(t, repo, fmt.Sprintf("pkg/good%02d.go", index),
					fmt.Sprintf("package pkg\n\n// CoverageTarget%02d is found by the query.\nfunc CoverageTarget%02d() int { return %d }\n", index, index, index))
			}
			for index := 0; index < tc.broken; index++ {
				write(t, repo, fmt.Sprintf("pkg/broken%02d.go", index), "package pkg\n\nfunc broken( {\n}\n")
			}
			if tc.skip {
				write(t, repo, "dist/app.min.js", "var a=\""+strings.Repeat("x", 6000)+"\";\n")
			}
			git(t, repo, "add", "-A")
			git(t, repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "fixture")

			cacheDir := t.TempDir()
			run := func(args ...string) string {
				t.Helper()
				var stdout, stderr bytes.Buffer
				err := Run(t.Context(), Options{
					Version: "test-version",
					Env:     EntireEnv{RepoRoot: repo, PluginDataDir: cacheDir},
					Stdout:  &stdout,
					Stderr:  &stderr,
				}, args)
				if err != nil {
					t.Fatalf("%v: %v\n%s", args, err, stderr.String())
				}
				return stdout.String()
			}
			wantFailures := tc.broken
			if tc.skip {
				wantFailures++
			}
			paths := []struct {
				name string
				args []string
			}{
				{"working tree", []string{"query", "--repo", repo, "--cache-dir", cacheDir, "--query", "CoverageTarget03"}},
				{"head cold", []string{"query", "--repo", repo, "--head", "--cache-dir", cacheDir, "--query", "CoverageTarget03"}},
				{"head warm", []string{"query", "--repo", repo, "--head", "--cache-dir", cacheDir, "--query", "CoverageTarget03"}},
				{"head warm, no hit", []string{"query", "--repo", repo, "--head", "--cache-dir", cacheDir, "--query", "zzzunmatchedneedlezzz"}},
			}
			for _, path := range paths {
				raw := run(append(append([]string{}, path.args...), "--format", "json")...)
				var response sem.SearchResponse
				if err := json.Unmarshal([]byte(raw), &response); err != nil {
					t.Fatalf("%s: decode: %v\n%s", path.name, err, raw)
				}
				health := response.Completeness.Health
				if health.Status == "" {
					t.Fatalf("%s: completeness.health.status is not populated: %+v", path.name, response.Completeness)
				}
				if len(response.PartialFailures) != wantFailures {
					t.Fatalf("%s: %d partial failures, want %d: %+v", path.name, len(response.PartialFailures), wantFailures, response.PartialFailures)
				}
				wantStatus := "ok"
				if tc.level != "notice" {
					wantStatus = tc.level
				}
				if health.Status != wantStatus {
					t.Fatalf("%s: health = %+v, want status %q", path.name, health, wantStatus)
				}
				agent := run(append(append([]string{}, path.args...), "--format", "agent")...)
				if !strings.Contains(agent, "Coverage: "+tc.level+" (") {
					t.Fatalf("%s: want Coverage: %s with health %+v, got:\n%s", path.name, tc.level, health, agent)
				}
				// Nothing is hidden at "notice": the failures are still listed.
				if !strings.Contains(agent, "- partial ") {
					t.Fatalf("%s: the failures are no longer listed:\n%s", path.name, agent)
				}
			}
		})
	}
}

// A query that matches nothing, with no graph built or cached, still states a
// health status rather than leaving it empty.
func TestNoHitQueryWithoutAGraphReportsHealth(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.name", "Coverage Health Test")
	git(t, repo, "config", "user.email", "coverage-health@example.invalid")
	for index := 0; index < 40; index++ {
		write(t, repo, fmt.Sprintf("pkg/file%02d.go", index), fmt.Sprintf("package pkg\n\nfunc Thing%02d() {}\n", index))
	}
	git(t, repo, "add", "-A")
	git(t, repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "fixture")

	var stdout, stderr bytes.Buffer
	cacheDir := t.TempDir()
	err := Run(t.Context(), Options{
		Version: "test-version",
		Env:     EntireEnv{RepoRoot: repo, PluginDataDir: cacheDir},
		Stdout:  &stdout,
		Stderr:  &stderr,
	}, []string{"query", "--repo", repo, "--head", "--cache-dir", cacheDir, "--format", "json", "--query", "zzzunmatchedneedlezzz"})
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	var response sem.SearchResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	if len(response.Results) != 0 {
		t.Fatalf("the needle matched: %+v", response.Results)
	}
	if response.Completeness.Health.Status != "ok" {
		t.Fatalf("health = %+v, want an explicit ok", response.Completeness.Health)
	}
}
