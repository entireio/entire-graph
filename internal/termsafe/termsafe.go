// Package termsafe neutralizes terminal control sequences in output derived from
// repository content.
//
// Every human-readable verb in this tool prints bytes a scanned repository
// controls: pathnames from `git diff -z`, entity names parsed out of YAML, and
// whole source lines lifted verbatim into snippets. A Git pathname may hold any
// byte except NUL and '/', and a source file may hold any byte at all, so an ESC
// in a filename or a declaration reaches the reader's terminal and is INTERPRETED
// rather than displayed. That is a spoofing primitive: the repository, not the
// tool, decides what the reader believes was reported.
//
// The neutralization sits at the WRITER, not at each print site. The sinks are
// not a handful of auditable Fprintf calls — paths and source text are printed
// from roughly forty places across the search, def, impact, neighbors, and
// callsite renderers, and every renderer added later brings more. Wrapping the
// renderer's writer makes the property structural: a new print site cannot
// reintroduce the hole, because it cannot reach the terminal without passing
// through here.
//
// What survives untouched is chosen so ordinary output stays BYTE-IDENTICAL,
// because output stability is what lets an agent copy a snippet verbatim as an
// edit anchor:
//
//   - LF, TAB, FF and VT pass, being page whitespace the renderers and the
//     scanned sources themselves depend on.
//   - CR immediately followed by LF passes. Snippet lines are split on LF alone,
//     so a CRLF-authored file carries its CR into every printed line; escaping it
//     would put a visible marker at the end of every line of a Windows-authored
//     repository, for no security gain.
//   - A LONE CR is escaped, because carriage-return-without-newline is exactly
//     how an attacker overwrites a line the reader has already seen.
//   - Everything else in C0, DEL, and C1 is escaped to its Go literal form, so
//     the byte is shown rather than obeyed and nothing is silently dropped.
//
// JSON and NDJSON output is deliberately NOT wrapped: encoding/json already
// escapes control characters inside strings, so no raw byte reaches that stream,
// and a consumer of the machine formats is entitled to the exact bytes Git
// reported.
//
// One accepted cost: renderers that trim a payload to a byte budget do so before
// their writer is wrapped, so escaping can push a payload past its budget. Each
// escaped byte grows to at most six, and only input that already contains control
// bytes grows at all, so the overshoot is bounded and reachable only by the
// attacker it defends against.
package termsafe

import "io"

// Writer neutralizes terminal control sequences in everything written through it.
//
// It holds no state between calls: each Write is sanitized on its own, and the
// two lookahead rules (CRLF, and the two-byte UTF-8 form of C1) treat the end of
// a buffer as the end of the input. Renderers here always format a complete
// string before writing it, so a sequence never straddles a Write; were one to,
// the worst outcome is a single visibly escaped CR or a passed-through 0xc2,
// neither of which is a sequence a terminal acts on.
type Writer struct {
	out io.Writer
}

// NewWriter wraps a terminal-bound sink. Wrapping an already-wrapped writer is
// harmless: escaping is idempotent, since every byte it emits is printable ASCII
// that a second pass leaves alone.
func NewWriter(out io.Writer) *Writer {
	return &Writer{out: out}
}

// Write sanitizes p and writes it. It reports len(p) rather than the number of
// bytes actually written, because io.Writer's contract describes progress through
// the CALLER's buffer and a longer count would be read as a corrupt writer. The
// common case — output with nothing to escape — passes p through untouched and
// reports the underlying writer's own count.
func (w *Writer) Write(p []byte) (int, error) {
	if !needsEscape(p, keepLayout) {
		return w.out.Write(p)
	}
	safe := appendEscaped(make([]byte, 0, len(p)+escapeHeadroom), p, keepLayout)
	if _, err := w.out.Write(safe); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Line neutralizes one value that must occupy a single line: a file path, a
// symbol name, a one-line declaration — anything embedded in a record whose
// layout is "one per line".
//
// It escapes LF and TAB on top of what String escapes, because in that position
// they are not layout, they are forgery. A repository can name a file
//
//	a.go\n1. src/real.go:1 score=99.0
//
// and a renderer that passes the LF through prints a second entry the search
// never returned. The writer wrap cannot make this distinction — by the time
// bytes reach it, a snippet's newlines and a path's are the same byte — so
// values that go into single-line records are escaped here, at the point where
// the renderer still knows which is which.
func Line(value string) string {
	if !needsEscape(value, escapeLayout) {
		return value
	}
	return string(appendEscaped(make([]byte, 0, len(value)+escapeHeadroom), value, escapeLayout))
}

// Bytes neutralizes an already-rendered block that may legitimately span lines.
// It returns the input itself when there is nothing to escape, so the ordinary
// path neither copies nor allocates.
func Bytes(value []byte) []byte {
	if !needsEscape(value, keepLayout) {
		return value
	}
	return appendEscaped(make([]byte, 0, len(value)+escapeHeadroom), value, keepLayout)
}

// layout says whether LF, TAB, and CRLF are this value's own structure (a source
// snippet, a rendered block) or content it must not be allowed to fake (a path or
// a name inside a one-line record).
type layout bool

const (
	keepLayout   layout = true
	escapeLayout layout = false
)

// escapeHeadroom is the slack the output buffer starts with. It is a guess at how
// much a hostile string grows, not a bound; append handles the rest.
const escapeHeadroom = 16

const hexDigits = "0123456789abcdef"

// text is the shape both entry points share. Sanitizing reads by index and never
// slices, so one implementation serves strings and byte slices without either
// caller converting — and a conversion here would copy every payload the tool
// prints.
type text interface {
	~string | ~[]byte
}

// escapedAt reports how the byte at index i must be treated: keep it (0), escape
// just it (1), or escape it together with the byte that follows (2). Returning
// the width is what lets the scan and the rewrite share one copy of the rules.
func escapedAt[T text](data T, i int, keep layout) int {
	character := data[i]
	switch {
	case character == '\n' || character == '\t' || character == '\f' || character == '\v':
		// FF and VT ride along with LF and TAB because they are page whitespace,
		// not a cursor primitive: a terminal treats both as an index (move down a
		// line) and neither can reposition horizontally, restyle, or hide text.
		// They have to pass, because a form feed is how GNU-style C, Emacs Lisp
		// and older Perl separate pages — escaping it would rewrite the bytes of
		// every such file's snippet and break the verbatim edit anchor this
		// package promises to preserve.
		if keep {
			return 0
		}
		return 1
	case character == '\r':
		// CRLF is the line ending of a Windows-authored file, not an overwrite.
		if keep && i+1 < len(data) && data[i+1] == '\n' {
			return 0
		}
		return 1
	case character < 0x20 || character == 0x7f:
		// C0 and DEL. ESC (0x1b) is the sequence introducer that matters most,
		// but BEL, backspace, and the cursor controls all rewrite what is seen.
		return 1
	case character == 0xc2 && i+1 < len(data) && data[i+1] >= 0x80 && data[i+1] <= 0x9f:
		// The C1 controls in their two-byte UTF-8 form. U+009B is CSI, which a
		// terminal in UTF-8 mode acts on exactly as it acts on ESC followed by
		// '['; escaping only C0 would leave that introducer reachable.
		return 2
	default:
		return 0
	}
}

func needsEscape[T text](data T, keep layout) bool {
	for i := 0; i < len(data); i++ {
		if escapedAt(data, i, keep) > 0 {
			return true
		}
	}
	return false
}

// appendEscaped writes data to dst with every control byte replaced by the Go
// literal that denotes it, so a reader who meets the escape in a terminal or a
// log sees the byte written the way source would write it.
func appendEscaped[T text](dst []byte, data T, keep layout) []byte {
	for i := 0; i < len(data); i++ {
		switch escapedAt(data, i, keep) {
		case 1:
			dst = append(dst, '\\', 'x', hexDigits[data[i]>>4], hexDigits[data[i]&0x0f])
		case 2:
			// In the two-byte form the trailing byte IS the code point: 0xc2
			// contributes the 0x80 that its 0x00-0x1f payload is added to.
			point := data[i+1]
			dst = append(dst, '\\', 'u', '0', '0', hexDigits[point>>4], hexDigits[point&0x0f])
			i++
		default:
			dst = append(dst, data[i])
		}
	}
	return dst
}
