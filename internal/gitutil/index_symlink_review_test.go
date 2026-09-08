package gitutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIndexUnreadableReplacementIsNotHidden(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	write(t, repo, "target", "real.go")
	oid := gitOutput(t, repo, "hash-object", "-w", "target")
	git(t, repo, "update-index", "--add", "--cacheinfo", "120000,"+oid+",link.go")
	name := filepath.Join(repo, "link.go")
	write(t, repo, "link.go", "newfile")
	if err := os.Chmod(name, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(name, 0600) })
	if _, err := os.ReadFile(name); err == nil {
		t.Skip("user can read mode-000 files")
	}
	entries, err := IndexNonRegularPaths(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := IndexReplacedNonRegularPaths(t.Context(), repo, entries)
	if err != nil {
		return
	}
	if _, ok := replaced["link.go"]; !ok {
		t.Fatal("unreadable regular replacement silently hidden")
	}
}

func TestIndexTargetReaderBoundsBodies(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	write(t, repo, "target", strings.Repeat("x", maxSymlinkTargetBytes))
	atLimit := gitOutput(t, repo, "hash-object", "-w", "target")
	write(t, repo, "target", strings.Repeat("x", maxSymlinkTargetBytes+1))
	overLimit := gitOutput(t, repo, "hash-object", "-w", "target")
	reader, err := NewBatchFileReader(t.Context(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for i := 0; i < 20; i++ {
		blob, ok, err := reader.readIndexTarget(overLimit)
		if err != nil || ok || blob != "" {
			t.Fatalf("oversized target admitted: len=%d ok=%v err=%v", len(blob), ok, err)
		}
		blob, ok, err = reader.readIndexTarget(atLimit)
		if err != nil || !ok || len(blob) != maxSymlinkTargetBytes {
			t.Fatalf("bounded target after rejected body: len=%d ok=%v err=%v", len(blob), ok, err)
		}
	}
	if _, _, err := reader.readIndexTarget(strings.Repeat("1", 40)); err == nil {
		t.Fatal("missing index object silently accepted")
	}
}

func TestIndexMaterializedSymlinkReplacements(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	write(t, repo, "target", "real.go")
	oid := gitOutput(t, repo, "hash-object", "-w", "target")
	names := []string{"link.go"}
	if runtime.GOOS != "windows" {
		names = append(names, "0:link.go", "we\nird.go")
	}
	for _, name := range names {
		git(t, repo, "update-index", "--add", "--cacheinfo", "120000,"+oid+","+name)
		write(t, repo, name, "real.go")
	}
	entries, err := IndexNonRegularPaths(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := IndexReplacedNonRegularPaths(t.Context(), repo, entries)
	if err != nil || len(replaced) != 0 {
		t.Fatalf("untouched materializations: %v %v", replaced, err)
	}
	for _, name := range names {
		write(t, repo, name, "newfile")
	}
	replaced, err = IndexReplacedNonRegularPaths(t.Context(), repo, entries)
	if err != nil || len(replaced) != len(names) {
		t.Fatalf("same-length replacements: %v %v", replaced, err)
	}
}
