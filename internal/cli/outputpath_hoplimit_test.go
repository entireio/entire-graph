package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestOutsideRouteHopLimitNeverExceedsTheHostPathnameLimit is the regression for a SECOND notion of
// the symlink budget.
//
// openUnconfinedOutputFile expands every link itself and hands each component to a separate openat,
// so the kernel never sees the chain as a chain and its own cumulative ELOOP counter never fires.
// The limit this walk applies is therefore the ONLY limit there is, and a fixed 40 — the Linux
// figure — allowed chains Darwin, the BSDs, illumos, Solaris and AIX all refuse: the walk would
// follow a 33-link route to a destination the host itself would never have resolved, and write
// there. That is the same defect the contained walk was already fixed for (linkHopLimitFor), which
// is why this walk now asks the same function rather than carrying a number of its own.
//
// The host's limit is MEASURED rather than asserted, so this stays a claim about the machine the
// test runs on instead of a restatement of the table in agents.go. On Linux the two figures
// coincide (SYMLOOP_MAX 40) and this test can only pass; everywhere else it fails without the fix.
func TestOutsideRouteHopLimitNeverExceedsTheHostPathnameLimit(t *testing.T) {
	requireSymlinkSupport(t)
	t.Parallel()

	hostLimit := hostPathnameLinkLimit(t)
	// The shortest chain the host itself refuses. Following it is the bug: the destination is one
	// this machine's own pathname resolution cannot reach.
	refusedByHost := hostLimit + 1

	dir := t.TempDir()
	route := linkChain(t, dir, refusedByHost)
	if _, err := os.Stat(route); err == nil {
		t.Fatalf("the %d-link chain the host was measured to refuse resolved after all", refusedByHost)
	}

	// A caller-owned destination: outside the repository, so this is the unconfined walk.
	err := writeOutputFileUnder(t.TempDir(), route, []byte("x"), 0o644, false)
	if err == nil {
		t.Fatalf("the walk followed a %d-link chain that %s itself refuses (host limit %d): "+
			"a fixed hop budget lets this machine's ELOOP become a write",
			refusedByHost, runtime.GOOS, hostLimit)
	}
	if !strings.Contains(err.Error(), "symbolic links") {
		t.Fatalf("the chain was refused for the wrong reason: %v", err)
	}
}

// hostPathnameLinkLimit measures how many symbolic links this host's own pathname resolution will
// follow, by asking the kernel — one chain, walked from progressively deeper links.
func hostPathnameLinkLimit(t *testing.T) int {
	t.Helper()
	const probe = 64
	dir := t.TempDir()
	linkChain(t, dir, probe)
	for length := 1; length <= probe; length++ {
		if _, err := os.Stat(filepath.Join(dir, chainLinkName(probe-length))); err != nil {
			return length - 1
		}
	}
	t.Fatalf("this host resolved a %d-link chain; the probe is too short to find its limit", probe)
	return 0
}

// linkChain builds link0 -> link1 -> … -> link(n-1) -> target inside dir and returns the path whose
// resolution follows exactly n links. Every target is relative, so the chain is one link per hop
// with no directory components of its own.
func linkChain(t *testing.T, dir string, n int) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "target"), []byte("victim\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for index := n - 1; index >= 0; index-- {
		next := "target"
		if index < n-1 {
			next = chainLinkName(index + 1)
		}
		link := filepath.Join(dir, chainLinkName(index))
		if err := os.Symlink(next, link); err != nil {
			if _, statErr := os.Lstat(link); statErr == nil {
				continue
			}
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, chainLinkName(0))
}

func chainLinkName(index int) string {
	return "link" + strconv.Itoa(index)
}
