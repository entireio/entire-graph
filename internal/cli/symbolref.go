package cli

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

// Addressing one definition
// =========================
//
// `neighbors`/`impact` operate on ONE definition, so a name that resolves to several
// definitions has to be narrowed. `--file` is enough only while the definitions live in
// different files: two definitions in the SAME file with the same qualified name (C
// `typedef struct {...} State` twice, overload sets, a declarator and its
// function-expression value before that duplication was collapsed) are byte-identical
// under both `--file` and a qualified `--symbol`, and the tool used to ask for remedies
// that could not possibly work.
//
// A definition's NAME plus its LINE always separates them, so `--line` is the escape hatch
// that is guaranteed to exist, with `--kind` for the last case where two records share a
// name AND a line. `--symbol <file>:<line>` is also accepted as a positional shorthand, but
// it drops the name and therefore selects every definition on that line — convenient to
// type, not something the tool should recommend.

// symbolRef is a resolved --symbol/--file/--line/--kind address.
type symbolRef struct {
	// Name is the (possibly qualified) symbol name, empty when the address is
	// purely positional (`--symbol path/to/file.ts:83`).
	Name string
	File string
	Line int
	Kind string
}

// parseSymbolRef reads the `--symbol` value together with `--file`/`--line`.
//
// `--symbol <file>:<line>` is accepted only when the trailing segment is a line number AND
// the prefix names a file that is actually in the snapshot. That is what keeps names that
// legitimately contain a colon (`Foo::bar`, `A::B::C`) from being mistaken for a location:
// they do not end in digits, and `Foo::` is not a path in the repo.
func parseSymbolRef(symbol, file string, line int, kind, repoRoot string, filePaths []string) symbolRef {
	ref := symbolRef{
		Name: strings.TrimSpace(symbol),
		File: normalizeSymbolRefFile(file, repoRoot),
		Line: line,
		Kind: strings.TrimSpace(kind),
	}
	cut := strings.LastIndexByte(ref.Name, ':')
	if cut <= 0 || cut == len(ref.Name)-1 {
		return ref
	}
	parsed, err := strconv.Atoi(ref.Name[cut+1:])
	if err != nil || parsed <= 0 {
		return ref
	}
	candidate := ref.Name[:cut]
	resolved, ok := matchSnapshotFilePath(candidate, filePaths)
	if !ok {
		return ref
	}
	ref.Name = ""
	if ref.File == "" {
		ref.File = resolved
	}
	if ref.Line == 0 {
		ref.Line = parsed
	}
	return ref
}

// normalizeSymbolRefFile puts a `--file` value into the spelling the snapshot uses, so the
// filter matches on the path the caller MEANT rather than the one they typed. A definition
// filter that silently selects nothing is worse than no filter: the tool reports "no symbols
// matched" and the caller re-reads a correct command looking for the mistake.
//
// It absorbs the spellings that all name one file — a `./` prefix, `\` separators from a
// Windows shell, doubled slashes, and an absolute path under the repo root — without
// consulting the snapshot's file list, because the structural forms have to normalize even
// when the caller passed no file records at all. `matchSnapshotFilePath` handles the
// remaining case (a bare basename or a partial suffix) where the file list IS needed.
func normalizeSymbolRefFile(raw, repoRoot string) string {
	cleaned := strings.ReplaceAll(strings.TrimSpace(raw), `\`, "/")
	if cleaned == "" {
		return ""
	}
	cleaned = path.Clean(cleaned)
	root := path.Clean(strings.ReplaceAll(strings.TrimSpace(repoRoot), `\`, "/"))
	if root != "" && root != "." && root != "/" {
		// A filter naming the repo root itself selects everything, which is what no filter
		// already means.
		if strings.EqualFold(cleaned, root) {
			return ""
		}
		if len(cleaned) > len(root) && strings.EqualFold(cleaned[:len(root)], root) && cleaned[len(root)] == '/' {
			cleaned = cleaned[len(root)+1:]
		}
	}
	if cleaned == "." {
		return ""
	}
	return cleaned
}

// matchSnapshotFilePath resolves a path the caller typed against the snapshot's own paths.
// An exact (case-insensitive) match wins; otherwise a unique path-segment suffix match is
// accepted so `Combobox.tsx:12` works without spelling the whole repo-relative path.
func matchSnapshotFilePath(candidate string, filePaths []string) (string, bool) {
	candidate = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(candidate)), "./")
	if candidate == "" {
		return "", false
	}
	for _, path := range filePaths {
		if strings.EqualFold(path, candidate) {
			return path, true
		}
	}
	matched := ""
	for _, path := range filePaths {
		if !strings.HasSuffix(strings.ToLower(path), strings.ToLower("/"+candidate)) {
			continue
		}
		if matched != "" && matched != path {
			return "", false
		}
		matched = path
	}
	return matched, matched != ""
}

// snapshotFilePaths lists the snapshot's file paths, for --symbol <file>:<line> resolution.
func snapshotFilePaths(snapshot sem.ProviderSnapshot) []string {
	paths := make([]string, 0, len(snapshot.Files))
	for _, file := range snapshot.Files {
		paths = append(paths, file.Path)
	}
	return paths
}

// resolveFocusSymbols returns the definitions an address selects.
//
// An exact stable symbol ID wins outright, ahead of every filter. A `compound-v1` ID is
// `repoKey:language:path:kind:qualifiedName` (plus `#sig:<hash>` and an ordinal when one
// file declares the same name twice), so it already carries the file and the kind: a stale
// or differently spelled `--file`/`--kind`/`--line` must not be able to veto the definition
// the ID names, and an ID this tool printed — or that a consumer read out of the `symbols`
// NDJSON stream — has to keep resolving. Callers holding an ID have the one selector that
// survives edits shifting line numbers, which `--line` does not.
//
// Names fall through to the filters. The line filter prefers definitions that START on the
// requested line, because that is what the disambiguation listing prints and therefore what
// a caller copies back. When no definition starts there it falls back to the definitions
// whose body CONTAINS the line, narrowed to the innermost (smallest span) ones, so a line
// inside a method does not also select the class around it.
func resolveFocusSymbols(symbols []sem.SymbolRecord, ref symbolRef) []sem.SymbolRecord {
	if ref.Name != "" {
		if exact := resolveFocusSymbolsByID(symbols, ref.Name); len(exact) > 0 {
			return exact
		}
	}
	matches := make([]sem.SymbolRecord, 0)
	for _, symbol := range symbols {
		if ref.Name != "" &&
			!strings.EqualFold(symbol.Name, ref.Name) &&
			!strings.EqualFold(symbol.QualifiedName, ref.Name) {
			continue
		}
		if ref.File != "" && !strings.EqualFold(symbol.FilePath, ref.File) {
			continue
		}
		if ref.Kind != "" && !strings.EqualFold(symbol.Kind, ref.Kind) {
			continue
		}
		matches = append(matches, symbol)
	}
	if ref.Line > 0 {
		matches = filterFocusSymbolsByLine(matches, ref.Line)
	}
	return matches
}

// resolveFocusSymbolsByID returns the definitions whose stable ID is exactly `query`.
// The comparison is case-sensitive on purpose: an ID is a generated key, not something a
// human retypes, and folding case could merge two definitions that differ only in the case
// of their qualified name (`Handler` vs `handler` in the same file).
func resolveFocusSymbolsByID(symbols []sem.SymbolRecord, query string) []sem.SymbolRecord {
	matches := make([]sem.SymbolRecord, 0, 1)
	for _, symbol := range symbols {
		if symbol.ID == query {
			matches = append(matches, symbol)
		}
	}
	return matches
}

func filterFocusSymbolsByLine(symbols []sem.SymbolRecord, line int) []sem.SymbolRecord {
	starting := make([]sem.SymbolRecord, 0, len(symbols))
	for _, symbol := range symbols {
		if symbol.StartLine == line {
			starting = append(starting, symbol)
		}
	}
	if len(starting) > 0 {
		return starting
	}
	narrowest := 0
	for _, symbol := range symbols {
		if !focusSymbolSpansLine(symbol, line) {
			continue
		}
		lines := symbol.EndLine - symbol.StartLine + 1
		if narrowest == 0 || lines < narrowest {
			narrowest = lines
		}
	}
	if narrowest == 0 {
		return nil
	}
	containing := make([]sem.SymbolRecord, 0, len(symbols))
	for _, symbol := range symbols {
		if !focusSymbolSpansLine(symbol, line) {
			continue
		}
		if symbol.EndLine-symbol.StartLine+1 != narrowest {
			continue
		}
		containing = append(containing, symbol)
	}
	return containing
}

// focusSymbolSpansLine reports whether a definition's body covers the requested line.
func focusSymbolSpansLine(symbol sem.SymbolRecord, line int) bool {
	return symbol.StartLine <= line && symbol.EndLine >= line
}

// disambiguationSelectors returns, for each listed definition, an argument list that selects
// that definition and nothing else.
//
// This is the guarantee the old message lacked. `--file` separates definitions only across
// files and a qualified `--symbol` only across scopes, so definitions sharing a file AND a
// qualified name had no working remedy at all. Name plus location separates all but
// co-located same-named records, and `--kind` separates those:
//
//	--symbol <name> --file <file> --line <line>             the general form
//	--symbol <name> --file <file> --line <line> --kind <k>  two records share all three
//
// The shorter `--symbol <file>:<line>` shorthand is deliberately NOT printed even when it
// happens to be unique among the definitions LISTED: dropping the name widens the query, so
// a location that is unique among (say) the two `State` definitions can still match a
// differently-named record on the same line. Accepting the shorthand as input is useful;
// recommending it is not.
func disambiguationSelectors(definitions []neighborEndpoint) []string {
	named := make(map[string]int, len(definitions))
	for _, definition := range definitions {
		named[endpointNamedLocationKey(definition)]++
	}
	selectors := make([]string, len(definitions))
	for index, definition := range definitions {
		if definition.FilePath == "" || definition.StartLine <= 0 {
			continue
		}
		// The selector is printed at the end of a definition's line, so it is a
		// one-line value like the rest of that line.
		selector := fmt.Sprintf("--symbol %s --file %s --line %d",
			termsafe.Line(endpointDisplayName(definition)), termsafe.Line(definition.FilePath), definition.StartLine)
		if named[endpointNamedLocationKey(definition)] > 1 && definition.Kind != "" {
			selector += " --kind " + definition.Kind
		}
		selectors[index] = selector
	}
	return selectors
}

func endpointLocationKey(endpoint neighborEndpoint) string {
	return fmt.Sprintf("%s\x00%d", endpoint.FilePath, endpoint.StartLine)
}

func endpointNamedLocationKey(endpoint neighborEndpoint) string {
	return endpointDisplayName(endpoint) + "\x00" + endpointLocationKey(endpoint)
}

// writeNoFocusMatch explains an empty match. The old text suggested `--file`, which cannot
// turn zero matches into one; the useful next move is either dropping the narrowing that
// excluded everything, or `search` when the name itself is wrong.
func writeNoFocusMatch(out interface {
	Write([]byte) (int, error)
}, query, file string, line int) {
	fmt.Fprintf(out, "No symbols matched %q", query)
	switch {
	case file != "" && line > 0:
		fmt.Fprintf(out, " in %s at line %d. Drop --line (or --file) to widen, or run `entire graph search --query %q` to find the name.\n", file, line, query)
	case file != "":
		fmt.Fprintf(out, " in %s. Drop --file to widen, or run `entire graph search --query %q` to find the name.\n", file, query)
	case line > 0:
		fmt.Fprintf(out, " at line %d. Drop --line to widen, or run `entire graph search --query %q` to find the name.\n", line, query)
	default:
		fmt.Fprintf(out, ". Run `entire graph search --query %q` to find the name, or `entire graph symbols --repo .` for the full definition inventory.\n", query)
	}
}

// writeDisambiguationListing prints the ambiguity error plus one selectable line per
// definition. `total` is the pre-cap match count.
func writeDisambiguationListing(out interface {
	Write([]byte) (int, error)
}, query string, total int, definitions []neighborEndpoint, bodies []symbolMatchBody) {
	// FIX A: this is an ANSWER, not an error. Ambiguity means the tool found more than it was asked
	// for, and every definition it found is listed below with a selector for narrowing NEXT time — but
	// the caller is never told to re-run, because re-running is what cost $2.22 on lombok-3486.
	fmt.Fprintf(out, "%q matches %d definitions; all are listed, the first %d with source.\n",
		query, total, minSymbolInt(symbolAmbiguousBodyLimit, len(definitions)))
	selectors := disambiguationSelectors(definitions)
	for index, definition := range definitions {
		line := "- " + formatNeighborEndpoint(definition)
		if definition.Kind != "" {
			line += " [" + definition.Kind + "]"
		}
		if selectors[index] != "" {
			line += "  " + selectors[index]
		}
		fmt.Fprintln(out, line)
	}
	if omitted := total - len(definitions); omitted > 0 {
		fmt.Fprintf(out, "- ... %d more definitions; raise --limit to list them\n", omitted)
	}
	writeSymbolMatchBodies(out, bodies)
}

// writeFuzzyMatchListing is FIX B's renderer: the candidates an exact miss degraded to, labelled with
// HOW they matched so the caller is never misled into thinking it asked for them.
func writeFuzzyMatchListing(out interface {
	Write([]byte) (int, error)
}, query string, tier symbolMatchTier, definitions []neighborEndpoint, bodies []symbolMatchBody) {
	fmt.Fprintf(out, "No exact match for %q. Closest %d by %s match:\n",
		query, len(definitions), tier.label())
	selectors := disambiguationSelectors(definitions)
	for index, definition := range definitions {
		line := "- " + formatNeighborEndpoint(definition)
		if definition.Kind != "" {
			line += " [" + definition.Kind + "]"
		}
		if selectors[index] != "" {
			line += "  " + selectors[index]
		}
		fmt.Fprintln(out, line)
	}
	writeSymbolMatchBodies(out, bodies)
}

// ANSWERING INSTEAD OF REFUSING
// ============================
//
// Session-level forensics of 19 paid sessions found that the tool's most expensive failures were not
// wrong answers — they were REFUSALS to answer a live query at all. Two messages account for both
// measured blow-ups, and in each case the agent spent the money doing by hand what the tool declined
// to do:
//
//   - "Ambiguous symbol %q matched N definitions; rerun with the selector printed beside the one you
//     mean." — projectlombok__lombok-3486, +$2.22, 78 pre-edit operations while the agent manually
//     disambiguated two same-named methods it had already been told the locations of.
//   - "No symbols matched %q" — phpoffice__phpspreadsheet-3570, +$1.23, BOTH live queries empty and
//     shell greps 18 -> 39, because the query spelled the name in a different but obvious way.
//
// Neither refusal is necessary. Ambiguity means the tool found MORE than it was asked for, which is
// information, not an error; and an exact-match miss on an identifier is a spelling question the graph
// can answer itself. So:
//
//   FIX A  every definition is listed, and the top ones come back WITH SOURCE. No rerun instruction.
//   FIX B  an exact miss degrades to a fuzzy ladder (case, separators, tokens, substring) and returns
//          the best candidates, clearly labelled as fuzzy so the caller is never misled about what
//          matched.
//
// Both are implemented HERE, on the one resolver and the two renderers that `def`, `neighbors` and
// `impact` all share, so no verb can drift back to refusing.

const (
	// symbolFuzzyCandidateLimit is how many fuzzy candidates are returned. Three is the widest list
	// that still reads as "did you mean"; past that the honest answer is `search`.
	symbolFuzzyCandidateLimit = 3

	// symbolAmbiguousBodyLimit is how many of several matching definitions come back with source. The
	// list of locations is cheap and complete; bodies are what remove the follow-up read, and two is
	// the measured shape of the failure (a method declared once per backend, once per AST flavour).
	symbolAmbiguousBodyLimit = 2

	// symbolMatchBodyMaxLines caps one printed body.
	//
	// 40 -> 400. MEASURED (briannesbitt/carbon): the agent called def, got a body cut off before the
	// line it needed, piped it through `head -80` — which cut AGAIN, at the bug line — and abandoned the
	// tool for 87 turns of grep. A navigation answer that stops mid-symbol is worse than no answer: it
	// looks complete. 400 lines is the same safety ceiling the search allocator uses for a forced unit,
	// and anything past it says exactly where it stopped and how to resume.
	symbolMatchBodyMaxLines = 400
)

// symbolMatchTier ranks how a fuzzy candidate matched, lowest (best) first. It is reported so the
// label can say WHY something matched rather than just that it did.
type symbolMatchTier int

const (
	symbolMatchExact symbolMatchTier = iota
	symbolMatchCaseInsensitive
	symbolMatchSeparatorInsensitive
	symbolMatchTokenSubset
	symbolMatchSubstring
)

func (tier symbolMatchTier) label() string {
	switch tier {
	case symbolMatchCaseInsensitive:
		return "case-insensitive"
	case symbolMatchSeparatorInsensitive:
		return "separator-insensitive"
	case symbolMatchTokenSubset:
		return "identifier-token"
	case symbolMatchSubstring:
		return "substring"
	}
	return "exact"
}

// compactSymbolIdentifier reduces a name to letters and digits, lowercased, so that
// `Functions.flattenSingleValue`, `flatten_single_value` and `FLATTEN-SINGLE-VALUE` all compare equal.
// Separator style is a spelling convention, not an identity.
func compactSymbolIdentifier(value string) string {
	var out strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out.WriteRune(unicode.ToLower(r))
		}
	}
	return out.String()
}

// symbolIdentifierTokens splits a name into lowercase word tokens across camelCase, snake_case,
// kebab-case, `::` and `.`, so `flattenSingleValue` and `flatten_single_value` yield the same set.
func symbolIdentifierTokens(value string) map[string]bool {
	tokens := map[string]bool{}
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			tokens[strings.ToLower(current.String())] = true
			current.Reset()
		}
	}
	runes := []rune(value)
	for index, r := range runes {
		switch {
		case unicode.IsUpper(r):
			// A new word starts at a lower->upper boundary and at the last capital of a run
			// ("HTTPServer" -> http, server).
			if index > 0 && (unicode.IsLower(runes[index-1]) || unicode.IsDigit(runes[index-1]) ||
				(index+1 < len(runes) && unicode.IsLower(runes[index+1]))) {
				flush()
			}
			current.WriteRune(r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			current.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return tokens
}

// resolveFocusSymbolsOrFuzzy is FIX B. It returns the exact matches when there are any, and otherwise
// the best fuzzy candidates for the name — never an empty answer when the graph holds something the
// caller plausibly meant. `fuzzy` says which happened, so every renderer can label it.
//
// The ladder is ordered by how much it assumes. Case and separator differences are spelling; a token
// subset is the caller naming the same concept with a qualifier dropped; a substring is the weakest
// claim and comes last. Any narrowing the caller supplied (--file/--kind) still applies, because a
// fuzzy NAME plus an exact file is a much better answer than a fuzzy name alone.
func resolveFocusSymbolsOrFuzzy(
	symbols []sem.SymbolRecord, ref symbolRef, limit int,
) (matches []sem.SymbolRecord, tier symbolMatchTier, fuzzy bool) {
	if exact := resolveFocusSymbols(symbols, ref); len(exact) > 0 {
		return exact, symbolMatchExact, false
	}
	if ref.Name == "" || limit <= 0 {
		return nil, symbolMatchExact, false
	}
	wantCompact := compactSymbolIdentifier(ref.Name)
	if wantCompact == "" {
		return nil, symbolMatchExact, false
	}
	wantTokens := symbolIdentifierTokens(ref.Name)
	type scored struct {
		symbol sem.SymbolRecord
		tier   symbolMatchTier
	}
	var pool []scored
	for _, symbol := range symbols {
		if ref.File != "" && !strings.EqualFold(symbol.FilePath, ref.File) {
			continue
		}
		if ref.Kind != "" && !strings.EqualFold(symbol.Kind, ref.Kind) {
			continue
		}
		best := symbolMatchTier(-1)
		for _, candidate := range []string{symbol.Name, symbol.QualifiedName} {
			if candidate == "" {
				continue
			}
			if tier, ok := symbolNameMatchTier(candidate, ref.Name, wantCompact, wantTokens); ok &&
				(best < 0 || tier < best) {
				best = tier
			}
		}
		if best < 0 {
			continue
		}
		pool = append(pool, scored{symbol: symbol, tier: best})
	}
	if len(pool) == 0 {
		return nil, symbolMatchExact, false
	}
	sort.SliceStable(pool, func(left, right int) bool {
		if pool[left].tier != pool[right].tier {
			return pool[left].tier < pool[right].tier
		}
		// Among equally-matched candidates the shortest name is the closest spelling, then the
		// declaration order, so the answer is deterministic across runs.
		leftName, rightName := symbolRefDisplayName(pool[left].symbol), symbolRefDisplayName(pool[right].symbol)
		if len(leftName) != len(rightName) {
			return len(leftName) < len(rightName)
		}
		if pool[left].symbol.FilePath != pool[right].symbol.FilePath {
			return pool[left].symbol.FilePath < pool[right].symbol.FilePath
		}
		if pool[left].symbol.StartLine != pool[right].symbol.StartLine {
			return pool[left].symbol.StartLine < pool[right].symbol.StartLine
		}
		return pool[left].symbol.ID < pool[right].symbol.ID
	})
	if len(pool) > limit {
		pool = pool[:limit]
	}
	matches = make([]sem.SymbolRecord, 0, len(pool))
	for _, entry := range pool {
		matches = append(matches, entry.symbol)
	}
	return matches, pool[0].tier, true
}

// symbolNameMatchTier scores one candidate name against the query. Exact is excluded: the caller has
// already tried it, so reporting it here would hide a real miss behind a tier that cannot happen.
func symbolNameMatchTier(
	candidate, want, wantCompact string, wantTokens map[string]bool,
) (symbolMatchTier, bool) {
	if strings.EqualFold(candidate, want) {
		return symbolMatchCaseInsensitive, true
	}
	candidateCompact := compactSymbolIdentifier(candidate)
	if candidateCompact == "" {
		return 0, false
	}
	if candidateCompact == wantCompact {
		return symbolMatchSeparatorInsensitive, true
	}
	// A token subset in either direction: `flattenSingleValue` for a query of `flatten_single_value`
	// (equal sets), and `Functions.flattenSingleValue` for a query of `flattenSingleValue` (superset).
	if len(wantTokens) > 0 {
		candidateTokens := symbolIdentifierTokens(candidate)
		if len(candidateTokens) > 0 && (symbolTokensContain(candidateTokens, wantTokens) ||
			symbolTokensContain(wantTokens, candidateTokens)) {
			return symbolMatchTokenSubset, true
		}
	}
	// The weakest claim, and bounded hard. `def` has a standing invariant that a short fragment must
	// NEVER resolve to a longer name (TestDefNeverMatchesASubstring: "dele" is not "deletion"), and
	// that invariant is right — a four-letter fragment matches half a codebase. Eight characters is
	// past any accidental fragment while still admitting the case this rung exists for, a fully spelled
	// identifier whose qualifier differs. The rungs above already cover separator and token spelling,
	// so nothing real depends on relaxing this.
	if len(wantCompact) >= 8 &&
		(strings.Contains(candidateCompact, wantCompact) || strings.Contains(wantCompact, candidateCompact)) {
		return symbolMatchSubstring, true
	}
	return 0, false
}

func symbolTokensContain(outer, inner map[string]bool) bool {
	if len(inner) == 0 {
		return false
	}
	for token := range inner {
		if !outer[token] {
			return false
		}
	}
	return true
}

func symbolRefDisplayName(symbol sem.SymbolRecord) string {
	if symbol.QualifiedName != "" {
		return symbol.QualifiedName
	}
	return symbol.Name
}

// symbolMatchBody is the source of one matched definition, read for the answers FIX A and FIX B
// return in place of a refusal.
type symbolMatchBody struct {
	Name      string `json:"name"`
	Kind      string `json:"kind,omitempty"`
	FilePath  string `json:"file_path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Source    string `json:"source,omitempty"`
	Elided    bool   `json:"elided,omitempty"`
	// UnitEndLine is the symbol's true last line when the cap clipped the body, so the note can tell
	// the caller where to resume instead of only that something is missing.
	UnitEndLine int `json:"unit_end_line,omitempty"`
}

// symbolMatchBodies reads a compact body for the first `limit` records. A body that cannot be read
// (file gone, binary, oversized) is simply omitted: the locator list above it is still a complete
// answer, and a missing body must never turn an answer back into a refusal.
func symbolMatchBodies(repoRoot string, matches []sem.SymbolRecord, limit int) []symbolMatchBody {
	if repoRoot == "" || limit <= 0 || len(matches) == 0 {
		return nil
	}
	read := newRepoLineReader(repoRoot)
	bodies := make([]symbolMatchBody, 0, minSymbolInt(limit, len(matches)))
	for _, symbol := range matches {
		if len(bodies) >= limit {
			break
		}
		if symbol.FilePath == "" || symbol.StartLine <= 0 {
			continue
		}
		lines, ok := read(symbol.FilePath)
		if !ok || symbol.StartLine > len(lines) {
			continue
		}
		start := symbol.StartLine
		end := symbol.EndLine
		if end < start {
			end = start
		}
		if end > len(lines) {
			end = len(lines)
		}
		elided, unitEnd := false, 0
		if end-start+1 > symbolMatchBodyMaxLines {
			unitEnd = end
			end = start + symbolMatchBodyMaxLines - 1
			elided = true
		}
		bodies = append(bodies, symbolMatchBody{
			Name:        symbolRefDisplayName(symbol),
			Kind:        symbol.Kind,
			FilePath:    symbol.FilePath,
			StartLine:   start,
			EndLine:     end,
			Source:      strings.Join(lines[start-1:end], "\n"),
			Elided:      elided,
			UnitEndLine: unitEnd,
		})
	}
	if len(bodies) == 0 {
		return nil
	}
	return bodies
}

func minSymbolInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// writeSymbolMatchBodies prints the bodies under a locator list, each line in a NUMBERED gutter.
//
// The search payload's bodies are deliberately unnumbered — an agent copies them verbatim as an Edit
// anchor, and a gutter breaks the anchor. These are not those. `def`, `callers` and `neighbors` answer
// "where is it", and the measured failure mode is the opposite one: carbon's agent got an unnumbered
// body, could not tell which line was which, piped it through `head -80` and lost the line it needed.
// A navigation answer wants coordinates; an edit source wants fidelity. Two outputs, two rules.
func writeSymbolMatchBodies(out interface {
	Write([]byte) (int, error)
}, bodies []symbolMatchBody) {
	// The single place repository SOURCE is printed with a line-number gutter, and
	// the only sink `def` reaches that its own writer wrap does not cover — so the
	// wrap is made here, where all three callers pass through. Wrapping twice is
	// harmless; see termsafe.NewWriter.
	out = termsafe.NewWriter(out)
	for _, body := range bodies {
		// The locator above each body is one line, so its fields get the stricter
		// escape; the body itself keeps its newlines, being real layout.
		fmt.Fprintf(out, "\n%s:%d-%d %s", termsafe.Line(body.FilePath), body.StartLine, body.EndLine,
			termsafe.Line(body.Name))
		if body.Kind != "" {
			fmt.Fprintf(out, " [%s]", termsafe.Line(body.Kind))
		}
		fmt.Fprintln(out)
		writeNumberedSource(out, body.Source, body.StartLine)
		if body.Elided {
			// Actionable, not merely honest: the caller is told where the body continues AND the exact
			// invocation that resumes it, because "output truncated" is what sent carbon to grep.
			fmt.Fprintf(out, "  …continues to line %d — rerun with --from %d\n",
				body.UnitEndLine, body.EndLine+1)
		}
	}
}

// writeNumberedSource prints source with a right-aligned line-number gutter starting at `first`.
func writeNumberedSource(out interface {
	Write([]byte) (int, error)
}, source string, first int) {
	lines := strings.Split(source, "\n")
	width := len(strconv.Itoa(first + len(lines) - 1))
	for offset, line := range lines {
		fmt.Fprintf(out, "  %*d→ %s\n", width, first+offset, line)
	}
}

// fuzzyKindLabel is the label a response carries, empty when the match was exact. Keeping the
// conversion in one place is what stops "exact" from ever being reported as a fuzzy rung.
func fuzzyKindLabel(fuzzy bool, tier symbolMatchTier) string {
	if !fuzzy {
		return ""
	}
	return tier.label()
}

// symbolMatchTierFromLabel is the inverse, for renderers that only have the response. An unknown
// label degrades to the weakest rung rather than to "exact", so a wrong label can never overstate
// how well something matched.
func symbolMatchTierFromLabel(label string) symbolMatchTier {
	for _, tier := range []symbolMatchTier{
		symbolMatchCaseInsensitive, symbolMatchSeparatorInsensitive,
		symbolMatchTokenSubset, symbolMatchSubstring,
	} {
		if tier.label() == label {
			return tier
		}
	}
	return symbolMatchSubstring
}
