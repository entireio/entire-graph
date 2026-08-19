package sem

import (
	"runtime"
	"strings"
	"testing"
)

// Every reader built on `git cat-file --batch` has to route a path the line
// protocol cannot carry to the argv-based one-shot reader. There are two such
// shapes, not one: an embedded newline, and a TRAILING CARRIAGE RETURN, which
// git's own request parser strips before the lookup so the request silently
// resolves to the CR-less name instead.
//
// A tracked path ending in CR is not hypothetical for these readers. Language
// detection matches `dockerfile.` and `makefile.` by PREFIX, so "Dockerfile.dev\r"
// is a supported, indexable path — and the decoy "Dockerfile.dev" alongside it is
// exactly what the stripped request resolves to.
//
// Each reader used to carry its own copy of the rule and each copy tested for
// newlines only, so this shape reached the batch reader. Routing on the reader's
// own answer instead makes the set total: one place decides, every caller obeys.
func TestHeadReadersRouteCRSuffixedPathsToTheArgvSafeReader(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows filenames cannot end in a carriage return; the shape is unrepresentable there")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	const crPath = "Dockerfile.dev\r"
	const crContent = "FROM alpine\nRUN echo the-cr-suffixed-file\n"
	// The decoy: the same name without the CR. git strips the trailing CR from
	// the batch request line, so an unguarded reader answers a request for
	// crPath with THIS blob and reports success.
	write(t, repo, "Dockerfile.dev", "FROM alpine\nRUN echo the-DECOY\n")
	write(t, repo, crPath, crContent)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "a supported path ending in a carriage return")
	head := rev(t, repo, "HEAD")

	if !Supported(crPath) {
		t.Fatalf("%q is not a supported path, so no reader would ever be asked for it", crPath)
	}

	t.Run("provider committed source", func(t *testing.T) {
		opened, err := openSource(t.Context(), repo, head, sourceOptions{maxReadBytes: defaultMaxParseBytes})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if opened.close != nil {
				_ = opened.close()
			}
		}()
		content, ok := opened.read(crPath)
		if !ok {
			t.Fatalf("openSource read(%q) = not found; a tracked file was dropped", crPath)
		}
		if content != crContent {
			t.Fatalf("openSource read(%q) = %q, want %q", crPath, content, crContent)
		}
	})

	t.Run("search head content", func(t *testing.T) {
		read, closeReader, err := openSearchContentReader(t.Context(), repo, head, true, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if closeReader != nil {
				_ = closeReader()
			}
		}()
		content, ok := read(crPath)
		if !ok {
			t.Fatalf("search head read(%q) = not found; a tracked file was dropped", crPath)
		}
		if content != crContent {
			t.Fatalf("search head read(%q) = %q, want %q", crPath, content, crContent)
		}
	})
}

// TestOpenSourceBoundsTheNonBatchableFallbackRead pins openSource's stated
// bound -- "a file above maxReadBytes is never materialized, and is instead
// recorded in the oversize registry from a streamed digest" -- on the path that
// does NOT go through the batch reader. The batch reader is capped by
// SetMaxBytes(maxReadBytes); the argv fallback for a path the line protocol
// cannot carry had no cap at all, so one CR-suffixed tracked file could make
// the snapshot's memory the size of the largest blob in the revision, and the
// oversize registry never learned the file existed.
func TestOpenSourceBoundsTheNonBatchableFallbackRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows filenames cannot end in a carriage return; the shape is unrepresentable there")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	const crPath = "Dockerfile.dev\r"
	const maxReadBytes = 4096
	crContent := "FROM alpine\n" + strings.Repeat("RUN echo padding-well-past-the-read-ceiling\n", 100_000)
	write(t, repo, crPath, crContent)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "an oversized blob at a path the batch protocol cannot carry")
	head := rev(t, repo, "HEAD")

	opened, err := openSource(t.Context(), repo, head, sourceOptions{maxReadBytes: maxReadBytes, maxFiles: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if opened.close != nil {
			_ = opened.close()
		}
	}()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	content, ok := opened.read(crPath)
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc

	if ok {
		t.Errorf("read(%q) returned %d bytes with a %d-byte ceiling: the non-batchable fallback is unbounded",
			crPath, len(content), maxReadBytes)
	}
	if ceiling := uint64(len(crContent)); allocated >= ceiling {
		t.Errorf("read allocated %d bytes, want < %d: the blob was materialized instead of refused by size",
			allocated, ceiling)
	}
	// Refusing it is only half the contract: the file must still be EXPLAINED,
	// or bounding the read has traded an unbounded allocation for a silent drop.
	record, recorded := opened.oversize(crPath)
	if !recorded {
		t.Fatalf("oversize(%q) = not recorded; the refused file has no explanation", crPath)
	}
	if got, want := record.Bytes, int64(len(crContent)); got != want {
		t.Errorf("oversize(%q).Bytes = %d, want %d", crPath, got, want)
	}
	if record.Hash == "" || record.Lines == 0 {
		t.Errorf("oversize(%q) = %+v, want the streamed hash and line count the batch reader records", crPath, record)
	}
}

// TestHeadReadersRefuseANonBlobAtANonBatchablePath is the previous test's other
// half. Routing a CR-suffixed path to the argv reader is only correct if that
// reader answers the same QUESTION the batch reader does: "give me this blob".
//
// `git ls-tree -r` lists a GITLINK -- a tree entry of mode 160000, the shape a
// submodule takes -- exactly like a file, and "Dockerfile.dev\r" is a supported
// path (prefix language detection) that the line protocol cannot carry. So a
// gitlink can be BOTH listed and routed to the fallback. The batch reader reads
// the object type out of its response header and reports a non-blob as absent;
// `git show` renders whatever the spec names, and for a gitlink pointing at a
// commit in this repository that is the commit itself. The provider then
// hashed, parsed and indexed commit output -- author line, message, diff -- as
// Dockerfile content, with no partial failure to show for it.
func TestHeadReadersRefuseANonBlobAtANonBatchablePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows filenames cannot end in a carriage return; the shape is unrepresentable there")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "keep.txt", "anchor\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "subject line\n\nRUN echo body-rendered-as-content")
	pointee := rev(t, repo, "HEAD")

	const crPath = "Dockerfile.dev\r"
	git(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+pointee+","+crPath)
	git(t, repo, "commit", "-m", "a gitlink at a path the batch protocol cannot carry")
	head := rev(t, repo, "HEAD")

	if !Supported(crPath) {
		t.Fatalf("%q is not a supported path, so no reader would ever be asked for it", crPath)
	}

	assertRefused := func(t *testing.T, label, content string, ok bool) {
		t.Helper()
		if ok {
			t.Fatalf("%s read(%q) = %q, ok=true; want not found -- a gitlink has no blob, and the rendered commit is not this file's content", label, crPath, content)
		}
	}

	t.Run("provider committed source", func(t *testing.T) {
		opened, err := openSource(t.Context(), repo, head, sourceOptions{maxReadBytes: defaultMaxParseBytes})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if opened.close != nil {
				_ = opened.close()
			}
		}()
		content, ok := opened.read(crPath)
		assertRefused(t, "openSource", content, ok)
	})

	t.Run("search head content", func(t *testing.T) {
		read, closeReader, err := openSearchContentReader(t.Context(), repo, head, true, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if closeReader != nil {
				_ = closeReader()
			}
		}()
		content, ok := read(crPath)
		assertRefused(t, "search head", content, ok)
	})
}
