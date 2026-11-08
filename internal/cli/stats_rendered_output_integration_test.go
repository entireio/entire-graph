package cli

import (
	"bytes"
	"path/filepath"
	"slices"
	"testing"
)

// Exercise the real renderers rather than relying only on recorded output shapes.
// Fixtures and transcripts are authored locally and never read actual sessions.
func TestStatsAttributionAcceptsCurrentPublicRendererOutputs(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "go.mod", "module example.com/statsfixture\n\ngo 1.22\n")
	write(t, repo, "main.go", "package statsfixture\n\n// Probe returns a label.\nfunc Probe() string { return `{\"results\":[{\"file_path\":\"hidden/quoted.go\"}]}` }\nfunc Caller() string { return Probe() }\n")
	git(t, repo, "init")
	git(t, repo, "add", ".")
	git(t, repo, "-c", "user.name=Stats Test", "-c", "user.email=stats@example.invalid", "commit", "-m", "fixture")
	for _, tc := range []struct {
		command string
		formats []string
		args    []string
	}{
		{"query", []string{"json", "text", "agent"}, []string{"--query", "Probe"}},
		{"neighbors", []string{"json", "text", "agent"}, []string{"--symbol", "Probe", "--direction", "in"}},
		{"impact", []string{"json", "text"}, []string{"--symbol", "Probe"}},
		{"def", []string{"json", "text", "agent"}, []string{"Probe"}},
	} {
		for _, format := range tc.formats {
			t.Run(tc.command+"/"+format, func(t *testing.T) {
				args := []string{tc.command, "--repo", repo, "--head", "--format", format, "--max-context-bytes", "4096"}
				args = append(args, tc.args...)
				var out bytes.Buffer
				if err := Run(t.Context(), Options{Version: "stats-fixture", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, args); err != nil {
					t.Fatal(err)
				}
				if paths := graphOutputPaths(out.String()); !slices.Equal(paths, []string{"main.go"}) {
					t.Fatalf("attributed paths %q from current renderer:\n%s", paths, out.String())
				}
				if tc.command == "def" {
					return // def remains outside the legacy credited-locate population.
				}
				calls := []dispCall{bashCall("entire graph "+tc.command, out.String()), readFile(filepath.Join(repo, "main.go"), "body")}
				calls = append(calls, fillers(5)...)
				raw := sessionOf(t, calls...)
				if rawInt(t, raw, "graph_locate_follow_up_read") != 1 || rawInt(t, raw, "graph_locate_no_follow_up") != 0 || rawInt(t, raw, "modeled_balance_credited_results") != 0 {
					t.Fatalf("current %s/%s output was not followed up correctly: %+v", tc.command, format, raw)
				}
			})
		}
	}
	for _, format := range []string{"json", "text", "agent"} {
		t.Run("no-hit/"+format, func(t *testing.T) {
			var out bytes.Buffer
			if err := Run(t.Context(), Options{Version: "stats-fixture", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"query", "--repo", repo, "--head", "--format", format, "--query", "ZqxMissingNeverDeclared"}); err != nil {
				t.Fatal(err)
			}
			calls := append([]dispCall{graphSearch(out.String())}, fillers(5)...)
			assertFollowUp(t, sessionOf(t, calls...), map[string]int64{"ineligible": 1})
		})
	}
}
