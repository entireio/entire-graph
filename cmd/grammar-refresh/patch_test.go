package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const addPatchedLine = `diff --git a/src/parser.c b/src/parser.c
--- a/src/parser.c
+++ b/src/parser.c
@@ -1 +1,2 @@
 #define LANGUAGE_VERSION 14
+// patched
`

func patchManifest(t *testing.T, repo, subdir, patchPath, patch string) (manifest, root string) {
	t.Helper()
	root = t.TempDir()
	manifest = filepath.Join(root, "grammars.json")
	if err := saveManifest(manifest, []Grammar{{
		Language:   "demo",
		URL:        repo,
		Reference:  "v0",
		Revision:   "0000000",
		Path:       subdir,
		Files:      []string{"LICENSE", "parser.c"},
		ABI:        14,
		LocalBuild: &LocalBuild{Patch: patchPath},
	}}); err != nil {
		t.Fatal(err)
	}
	if filepath.IsLocal(patchPath) {
		writeTestFile(t, filepath.Join(root, patchPath), patch)
	}
	writeTestFile(t, filepath.Join(root, "demo", "parser.c"), "old\n")
	return manifest, root
}

func TestRefreshAppliesPatch(t *testing.T) {
	repo, _ := generatedGrammarRepo(t)
	manifest, root := patchManifest(t, repo, "", "demo/fix.patch", addPatchedLine)
	if err := refresh(options{Lang: "demo", Ref: "HEAD", ManifestPath: manifest, GrammarsDir: root}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "demo", "parser.c")); !strings.Contains(string(got), "// patched") {
		t.Fatalf("vendored parser.c lacks the patch: %q", got)
	}
	grammars, err := loadManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if lb := grammars[0].LocalBuild; lb == nil || lb.Patch != "demo/fix.patch" {
		t.Fatalf("local_build not kept: %+v", grammars[0])
	}
}

func TestRefreshAppliesPatchInSubdir(t *testing.T) {
	repo := gitRepo(t, map[string]string{"sub/src/parser.c": "#define LANGUAGE_VERSION 14\n", "LICENSE": "MIT\n"})
	manifest, root := patchManifest(t, repo, "sub", "demo/fix.patch", addPatchedLine)
	if err := refresh(options{Lang: "demo", Ref: "HEAD", ManifestPath: manifest, GrammarsDir: root}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "demo", "parser.c")); !strings.Contains(string(got), "// patched") {
		t.Fatalf("vendored parser.c lacks the patch: %q", got)
	}
}

func TestRefreshPatchFailureLeavesVendorUntouched(t *testing.T) {
	repo, _ := generatedGrammarRepo(t)
	mismatch := strings.Replace(addPatchedLine, "VERSION 14", "VERSION 13", 1)
	manifest, root := patchManifest(t, repo, "", "demo/fix.patch", mismatch)
	before, _ := os.ReadFile(manifest)
	err := refresh(options{Lang: "demo", Ref: "HEAD", ManifestPath: manifest, GrammarsDir: root})
	if err == nil || !strings.Contains(err.Error(), "fix.patch") {
		t.Fatalf("err = %v; want a failure naming fix.patch", err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "demo", "parser.c")); string(got) != "old\n" {
		t.Fatalf("parser.c changed after a failed patch: %q", got)
	}
	if after, _ := os.ReadFile(manifest); string(after) != string(before) {
		t.Fatalf("manifest changed after a failed patch:\n%s", after)
	}
}

func TestRefreshRejectsUnsafePatchPath(t *testing.T) {
	for _, patch := range []string{"../x.patch", "/tmp/x.patch"} {
		manifest, root := patchManifest(t, filepath.Join(t.TempDir(), "no-such-repo"), "", patch, "")
		err := refresh(options{Lang: "demo", Ref: "HEAD", ManifestPath: manifest, GrammarsDir: root})
		if err == nil || !strings.Contains(err.Error(), "patch") || strings.Contains(err.Error(), "fetch") {
			t.Errorf("patch %q: err = %v; want a patch validation error before any fetch", patch, err)
		}
	}
}

// patchGrammarJS edits the grammar, not a vendored file, so it changes nothing
// about the vendored folder unless the parser is regenerated from it.
const patchGrammarJS = `diff --git a/grammar.js b/grammar.js
--- a/grammar.js
+++ b/grammar.js
@@ -1 +1,2 @@
 module.exports = grammar({});
+// patched
`

func grammarJSRepo(t *testing.T) string {
	t.Helper()
	return gitRepo(t, map[string]string{
		"grammar.js":   "module.exports = grammar({});\n",
		"src/parser.c": "#define LANGUAGE_VERSION 14\n",
		"LICENSE":      "MIT\n",
	})
}

// #357 vendored a Swift parser built from a hand-applied grammar.js patch, and
// nothing in the tree tied the two together. Applying that patch and then
// copying upstream's committed parser.c is the same silent loss, reported as a
// success, so it is refused instead.
func TestRefreshRefusesAPatchThatNeedsGeneration(t *testing.T) {
	manifest, root := patchManifest(t, grammarJSRepo(t), "", "demo/fix.patch", patchGrammarJS)
	before, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	err = refresh(options{Lang: "demo", Ref: "HEAD", ManifestPath: manifest, GrammarsDir: root})
	if err == nil {
		t.Fatal("want a refusal: the patch edits grammar.js and nothing regenerates the parser")
	}
	for _, want := range []string{"grammar.js", "tree_sitter_cli", "-generate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v; want it to name %q", err, want)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(root, "demo", "parser.c")); string(got) != "old\n" {
		t.Errorf("parser.c changed after the refusal: %q", got)
	}
	if after, _ := os.ReadFile(manifest); string(after) != string(before) {
		t.Errorf("manifest changed after the refusal:\n%s", after)
	}
}

// The other direction: the same patch with a generator recorded is the
// supported arrangement, and must not be refused.
func TestRefreshAcceptsAGrammarPatchThatRegenerates(t *testing.T) {
	calls := fakeNPX(t, "0.24.7", "14")
	root := t.TempDir()
	manifest := filepath.Join(root, "grammars.json")
	if err := saveManifest(manifest, []Grammar{{
		Language:  "demo",
		URL:       grammarJSRepo(t),
		Reference: "v0",
		Revision:  "0000000",
		Files:     []string{"LICENSE", "parser.c"},
		ABI:       14,
		LocalBuild: &LocalBuild{
			Patch:         "demo/fix.patch",
			TreeSitterCLI: "0.24.7",
		},
	}}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "demo", "fix.patch"), patchGrammarJS)
	writeTestFile(t, filepath.Join(root, "demo", "parser.c"), "old\n")
	if err := refresh(options{Lang: "demo", Ref: "HEAD", ManifestPath: manifest, GrammarsDir: root}); err != nil {
		t.Fatalf("a patched grammar with a recorded generator was refused: %v", err)
	}
	if len(*calls) == 0 {
		t.Fatal("the parser was never regenerated")
	}
	if got, _ := os.ReadFile(filepath.Join(root, "demo", "parser.c")); !strings.Contains(string(got), "// generated") {
		t.Errorf("vendored parser.c is not the generated one: %q", got)
	}
}

// A patch confined to vendored files is a complete change on its own.
func TestRefreshAcceptsAVendoredFilePatchWithoutGeneration(t *testing.T) {
	repo, _ := generatedGrammarRepo(t)
	manifest, root := patchManifest(t, repo, "", "demo/fix.patch", addPatchedLine)
	if err := refresh(options{Lang: "demo", Ref: "HEAD", ManifestPath: manifest, GrammarsDir: root}); err != nil {
		t.Fatalf("a patch touching only src/parser.c was refused: %v", err)
	}
}

func TestPatchPathsNeedingGeneration(t *testing.T) {
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		patch string
		want  []string
	}{
		"grammar":              {patchGrammarJS, []string{"grammar.js"}},
		"parser":               {addPatchedLine, nil},
		"scanner":              {strings.ReplaceAll(addPatchedLine, "src/parser.c", "src/scanner.c"), nil},
		"header":               {strings.ReplaceAll(addPatchedLine, "src/parser.c", "src/tree_sitter/parser.h"), nil},
		"license":              {strings.ReplaceAll(addPatchedLine, "src/parser.c", "LICENSE"), nil},
		"queries":              {strings.ReplaceAll(addPatchedLine, "src/parser.c", "queries/highlights.scm"), []string{"queries/highlights.scm"}},
		"new grammar file":     {strings.Replace(patchGrammarJS, "--- a/grammar.js", "--- /dev/null", 1), []string{"grammar.js"}},
		"deleted grammar file": {strings.Replace(patchGrammarJS, "+++ b/grammar.js", "+++ /dev/null", 1), []string{"grammar.js"}},
	} {
		path := filepath.Join(dir, name+".patch")
		writeTestFile(t, path, tc.patch)
		got, err := patchPathsNeedingGeneration(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: patchPathsNeedingGeneration = %q, want %q", name, got, tc.want)
		}
	}
}
