package sem

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestSplitLineWindowMatchesStringsSplit checks the window against the
// whole-file split it replaces, for every index (in range, past the end and
// negative) over contents that end with and without a newline.
func TestSplitLineWindowMatchesStringsSplit(t *testing.T) {
	t.Parallel()
	contents := []string{"", "\n", "one", "one\n", "a\nb", "a\nb\n", "\n\n\n", "l0\r\nl1\r\nl2", strings.Repeat("x\n", 40), strings.Repeat("y\n", 40) + "tail"}
	for _, content := range contents {
		lines := strings.Split(content, "\n")
		for radius := 0; radius <= 9; radius++ {
			for index := -2; index <= len(lines)+12; index++ {
				want := index
				if want >= len(lines) {
					want = len(lines) - 1
				}
				window, at := splitLineWindow(content, index, radius)
				if want < 0 {
					if at >= 0 || len(window) != 0 {
						t.Fatalf("content %q index %d: got window %q at %d, want nothing at a negative index", content, index, window, at)
					}
					continue
				}
				if at < 0 || at >= len(window) || window[at] != lines[want] {
					t.Fatalf("content %q index %d radius %d: window %q at %d does not hold line %d %q", content, index, radius, window, at, want, lines[want])
				}
				low := want - at
				if wantLow := max(want-radius, 0); low != wantLow {
					t.Fatalf("content %q index %d radius %d: window starts at line %d, want %d", content, index, radius, low, wantLow)
				}
				wantHigh := min(want+radius, len(lines)-1)
				if high := low + len(window) - 1; high != wantHigh {
					t.Fatalf("content %q index %d radius %d: window ends at line %d, want %d", content, index, radius, high, wantHigh)
				}
				for i, line := range window {
					if line != lines[low+i] {
						t.Fatalf("content %q index %d radius %d: window line %d is %q, want %q", content, index, radius, low+i, line, lines[low+i])
					}
				}
			}
		}
	}
}

func decoratedHandlersSource(handlers int) (string, []SymbolRecord) {
	var b strings.Builder
	b.WriteString("export class Handlers {\n")
	symbols := make([]SymbolRecord, 0, handlers)
	line := 2
	for i := range handlers {
		fmt.Fprintf(&b, "  @MessagePattern('topic.%d')\n  handle%d(data: unknown) { return data; }\n", i, i)
		symbols = append(symbols, SymbolRecord{Name: fmt.Sprintf("handle%d", i), Kind: "method", StartLine: line + 1, EndLine: line + 1})
		line += 2
	}
	b.WriteString("}\n")
	return b.String(), symbols
}

// TestAnnotationScansDoNotSplitTheWholeFilePerSymbol pins the per-symbol
// whole-file split. Each scan looks at most annotationScanLines lines either
// side of a symbol, so its allocation must not grow with the file.
func TestAnnotationScansDoNotSplitTheWholeFilePerSymbol(t *testing.T) {
	content, symbols := decoratedHandlersSource(20000)
	var before, after runtime.MemStats
	found := 0
	finishesWithin(t, 30*time.Second, func() {
		runtime.GC()
		runtime.ReadMemStats(&before)
		for _, symbol := range symbols {
			found += len(nestJSMessagePatternChannelsAroundSymbol(content, symbol))
			found += len(nestJSMethodRouteLiteralsAroundSymbol(content, symbol))
		}
		runtime.ReadMemStats(&after)
	})
	if found != len(symbols) {
		t.Fatalf("found %d channels across %d decorated handlers, want one each", found, len(symbols))
	}
	perSymbol := (after.TotalAlloc - before.TotalAlloc) / uint64(len(symbols))
	if perSymbol > 8192 {
		t.Fatalf("annotation scans allocated %d bytes per symbol over a %d-line file, want a constant under 8192", perSymbol, strings.Count(content, "\n"))
	}
}

func TestAnnotationScansStillReadDecoratorsAtTheFileEdges(t *testing.T) {
	t.Parallel()
	content := "@MessagePattern('first')\nhandleFirst() {}\n\n@Get('/tail')\nlast() {}"
	if got := nestJSMessagePatternChannelsAroundSymbol(content, SymbolRecord{StartLine: 2}); len(got) != 1 || got[0] != "first" {
		t.Fatalf("decorator on line 1: got %q, want [first]", got)
	}
	if got := nestJSMethodRouteLiteralsAroundSymbol(content, SymbolRecord{StartLine: 5}); len(got) != 1 || got[0] != "/tail" {
		t.Fatalf("symbol on the last line: got %q, want [/tail]", got)
	}
	if got := nestJSMethodRouteLiteralsAroundSymbol(content, SymbolRecord{StartLine: 99}); len(got) != 1 || got[0] != "/tail" {
		t.Fatalf("symbol past the end clamps to the last line: got %q, want [/tail]", got)
	}
	// A leading string literal outranks a later path property; the decorator
	// regexes are tried in that order.
	ordered := "@Get('/first', { path: '/second' })\nhandler() {}"
	if got := nestJSMethodRouteLiteralsAroundSymbol(ordered, SymbolRecord{StartLine: 2}); len(got) != 1 || got[0] != "/first" {
		t.Fatalf("string literal before a path property: got %q, want [/first]", got)
	}
	if got := nestJSMethodRouteLiteralsAroundSymbol(content, SymbolRecord{StartLine: 0}); len(got) != 0 {
		t.Fatalf("symbol with no line: got %q, want none", got)
	}
}

// finishesWithin fails the test if fn has not returned before limit.
func finishesWithin(t *testing.T, limit time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("did not finish within %s", limit)
	}
}

// TestSplitLineWindowComputesLineStartsOncePerContent pins the memo: without
// it every per-symbol scan walks the file from the top to find its window,
// which keeps the S*L cost the window was introduced to remove.
func TestSplitLineWindowComputesLineStartsOncePerContent(t *testing.T) {
	t.Parallel()
	content := strings.Repeat("memo line\n", 64) + fmt.Sprint(time.Now().UnixNano())
	first := recentLineStarts.lookup(content)
	again := recentLineStarts.lookup(strings.Clone(content))
	if len(first) != 65 || len(again) != 65 || &first[0] != &again[0] {
		t.Fatalf("second lookup of the same content recomputed its line starts (%d then %d entries, shared=%v)", len(first), len(again), len(first) > 0 && len(again) > 0 && &first[0] == &again[0])
	}
}
