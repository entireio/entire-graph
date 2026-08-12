package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/entireio/entire-graph/internal/sem"
)

// A CALLS relation records, as evidence, the span of the CALLER'S BODY — not the
// line the call is written on. Reporting that span's first line as the caller's
// location is the single most expensive inaccuracy a relation answer can carry:
// on a long dispatch function it points thousands of lines away from the call,
// and the reader has to go find it. The definition line is still reported, but
// the primary location is now the call itself.
//
// The call line is recovered from the source rather than from the parser, so
// this stays entirely inside the relation-output layer: the caller's body span
// is known, the call expression's text is known (evidence detail), and the file
// is on disk. Everything below is deterministic and language-agnostic.

const (
	// callContextGuardLimit caps how many enclosing block headers are quoted.
	// Six covers the deepest real dispatch nesting seen in practice while
	// keeping the block far cheaper than the caller's body.
	callContextGuardLimit = 6
	// callContextWindowLines is the size of the verbatim window around a call.
	callContextWindowLines = 10
	// callContextBlocks caps how many callers get a source window. Reporting
	// the LINE is unconditional and costs ~4 bytes; quoting source is not.
	callContextBlocks = 3
	// callContextBudget is the total byte ceiling for all quoted call context in
	// one answer. A window is ~100x cheaper than the caller body it replaces, so
	// this is generous, but it must still be bounded.
	callContextBudget = 2000
	// callContextLineWidth truncates a quoted line. Machine-generated source can
	// carry multi-kilobyte lines that would blow the budget on one entry.
	callContextLineWidth = 160
	// callSiteMaxFileBytes bounds the file read behind call-site resolution.
	callSiteMaxFileBytes = 4 << 20
	// callSiteHeaderLookback is how far above an opening brace the statement
	// that owns it may start (`if cond\n  && other\n{`).
	callSiteHeaderLookback = 6
)

// callSite is the concrete location of a call inside a caller's body.
type callSite struct {
	FilePath string `json:"file_path"`
	Line     int    `json:"line"`
	// AdditionalSites counts further call sites of the same callee in the same
	// caller, so a reader knows one line is not the whole story.
	AdditionalSites int `json:"additional_sites,omitempty"`
	// Guards are the enclosing block headers in force at the call, outermost
	// first: the `if let` / `match` / `else if` chain a patch has to stay
	// correct under. Verbatim source lines, never a synthesized claim.
	Guards []sourceLine `json:"guards,omitempty"`
	// GuardsOmitted counts enclosing block headers dropped by the guard cap, so
	// a truncated chain is never presented as the whole chain.
	GuardsOmitted int `json:"outer_guards_omitted,omitempty"`
	// Window is the verbatim source around the call. Excluded from JSON: a JSON
	// consumer has the line number and the file.
	Window      []sourceLine `json:"-"`
	WindowStart int          `json:"-"`
	WindowEnd   int          `json:"-"`
}

// sourceLine is one numbered, verbatim line of source.
type sourceLine struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

// lineReader returns a repo-relative file's lines. Missing/unreadable/binary
// files report false; callers degrade to the definition line.
type lineReader func(relPath string) ([]string, bool)

// newRepoLineReader reads files under repoRoot, memoizing per path so several
// call sites in one file cost one read. It refuses oversized and NUL-bearing
// files: neither can usefully be quoted.
func newRepoLineReader(repoRoot string) lineReader {
	cache := map[string][]string{}
	return func(relPath string) ([]string, bool) {
		if relPath == "" || repoRoot == "" {
			return nil, false
		}
		if lines, ok := cache[relPath]; ok {
			return lines, lines != nil
		}
		// A relation's file path may have come from a loaded snapshot. Root.Open
		// rejects absolute/traversing paths and symlinks that escape repoRoot.
		root, err := os.OpenRoot(repoRoot)
		if err != nil {
			cache[relPath] = nil
			return nil, false
		}
		defer root.Close()
		info, err := root.Stat(filepath.FromSlash(relPath))
		if err != nil || !info.Mode().IsRegular() || info.Size() > callSiteMaxFileBytes {
			cache[relPath] = nil
			return nil, false
		}
		file, err := root.Open(filepath.FromSlash(relPath))
		if err != nil {
			cache[relPath] = nil
			return nil, false
		}
		defer file.Close()
		info, err = file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > callSiteMaxFileBytes {
			cache[relPath] = nil
			return nil, false
		}
		content, err := io.ReadAll(io.LimitReader(file, callSiteMaxFileBytes+1))
		if len(content) > callSiteMaxFileBytes {
			err = io.ErrUnexpectedEOF
		}
		if err != nil || strings.IndexByte(string(content), 0) >= 0 {
			cache[relPath] = nil
			return nil, false
		}
		lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
		cache[relPath] = lines
		return lines, true
	}
}

// callEvidenceSpan extracts the caller-body span and the written call text from
// a relation's evidence. The caller of a CALLS edge owns the evidence, so the
// span is the caller's body and the detail is the callee as it appears there.
func callEvidenceSpan(evidence []sem.Evidence) (filePath string, start, end int, token string, ok bool) {
	for _, item := range evidence {
		if item.Kind != "call_site" || item.StartLine <= 0 {
			continue
		}
		return item.FilePath, item.StartLine, item.EndLine, item.Detail, true
	}
	return "", 0, 0, "", false
}

// callTokenFromDetail reduces a written call expression to the identifier that
// appears immediately before the argument list: `diagnostic.try_set_fix` ->
// `try_set_fix`, `pyflakes::rules::foo` -> `foo`, `Foo<T>::bar()` -> `bar`.
func callTokenFromDetail(detail, fallback string) string {
	token := strings.TrimSpace(detail)
	if index := strings.IndexByte(token, '('); index >= 0 {
		token = token[:index]
	}
	token = stripGenericArguments(token)
	for _, separator := range []string{"::", "->", ".", ":", "\\", "$"} {
		if index := strings.LastIndex(token, separator); index >= 0 {
			token = token[index+len(separator):]
		}
	}
	token = strings.TrimSpace(token)
	if !isIdentifierText(token) {
		token = strings.TrimSpace(fallback)
	}
	if !isIdentifierText(token) {
		return ""
	}
	return token
}

// stripGenericArguments removes balanced `<...>` groups so a generic receiver or
// turbofish (`Foo<T>::bar`, `convert::<u32>`) does not hide the called name.
func stripGenericArguments(text string) string {
	var builder strings.Builder
	depth := 0
	for index := 0; index < len(text); index++ {
		switch text[index] {
		case '<':
			depth++
		case '>':
			// A `>` outside a generic group is program text — the arrow in
			// `obj->method` must survive so the separator split still works.
			if depth > 0 {
				depth--
			} else {
				builder.WriteByte(text[index])
			}
		default:
			if depth == 0 {
				builder.WriteByte(text[index])
			}
		}
	}
	return builder.String()
}

func isIdentifierText(text string) bool {
	if text == "" {
		return false
	}
	for index := 0; index < len(text); index++ {
		if !isIdentifierByte(text[index]) {
			return false
		}
	}
	return !(text[0] >= '0' && text[0] <= '9')
}

func isIdentifierByte(character byte) bool {
	return character == '_' ||
		(character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z') ||
		(character >= '0' && character <= '9')
}

// resolveCallSite locates the call to token inside [spanStart, spanEnd] of
// relPath and builds the guard chain and window around it. It reports ok=false
// when the file cannot be read or the token does not occur in the span, in
// which case the caller keeps the definition line and says so.
func resolveCallSite(read lineReader, relPath, token string, spanStart, spanEnd int) (callSite, bool) {
	if read == nil || relPath == "" || token == "" || spanStart <= 0 {
		return callSite{}, false
	}
	lines, ok := read(relPath)
	if !ok || len(lines) == 0 {
		return callSite{}, false
	}
	if spanEnd <= 0 || spanEnd > len(lines) {
		spanEnd = len(lines)
	}
	if spanStart > len(lines) {
		return callSite{}, false
	}
	masked := maskedSourceLines(lines, maskingRules(relPath))
	hits := callTokenLines(masked, token, spanStart, spanEnd)
	if len(hits) == 0 {
		return callSite{}, false
	}
	site := callSite{
		FilePath:        relPath,
		Line:            hits[0],
		AdditionalSites: len(hits) - 1,
	}
	site.Guards, site.GuardsOmitted = enclosingGuardLines(masked, lines, spanStart, hits[0], bracedLanguage(relPath))
	site.Window, site.WindowStart, site.WindowEnd = callWindow(lines, spanStart, spanEnd, hits[0])
	return site, true
}

// callTokenLines reports the lines in [start, end] where token is called.
//
// Two passes, most specific first: an identifier occurrence followed by a call
// opener (`(`, a turbofish `::<`, or a macro `!`) is a call; if the language or
// the formatting hides that, a bare whole-identifier occurrence is still the
// place the name is written, which is what the reader asked for. Comments and
// string literals are masked out before either pass, so a mention in prose or
// in a message template can never be reported as a call.
func callTokenLines(masked []string, token string, start, end int) []int {
	strict := []int{}
	loose := []int{}
	for line := start; line <= end && line <= len(masked); line++ {
		text := masked[line-1]
		for _, column := range identifierColumns(text, token) {
			rest := strings.TrimLeft(text[column+len(token):], " \t")
			switch {
			case strings.HasPrefix(rest, "("), strings.HasPrefix(rest, "::<"),
				strings.HasPrefix(rest, "!"):
				strict = append(strict, line)
			default:
				loose = append(loose, line)
			}
			break
		}
	}
	if len(strict) > 0 {
		return strict
	}
	return loose
}

// identifierColumns returns the byte offsets where token occurs as a complete
// identifier (not as a substring of a longer name).
func identifierColumns(text, token string) []int {
	columns := []int{}
	for offset := 0; ; {
		index := strings.Index(text[offset:], token)
		if index < 0 {
			return columns
		}
		at := offset + index
		before := byte(' ')
		if at > 0 {
			before = text[at-1]
		}
		after := byte(' ')
		if at+len(token) < len(text) {
			after = text[at+len(token)]
		}
		if !isIdentifierByte(before) && !isIdentifierByte(after) {
			columns = append(columns, at)
		}
		offset = at + len(token)
	}
}

// enclosingGuardLines returns the block headers whose bodies contain callLine —
// the conditions in force at that call — outermost first, verbatim from source.
//
// It never synthesizes a claim about what those conditions imply: an invariant
// derived from a brace scan would be a guess, and a wrong invariant is worse
// than none. The lines themselves are the evidence.
func enclosingGuardLines(masked, raw []string, spanStart, callLine int, braced bool) ([]sourceLine, int) {
	var headers []int
	if braced {
		headers = bracedEnclosingHeaders(masked, spanStart, callLine)
	} else {
		headers = indentedEnclosingHeaders(masked, spanStart, callLine)
	}
	guards := make([]sourceLine, 0, len(headers))
	for _, line := range headers {
		if line <= 0 || line > len(raw) {
			continue
		}
		guards = append(guards, sourceLine{Line: line, Text: raw[line-1]})
	}
	if len(guards) <= callContextGuardLimit {
		return guards, 0
	}
	// Over the cap, keep the INNERMOST guards. The outer ones are usually the
	// function's dispatch skeleton (`match expr {`), while the inner ones carry
	// the pattern bindings and conditions a patch actually has to hold under.
	return guards[len(guards)-callContextGuardLimit:],
		outerGuardsOmitted(len(guards), callContextGuardLimit)
}

// outerGuardsOmitted reports how many enclosing block headers the guard cap
// dropped, so the chain is never silently presented as complete.
func outerGuardsOmitted(found, kept int) int {
	if found <= kept {
		return 0
	}
	return found - kept
}

// bracedEnclosingHeaders finds, for a brace-delimited language, the header line
// of every block still open at callLine, then keeps the ones that are guards.
func bracedEnclosingHeaders(masked []string, spanStart, callLine int) []int {
	stack := []int{}
	for line := spanStart; line <= callLine && line <= len(masked); line++ {
		for _, character := range []byte(masked[line-1]) {
			switch character {
			case '{':
				stack = append(stack, line)
			case '}':
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
			}
		}
	}
	headers := []int{}
	seen := map[int]bool{}
	for _, opener := range stack {
		header := bracedBlockHeader(masked, spanStart, opener)
		if header == 0 || seen[header] {
			continue
		}
		seen[header] = true
		headers = append(headers, header)
	}
	sort.Ints(headers)
	return headers
}

// bracedBlockHeader resolves the source line that OWNS the brace opened on
// openerLine and reports it only when it is a guard. A brace frequently sits on
// its own line, or after a wrapped condition, so the statement start is looked
// for a bounded distance above.
func bracedBlockHeader(masked []string, spanStart, openerLine int) int {
	for line := openerLine; line >= spanStart && line > openerLine-callSiteHeaderLookback; line-- {
		text := strings.TrimSpace(masked[line-1])
		if text == "" {
			return 0
		}
		if isGuardHeader(text) {
			return line
		}
		if line < openerLine && strings.HasSuffix(text, ";") {
			return 0
		}
	}
	return 0
}

// indentedEnclosingHeaders is the same question for an indentation-delimited
// language: walking up from the call, every line indented strictly less than
// the last accepted one opens a block that contains the call.
func indentedEnclosingHeaders(masked []string, spanStart, callLine int) []int {
	headers := []int{}
	inner := lineIndent(masked, callLine)
	for line := callLine - 1; line >= spanStart; line-- {
		text := strings.TrimSpace(masked[line-1])
		if text == "" {
			continue
		}
		indent := lineIndent(masked, line)
		if indent >= inner {
			continue
		}
		inner = indent
		if isGuardHeader(text) {
			headers = append(headers, line)
		}
		if indent == 0 {
			break
		}
	}
	sort.Ints(headers)
	return headers
}

func lineIndent(masked []string, line int) int {
	if line <= 0 || line > len(masked) {
		return 0
	}
	text := masked[line-1]
	indent := 0
	for index := 0; index < len(text); index++ {
		switch text[index] {
		case ' ':
			indent++
		case '\t':
			indent += 4
		default:
			return indent
		}
	}
	return indent
}

// guardKeywords are the block openers that constrain whether a call runs, or
// what its inputs are, across the brace and indentation language families.
// Definition openers (fn/def/func/class) are deliberately absent: they are the
// container, not a condition.
var guardKeywords = []string{
	"if", "else", "elif", "elsif", "unless", "match", "switch", "case", "when",
	"while", "until", "for", "foreach", "loop", "try", "catch", "except",
	"rescue", "with", "guard", "select",
}

// isGuardHeader reports whether a masked, trimmed line opens a conditional or
// iterative block. A leading `}` is stripped first so `} else if x {` counts,
// and a trailing `=> {` is accepted for pattern-match arms, which bind the
// values the call then uses.
func isGuardHeader(text string) bool {
	text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "}"))
	if text == "" {
		return false
	}
	if strings.HasSuffix(text, "=>") || strings.HasSuffix(text, "=> {") {
		return true
	}
	for _, keyword := range guardKeywords {
		if !strings.HasPrefix(text, keyword) {
			continue
		}
		rest := text[len(keyword):]
		if rest == "" || !isIdentifierByte(rest[0]) {
			return true
		}
	}
	return false
}

// callWindow is the verbatim source around a call, clamped to the caller's body
// so a window never spills into an unrelated function.
func callWindow(raw []string, spanStart, spanEnd, callLine int) ([]sourceLine, int, int) {
	const lead = 2
	start := callLine - lead
	if start < spanStart {
		start = spanStart
	}
	if start < 1 {
		start = 1
	}
	end := start + callContextWindowLines - 1
	if end > spanEnd {
		end = spanEnd
	}
	if end > len(raw) {
		end = len(raw)
	}
	window := make([]sourceLine, 0, end-start+1)
	for line := start; line <= end; line++ {
		window = append(window, sourceLine{Line: line, Text: raw[line-1]})
	}
	return window, start, end
}

// maskedSourceLines replaces comment and string-literal content with spaces,
// preserving every line and column. Brace counting, guard detection and call
// matching all run on the masked text, so a `{` in a doc comment cannot open a
// block and a callee named in a log message cannot look like a call.
func maskedSourceLines(lines []string, rules sourceMaskingRules) []string {
	masked := make([]string, len(lines))
	inBlockComment := false
	inTripleQuote := byte(0)
	for index, line := range lines {
		out := []byte(line)
		column := 0
		for column < len(out) {
			character := out[column]
			switch {
			case inBlockComment:
				if character == '*' && column+1 < len(out) && out[column+1] == '/' {
					out[column], out[column+1] = ' ', ' '
					column += 2
					inBlockComment = false
					continue
				}
				out[column] = ' '
				column++
			case inTripleQuote != 0:
				if tripleQuoteAt(out, column, inTripleQuote) {
					out[column], out[column+1], out[column+2] = ' ', ' ', ' '
					column += 3
					inTripleQuote = 0
					continue
				}
				out[column] = ' '
				column++
			case character == '/' && column+1 < len(out) && out[column+1] == '/':
				blankFrom(out, column)
				column = len(out)
			case character == '/' && column+1 < len(out) && out[column+1] == '*':
				out[column], out[column+1] = ' ', ' '
				column += 2
				inBlockComment = true
			case rules.hashComments && character == '#':
				blankFrom(out, column)
				column = len(out)
			case rules.tripleQuotes && (character == '"' || character == '\'') && tripleQuoteAt(out, column, character):
				out[column], out[column+1], out[column+2] = ' ', ' ', ' '
				inTripleQuote = character
				column += 3
			case character == '"' || character == '\'' || character == '`':
				column = blankStringLiteral(out, column, rules.singleQuoteIsChar)
			default:
				column++
			}
		}
		masked[index] = string(out)
	}
	return masked
}

func tripleQuoteAt(out []byte, column int, quote byte) bool {
	return column+2 < len(out) && out[column] == quote && out[column+1] == quote && out[column+2] == quote
}

func blankFrom(out []byte, from int) {
	for index := from; index < len(out); index++ {
		out[index] = ' '
	}
}

// charLiteralWidth bounds how far a `'` may be from its partner to still be a
// character literal (`'x'`, `'\n'`, `'\u{1F600}'`). Beyond that the quote is
// almost certainly not a literal at all — a Rust or C++ lifetime/label (`&'a
// str`, `'outer: loop`) — and must not blank the rest of the line, which could
// contain the call.
const charLiteralWidth = 12

// blankStringLiteral blanks a single-line string/char literal starting at the
// opening quote and returns the column just past it. An unterminated string
// (a raw or multi-line form this scanner does not model) blanks to end of
// line, which is the conservative direction: masked text can only lose call
// candidates, never invent them.
func blankStringLiteral(out []byte, open int, singleQuoteIsChar bool) int {
	quote := out[open]
	if quote == '\'' && singleQuoteIsChar && !closesWithin(out, open, charLiteralWidth) {
		// A lifetime or loop label, not a literal. Leave the line alone. This
		// only applies where `'` cannot open a string: in Python/JS/PHP a long
		// single-quoted string is exactly what must be masked.
		return open + 1
	}
	out[open] = ' '
	for column := open + 1; column < len(out); column++ {
		if out[column] == '\\' {
			out[column] = ' '
			if column+1 < len(out) {
				out[column+1] = ' '
				column++
			}
			continue
		}
		if out[column] == quote {
			out[column] = ' '
			return column + 1
		}
		out[column] = ' '
	}
	return len(out)
}

// closesWithin reports whether the quote opened at `open` has a matching quote
// within `width` bytes, honouring backslash escapes.
func closesWithin(out []byte, open, width int) bool {
	quote := out[open]
	limit := open + width
	if limit >= len(out) {
		limit = len(out) - 1
	}
	for column := open + 1; column <= limit; column++ {
		if out[column] == '\\' {
			column++
			continue
		}
		if out[column] == quote {
			return true
		}
	}
	return false
}

// sourceMaskingRules are the per-language details of what counts as a comment or
// a string literal, resolved once per file from its extension.
type sourceMaskingRules struct {
	hashComments      bool
	tripleQuotes      bool
	singleQuoteIsChar bool
}

func maskingRules(path string) sourceMaskingRules {
	return sourceMaskingRules{
		hashComments:      hashCommentLanguage(path),
		tripleQuotes:      tripleQuoteLanguage(path),
		singleQuoteIsChar: singleQuoteIsCharLiteral(path),
	}
}

// singleQuoteIsCharLiteral reports whether `'` opens a CHARACTER literal rather
// than a string. Only in those languages can an unpaired `'` be program text (a
// Rust lifetime, a loop label), and only there may the scanner decline to mask it.
func singleQuoteIsCharLiteral(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".rs", ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hh", ".go", ".java",
		".cs", ".swift", ".kt", ".kts", ".scala", ".zig", ".m", ".mm", ".dart":
		return true
	default:
		return false
	}
}

// bracedLanguage reports whether a path's language delimits blocks with braces.
// Unknown extensions fall back to indentation scanning, which degrades to
// "fewer guards found" rather than to wrong guards.
func bracedLanguage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	// Ruby is deliberately absent: its conditionals are `end`-delimited, so a
	// brace scan would find no enclosing block. It falls through to the
	// indentation scanner, which reads well-formatted Ruby correctly.
	case ".rs", ".go", ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hh", ".java",
		".kt", ".kts", ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".cs", ".swift",
		".php", ".scala", ".dart", ".m", ".mm", ".zig", ".groovy", ".gradle",
		".pl", ".pm", ".hcl", ".tf":
		return true
	default:
		return false
	}
}

// hashCommentLanguage reports whether `#` starts a line comment. It must stay
// false for C (`#include`) and Rust (`#[attr]`), whose `#` is program text.
func hashCommentLanguage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py", ".pyi", ".rb", ".sh", ".bash", ".zsh", ".pl", ".pm", ".r",
		".jl", ".yaml", ".yml", ".toml", ".tf", ".hcl", ".ex", ".exs", ".nim":
		return true
	default:
		return false
	}
}

// tripleQuoteLanguage reports whether the language has triple-quoted multi-line
// strings that must be masked across line boundaries.
func tripleQuoteLanguage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py", ".pyi", ".scala", ".kt", ".kts", ".groovy", ".jl":
		return true
	default:
		return false
	}
}

// callContextBudgeter hands out the shared byte budget for quoted call context
// so one answer's windows cannot grow without bound.
type callContextBudgeter struct {
	blocks    int
	remaining int
}

func newCallContextBudgeter() *callContextBudgeter {
	return &callContextBudgeter{blocks: callContextBlocks, remaining: callContextBudget}
}

// writeCallContext renders the guard chain and window for one call site,
// indented under its caller entry. It writes nothing once the block or byte
// budget is exhausted, and nothing when there is no source to show.
func writeCallContext(out io.Writer, site *callSite, budget *callContextBudgeter) {
	if site == nil || budget == nil || budget.blocks <= 0 {
		return
	}
	if len(site.Guards) == 0 && len(site.Window) == 0 {
		return
	}
	rendered := renderCallContext(site)
	if rendered == "" || len(rendered) > budget.remaining {
		return
	}
	budget.blocks--
	budget.remaining -= len(rendered)
	_, _ = io.WriteString(out, rendered)
}

// renderCallContext formats one call site's guard chain and window. Both are
// dedented by the shallowest indentation present, which is what makes quoting a
// deeply nested call site affordable, and each line is width-capped.
func renderCallContext(site *callSite) string {
	shown := make([]sourceLine, 0, len(site.Guards)+len(site.Window))
	shown = append(shown, site.Guards...)
	guarded := make(map[int]bool, len(site.Guards))
	for _, guard := range site.Guards {
		guarded[guard.Line] = true
	}
	for _, line := range site.Window {
		if !guarded[line.Line] {
			shown = append(shown, line)
		}
	}
	dedent := commonIndentWidth(shown)
	var builder strings.Builder
	if len(site.Guards) > 0 {
		builder.WriteString("  conditions in force at this call (enclosing block headers, verbatim):\n")
		if site.GuardsOmitted > 0 {
			fmt.Fprintf(&builder, "    … +%d outer condition%s omitted\n",
				site.GuardsOmitted, pluralSuffix(site.GuardsOmitted))
		}
		for _, guard := range site.Guards {
			fmt.Fprintf(&builder, "    %d | %s\n", guard.Line, trimSourceLine(guard.Text, dedent))
		}
	}
	windowLines := 0
	var window strings.Builder
	for _, line := range site.Window {
		if guarded[line.Line] {
			continue
		}
		fmt.Fprintf(&window, "    %d | %s\n", line.Line, trimSourceLine(line.Text, dedent))
		windowLines++
	}
	if windowLines > 0 {
		fmt.Fprintf(&builder, "  %s:%d-%d\n", site.FilePath, site.WindowStart, site.WindowEnd)
		builder.WriteString(window.String())
	}
	return builder.String()
}

// commonIndentWidth is the shallowest leading-whitespace width across non-blank
// lines, so relative nesting survives dedenting.
func commonIndentWidth(lines []sourceLine) int {
	width := -1
	for _, line := range lines {
		if strings.TrimSpace(line.Text) == "" {
			continue
		}
		indent := len(line.Text) - len(strings.TrimLeft(line.Text, " \t"))
		if width < 0 || indent < width {
			width = indent
		}
	}
	if width < 0 {
		return 0
	}
	return width
}

func trimSourceLine(text string, dedent int) string {
	trimmed := strings.TrimRight(text, " \t")
	if dedent > 0 && len(trimmed) >= dedent && strings.TrimSpace(trimmed[:dedent]) == "" {
		trimmed = trimmed[dedent:]
	}
	if len(trimmed) > callContextLineWidth {
		return trimmed[:callContextLineWidth] + "…"
	}
	return trimmed
}
