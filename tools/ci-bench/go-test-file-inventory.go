//go:build ignore

// Command go-test-file-inventory parses an explicit set of Go test files and
// emits the syntax facts needed by generate-test-overlays.py. It deliberately
// does not load the package for the host platform: the caller obtains the file
// set from a target-specific `go list -json` invocation first.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type declaration struct {
	Names      []string `json:"names"`
	Kind       string   `json:"kind"`
	Start      int      `json:"start"`
	End        int      `json:"end"`
	References []string `json:"references"`
}

type testFunction struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	SHA256 string `json:"sha256"`
}

type fileInventory struct {
	Path         string            `json:"path"`
	Package      string            `json:"package"`
	SHA256       string            `json:"sha256"`
	Imports      map[string]string `json:"imports"`
	Declarations []declaration     `json:"declarations"`
	Tests        []testFunction    `json:"tests"`
	TestMain     *testFunction     `json:"testMain,omitempty"`
}

type output struct {
	SchemaVersion int             `json:"schemaVersion"`
	Files         []fileInventory `json:"files"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: go run go-test-file-inventory.go FILE...")
		os.Exit(2)
	}

	var paths []string
	for _, argument := range os.Args[1:] {
		if argument == "--" {
			continue
		}
		if !strings.HasPrefix(argument, "--file=") {
			fmt.Fprintf(os.Stderr, "unexpected argument %q; use --file=PATH\n", argument)
			os.Exit(2)
		}
		paths = append(paths, strings.TrimPrefix(argument, "--file="))
	}
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "at least one --file=PATH is required")
		os.Exit(2)
	}
	sort.Strings(paths)
	result := output{SchemaVersion: 1}
	for _, path := range paths {
		inventory, err := inspectFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			os.Exit(1)
		}
		result.Files = append(result.Files, inventory)
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "encode inventory: %v\n", err)
		os.Exit(1)
	}
}

func inspectFile(path string) (fileInventory, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fileInventory{}, err
	}
	source, err := os.ReadFile(absolute)
	if err != nil {
		return fileInventory{}, err
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, absolute, source, parser.ParseComments)
	if err != nil {
		return fileInventory{}, err
	}
	tokenFile := fset.File(parsed.Pos())
	if tokenFile == nil {
		return fileInventory{}, fmt.Errorf("parser returned no token file")
	}

	digest := sha256.Sum256(source)
	result := fileInventory{
		Path:         absolute,
		Package:      parsed.Name.Name,
		SHA256:       hex.EncodeToString(digest[:]),
		Imports:      map[string]string{},
		Declarations: []declaration{},
		Tests:        []testFunction{},
	}
	for _, spec := range parsed.Imports {
		pathValue := strings.Trim(spec.Path.Value, "\"")
		alias := filepath.Base(pathValue)
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		result.Imports[alias] = pathValue
	}

	selectorNames := map[*ast.Ident]struct{}{}
	packageObjects := map[*ast.Object]struct{}{}
	for _, object := range parsed.Scope.Objects {
		packageObjects[object] = struct{}{}
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok {
			selectorNames[selector.Sel] = struct{}{}
		}
		return true
	})

	for _, decl := range parsed.Decls {
		names, kind := declarationNames(decl)
		if kind == "" {
			continue
		}
		references := map[string]struct{}{}
		ast.Inspect(decl, func(node ast.Node) bool {
			identifier, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			if identifier.Obj != nil {
				if _, isPackageObject := packageObjects[identifier.Obj]; !isPackageObject {
					return true
				}
			}
			if _, isSelector := selectorNames[identifier]; isSelector {
				return true
			}
			references[identifier.Name] = struct{}{}
			return true
		})
		for _, name := range names {
			delete(references, name)
		}
		referenceList := make([]string, 0, len(references))
		for name := range references {
			referenceList = append(referenceList, name)
		}
		sort.Strings(referenceList)
		start := decl.Pos()
		switch typed := decl.(type) {
		case *ast.FuncDecl:
			if typed.Doc != nil {
				start = typed.Doc.Pos()
			}
		case *ast.GenDecl:
			if typed.Doc != nil {
				start = typed.Doc.Pos()
			}
		}
		item := declaration{
			Names:      names,
			Kind:       kind,
			Start:      tokenFile.Offset(start),
			End:        tokenFile.Offset(decl.End()),
			References: referenceList,
		}
		result.Declarations = append(result.Declarations, item)

		function, ok := decl.(*ast.FuncDecl)
		if !ok || function.Recv != nil {
			continue
		}
		if function.Name.Name == "TestMain" {
			start := tokenFile.Offset(function.Pos())
			end := tokenFile.Offset(function.End())
			functionDigest := sha256.Sum256(source[start:end])
			entry := testFunction{
				Name:   function.Name.Name,
				Kind:   "main",
				Start:  start,
				End:    end,
				SHA256: hex.EncodeToString(functionDigest[:]),
			}
			result.TestMain = &entry
			continue
		}
		if testKind := runnableKind(function.Name.Name); testKind != "" {
			start := tokenFile.Offset(function.Pos())
			end := tokenFile.Offset(function.End())
			functionDigest := sha256.Sum256(source[start:end])
			result.Tests = append(result.Tests, testFunction{
				Name:   function.Name.Name,
				Kind:   testKind,
				Start:  start,
				End:    end,
				SHA256: hex.EncodeToString(functionDigest[:]),
			})
		}
	}
	return result, nil
}

func declarationNames(node ast.Decl) ([]string, string) {
	switch decl := node.(type) {
	case *ast.FuncDecl:
		if decl.Recv != nil {
			return []string{decl.Name.Name}, "method"
		}
		return []string{decl.Name.Name}, "func"
	case *ast.GenDecl:
		if decl.Tok == token.IMPORT {
			return nil, ""
		}
		var names []string
		for _, spec := range decl.Specs {
			switch typed := spec.(type) {
			case *ast.TypeSpec:
				names = append(names, typed.Name.Name)
			case *ast.ValueSpec:
				for _, name := range typed.Names {
					if name.Name != "_" {
						names = append(names, name.Name)
					}
				}
			}
		}
		return names, strings.ToLower(decl.Tok.String())
	default:
		return nil, ""
	}
}

func runnableKind(name string) string {
	for _, candidate := range []struct {
		prefix string
		kind   string
	}{
		{"Test", "test"},
		{"Benchmark", "benchmark"},
		{"Fuzz", "fuzz"},
		{"Example", "example"},
	} {
		if strings.HasPrefix(name, candidate.prefix) && suffixStartsNonLower(name[len(candidate.prefix):]) {
			return candidate.kind
		}
	}
	return ""
}

func suffixStartsNonLower(suffix string) bool {
	if suffix == "" {
		return true
	}
	first := suffix[0]
	return first < 'a' || first > 'z'
}
