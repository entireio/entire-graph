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

// TestRenderSearchFileOutlineEscapesRows covers the rows under that path. They
// are one symbol per line, so a name is a one-line value in the same sense the
// path above it is: escaping only the header would let a symbol index a symbol
// the file does not contain, which is worse here than elsewhere because the whole
// point of the block is to be believed without reading the file.
func TestRenderSearchFileOutlineEscapesRows(t *testing.T) {
	t.Parallel()
	assertNoForgedLine(t, "RenderSearchFileOutline rows", RenderSearchFileOutline([]SearchFileOutline{{
		FilePath: "src/router.go", Lines: 40,
		Rows: []SearchOutlineRow{{
			Start: 10, End: 14, Kind: "func" + forgedPath, Name: "merge" + forgedPath, Hit: true,
		}},
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

// TestRenderSearchVerifyCommandEscapesOneLineFields is the highest-stakes case in
// this file. VERIFY is the line an agent is instructed to RUN, so a pathname that
// splits the record lets a repository display a command the deriver never
// produced. Execution was never at risk — the path is shell-quoted before it
// reaches the command string — but what the reader is shown was.
func TestRenderSearchVerifyCommandEscapesOneLineFields(t *testing.T) {
	t.Parallel()
	rendered := RenderSearchVerifyCommand(&SearchVerifyCommand{
		Command:     "python -m py_compile '" + forgedPath + "'",
		Targets:     forgedPath,
		DerivedFrom: "build check on " + forgedPath,
		Guard:       "cfg(" + forgedPath + ")",
		Tier:        "build-check",
	})
	assertNoForgedLine(t, "RenderSearchVerifyCommand", rendered)
	// Every line of the block must still belong to the block: the record is
	// "VERIFY:" then indented evidence, and nothing else.
	for _, line := range strings.Split(strings.TrimRight(string(rendered), "\n"), "\n") {
		if !strings.HasPrefix(line, "VERIFY:") && !strings.HasPrefix(line, "  ") {
			t.Errorf("a field forged a line outside the block's shape: %q\nin:\n%s", line, rendered)
		}
	}
}

func TestRenderSearchCoverageNoteEscapesOneLineFields(t *testing.T) {
	t.Parallel()
	assertNoForgedLine(t, "RenderSearchCoverageNote", RenderSearchCoverageNote(&SearchCoverageNote{
		Symbol: "Merge" + forgedPath, Total: 3,
		Peers:    []string{"TestMerge" + forgedPath},
		FilePath: forgedPath,
	}))
}
