package cli

import (
	"bytes"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

func f06fixSmallRepo(t *testing.T) string {
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

func f06fixRun(t *testing.T, repo string, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), Options{Env: EntireEnv{RepoRoot: repo}, Stdout: &stdout, Stderr: &stderr}, args)
	return stdout.String(), stderr.String(), err
}

// TestF06FixCompactNdjsonRejectsMaxSeconds reproduces the MEDIUM finding. A
// compact artifact is defined to represent a COMPLETE snapshot: LoadCompactSnapshot
// and snapshot-query accept whatever prefix they are given, so a budget-truncated
// compact file turns every missing symbol into a confident negative answer.
// The combination has to be refused at the flag boundary.
func TestF06FixCompactNdjsonRejectsMaxSeconds(t *testing.T) {
	repo := f06fixSmallRepo(t)

	_, _, err := f06fixRun(t, repo, "snapshot", "--repo", repo, "--format", "compact-ndjson", "--max-seconds", "1", "--no-cache")
	if err == nil {
		t.Fatal("--max-seconds with --format compact-ndjson must be rejected: a truncated compact artifact is indistinguishable from a complete one")
	}
	if !strings.Contains(err.Error(), "compact-ndjson") || !strings.Contains(err.Error(), "--max-seconds") {
		t.Fatalf("the error must name both flags, got %v", err)
	}
	// 0 (explicitly unlimited) is still fine: nothing can be truncated.
	if _, _, err := f06fixRun(t, repo, "snapshot", "--repo", repo, "--format", "compact-ndjson", "--max-seconds", "0", "--no-cache"); err != nil {
		t.Fatalf("--max-seconds 0 with compact output must stay accepted, got %v", err)
	}
}

// TestF06FixMaxSecondsOverflowRejected reproduces the LOW overflow finding.
// strconv.Atoi accepts every int64, but seconds * time.Second overflows past
// math.MaxInt64/1e9 and wraps to a NEGATIVE duration, which makes
// context.WithDeadline fire in the past or, on the provider commands, disables
// the ceiling entirely (MaxDuration > 0 is false).
func TestF06FixMaxSecondsOverflowRejected(t *testing.T) {
	repo := f06fixSmallRepo(t)
	overflow := strconv.Itoa(math.MaxInt64)
	justOver := strconv.FormatInt(int64(math.MaxInt64/int64(time.Second))+1, 10)
	maxOK := strconv.FormatInt(int64(math.MaxInt64/int64(time.Second)), 10)

	for _, mode := range []string{"symbols", "edges", "snapshot"} {
		for _, value := range []string{overflow, justOver} {
			if _, _, err := f06fixRun(t, repo, mode, "--repo", repo, "--format", "ndjson", "--no-cache", "--max-seconds", value); err == nil {
				t.Fatalf("%s --max-seconds %s must be rejected: it overflows time.Duration and disables the ceiling", mode, value)
			}
		}
		if _, _, err := f06fixRun(t, repo, mode, "--repo", repo, "--format", "ndjson", "--no-cache", "--max-seconds", maxOK); err != nil {
			t.Fatalf("%s --max-seconds %s (the largest representable value) must stay accepted, got %v", mode, maxOK, err)
		}
	}
	// The same parser bug exists on diff/commit, which have carried
	// --max-seconds since before this change.
	if _, _, err := f06fixRun(t, repo, "commit", "--repo", repo, "--max-seconds", overflow); err == nil {
		t.Fatalf("commit --max-seconds %s must be rejected", overflow)
	}
}
