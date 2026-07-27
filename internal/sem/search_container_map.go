package sem

import (
	"fmt"
	"sort"
	"strings"
)

// The container map
// =================
//
// A ranked hit is a member-level answer: "the thing you asked about is at file:line, and here
// is its body". It says nothing about the rest of the unit that member lives in — and that is
// the piece an agent needs in order to decide whether it may read a RANGE instead of the whole
// file.
//
// Measured on a real session (apache/druid 13704, "add a pow function to
// ArithmeticPostAggregator"): the fix needed lines 198-282 of a 353-line file, 1,567 bytes. The
// agent read all 11.2 kB of the file — 5.3x waste — and its own account of why is precise: the
// payload gave it one member-level hit with no map of the file's other members, so it did not
// know where the variant definition (`private enum Ops`, lines 213-282) was, how long the file
// was, or whether there was a registry to update. Two lines past the end of the returned
// snippet sat the entire subject of the issue, and nothing in the payload said so. Reading a
// range would have been a gamble; reading everything was the rational move.
//
// The map removes the gamble. For the top hit it emits the file's EXTENT and the enclosing
// container's members — line ranges and a one-line identity each, the hit's own member marked —
// so a range read can be sized from the first response.
//
// What it deliberately does NOT contain:
//
//   - member BODIES, or any source line at all. Every region in the payload is printed exactly
//     once; the map is an index, not a second copy of the code.
//   - co-change statistics. They need a history walk and an agent said plainly it would ignore
//     them.
//   - anything inferred by a model. Every field is read off the graph's own symbol records and
//     the declaration text, so the map is deterministic like the rest of the provider.
//
// Cost: ~500 B in the first response. A turn is ~40k tokens and 95.9% of billed tokens are
// context re-read, so the map pays for itself roughly 50x over if it removes even one read —
// and it is bounded (see the caps below) so it can never grow into the cost it is preventing.
const (
	// searchContainerMapMaxMembers caps the member list. Past this the container is a
	// utility class or a large module, "the member beside the hit" stops being a specific
	// answer, and the remainder is reported honestly as a count instead of as bytes.
	searchContainerMapMaxMembers = 18

	// searchContainerMapMaxFields caps the collapsed field line. Fields are named, never
	// ranged: the value of knowing a class has an `op:Ops` field is the name and the type,
	// and a constant table of 200 names is not worth the budget.
	searchContainerMapMaxFields = 10

	// searchContainerMapOverloadGapLines is how far apart two same-named declarations may be and
	// still be reported as one overload set. A blank line between overloads is normal; anything
	// wider means something else is declared in between, and a merged range would then claim
	// lines that are not part of the set.
	searchContainerMapOverloadGapLines = 2

	// searchContainerMapMaxParams is how long a rendered parameter list may be before it
	// degrades to an arity. A list past this is a wide constructor whose types are no longer
	// telling an agent which member this is; the count still is.
	searchContainerMapMaxParams = 38

	// searchContainerMapMaxBytes bounds the whole block, measured on the LARGER of its two wire
	// forms (the rendered text and the JSON object), because a caller pays whichever one it
	// asked for and the JSON form is about twice the text. It is a ceiling, not a target: a
	// small container costs a fraction of it, and the block only reaches the ceiling for a
	// container wide enough that its member list is the whole point.
	//
	// 1.5 kB ~= 380 tokens ~= 1% of one agent turn, against a measured 11.2 kB whole-file read
	// (2.8k tokens) that the map exists to replace.
	searchContainerMapMaxBytes = 1536
)

// SearchContainerMember is one member of the mapped container: where it is, what it is, and
// whether it is the member the ranked hit landed in.
type SearchContainerMember struct {
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	// Params is the member's parameter list compacted to the tokens that identify it — the
	// parameter TYPES where the language writes them — or `(N args)` when that does not fit.
	Params string `json:"params,omitempty"`
	// Flags are the structural facts that change how a member is treated: NESTED, PRIVATE, and
	// at most one of abstract/static/async. Never prose, never model-inferred.
	Flags []string `json:"flags,omitempty"`
	// Members counts what a member that is itself a container holds, so a nested type reports
	// how much code sits inside it without the map having to descend into it.
	Members int `json:"members,omitempty"`
	// Overloads counts the declarations collapsed into this row when a container declares an
	// overload set back to back. 0 and 1 both mean "one declaration".
	Overloads int `json:"overloads,omitempty"`
	// Hit marks the member the top-ranked result lies in.
	Hit bool `json:"hit,omitempty"`
}

// SearchContainerMap is the map of the top hit's enclosing container.
type SearchContainerMap struct {
	FilePath string `json:"file_path"`
	// FileLines is the file's total line count — the number an agent needs to size a range
	// read, and the single most requested missing field in the session this block comes from.
	FileLines int `json:"file_lines"`
	// Kind/Name/StartLine/EndLine describe the container. They are empty and 0 when the hit is
	// a top-level declaration: the container is then the file itself, and Members lists its
	// top level.
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	// Fields are the container's data members, names only (see searchContainerMapMaxFields).
	Fields []string `json:"fields,omitempty"`
	// FieldsOmitted counts fields past the cap.
	FieldsOmitted int                     `json:"fields_omitted,omitempty"`
	Members       []SearchContainerMember `json:"members"`
	// MembersOmitted counts members past the cap, so a truncated map says so.
	MembersOmitted int `json:"members_omitted,omitempty"`
	// AnchorRank is the rank of the result this map was built for.
	AnchorRank int `json:"anchor_rank,omitempty"`
}

// searchContainerMapFieldKind reports whether a member is a data member, which the map collapses
// into one names-only line rather than giving it a ranged row.
func searchContainerMapFieldKind(kind string) bool {
	switch kind {
	case "field", "property", "constant", "variable", "attribute":
		return true
	}
	return false
}

// buildSearchContainerMap builds the map for the highest-ranked primary result that resolves to a
// symbol. It returns nil when there is nothing to map: no ranked code hit, no symbol records for
// its file, or an unreadable file.
//
// The anchor is the top hit's enclosing CALLABLE (the same resolution the complete-body allocator
// and the related-site block use, so all three describe the same member), and the mapped container
// is that callable's container. A hit on a top-level declaration maps the file's top level.
func buildSearchContainerMap(
	results []SearchResult,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	read contentReader,
) *SearchContainerMap {
	for _, result := range results {
		if result.Section != searchSectionPrimary {
			continue
		}
		fileSymbols := symbolsByFile[result.FilePath]
		if len(fileSymbols) == 0 {
			continue
		}
		anchor, ok := searchContainerMapAnchor(result, symbolsByID, fileSymbols)
		if !ok {
			continue
		}
		// Read last: the file extent is the only thing the map needs the content for, so a
		// result whose member cannot be resolved costs no IO. Ranked files are already in the
		// shared content cache, so this read is normally free.
		content, ok := read(result.FilePath)
		if !ok || strings.IndexByte(content, 0) >= 0 {
			continue
		}
		return searchContainerMapFor(result, anchor, fileSymbols, countLines(content))
	}
	return nil
}

// searchContainerMapAnchor resolves the member a result lies in. It prefers the result's own
// callable (so the marked member is the one whose body the payload returned) and falls back to
// the smallest symbol containing the hit, which recovers a member for region hits that carry no
// symbol of their own.
func searchContainerMapAnchor(
	result SearchResult,
	symbolsByID map[string]SymbolRecord,
	fileSymbols []SymbolRecord,
) (SymbolRecord, bool) {
	if symbol, ok := enclosingCallableForResult(result, symbolsByID, symbolsByFileForPath(fileSymbols, result.FilePath)); ok {
		return symbol, true
	}
	return smallestSearchSymbolContainingLineWhere(
		fileSymbols, searchContainerMapAnchorLine(result),
		func(SymbolRecord) bool { return true },
	)
}

// symbolsByFileForPath adapts a single file's records to the by-file map shape
// enclosingCallableForResult expects, without copying the slice.
func symbolsByFileForPath(fileSymbols []SymbolRecord, path string) map[string][]SymbolRecord {
	return map[string][]SymbolRecord{path: fileSymbols}
}

func searchContainerMapAnchorLine(result SearchResult) int {
	if result.FocusLine > 0 {
		return result.FocusLine
	}
	return result.StartLine
}

// searchContainerMapFor assembles the map around a resolved anchor.
func searchContainerMapFor(
	result SearchResult,
	anchor SymbolRecord,
	fileSymbols []SymbolRecord,
	fileLines int,
) *SearchContainerMap {
	container, hasContainer := searchSymbolByID(fileSymbols, anchor.ContainerID)
	if hasContainer && container.ID == anchor.ID {
		// Defensive: a symbol can never be its own container, and mapping one as its own
		// parent would list it beside its siblings while marking nothing.
		hasContainer = false
	}
	if !hasContainer && anchorIsContainer(anchor) {
		// The hit landed on a container rather than inside one — a class-level region, or a
		// top-level type. Map THAT container: its members are exactly what the payload did not
		// show. The anchor is then the map's subject rather than one of its rows, so no row is
		// marked.
		container, hasContainer = anchor, true
	}
	out := &SearchContainerMap{
		FilePath:   result.FilePath,
		FileLines:  fileLines,
		AnchorRank: result.Rank,
	}
	containerID := ""
	hitID := anchor.ID
	if hasContainer {
		out.Kind, out.Name = container.Kind, containerDisplayName(container)
		out.StartLine, out.EndLine = container.StartLine, container.EndLine
		containerID = container.ID
		if container.ID == anchor.ID {
			hitID = ""
		}
	}
	members := searchContainerMapMembers(fileSymbols, containerID)
	if len(members) == 0 {
		return nil
	}
	fields, fieldsOmitted, rows := splitSearchContainerMembers(members, fileSymbols, hitID)
	out.Fields, out.FieldsOmitted = fields, fieldsOmitted
	rows, omitted := capSearchContainerMembers(rows, searchContainerMapMaxMembers)
	out.Members, out.MembersOmitted = rows, omitted
	if len(out.Members) == 0 {
		return nil
	}
	return out
}

// anchorIsContainer reports whether a symbol can sensibly be mapped as a container in its own
// right — it holds members rather than code.
func anchorIsContainer(symbol SymbolRecord) bool {
	return typeLikeKind(symbol.Kind) || symbol.Kind == "module" || symbol.Kind == "namespace"
}

// containerDisplayName prefers the qualified name, which is what tells an agent that a mapped
// container is itself nested.
func containerDisplayName(symbol SymbolRecord) string {
	if symbol.QualifiedName != "" {
		return symbol.QualifiedName
	}
	return symbol.Name
}

// searchContainerMapMembers returns the direct members of a container, in source order. An empty
// container id selects the file's top level (every symbol with no container of its own).
func searchContainerMapMembers(fileSymbols []SymbolRecord, containerID string) []SymbolRecord {
	var out []SymbolRecord
	for _, symbol := range fileSymbols {
		if symbol.ID == "" || symbol.ContainerID != containerID {
			continue
		}
		out = append(out, symbol)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartLine != out[j].StartLine {
			return out[i].StartLine < out[j].StartLine
		}
		return out[i].EndLine < out[j].EndLine
	})
	return out
}

// splitSearchContainerMembers separates data members (collapsed to a names-only line) from the
// ranged rows, collapsing an overload set declared back to back into one row so a class with
// four constructors does not spend four rows saying so.
func splitSearchContainerMembers(
	members, fileSymbols []SymbolRecord, hitID string,
) ([]string, int, []SearchContainerMember) {
	var fields []string
	fieldsOmitted := 0
	var rows []SearchContainerMember
	for _, symbol := range members {
		if searchContainerMapFieldKind(symbol.Kind) {
			if len(fields) < searchContainerMapMaxFields {
				fields = append(fields, searchContainerMapFieldLabel(symbol))
			} else {
				fieldsOmitted++
			}
			continue
		}
		row := SearchContainerMember{
			Kind:      searchContainerMapRowKind(symbol),
			Name:      symbol.Name,
			StartLine: symbol.StartLine,
			EndLine:   symbol.EndLine,
			Params:    compactParameterList(symbol.Signature),
			Flags:     searchContainerMemberFlags(symbol),
			Hit:       hitID != "" && symbol.ID == hitID,
		}
		if anchorIsContainer(symbol) {
			row.Members = len(searchContainerMapMembers(fileSymbols, symbol.ID))
		}
		if last := len(rows) - 1; last >= 0 && mergeableSearchContainerRows(rows[last], row) {
			rows[last] = mergeSearchContainerRows(rows[last], row)
			continue
		}
		rows = append(rows, row)
	}
	return fields, fieldsOmitted, rows
}

// mergeableSearchContainerRows reports whether two rows are the same overload set: the same name
// and kind, declared back to back. Both bounds matter for honesty — only rows ADJACENT in the
// member list merge, and only across a blank-line-sized gap — so the merged range stays a span of
// the file that really does contain nothing but those declarations.
func mergeableSearchContainerRows(left, right SearchContainerMember) bool {
	return left.Name == right.Name && left.Kind == right.Kind &&
		left.Members == 0 && right.Members == 0 &&
		right.StartLine >= left.EndLine &&
		right.StartLine-left.EndLine <= searchContainerMapOverloadGapLines
}

func mergeSearchContainerRows(left, right SearchContainerMember) SearchContainerMember {
	merged := left
	merged.EndLine = maxInt(left.EndLine, right.EndLine)
	merged.Hit = left.Hit || right.Hit
	merged.Overloads = maxInt(left.Overloads, 1) + 1
	// The identity of an overload set is the set, not one arbitrary member's signature.
	merged.Params = ""
	return merged
}

// capSearchContainerMembers trims the row list to `limit`, keeping what an agent navigating from
// the hit actually needs: the hit's own member, then every member that is itself a container
// (nested types are the structural landmarks of a file and the reason the map exists at all),
// then the members nearest the hit. Survivors are returned in source order.
func capSearchContainerMembers(rows []SearchContainerMember, limit int) ([]SearchContainerMember, int) {
	if limit <= 0 || len(rows) <= limit {
		return rows, 0
	}
	hitLine := 0
	for _, row := range rows {
		if row.Hit {
			hitLine = row.StartLine
			break
		}
	}
	order := make([]int, len(rows))
	for index := range order {
		order[index] = index
	}
	sort.SliceStable(order, func(i, j int) bool {
		left, right := rows[order[i]], rows[order[j]]
		if left.Hit != right.Hit {
			return left.Hit
		}
		if leftLandmark, rightLandmark :=
			searchContainerMapLandmarkRow(left), searchContainerMapLandmarkRow(right); leftLandmark != rightLandmark {
			return leftLandmark
		}
		return absInt(left.StartLine-hitLine) < absInt(right.StartLine-hitLine)
	})
	keep := make(map[int]bool, limit)
	for _, index := range order[:limit] {
		keep[index] = true
	}
	out := make([]SearchContainerMember, 0, limit)
	for index, row := range rows {
		if keep[index] {
			out = append(out, row)
		}
	}
	return out, len(rows) - len(out)
}

// searchContainerMapLandmarkRow reports whether a row is a nested declaration — a type, module or
// namespace declared inside the mapped container. Those are the rows a truncated map keeps after
// the hit itself: they are the only members the ranking cannot reach on its own, and each one is a
// whole subtree of code whose line range the caller otherwise has no way to learn.
func searchContainerMapLandmarkRow(row SearchContainerMember) bool {
	return row.Members > 0 || typeLikeKind(row.Kind) || row.Kind == "module" || row.Kind == "namespace"
}

// searchContainerMapRowKind reports the kind worth printing beside a member name. A plain
// callable prints none: `compute(Map)` already says what it is, and the map's budget is better
// spent on the members whose kind is the surprise (a nested enum, an inner class, a type alias).
func searchContainerMapRowKind(symbol SymbolRecord) string {
	switch symbol.Kind {
	case "function", "method", "constructor", "destructor", "accessor", "initializer",
		"procedure", "subroutine", "operator", "closure", "lambda":
		return ""
	}
	return symbol.Kind
}

// searchContainerMapFieldLabel renders a data member as `name:Type` when the declaration states a
// type, and as `name` otherwise. The type is the half that says what the field is FOR.
func searchContainerMapFieldLabel(symbol SymbolRecord) string {
	if fieldType := declaredFieldType(symbol); fieldType != "" {
		return symbol.Name + ":" + fieldType
	}
	return symbol.Name
}

// searchContainerMemberFlags reports the structural facts about a declaration that change how an
// agent treats it, read off the declaration text. At most two are kept: the list is an identity,
// not a modifier dump.
//
// NESTED and PRIVATE lead because they are the two that made a real fix site unfindable: the
// declaration the agent needed was a PRIVATE NESTED enum, and both facts are invisible in a
// member-ranked hit list.
func searchContainerMemberFlags(symbol SymbolRecord) []string {
	var flags []string
	if anchorIsContainer(symbol) {
		flags = append(flags, "NESTED")
	}
	if declaredPrivate(symbol) {
		flags = append(flags, "PRIVATE")
	}
	for _, modifier := range []string{"abstract", "static", "async"} {
		if len(flags) >= 2 {
			break
		}
		if signatureHasModifier(symbol.Signature, modifier) {
			flags = append(flags, modifier)
		}
	}
	if len(flags) > 2 {
		flags = flags[:2]
	}
	return flags
}

// declaredPrivate reports whether a declaration is not part of its unit's public surface. It
// reads the explicit keyword where a language has one, and otherwise the language's naming
// convention (a leading underscore, or a lowercase initial in Go).
func declaredPrivate(symbol SymbolRecord) bool {
	for _, keyword := range []string{"private", "fileprivate", "protected", "internal"} {
		if signatureHasModifier(symbol.Signature, keyword) {
			return true
		}
	}
	switch symbol.Language {
	case "Python", "Ruby", "JavaScript", "TypeScript":
		return strings.HasPrefix(symbol.Name, "_") || strings.HasPrefix(symbol.Name, "#")
	case "Go":
		return symbol.Name != "" && strings.ToLower(symbol.Name[:1]) == symbol.Name[:1] &&
			strings.ToUpper(symbol.Name[:1]) != symbol.Name[:1]
	}
	return false
}

// signatureHasModifier reports whether a signature carries a modifier as a WHOLE WORD, so
// `internalise()` is not read as `internal` and a parameter named `static` is not a modifier.
func signatureHasModifier(signature, modifier string) bool {
	lower := strings.ToLower(signature)
	if cut := strings.IndexByte(lower, '('); cut >= 0 {
		lower = lower[:cut]
	}
	for _, field := range strings.FieldsFunc(lower, func(r rune) bool {
		return r != '_' && (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		if field == modifier {
			return true
		}
	}
	return false
}

// compactParameterList extracts a signature's parameter list and reduces it to the part that
// identifies the member: the last word of each parameter is dropped when the language writes
// `Type name` (Java, Go, C#, C++), because the TYPES are what distinguish overloads. A parameter
// list that does not fit the identity budget degrades to an arity (`(3 args)`).
func compactParameterList(signature string) string {
	open, close, ok := parameterListBounds(signature)
	if !ok {
		return ""
	}
	inner := strings.TrimSpace(signature[open+1 : close])
	if inner == "" {
		return "()"
	}
	parts := splitTopLevelParameters(inner)
	compact := make([]string, 0, len(parts))
	for _, part := range parts {
		if name := parameterIdentity(part); name != "" {
			compact = append(compact, name)
		}
	}
	if len(compact) == 0 {
		return fmt.Sprintf("(%d args)", len(parts))
	}
	joined := "(" + strings.Join(compact, ",") + ")"
	if len(joined) > searchContainerMapMaxParams {
		return fmt.Sprintf("(%d args)", len(parts))
	}
	return joined
}

// parameterListBounds locates a signature's parameter list: the LAST balanced parenthesis group.
// Scanning from the end matters because a declaration's earlier parentheses belong to something
// else — a Java annotation (`@JsonProperty("fn") public String getFnName()`), a Go receiver
// (`func (o *Outer) Run() int`), a C++ attribute — and taking the first `(` reported those as
// the member's parameters, so a no-argument getter came back as taking a String.
func parameterListBounds(signature string) (int, int, bool) {
	close := strings.LastIndexByte(signature, ')')
	if close < 0 {
		return 0, 0, false
	}
	depth := 0
	for index := close; index >= 0; index-- {
		switch signature[index] {
		case ')':
			depth++
		case '(':
			depth--
			if depth == 0 {
				return index, close, true
			}
		}
	}
	return 0, 0, false
}

// splitTopLevelParameters splits a parameter list on commas that are not inside brackets, so a
// generic parameter (`Map<String, Ops> m`) counts once.
func splitTopLevelParameters(inner string) []string {
	var parts []string
	depth := 0
	start := 0
	for index, char := range inner {
		switch char {
		case '<', '(', '[', '{':
			depth++
		case '>', ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(inner[start:index]))
				start = index + 1
			}
		}
	}
	return append(parts, strings.TrimSpace(inner[start:]))
}

// parameterIdentity reduces one parameter declaration to the token that identifies it: its type
// where the declaration states one, its own name otherwise. Annotations, default values and
// generic arguments are dropped — they are never what separates two members of one container.
func parameterIdentity(part string) string {
	part = strings.TrimSpace(part)
	if part == "" || part == "..." {
		return part
	}
	if cut := strings.IndexByte(part, '='); cut >= 0 {
		part = strings.TrimSpace(part[:cut])
	}
	// Generic arguments are dropped BEFORE the declaration is split into words: they contain
	// whitespace of their own (`Map<String, Object> values`), so splitting first made the last
	// argument look like the parameter's type.
	part = stripBracketedArguments(part)
	if cut := strings.IndexByte(part, ':'); cut >= 0 {
		// `name: Type` (TypeScript, Python, Kotlin, Swift): the type is after the colon.
		if typed := strings.TrimSpace(part[cut+1:]); typed != "" {
			part = typed
		} else {
			part = strings.TrimSpace(part[:cut])
		}
	}
	words := strings.Fields(part)
	kept := make([]string, 0, len(words))
	for _, word := range words {
		if strings.HasPrefix(word, "@") || strings.HasPrefix(word, "#") {
			continue
		}
		kept = append(kept, word)
	}
	if len(kept) == 0 {
		return ""
	}
	// `Type name` -> Type; a lone token is the type (or the name, when untyped).
	token := kept[0]
	if len(kept) > 1 {
		token = kept[len(kept)-2]
	}
	token = strings.TrimLeft(token, "*&")
	if cut := strings.LastIndexAny(token, ".:"); cut >= 0 && cut+1 < len(token) {
		token = token[cut+1:]
	}
	return token
}

// stripBracketedArguments removes generic/array argument groups from a type reference, leaving
// the bare type name: `Map<String, Object>` -> `Map`, `List<Ops>[]` -> `List`.
func stripBracketedArguments(value string) string {
	var out strings.Builder
	depth := 0
	for _, char := range value {
		switch char {
		case '<', '[':
			depth++
			continue
		case '>', ']':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth == 0 {
			out.WriteRune(char)
		}
	}
	return strings.TrimSpace(out.String())
}

// declaredFieldType extracts a data member's declared type from its signature, for the collapsed
// field line. It is best-effort by design: a field whose type cannot be read prints as a bare
// name rather than as a guess.
func declaredFieldType(symbol SymbolRecord) string {
	signature := symbol.Signature
	if signature == "" {
		return ""
	}
	if cut := strings.IndexByte(signature, '='); cut >= 0 {
		signature = signature[:cut]
	}
	if cut := strings.IndexByte(signature, ':'); cut >= 0 {
		if typed := parameterIdentity(strings.TrimSpace(signature[cut+1:])); typed != "" && typed != symbol.Name {
			return typed
		}
		return ""
	}
	// Field signatures do not agree on word order across languages — the parsers emit both
	// `name Type` (Java, Go) and `private final Type name` (C#, C++) — so the type is found by
	// ELIMINATION rather than by position: drop the declaration keywords and the field's own
	// name, and the type is what is left.
	var remaining []string
	for _, word := range strings.Fields(stripBracketedArguments(signature)) {
		word = strings.Trim(word, ";,*&")
		if word == "" || word == symbol.Name || searchContainerMapKeyword(word) {
			continue
		}
		remaining = append(remaining, word)
	}
	if len(remaining) == 0 {
		return ""
	}
	return remaining[len(remaining)-1]
}

// searchContainerMapKeyword reports whether a token is a declaration keyword rather than a type,
// so `private final name` does not report `final` as the field's type.
func searchContainerMapKeyword(token string) bool {
	switch strings.ToLower(token) {
	case "final", "static", "const", "var", "let", "readonly", "public", "private", "protected",
		"internal", "volatile", "transient", "mutable", "val", "def", "field", "attr":
		return true
	}
	return false
}

func countLines(content string) int {
	if content == "" {
		return 0
	}
	lines := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		lines++
	}
	return lines
}

// searchContainerMapCost is what a map costs the caller: the larger of its two wire forms, so a
// budget holds however the payload is asked for.
func searchContainerMapCost(containerMap *SearchContainerMap) int {
	return maxInt(
		len(RenderSearchContainerMap(containerMap, false)),
		serializedSearchResultBytes(containerMap),
	)
}

// fitSearchContainerMap shrinks a map until it fits `budget`, dropping in the order that costs an
// agent least.
//
// The order is by measured value per byte, cheapest information last:
//
//  1. parameter lists go first — they are the bulkiest field (~20 B each in JSON) and the least
//     load-bearing: `compute` and `compute(Map)` point at the same line range.
//  2. flags go next, but only after the parameter lists, because NESTED and PRIVATE are the two
//     facts that made a real fix site unfindable and they cost ~20 B for the whole block.
//  3. rows go last, furthest-from-the-hit first, and the count of what went is reported in
//     MembersOmitted — a truncated map always says it is truncated.
//
// Names and line ranges therefore survive every stage but the last: sizing a range read needs the
// file extent and the ranges, and nothing else in the block is load-bearing for that.
func fitSearchContainerMap(containerMap *SearchContainerMap, budget int) *SearchContainerMap {
	if containerMap == nil || budget <= 0 {
		return containerMap
	}
	if searchContainerMapCost(containerMap) <= budget {
		return containerMap
	}
	for _, dropFlags := range []bool{false, true} {
		stripped := *containerMap
		stripped.Members = append([]SearchContainerMember(nil), containerMap.Members...)
		for index := range stripped.Members {
			stripped.Members[index].Params = ""
			if dropFlags {
				stripped.Members[index].Flags = nil
			}
		}
		if searchContainerMapCost(&stripped) <= budget {
			return &stripped
		}
		if !dropFlags {
			continue
		}
		for limit := len(stripped.Members) - 1; limit > 0; limit-- {
			trial := stripped
			rows, omitted := capSearchContainerMembers(stripped.Members, limit)
			trial.Members = rows
			trial.MembersOmitted = containerMap.MembersOmitted + omitted
			if searchContainerMapCost(&trial) <= budget {
				return &trial
			}
		}
	}
	return nil
}

// RenderSearchContainerMap renders the map as the text renderers print it. It is exported
// because every renderer must print the same map: the block is a navigation aid, and two
// formats disagreeing about where a member is would be worse than not printing it.
//
// `compact` drops the identities and flags, keeping names and ranges — the fields a range read
// is actually sized from — for budgets that cannot hold the full block.
func RenderSearchContainerMap(containerMap *SearchContainerMap, compact bool) []byte {
	if containerMap == nil || len(containerMap.Members) == 0 {
		return nil
	}
	var out strings.Builder
	fmt.Fprintf(&out, "CONTAINER MAP %s [%d lines]", containerMap.FilePath, containerMap.FileLines)
	if containerMap.Name != "" {
		fmt.Fprintf(&out, " (%s %s, %d-%d)", containerMap.Kind, containerMap.Name, containerMap.StartLine, containerMap.EndLine)
	} else {
		out.WriteString(" (file top level)")
	}
	out.WriteString("\n")
	if len(containerMap.Fields) > 0 {
		fmt.Fprintf(&out, "  fields: %s", strings.Join(containerMap.Fields, ", "))
		if containerMap.FieldsOmitted > 0 {
			fmt.Fprintf(&out, ", +%d more", containerMap.FieldsOmitted)
		}
		out.WriteString("\n")
	}
	for _, member := range containerMap.Members {
		marker := "  "
		if member.Hit {
			marker = " *"
		}
		fmt.Fprintf(&out, "%s %d-%d %s", marker, member.StartLine, member.EndLine, searchContainerMemberLabel(member, compact))
		if !compact {
			if len(member.Flags) > 0 {
				fmt.Fprintf(&out, "  %s", strings.Join(member.Flags, ","))
			}
			if member.Members > 0 {
				fmt.Fprintf(&out, "  +%d member%s", member.Members, pluralS(member.Members))
			}
		}
		if member.Hit {
			fmt.Fprintf(&out, "   <- rank %d", containerMap.AnchorRank)
		}
		out.WriteString("\n")
	}
	if containerMap.MembersOmitted > 0 {
		fmt.Fprintf(&out, "  +%d more members\n", containerMap.MembersOmitted)
	}
	return []byte(out.String())
}

// searchContainerMemberLabel renders `kind name(params)` for a member whose kind is worth
// printing and `name(params)` otherwise, with the overload count appended when a set was
// collapsed. The parameter list rides on the name because `compute(Map)` is one identity, not a
// name and a separate fact about it.
func searchContainerMemberLabel(member SearchContainerMember, compact bool) string {
	label := member.Name
	if !compact {
		label += member.Params
	}
	if member.Kind != "" {
		label = member.Kind + " " + label
	}
	if member.Overloads > 1 {
		label += fmt.Sprintf(" x%d", member.Overloads)
	}
	return label
}

func pluralS(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}
