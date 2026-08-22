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
// JSON and NDJSON output is wrapped too, but by JSONWriter and for the C1 range
// only. This package shipped asserting the opposite — that encoding/json escapes
// the control characters inside strings, so no raw byte can reach a machine
// format and none of them needs wrapping. That is true of C0 and FALSE of C1:
// encoding/json escapes below U+0020 and folds invalid UTF-8 to U+FFFD, but
// U+0080-U+009F encode to a WELL-FORMED two-byte sequence it copies through
// verbatim. A repository that names a file with U+009D (OSC) and U+009C (ST)
// therefore reaches the reader's terminal with the pair that brackets an OSC 52
// clipboard write, through `entire graph search` with no --format flag at all —
// json is that verb's default. The escape JSONWriter writes is LOSSLESS: \u009d
// decodes to the same code point, so a consumer of the machine formats still
// receives the exact bytes Git reported.
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
//
// A short write with no error is a contract violation by the sink, not a state
// this writer can be in — but reporting len(p) for it would turn truncated
// output into a successful write, and truncated output is precisely how a
// half-written escape stops being an escape. It is reported as io.ErrShortWrite
// instead.
func (w *Writer) Write(p []byte) (int, error) {
	return writeEscaped(w.out, p, keepLayout)
}

// JSONWriter neutralizes the one control an encoded JSON document still carries
// raw, and rewrites nothing else.
//
// It wraps the SINK an encoder writes to rather than the values going into the
// encoder, for the reason Writer does: the machine formats are emitted from
// json.Encoder and CompactSnapshotEncoder instances created in a dozen verbs, and
// a rule enforced at each of them is a rule the next verb forgets. Wrapping the
// sink makes it structural.
//
// What it rewrites is U+0080-U+009F, and only that. The escape it writes is the
// JSON escape \u00XX, which decodes to the identical code point, so the stream
// stays a valid JSON document carrying the exact value the repository holds — no
// consumer sees a different string, and none has to know this wrapper exists.
//
// It DOES hold state between calls, unlike Writer: a trailing 0xc2 that has not
// yet met its continuation byte is withheld rather than judged as an orphan,
// because io.Writer's contract describes progress through the caller's buffer
// and makes no promise about where one Write ends and the next begins.
// json.Encoder.Encode happens to write each encoded value to its sink in a
// single Write (measured: one 200 KB value, one Write) and a complete, valid
// JSON value never itself ends on an unterminated UTF-8 sequence, so no current
// caller in this codebase ever leaves anything pending — but a caller who does
// split a stream, present or future, gets the one-shot answer either way. Were
// this writer stateless like Writer, a split would pass the leading 0xc2 through
// raw and then read the genuine continuation byte on its own in the next call:
// U+0080-U+009F misread as a bare byte is exactly the C1 range this writer
// exists to escape, so it would escape that stray byte as if it were a raw
// control the repository had planted alone — turning one legitimate two-byte
// character into a spurious \u00XX escape, and leaving the orphaned 0xc2 as an
// invalid UTF-8 byte in what is supposed to be a valid JSON document.
type JSONWriter struct {
	out io.Writer
	// pendingLead is true when the previous Write ended on a bare 0xc2 whose
	// continuation byte had not arrived yet. Write resolves it against the
	// next call's first byte before scanning the rest of that call's data.
	pendingLead bool
}

// NewJSONWriter wraps the sink of a machine-format encoder.
func NewJSONWriter(out io.Writer) *JSONWriter {
	return &JSONWriter{out: out}
}

// Write sanitizes p and writes it, reporting progress through the CALLER's buffer
// exactly as Writer.Write does and for the same reasons.
func (w *JSONWriter) Write(p []byte) (int, error) {
	total := len(p)
	rest := p
	if w.pendingLead {
		w.pendingLead = false
		if len(rest) == 0 {
			// Nothing arrived to resolve it with; keep waiting.
			w.pendingLead = true
			return total, nil
		}
		// Judge the withheld 0xc2 together with the byte that follows it,
		// through the SAME rule a same-call scan applies, so a completed C1
		// escape, a completed ordinary two-byte character, and a true orphan
		// are told apart exactly as they would be had both bytes arrived
		// together.
		pair := [2]byte{0xc2, rest[0]}
		width, escape := escapedAt(pair[:], 0, jsonLayout)
		if err := flushEscaped(w.out, appendEscapedAt(nil, pair[:], 0, width, escape)); err != nil {
			return 0, err
		}
		if width == 2 {
			// rest[0] was consumed as the pair's second byte, either escaped
			// (a completed C1 control) or copied (an ordinary character).
			rest = rest[1:]
		}
		// width == 1 means the 0xc2 was written alone, an orphan with no
		// valid continuation at all; rest[0] is unconsumed and scanned below
		// exactly as any other byte in this call is.
	}
	// A trailing 0xc2 might be the lead of a sequence this call's OWN boundary
	// cuts off, indistinguishable here from a true orphan; withhold it rather
	// than resolve it now, and let the next Write judge it against whatever
	// arrives.
	withheldLead := len(rest) > 0 && rest[len(rest)-1] == 0xc2
	if withheldLead {
		rest = rest[:len(rest)-1]
	}
	if _, err := writeEscaped(w.out, rest, jsonLayout); err != nil {
		return 0, err
	}
	w.pendingLead = withheldLead
	return total, nil
}

// Close flushes a 0xc2 withheld by Write that never received its continuation
// byte, writing it exactly as an unsplit Write already would: raw, since a bare
// 0xc2 is an incomplete UTF-8 sequence no terminal acts on.
//
// No caller in this codebase needs it — see the type doc for why a complete
// JSON value never leaves anything pending — but a caller who deliberately
// chunks a byte stream through this writer and stops before its final chunk's
// trailing 0xc2 is resolved must call it to avoid losing that byte.
func (w *JSONWriter) Close() error {
	if !w.pendingLead {
		return nil
	}
	w.pendingLead = false
	_, err := w.out.Write([]byte{0xc2})
	return err
}

// writeEscaped is the write path both writers share.
//
// The escaped bytes are flushed in bounded pieces rather than built as one buffer
// the size of p, because p is not always a rendered line. The provider-record
// cache hit in internal/cli/root.go hands this a single []byte holding the whole
// decompressed snapshot, and the search replay paths in internal/cli/search.go
// hand it a whole stored payload; a full-size copy of one of those doubles the
// peak footprint of the largest thing the tool holds in memory, on input the
// repository controls — one C1 byte anywhere in a cached snapshot is enough to
// take the no-copy fast path away. The flush buffer never grows past
// escapedFlushCapacity(len(p)): one position contributes at most six bytes and
// the headroom exceeds that, so a stream at or above escapeFlushBytes costs
// escapeFlushBytes and nothing more — but a SHORT write costs only what that
// write could possibly escape to, not a flat escapeFlushBytes regardless of
// size. A hostile C1 byte repeated across many small symbol and relation
// records used to allocate the full escapeFlushBytes buffer for every one of
// them; sizing to the smaller of the two bounds turns tens of thousands of
// oversized allocations into ones proportional to what each record needs.
//
// The SCAN still runs over the whole input, and that is the part that must not be
// chunked. Both lookahead rules read the byte after the one being judged — CRLF
// passes only as a pair, and a C1 control is recognised from its two-byte form —
// so escaping one bounded window at a time would judge the last byte of every
// window against the end of that window instead of against the real next byte.
// Only the output is in pieces, which is why the result is byte-for-byte the
// one-shot escape (TestFlushBoundariesDoNotChangeTheEscaping).
func writeEscaped(out io.Writer, p []byte, keep layout) (int, error) {
	if !needsEscape(p, keep) {
		return out.Write(p)
	}
	buffer := make([]byte, 0, escapedFlushCapacity(len(p)))
	for i := 0; i < len(p); {
		width, escape := escapedAt(p, i, keep)
		buffer = appendEscapedAt(buffer, p, i, width, escape)
		i += width
		if len(buffer) < escapeFlushBytes {
			continue
		}
		if err := flushEscaped(out, buffer); err != nil {
			return 0, err
		}
		buffer = buffer[:0]
	}
	if err := flushEscaped(out, buffer); err != nil {
		return 0, err
	}
	return len(p), nil
}

// flushEscaped writes one piece of the escaped stream, refusing a short write for
// the reason Writer.Write documents.
func flushEscaped(out io.Writer, buffer []byte) error {
	if len(buffer) == 0 {
		return nil
	}
	written, err := out.Write(buffer)
	if err != nil {
		return err
	}
	if written != len(buffer) {
		return io.ErrShortWrite
	}
	return nil
}

// Line neutralizes one value that must occupy a single line: a file path, a
// symbol name, a one-line declaration — anything embedded in a record whose
// layout is "one per line".
//
// It escapes LF and TAB on top of what Writer and Bytes escape, because in that position
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

// EscapesLine reports whether Line would rewrite value.
//
// It exists for the one caller that has to decide something BEFORE rendering:
// the VERIFY deriver, whose command must be runnable exactly as printed and so
// must not be emitted at all when display would rewrite it. That decision has to
// be made against the same rule the renderer applies, not a copy of it — a copy
// that checked only C0 would emit a command whose C1 byte the renderer then
// escaped, printing a line that is neither honest about being unprintable nor
// runnable as shown.
func EscapesLine(value string) bool {
	return needsEscape(value, escapeLayout)
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

// layout says what the value being scanned IS, which is what decides whether a
// given byte is its own structure or content it must not be allowed to fake.
type layout uint8

const (
	// keepLayout: a source snippet or an already-rendered block, whose LF, TAB and
	// CRLF are its own page structure.
	keepLayout layout = iota
	// escapeLayout: a path or a name going into a one-line record, where an LF is
	// not layout but forgery.
	escapeLayout
	// jsonLayout: an already-ENCODED JSON document, where every byte below 0x80 is
	// either the document's own syntax or an escape encoding/json has already
	// chosen, and the C1 range is the only raw control left to neutralize.
	jsonLayout
)

// escapeHeadroom is the slack the output buffer starts with. For the value
// helpers it is a guess at how much a hostile string grows, not a bound, and
// append handles the rest. For writeEscaped's flush buffer it IS the bound: that
// buffer is flushed as soon as it reaches escapeFlushBytes, and one position
// contributes at most six bytes, so 16 bytes of slack is never exhausted.
const escapeHeadroom = 16

// escapeFlushBytes is how much escaped output writeEscaped accumulates before
// handing it to the sink. It is the whole memory cost of escaping a stream, so it
// is sized to amortize the write syscall rather than to fit any payload — 32 KiB
// is what io.Copy uses for the same trade.
const escapeFlushBytes = 32 << 10

// escapedFlushCapacity is how large writeEscaped's flush buffer should start,
// for an input of inputBytes. It is the smaller of two independent bounds: the
// flush limit above, which caps the memory cost of an arbitrarily large input,
// and inputBytes*6 + escapeHeadroom, the most this particular input could ever
// escape to (every byte escapes to at most six, per appendEscapedAt) plus the
// same slack the flush buffer always carries. Below escapeFlushBytes an input
// cannot fill the flush buffer at all, so allocating the flush buffer's full
// size for it is pure waste — and a repository-controlled record repeated many
// times over is exactly the shape that turns that waste into a resource-
// exhaustion cost paid once per record.
func escapedFlushCapacity(inputBytes int) int {
	if worst := inputBytes*6 + escapeHeadroom; worst < escapeFlushBytes+escapeHeadroom {
		return worst
	}
	return escapeFlushBytes + escapeHeadroom
}

const hexDigits = "0123456789abcdef"

// text is the shape both entry points share. Sanitizing reads by index and never
// slices, so one implementation serves strings and byte slices without either
// caller converting — and a conversion here would copy every payload the tool
// prints.
type text interface {
	~string | ~[]byte
}

// runeWidthAt reports how many bytes form a VALID UTF-8 sequence starting at i,
// or 0 when the byte there begins no valid sequence.
//
// The scan needs this to tell a stray C1 byte from a continuation byte that only
// looks like one: the middle byte of U+65E5 (0xe6 0x97 0xa5) is 0x97, squarely
// inside the C1 range, and a per-byte scan would mangle every CJK identifier it
// met. Knowing the width lets the scan step OVER a valid sequence instead of
// inspecting its interior.
//
// "Valid" here means WELL-FORMED, not merely well-shaped, and the second byte's
// range is where the two differ. Accepting any continuation byte would let the
// overlong form 0xe0 0x80 0x9b carry a CSI byte through as the interior of a
// three-byte rune: a decoder that rejects overlongs shows a replacement
// character, but one that does not decodes those three bytes to U+001B — the
// classic overlong smuggling of an ASCII control. This package escapes a stray
// C1 byte precisely because it cannot assume how the terminal decodes; treating
// an ill-formed sequence as a rune to step over would assume exactly that.
func runeWidthAt[T text](data T, i int) int {
	lead := data[i]
	switch {
	case lead < 0x80:
		return 1
	case lead < 0xc2:
		// A continuation byte with no lead, or an overlong two-byte form.
		return 0
	case lead < 0xe0:
		return sequenceWidth(data, i, 2, 0x80, 0xbf)
	case lead == 0xe0:
		// Below 0xa0 the code point would fit the two-byte form: overlong.
		return sequenceWidth(data, i, 3, 0xa0, 0xbf)
	case lead == 0xed:
		// 0xed 0xa0.. and up is a surrogate half, which UTF-8 does not encode.
		return sequenceWidth(data, i, 3, 0x80, 0x9f)
	case lead < 0xf0:
		return sequenceWidth(data, i, 3, 0x80, 0xbf)
	case lead == 0xf0:
		// Below 0x90 the code point would fit the three-byte form.
		return sequenceWidth(data, i, 4, 0x90, 0xbf)
	case lead == 0xf4:
		// Above U+10FFFF there is nothing to encode.
		return sequenceWidth(data, i, 4, 0x80, 0x8f)
	case lead < 0xf4:
		return sequenceWidth(data, i, 4, 0x80, 0xbf)
	default:
		return 0
	}
}

// sequenceWidth reports width when the bytes after the lead are continuations and
// the SECOND one falls in the range that lead admits, or 0 otherwise. Only the
// second byte's range varies by lead: it carries the code point's high bits, so
// it is the byte that decides whether a sequence is overlong, a surrogate half,
// or past the last code point.
func sequenceWidth[T text](data T, i, width int, secondLow, secondHigh byte) int {
	if i+width > len(data) {
		return 0
	}
	if data[i+1] < secondLow || data[i+1] > secondHigh {
		return 0
	}
	for offset := 2; offset < width; offset++ {
		if data[i+offset] < 0x80 || data[i+offset] > 0xbf {
			return 0
		}
	}
	return width
}

// escapedAt reports how the sequence at index i must be treated: its width in
// bytes, and whether those bytes are escaped rather than copied. Returning the
// width is what lets the scan and the rewrite share one copy of the rules, and
// what lets both step over a multi-byte rune without reading its interior.
func escapedAt[T text](data T, i int, keep layout) (int, bool) {
	character := data[i]
	switch {
	case keep == jsonLayout && character < 0x80:
		// Nothing below 0x80 survives encoding as a control: the C0 range is already
		// a \uXXXX escape, and every remaining ASCII byte is the document's own
		// syntax. Rewriting any of it would corrupt the JSON rather than defend it,
		// so the scan skips straight to the range encoding/json left raw.
		return 1, false
	case character == '\n' || character == '\t' || character == '\f' || character == '\v':
		// FF and VT ride along with LF and TAB because they are page whitespace,
		// not a cursor primitive: a terminal treats both as an index (move down a
		// line) and neither can reposition horizontally, restyle, or hide text.
		// They have to pass, because a form feed is how GNU-style C, Emacs Lisp
		// and older Perl separate pages — escaping it would rewrite the bytes of
		// every such file's snippet and break the verbatim edit anchor this
		// package promises to preserve.
		return 1, keep != keepLayout
	case character == '\r':
		// CRLF is the line ending of a Windows-authored file, not an overwrite.
		if keep == keepLayout && i+1 < len(data) && data[i+1] == '\n' {
			return 1, false
		}
		return 1, true
	case character < 0x20 || character == 0x7f:
		// C0 and DEL. ESC (0x1b) is the sequence introducer that matters most,
		// but BEL, backspace, and the cursor controls all rewrite what is seen.
		return 1, true
	case character < 0x80:
		return 1, false
	}
	// Above ASCII the byte is only safe to judge in UTF-8 terms.
	width := runeWidthAt(data, i)
	switch {
	case width == 2 && character == 0xc2 && data[i+1] >= 0x80 && data[i+1] <= 0x9f:
		// The C1 controls in their two-byte UTF-8 form. U+009B is CSI, which a
		// terminal in UTF-8 mode acts on exactly as it acts on ESC followed by
		// '['; escaping only C0 would leave that introducer reachable.
		return 2, true
	case width > 0:
		return width, false
	case character <= 0x9f:
		// A STRAY C1 byte — one that begins no valid sequence. A Git pathname is
		// a byte string, not text, so 0x9b can arrive raw, and a terminal in an
		// 8-bit locale reads that single byte as CSI. Only 0x80-0x9f is escaped
		// here: stray bytes above it are not controls in any encoding, and
		// rewriting them would corrupt Latin-1-encoded sources for no gain.
		return 1, true
	default:
		return 1, false
	}
}

func needsEscape[T text](data T, keep layout) bool {
	for i := 0; i < len(data); {
		width, escape := escapedAt(data, i, keep)
		if escape {
			return true
		}
		i += width
	}
	return false
}

// appendEscaped writes data to dst with every control byte replaced by the Go
// literal that denotes it, so a reader who meets the escape in a terminal or a
// log sees the byte written the way source would write it.
func appendEscaped[T text](dst []byte, data T, keep layout) []byte {
	for i := 0; i < len(data); {
		width, escape := escapedAt(data, i, keep)
		dst = appendEscapedAt(dst, data, i, width, escape)
		i += width
	}
	return dst
}

// appendEscapedAt appends the one sequence escapedAt just judged, verbatim or as
// its escape. It is separate from the loop above so that the streaming write path
// emits the identical bytes from the identical code: two copies of these four
// cases would be two things to keep in step, and the escape FORM is what every
// consumer of the machine formats decodes.
//
// It appends at most six bytes, which is what bounds writeEscaped's flush buffer.
func appendEscapedAt[T text](dst []byte, data T, i, width int, escape bool) []byte {
	switch {
	case !escape:
		for offset := 0; offset < width; offset++ {
			dst = append(dst, data[i+offset])
		}
	case width == 2:
		// In the two-byte form the trailing byte IS the code point: 0xc2
		// contributes the 0x80 that its 0x00-0x1f payload is added to.
		point := data[i+1]
		dst = append(dst, '\\', 'u', '0', '0', hexDigits[point>>4], hexDigits[point&0x0f])
	case data[i] >= 0x80:
		// A stray C1 byte denotes the code point of the same value.
		dst = append(dst, '\\', 'u', '0', '0', hexDigits[data[i]>>4], hexDigits[data[i]&0x0f])
	default:
		dst = append(dst, '\\', 'x', hexDigits[data[i]>>4], hexDigits[data[i]&0x0f])
	}
	return dst
}
