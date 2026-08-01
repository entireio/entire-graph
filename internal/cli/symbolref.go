package cli

import (
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/entireio/entire-graph/internal/sem"
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
		selector := fmt.Sprintf("--symbol %s --file %s --line %d",
			endpointDisplayName(definition), definition.FilePath, definition.StartLine)
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
}, query string, total int, definitions []neighborEndpoint) {
	fmt.Fprintf(out,
		"Ambiguous symbol %q matched %d definitions; rerun with the selector printed beside the one you mean.\n",
		query, total,
	)
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
}
