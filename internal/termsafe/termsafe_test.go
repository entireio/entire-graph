package termsafe

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

// escapeCases are the rules the package promises, stated once and driven through
// all three entry points. A rule that holds for Bytes but not for Writer is a
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
	{"a STRAY C1 byte is CSI to an 8-bit terminal", "x\x9b31mred", `x\u009b31mred`},
	{"a stray byte above C1 is not a control in any encoding", "caf\xe9 latin-1", "caf\xe9 latin-1"},
	{"continuation bytes inside a valid rune are not stray C1", "\u65e5\u672c\u8a9e", "\u65e5\u672c\u8a9e"},
	{"a truncated lead byte is left alone when it is not C1", "ok\xf0", "ok\xf0"},
	{"multi-byte runes survive intact", "héllo → wörld 日本語", "héllo → wörld 日本語"},
	// An ill-formed sequence is not a rune to step over. Each of these carries a
	// C1 byte in a position that LOOKS like the interior of a longer rune, which is
	// the shape that would smuggle it past a scan that only checked whether the
	// following bytes were continuations. The lead byte itself stays: above 0x9f it
	// is not a control in any encoding.
	{"an overlong three-byte form hides a CSI byte", "x\xe0\x80\x9by", "x\xe0" + `\u0080\u009b` + "y"},
	{"an overlong two-byte form hides a C1 byte", "x\xc0\x80y", "x\xc0" + `\u0080` + "y"},
	{"a surrogate half is not a rune either", "x\xed\xa0\x80y", "x\xed\xa0" + `\u0080` + "y"},
	{"a four-byte form past U+10FFFF is not a rune", "x\xf4\x90\x80\x80y", "x\xf4" + `\u0090\u0080\u0080` + "y"},
	// The other side of the same rule: the smallest and largest well-formed
	// sequence of each length must still be stepped over, or tightening the scan
	// would have mangled the runes at every boundary.
	{"the boundary rune of each length is still a rune",
		"\u00a0\u07ff\u0800\ud7ff\uffff\U00010000\U0010ffff",
		"\u00a0\u07ff\u0800\ud7ff\uffff\U00010000\U0010ffff"},
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
	// Everything Bytes escapes, Line escapes too — in EITHER spelling a control
	// arrives in. Scanning for ESC alone would say nothing about the raw and
	// overlong C1 cases above, which are the ones that reached this package by
	// being missed.
	for _, testCase := range escapeCases {
		if index := indexControl(Line(testCase.in)); index >= 0 {
			t.Errorf("Line(%q) left a control at byte %d: %q", testCase.in, index, Line(testCase.in))
		}
	}
	// And a value with no layout bytes is left exactly as Bytes leaves it.
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

// TestWriterReportsAShortWriteAsAnError covers the sink that writes part of the
// buffer and reports no error. That is a contract violation by the sink, but
// reporting len(p) for it would turn truncated output into a successful write —
// and truncation is exactly how a half-written escape stops being an escape, with
// the tail of the sanitized buffer never reaching the reader.
func TestWriterReportsAShortWriteAsAnError(t *testing.T) {
	t.Parallel()
	var sink shortWriter
	written, err := NewWriter(&sink).Write([]byte("hostile \x1b[2J tail"))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("short write returned (%d, %v), want io.ErrShortWrite", written, err)
	}
	// The clean path hands the sink's own count back, because there is nothing to
	// re-count: p and the bytes written are the same buffer.
	var clean shortWriter
	if written, err := NewWriter(&clean).Write([]byte("clean text")); written != 4 || err != nil {
		t.Errorf("clean path returned (%d, %v), want the sink's own (4, nil)", written, err)
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

// indexControl reports the first byte a terminal would act on, in every spelling
// a value can carry one: ESC, a decoded C1 code point, and an undecodable byte in
// the C1 range. Decoding rather than scanning bytes is what keeps it from firing
// on the continuation bytes of an ordinary rune — U+0800 is 0xe0 0xa0 0x80, whose
// last byte is 0x80.
func indexControl(value string) int {
	for index, character := range value {
		switch {
		case character == 0x1b:
			return index
		case character >= 0x80 && character <= 0x9f:
			return index
		case character == utf8.RuneError && value[index] >= 0x80 && value[index] <= 0x9f:
			return index
		}
	}
	return -1
}

// shortWriter accepts the first four bytes of anything and claims success, the
// shape io.Writer forbids and this package must not paper over.
type shortWriter struct{ got []byte }

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > 4 {
		p = p[:4]
	}
	w.got = append(w.got, p...)
	return len(p), nil
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }
