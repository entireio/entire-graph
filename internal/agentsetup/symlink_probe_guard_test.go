package agentsetup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestSymlinkFixturesProbeFirst keeps the contract skipIfSymlinksUnrepresentable
// documents: a test that builds a symlink fixture with symlinkForTest probes
// first, so a host that withholds the privilege SKIPS it rather than failing.
// symlinkForTest itself is deliberately fatal, because once the probe has
// passed a refusal is a real failure.
//
// Nothing else catches a missing probe. CI runners and Developer Mode
// machines grant the privilege, so a test that forgets the probe stays green
// everywhere a pull request is checked and fails only on an ordinary
// unprivileged Windows account ("A required privilege is not held by the
// client"). The rule is enforced from source so every OS checks it.
func TestSymlinkFixturesProbeFirst(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var offenders []string
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			// The first call to either helper, in source order. A probe inside
			// a subtest closure counts only for fixtures that follow it, which
			// source order models correctly for the shapes used here.
			first := ""
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if first != "" {
					return false
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && (id.Name == "symlinkForTest" || id.Name == "skipIfSymlinksUnrepresentable") {
					first = id.Name
					return false
				}
				return true
			})
			if first == "symlinkForTest" {
				offenders = append(offenders, name+": "+fn.Name.Name)
			}
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Fatalf("these tests build a symlink fixture before calling skipIfSymlinksUnrepresentable, so they FAIL instead of skipping on a host without the symlink privilege:\n  %s", strings.Join(offenders, "\n  "))
	}
}
