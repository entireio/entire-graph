package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/entireio/cli/cmd/entire/cli/palette"
	"github.com/entireio/entire-graph/internal/termsafe"
)

// setup_ui.go renders setup's settled rows in the style entire-brain's setup
// uses: a state glyph in column 0, a padded phase-title column, then the text.
// Styling follows the conventions the impact progress UI already follows — the
// Entire CLI palette rather than a competing color set, color only at a
// terminal that has not opted out, and ASCII glyph fallbacks when the locale
// cannot draw the Unicode ones. Under a pipe the rows are the same bytes minus
// the color, so transcripts stay assertable.

// setupMark is the per-row outcome glyph.
type setupMark int

const (
	setupMarkDone setupMark = iota
	setupMarkSkipped
)

// setupTitleWidth fits the widest phase title, "agent instructions".
const setupTitleWidth = len("agent instructions")

type setupUI struct {
	out     io.Writer
	color   bool
	unicode bool
}

func newSetupUI(out io.Writer) *setupUI {
	color := isTerminal(out) &&
		os.Getenv("NO_COLOR") == "" && os.Getenv("ACCESSIBLE") == "" &&
		os.Getenv("TERM") != "dumb" && os.Getenv("TERM") != "cygwin"
	return &setupUI{out: out, color: color, unicode: localeDrawsUnicode(os.Getenv)}
}

// localeDrawsUnicode mirrors brain's capability check: a POSIX locale naming
// UTF-8 wins, and Windows Terminal / the VS Code terminal are UTF-8 without
// ever setting one. A hard-coded "✓" on any other terminal renders as a
// replacement box.
func localeDrawsUnicode(getenv func(string) string) bool {
	for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if value := getenv(key); value != "" {
			lowered := strings.ToLower(value)
			return strings.Contains(lowered, "utf-8") || strings.Contains(lowered, "utf8")
		}
	}
	return strings.TrimSpace(getenv("WT_SESSION")) != "" ||
		strings.EqualFold(strings.TrimSpace(getenv("TERM_PROGRAM")), "vscode")
}

func (u *setupUI) glyph(mark setupMark) string {
	var plain, ascii, color string
	switch mark {
	case setupMarkSkipped:
		plain, ascii, color = "−", "-", palette.Muted
	default:
		plain, ascii, color = "✓", "+", palette.Success
	}
	if !u.unicode {
		plain = ascii
	}
	if !u.color {
		return plain
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true).Render(plain)
}

// Row writes one permanent row. Text is escaped by value: it carries
// repository-controlled paths, and this sink is a terminal.
func (u *setupUI) Row(mark setupMark, title, text string) {
	pad := strings.Repeat(" ", max(0, setupTitleWidth-len(title)))
	fmt.Fprintln(u.out, u.glyph(mark)+" "+title+pad+" "+termsafe.Line(text))
}

// Intro opens the run the way brain's setup does: a blank line, one sentence
// saying what the product is for, another blank line, and then the rows.
func (u *setupUI) Intro(sentence string) {
	fmt.Fprintln(u.out)
	fmt.Fprintln(u.out, sentence)
	fmt.Fprintln(u.out)
}

// End closes the run with the blank line that separates it from the prompt.
func (u *setupUI) End() {
	fmt.Fprintln(u.out)
}
