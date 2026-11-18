package sem

import (
	"bytes"
	"strings"
	"testing"
)

// On Windows the documented flow is `entire graph snapshot --format
// compact-ndjson > graph.compact.ndjson`, then `snapshot-query --input`. In
// Windows PowerShell 5.1, Out-File -Encoding utf8 and Set-Content -Encoding
// utf8 write a UTF-8 byte-order mark (so does `>` wherever the profile makes
// UTF-8 its default), and the loader refused the file on its first byte:
//
//	compact snapshot line 1: invalid character U+FEFF looking for beginning of value
//
// One leading BOM is tolerated, as the git-config guard and the Claude status
// line reader already do. Anywhere else it is still malformed input.
func TestLoadCompactSnapshotAllowsUTF8BOMAtStart(t *testing.T) {
	records := compactFixtureRecords()
	data, _ := encodeCompactFixture(t, records)
	plain, err := LoadCompactSnapshot(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	withBOM := append([]byte{0xef, 0xbb, 0xbf}, data...)
	index, err := LoadCompactSnapshot(bytes.NewReader(withBOM))
	if err != nil {
		t.Fatalf("a snapshot saved by Windows PowerShell 5.1 (leading UTF-8 BOM) was refused: %v", err)
	}
	if index.CanonicalSemanticHash != plain.CanonicalSemanticHash {
		t.Fatalf("the BOM changed the decoded snapshot: hash %q, want %q", index.CanonicalSemanticHash, plain.CanonicalSemanticHash)
	}
	if got := index.Query(CompactSnapshotQuery{Symbol: "symbol-id"}).Symbols; len(got) != 1 || got[0].ID != "symbol-id" {
		t.Fatalf("query against the BOM-prefixed snapshot = %#v", got)
	}
}

func TestLoadCompactSnapshotRejectsMisplacedUTF8BOM(t *testing.T) {
	records := compactFixtureRecords()
	data, _ := encodeCompactFixture(t, records)
	bom := []byte{0xef, 0xbb, 0xbf}

	twice := append(append(append([]byte{}, bom...), bom...), data...)
	if _, err := LoadCompactSnapshot(bytes.NewReader(twice)); err == nil {
		t.Fatal("only ONE leading BOM is a save artifact; a second must be refused")
	}

	newline := bytes.IndexByte(data, '\n')
	if newline < 0 {
		t.Fatal("fixture has a single line; cannot place a BOM on line 2")
	}
	midway := append(append(append([]byte{}, data[:newline+1]...), bom...), data[newline+1:]...)
	_, err := LoadCompactSnapshot(bytes.NewReader(midway))
	if err == nil {
		t.Fatal("a BOM at the start of line 2 is not a save artifact and must be refused")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("the refusal should name the line: %v", err)
	}
}
