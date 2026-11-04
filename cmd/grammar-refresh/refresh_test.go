package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func grammarSource(t *testing.T, abi string, withScanner bool) string {
	t.Helper()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "src", "parser.c"), "#include \"tree_sitter/parser.h\"\n#define LANGUAGE_VERSION "+abi+"\n")
	writeTestFile(t, filepath.Join(root, "src", "tree_sitter", "parser.h"), "// parser.h\n")
	writeTestFile(t, filepath.Join(root, "LICENSE"), "MIT\n")
	if withScanner {
		writeTestFile(t, filepath.Join(root, "src", "scanner.c"), "// scanner\n")
	}
	return root
}

func TestLanguageABI(t *testing.T) {
	abi, err := languageABI([]byte("#define STATE_COUNT 9\n#define LANGUAGE_VERSION 14\n"))
	if err != nil || abi != 14 {
		t.Fatalf("languageABI = %d, %v; want 14", abi, err)
	}
	if _, err := languageABI([]byte("#define STATE_COUNT 9\n")); err == nil {
		t.Fatal("languageABI without LANGUAGE_VERSION: want error")
	}
}

func TestVendorCopiesGrammarFiles(t *testing.T) {
	src := grammarSource(t, "14", true)
	dest := t.TempDir()
	files, abi, err := vendor(src, "", dest)
	if err != nil {
		t.Fatal(err)
	}
	if abi != 14 {
		t.Fatalf("abi = %d", abi)
	}
	want := []string{"LICENSE", "parser.c", "scanner.c", "tree_sitter/parser.h"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
	got, err := os.ReadFile(filepath.Join(dest, "parser.c"))
	if err != nil || !strings.Contains(string(got), `#include "tree_sitter/parser.h"`) {
		t.Fatalf("parser.c not copied verbatim: %q, %v", got, err)
	}
}

func TestVendorCopiesScannerHeaders(t *testing.T) {
	src := grammarSource(t, "14", true)
	writeTestFile(t, filepath.Join(src, "src", "unicode.h"), "// unicode\n")
	dest := t.TempDir()
	writeTestFile(t, filepath.Join(dest, "stale.h"), "stale\n")
	files, _, err := vendor(src, "", dest)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"LICENSE", "parser.c", "scanner.c", "tree_sitter/parser.h", "unicode.h"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
	if _, err := os.Stat(filepath.Join(dest, "stale.h")); !os.IsNotExist(err) {
		t.Fatalf("stale.h still present (err=%v)", err)
	}
}

func TestVendorReadsSubdirectoryGrammar(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, filepath.Join(repo, "LICENSE"), "MIT\n")
	writeTestFile(t, filepath.Join(repo, "fsharp", "src", "parser.c"), "#define LANGUAGE_VERSION 14\n")
	writeTestFile(t, filepath.Join(repo, "fsharp", "src", "tree_sitter", "parser.h"), "// parser.h\n")
	dest := t.TempDir()
	files, _, err := vendor(repo, "fsharp", dest)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"LICENSE", "parser.c", "tree_sitter/parser.h"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
}

func TestVendorRefusesABI15(t *testing.T) {
	src := grammarSource(t, "15", false)
	dest := t.TempDir()
	writeTestFile(t, filepath.Join(dest, "parser.c"), "old\n")
	_, _, err := vendor(src, "", dest)
	if err == nil {
		t.Fatal("want error for ABI 15")
	}
	for _, want := range []string{"ABI 15", "13-14", "--abi 14"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	// Nothing in destDir may change, which holds only while the ABI gate runs
	// before every write and every removal in vendor.
	got, _ := os.ReadFile(filepath.Join(dest, "parser.c"))
	if string(got) != "old\n" {
		t.Fatalf("destination changed on refusal: %q", got)
	}
}

// A grammar whose new file set drops a file must keep every file it does ship,
// even when a later write fails: deleting first can strip a header the parser.c
// just written still includes, leaving a directory that does not compile.
func TestVendorKeepsOldFilesWhenAWriteFails(t *testing.T) {
	src := grammarSource(t, "14", false)
	writeTestFile(t, filepath.Join(src, "src", "unicode.h"), "// unicode\n")
	dest := t.TempDir()
	writeTestFile(t, filepath.Join(dest, "parser.c"), "old\n")
	writeTestFile(t, filepath.Join(dest, "scanner.c"), "old scanner\n")
	// A regular file where the tree_sitter/ directory must go makes MkdirAll
	// fail part way through the writes, standing in for ENOSPC or EACCES.
	writeTestFile(t, filepath.Join(dest, "tree_sitter"), "not a directory\n")
	if _, _, err := vendor(src, "", dest); err == nil {
		t.Fatal("want error when tree_sitter/ cannot be created")
	}
	// scanner.c is absent from the new grammar, so the stale sweep would have
	// removed it. The sweep must not have run at all.
	if _, err := os.Stat(filepath.Join(dest, "scanner.c")); err != nil {
		t.Fatalf("a failed vendor deleted scanner.c: %v", err)
	}
}

func TestVendorMissingParser(t *testing.T) {
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "grammar.js"), "module.exports = grammar({})\n")
	_, _, err := vendor(src, "", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "src/parser.c") {
		t.Fatalf("want error naming src/parser.c, got %v", err)
	}
}

func TestVendorRemovesStaleScanner(t *testing.T) {
	src := grammarSource(t, "14", false)
	dest := t.TempDir()
	writeTestFile(t, filepath.Join(dest, "scanner.c"), "stale\n")
	writeTestFile(t, filepath.Join(dest, "tree_sitter", "array.h"), "stale\n")
	writeTestFile(t, filepath.Join(dest, "binding.go"), "package x\n")
	if _, _, err := vendor(src, "", dest); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"scanner.c", "tree_sitter/array.h"} {
		if _, err := os.Stat(filepath.Join(dest, gone)); !os.IsNotExist(err) {
			t.Errorf("%s still present (err=%v)", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "binding.go")); err != nil {
		t.Fatalf("binding.go removed: %v", err)
	}
}

func TestManifestRoundTripStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grammars.json")
	original := `[
  {
    "language": "fsharp",
    "url": "https://github.com/ionide/tree-sitter-fsharp",
    "reference": "main",
    "revision": "02929f0",
    "path": "fsharp",
    "files": [
      "LICENSE",
      "parser.c"
    ],
    "abi": 14
  },
  {
    "language": "swift",
    "url": "https://github.com/alex-pinkus/tree-sitter-swift",
    "reference": "0.7.4-with-generated-files",
    "revision": "82bb3a533e0801fd2bbaa11dc49676e10bf41948",
    "files": [
      "parser.c"
    ],
    "abi": 14,
    "note": "copied verbatim"
  }
]
`
	writeTestFile(t, path, original)
	grammars, err := loadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveManifest(path, grammars); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("round trip changed manifest:\n%s", got)
	}
}

func TestVendorWritesLicenseAsLICENSE(t *testing.T) {
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "src", "parser.c"), "#define LANGUAGE_VERSION 14\n")
	writeTestFile(t, filepath.Join(src, "COPYING.txt"), "CC0\n")
	dest := t.TempDir()
	writeTestFile(t, filepath.Join(dest, "LICENSE"), "old\n")
	files, _, err := vendor(src, "", dest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files, []string{"LICENSE", "parser.c"}) {
		t.Fatalf("files = %v", files)
	}
	got, err := os.ReadFile(filepath.Join(dest, "LICENSE"))
	if err != nil || string(got) != "CC0\n" {
		t.Fatalf("LICENSE = %q, %v; scripts/gen-notices.sh only finds files named LICENSE", got, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "COPYING.txt")); !os.IsNotExist(err) {
		t.Fatalf("COPYING.txt vendored under its upstream name (err=%v)", err)
	}
}

func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	for name, content := range files {
		writeTestFile(t, filepath.Join(repo, name), content)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "."},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "fixture"},
		{"tag", "v1"},
	} {
		if err := git(repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	return repo
}

func TestRefreshMissingParserNamesRef(t *testing.T) {
	repo := gitRepo(t, map[string]string{"grammar.js": "module.exports = grammar({})\n", "LICENSE": "MIT\n"})
	root := t.TempDir()
	err := refresh("demo", "v1", repo, "", filepath.Join(root, "grammars.json"), root, false)
	if err == nil {
		t.Fatal("want error for a ref without generated sources")
	}
	for _, want := range []string{repo, "v1", "src/parser.c"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestRefreshRejectsUnsafeNames(t *testing.T) {
	root := t.TempDir()
	manifest := filepath.Join(root, "grammars.json")
	for _, tc := range []struct{ lang, subdir string }{
		{"../..", ""},
		{"Swift", ""},
		{"swift", "../outside"},
		{"swift", "/abs"},
	} {
		err := refresh(tc.lang, "v1", filepath.Join(root, "no-such-repo"), tc.subdir, manifest, root, false)
		if err == nil || !strings.Contains(err.Error(), "invalid -") {
			t.Errorf("refresh(lang=%q, path=%q) = %v; want a validation error before any fetch", tc.lang, tc.subdir, err)
		}
	}
}

func TestRefreshTreatsDashURLAsRepository(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "ran")
	err := refresh("demo", "v1", "--upload-pack=touch "+marker, "", filepath.Join(root, "grammars.json"), root, false)
	if err == nil {
		t.Fatal("want fetch error")
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("a URL starting with - was parsed as a git option")
	}
}

func TestUpsertKeepsManifestSorted(t *testing.T) {
	grammars := []Grammar{{Language: "dart"}, {Language: "zig"}}
	grammars = upsert(grammars, Grammar{Language: "swift", Revision: "a"})
	grammars = upsert(grammars, Grammar{Language: "swift", Revision: "b"})
	var names []string
	for _, g := range grammars {
		names = append(names, g.Language)
	}
	if !reflect.DeepEqual(names, []string{"dart", "swift", "zig"}) || grammars[1].Revision != "b" {
		t.Fatalf("upsert = %+v", grammars)
	}
}

// Appending a new grammar to the end of grammars.json is the natural hand
// edit, and it leaves the manifest unsorted. upsert must still replace the
// entry it is given rather than add a second one for the same language: a
// duplicate makes the manifest describe one language two ways, and the stale
// half then drives the next refresh.
func TestUpsertReplacesInAnUnsortedManifest(t *testing.T) {
	for _, appended := range []string{"kotlin", "ada", "lua"} {
		t.Run(appended, func(t *testing.T) {
			base := []string{"clojure", "csharp", "dart", "erlang", "fsharp", "haskell", "julia", "objc", "perl", "r", "swift", "zig"}
			var grammars []Grammar
			for _, lang := range base {
				grammars = append(grammars, Grammar{Language: lang, Revision: "old"})
			}
			grammars = append(grammars, Grammar{Language: appended, Revision: "old"})
			for _, target := range append(base, appended) {
				got := upsert(append([]Grammar(nil), grammars...), Grammar{Language: target, Revision: "new"})
				count := 0
				for _, g := range got {
					if g.Language == target {
						count++
						if g.Revision != "new" {
							t.Errorf("refreshing %q left a stale entry: %+v", target, g)
						}
					}
				}
				if count != 1 {
					t.Errorf("refreshing %q with %q appended produced %d entries for it", target, appended, count)
				}
				if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].Language < got[j].Language }) {
					t.Errorf("refreshing %q left the manifest unsorted", target)
				}
			}
		})
	}
}

func TestLoadManifestRejectsADuplicateLanguage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grammars.json")
	writeTestFile(t, path, `[{"language":"swift","local_changes":true,"note":"patched"},{"language":"swift"}]`)
	_, err := loadManifest(path)
	if err == nil || !strings.Contains(err.Error(), "listed twice") {
		t.Fatalf("loadManifest with a duplicate language = %v; want it refused", err)
	}
}

func localChangesManifest(t *testing.T, repo, revision string) (manifest, root string) {
	t.Helper()
	root = t.TempDir()
	manifest = filepath.Join(root, "grammars.json")
	if err := saveManifest(manifest, []Grammar{{
		Language:     "demo",
		URL:          repo,
		Reference:    "v0",
		Revision:     revision,
		Files:        []string{"LICENSE", "parser.c"},
		ABI:          14,
		LocalChanges: true,
		Note:         "reapply demo.patch",
	}}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "demo", "parser.c"), "patched\n")
	return manifest, root
}

func generatedGrammarRepo(t *testing.T) (repo, revision string) {
	t.Helper()
	repo = gitRepo(t, map[string]string{"src/parser.c": "#define LANGUAGE_VERSION 14\n", "LICENSE": "MIT\n"})
	revision, err := gitOutput(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return repo, revision
}

func TestRefreshRefusesLocalChanges(t *testing.T) {
	repo, revision := generatedGrammarRepo(t)
	for name, recorded := range map[string]string{"same revision": revision, "new revision": "0000000"} {
		t.Run(name, func(t *testing.T) {
			manifest, root := localChangesManifest(t, repo, recorded)
			before, _ := os.ReadFile(manifest)
			err := refresh("demo", "v1", "", "", manifest, root, false)
			if err == nil {
				t.Fatal("want refusal for a grammar with local changes")
			}
			for _, want := range []string{"-allow-local-changes", "reapply demo.patch"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
			if got, _ := os.ReadFile(filepath.Join(root, "demo", "parser.c")); string(got) != "patched\n" {
				t.Fatalf("parser.c changed on refusal: %q", got)
			}
			if after, _ := os.ReadFile(manifest); string(after) != string(before) {
				t.Fatalf("manifest changed on refusal:\n%s", after)
			}
		})
	}
}

func TestRefreshKeepsNoteAcrossRevisions(t *testing.T) {
	repo, revision := generatedGrammarRepo(t)
	for name, recorded := range map[string]string{"same revision": revision, "new revision": "0000000"} {
		t.Run(name, func(t *testing.T) {
			manifest, root := localChangesManifest(t, repo, recorded)
			if err := refresh("demo", "v1", "", "", manifest, root, true); err != nil {
				t.Fatal(err)
			}
			grammars, err := loadManifest(manifest)
			if err != nil {
				t.Fatal(err)
			}
			got := grammars[0]
			if got.Revision != revision || got.Note != "reapply demo.patch" || !got.LocalChanges {
				t.Fatalf("entry = %+v", got)
			}
		})
	}
}
