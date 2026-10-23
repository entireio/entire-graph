package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	minABI = 13
	maxABI = 14
)

// Grammar is one vendored grammar's origin, as recorded in grammars.json.
type Grammar struct {
	Language  string   `json:"language"`
	URL       string   `json:"url"`
	Reference string   `json:"reference"`
	Revision  string   `json:"revision"`
	Path      string   `json:"path,omitempty"`
	Files     []string `json:"files"`
	ABI       int      `json:"abi"`
	Note      string   `json:"note,omitempty"`
}

var languageVersionPattern = regexp.MustCompile(`(?m)^#define LANGUAGE_VERSION (\d+)\s*$`)

func languageABI(parserC []byte) (int, error) {
	m := languageVersionPattern.FindSubmatch(parserC)
	if m == nil {
		return 0, fmt.Errorf("parser.c has no #define LANGUAGE_VERSION")
	}
	return strconv.Atoi(string(m[1]))
}

// vendor copies a generated grammar from repoRoot/subdir into destDir:
// src/parser.c, src/scanner.c when present, src/*.h, src/tree_sitter/*.h and the
// license file as LICENSE (from the grammar directory, else the repository root). Files
// of those kinds already in destDir that the new grammar lacks are removed;
// everything else there, such as binding.go, is left alone. Nothing in destDir
// changes unless the grammar has a parser.c the go-tree-sitter runtime can load.
func vendor(repoRoot, subdir, destDir string) ([]string, int, error) {
	grammarRoot := filepath.Join(repoRoot, subdir)
	parserC, err := os.ReadFile(filepath.Join(grammarRoot, "src", "parser.c"))
	if err != nil {
		return nil, 0, fmt.Errorf("no generated %s (pick a ref that ships generated sources)", filepath.ToSlash(filepath.Join(subdir, "src", "parser.c")))
	}
	abi, err := languageABI(parserC)
	if err != nil {
		return nil, 0, err
	}
	if abi < minABI || abi > maxABI {
		return nil, 0, fmt.Errorf("grammar is ABI %d, but the go-tree-sitter runtime accepts ABI %d-%d; regenerate it with `tree-sitter generate --abi 14` and vendor the result", abi, minABI, maxABI)
	}

	sources := map[string]string{"parser.c": filepath.Join(grammarRoot, "src", "parser.c")}
	if scanner := filepath.Join(grammarRoot, "src", "scanner.c"); fileExists(scanner) {
		sources["scanner.c"] = scanner
	}
	for _, dir := range []string{"", "tree_sitter/"} {
		headers, err := filepath.Glob(filepath.Join(grammarRoot, "src", filepath.FromSlash(dir), "*.h"))
		if err != nil {
			return nil, 0, err
		}
		for _, h := range headers {
			sources[dir+filepath.Base(h)] = h
		}
	}
	license := findLicense(grammarRoot)
	if license == "" {
		license = findLicense(repoRoot)
	}
	if license == "" {
		return nil, 0, fmt.Errorf("no LICENSE or COPYING file in %s or the repository root", grammarRoot)
	}
	// Always LICENSE: scripts/gen-notices.sh only attributes files by that name.
	sources["LICENSE"] = license

	stale, err := vendoredFiles(destDir)
	if err != nil {
		return nil, 0, err
	}
	for _, rel := range stale {
		if _, keep := sources[rel]; !keep {
			if err := os.Remove(filepath.Join(destDir, filepath.FromSlash(rel))); err != nil {
				return nil, 0, err
			}
		}
	}
	files := make([]string, 0, len(sources))
	for rel, src := range sources {
		data, err := os.ReadFile(src)
		if err != nil {
			return nil, 0, err
		}
		dst := filepath.Join(destDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, 0, err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return nil, 0, err
		}
		files = append(files, rel)
	}
	sort.Strings(files)
	return files, abi, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func findLicense(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		name := strings.ToUpper(e.Name())
		if !e.IsDir() && (strings.HasPrefix(name, "LICENSE") || strings.HasPrefix(name, "COPYING")) {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

// vendoredFiles lists the files in destDir that vendor manages.
func vendoredFiles(destDir string) ([]string, error) {
	var out []string
	entries, err := os.ReadDir(destDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		upper := strings.ToUpper(name)
		switch {
		case e.IsDir() && name == "tree_sitter":
			headers, err := filepath.Glob(filepath.Join(destDir, "tree_sitter", "*.h"))
			if err != nil {
				return nil, err
			}
			for _, h := range headers {
				out = append(out, "tree_sitter/"+filepath.Base(h))
			}
		case name == "parser.c" || name == "scanner.c", !e.IsDir() && strings.HasSuffix(name, ".h"),
			strings.HasPrefix(upper, "LICENSE"), strings.HasPrefix(upper, "COPYING"):
			out = append(out, name)
		}
	}
	return out, nil
}

func loadManifest(path string) ([]Grammar, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var grammars []Grammar
	if err := json.Unmarshal(data, &grammars); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return grammars, nil
}

func saveManifest(path string, grammars []Grammar) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(grammars); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// upsert replaces the entry for g.Language, or inserts it, keeping the
// manifest sorted by language.
func upsert(grammars []Grammar, g Grammar) []Grammar {
	i := sort.Search(len(grammars), func(i int) bool { return grammars[i].Language >= g.Language })
	if i < len(grammars) && grammars[i].Language == g.Language {
		grammars[i] = g
		return grammars
	}
	grammars = append(grammars, Grammar{})
	copy(grammars[i+1:], grammars[i:])
	grammars[i] = g
	return grammars
}
