package sem

import (
	"bytes"
	"strings"
	"testing"
)

// hostileEntity is what a YAML `metadata.name` can hold: yamlLineValue strips
// comments and quotes but neutralizes nothing, so the scalar reaches Entity.Name
// with its bytes intact and is carried into EntityChange.Name from there.
const hostileEntity = "evil\x1b[2K\x1b[31m"

func hostileDiffResult() Result {
	return Result{
		Base: "main", Head: "HEAD",
		Files: []FileChange{{
			Path: "deploy/" + hostileEntity + ".yaml", Status: "M", Language: "yaml",
			Changes: []EntityChange{
				{Type: "added", Kind: "resource", Name: hostileEntity},
				{Type: "removed", Kind: "resource", Name: hostileEntity, DependentsCount: 2},
			},
		}},
	}
}

// TestWriteTextEscapesEntityNameWithoutColor covers the plain path, where
// textStyles.render used to return the untrusted value completely unchanged.
func TestWriteTextEscapesEntityNameWithoutColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buffer bytes.Buffer
	writeText(&buffer, hostileDiffResult())
	assertDiffTextIsDefanged(t, buffer.String())
}

// TestWriteTextEscapesEntityNameWithColor covers the worse half. With color on
// the value is concatenated BETWEEN codes this renderer opened, so an embedded
// ESC does not merely inject its own sequence — it terminates the styling early
// and leaves the terminal in a state the trailing reset cannot close.
func TestWriteTextEscapesEntityNameWithColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("ENTIRE_GRAPH_FORCE_COLOR", "1")
	var buffer bytes.Buffer
	writeText(&buffer, hostileDiffResult())
	rendered := buffer.String()
	if !strings.Contains(rendered, "\x1b[") {
		t.Fatalf("expected this case to exercise the colored path:\n%q", rendered)
	}
	// Every ESC still present must be one this renderer emitted itself: an SGR
	// introducer immediately followed by digits or the reset.
	for _, fragment := range strings.Split(rendered, "\x1b")[1:] {
		if !strings.HasPrefix(fragment, "[") {
			t.Errorf("ESC not introducing an SGR sequence survived:\n%q", rendered)
			continue
		}
		code := strings.SplitN(fragment[1:], "m", 2)
		if len(code) != 2 || strings.Trim(code[0], "0123456789;") != "" {
			t.Errorf("ESC introducing a non-SGR sequence survived:\n%q", rendered)
		}
	}
	assertDiffTextIsDefanged(t, rendered)
}

func assertDiffTextIsDefanged(t *testing.T, rendered string) {
	t.Helper()
	if strings.Contains(rendered, hostileEntity) {
		t.Errorf("the repository-controlled sequence reached the terminal intact:\n%q", rendered)
	}
	// Defanged, not dropped: the reader must still be told which entity changed.
	if !strings.Contains(rendered, "evil") {
		t.Errorf("the entity name was dropped rather than escaped:\n%s", rendered)
	}
}

// TestWriteTextWithoutControlBytesIsUnchanged keeps ordinary diffs byte-identical,
// which is what stops this guard from churning every text-output fixture.
func TestWriteTextWithoutControlBytesIsUnchanged(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	clean := Result{
		Base: "main", Head: "HEAD",
		Files: []FileChange{{
			Path: "internal/sem/text.go", Status: "M", Language: "go",
			Changes: []EntityChange{{Type: "signature_changed", Kind: "function", Name: "render", DependentsCount: 7}},
		}},
	}
	var buffer bytes.Buffer
	writeText(&buffer, clean)
	rendered := buffer.String()
	for _, want := range []string{"internal/sem/text.go", "render", "(7 dependents)", "(go)"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("clean diff no longer reports %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, `\x`) {
		t.Errorf("clean diff gained an escape artifact:\n%s", rendered)
	}
}
