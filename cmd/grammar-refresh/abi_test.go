package main

import (
	"go/build"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

func TestABIRangeMatchesRuntime(t *testing.T) {
	pkg, err := build.Import("github.com/smacker/go-tree-sitter", ".", build.FindOnly)
	if err != nil {
		t.Fatal(err)
	}
	api, err := os.ReadFile(filepath.Join(pkg.Dir, "api.h"))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]int{
		"TREE_SITTER_LANGUAGE_VERSION":                maxABI,
		"TREE_SITTER_MIN_COMPATIBLE_LANGUAGE_VERSION": minABI,
	} {
		m := regexp.MustCompile(`(?m)^#define ` + name + ` (\d+)\s*$`).FindSubmatch(api)
		if m == nil {
			t.Fatalf("api.h has no #define %s", name)
		}
		if got, _ := strconv.Atoi(string(m[1])); got != want {
			t.Errorf("go-tree-sitter %s is %d, but grammar-refresh uses %d; update minABI/maxABI in refresh.go", name, got, want)
		}
	}
}
