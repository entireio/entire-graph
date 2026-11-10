package sem

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWorktreeRejectsIndexModeListingFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX Git wrapper")
	}
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "source.go", "package p\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "seed")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in *\"ls-files -s -z\"*) exit 41;; esac\nexec \"$REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("PATH", bin)
	_, err = BuildProviderSnapshotWithOptions(t.Context(), repo, "test", ProviderSnapshotOptions{Worktree: true})
	if err == nil || !strings.Contains(err.Error(), "index") {
		t.Fatalf("index listing failure not propagated: %v", err)
	}
}
