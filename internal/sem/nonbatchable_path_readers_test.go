package sem

import (
	"runtime"
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
