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
	// LocalChanges marks vendored files that differ from upstream at Revision
	// in ways a refresh would discard. Note says how to reapply them.
	LocalChanges bool   `json:"local_changes,omitempty"`
	Note         string `json:"note,omitempty"`
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
//
// Every new file is written before any stale one is removed, so a failure part
// way through leaves a superset of the two grammars rather than a hole: the
// package still compiles and `git checkout` restores it. Deleting first would
// strip a header the newly written parser.c still includes.
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
	licenses := findLicenses(grammarRoot)
	if len(licenses) == 0 {
		licenses = findLicenses(repoRoot)
	}
	switch len(licenses) {
	case 0:
		return nil, 0, fmt.Errorf("no LICENSE or COPYING file in %s or the repository root", grammarRoot)
	case 1:
	default:
		// Refuse rather than pick. scripts/gen-notices.sh finds vendored
		// licences by the name LICENSE alone and prints one SPDX label per
		// file, taking the first its grep recognises, so a dual-licensed
		// upstream has no faithful single-file form: keeping one under-
		// attributes, and concatenating gets labelled as whichever text
		// classify_license matches first. Which grant we ship is a decision
		// for a person.
		names := make([]string, 0, len(licenses))
		for _, l := range licenses {
			names = append(names, filepath.Base(l))
		}
		return nil, 0, fmt.Errorf("%s carries %d licence files (%s); pick the grant to vendor, record the choice in the grammar's note, and vendor it by hand", filepath.ToSlash(subdir), len(licenses), strings.Join(names, ", "))
	}
	// Always LICENSE: scripts/gen-notices.sh only attributes files by that name.
	sources["LICENSE"] = licenses[0]

	stale, err := vendoredFiles(destDir)
	if err != nil {
		return nil, 0, err
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
	for _, rel := range stale {
		if _, keep := sources[rel]; !keep {
			if err := os.Remove(filepath.Join(destDir, filepath.FromSlash(rel))); err != nil {
				return nil, 0, err
			}
		}
	}
	sort.Strings(files)
	return files, abi, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// findLicenses returns every licence file directly in dir, sorted by name.
//
// It returns all of them rather than the first, because the first is only the
// right answer when there is exactly one: an upstream offering a choice of
// grants ships LICENSE-MIT beside LICENSE-APACHE, and returning either one
// alone silently drops half of what the vendored bytes are licensed under.
// The caller decides what to do with more than one.
func findLicenses(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := strings.ToUpper(e.Name())
		if !e.IsDir() && (strings.HasPrefix(name, "LICENSE") || strings.HasPrefix(name, "COPYING")) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out
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
		case !e.IsDir() && (name == "parser.c" || name == "scanner.c"),
			!e.IsDir() && strings.HasSuffix(name, ".h"),
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
	// One entry per language, because every lookup here resolves a language to
	// exactly one origin. A duplicate would let a refresh read one entry's
	// local_changes guard and overwrite the files the other describes.
	seen := make(map[string]bool, len(grammars))
	for _, g := range grammars {
		if seen[g.Language] {
			return nil, fmt.Errorf("%s: %q is listed twice; keep one entry per language", path, g.Language)
		}
		seen[g.Language] = true
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

// upsert replaces the entry for g.Language, or inserts it, and returns the
// manifest sorted by language.
//
// The scan is linear and the sort unconditional on purpose: a binary search
// would need the manifest to already be sorted, and nothing enforces that. One
// entry hand-appended out of order made the search miss a language that was
// present and insert a second entry for it.
func upsert(grammars []Grammar, g Grammar) []Grammar {
	replaced := false
	for i := range grammars {
		if grammars[i].Language == g.Language {
			grammars[i] = g
			replaced = true
			break
		}
	}
	if !replaced {
		grammars = append(grammars, g)
	}
	sort.Slice(grammars, func(i, j int) bool { return grammars[i].Language < grammars[j].Language })
	return grammars
}
