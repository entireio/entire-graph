package main

import (
	"os"
	"path/filepath"
	"reflect"
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
	got, _ := os.ReadFile(filepath.Join(dest, "parser.c"))
	if string(got) != "old\n" {
		t.Fatalf("destination changed on refusal: %q", got)
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
	err := refresh("demo", "v1", repo, "", filepath.Join(root, "grammars.json"), root)
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
		err := refresh(tc.lang, "v1", filepath.Join(root, "no-such-repo"), tc.subdir, manifest, root)
		if err == nil || !strings.Contains(err.Error(), "invalid -") {
			t.Errorf("refresh(lang=%q, path=%q) = %v; want a validation error before any fetch", tc.lang, tc.subdir, err)
		}
	}
}

func TestRefreshTreatsDashURLAsRepository(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "ran")
	err := refresh("demo", "v1", "--upload-pack=touch "+marker, "", filepath.Join(root, "grammars.json"), root)
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
