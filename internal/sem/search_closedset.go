package sem

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/entireio/entire-graph/internal/termsafe"
)

// The closed-set warning
// ======================
//
// An agent that had just been shown an enum said this was the single highest-value correctness
// output the tool could give it, and that it was nearly free: "adding a constant without adding a
// case is a runtime failure in getCacheKey(), not a compile error" — because the switch over that
// enum ends in a `default:` that throws. On any add-a-variant task that one fact prevents the most
// likely wrong patch, and no amount of ranking quality substitutes for it: the payload can put the
// enum at rank 1 and the patch is still wrong.
//
// The block therefore answers exactly one question: if I add a variant to this set, what breaks, and
// does the COMPILER tell me?
//
// How it is decided, and why it is decided this way
// ------------------------------------------------
//
// The switch sites are found from the ARMS, not from the scrutinee's type. Resolving "what type is
// the expression in `switch (x)`" needs type inference this tool does not do, and a wrong answer
// would either invent a warning or miss the real one. But a block whose arms name two or more
// members of the set IS a switch over that set — that is direct textual evidence, it needs no
// inference, and it cannot be produced by a coincidence of names.
//
// Whether a missing arm is a compile error or a runtime failure is a property of the LANGUAGE and of
// the construct, so it is reported per site rather than assumed:
//
//	Rust `match` with no wildcard arm      compiler — the match itself stops compiling
//	Kotlin `when` used as an expression    compiler — an expression `when` must be exhaustive
//	TypeScript `switch` with a `never`     compiler — the exhaustiveness assertion fails to typecheck
//	everything else                        runtime  — Java/C#/Go/JS/TS/PHP/Python/C/C++ switch
//
// The warning is only emitted when at least one site is in the RUNTIME column and that site is
// currently exhaustive with a throwing or absent default. That is the shape that fails silently at
// build time, which is the shape the agent was warning about.
//
// Covered set kinds: `enum` in every language whose parser emits the kind (Java, Kotlin, C#, C, C++,
// Rust, PHP, TypeScript, Swift), TypeScript/Flow string-union type aliases, Java `sealed ... permits`
// hierarchies, and Go typed const groups. NOT covered: Python `Enum` subclasses and Ruby constant
// modules (both spelled as an ordinary class, so there is no structural marker to key on), and
// pattern matches on protocols/traits rather than on a variant set (Scala, Swift). Those get nothing
// rather than a guess.
const (
	// searchClosedSetMaxBytes caps the block on the larger of its two wire forms.
	searchClosedSetMaxBytes = 420

	// searchClosedSetSiteLimit is how many switch sites the block names. Two: the one that fails and
	// one more to show it is a pattern rather than an accident.
	searchClosedSetSiteLimit = 2

	// searchClosedSetMinArms is how many members of the set a block's arms must name before the
	// block is accepted as a switch over that set. One shared name is a coincidence.
	searchClosedSetMinArms = 2

	// searchClosedSetMinMembers is the smallest set that can be one at all. Two, not three: a
	// two-variant enum growing a third is precisely the add-a-variant task the warning exists for,
	// and requiring three would refuse the case most likely to be asked about.
	searchClosedSetMinMembers = 2

	// searchClosedSetTypeCandidates bounds how many types named in the anchor are considered as the
	// closed set. The scrutinee's type is usually named in the enclosing function's signature, and
	// that is a link the graph gives for free.
	searchClosedSetTypeCandidates = 8

	// searchClosedSetMaxMembers bounds member extraction.
	searchClosedSetMaxMembers = 64

	// searchClosedSetBlockLines bounds the forward scan from a switch header.
	searchClosedSetBlockLines = 160

	// searchClosedSetMaxFiles bounds how many files are scanned for switch sites.
	searchClosedSetMaxFiles = 12

	// searchClosedSetDefaultLookahead is how far past a default arm the scan looks for the throw
	// that makes it fatal.
	searchClosedSetDefaultLookahead = 3
)

// Default-arm dispositions.
const (
	searchClosedSetDefaultThrows = "throws"
	searchClosedSetDefaultSilent = "silent"
	searchClosedSetDefaultAbsent = "absent"
)

// Where a missing arm is caught.
const (
	searchClosedSetCheckedRuntime  = "runtime"
	searchClosedSetCheckedCompiler = "compiler"
)

// SearchClosedSet is the warning for a closed variant set the top hit belongs to.
type SearchClosedSet struct {
	// Type is the set's name and Kind how it is spelled (enum, union, sealed, const-group).
	Type string `json:"type"`
	Kind string `json:"kind"`
	// Variants is how many members the set currently has.
	Variants int `json:"variants"`
	// Sites are the switch/match statements found over this set.
	Sites []SearchClosedSetSite `json:"sites"`
	// Warning is the consequence, stated. Empty when no site is at runtime risk.
	Warning string `json:"warning,omitempty"`
}

// SearchClosedSetSite is one switch/match over the set.
type SearchClosedSetSite struct {
	FilePath string `json:"file_path"`
	Line     int    `json:"line"`
	Symbol   string `json:"symbol,omitempty"`
	// Arms is how many of the set's members this site handles.
	Arms int `json:"arms"`
	// Exhaustive reports whether every current member has an arm.
	Exhaustive bool `json:"exhaustive"`
	// Default is what the fall-through arm does: throws, silent, or absent.
	Default string `json:"default"`
	// Checked is where a missing arm surfaces: compiler or runtime.
	Checked string `json:"checked"`
}

// searchClosedVariantSet is the set itself, before any site is looked for.
type searchClosedVariantSet struct {
	name     string
	kind     string
	language string
	members  []string
	// quoted marks a set whose members are string literals (a TypeScript union), because its arms
	// are spelled `case "fast":` rather than `case Fast:`.
	quoted bool
	// declaredIn is the file the set is declared in; it is always scanned for sites.
	declaredIn string
}

// buildSearchClosedSet finds the set the top hit belongs to and the switch sites over it, or returns
// nil.
func buildSearchClosedSet(
	results []SearchResult,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	index *searchNeedleIndex,
	maxBytes int,
) *SearchClosedSet {
	if index == nil || maxBytes <= 0 {
		return nil
	}
	anchor, ok := searchLiteralAnchor(results)
	if !ok {
		return nil
	}
	set, found := searchClosedVariantSetFor(anchor, symbolsByID, symbolsByFile, index)
	if !found {
		return nil
	}
	sites := searchClosedSetSites(set, results, symbolsByFile, index)
	if len(sites) == 0 {
		return nil
	}
	block := &SearchClosedSet{
		Type:     set.name,
		Kind:     set.kind,
		Variants: len(set.members),
		Sites:    sites,
		Warning:  searchClosedSetWarning(sites),
	}
	// A set with no runtime-risk site is a set the compiler already guards. Saying so costs bytes
	// and prevents nothing, so it is not said.
	if block.Warning == "" {
		return nil
	}
	for len(block.Sites) > 1 && searchClosedSetCost(block) > maxBytes {
		block.Sites = block.Sites[:len(block.Sites)-1]
	}
	if searchClosedSetCost(block) > maxBytes {
		return nil
	}
	return block
}

// searchClosedVariantSetFor resolves the closed set the anchor is, is a member of, or is declared
// beside.
func searchClosedVariantSetFor(
	anchor SearchResult,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	index *searchNeedleIndex,
) (searchClosedVariantSet, bool) {
	lines, readable := index.readLines(anchor.FilePath)
	if !readable {
		return searchClosedVariantSet{}, false
	}
	for _, symbol := range searchClosedSetCandidateSymbols(anchor, lines, symbolsByID, symbolsByFile) {
		declaredLines := lines
		if symbol.FilePath != anchor.FilePath {
			other, readable := index.readLines(symbol.FilePath)
			if !readable {
				continue
			}
			declaredLines = other
		}
		if set, ok := searchClosedVariantSetFromSymbol(symbol, declaredLines); ok {
			return set, true
		}
	}
	return searchClosedVariantSet{}, false
}

// searchClosedSetCandidateSymbols is what the anchor could be a member of, or dispatch on:
//
//   - its own symbol and that symbol's container — a hit inside a method of an enum is a hit about
//     the enum;
//   - every symbol in the file containing the hit's focus line, innermost first;
//   - every enum/type/sealed declaration the graph knows whose NAME the anchor's own text mentions.
//     A switch's scrutinee type is almost always named in the enclosing function's signature, and
//     that link is free — no type inference, just the name the code already writes. A collision
//     cannot produce a false warning, because a site only counts when its arms name two members of
//     the set that was picked.
func searchClosedSetCandidateSymbols(
	anchor SearchResult,
	anchorLines []string,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
) []SymbolRecord {
	seen := map[string]bool{}
	out := make([]SymbolRecord, 0, 4)
	add := func(symbol SymbolRecord) {
		if symbol.ID == "" || seen[symbol.ID] {
			return
		}
		seen[symbol.ID] = true
		out = append(out, symbol)
	}
	if anchor.SymbolID != "" {
		if symbol, ok := symbolsByID[anchor.SymbolID]; ok {
			add(symbol)
			if container, ok := symbolsByID[symbol.ContainerID]; ok {
				add(container)
			}
		}
	}
	containing := make([]SymbolRecord, 0, 4)
	for _, symbol := range symbolsByFile[anchor.FilePath] {
		if symbol.StartLine <= anchor.FocusLine && anchor.FocusLine <= symbol.EndLine {
			containing = append(containing, symbol)
		}
	}
	sort.SliceStable(containing, func(left, right int) bool {
		return containing[left].EndLine-containing[left].StartLine <
			containing[right].EndLine-containing[right].StartLine
	})
	for _, symbol := range containing {
		add(symbol)
		if container, ok := symbolsByID[symbol.ContainerID]; ok {
			add(container)
		}
	}
	for _, symbol := range searchClosedSetNamedTypes(anchor, anchorLines, symbolsByID) {
		add(symbol)
	}
	return out
}

// searchClosedSetNamedTypes returns the variant-set-shaped declarations whose name appears in the
// anchor's own signature or body, nearest declaration first (same file before elsewhere).
func searchClosedSetNamedTypes(
	anchor SearchResult,
	anchorLines []string,
	symbolsByID map[string]SymbolRecord,
) []SymbolRecord {
	tokens := searchIdentifierTokenSet(anchor.Signature)
	start, end := anchor.StartLine, anchor.EndLine
	if start >= 1 && end >= start && start <= len(anchorLines) {
		if end > len(anchorLines) {
			end = len(anchorLines)
		}
		for _, line := range anchorLines[start-1 : end] {
			for token := range searchIdentifierTokenSet(line) {
				tokens[token] = true
			}
		}
	}
	if len(tokens) == 0 {
		return nil
	}
	named := make([]SymbolRecord, 0, searchClosedSetTypeCandidates)
	for _, symbol := range symbolsByID {
		if !searchClosedSetVariantSetKind(symbol.Kind) || !tokens[symbol.Name] {
			continue
		}
		named = append(named, symbol)
	}
	sort.SliceStable(named, func(left, right int) bool {
		leftLocal := named[left].FilePath == anchor.FilePath
		rightLocal := named[right].FilePath == anchor.FilePath
		if leftLocal != rightLocal {
			return leftLocal
		}
		if named[left].FilePath != named[right].FilePath {
			return named[left].FilePath < named[right].FilePath
		}
		return named[left].StartLine < named[right].StartLine
	})
	if len(named) > searchClosedSetTypeCandidates {
		named = named[:searchClosedSetTypeCandidates]
	}
	return named
}

// searchClosedSetVariantSetKind is the kinds that can spell a closed set.
func searchClosedSetVariantSetKind(kind string) bool {
	switch kind {
	case "enum", "type", "type_alias", "class", "interface":
		return true
	}
	return false
}

// searchIdentifierTokenSet splits a line into its identifier tokens.
func searchIdentifierTokenSet(text string) map[string]bool {
	tokens := map[string]bool{}
	start := -1
	for offset := 0; offset <= len(text); offset++ {
		if offset < len(text) && searchIdentifierByte(text[offset]) {
			if start < 0 {
				start = offset
			}
			continue
		}
		if start >= 0 {
			tokens[text[start:offset]] = true
			start = -1
		}
	}
	return tokens
}

// searchClosedVariantSetFromSymbol reads a closed set out of one symbol, or reports that the symbol
// is not one.
func searchClosedVariantSetFromSymbol(symbol SymbolRecord, lines []string) (searchClosedVariantSet, bool) {
	set := searchClosedVariantSet{
		name:       symbol.Name,
		language:   symbol.Language,
		declaredIn: symbol.FilePath,
	}
	switch {
	case symbol.Kind == "enum":
		set.kind = "enum"
		set.members = searchClosedSetMembersFromBody(lines, symbol)
	case strings.Contains(symbol.Signature, "sealed ") && strings.Contains(symbol.Signature, "permits"):
		set.kind = "sealed"
		set.members = searchClosedSetPermittedTypes(symbol.Signature)
	case symbol.Kind == "type" || symbol.Kind == "type_alias":
		if members, quoted, ok := searchClosedSetUnionMembers(symbol.Signature); ok {
			set.kind = "union"
			set.members, set.quoted = members, quoted
			break
		}
		if members, ok := searchClosedSetGoConstGroup(lines, symbol); ok {
			set.kind = "const-group"
			set.members = members
			break
		}
		return set, false
	default:
		return set, false
	}
	if set.name == "" || len(set.members) < searchClosedSetMinMembers {
		return set, false
	}
	if len(set.members) > searchClosedSetMaxMembers {
		return set, false
	}
	return set, true
}

// searchClosedSetDeclarationKeywords are the tokens that start a MEMBER of a type rather than a
// variant of it. They are the reason a field or a method inside an enum body is not read as a
// variant, and they are shared vocabulary across the languages in scope rather than a per-language
// table.
var searchClosedSetDeclarationKeywords = map[string]bool{
	"public": true, "private": true, "protected": true, "internal": true, "static": true,
	"final": true, "const": true, "let": true, "var": true, "val": true, "fun": true,
	"func": true, "def": true, "class": true, "struct": true, "enum": true, "impl": true,
	"pub": true, "override": true, "abstract": true, "virtual": true, "readonly": true,
	"return": true, "if": true, "else": true, "switch": true, "match": true, "for": true,
	"while": true, "this": true, "self": true, "new": true, "throw": true, "try": true,
	"catch": true, "function": true, "interface": true, "trait": true, "type": true,
	"import": true, "package": true, "namespace": true, "using": true, "where": true,
	"default": true, "constructor": true, "get": true, "set": true, "async": true,
	"declare": true, "export": true, "extends": true, "implements": true, "sealed": true,
}

// searchClosedSetMembersFromBody extracts the variants declared in a type's own body.
//
// The parsers in this package do not emit enum constants as symbols in any language, so the body is
// the only source. The rule is one surface fact: a variant is a line whose leading identifier is
// immediately followed by a variant terminator (`,` `;` `(` `=` `{` `:`) or by nothing. That covers
// `PLUS("+"),`, `Alpha,`, `Beta(u32),`, `Gamma { x: u8 },`, `Delta = 1,`, and PHP/Swift `case Draft;`
// — and it excludes fields and methods, which start with a declaration keyword or put a type before
// the name.
func searchClosedSetMembersFromBody(lines []string, symbol SymbolRecord) []string {
	start, end := symbol.StartLine, symbol.EndLine
	if start < 1 || end < start || start > len(lines) {
		return nil
	}
	if end > len(lines) {
		end = len(lines)
	}
	members := make([]string, 0, 8)
	seen := map[string]bool{}
	for _, line := range lines[start-1 : end] {
		name, ok := searchClosedSetVariantOnLine(line, symbol.Name)
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		members = append(members, name)
		if len(members) > searchClosedSetMaxMembers {
			break
		}
	}
	return members
}

// searchClosedSetVariantOnLine applies the surface rule above to one line.
func searchClosedSetVariantOnLine(line, typeName string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || searchClosedSetCommentLine(trimmed) {
		return "", false
	}
	name, rest := searchLeadingIdentifier(trimmed)
	if name == "" {
		return "", false
	}
	lowered := strings.ToLower(name)
	// `case Draft;` (PHP, Swift): the variant is the token after the keyword.
	if lowered == "case" {
		name, rest = searchLeadingIdentifier(strings.TrimSpace(rest))
		if name == "" {
			return "", false
		}
	} else if searchClosedSetDeclarationKeywords[lowered] {
		return "", false
	}
	// The type's own name at the start of a line inside its body is its constructor.
	if name == typeName {
		return "", false
	}
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" {
		return name, true
	}
	switch rest[0] {
	case ',', ';', '(', '=', '{', ':':
		return name, true
	}
	return "", false
}

func searchClosedSetCommentLine(trimmed string) bool {
	for _, prefix := range []string{"//", "#", "*", "/*", "--", "\"\"\""} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// searchLeadingIdentifier splits a leading identifier off a string.
func searchLeadingIdentifier(value string) (string, string) {
	end := 0
	for end < len(value) {
		character := value[end]
		isWord := character == '_' ||
			(character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(end > 0 && character >= '0' && character <= '9')
		if !isWord {
			break
		}
		end++
	}
	if end == 0 {
		return "", value
	}
	return value[:end], value[end:]
}

// searchClosedSetPermittedTypes reads a Java `sealed ... permits A, B, C` clause.
func searchClosedSetPermittedTypes(signature string) []string {
	_, list, found := strings.Cut(signature, "permits")
	if !found {
		return nil
	}
	list = strings.TrimSpace(strings.Trim(strings.TrimSpace(list), "{"))
	members := make([]string, 0, 4)
	for _, part := range strings.Split(list, ",") {
		name, _ := searchLeadingIdentifier(strings.TrimSpace(part))
		if name != "" {
			members = append(members, name)
		}
	}
	return members
}

// searchClosedSetUnionMembers reads a TypeScript/Flow union type alias out of a signature:
// `type Mode = "fast" | "slow" | "auto"`. A union of quoted literals reports quoted=true, because its
// switch arms are spelled with the quotes.
//
// A union whose alternatives are not ALL literals or not ALL plain identifiers is rejected: a mixed
// or generic union (`A | B<C> | null`) has no reliable arm spelling.
func searchClosedSetUnionMembers(signature string) ([]string, bool, bool) {
	_, definition, found := strings.Cut(signature, "=")
	if !found || !strings.Contains(definition, "|") {
		return nil, false, false
	}
	parts := strings.Split(definition, "|")
	if len(parts) < searchClosedSetMinMembers {
		return nil, false, false
	}
	members := make([]string, 0, len(parts))
	quotedCount := 0
	for _, part := range parts {
		part = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(part), ";"))
		if part == "" {
			return nil, false, false
		}
		if len(part) >= 2 && (part[0] == '"' || part[0] == '\'') && part[len(part)-1] == part[0] {
			members = append(members, part[1:len(part)-1])
			quotedCount++
			continue
		}
		name, rest := searchLeadingIdentifier(part)
		if name == "" || strings.TrimSpace(rest) != "" {
			return nil, false, false
		}
		members = append(members, name)
	}
	if quotedCount != 0 && quotedCount != len(members) {
		return nil, false, false
	}
	return members, quotedCount == len(members), true
}

// searchClosedSetGoConstGroup reads a Go typed const group: `type Level int` plus a `const (...)`
// block in the same file whose first entry declares that type. The bare identifiers that follow
// belong to the same group by Go's own iota rule, which is why they need no repeated type.
func searchClosedSetGoConstGroup(lines []string, symbol SymbolRecord) ([]string, bool) {
	if symbol.Language != "Go" {
		return nil, false
	}
	members := make([]string, 0, 8)
	inBlock, typed := false, false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if strings.HasPrefix(trimmed, "const (") {
				inBlock, typed = true, false
				members = members[:0]
			}
			continue
		}
		if trimmed == ")" {
			if typed && len(members) >= searchClosedSetMinMembers {
				return members, true
			}
			inBlock = false
			continue
		}
		if trimmed == "" || searchClosedSetCommentLine(trimmed) {
			continue
		}
		name, rest := searchLeadingIdentifier(trimmed)
		if name == "" {
			continue
		}
		rest = strings.TrimSpace(rest)
		if declared, _ := searchLeadingIdentifier(rest); declared == symbol.Name {
			typed = true
		}
		members = append(members, name)
		if len(members) > searchClosedSetMaxMembers {
			return nil, false
		}
	}
	return nil, false
}

// readLines returns a file's lines through the needle index's shared read budget.
func (index *searchNeedleIndex) readLines(filePath string) ([]string, bool) {
	if index == nil || index.read == nil {
		return nil, false
	}
	if index.reads >= searchNeedleMaxReads {
		return nil, false
	}
	index.reads++
	content, ok := index.read(filePath)
	if !ok || strings.IndexByte(content, 0) >= 0 {
		return nil, false
	}
	return strings.Split(content, "\n"), true
}

// searchClosedSetSites finds the switch/match statements over a set.
//
// The candidate files are the set's own declaration file, every file the payload already names, and
// — when the set's name is reachable through the repository-wide literal lookup — every file that
// mentions it. Nothing here walks the repository.
func searchClosedSetSites(
	set searchClosedVariantSet,
	results []SearchResult,
	symbolsByFile map[string][]SymbolRecord,
	index *searchNeedleIndex,
) []SearchClosedSetSite {
	fallback := make([]string, 0, len(results)+1)
	if set.declaredIn != "" {
		fallback = append(fallback, set.declaredIn)
	}
	for _, result := range results {
		if result.FilePath != "" && !NonProgramTextPath(result.FilePath) {
			fallback = append(fallback, result.FilePath)
		}
	}
	paths, found := index.candidatePaths(set.name, fallback)
	if !found {
		return nil
	}
	ordered := searchClosedSetOrderedPaths(paths, set.declaredIn)
	sites := make([]SearchClosedSetSite, 0, searchClosedSetSiteLimit)
	scanned := 0
	for _, filePath := range ordered {
		if scanned >= searchClosedSetMaxFiles {
			break
		}
		lines, ok := index.readLines(filePath)
		if !ok {
			continue
		}
		scanned++
		for _, site := range searchClosedSetSitesInFile(set, filePath, lines, symbolsByFile[filePath]) {
			sites = append(sites, site)
		}
	}
	// The site that fails silently at build time is the one worth the bytes, so it sorts first.
	sort.SliceStable(sites, func(left, right int) bool {
		return searchClosedSetSiteRisk(sites[left]) > searchClosedSetSiteRisk(sites[right])
	})
	if len(sites) > searchClosedSetSiteLimit {
		sites = sites[:searchClosedSetSiteLimit]
	}
	return sites
}

// searchClosedSetOrderedPaths puts the declaring file first and deduplicates the rest.
func searchClosedSetOrderedPaths(paths []string, declaredIn string) []string {
	seen := map[string]bool{}
	ordered := make([]string, 0, len(paths)+1)
	if declaredIn != "" {
		seen[declaredIn] = true
		ordered = append(ordered, declaredIn)
	}
	for _, filePath := range paths {
		if filePath == "" || seen[filePath] {
			continue
		}
		seen[filePath] = true
		ordered = append(ordered, filePath)
	}
	return ordered
}

// searchClosedSetSiteRisk ranks a site by how badly it fails when a variant is added.
//
// Only a RUNTIME-checked site can fail silently at build time, so a compiler-checked one scores
// zero — the compiler is already the warning. Among runtime sites, a throwing fall-through is the
// worst (the new variant reaches production and crashes), an absent one next (it falls out of the
// switch and does whatever comes after), and a fall-through that quietly returns something scores
// zero: that may be exactly what the author intended, and warning about intent is guessing.
//
// Exhaustiveness raises the rank but is not required. A switch that already misses a variant is not
// safer for it; the missing variant is already reaching the throw.
func searchClosedSetSiteRisk(site SearchClosedSetSite) int {
	if site.Checked != searchClosedSetCheckedRuntime {
		return 0
	}
	switch site.Default {
	case searchClosedSetDefaultThrows:
		if site.Exhaustive {
			return 4
		}
		return 3
	case searchClosedSetDefaultAbsent:
		if site.Exhaustive {
			return 2
		}
		return 1
	}
	return 0
}

// searchClosedSetSitesInFile scans one file for switch/match blocks over the set.
func searchClosedSetSitesInFile(
	set searchClosedVariantSet,
	filePath string,
	lines []string,
	symbols []SymbolRecord,
) []SearchClosedSetSite {
	var sites []SearchClosedSetSite
	for offset, line := range lines {
		construct, ok := searchClosedSetConstruct(line)
		if !ok {
			continue
		}
		site, found := searchClosedSetScanBlock(set, construct, lines, offset)
		if !found {
			continue
		}
		site.FilePath = filePath
		if enclosing, ok := smallestSearchSymbolContainingLineWhere(
			symbols, site.Line,
			func(symbol SymbolRecord) bool { return searchEnclosableSymbolKind(symbol.Kind) },
		); ok {
			site.Symbol = searchLiteralSymbolName(enclosing)
		}
		sites = append(sites, site)
	}
	return sites
}

// searchClosedSetConstruct reports which dispatch construct a line opens, if any.
func searchClosedSetConstruct(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	for _, keyword := range []string{"switch", "match", "when"} {
		rest, found := strings.CutPrefix(trimmed, keyword)
		if !found {
			continue
		}
		if rest == "" || rest[0] == ' ' || rest[0] == '(' || rest[0] == '\t' {
			return keyword, true
		}
	}
	// `return match x {`, `let y = match x {`, `= when (x) {`: the construct is not at the start of
	// the line but the block still follows it.
	for _, keyword := range []string{" match ", " when ", "= match ", "= when "} {
		if strings.Contains(trimmed, keyword) {
			return strings.TrimSpace(strings.Trim(keyword, "= ")), true
		}
	}
	return "", false
}

// searchClosedSetScanBlock reads one dispatch block and reports what it does with the set.
//
// The block's extent is decided by indentation rather than by brace counting: it is the same answer
// in every language in scope, it survives arms that contain braces, and a scan that over-reads by a
// few lines can only add arms the block really does handle.
func searchClosedSetScanBlock(
	set searchClosedVariantSet,
	construct string,
	lines []string,
	headerOffset int,
) (SearchClosedSetSite, bool) {
	header := lines[headerOffset]
	headerIndent := searchLineIndent(header)
	matched := map[string]bool{}
	defaultArm := searchClosedSetDefaultAbsent
	wildcard := false
	never := false
	end := minInt(len(lines), headerOffset+1+searchClosedSetBlockLines)
	for offset := headerOffset + 1; offset < end; offset++ {
		line := lines[offset]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if searchLineIndent(line) <= headerIndent && !searchClosedSetContinuation(trimmed) {
			break
		}
		if strings.Contains(trimmed, ": never") || strings.Contains(trimmed, ":never") {
			never = true
		}
		for _, member := range set.members {
			if searchClosedSetArmNames(trimmed, member, set.quoted) {
				matched[member] = true
			}
		}
		if kind, ok := searchClosedSetDefaultArm(trimmed); ok {
			if kind == "wildcard" {
				wildcard = true
			}
			if defaultArm == searchClosedSetDefaultAbsent {
				defaultArm = searchClosedSetDefaultSilent
			}
			if searchClosedSetThrows(lines, offset, end) {
				defaultArm = searchClosedSetDefaultThrows
			}
		}
	}
	if len(matched) < searchClosedSetMinArms {
		return SearchClosedSetSite{}, false
	}
	return SearchClosedSetSite{
		Line:       headerOffset + 1,
		Arms:       len(matched),
		Exhaustive: len(matched) == len(set.members),
		Default:    defaultArm,
		Checked:    searchClosedSetChecked(set, construct, header, wildcard, never),
	}, true
}

// searchClosedSetContinuation keeps a closing brace or a dedented arm from ending the block early.
func searchClosedSetContinuation(trimmed string) bool {
	switch trimmed {
	case "{", "}", "};", ")", ");", "):":
		return true
	}
	return strings.HasPrefix(trimmed, "case ") || strings.HasPrefix(trimmed, "default")
}

func searchLineIndent(line string) int {
	indent := 0
	for offset := 0; offset < len(line); offset++ {
		switch line[offset] {
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

// searchClosedSetArmNames reports whether a line is an ARM naming a member. It requires both an arm
// marker and a whole-word mention, so a member merely referenced inside an arm's body does not count
// as handling it.
func searchClosedSetArmNames(trimmed, member string, quoted bool) bool {
	isArm := strings.HasPrefix(trimmed, "case ") ||
		strings.HasPrefix(trimmed, "case(") ||
		strings.HasPrefix(trimmed, "is ") ||
		strings.HasPrefix(trimmed, "when ") ||
		strings.Contains(trimmed, "=>") ||
		strings.Contains(trimmed, "->")
	if !isArm {
		return false
	}
	if quoted {
		return strings.Contains(trimmed, `"`+member+`"`) || strings.Contains(trimmed, `'`+member+`'`)
	}
	return searchContainsWord(trimmed, member)
}

// searchContainsWord reports a whole-identifier occurrence of a name.
func searchContainsWord(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	for offset := 0; ; {
		index := strings.Index(haystack[offset:], needle)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(needle)
		beforeOK := start == 0 || !searchIdentifierByte(haystack[start-1])
		afterOK := end == len(haystack) || !searchIdentifierByte(haystack[end])
		if beforeOK && afterOK {
			return true
		}
		offset = end
	}
}

// searchClosedSetDefaultArm reports whether a line is the fall-through arm, and whether it is a
// wildcard pattern (which is what makes a Rust match non-exhaustive-checked).
func searchClosedSetDefaultArm(trimmed string) (string, bool) {
	switch {
	case strings.HasPrefix(trimmed, "default:"), strings.HasPrefix(trimmed, "default =>"),
		strings.HasPrefix(trimmed, "default {"), trimmed == "default":
		return "default", true
	case strings.HasPrefix(trimmed, "_ =>"), strings.HasPrefix(trimmed, "_=>"),
		strings.HasPrefix(trimmed, "_ |"), strings.HasPrefix(trimmed, "case _"):
		return "wildcard", true
	case strings.HasPrefix(trimmed, "else ->"), strings.HasPrefix(trimmed, "else:"),
		strings.HasPrefix(trimmed, "else =>"), strings.HasPrefix(trimmed, "else {"):
		return "else", true
	}
	return "", false
}

// searchClosedSetThrowMarkers are the ways the languages in scope spell "this cannot happen, die".
var searchClosedSetThrowMarkers = []string{
	"throw", "panic(", "raise ", "unreachable!", "todo!", "unimplemented!",
	"abort(", "fatalerror", "os.exit", "die(", "assert(", "exit(",
}

// searchClosedSetThrows reports whether a fall-through arm is fatal, looking at the arm's own line
// and a few lines after it.
func searchClosedSetThrows(lines []string, armOffset, end int) bool {
	limit := minInt(end, armOffset+1+searchClosedSetDefaultLookahead)
	for offset := armOffset; offset < limit; offset++ {
		lowered := strings.ToLower(lines[offset])
		for _, marker := range searchClosedSetThrowMarkers {
			if strings.Contains(lowered, marker) {
				return true
			}
		}
	}
	return false
}

// searchClosedSetChecked decides where a missing arm surfaces. Everything not positively known to be
// compiler-checked is reported as runtime, which is the conservative direction: it can only make the
// warning appear where the compiler would in fact have caught it, never suppress it where it would
// not.
func searchClosedSetChecked(
	set searchClosedVariantSet,
	construct, header string,
	wildcard, never bool,
) string {
	language := strings.ToLower(set.language)
	switch {
	case construct == "match" && language == "rust" && !wildcard:
		return searchClosedSetCheckedCompiler
	case construct == "when" && language == "kotlin" && searchClosedSetExpressionHeader(header):
		return searchClosedSetCheckedCompiler
	case never:
		return searchClosedSetCheckedCompiler
	}
	return searchClosedSetCheckedRuntime
}

// searchClosedSetExpressionHeader reports whether a Kotlin `when` is used as an expression, which is
// the form the compiler requires to be exhaustive. A statement `when` is not checked.
func searchClosedSetExpressionHeader(header string) bool {
	trimmed := strings.TrimSpace(header)
	return strings.Contains(trimmed, "= when") || strings.HasPrefix(trimmed, "return when")
}

// searchClosedSetWarning states the consequence for the riskiest site, or "" when the compiler
// already guards every site found.
func searchClosedSetWarning(sites []SearchClosedSetSite) string {
	for _, site := range sites {
		if searchClosedSetSiteRisk(site) < 1 {
			continue
		}
		if site.Default == searchClosedSetDefaultThrows {
			return "adding a variant without adding an arm compiles and then throws at runtime"
		}
		return "adding a variant without adding an arm compiles and then falls out of the switch"
	}
	return ""
}

// searchClosedSetCost measures the block on the larger of its two wire forms.
func searchClosedSetCost(block *SearchClosedSet) int {
	if block == nil {
		return 0
	}
	encoded, err := json.Marshal(block)
	if err != nil {
		return 0
	}
	return maxInt(len(encoded), len(RenderSearchClosedSet(block)))
}

// RenderSearchClosedSet renders the block for a text reader. The warning comes first: it is the
// reason the block exists, and a reader who stops after one line has still been told the thing that
// prevents the wrong patch.
func RenderSearchClosedSet(block *SearchClosedSet) []byte {
	if block == nil || len(block.Sites) == 0 {
		return nil
	}
	var buffer strings.Builder
	// termsafe.Line, not the writer wrap, for the same reason the site lines below use it: this is
	// a one-record-per-line block, and Type is a name read off the scanned repository. A newline in
	// it would split this record and let a file print a line the search never emitted. Kind and
	// Warning are tool-authored today; they are escaped too so that stays true by construction
	// rather than by anyone remembering. See internal/cli/search_forgery.go for the same problem in
	// the one place it cannot be solved this way — a snippet body, whose newlines are its structure.
	fmt.Fprintf(&buffer, "CLOSED SET %s (%s, %d variants): %s\n",
		termsafe.Line(block.Type), termsafe.Line(block.Kind), block.Variants, termsafe.Line(block.Warning))
	for _, site := range block.Sites {
		coverage := fmt.Sprintf("%d/%d arms", site.Arms, block.Variants)
		if site.Exhaustive {
			coverage = "exhaustive"
		}
		fmt.Fprintf(&buffer, "  %s:%d", termsafe.Line(site.FilePath), site.Line)
		if site.Symbol != "" {
			fmt.Fprintf(&buffer, " %s", termsafe.Line(site.Symbol))
		}
		fmt.Fprintf(&buffer, " %s, default %s, checked at %s\n", coverage, site.Default, site.Checked)
	}
	return []byte(buffer.String())
}
