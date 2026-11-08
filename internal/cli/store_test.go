package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreDescriptorUsesCurrentTreeAndRequiresBuild(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_SEARCH_STORE", "1")
	repo := t.TempDir()
	cache := filepath.Join(t.TempDir(), "cache-\u009b")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	git("init", "-q")
	git("config", "user.email", "fixture@example.test")
	git("config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package sample\nfunc Render() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "main.go")
	git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture")
	var out bytes.Buffer
	opts := Options{Stdout: &out, Stderr: &out, Version: "store-contract-test"}
	args := []string{"--repo", repo, "--cache-dir", cache}
	if err := runStore(context.Background(), opts, args); err != nil {
		t.Fatal(err)
	}
	var missing struct {
		Unavailable bool `json:"store_unavailable"`
	}
	if err := json.Unmarshal(out.Bytes(), &missing); err != nil || !missing.Unavailable {
		t.Fatalf("missing=%s err=%v", out.String(), err)
	}
	out.Reset()
	if err := runIndex(context.Background(), opts, append(args, "--format", "json")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runStore(context.Background(), opts, args); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(out.String(), '\u009b') {
		t.Fatalf("descriptor emitted a raw C1 control: %q", out.String())
	}
	var d struct {
		Version int    `json:"contract_version"`
		Path    string `json:"path"`
		Commit  string `json:"commit"`
		Symbols int    `json:"symbols"`
	}
	if err := json.Unmarshal(out.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Version != 1 || d.Commit == "" || d.Symbols == 0 || !filepath.IsAbs(d.Path) {
		t.Fatalf("descriptor=%s", out.String())
	}
	if !strings.ContainsRune(d.Path, '\u009b') {
		t.Fatalf("JSON escaping changed the decoded cache path: %q", d.Path)
	}
	if _, err := os.Stat(d.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package sample\nfunc Other() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "main.go")
	git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "new tree")
	out.Reset()
	if err := runStore(context.Background(), opts, args); err != nil {
		t.Fatal(err)
	}
	missing.Unavailable = false
	if err := json.Unmarshal(out.Bytes(), &missing); err != nil || !missing.Unavailable {
		t.Fatalf("served prior tree: %s (%v)", out.String(), err)
	}
}
