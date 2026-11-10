package sem

import (
	"sort"
	"testing"
)

// A native build input in a Go package is a Go verification subject only on
// positive package evidence: a same-directory Go file from the inventory. The
// above-scope walk built its evidence without that inventory, so from a
// subdirectory whose go.mod sits above it the assembly file lost the `go test`
// the same search at the toplevel derives.
func TestSearchVerifyAboveScopeKeepsTheGoNativeInventory(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"go.mod":                  "module example.com/app\n",
		"cmd/app/asm/add.go":      "package asm\n\nfunc Add(a, b int) int\n",
		"cmd/app/asm/add_amd64.s": "TEXT ·Add(SB),$0\n",
		"cmd/app/asm/add_test.go": "package asm\n\nfunc TestAdd(t *testing.T) {}\n",
	}
	const prefix = "cmd/app"
	top := searchVerifyTestEvidence(files)
	for filePath := range files {
		top.files = append(top.files, filePath)
	}
	sort.Strings(top.files)
	topCommand := buildSearchVerifyCommand(
		searchVerifyScopeResults(prefix+"/asm/add_amd64.s", "", ""), top)
	const wantTop = "go test ./cmd/app/asm"
	if topCommand == nil || topCommand.Command != wantTop {
		t.Fatalf("toplevel command = %#v, want %q", topCommand, wantTop)
	}

	scoped := searchVerifyScopedEvidence(files, prefix, nil)
	for filePath := range files {
		if relative, inside := searchVerifyRelative(prefix, filePath); inside {
			scoped.files = append(scoped.files, relative)
		}
	}
	sort.Strings(scoped.files)
	scopedCommand := buildSearchVerifyCommand(
		searchVerifyScopeResults("asm/add_amd64.s", "", ""), scoped)
	if want := "cd ../.. && " + wantTop; scopedCommand == nil || scopedCommand.Command != want {
		t.Fatalf("subdirectory command = %#v, want %q", scopedCommand, want)
	}
}
