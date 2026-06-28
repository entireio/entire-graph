package lsp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CallEdge is a resolved call in (file, name-line) identity — 1-based lines,
// repo-relative paths — matching brain-bench's oracle/comparison contract.
type CallEdge struct {
	FromFile string `json:"from_file"`
	FromLine int    `json:"from_line"`
	ToFile   string `json:"to_file"`
	ToLine   int    `json:"to_line"`
	FromName string `json:"from_name"`
	ToName   string `json:"to_name"`
}

// Symbol is a function/method declaration at its name position (1-based line).
type Symbol struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Name string `json:"name"`
}

// Result is the LSP call-resolution output for a repo.
type Result struct {
	Symbols    []Symbol
	Calls      []CallEdge
	LoadErrors int
	Stalled    int // functions skipped on a call-hierarchy stall
}

// fnSymbolKinds: LSP SymbolKind Function (12) and Method (6).
var fnSymbolKinds = map[int]bool{6: true, 12: true}

type docSymbol struct {
	Name           string      `json:"name"`
	Kind           int         `json:"kind"`
	SelectionRange lspRange    `json:"selectionRange"`
	Children       []docSymbol `json:"children"`
}

type lspRange struct {
	Start lspPos `json:"start"`
	End   lspPos `json:"end"`
}
type lspPos struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type callHierarchyItem struct {
	Name           string   `json:"name"`
	URI            string   `json:"uri"`
	SelectionRange lspRange `json:"selectionRange"`
	Range          lspRange `json:"range"`
	Kind           int      `json:"kind"`
}

// ResolveCalls runs a full LSP call-resolution pass over the repo's in-scope
// source files for the given language and returns the resolved call graph in
// (file, name-line) identity. Returns (nil, false, nil) when no server for the
// language is available — the caller silently falls back to the heuristic.
func ResolveCalls(ctx context.Context, repo, language string) (*Result, bool, error) {
	server, ok := serverFor(language)
	if !ok {
		return nil, false, nil
	}
	files := server.sourceFiles(repo)
	if len(files) == 0 {
		return &Result{}, true, nil
	}

	client, err := Start(ctx, server.command, server.args, repo)
	if err != nil {
		return nil, false, err
	}
	defer client.Close()

	rootURI := pathToURI(repo)
	caps := map[string]any{
		"textDocument": map[string]any{
			"callHierarchy":      map[string]any{"dynamicRegistration": false},
			"documentSymbol":     map[string]any{"hierarchicalDocumentSymbolSupport": true},
			"publishDiagnostics": map[string]any{},
		},
		"window": map[string]any{"workDoneProgress": true},
	}
	if _, err := client.request("initialize", map[string]any{
		"processId":        os.Getpid(),
		"rootUri":          rootURI,
		"capabilities":     caps,
		"workspaceFolders": []map[string]any{{"uri": rootURI, "name": filepath.Base(repo)}},
	}, 60*time.Second); err != nil {
		return nil, false, err
	}
	_ = client.notify("initialized", map[string]any{})
	for _, f := range files {
		text, _ := os.ReadFile(f)
		_ = client.notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri": pathToURI(f), "languageId": server.languageID, "version": 1, "text": string(text),
			},
		})
	}

	// Wait for the semantic index (cachePriming end). documentSymbol answers from
	// syntax far earlier — using that is the trap the oracle client documents.
	client.pumpUntil(func() bool { return client.ready }, 180*time.Second)

	rel := func(p string) string {
		if r, err := filepath.Rel(repo, p); err == nil {
			return filepath.ToSlash(r)
		}
		return p
	}
	site := func(file string, line0 int) (string, int) { return rel(file), line0 + 1 }

	type fn struct {
		file string
		line int // 0-based selectionRange line
		char int
		name string
	}
	var funcs []fn
	declared := map[[2]any]string{} // (relfile, 1-based line) -> name
	for _, f := range files {
		raw, err := client.request("textDocument/documentSymbol",
			map[string]any{"textDocument": map[string]any{"uri": pathToURI(f)}}, 30*time.Second)
		if err != nil {
			continue
		}
		var syms []docSymbol
		if json.Unmarshal(raw, &syms) != nil {
			continue
		}
		var walk func(items []docSymbol)
		walk = func(items []docSymbol) {
			for _, s := range items {
				if fnSymbolKinds[s.Kind] {
					funcs = append(funcs, fn{f, s.SelectionRange.Start.Line, s.SelectionRange.Start.Character, s.Name})
					rf, ln := site(f, s.SelectionRange.Start.Line)
					declared[[2]any{rf, ln}] = s.Name
				}
				walk(s.Children)
			}
		}
		walk(syms)
	}

	result := &Result{}
	for key, name := range declared {
		result.Symbols = append(result.Symbols, Symbol{File: key[0].(string), Line: key[1].(int), Name: name})
	}
	sort.Slice(result.Symbols, func(i, j int) bool {
		if result.Symbols[i].File != result.Symbols[j].File {
			return result.Symbols[i].File < result.Symbols[j].File
		}
		return result.Symbols[i].Line < result.Symbols[j].Line
	})

	edgeSet := map[CallEdge]bool{}
	for _, fnc := range funcs {
		rf, fl := site(fnc.file, fnc.line)
		prepared, err := client.request("textDocument/prepareCallHierarchy", map[string]any{
			"textDocument": map[string]any{"uri": pathToURI(fnc.file)},
			"position":     map[string]any{"line": fnc.line, "character": fnc.char},
		}, 30*time.Second)
		if err != nil {
			result.Stalled++
			continue
		}
		var items []callHierarchyItem
		if json.Unmarshal(prepared, &items) != nil {
			continue
		}
		stalled := false
		for _, it := range items {
			rawOut, err := client.request("callHierarchy/outgoingCalls",
				map[string]any{"item": it}, 30*time.Second)
			if err != nil {
				stalled = true
				break
			}
			var outs []struct {
				To callHierarchyItem `json:"to"`
			}
			if json.Unmarshal(rawOut, &outs) != nil {
				continue
			}
			for _, oc := range outs {
				trf, tl := site(uriToPath(oc.To.URI), oc.To.SelectionRange.Start.Line)
				if name, ok := declared[[2]any{trf, tl}]; ok && !(trf == rf && tl == fl) {
					edgeSet[CallEdge{rf, fl, trf, tl, fnc.name, name}] = true
				}
			}
		}
		if stalled {
			result.Stalled++
		}
	}
	for e := range edgeSet {
		result.Calls = append(result.Calls, e)
	}
	sort.Slice(result.Calls, func(i, j int) bool {
		a, b := result.Calls[i], result.Calls[j]
		if a.FromFile != b.FromFile {
			return a.FromFile < b.FromFile
		}
		if a.FromLine != b.FromLine {
			return a.FromLine < b.FromLine
		}
		if a.ToFile != b.ToFile {
			return a.ToFile < b.ToFile
		}
		return a.ToLine < b.ToLine
	})

	compiled := map[string]bool{}
	for _, f := range files {
		compiled[rel(f)] = true
	}
	for p, n := range client.diags {
		if compiled[rel(p)] {
			result.LoadErrors += n
		}
	}
	return result, true, nil
}

// server describes an LSP backend for a language.
type server struct {
	command    string
	args       []string
	languageID string
	exts       []string
	testDirs   []string
	srcSubdir  string // restrict to this subdir if present (e.g. "src" for Rust)
}

func (s server) sourceFiles(repo string) []string {
	roots := []string{repo}
	if s.srcSubdir != "" {
		if sub := filepath.Join(repo, s.srcSubdir); isDir(sub) {
			roots = []string{sub}
		}
	}
	var out []string
	for _, root := range roots {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			ok := false
			for _, ext := range s.exts {
				if strings.HasSuffix(path, ext) {
					ok = true
					break
				}
			}
			if !ok {
				return nil
			}
			slashed := "/" + filepath.ToSlash(strings.TrimPrefix(path, repo))
			for _, d := range s.testDirs {
				if strings.Contains(slashed, d) {
					return nil
				}
			}
			out = append(out, path)
			return nil
		})
	}
	sort.Strings(out)
	return out
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// serverFor returns the configured, available LSP server for a language.
func serverFor(language string) (server, bool) {
	switch strings.ToLower(language) {
	case "rust":
		cmd := rustAnalyzerPath()
		if cmd == "" {
			return server{}, false
		}
		return server{
			command:    cmd,
			languageID: "rust",
			exts:       []string{".rs"},
			testDirs:   []string{"/tests/", "/examples/", "/benches/", "/target/"},
			srcSubdir:  "src",
		}, true
	}
	return server{}, false
}

func rustAnalyzerPath() string {
	if p, err := exec.LookPath("rust-analyzer"); err == nil {
		return p
	}
	out, err := exec.Command("rustc", "--print", "sysroot").Output()
	if err != nil {
		return ""
	}
	cand := filepath.Join(strings.TrimSpace(string(out)), "bin", "rust-analyzer")
	if _, err := os.Stat(cand); err == nil {
		return cand
	}
	return ""
}
