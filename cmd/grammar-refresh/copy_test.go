package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The shape of the F# grammar: the scanner includes a header that upstream
// keeps outside the grammar directory, so vendor never sees it.
const repointInclude = `--- a/src/scanner.c
+++ b/src/scanner.c
@@ -1,2 +1,2 @@
-#include "../../common/scanner.h"
+#include "scanner.h"
 // scanner
`

func sharedHeaderRepo(t *testing.T, header string) string {
	t.Helper()
	files := map[string]string{
		"sub/src/parser.c":  "#define LANGUAGE_VERSION 14\n",
		"sub/src/scanner.c": "#include \"../../common/scanner.h\"\n// scanner\n",
		"LICENSE":           "MIT\n",
	}
	if header != "" {
		files["common/scanner.h"] = header
	}
	return gitRepo(t, files)
}

func copyManifest(t *testing.T, repo string, build *LocalBuild) (manifest, root string) {
	t.Helper()
	root = t.TempDir()
	manifest = filepath.Join(root, "grammars.json")
	if err := saveManifest(manifest, []Grammar{{
		Language:   "demo",
		URL:        repo,
		Reference:  "v0",
		Revision:   "0000000",
		Path:       "sub",
		Files:      []string{"LICENSE", "parser.c", "scanner.c", "scanner.h"},
		ABI:        14,
		LocalBuild: build,
	}}); err != nil {
		t.Fatal(err)
	}
	if build.Patch != "" {
		writeTestFile(t, filepath.Join(root, build.Patch), repointInclude)
	}
	writeTestFile(t, filepath.Join(root, "demo", "parser.c"), "old\n")
	writeTestFile(t, filepath.Join(root, "demo", "scanner.h"), "// header at the old revision\n")
	return manifest, root
}

func sharedHeaderBuild() *LocalBuild {
	return &LocalBuild{
		Copy:  []FileCopy{{From: "common/scanner.h", To: "src/scanner.h"}},
		Patch: "demo/include.patch",
	}
}

// A patch that embedded the header would still apply to a newer revision and
// pair its parser with the old header. The copy is read from the checkout, so
// the header always belongs to the revision being vendored.
func TestRefreshCopiesTheHeaderOfTheRevisionBeingVendored(t *testing.T) {
	repo := sharedHeaderRepo(t, "// header at the new revision\n")
	manifest, root := copyManifest(t, repo, sharedHeaderBuild())
	if err := refresh(options{Lang: "demo", Ref: "HEAD", ManifestPath: manifest, GrammarsDir: root}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "demo", "scanner.h")); string(got) != "// header at the new revision\n" {
		t.Errorf("vendored scanner.h = %q; want the new revision's header", got)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "demo", "scanner.c")); !strings.HasPrefix(string(got), "#include \"scanner.h\"\n") {
		t.Errorf("vendored scanner.c does not include the copied header: %q", got)
	}
	grammars, err := loadManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	got := grammars[0]
	if !slices.Contains(got.Files, "scanner.h") {
		t.Errorf("files = %v; want scanner.h recorded", got.Files)
	}
	if lb := got.LocalBuild; lb == nil || len(lb.Copy) != 1 || lb.Copy[0] != (FileCopy{From: "common/scanner.h", To: "src/scanner.h"}) || lb.Patch != "demo/include.patch" {
		t.Errorf("local_build not kept: %+v", got.LocalBuild)
	}
}

func assertCopyRefused(t *testing.T, repo string, build *LocalBuild, wants ...string) {
	t.Helper()
	manifest, root := copyManifest(t, repo, build)
	before, _ := os.ReadFile(manifest)
	err := refresh(options{Lang: "demo", Ref: "HEAD", ManifestPath: manifest, GrammarsDir: root})
	if err == nil {
		t.Fatal("want a refusal")
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v; want it to name %q", err, want)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(root, "demo", "scanner.h")); string(got) != "// header at the old revision\n" {
		t.Errorf("scanner.h changed after the refusal: %q", got)
	}
	if after, _ := os.ReadFile(manifest); string(after) != string(before) {
		t.Errorf("manifest changed after the refusal:\n%s", after)
	}
}

func TestRefreshRefusesWhenUpstreamDroppedTheCopySource(t *testing.T) {
	assertCopyRefused(t, sharedHeaderRepo(t, ""), sharedHeaderBuild(), "common/scanner.h", "local_build.copy")
}

// Upstream starting to ship its own file at the destination means the entry
// is out of date; overwriting it would hide that.
func TestRefreshRefusesToOverwriteAnUpstreamFile(t *testing.T) {
	build := &LocalBuild{Copy: []FileCopy{{From: "common/scanner.h", To: "src/scanner.c"}}}
	assertCopyRefused(t, sharedHeaderRepo(t, "// header\n"), build, "sub/src/scanner.c", "local_build.copy")
}

func TestRefreshRefusesACopyThatNeedsGeneration(t *testing.T) {
	build := &LocalBuild{Copy: []FileCopy{{From: "common/scanner.h", To: "helpers.js"}}}
	assertCopyRefused(t, sharedHeaderRepo(t, "// header\n"), build, "helpers.js", "-generate")
}

func TestRefreshRejectsUnsafeCopyPaths(t *testing.T) {
	for _, c := range []FileCopy{
		{From: "../outside.h", To: "src/scanner.h"},
		{From: "/etc/hosts", To: "src/scanner.h"},
		{From: "common/scanner.h", To: "../../escape.h"},
		{From: "common/scanner.h", To: ""},
	} {
		build := &LocalBuild{Copy: []FileCopy{c}}
		manifest, root := copyManifest(t, filepath.Join(t.TempDir(), "no-such-repo"), build)
		err := refresh(options{Lang: "demo", Ref: "HEAD", ManifestPath: manifest, GrammarsDir: root})
		if err == nil || !strings.Contains(err.Error(), "invalid copy") {
			t.Errorf("copy %+v: err = %v; want a copy validation error before any fetch", c, err)
		}
	}
}

func TestCopyUpstreamFileRefusesASymlink(t *testing.T) {
	checkout := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	writeTestFile(t, outside, "secret\n")
	if err := os.MkdirAll(filepath.Join(checkout, "common"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(checkout, "common", "scanner.h")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	err := copyUpstreamFile(checkout, "sub", FileCopy{From: "common/scanner.h", To: "src/scanner.h"})
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("err = %v; want a refusal to follow a symlink", err)
	}
}

// local_changes used to make the tool refuse a grammar. Silently ignoring a
// leftover key would drop that refusal and then delete the key on save.
func TestLoadManifestRejectsUnknownKeys(t *testing.T) {
	for key, want := range map[string]string{
		`"local_changes": true`: "no longer supported",
		`"locl_build": {}`:      "locl_build",
	} {
		path := filepath.Join(t.TempDir(), "grammars.json")
		writeTestFile(t, path, `[{"language": "demo", "url": "u", "reference": "r", "revision": "r", "files": [], "abi": 14, `+key+`}]`)
		_, err := loadManifest(path)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), path) {
			t.Errorf("%s: err = %v; want an error naming %q and the manifest", key, err, want)
		}
	}
	path := filepath.Join(t.TempDir(), "grammars.json")
	writeTestFile(t, path, `[{"language": "demo", "local_build": {"patches": "x"}}]`)
	if _, err := loadManifest(path); err == nil || !strings.Contains(err.Error(), "patches") {
		t.Errorf("unknown local_build key: err = %v", err)
	}
	writeTestFile(t, path, "[]\n[]\n")
	if _, err := loadManifest(path); err == nil {
		t.Error("trailing data after the list was accepted")
	}
}

// The committed manifest must be one the tool reads and writes back unchanged,
// and no patch in it may carry a whole upstream file: that is what copy is for.
func TestCommittedManifestLoads(t *testing.T) {
	dir := filepath.Join("..", "..", "internal", "sem", "grammars")
	path := filepath.Join(dir, "grammars.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	grammars, err := loadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "grammars.json")
	if err := saveManifest(out, grammars); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); string(got) != string(original) {
		t.Errorf("saving the committed manifest changes it:\n%s", got)
	}
	for _, g := range grammars {
		if g.LocalBuild == nil || g.LocalBuild.Patch == "" {
			continue
		}
		patch, err := os.ReadFile(filepath.Join(dir, g.LocalBuild.Patch))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(patch), "--- /dev/null") {
			t.Errorf("%s creates a file; take it from the checkout with local_build.copy instead", g.LocalBuild.Patch)
		}
	}
}

// Runs the committed F# recipe against a stand-in for upstream laid out the
// way upstream is (header in common/, scanner including it by relative path)
// and checks it reproduces the vendored scanner files byte for byte. The
// parser itself comes from the generator, which no test runs.
func TestFSharpRecipeReproducesVendoredScanner(t *testing.T) {
	dir := filepath.Join("..", "..", "internal", "sem", "grammars")
	committed, err := loadManifest(filepath.Join(dir, "grammars.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entry Grammar
	for _, g := range committed {
		if g.Language == "fsharp" {
			entry = g
		}
	}
	if entry.LocalBuild == nil || entry.LocalBuild.Patch == "" {
		t.Fatalf("fsharp entry has no patch: %+v", entry)
	}
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	scannerC, scannerH := read("fsharp/scanner.c"), read("fsharp/scanner.h")
	upstreamC := strings.Replace(scannerC, `#include "scanner.h"`, `#include "../../common/scanner.h"`, 1)
	if upstreamC == scannerC {
		t.Fatal("vendored scanner.c does not include scanner.h")
	}
	entry.URL = gitRepo(t, map[string]string{
		"fsharp/src/scanner.c": upstreamC,
		"common/scanner.h":     scannerH,
		"LICENSE":              "MIT\n",
	})
	fakeNPX(t, entry.LocalBuild.TreeSitterCLI, "14")
	root := t.TempDir()
	manifest := filepath.Join(root, "grammars.json")
	if err := saveManifest(manifest, []Grammar{entry}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, entry.LocalBuild.Patch), read(entry.LocalBuild.Patch))
	if err := refresh(options{Lang: "fsharp", Ref: "HEAD", ManifestPath: manifest, GrammarsDir: root}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"scanner.c": scannerC, "scanner.h": scannerH} {
		if got, _ := os.ReadFile(filepath.Join(root, "fsharp", name)); string(got) != want {
			t.Errorf("fsharp/%s differs from the vendored file", name)
		}
	}
}
