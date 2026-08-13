package termsafe

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// escapeCases are the rules the package promises, stated once and driven through
// all three entry points. A rule that holds for String but not for Writer is a
// hole, since which one a sink uses is an implementation detail of the renderer.
var escapeCases = []struct {
	name string
	in   string
	want string
}{
	{"plain text is untouched", "internal/cli/search.go:42", "internal/cli/search.go:42"},
	{"newlines and tabs are layout, not control", "func f() {\n\treturn\n}", "func f() {\n\treturn\n}"},
	{"CRLF is a Windows line ending", "line one\r\nline two\r\n", "line one\r\nline two\r\n"},
	{"a lone CR overwrites the line already read", "real output\rfake output", `real output\x0dfake output`},
	{"CR at end of buffer has no LF to pair with", "trailing\r", `trailing\x0d`},
	{"ESC is the sequence introducer", "before\x1b[31mafter", `before\x1b[31mafter`},
	{"OSC can retitle the window or reach the clipboard", "\x1b]0;owned\x07", `\x1b]0;owned\x07`},
	{"BEL and backspace rewrite what was printed", "a\x07b\x08c", `a\x07b\x08c`},
	{"DEL is not printable", "a\x7fb", `a\x7fb`},
	{"C1 CSI in its two-byte UTF-8 form is an ESC-[ equivalent", "x\u009b31mred", `x\u009b31mred`},
	{"the low end of C1 escapes too", "x\u0080y", `x\u0080y`},
	{"0xc2 not introducing C1 is an ordinary lead byte", "caf\u00e9 \u00c2\u00a0 break", "caf\u00e9 \u00c2\u00a0 break"},
	{"NUL is a control byte like any other", "a\x00b", `a\x00b`},
	{"FF is a page separator in GNU-style source", "top\n\fbottom", "top\n\fbottom"},
	{"VT is page whitespace too", "a\vb", "a\vb"},
	{"multi-byte runes survive intact", "héllo → wörld 日本語", "héllo → wörld 日本語"},
}

func TestBytesEscapesControlSequences(t *testing.T) {
	t.Parallel()
	for _, testCase := range escapeCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := string(Bytes([]byte(testCase.in))); got != testCase.want {
				t.Errorf("Bytes(%q) = %q, want %q", testCase.in, got, testCase.want)
			}
		})
	}
}

func TestWriterEscapesControlSequences(t *testing.T) {
	t.Parallel()
	for _, testCase := range escapeCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			written, err := NewWriter(&buffer).Write([]byte(testCase.in))
			if err != nil {
				t.Fatal(err)
			}
			// The count describes the caller's buffer, not the sink's, so a
			// renderer that checks it against len(p) never sees a short write.
			if written != len(testCase.in) {
				t.Errorf("Write reported %d bytes, want %d", written, len(testCase.in))
			}
			if got := buffer.String(); got != testCase.want {
				t.Errorf("Write(%q) wrote %q, want %q", testCase.in, got, testCase.want)
			}
		})
	}
}

// TestLineEscapesLayoutBytes covers the stricter variant. In a one-line record a
// newline is not layout, it is a way to print a record the tool never produced.
func TestLineEscapesLayoutBytes(t *testing.T) {
	t.Parallel()
	forgery := "a.go\n1. src/real.go:1 score=99.0"
	got := Line(forgery)
	if strings.ContainsAny(got, "\n\r\t") {
		t.Errorf("Line(%q) = %q, want no layout bytes", forgery, got)
	}
	if want := `a.go\x0a1. src/real.go:1 score=99.0`; got != want {
		t.Errorf("Line(%q) = %q, want %q", forgery, got, want)
	}
	if got := Line("col\tumn"); got != `col\x09umn` {
		t.Errorf("Line did not escape a tab: %q", got)
	}
	// Page whitespace passes in keep-layout mode because it is real source
	// formatting, but in a one-line record it still moves the reader down a line,
	// so Line escapes it.
	if got := Line("page\fbreak"); got != `page\x0cbreak` {
		t.Errorf("Line did not escape a form feed: %q", got)
	}
	// Everything String escapes, Line escapes too.
	for _, testCase := range escapeCases {
		if strings.ContainsAny(Line(testCase.in), "\x1b") {
			t.Errorf("Line(%q) left a control sequence: %q", testCase.in, Line(testCase.in))
		}
	}
	// And a value with no layout bytes is left exactly as String leaves it.
	if got, want := Line("internal/cli/search.go"), "internal/cli/search.go"; got != want {
		t.Errorf("Line rewrote a clean path: %q", got)
	}
}

// TestOutputWithoutControlBytesIsByteIdentical is the compatibility half of the
// contract. Agents copy printed snippets verbatim as edit anchors, so ordinary
// source must come through unchanged — an escape that "helpfully" rewrote a tab
// or a Unicode identifier would break every anchor it touched.
func TestOutputWithoutControlBytesIsByteIdentical(t *testing.T) {
	t.Parallel()
	source := "func (s textStyles) render(code, value string) string {\n" +
		"\tif !s.color || value == \"\" {\n\t\treturn value\n\t}\n}\n" +
		"// naïve — 日本語 — \"quoted\" 'single' `back` $shell\r\n"
	if got := Bytes([]byte(source)); !bytes.Equal(got, []byte(source)) {
		t.Errorf("Bytes rewrote clean source:\ngot  %q\nwant %q", got, source)
	}
}

// TestEscapingIsIdempotent is what lets a wrapped writer be wrapped again — which
// happens whenever one wrapped renderer delegates to another.
func TestEscapingIsIdempotent(t *testing.T) {
	t.Parallel()
	for _, testCase := range escapeCases {
		once := string(Bytes([]byte(testCase.in)))
		if twice := string(Bytes([]byte(once))); twice != once {
			t.Errorf("Bytes(Bytes(%q)) = %q, want %q", testCase.in, twice, once)
		}
	}
}

// TestWriterPassesThroughUnderlyingError keeps a failed write a failed write: a
// renderer that ignored the error would report success on a closed pipe.
func TestWriterPassesThroughUnderlyingError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("sink closed")
	writer := NewWriter(failingWriter{err: sentinel})
	if _, err := writer.Write([]byte("clean text")); !errors.Is(err, sentinel) {
		t.Errorf("clean path returned %v, want %v", err, sentinel)
	}
	if _, err := writer.Write([]byte("hostile \x1b[2J")); !errors.Is(err, sentinel) {
		t.Errorf("escaped path returned %v, want %v", err, sentinel)
	}
}

// TestWriterEscapesAcrossSeparateWrites documents the stateless boundary. Each
// Write is sanitized on its own, so a renderer that formats a complete line per
// call — which every renderer here does — gets the same result as one big write.
func TestWriterEscapesAcrossSeparateWrites(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	writer := NewWriter(&buffer)
	for _, chunk := range []string{"1. src/a.go:1\n", "func \x1b[31mf\x1b[0m() {\n", "}\n"} {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if strings.ContainsRune(buffer.String(), 0x1b) {
		t.Errorf("ESC survived a chunked write: %q", buffer.String())
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }
