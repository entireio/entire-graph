package sem

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf16"
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

// encodeWideFixture re-encodes UTF-8 bytes as UTF-16 or UTF-32 with the
// matching byte-order mark, which is what Windows PowerShell 5.1 produces for
// `snapshot ... > file` (its `>` is Out-File, whose default is UTF-16LE).
func encodeWideFixture(data []byte, encoding string) []byte {
	var out []byte
	switch encoding {
	case "UTF-16LE", "UTF-16BE":
		little := encoding == "UTF-16LE"
		if little {
			out = []byte{0xff, 0xfe}
		} else {
			out = []byte{0xfe, 0xff}
		}
		for _, unit := range utf16.Encode([]rune(string(data))) {
			if little {
				out = append(out, byte(unit), byte(unit>>8))
			} else {
				out = append(out, byte(unit>>8), byte(unit))
			}
		}
	case "UTF-32LE", "UTF-32BE":
		little := encoding == "UTF-32LE"
		if little {
			out = []byte{0xff, 0xfe, 0x00, 0x00}
		} else {
			out = []byte{0x00, 0x00, 0xfe, 0xff}
		}
		for _, r := range string(data) {
			if little {
				out = append(out, byte(r), byte(r>>8), byte(r>>16), byte(r>>24))
			} else {
				out = append(out, byte(r>>24), byte(r>>16), byte(r>>8), byte(r))
			}
		}
	}
	return out
}

// TestLoadCompactSnapshotNamesAWideEncoding covers the half of the Windows
// save-then-query flow the UTF-8 BOM skip above does NOT reach. PowerShell
// 5.1's `>` defaults to UTF-16LE, not UTF-8-with-BOM, so on a stock Windows
// box the documented
//
//	entire graph snapshot --format compact-ndjson > graph.compact.ndjson
//	entire graph snapshot-query --input graph.compact.ndjson --symbol Foo
//
// produced a UTF-16LE file and this error:
//
//	compact snapshot line 1: invalid character '\xff' looking for beginning of value
//
// which names neither the encoding nor anything to do about it. The refusal
// has to name both, because the file cannot be salvaged by reading further:
// every byte of it is in that encoding.
func TestLoadCompactSnapshotNamesAWideEncoding(t *testing.T) {
	records := compactFixtureRecords()
	data, _ := encodeCompactFixture(t, records)
	for _, encoding := range []string{"UTF-16LE", "UTF-16BE", "UTF-32LE", "UTF-32BE"} {
		t.Run(encoding, func(t *testing.T) {
			wide := encodeWideFixture(data, encoding)
			_, err := LoadCompactSnapshot(bytes.NewReader(wide))
			if err == nil {
				t.Fatalf("a %s snapshot decoded as though it were UTF-8", encoding)
			}
			// The UTF-32LE mark begins with the UTF-16LE mark, so a decoder
			// that checks the short marks first reports the wrong encoding.
			if !strings.Contains(err.Error(), encoding) {
				t.Fatalf("the refusal must name the encoding %s: %v", encoding, err)
			}
			if !strings.Contains(err.Error(), "--output") {
				t.Fatalf("the refusal must name the way out (--output): %v", err)
			}
		})
	}
}

// TestLoadCompactSnapshotDoesNotMistakeShortOrUTF8InputForAWideEncoding is the
// other direction: the encoding check peeks four bytes, and neither a stream
// too short to hold a mark nor an ordinary UTF-8 artifact may be accused of
// being UTF-16.
func TestLoadCompactSnapshotDoesNotMistakeShortOrUTF8InputForAWideEncoding(t *testing.T) {
	records := compactFixtureRecords()
	data, _ := encodeCompactFixture(t, records)

	for _, tc := range []struct {
		name  string
		input []byte
	}{
		{"empty", nil},
		{"one byte of a mark", []byte{0xff}},
		{"three bytes", []byte{0xff, 0x01, 0x02}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadCompactSnapshot(bytes.NewReader(tc.input))
			if err == nil {
				t.Fatal("a truncated artifact must still be refused")
			}
			for _, encoding := range []string{"UTF-16", "UTF-32"} {
				if strings.Contains(err.Error(), encoding) {
					t.Fatalf("%d byte(s) cannot carry a %s mark, yet the refusal claims one: %v", len(tc.input), encoding, err)
				}
			}
		})
	}

	for _, tc := range []struct {
		name  string
		input []byte
	}{
		{"plain UTF-8", data},
		{"UTF-8 with a leading BOM", append(append([]byte{}, 0xef, 0xbb, 0xbf), data...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadCompactSnapshot(bytes.NewReader(tc.input)); err != nil {
				t.Fatalf("a UTF-8 snapshot was refused: %v", err)
			}
		})
	}
}
