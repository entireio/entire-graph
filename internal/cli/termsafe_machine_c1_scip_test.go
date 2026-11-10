package cli

import (
	"strings"
	"testing"

	scippb "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// TestSCIPIndexIsNotC1Escaped is the counter-case to
// TestMachineFormatsEscapeC1Controls: the one snapshot format the C1 rule must
// NOT be applied to.
//
// `--format scip` writes a binary protobuf, and the rule is defined over a TEXT
// stream. Escaping it rewrites 0xc2 0x8X wherever the wire format happens to put
// those bytes — inside a varint, a length prefix, or a UTF-8 name — and turns a
// parseable Index into an unparseable one. The hostile pathname still has to
// arrive intact, because a consumer that renders it applies its own escape and
// cannot do so to a value this encoder already mangled.
//
// The omission note is the one text stream this format writes, and it goes to a
// terminal, so the rule does apply there.
func TestSCIPIndexIsNotC1Escaped(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	repo, hostilePath := c1HostileRepo(t)
	if !hostilePath {
		t.Skip("filesystem rejects C1 code points in filenames, so there is no pathname to carry")
	}
	cacheDir := t.TempDir()

	stdout, stderr := runVerb(t, repo, cacheDir, []string{"snapshot", "--format", "scip"})

	var index scippb.Index
	if err := proto.Unmarshal([]byte(stdout), &index); err != nil {
		t.Fatalf("scip output is not a valid Index protobuf: %v", err)
	}
	paths := make([]string, 0, len(index.GetDocuments()))
	carried := false
	for _, doc := range index.GetDocuments() {
		paths = append(paths, doc.GetRelativePath())
		if strings.Contains(doc.GetRelativePath(), c1ClipboardWrite) {
			carried = true
		}
	}
	if !carried {
		t.Errorf("the hostile pathname did not survive the protobuf intact: %q", paths)
	}
	if strings.Contains(stdout, "\\u009d") {
		t.Errorf("scip output carries the JSON escape, so the binary stream was rewritten as text")
	}

	if strings.TrimSpace(stderr) == "" {
		t.Fatalf("scip wrote no omission note to stderr")
	}
	assertNoRawC1(t, stderr)
}
