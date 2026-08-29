package gitutil

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// execMarkerScript writes an executable POSIX script into a directory of its
// own and returns the script path plus the path of the "fired" file the script
// creates when it runs. The script finds that file through $0 rather than an
// embedded path, so the marker path never has to survive being quoted into a
// shell word. The script path itself still does — git runs core.fsmonitor
// through `sh -c`, where the configured value is shell input rather than an
// argv element — so callers that put it in config wrap it in posixShellWord.
func execMarkerScript(t *testing.T, name string, exitCode int) (script, marker string) {
	t.Helper()
	dir := t.TempDir()
	script = filepath.Join(dir, name)
	body := fmt.Sprintf("#!/bin/sh\n: > \"$(dirname \"$0\")/fired\"\nexit %d\n", exitCode)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, filepath.Join(dir, "fired")
}

// posixShellWord single-quotes a value so a POSIX shell parses it as one word:
// without it, a temporary directory containing a space would split the
// core.fsmonitor command and silently downgrade the control below to a skip.
func posixShellWord(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// markerFired reports whether the script ran, and clears the marker so the next
// assertion starts from a known state.
func markerFired(t *testing.T, marker string) bool {
	t.Helper()
	if _, err := os.Stat(marker); err != nil {
		return false
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	return true
}

// TestGitSubprocessesDisableRepositoryFsmonitorHook pins the fix for a
// confused-deputy bug: core.fsmonitor is a configuration value git EXECUTES
// (through `sh -c`) whenever it refreshes the index, and the value is read from
// the *target repository's* .git/config — attacker-controlled input for a tool
// whose entire purpose is indexing repositories the user has not audited.
// `git ls-files` and working-tree `git grep` both refresh the index, so before
// the fix simply listing a repository ran whatever command its config named.
func TestGitSubprocessesDisableRepositoryFsmonitorHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("git invokes the core.fsmonitor hook through `sh -c`, so this fixture needs a POSIX shell and a #!-script; the hardening itself is per-invocation config injection and identical on every platform")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "keep.go"), []byte("package keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "tracked file")

	hook, marker := execMarkerScript(t, "fsmonitor-hook", 0)
	git(t, repo, "config", "core.fsmonitor", posixShellWord(hook))

	// Control. Prove this git really does run the hook for the argv this package
	// uses; otherwise a git build that ignored core.fsmonitor would make every
	// assertion below pass without the fix being present.
	control := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	control.Dir = repo
	if out, err := control.CombinedOutput(); err != nil {
		t.Fatalf("control `git ls-files`: %v\n%s", err, out)
	}
	if !markerFired(t, marker) {
		t.Skip("this git does not run core.fsmonitor for `git ls-files`, so the hardening is unobservable here")
	}

	worktree, err := ListWorktreeFiles(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if markerFired(t, marker) {
		t.Error("ListWorktreeFiles executed the repository's core.fsmonitor command")
	}
	if !slices.Contains(worktree, "keep.go") {
		t.Errorf("ListWorktreeFiles lost its listing after hardening: %#v", worktree)
	}

	if _, err := ListIndexFiles(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	if markerFired(t, marker) {
		t.Error("ListIndexFiles executed the repository's core.fsmonitor command")
	}

	if _, err := ListIgnoredWorktreeFiles(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	if markerFired(t, marker) {
		t.Error("ListIgnoredWorktreeFiles executed the repository's core.fsmonitor command")
	}

	// The two working-tree grep paths build their own exec.Cmd instead of going
	// through newCmd, so they need the same hardening rather than inheriting it.
	matches, err := GrepIndexMatches(t.Context(), repo, []string{"package"}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if markerFired(t, marker) {
		t.Error("GrepIndexMatches executed the repository's core.fsmonitor command")
	}
	if len(matches) == 0 {
		t.Error("GrepIndexMatches lost its matches after hardening")
	}

	paths, err := GrepFixedStringPaths(t.Context(), repo, "", "package")
	if err != nil {
		t.Fatal(err)
	}
	if markerFired(t, marker) {
		t.Error("GrepFixedStringPaths executed the repository's core.fsmonitor command")
	}
	if !slices.Contains(paths, "keep.go") {
		t.Errorf("GrepFixedStringPaths lost its listing after hardening: %#v", paths)
	}
}

// TestGitLogSubprocessesDisableRepositorySignatureVerification pins the second
// half of the same class: gpg.program is a configuration value git EXECUTES,
// and log.showSignature — also repository config — is what makes git reach it.
// A commit carrying a `gpgsig` header is enough to trigger verification, and a
// repository the tool does not trust controls both the header and the program.
func TestGitLogSubprocessesDisableRepositorySignatureVerification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake gpg program is a #!-script, which needs a POSIX exec; the hardening itself is per-invocation config injection and identical on every platform")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "keep.go"), []byte("package keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "tracked file")
	if err := os.WriteFile(filepath.Join(repo, "keep.go"), []byte("package keep\n\nvar Second = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	// The checkpoint trailer goes on HEAD, which is the commit signCommitHeader
	// then forges a signature onto: git only reaches gpg.program for a commit it
	// actually displays, so the commit FindCommitWithCheckpoint selects has to be
	// the signed one or the assertion below would pass vacuously.
	const checkpoint = "cp-hardening-fixture"
	git(t, repo, "commit", "-m", "second change\n\nEntire-Checkpoint: "+checkpoint)

	signCommitHeader(t, repo)

	gpg, marker := execMarkerScript(t, "gpg", 1)
	git(t, repo, "config", "log.showSignature", "true")
	git(t, repo, "config", "gpg.program", gpg)

	// Control, as above: prove this git reaches gpg.program for the argv the
	// package uses before asserting that the hardened call does not.
	control := exec.Command("git", "log", "--all", "--format=%H", "-n", "1")
	control.Dir = repo
	if out, err := control.CombinedOutput(); err != nil {
		t.Fatalf("control `git log`: %v\n%s", err, out)
	}
	if !markerFired(t, marker) {
		t.Skip("this git does not run gpg.program for `git log`, so the hardening is unobservable here")
	}

	commit, err := FindCommitWithCheckpoint(t.Context(), repo, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if markerFired(t, marker) {
		t.Error("FindCommitWithCheckpoint executed the repository's gpg.program")
	}
	if commit == "" {
		t.Error("FindCommitWithCheckpoint lost its result after hardening")
	}

	if _, err := FileCochanges(t.Context(), repo, "HEAD", 8); err != nil {
		t.Fatal(err)
	}
	if markerFired(t, marker) {
		t.Error("FileCochanges executed the repository's gpg.program")
	}
}

// signCommitHeader rewrites HEAD as a byte-identical commit carrying a bogus
// `gpgsig` header, which is all git needs to attempt signature verification.
// No real key is involved: the point is reaching gpg.program at all.
func signCommitHeader(t *testing.T, repo string) {
	t.Helper()
	raw := gitStdout(t, repo, "cat-file", "commit", "HEAD")
	headerEnd := bytes.Index(raw, []byte("\n\n"))
	if headerEnd < 0 {
		t.Fatalf("commit object has no header/message separator: %q", raw)
	}
	const gpgsig = "gpgsig -----BEGIN PGP SIGNATURE-----\n \n ZmFrZQ==\n -----END PGP SIGNATURE-----\n"
	signed := make([]byte, 0, len(raw)+len(gpgsig))
	signed = append(signed, raw[:headerEnd+1]...)
	signed = append(signed, gpgsig...)
	signed = append(signed, raw[headerEnd+1:]...)

	write := exec.Command("git", "hash-object", "-w", "-t", "commit", "--stdin")
	write.Dir = repo
	write.Stdin = bytes.NewReader(signed)
	out, err := write.Output()
	if err != nil {
		t.Fatalf("git hash-object: %v", err)
	}
	newCommit := string(bytes.TrimSpace(out))
	branch := string(bytes.TrimSpace(gitStdout(t, repo, "symbolic-ref", "HEAD")))
	git(t, repo, "update-ref", branch, newCommit)
}

func gitStdout(t *testing.T, repo string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}
