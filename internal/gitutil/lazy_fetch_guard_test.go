package gitutil

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestParseLazyFetchGuardStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		out       string
		ok        bool
		version   string
		effective bool
	}{
		{"git version 2.45.0\n", true, "2.45.0", true},
		{"git version 2.54.0", true, "2.54.0", true},
		{"git version 2.45.0.rc1", true, "2.45.0.rc1", true},
		{"git version 2.45.2.windows.1", true, "2.45.2.windows.1", true},
		{"git version 3.0.0", true, "3.0.0", true},
		{"git version 2.100.1", true, "2.100.1", true},
		// The last release before GIT_NO_LAZY_FETCH, and the version Xcode ships.
		{"git version 2.44.4", true, "2.44.4", false},
		{"git version 2.39.5 (Apple Git-154)", true, "2.39.5", false},
		{"git version 2.36.0", true, "2.36.0", false},
		{"git version 1.99.0", true, "1.99.0", false},
		// Anything unreadable must report ok=false so callers fail closed.
		{"", false, "", false},
		{"git version", false, "", false},
		{"git version 2", false, "", false},
		{"git version two.45", false, "", false},
		{"git version 2.x", false, "", false},
		{"hub version 2.45.0", false, "", false},
	}
	for _, tc := range cases {
		got, ok := parseLazyFetchGuardStatus(tc.out)
		if ok != tc.ok || got.Version != tc.version || got.Effective != tc.effective {
			t.Errorf("parseLazyFetchGuardStatus(%q) = %+v, ok %v; want version %q effective %v ok %v",
				tc.out, got, ok, tc.version, tc.effective, tc.ok)
		}
	}
}

// writeFakeGitVersion installs a POSIX `git` that reports version and records
// that it ran; any other subcommand fails so a probe cannot accidentally run
// something else.
func writeFakeGitVersion(t *testing.T, version string) (dir, ranMarker string) {
	t.Helper()
	dir = t.TempDir()
	ranMarker = filepath.Join(t.TempDir(), "ran")
	script := "#!/bin/sh\n" +
		"[ \"$GIT_NO_LAZY_FETCH\" = 1 ] || exit 97\n" +
		"case \"$1\" in version) : > " + strconv.Quote(ranMarker) + "; echo 'git version " + version + "' ;; *) exit 98 ;; esac\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, ranMarker
}

func TestLazyFetchGuardProbesTheGitOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake Git is a POSIX shell script")
	}
	for _, tc := range []struct {
		version   string
		effective bool
	}{
		{"2.44.0", false},
		{"2.45.0", true},
	} {
		dir, ran := writeFakeGitVersion(t, tc.version)
		t.Setenv("PATH", dir)
		got := LazyFetchGuard()
		if got.Version != tc.version || got.Effective != tc.effective {
			t.Fatalf("LazyFetchGuard() with Git %s on PATH = %+v, want effective %v", tc.version, got, tc.effective)
		}
		if _, err := os.Stat(ran); err != nil {
			t.Fatalf("Git %s probe did not run the Git on PATH: %v", tc.version, err)
		}
	}

	t.Setenv("PATH", t.TempDir())
	if got := LazyFetchGuard(); got.Effective || got.Version != "" {
		t.Fatalf("LazyFetchGuard() with no Git on PATH = %+v, want an ineffective guard", got)
	}
}

// TestEveryGitSubprocessIsBuiltByTheGuardedConstructors is the static half of
// the no-egress argument for partial clones: GIT_NO_LAZY_FETCH and
// GIT_ALLOW_PROTOCOL protect a command only if it was built by newCmd or
// newGitCmdWithCallerLocale, the two constructors that apply
// gitSubprocessEnvironment. It walks every non-test Go file in the module and
// fails on any other process launch, and on any newCmd/run/runWithStderr call
// whose program is not the literal "git" (newCmd applies the Git environment
// only to "git").
func TestEveryGitSubprocessIsBuiltByTheGuardedConstructors(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found from %s: %v", root, err)
	}
	// Launches that are not provider Git subprocesses, each reviewed:
	allowedLaunchers := map[string]string{
		"internal/gitutil/git.go:newCmd":                    "the guarded constructor",
		"internal/gitutil/git.go:newGitCmdWithCallerLocale": "the guarded caller-locale constructor",
		"internal/cli/verify.go:runVerifyShell":             "runs the user's own test command through their shell",
		"internal/bench/worker.go:":                         "benchmark harness, not shipped provider code",
		"cmd/graph-bench/main.go:":                          "benchmark harness that clones its corpus by design",
	}
	launchSelectors := map[string]bool{
		"exec.Command": true, "exec.CommandContext": true,
		"os.StartProcess": true, "syscall.Exec": true, "syscall.ForkExec": true, "syscall.StartProcess": true,
	}
	gitProgramWrappers := map[string]bool{"newCmd": true, "run": true, "runWithStderr": true}
	passThroughWrappers := map[string]bool{"run": true, "runWithStderr": true}

	var launches, wrappedCalls int
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "node_modules" || name == "bin") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		isGitutil := strings.HasPrefix(rel, "internal/gitutil/")
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				switch node := node.(type) {
				case *ast.CompositeLit:
					if selectorName(node.Type) == "exec.Cmd" {
						t.Errorf("%s: %s builds an exec.Cmd literal; build Git commands with newCmd", fset.Position(node.Pos()), fn.Name.Name)
					}
				case *ast.CallExpr:
					if launchSelectors[selectorName(node.Fun)] {
						launches++
						key := rel + ":" + fn.Name.Name
						if _, ok := allowedLaunchers[key]; !ok {
							if _, ok := allowedLaunchers[rel+":"]; !ok {
								t.Errorf("%s: %s launches a process outside the guarded Git constructors", fset.Position(node.Pos()), fn.Name.Name)
							}
						}
					}
					ident, ok := node.Fun.(*ast.Ident)
					if !isGitutil || !ok || !gitProgramWrappers[ident.Name] || len(node.Args) < 3 {
						return true
					}
					wrappedCalls++
					if lit, ok := node.Args[2].(*ast.BasicLit); ok && lit.Kind == token.STRING && lit.Value == `"git"` {
						return true
					}
					if passThroughWrappers[fn.Name.Name] {
						return true // run/runWithStderr forward their own name argument
					}
					if fn.Name.Name == "newCmd" {
						return true
					}
					t.Errorf("%s: %s calls %s with a program other than the literal \"git\"; newCmd would skip the Git environment",
						fset.Position(node.Pos()), fn.Name.Name, ident.Name)
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Guard against a vacuous scan: the constructors and their callers exist.
	if launches < 2 || wrappedCalls < 20 {
		t.Fatalf("scan found %d launches and %d wrapped Git calls; the walk is not seeing the sources", launches, wrappedCalls)
	}
}

func selectorName(expr ast.Expr) string {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return pkg.Name + "." + selector.Sel.Name
}
