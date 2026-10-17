package sem

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestVerifyAboveScopeCannotFollowCoreWorktreeOutsideCheckout(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	scope := filepath.Join(repo, "sub")
	if err := os.MkdirAll(scope, 0755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init")
	write(t, repo, "go.mod", "module example.com/test\n")
	above, close, ok := openSearchVerifyAboveScope(t.Context(), scope, "", false)
	if !ok || above.read == nil {
		t.Fatal("valid checkout was refused")
	}
	close()
	git(t, repo, "config", "core.worktree", "../..")
	_, close, ok = openSearchVerifyAboveScope(t.Context(), scope, "", false)
	if close != nil {
		close()
	}
	if ok {
		t.Fatal("core.worktree escaped nearest physical checkout")
	}
}

func TestVerifyAboveScopeUsesPhysicalRootForSymlinkedScope(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	scope := filepath.Join(repo, "sub", "pkg")
	if err := os.MkdirAll(scope, 0755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init")
	link := filepath.Join(parent, "linkpkg")
	if err := os.Symlink(scope, link); err != nil {
		t.Skip(err)
	}
	above, close, ok := openSearchVerifyAboveScope(t.Context(), link, "", false)
	if !ok {
		t.Fatal("symlink scope refused")
	}
	defer close()
	physical, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if above.up != physical {
		t.Fatalf("cd root=%q, want %q", above.up, physical)
	}
}

func TestVerifyAboveScopeAcceptsRelativeRepo(t *testing.T) {
	repo := t.TempDir()
	scope := filepath.Join(repo, "sub", "pkg")
	if err := os.MkdirAll(scope, 0755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init")
	write(t, repo, "go.mod", "module example.com/test\n")
	physical, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(repo, "sub"))
	for _, relative := range []string{".", "pkg"} {
		above, close, ok := openSearchVerifyAboveScope(t.Context(), relative, "", false)
		if !ok {
			t.Fatalf("relative scope %q refused", relative)
		}
		close()
		if above.up != physical {
			t.Fatalf("cd root=%q, want %q", above.up, physical)
		}
	}
}

func TestVerifyAboveScopeRefusesSymlinkedManifest(t *testing.T) {
	repo := t.TempDir()
	scope := filepath.Join(repo, "sub")
	if err := os.MkdirAll(scope, 0755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init")
	write(t, repo, "actual.mod", "module example.com/fixture\n")
	if err := os.Symlink("actual.mod", filepath.Join(repo, "go.mod")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	above, closeScope, ok := openSearchVerifyAboveScope(t.Context(), scope, "", false)
	if !ok {
		t.Fatal("valid physical checkout refused")
	}
	defer closeScope()
	if content, found := above.read("actual.mod"); !found || content != "module example.com/fixture\n" {
		t.Fatal("control: ordinary manifest must be readable")
	}
	if _, found := above.read("go.mod"); found {
		t.Fatal("a manifest symlink was followed even though its target is inside the checkout")
	}
}

func TestVerifyAboveScopeRetainsNativePackageInventory(t *testing.T) {
	for _, extension := range []string{".s", ".c"} {
		t.Run(extension, func(t *testing.T) {
			source := "sub/native/input" + extension
			files := map[string]string{
				"go.mod":               "module example.com/fixture\n",
				"sub/native/native.go": "package native\nimport \"C\"\n",
				source:                 "// native input\n",
			}
			top := searchVerifyTestEvidence(files)
			scoped := searchVerifyScopedEvidence(files, "sub", nil)
			for name := range files {
				top.files = append(top.files, name)
				if relative, ok := strings.CutPrefix(name, "sub/"); ok {
					scoped.files = append(scoped.files, relative)
				}
			}
			sort.Strings(top.files)
			sort.Strings(scoped.files)
			control := buildSearchVerifyCommand(searchVerifyScopeResults(source, "", ""), top)
			if control == nil || control.Command != "go test ./sub/native" {
				t.Fatalf("control: %#v", control)
			}
			result := buildSearchVerifyCommand(searchVerifyScopeResults("native/input"+extension, "", ""), scoped)
			if result == nil || result.Command != "cd .. && "+control.Command {
				t.Fatalf("scoped VERIFY lost native package: %#v", result)
			}
		})
	}
}

func TestVerifyAboveScopeKeepsRemainingReadBudget(t *testing.T) {
	for _, initial := range []int{0, searchVerifyMaxReads - 1, searchVerifyMaxReads} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			reads := 0
			evidence := searchVerifyEvidence{reads: initial, aboveScope: func() (searchVerifyAboveScope, bool) {
				return searchVerifyAboveScope{prefix: "sub", up: "..", read: func(name string) (string, bool) {
					reads++
					return "module example.com/fixture\n", name == "go.mod"
				}}, true
			}}
			command, _ := deriveSearchVerifyAboveScope(searchVerifySubject{sourcePath: "work.go"}, &evidence)
			if initial == 0 && command == nil {
				t.Fatal("control: manifest did not produce a command")
			}
			if reads > searchVerifyMaxReads-initial {
				t.Fatalf("read budget reset: %d new reads with %d already spent", reads, initial)
			}
			if evidence.reads != initial+reads {
				t.Fatalf("ancestor reads were not accounted: counter=%d, want %d", evidence.reads, initial+reads)
			}
		})
	}
}
