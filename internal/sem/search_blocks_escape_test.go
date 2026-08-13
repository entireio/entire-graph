package sem

import (
	"strings"
	"testing"
)

// The auxiliary search blocks are all one-record-per-line, so the path and symbol
// fields in them are one-line values in exactly the sense the ranked-result
// headers are. The writer wrap the CLI puts around this output stops a control
// sequence but cannot stop a NEWLINE — by the time bytes reach it, a snippet's
// newline and a path's are the same byte — so a repository could otherwise name a
// file such that a block reports a site the search never found.

// forgedPath is a pathname that tries to print a second entry, plus a live escape
// sequence so both halves of the guard are exercised at once.
const forgedPath = "a.go\n  src/attacker-owned.go:1 forged\x1b[2K"

func assertNoForgedLine(t *testing.T, what string, rendered []byte) {
	t.Helper()
	output := string(rendered)
	if strings.ContainsRune(output, 0x1b) {
		t.Errorf("%s passed a raw ESC through:\n%q", what, output)
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "attacker-owned.go") && !strings.Contains(line, "a.go") {
			t.Errorf("%s let a pathname forge its own line:\n%s", what, output)
		}
	}
	if !strings.Contains(output, "a.go") {
		t.Errorf("%s dropped the path instead of escaping it:\n%s", what, output)
	}
}

func TestRenderSearchClosedSetEscapesOneLineFields(t *testing.T) {
	t.Parallel()
	assertNoForgedLine(t, "RenderSearchClosedSet", RenderSearchClosedSet(&SearchClosedSet{
		Type: "Ops", Kind: "enum", Variants: 3,
		Sites: []SearchClosedSetSite{{
			FilePath: forgedPath, Line: 12, Symbol: "apply" + forgedPath,
			Arms: 2, Default: "silent", Checked: "runtime",
		}},
	}))
}

func TestRenderSearchLiteralClusterEscapesOneLineFields(t *testing.T) {
	t.Parallel()
	assertNoForgedLine(t, "RenderSearchLiteralCluster", RenderSearchLiteralCluster(&SearchLiteralCluster{
		Literal: "retry",
		Hits: []SearchLiteralHit{{
			FilePath: forgedPath, Line: 7, Symbol: "doRetry", Role: "edit",
		}},
		HitsTotal: 1, FilesTotal: 1,
	}))
}

func TestRenderSearchContainerMapEscapesOneLineFields(t *testing.T) {
	t.Parallel()
	assertNoForgedLine(t, "RenderSearchContainerMap", RenderSearchContainerMap(&SearchContainerMap{
		FilePath: forgedPath, FileLines: 40, Kind: "struct", Name: "Router" + forgedPath,
		StartLine: 1, EndLine: 40,
		Fields:  []string{"inner" + forgedPath},
		Members: []SearchContainerMember{{Name: "merge", StartLine: 10, EndLine: 14}},
	}, false))
}

func TestRenderSearchFileOutlineEscapesPath(t *testing.T) {
	t.Parallel()
	assertNoForgedLine(t, "RenderSearchFileOutline", RenderSearchFileOutline([]SearchFileOutline{{
		FilePath: forgedPath, Lines: 40,
	}}))
}

// TestSearchBlocksLeaveCleanInputAlone is the compatibility half: these blocks are
// budget-sized by the caller, so an escape that fired on ordinary paths would
// change every payload's length as well as its text.
func TestSearchBlocksLeaveCleanInputAlone(t *testing.T) {
	t.Parallel()
	rendered := string(RenderSearchLiteralCluster(&SearchLiteralCluster{
		Literal: "retry",
		Hits: []SearchLiteralHit{
			{FilePath: "internal/cli/search.go", Line: 7, Symbol: "runSearch", Role: "edit"},
			{FilePath: "src/naïve/日本語.go", Line: 9, Symbol: "handle", Role: "read"},
		},
		HitsTotal: 2, FilesTotal: 2,
	}))
	for _, want := range []string{"internal/cli/search.go:7", "src/naïve/日本語.go:9"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("clean path %q no longer renders byte-identically:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, `\x`) {
		t.Errorf("clean block gained an escape artifact:\n%s", rendered)
	}
}
