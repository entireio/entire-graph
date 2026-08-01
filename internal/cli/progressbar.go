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
	return info.Mode()&os.ModeCharDevice != 0
}

// progressBar renders indexing progress as a single self-overwriting line on a
// terminal. It is only wired up when the target is a TTY (see runIndex), so the
// ANSI clear-to-end-of-line (\033[K) and carriage-return redraws are safe, and
// stdout — which may carry JSON — is never touched.
type progressBar struct {
	w        io.Writer
	label    string
	rendered bool
}

func newProgressBar(w io.Writer, label string) *progressBar {
	return &progressBar{w: w, label: label}
}

// update redraws the bar for one progress event. With a known file total it
// shows a filled bar and a ratio; before the total is known (the "start" phase)
// it shows a running count.
func (b *progressBar) update(e sem.ProgressEvent) {
	elapsed := e.Elapsed.Round(100 * time.Millisecond)
	var line string
	if e.FilesTotal > 0 {
		const width = 24
		filled := e.FilesDone * width / e.FilesTotal
		if filled > width {
			filled = width
		}
		bar := repeat('█', filled) + repeat('░', width-filled)
		line = fmt.Sprintf("%s [%s] %d/%d files · %s symbols · %s relations · %s",
			b.label, bar, e.FilesDone, e.FilesTotal,
			humanInt(int64(e.Symbols)), humanInt(int64(e.Relations)), elapsed)
	} else {
		line = fmt.Sprintf("%s… %s symbols · %s relations · %s",
			b.label, humanInt(int64(e.Symbols)), humanInt(int64(e.Relations)), elapsed)
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
