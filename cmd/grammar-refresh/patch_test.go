package main

import (
	"os"
	"path/filepath"
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
