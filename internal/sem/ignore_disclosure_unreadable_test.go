package sem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unreadableDirectoriesHold probes whether this user and filesystem actually
// honour a mode-000 directory, the way foldsCase probes case folding: running as
// root, or on a filesystem that ignores the permission bits, makes the fixture
// below meaningless and the assertion vacuous. A GOOS check would be a guess
// about the same property.
func unreadableDirectoriesHold(t *testing.T) bool {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "probe")
	if err := os.MkdirAll(filepath.Join(probe, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(probe, 0o000); err != nil {
		return false
	}
	t.Cleanup(func() { _ = os.Chmod(probe, 0o755) })
	_, err := os.ReadDir(probe)
	return err != nil
}

// prunedTreeResponse runs the walk fallback over root and hands back the whole
// response, because the claim under test spans two of its fields.
func prunedTreeResponse(t *testing.T, root string) SearchResponse {
	t.Helper()
	response, err := SearchRepository(t.Context(), root, "test", "bearer token validation", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     5,
	})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// TestPrunedTreeDisclosureAdmitsWhatItCouldNotRead is the honesty of the count.
//
// RepoIgnoreReport.Files documents itself as "the exact number of listed paths
// excluded", and the coverage line renders it as a count of files the repository
// removed. The prune enumerator walked the ignored tree discarding every WalkDir
// error, so a directory it could not open contributed nothing and said nothing:
// the response came back successful, with a smaller number, still presented as
// exact. An understated security disclosure is worse than a loud one, because
// the reader has no way to tell it is understated.
//
// Uses only fields that exist before the fix, so it compiles unchanged against
// the current head and FAILS THERE AT RUNTIME: three files leave the corpus, the
// report claims one, and partial_failures is empty.
func TestPrunedTreeDisclosureAdmitsWhatItCouldNotRead(t *testing.T) {
	t.Parallel()
	if !unreadableDirectoriesHold(t) {
		t.Skip("this user can read a mode-000 directory (root, or a filesystem that ignores the bits), so the fixture cannot be built")
	}
	root := t.TempDir()
	write(t, root, graphIgnoreFileName, "hidden/\n")
	write(t, root, "hidden/auth.go", "package hidden\n\nfunc ValidateToken(token string) bool { return len(token) == 64 }\n")
	write(t, root, "hidden/sub/deep.go", "package sub\n\nfunc DeepToken(token string) bool { return len(token) == 32 }\n")
	write(t, root, "hidden/sub/deeper.go", "package sub\n\nfunc DeeperToken(token string) bool { return token != \"\" }\n")
	write(t, root, "visible/auth_stub.go", "package visible\n\nfunc ValidateTokenStub(token string) bool { return token != \"\" }\n")

	unreadable := filepath.Join(root, "hidden", "sub")
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	// Registered after t.TempDir's own cleanup, so it runs first and the tree is
	// removable again.
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o755) })

	response := prunedTreeResponse(t, root)
	if response.RepoIgnored == nil {
		t.Fatalf("a %s prune removed three source files and the response disclosed nothing", graphIgnoreFileName)
	}
	if response.RepoIgnored.Files == 3 {
		return // The walk read everything after all; nothing was understated.
	}
	admitted := ""
	for _, failure := range response.PartialFailures {
		if strings.Contains(failure.FilePath, "hidden/sub") || strings.Contains(failure.Detail, "hidden/sub") {
			admitted = failure.Code
		}
	}
	if admitted == "" {
		t.Fatalf("the disclosure counts %d of the 3 files the %s rule removed because it could not read "+
			"hidden/sub, and reports the shortfall nowhere: partial_failures=%+v, report=%+v",
			response.RepoIgnored.Files, graphIgnoreFileName, response.PartialFailures, response.RepoIgnored)
	}
}

// TestReadablePrunedTreeAdmitsNothing is the kind-(b) guard on the fix above and
// must pass BEFORE and AFTER it. A prune the walk read completely has nothing to
// admit, and a partial failure raised on the ordinary path would be exactly the
// noise that teaches readers to skip the disclosure that matters.
func TestReadablePrunedTreeAdmitsNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, graphIgnoreFileName, "hidden/\n")
	write(t, root, "hidden/auth.go", "package hidden\n\nfunc ValidateToken(token string) bool { return len(token) == 64 }\n")
	write(t, root, "hidden/sub/deep.go", "package sub\n\nfunc DeepToken(token string) bool { return len(token) == 32 }\n")
	write(t, root, "visible/auth_stub.go", "package visible\n\nfunc ValidateTokenStub(token string) bool { return token != \"\" }\n")

	response := prunedTreeResponse(t, root)
	if response.RepoIgnored == nil || response.RepoIgnored.Files != 2 {
		t.Fatalf("the readable prune should disclose exactly its two files: %+v", response.RepoIgnored)
	}
	if len(response.PartialFailures) != 0 {
		t.Fatalf("a fully readable prune raised %+v; the disclosure must stay quiet when its count is exact",
			response.PartialFailures)
	}
}
