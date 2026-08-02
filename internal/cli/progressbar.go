package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/entireio/entire-graph/internal/sem"
)

// isTerminal reports whether w is an interactive terminal. It is dependency-free
// (no golang.org/x/term): a character device is a TTY, a pipe/file/bytes.Buffer
// is not. Used to decide whether to draw a live progress bar and whether to
// render a human summary instead of JSON.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	// /dev/null is a character device too, so the mode bit alone would call a
	// redirect to it "interactive" and pick text output for a caller that asked
	// for none. Name-check it rather than take a terminal dependency.
	if name := f.Name(); name == os.DevNull {
		return false
	}
	return true
}

// progressBar renders indexing progress as a single self-overwriting line on a
// terminal. It is only wired up when the target is a TTY (see runIndex), so the
// ANSI clear-to-end-of-line (\033[K) and carriage-return redraws are safe, and
// stdout — which may carry JSON — is never touched.
type progressBar struct {
	w        io.Writer
	label    string
	rendered bool
	tick     int
}

func newProgressBar(w io.Writer, label string) *progressBar {
	return &progressBar{w: w, label: label}
}

// spinnerFrames animates the indeterminate phases (relations linking, and the
// pre-total "start" tick) where there is no denominator to fill a bar with.
var spinnerFrames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

// update redraws the bar for one progress event. Parsing files has a known total,
// so it shows a real filled bar and ratio. The relations phase streams with no
// total (files are already at 100%), so a full bar there would look "done" while
// work continues — instead it shows a spinner and the climbing relation count.
func (b *progressBar) update(e sem.ProgressEvent) {
	b.tick++
	elapsed := e.Elapsed.Round(100 * time.Millisecond)
	spin := spinnerFrames[b.tick%len(spinnerFrames)]

	var line string
	switch e.Phase {
	case "summary":
		// Relations are already linked by this tick; saying otherwise makes the
		// bar read as stuck at the moment the run is actually finishing.
		line = fmt.Sprintf("%s %c finishing · %s relations · %s symbols · %s",
			b.label, spin, humanInt(int64(e.Relations)), humanInt(int64(e.Symbols)), elapsed)
	case "relations":
		line = fmt.Sprintf("%s %c linking relations · %s relations · %s symbols · %s",
			b.label, spin, humanInt(int64(e.Relations)), humanInt(int64(e.Symbols)), elapsed)
	default: // start, parse — file parsing, with a known total
		if e.FilesTotal > 0 {
			const width = 24
			filled := e.FilesDone * width / e.FilesTotal
			if filled > width {
				filled = width
			}
			bar := repeat('█', filled) + repeat('░', width-filled)
			line = fmt.Sprintf("%s [%s] %d/%d files · %s symbols · %s",
				b.label, bar, e.FilesDone, e.FilesTotal, humanInt(int64(e.Symbols)), elapsed)
		} else {
			line = fmt.Sprintf("%s %c parsing · %s files · %s symbols · %s",
				b.label, spin, humanInt(int64(e.FilesDone)), humanInt(int64(e.Symbols)), elapsed)
		}
	}
	fmt.Fprintf(b.w, "\r%s\033[K", line)
	b.rendered = true
}

// clear erases the bar line so the following output starts clean. It is a no-op
// when nothing was ever drawn (e.g. a cache hit that returns before any work).
func (b *progressBar) clear() {
	if !b.rendered {
		return
	}
	fmt.Fprint(b.w, "\r\033[K")
}

func repeat(r rune, n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]rune, n)
	for i := range out {
		out[i] = r
	}
	return string(out)
}
