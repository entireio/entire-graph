package gitutil

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestStreamNULDirectoryEntriesHandlesLargeMixedOutput reproduces the trail
// finding on ListIgnoredWorktreeDirectoryEntries: `--directory` only
// collapses a directory git classifies as wholly ignored. A directory
// ignored only by a file-pattern rule (e.g. `*.o`) alongside other content is
// never collapsed, so an untrusted checkout with a build or dependency tree
// matched by such a pattern makes git print every one of those filenames
// individually — potentially millions of them, entirely attacker-controlled.
// The prior implementation ran that listing through `run`, which buffers the
// COMPLETE subprocess stdout into memory before splitNULDirectoryEntries
// ever discarded a single non-directory field.
//
// This drives a synthetic producer standing in for such a listing (one
// million non-directory fields plus two real directory entries) through
// streamNULDirectoryEntries directly, so the test exercises the actual pipe
// + incremental-filter code path rather than only its output-format parsing.
// It pins two things a regression to the old buffer-then-filter approach
// would break: exactly the directory entries come back (no leaked filenames,
// no lost/duplicated directories), and processing a million fields does not
// take remotely as long as buffering and then linearly re-scanning that data
// would — the streaming/filtering must happen in the same pass.
func TestStreamNULDirectoryEntriesHandlesLargeMixedOutput(t *testing.T) {
	const fields = 1_000_000
	script := fmt.Sprintf(
		`awk 'BEGIN{for(i=0;i<%d;i++) printf "vendor/dep-%%d.o%%c", i, 0; printf "build/%%c", 0; printf "dist/%%c", 0; printf "build/%%c", 0}'`,
		fields,
	)

	start := time.Now()
	dirs, err := streamNULDirectoryEntries(t.Context(), t.TempDir(), "sh", "-c", script)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("streamNULDirectoryEntries: %v", err)
	}

	want := map[string]bool{"build/": true, "dist/": true}
	if len(dirs) != len(want) {
		t.Fatalf("dirs = %#v, want exactly %v (no leaked filenames, duplicate build/ deduped)", dirs, want)
	}
	for _, d := range dirs {
		if !want[d] {
			t.Fatalf("dirs contains unexpected entry %q (a pattern-ignored filename leaked through): %#v", d, dirs)
		}
	}

	// A generous ceiling: this is a fast local subprocess pipe read of ~13MB
	// with a filter that is O(1) work per field. Multiple seconds would
	// indicate a regression back to materializing the whole listing (or
	// worse, a quadratic dedup/append path) rather than filtering as it
	// streams.
	const budget = 5 * time.Second
	if elapsed > budget {
		t.Fatalf("streamNULDirectoryEntries took %s to process %d fields (budget %s): "+
			"this smells like the listing is being buffered/rescanned instead of streamed", elapsed, fields, budget)
	}
}

// TestStreamNULDirectoryEntriesTruncatesAPathologicalFieldCount is the
// narrowing direction the trail finding on ListIgnoredWorktreeDirectoryEntries
// (git.go:152) is about: the prior fix above bounds this call to the
// ORDINARY case (filtering as fields arrive instead of buffering), but a
// scanned repository that arranges for a file-pattern ignore matching FAR
// more entries than the sweep's own directory budget, with zero of them ever
// collapsing into a directory, still made the loop read every one of them
// before returning -- unbounded CPU and pipe I/O ahead of the caller's own
// budget, and unbounded ahead of anything this test's sibling above proves
// bounded. This drives a producer well past maxIgnoredDirectoryFields with NO
// real directory entries anywhere in the stream (unlike the sibling test,
// which places two after a mere 1,000,000) and asserts the call gives up --
// reporting errIgnoredListingTruncated -- rather than draining the full
// stream, in time comparable to the bounded case above rather than scaling
// with the producer's true size.
func TestStreamNULDirectoryEntriesTruncatesAPathologicalFieldCount(t *testing.T) {
	const fields = maxIgnoredDirectoryFields * 3
	script := fmt.Sprintf(
		`awk 'BEGIN{for(i=0;i<%d;i++) printf "vendor/dep-%%d.o%%c", i, 0}'`,
		fields,
	)

	start := time.Now()
	dirs, err := streamNULDirectoryEntries(t.Context(), t.TempDir(), "sh", "-c", script)
	elapsed := time.Since(start)

	if !errors.Is(err, errIgnoredListingTruncated) {
		t.Fatalf("streamNULDirectoryEntries err = %v, want errIgnoredListingTruncated", err)
	}
	if len(dirs) != 0 {
		t.Fatalf("a truncated listing returned %d dir(s), want none returned alongside the error", len(dirs))
	}
	// A generous ceiling well under how long fully producing and draining
	// 3x the field bound would take: this call must give up at the bound,
	// not after the producer finishes emitting fields ~3x past it.
	const budget = 10 * time.Second
	if elapsed > budget {
		t.Fatalf("streamNULDirectoryEntries took %s to give up on a %d-field pathological listing (budget %s): "+
			"it is reading past maxIgnoredDirectoryFields instead of stopping there", elapsed, fields, budget)
	}
}
