//go:build ignore

// inventory_testmain reads one `go list -json` package from stdin and reports
// every top-level TestMain declaration selected for that exact target. The
// declaration slice is normalized to LF before hashing so Git checkout line-
// ending policy cannot change the identity of otherwise identical Go source.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const outputSchema = "entire-graph.windows-ci.testmain-inventory.v1"

type packageInput struct {
	Dir          string
	TestGoFiles  []string
	XTestGoFiles []string
}

type declaration struct {
	File                   string `json:"file"`
	NormalizedSourceSHA256 string `json:"normalizedSourceSha256"`
}

type output struct {
	Schema       string        `json:"schema"`
	Declarations []declaration `json:"declarations"`
}

func normalizedDeclarationHash(source []byte) string {
	// CRLF is the expected Windows checkout variation. Normalizing remaining
	// lone CR bytes as well gives this field one explicit, platform-independent
	// text policy instead of depending on a Git client's checkout settings.
	normalized := bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n"))
	normalized = bytes.ReplaceAll(normalized, []byte("\r"), []byte("\n"))
	digest := sha256.Sum256(normalized)
	return hex.EncodeToString(digest[:])
}

func inspectFile(directory, name string) ([]declaration, error) {
	if name == "" || filepath.Base(name) != name {
		return nil, fmt.Errorf("unsafe target-selected test file %q", name)
	}
	path := filepath.Join(directory, name)
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read target-selected test file %q: %w", name, err)
	}
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, path, source, 0)
	if err != nil {
		return nil, fmt.Errorf("parse target-selected test file %q: %w", name, err)
	}
	tokenFile := files.File(parsed.Pos())
	if tokenFile == nil {
		return nil, fmt.Errorf("target-selected test file %q has no token file", name)
	}
	var result []declaration
	for _, candidate := range parsed.Decls {
		function, ok := candidate.(*ast.FuncDecl)
		if !ok || function.Recv != nil || function.Name.Name != "TestMain" {
			continue
		}
		start := tokenFile.Offset(function.Pos())
		end := tokenFile.Offset(function.End())
		if start < 0 || end < start || end > len(source) {
			return nil, fmt.Errorf("invalid TestMain source range in %q", name)
		}
		result = append(result, declaration{
			File:                   name,
			NormalizedSourceSHA256: normalizedDeclarationHash(source[start:end]),
		})
	}
	return result, nil
}

func run(reader io.Reader, writer io.Writer) error {
	decoder := json.NewDecoder(reader)
	var input packageInput
	if err := decoder.Decode(&input); err != nil {
		return fmt.Errorf("decode go-list package: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("go-list input contains more than one JSON value")
		}
		return fmt.Errorf("decode trailing go-list input: %w", err)
	}
	if input.Dir == "" || !filepath.IsAbs(input.Dir) {
		return fmt.Errorf("go-list package directory is not absolute: %q", input.Dir)
	}
	selected := append(append([]string(nil), input.TestGoFiles...), input.XTestGoFiles...)
	seen := make(map[string]bool, len(selected))
	declarations := make([]declaration, 0)
	for _, name := range selected {
		if seen[name] {
			return fmt.Errorf("go-list selected test file %q more than once", name)
		}
		seen[name] = true
		found, err := inspectFile(input.Dir, name)
		if err != nil {
			return err
		}
		declarations = append(declarations, found...)
	}
	sort.Slice(declarations, func(i, j int) bool {
		if declarations[i].File != declarations[j].File {
			return declarations[i].File < declarations[j].File
		}
		return declarations[i].NormalizedSourceSHA256 < declarations[j].NormalizedSourceSHA256
	})
	return json.NewEncoder(writer).Encode(output{
		Schema:       outputSchema,
		Declarations: declarations,
	})
}

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
}
