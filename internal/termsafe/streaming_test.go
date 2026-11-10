package termsafe

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

// countingSink accepts bytes and allocates nothing, so a measurement taken around
// a Write measures the writer rather than the sink. A bytes.Buffer would allocate
// the whole output as it grew and swamp the number under test.
type countingSink struct {
	bytes  int
	writes int
}

func (s *countingSink) Write(p []byte) (int, error) {
	s.bytes += len(p)
	s.writes++
	return len(p), nil
}

// The size the streaming assertions run at. Large enough that a whole-stream copy
// is unmistakable against the bound, small enough to stay a unit test.
const streamingTestBytes = 8 << 20

// TestWritersDoNotAllocateTheWholeStream is the resource half of the machine-format
// wrap.
//
// The provider-record cache hit in internal/cli/root.go replays one []byte holding
// the entire decompressed snapshot, and the search replay paths in
// internal/cli/search.go replay one stored payload the same way. Escaping by
// building a second buffer the size of the whole stream therefore doubles the peak
// footprint of the largest thing this tool holds in memory, and it does so on the
// input an attacker controls: one C1 byte anywhere in a cached snapshot is enough
// to take the no-copy fast path away.
//
// The scan still runs over the whole input — that is what keeps the CRLF and
// two-byte lookahead honest, and TestFlushBoundariesDoNotChangeTheEscaping pins
// the output — but the escaped bytes are flushed in bounded pieces, so what a
// Write allocates is a constant.
func TestWritersDoNotAllocateTheWholeStream(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		write func(sink *countingSink, payload []byte) (int, error)
	}{
		{"JSONWriter", func(sink *countingSink, payload []byte) (int, error) { return NewJSONWriter(sink).Write(payload) }},
		{"Writer", func(sink *countingSink, payload []byte) (int, error) { return NewWriter(sink).Write(payload) }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			payload := hostileStream(streamingTestBytes)
			sink := &countingSink{}

			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			written, err := testCase.write(sink, payload)
			runtime.ReadMemStats(&after)

			if err != nil {
				t.Fatalf("write: %v", err)
			}
			if written != len(payload) {
				t.Fatalf("reported %d bytes of a %d-byte payload", written, len(payload))
			}
			if sink.bytes <= len(payload) {
				t.Fatalf("sink received %d bytes for a %d-byte payload: the escapes did not happen, so this case proves nothing",
					sink.bytes, len(payload))
			}
			// A quarter of the input is far above what a bounded flush buffer costs and
			// far below the whole-stream copy this replaces, so the bound distinguishes
			// the two without pinning an implementation detail.
			allocated := after.TotalAlloc - before.TotalAlloc
			if limit := uint64(len(payload) / 4); allocated > limit {
				t.Errorf("escaping a %d-byte payload allocated %d bytes, want at most %d: the buffer scales with the stream",
					len(payload), allocated, limit)
			}
		})
	}
}

// TestFlushBoundariesDoNotChangeTheEscaping is what makes the bounded flush safe.
//
// Both lookahead rules read the byte AFTER the one being judged — CRLF passes only
// as a pair, and a C1 control is recognised from its two-byte form — so an
// implementation that escaped one bounded window at a time would judge the last
// byte of every window against the end of that window instead of against the real
// next byte, and a CR or a 0xc2 landing there would be escaped differently. The
// scan therefore stays over the whole input and only the output is flushed in
// pieces, which this pins by comparing against the one-shot escape of the same
// bytes.
func TestFlushBoundariesDoNotChangeTheEscaping(t *testing.T) {
	// Each payload is longer than one flush buffer and puts the bytes whose meaning
	// depends on the next one at every offset modulo a small stride, so some land on
	// a flush boundary wherever that boundary falls.
	for name, payload := range map[string][]byte{
		"C1 pairs":      bytes.Repeat([]byte("a\u009db\u009c"), streamingTestBytes/6),
		"CRLF pairs":    bytes.Repeat([]byte("line\r\n"), streamingTestBytes/6),
		"lone CR":       bytes.Repeat([]byte("over\rwrite"), streamingTestBytes/10),
		"stray C1":      bytes.Repeat([]byte{'a', 0x9b, 'b', 0xc2, 'c'}, streamingTestBytes/5),
		"wide runes":    bytes.Repeat([]byte("\u65e5\u672c\u8a9e"), streamingTestBytes/9),
		"hostile mix":   hostileStream(streamingTestBytes),
		"nothing to do": bytes.Repeat([]byte("{\"p\":\"plain\"}\n"), streamingTestBytes/14),
	} {
		for _, layoutCase := range []struct {
			name  string
			keep  layout
			write func(sink *bytes.Buffer, payload []byte) (int, error)
		}{
			{"JSONWriter", jsonLayout, func(sink *bytes.Buffer, payload []byte) (int, error) { return NewJSONWriter(sink).Write(payload) }},
			{"Writer", keepLayout, func(sink *bytes.Buffer, payload []byte) (int, error) { return NewWriter(sink).Write(payload) }},
		} {
			t.Run(name+"/"+layoutCase.name, func(t *testing.T) {
				var sink bytes.Buffer
				written, err := layoutCase.write(&sink, payload)
				if err != nil {
					t.Fatalf("write: %v", err)
				}
				if written != len(payload) {
					t.Fatalf("reported %d bytes of a %d-byte payload", written, len(payload))
				}
				want := appendEscaped(make([]byte, 0, len(payload)), payload, layoutCase.keep)
				if !bytes.Equal(sink.Bytes(), want) {
					t.Fatalf("flushed output differs from the one-shot escape of the same bytes (%d vs %d bytes, first difference at %d)",
						sink.Len(), len(want), firstDifference(sink.Bytes(), want))
				}
			})
		}
	}
}

// hostileStream is a payload the size of a cached snapshot with the shapes a
// repository can smuggle in scattered through it: both C1 code points, a stray C1
// byte, a lone CR, a CRLF, and multi-byte runes whose interior bytes fall inside
// the C1 range (0x97 is the middle byte of U+65E5).
func hostileStream(size int) []byte {
	unit := "{\"path\":\"evil\u009d52;c;aGVsbG8=\u009cfile.go\",\"line\":\"\u65e5\u672c\"}\r\nover\rwrite\n"
	stream := []byte(strings.Repeat(unit, size/len(unit)+1))
	return stream[:size]
}

func firstDifference(got, want []byte) int {
	for index := 0; index < min(len(got), len(want)); index++ {
		if got[index] != want[index] {
			return index
		}
	}
	return min(len(got), len(want))
}
