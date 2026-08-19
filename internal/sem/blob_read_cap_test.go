package sem

import (
	"runtime"
	"strings"
	"testing"
)

// TestAnalyzeGitRangeRefusesBlobsAboveTheReadCap pins the memory and time bound
// of the semantic diff: `diff`/`commit` read BOTH sides of every changed file,
// so an uncapped read makes one oversized blob set the command's cost. The file
// must not vanish either — it is skipped with a machine-readable
// E_FILE_TOO_LARGE warning, the same code the snapshot path uses.
func TestAnalyzeGitRangeRefusesBlobsAboveTheReadCap(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)

	// One long string literal, so the blob is large but cheap to lex: the test
	// is about the read, not about parser throughput.
	// Sized against defaultMaxParseBytes rather than against the diff cap's own
	// name, so this file compiles against a tree where the cap does not exist
	// yet and the test can be shown to FAIL there rather than not build.
	huge := func(fill string) string {
		return "BLOB = \"" + strings.Repeat(fill, defaultMaxParseBytes+1024) + "\"\n"
	}
	write(t, repo, "huge.py", huge("x"))
	write(t, repo, "small.py", "def helper(value):\n    return value\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")
	write(t, repo, "huge.py", huge("y"))
	write(t, repo, "small.py", "def helper(value):\n    return value + 1\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "change")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(t.Context(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, file := range result.Files {
		if file.Path == "huge.py" {
			t.Fatalf("a blob above the %d-byte read cap was analyzed: %#v", defaultMaxParseBytes, file)
		}
	}
	analyzedSmall := false
	for _, file := range result.Files {
		if file.Path == "small.py" {
			analyzedSmall = true
		}
	}
	if !analyzedSmall {
		t.Fatalf("the cap must refuse only the oversized file; small.py was not analyzed: %#v", result.Files)
	}

	var refusal *ProviderWarning
	for i, warning := range result.Warnings {
		if warning.Code == "E_FILE_TOO_LARGE" && warning.FilePath == "huge.py" {
			refusal = &result.Warnings[i]
		}
	}
	if refusal == nil {
		t.Fatalf("a refused file must not disappear silently; warnings = %#v", result.Warnings)
	}
	if refusal.Severity != "warning" {
		t.Fatalf("severity = %q, want warning", refusal.Severity)
	}
	if refusal.EffectOnCompleteness == "" || refusal.Detail == "" {
		t.Fatalf("refusal warning must explain itself: %#v", *refusal)
	}
}

// TestHeadReadersCapNewlineBearingPaths pins the bound on the one HEAD read
// that the `git cat-file --batch` protocol cannot carry. The batch reader is
// line based, so a Git path containing a newline falls back to a one-shot
// reader — a fallback selected by the file's NAME, which the repository under
// analysis chooses. An uncapped fallback therefore lets any repository opt a
// blob out of the cap by renaming it.
func TestHeadReadersCapNewlineBearingPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Win32 rejects newlines (and every other control character) in file
		// names, so this input cannot exist there and Git cannot check it out.
		t.Skip("a newline-bearing file name is unrepresentable on Windows")
	}

	const newlinePath = "over\ncap.py"
	const smallNewlinePath = "under\ncap.py"

	repo := t.TempDir()
	initRepo(t, repo)
	// defaultMaxParseBytes is the ceiling openSearchContentReader applies, so
	// the oversized fixture is sized against it; openSource takes its ceiling
	// from the caller and is exercised with a much smaller one below.
	writeSparseFile(t, repo, newlinePath, int64(defaultMaxParseBytes)+1, "# over the cap\n")
	write(t, repo, smallNewlinePath, "def helper(value):\n    return value\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	head := rev(t, repo, "HEAD")

	t.Run("openSource", func(t *testing.T) {
		opened, err := openSource(t.Context(), repo, head, sourceOptions{maxReadBytes: 1024})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if opened.close != nil {
				_ = opened.close()
			}
		}()
		listed := false
		for _, path := range opened.paths {
			if path == newlinePath {
				listed = true
			}
		}
		if !listed {
			t.Fatalf("fixture is not exercising the read closure; paths = %#v", opened.paths)
		}
		if content, ok := opened.read(newlinePath); ok {
			t.Fatalf("read returned %d bytes for a blob above the 1024-byte cap", len(content))
		}
		if content, ok := opened.readPrefix(newlinePath, 64); ok {
			t.Fatalf("readPrefix returned %d bytes for a blob above the 1024-byte cap", len(content))
		}
		if _, ok := opened.read(smallNewlinePath); !ok {
			t.Fatal("a newline-bearing path under the cap must still be readable")
		}
	})

	// The refused blob must still be accounted for. docs/trust-and-security.md
	// promises that a file the graph cannot process emits a machine-readable
	// partial failure "rather than disappearing silently", and that promise is
	// the reason this assertion does not name a code: it holds whichever code
	// the snapshot chooses for a read it declined.
	t.Run("snapshotAccountsForTheRefusedFile", func(t *testing.T) {
		snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test", ProviderSnapshotOptions{MaxParseBytes: 1024})
		if err != nil {
			t.Fatal(err)
		}
		reported := false
		for _, failure := range snapshot.Header.PartialFailures {
			if failure.FilePath == newlinePath {
				reported = true
				if failure.Code == "" || failure.EffectOnCompleteness == "" {
					t.Fatalf("partial failure must be machine-readable: %#v", failure)
				}
			}
		}
		if !reported {
			t.Fatalf("the refused file disappeared silently; partial failures = %#v", snapshot.Header.PartialFailures)
		}
	})

	t.Run("openSearchContentReader", func(t *testing.T) {
		read, closeReader, err := openSearchContentReader(t.Context(), repo, head, true, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if closeReader != nil {
				_ = closeReader()
			}
		}()
		if content, ok := read(newlinePath); ok {
			t.Fatalf("read returned %d bytes for a blob above the %d-byte cap", len(content), defaultMaxParseBytes)
		}
		if _, ok := read(smallNewlinePath); !ok {
			t.Fatal("a newline-bearing path under the cap must still be readable")
		}
	})
}
