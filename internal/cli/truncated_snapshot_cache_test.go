package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// truncatedSnapshotRepo is the smallest repository whose records cache can
// round-trip: one committed file with one function, so the real stream emits a
// header, a file, a symbol, at least one relation and a summary.
func truncatedSnapshotRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.js", "function validateToken(token) {\n  return Boolean(token);\n}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

// streamMarkingSummary runs the real stream and, when truncated is set, adds the
// truncation marker to its summary. Everything else -- the header the cache
// validates against the tree and commit, every record, the summary's own
// counts -- is the real build's, so the only difference a cache writer can see
// between the two runs is the marker. No build produces that marker any more
// (a stopped run returns its context's error instead), which is why it has to
// be injected to prove the refusals that guard against one.
func streamMarkingSummary(truncated bool, calls *int) func(context.Context, string, string, sem.ProviderSnapshotOptions, func(any) error) error {
	return func(ctx context.Context, repo, providerVersion string, options sem.ProviderSnapshotOptions, emit func(any) error) error {
		*calls++
		return sem.StreamSnapshot(ctx, repo, providerVersion, options, func(record any) error {
			if summary, ok := record.(sem.SnapshotSummary); ok && truncated {
				summary.PartialFailures = append(summary.PartialFailures, sem.PartialFailure{
					Code:                 sem.AnalysisBudgetExceededCode,
					Severity:             "warning",
					EffectOnCompleteness: "snapshot truncated",
				})
				summary.Stats.PartialFailures = len(summary.PartialFailures)
				record = summary
			}
			return emit(record)
		})
	}
}

// TestTruncatedIndexIsNeverCached pins the CLI half of the cache refusal on
// both record paths: the unfiltered stream (`symbols`) and the targeted edge
// query (`edges --to`, the filterActive branch). A merge once left the targeted
// branch printing "were not cached" and then storing the records anyway, so
// each path is asserted on its own.
//
// Each case also runs a complete build through the same seam and checks that
// it IS cached, so the refusal cannot pass against a cache that never stores.
func TestTruncatedIndexIsNeverCached(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		args    []string
		message string
	}{
		{"symbols", []string{"symbols", "--format", "ndjson"}, "result is partial and was not cached"},
		{"edges --to (filterActive)", []string{"edges", "--format", "ndjson", "--to", "validateToken"}, "the matched edges are partial and were not cached"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := func(repo, cacheDir string, truncated bool, calls *int) string {
				t.Helper()
				var stdout, stderr bytes.Buffer
				args := append(append([]string{}, tc.args...), "--repo", repo, "--cache-dir", cacheDir)
				err := Run(t.Context(), Options{
					Env:            EntireEnv{RepoRoot: repo},
					Stdout:         &stdout,
					Stderr:         &stderr,
					streamSnapshot: streamMarkingSummary(truncated, calls),
				}, args)
				if err != nil {
					t.Fatalf("%v: %v (stderr %q)", args, err, stderr.String())
				}
				return stderr.String()
			}

			// Control: a complete build is stored, so the second run is a hit
			// and never reaches the stream.
			repo, cacheDir := truncatedSnapshotRepo(t), t.TempDir()
			calls := 0
			if stderr := run(repo, cacheDir, false, &calls); strings.Contains(stderr, "not cached") {
				t.Fatalf("a complete build reported itself uncached: %q", stderr)
			}
			run(repo, cacheDir, false, &calls)
			if calls != 1 {
				t.Fatalf("control: a complete build was not served from the cache on the second run (%d stream call(s), want 1)", calls)
			}

			// A truncated build must say so and must not be stored: the second
			// run has to rebuild.
			repo, cacheDir = truncatedSnapshotRepo(t), t.TempDir()
			calls = 0
			if stderr := run(repo, cacheDir, true, &calls); !strings.Contains(stderr, tc.message) {
				t.Fatalf("a truncated build must report %q on stderr, got %q", tc.message, stderr)
			}
			run(repo, cacheDir, false, &calls)
			if calls != 2 {
				t.Fatalf("a truncated build was cached and served as the complete index (%d stream call(s), want 2)", calls)
			}
		})
	}
}
