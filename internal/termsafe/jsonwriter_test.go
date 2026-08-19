package termsafe

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

// TestJSONWriterEscapesOnlyTheC1Range pins both halves of the rule at once: the
// bytes encoding/json leaves raw are rewritten, and everything it already decided
// about is passed through untouched.
//
// The pass-through half is the one that can break a consumer. A JSON document is
// mostly ASCII structure, its C0 controls are already \uXXXX text, and the byte
// 0x97 sitting inside U+65E5 (0xe6 0x97 0xa5) only LOOKS like a C1 control — a
// per-byte rewrite would corrupt every CJK identifier in the stream.
func TestJSONWriterEscapesOnlyTheC1Range(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		input string
		want  string
	}{
		{
			name: "OSC and ST in their two-byte UTF-8 form",
			// The pair that brackets an OSC 52 clipboard write, as a pathname would
			// carry it through a JSON string.
			input: "{\"file_path\":\"evil\u009d52;c;aGVsbG8=\u009cfile.go\"}\n",
			want:  "{\"file_path\":\"evil\\u009d52;c;aGVsbG8=\\u009cfile.go\"}\n",
		},
		{
			name:  "CSI, the introducer escaping only C0 would leave reachable",
			input: "{\"p\":\"a\u009b31mb\"}",
			want:  "{\"p\":\"a\\u009b31mb\"}",
		},
		{
			name: "a stray C1 byte that begins no valid sequence",
			// encoding/json folds invalid UTF-8 to U+FFFD, so this shape does not
			// arrive from an encoder; it is escaped anyway because a sink is not
			// entitled to assume who wrote into it.
			input: "{\"p\":\"a\x9bb\"}",
			want:  "{\"p\":\"a\\u009bb\"}",
		},
		{
			name: "the C0 escapes encoding/json already wrote",
			// Six ASCII characters, not a control. Re-escaping the backslash would
			// change the decoded value.
			input: "{\"p\":\"a\\u001b[2Kb\"}\n",
			want:  "{\"p\":\"a\\u001b[2Kb\"}\n",
		},
		{
			name: "a rune whose interior byte falls in the C1 range",
			// U+65E5 is 0xe6 0x97 0xa5; 0x97 is a C1 value and must be stepped over,
			// not inspected.
			input: "{\"p\":\"\u65e5\u672c\"}",
			want:  "{\"p\":\"\u65e5\u672c\"}",
		},
		{
			name:  "the byte above the C1 range that Latin-1 sources depend on",
			input: "{\"p\":\"caf\u00e9\"}",
			want:  "{\"p\":\"caf\u00e9\"}",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var buffer bytes.Buffer
			written, err := NewJSONWriter(&buffer).Write([]byte(testCase.input))
			if err != nil {
				t.Fatalf("write: %v", err)
			}
			if written != len(testCase.input) {
				t.Errorf("reported %d bytes of a %d-byte buffer", written, len(testCase.input))
			}
			if buffer.String() != testCase.want {
				t.Errorf("got  %q\nwant %q", buffer.String(), testCase.want)
			}
		})
	}
}

// TestJSONWriterEscapeIsLossless is the property that lets this wrapper sit on a
// machine format at all: the consumer must still decode the code point the
// repository holds. If it did not, escaping would be a silent data change rather
// than a display fix, and the machine formats would stop being usable.
func TestJSONWriterEscapeIsLossless(t *testing.T) {
	original := map[string]string{"file_path": "evil\u009d52;c;aGVsbG8=\u009cfile.go"}

	var buffer bytes.Buffer
	encoder := json.NewEncoder(NewJSONWriter(&buffer))
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(original); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if index := indexRawC1(buffer.String()); index >= 0 {
		t.Fatalf("raw C1 control at byte %d of the encoded stream: %q", index, buffer.String())
	}

	var decoded map[string]string
	if err := json.Unmarshal(buffer.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v\n%s", err, buffer.String())
	}
	if decoded["file_path"] != original["file_path"] {
		t.Errorf("escaping changed the value:\n got  %q\n want %q", decoded["file_path"], original["file_path"])
	}
}

// TestJSONWriterLeavesOrdinaryOutputByteIdentical guards the cost side. Payloads
// with nothing to escape are the overwhelming majority, and they must not be
// copied, reallocated, or altered — the underlying writer's own count is returned
// for them.
func TestJSONWriterLeavesOrdinaryOutputByteIdentical(t *testing.T) {
	ordinary := "{\"query\":\"merge\",\"results\":[{\"file_path\":\"internal/cli/search.go\",\"rank\":1}]}\n"
	var buffer bytes.Buffer
	written, err := NewJSONWriter(&buffer).Write([]byte(ordinary))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if written != len(ordinary) {
		t.Errorf("reported %d bytes of a %d-byte buffer", written, len(ordinary))
	}
	if buffer.String() != ordinary {
		t.Errorf("rewrote output that held no control byte:\n got  %q\n want %q", buffer.String(), ordinary)
	}
}

// TestJSONWriterPassesThroughUnderlyingError and the short-write case below mirror
// Writer's own contract tests: a truncated write is how a half-written escape
// stops being an escape, so it must surface rather than be reported as progress.
func TestJSONWriterPassesThroughUnderlyingError(t *testing.T) {
	sentinel := errors.New("sink failed")
	for _, input := range []string{
		"{\"p\":\"plain\"}\n",
		"{\"p\":\"evil\u009d\"}\n",
	} {
		written, err := NewJSONWriter(failingWriter{err: sentinel}).Write([]byte(input))
		if !errors.Is(err, sentinel) {
			t.Errorf("%q: got error %v, want %v", input, err, sentinel)
		}
		if written != 0 {
			t.Errorf("%q: reported %d bytes written through a failing sink", input, written)
		}
	}
}

func TestJSONWriterReportsAShortWriteAsAnError(t *testing.T) {
	sink := &shortWriter{}
	written, err := NewJSONWriter(sink).Write([]byte("{\"p\":\"evil\u009d\"}\n"))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("got error %v, want %v", err, io.ErrShortWrite)
	}
	if written != 0 {
		t.Errorf("reported %d bytes written after a short write", written)
	}
}

// indexRawC1 reports the first C1 control still present as the code point itself,
// in either form a repository can deliver one.
func indexRawC1(value string) int {
	for index := 0; index < len(value); {
		width, escape := escapedAt(value, index, jsonLayout)
		if escape {
			return index
		}
		index += width
	}
	return -1
}

// TestJSONLayoutMatchesTheTextRuleOnC1 keeps the two layouts from drifting: the
// range the machine formats escape must be the same range the text renderers
// already refuse, or a reader would be protected by one output mode and not the
// other. It also states what jsonLayout deliberately does NOT share — the C0 and
// layout rules, which belong to bytes nobody has encoded yet.
func TestJSONLayoutMatchesTheTextRuleOnC1(t *testing.T) {
	for point := 0x00; point <= 0xff; point++ {
		value := string(rune(point))
		_, textEscapes := escapedAt(value, 0, keepLayout)
		_, jsonEscapes := escapedAt(value, 0, jsonLayout)
		inC1 := point >= 0x80 && point <= 0x9f
		if jsonEscapes != inC1 {
			t.Errorf("U+%04X: jsonLayout escaped=%v, want %v", point, jsonEscapes, inC1)
		}
		if inC1 && !textEscapes {
			t.Errorf("U+%04X: keepLayout does not escape a C1 the machine formats do", point)
		}
	}
}
