package gitutil

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestGitSubprocessesIgnoreInheritedGlobalAndSystemConfig(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")

	for _, variable := range []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"} {
		t.Run(variable, func(t *testing.T) {
			malformed := filepath.Join(t.TempDir(), "malformed-config")
			if err := os.WriteFile(malformed, []byte("[unterminated\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv(variable, malformed)
			if _, err := ListIndexFiles(t.Context(), repo); err != nil {
				t.Fatalf("provider Git command read inherited %s: %v", variable, err)
			}
		})
	}
}

func TestGitSubprocessesNeutralizeConfiguredExternalPolicyFiles(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "keep.go"), []byte("package keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "keep.go")
	if err := os.WriteFile(filepath.Join(repo, "hidden.secret"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	policyDir := t.TempDir()
	excludes := filepath.Join(policyDir, "excludes")
	attributes := filepath.Join(policyDir, "attributes")
	if err := os.WriteFile(excludes, []byte("*.secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attributes, []byte("*.go -diff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "config", "core.excludesFile", excludes)
	git(t, repo, "config", "core.attributesFile", attributes)

	// Controls prove this Git build consults both configured files for the exact
	// operations under test. Without the command-scope overrides the secret is
	// omitted and -I classifies keep.go as binary through the external attribute.
	controlList := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	controlList.Dir = repo
	listed, err := controlList.Output()
	if err != nil {
		t.Fatalf("control git ls-files: %v", err)
	}
	if bytes.Contains(listed, []byte("hidden.secret\x00")) {
		t.Fatal("control Git did not apply core.excludesFile")
	}
	controlGrep := exec.Command("git", "grep", "-z", "-I", "-F", "-l", "-e", "package", "--")
	controlGrep.Dir = repo
	grepOutput, grepErr := controlGrep.CombinedOutput()
	if exit, ok := grepErr.(*exec.ExitError); !ok || exit.ExitCode() != 1 || len(grepOutput) != 0 {
		t.Fatalf("control Git did not apply core.attributesFile: output=%q err=%v", grepOutput, grepErr)
	}

	files, err := ListWorktreeFiles(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(files, "hidden.secret") {
		t.Fatalf("provider Git honored external core.excludesFile: %#v", files)
	}
	paths, err := GrepFixedStringPaths(t.Context(), repo, "", "package")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(paths, "keep.go") {
		t.Fatalf("provider Git honored external core.attributesFile: %#v", paths)
	}
}

func TestGitGrepsPinMachineOutputAgainstRepositoryConfiguration(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	nested := filepath.Join(repo, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "target.go"), []byte("package target\nvar Needle = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "nested/target.go")

	for key, value := range map[string]string{
		"grep.lineNumber": "true",
		"grep.column":     "true",
		"grep.fullName":   "true",
		"color.grep":      "always",
		"color.ui":        "always",
	} {
		git(t, repo, "config", key, value)
	}

	paths, err := GrepFixedStringPaths(t.Context(), nested, "", "Needle")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(paths, []string{"target.go"}) {
		t.Fatalf("configured grep paths = %#v, want config-independent relative path", paths)
	}

	matches, err := GrepIndexMatches(t.Context(), nested, []string{"Needle"}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(matches, []GrepMatch{{Path: "target.go", Text: "Needle"}}) {
		t.Fatalf("configured grep matches = %#v, want path and text without line/column/color metadata", matches)
	}
}

func TestGitGrepsDoNotRecurseIntoConfiguredSubmodule(t *testing.T) {
	dependency := t.TempDir()
	git(t, dependency, "init")
	git(t, dependency, "config", "user.name", "Entire Graph Test")
	git(t, dependency, "config", "user.email", "graph@example.com")
	if err := os.WriteFile(filepath.Join(dependency, "dep.go"), []byte("package dep\nvar SubmoduleOnly = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dependency, "add", ".")
	git(t, dependency, "commit", "-m", "dependency")

	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	add := exec.Command("git", "-c", "protocol.file.allow=always", "submodule", "add", dependency, "dep")
	add.Dir = repo
	if output, err := add.CombinedOutput(); err != nil {
		t.Fatalf("add fixture submodule: %v\n%s", err, output)
	}
	git(t, repo, "commit", "-m", "add submodule")
	git(t, repo, "config", "submodule.recurse", "true")

	control := exec.Command("git", "grep", "-z", "-I", "-F", "-l", "-e", "SubmoduleOnly", "--")
	control.Dir = repo
	output, err := control.Output()
	if err != nil || !bytes.Contains(output, []byte("dep/dep.go\x00")) {
		t.Fatalf("control Git did not recurse into the configured submodule: output=%q err=%v", output, err)
	}

	paths, err := GrepFixedStringPaths(t.Context(), repo, "", "SubmoduleOnly")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("provider grep recursed into submodule: %#v", paths)
	}
	matches, err := GrepIndexMatches(t.Context(), repo, []string{"SubmoduleOnly"}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("provider grep match scan recursed into submodule: %#v", matches)
	}
}

func TestGitHistoryCommandsNeutralizeConfiguredOrderFile(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	if err := os.WriteFile(filepath.Join(repo, "one.go"), []byte("package one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "first")
	if err := os.WriteFile(filepath.Join(repo, "two.go"), []byte("package two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "second")

	missing := filepath.Join(t.TempDir(), "missing-order-file")
	git(t, repo, "config", "diff.orderFile", missing)
	control := exec.Command("git", "log", "-z", "--name-only", "-n", "2", "HEAD", "--")
	control.Dir = repo
	if output, err := control.CombinedOutput(); err == nil || !bytes.Contains(output, []byte("failed to read orderfile")) {
		t.Fatalf("control Git did not read diff.orderFile: output=%q err=%v", output, err)
	}

	if _, err := FileCochanges(t.Context(), repo, "HEAD", 2); err != nil {
		t.Fatalf("provider history command honored diff.orderFile: %v", err)
	}
}
