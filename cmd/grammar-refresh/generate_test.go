package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeNPX puts an npx stand-in first on PATH. It logs each invocation's
// arguments, answers --version with "tree-sitter <version>", and for
// generate writes src/parser.c at the given ABI (or exits 1 when abi is
// "fail").
func fakeNPX(t *testing.T, version, abi string) (logPath string) {
	t.Helper()
	bin := t.TempDir()
	logPath = filepath.Join(bin, "npx.log")
	script := `#!/bin/sh
echo "$*" >> "` + logPath + `"
while [ "$1" = "--yes" ]; do shift; done
shift
case "$1" in
--version) echo "tree-sitter ` + version + `" ;;
generate)
	[ "` + abi + `" = fail ] && exit 1
	mkdir -p src
	printf '#define LANGUAGE_VERSION ` + abi + `\n// generated\n' > src/parser.c ;;
esac
`
	writeTestFile(t, filepath.Join(bin, "npx"), script)
	if err := os.Chmod(filepath.Join(bin, "npx"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func npxLog(t *testing.T, logPath string) []string {
	t.Helper()
	data, _ := os.ReadFile(logPath)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func buildManifest(t *testing.T, repo, revision string, build *LocalBuild) (manifest, root string) {
	t.Helper()
	root = t.TempDir()
	manifest = filepath.Join(root, "grammars.json")
	if err := saveManifest(manifest, []Grammar{{
		Language:   "demo",
		URL:        repo,
		Reference:  "v0",
		Revision:   revision,
		Files:      []string{"LICENSE", "parser.c"},
		ABI:        14,
		LocalBuild: build,
	}}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "demo", "parser.c"), "old\n")
	return manifest, root
}

func TestCLIVersion(t *testing.T) {
	pinned := Grammar{Revision: "abc", LocalBuild: &LocalBuild{TreeSitterCLI: "0.24.7"}}
	for _, tc := range []struct {
		name     string
		existing Grammar
		revision string
		o        options
		want     string
	}{
		{"override wins", pinned, "abc", options{TreeSitterCLI: "0.25.0"}, "0.25.0"},
		{"same revision pins", pinned, "abc", options{}, "0.24.7"},
		{"new revision uses latest", pinned, "def", options{}, "latest"},
		{"no local build uses latest", Grammar{Revision: "abc"}, "abc", options{Generate: true}, "latest"},
	} {
		if got := cliVersion(tc.existing, tc.revision, tc.o); got != tc.want {
			t.Errorf("%s: cliVersion = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRefreshRegeneratesPinned(t *testing.T) {
	log := fakeNPX(t, "0.24.7", "14")
	repo, revision := generatedGrammarRepo(t)
	manifest, root := buildManifest(t, repo, revision, &LocalBuild{TreeSitterCLI: "0.24.7"})
	if err := refresh(options{Lang: "demo", Ref: "HEAD", ManifestPath: manifest, GrammarsDir: root}); err != nil {
		t.Fatal(err)
	}
	if got := npxLog(t, log); !reflect.DeepEqual(got, []string{
		"--yes tree-sitter-cli@0.24.7 --version",
		"--yes tree-sitter-cli@0.24.7 generate --abi 14",
	}) {
		t.Fatalf("npx calls = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "demo", "parser.c")); !strings.Contains(string(got), "// generated") {
		t.Fatalf("vendored parser.c was not regenerated: %q", got)
	}
	grammars, _ := loadManifest(manifest)
	if lb := grammars[0].LocalBuild; lb == nil || lb.TreeSitterCLI != "0.24.7" {
		t.Fatalf("local_build = %+v", lb)
	}
}

func TestRefreshGenerateRecordsLatest(t *testing.T) {
	log := fakeNPX(t, "0.27.1", "14")
	repo, _ := generatedGrammarRepo(t)
	root := t.TempDir()
	manifest := filepath.Join(root, "grammars.json")
	if err := refresh(options{Lang: "demo", Ref: "HEAD", URL: repo, ManifestPath: manifest, GrammarsDir: root, Generate: true}); err != nil {
		t.Fatal(err)
	}
	if got := npxLog(t, log); !reflect.DeepEqual(got, []string{
		"--yes tree-sitter-cli@latest --version",
		"--yes tree-sitter-cli@0.27.1 generate --abi 14",
	}) {
		t.Fatalf("npx calls = %q", got)
	}
	grammars, _ := loadManifest(manifest)
	want := &LocalBuild{TreeSitterCLI: "0.27.1", GenerateArgs: []string{"--abi", "14"}}
	if got := grammars[0].LocalBuild; !reflect.DeepEqual(got, want) {
		t.Fatalf("local_build = %+v, want %+v", got, want)
	}
}

func TestRefreshGenerateWithoutNPX(t *testing.T) {
	repo, _ := generatedGrammarRepo(t)
	root := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	err := refresh(options{Lang: "demo", Ref: "HEAD", URL: repo, ManifestPath: filepath.Join(root, "grammars.json"), GrammarsDir: root, Generate: true})
	if err == nil || !strings.Contains(err.Error(), "Node.js") {
		t.Fatalf("err = %v; want one naming Node.js", err)
	}
}

func TestRefreshGenerateFailureLeavesVendorUntouched(t *testing.T) {
	fakeNPX(t, "0.24.7", "fail")
	repo, revision := generatedGrammarRepo(t)
	manifest, root := buildManifest(t, repo, revision, &LocalBuild{TreeSitterCLI: "0.24.7"})
	before, _ := os.ReadFile(manifest)
	if err := refresh(options{Lang: "demo", Ref: "HEAD", ManifestPath: manifest, GrammarsDir: root}); err == nil {
		t.Fatal("want an error when generate fails")
	}
	if got, _ := os.ReadFile(filepath.Join(root, "demo", "parser.c")); string(got) != "old\n" {
		t.Fatalf("parser.c changed after a failed generate: %q", got)
	}
	if after, _ := os.ReadFile(manifest); string(after) != string(before) {
		t.Fatalf("manifest changed after a failed generate:\n%s", after)
	}
}

func TestRefreshGeneratedABI15SuggestsOlderCLI(t *testing.T) {
	fakeNPX(t, "0.30.0", "15")
	repo, _ := generatedGrammarRepo(t)
	root := t.TempDir()
	err := refresh(options{Lang: "demo", Ref: "HEAD", URL: repo, ManifestPath: filepath.Join(root, "grammars.json"), GrammarsDir: root, Generate: true})
	if err == nil || !strings.Contains(err.Error(), "-tree-sitter-cli") {
		t.Fatalf("err = %v; want a hint to pass -tree-sitter-cli", err)
	}
}

func TestResolveCLIRejectsUnparseableVersion(t *testing.T) {
	fakeNPX(t, "nonsense", "14")
	if v, err := resolveCLI(t.TempDir(), "latest"); err == nil {
		t.Fatalf("resolveCLI = %q; want an error", v)
	}
}
