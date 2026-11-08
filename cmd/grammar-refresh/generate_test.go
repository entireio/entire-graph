package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fakeNPX replaces the npx runner for one test, so no test needs Node or a
// shell. It records each call's arguments, answers --version with
// "tree-sitter <version>", and for generate writes src/parser.c at the given
// ABI (or fails when abi is "fail").
func fakeNPX(t *testing.T, version, abi string) *[]string {
	t.Helper()
	calls := &[]string{}
	run, find := npx, findNPX
	t.Cleanup(func() { npx, findNPX = run, find })
	findNPX = func() error { return nil }
	npx = func(dir string, args ...string) (string, error) {
		*calls = append(*calls, strings.Join(args, " "))
		switch args[1] {
		case "--version":
			return "tree-sitter " + version, nil
		case "generate":
			if abi == "fail" {
				return "", errors.New("exit status 1")
			}
			writeTestFile(t, filepath.Join(dir, "src", "parser.c"), "#define LANGUAGE_VERSION "+abi+"\n// generated\n")
		}
		return "", nil
	}
	return calls
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
	calls := fakeNPX(t, "0.24.7", "14")
	repo, revision := generatedGrammarRepo(t)
	manifest, root := buildManifest(t, repo, revision, &LocalBuild{TreeSitterCLI: "0.24.7"})
	if err := refresh(options{Lang: "demo", Ref: "HEAD", ManifestPath: manifest, GrammarsDir: root}); err != nil {
		t.Fatal(err)
	}
	if got := *calls; !reflect.DeepEqual(got, []string{
		"tree-sitter-cli@0.24.7 --version",
		"tree-sitter-cli@0.24.7 generate --abi 14",
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
	calls := fakeNPX(t, "0.27.1", "14")
	repo, _ := generatedGrammarRepo(t)
	root := t.TempDir()
	manifest := filepath.Join(root, "grammars.json")
	if err := refresh(options{Lang: "demo", Ref: "HEAD", URL: repo, ManifestPath: manifest, GrammarsDir: root, Generate: true}); err != nil {
		t.Fatal(err)
	}
	if got := *calls; !reflect.DeepEqual(got, []string{
		"tree-sitter-cli@latest --version",
		"tree-sitter-cli@0.27.1 generate --abi 14",
	}) {
		t.Fatalf("npx calls = %q", got)
	}
	grammars, _ := loadManifest(manifest)
	// GenerateArgs stays empty: see TestRefreshDoesNotPinTheDefaultABI.
	want := &LocalBuild{TreeSitterCLI: "0.27.1"}
	if got := grammars[0].LocalBuild; !reflect.DeepEqual(got, want) {
		t.Fatalf("local_build = %+v, want %+v", got, want)
	}
}

// TestRefreshDoesNotPinTheDefaultABI holds the one-constant promise. generate
// runs with --abi maxABI and the manifest records nothing, so the next runtime
// bump edits maxABI and every grammar follows. Writing the resolved default
// back would make it an explicit pin, and each grammar would go on generating
// the old ABI -- which vendor keeps accepting for as long as it is inside the
// range, so no test and no run would say a word.
func TestRefreshDoesNotPinTheDefaultABI(t *testing.T) {
	calls := fakeNPX(t, "0.27.1", "14")
	repo, _ := generatedGrammarRepo(t)
	root := t.TempDir()
	manifest := filepath.Join(root, "grammars.json")
	if err := refresh(options{Lang: "demo", Ref: "HEAD", URL: repo, ManifestPath: manifest, GrammarsDir: root, Generate: true}); err != nil {
		t.Fatal(err)
	}
	wantCall := "tree-sitter-cli@0.27.1 generate --abi " + strconv.Itoa(maxABI)
	if !slices.Contains(*calls, wantCall) {
		t.Errorf("npx calls = %q, want one of them to be %q", *calls, wantCall)
	}
	grammars, err := loadManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if args := grammars[0].LocalBuild.GenerateArgs; args != nil {
		t.Errorf("manifest recorded generate_args %q; the default must stay derived from maxABI", args)
	}
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "generate_args") {
		t.Errorf("manifest still mentions generate_args:\n%s", raw)
	}
}

// Explicit args are the maintainer's, so they are used and kept verbatim.
func TestRefreshKeepsExplicitGenerateArgs(t *testing.T) {
	calls := fakeNPX(t, "0.24.7", "14")
	repo, revision := generatedGrammarRepo(t)
	manifest, root := buildManifest(t, repo, revision, &LocalBuild{
		TreeSitterCLI: "0.24.7",
		GenerateArgs:  []string{"--abi", "13"},
	})
	if err := refresh(options{Lang: "demo", Ref: "HEAD", ManifestPath: manifest, GrammarsDir: root}); err != nil {
		t.Fatal(err)
	}
	if want := "tree-sitter-cli@0.24.7 generate --abi 13"; !slices.Contains(*calls, want) {
		t.Errorf("npx calls = %q, want one of them to be %q", *calls, want)
	}
	grammars, err := loadManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := grammars[0].LocalBuild.GenerateArgs, []string{"--abi", "13"}; !reflect.DeepEqual(got, want) {
		t.Errorf("generate_args = %q, want %q", got, want)
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

func TestRefreshGenerateFailureSuggestsOlderCLI(t *testing.T) {
	fakeNPX(t, "0.30.0", "fail")
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

func TestResolveCLIKeepsAPrereleaseVersion(t *testing.T) {
	const printed = "0.25.0-rc.1"
	fakeNPX(t, printed, "14")
	got, err := resolveCLI(t.TempDir(), "latest")
	if err != nil {
		t.Fatal(err)
	}
	if got != printed {
		t.Errorf("resolveCLI = %q, want %q; recording the release version pins a build that never generated these bytes", got, printed)
	}
}

func TestCLIVersionPatternTakesTheWholeVersion(t *testing.T) {
	for printed, want := range map[string]string{
		"tree-sitter 0.24.7":        "0.24.7",
		"tree-sitter 0.25.0-rc.1":   "0.25.0-rc.1",
		"tree-sitter 0.25.0-rc.1.":  "0.25.0-rc.1",
		"tree-sitter 1.0.0+build.5": "1.0.0+build.5",
		"tree-sitter nonsense":      "",
	} {
		if got := cliVersionPattern.FindString(printed); got != want {
			t.Errorf("%q -> %q, want %q", printed, got, want)
		}
	}
}

// TestRecordedLocalBuildLeavesTheDefaultDerived is the pure form of the
// one-constant promise: nothing writes --abi maxABI into the manifest, so a
// runtime bump that edits maxABI changes what every grammar generates.
func TestRecordedLocalBuildLeavesTheDefaultDerived(t *testing.T) {
	for name, tc := range map[string]struct {
		in   *LocalBuild
		want *LocalBuild
	}{
		"new grammar": {
			nil,
			&LocalBuild{TreeSitterCLI: "0.27.1"},
		},
		"version refreshed": {
			&LocalBuild{TreeSitterCLI: "0.24.7"},
			&LocalBuild{TreeSitterCLI: "0.27.1"},
		},
		"patch kept": {
			&LocalBuild{Patch: "swift/compatibility.patch", TreeSitterCLI: "0.24.7"},
			&LocalBuild{Patch: "swift/compatibility.patch", TreeSitterCLI: "0.27.1"},
		},
		"explicit args kept": {
			&LocalBuild{TreeSitterCLI: "0.24.7", GenerateArgs: []string{"--abi", "13"}},
			&LocalBuild{TreeSitterCLI: "0.27.1", GenerateArgs: []string{"--abi", "13"}},
		},
	} {
		got := recordedLocalBuild(tc.in, "0.27.1")
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: recordedLocalBuild = %+v, want %+v", name, got, tc.want)
		}
		if tc.in != nil && len(tc.in.GenerateArgs) == 0 && got.GenerateArgs != nil {
			t.Errorf("%s: the derived default was recorded as a pin: %q", name, got.GenerateArgs)
		}
	}
	// The default itself must still reach the generator.
	if args := generateArgs(nil); !reflect.DeepEqual(args, []string{"--abi", strconv.Itoa(maxABI)}) {
		t.Errorf("generateArgs(nil) = %q, want --abi %d", args, maxABI)
	}
}
