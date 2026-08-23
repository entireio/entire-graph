//go:build windows

package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const cliFakeGitMarkerEnv = "ENTIRE_GRAPH_CLI_TEST_FAKE_GIT_MARKER"

func TestMain(m *testing.M) {
	if marker := os.Getenv(cliFakeGitMarkerEnv); marker != "" {
		_ = os.WriteFile(marker, []byte("started"), 0o600)
		os.Exit(23)
	}
	os.Exit(m.Run())
}

func installCLIFakeGitMarker(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	marker := filepath.Join(binDir, "git-was-started")
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(testExecutable)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := os.OpenFile(filepath.Join(binDir, "git.exe"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		_ = source.Close()
		t.Fatalf("create fake git executable: %v", err)
	}
	_, copyErr := io.Copy(destination, source)
	closeErr := errors.Join(destination.Close(), source.Close())
	if copyErr != nil || closeErr != nil {
		t.Fatalf("install fake git executable: %v", errors.Join(copyErr, closeErr))
	}
	t.Setenv("PATH", binDir)
	t.Setenv(cliFakeGitMarkerEnv, marker)
	return marker
}

func TestCommittedRecordCacheRejectsUnsafeMetadataBeforeRevParse(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, ".git", `gitdir: \\203.0.113.1\share\repo`+"\n")
	write(t, repo, "main.go", "package main\n")
	marker := installCLIFakeGitMarker(t)
	pluginDataDir := t.TempDir()
	cacheDir := t.TempDir()
	var output bytes.Buffer

	done := make(chan error, 1)
	go func() {
		done <- Run(t.Context(), Options{
			Version: "test-version",
			Env:     EntireEnv{PluginDataDir: pluginDataDir},
			Stdout:  &output,
			Stderr:  &output,
		}, []string{"snapshot", "--repo", repo, "--cache-dir", cacheDir})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("warned filesystem fallback: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unsafe metadata did not fail before cache revision probes promptly")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Git subprocess marker stat = %v, want not-exist", err)
	}
	if !bytes.Contains(output.Bytes(), []byte("W_GIT_WORKTREE_FALLBACK")) {
		t.Fatalf("output = %q, want warned filesystem fallback", output.String())
	}
}

func TestImplicitRepoPreservesUnsafeMetadataFilesystemFallback(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, ".git", `gitdir: \\203.0.113.1\share\repo`+"\n")
	write(t, repo, "main.go", "package main\n")
	marker := installCLIFakeGitMarker(t)
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousDir) })

	var output bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- Run(t.Context(), Options{
			Version: "test-version",
			Env:     EntireEnv{PluginDataDir: t.TempDir()},
			Stdout:  &output,
			Stderr:  &output,
		}, []string{"snapshot", "--cache-dir", t.TempDir()})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("implicit-repo warned filesystem fallback: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("implicit repository discovery did not avoid Git promptly")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Git subprocess marker stat = %v, want not-exist", err)
	}
	if !bytes.Contains(output.Bytes(), []byte("W_GIT_WORKTREE_FALLBACK")) {
		t.Fatalf("output = %q, want warned filesystem fallback", output.String())
	}
}
