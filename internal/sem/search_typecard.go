package sem

import (
	"path/filepath"
	"sort"
	"strings"
)

// Type / signature card
// ====================
//
// A ranked list answers "where do I edit?" and the complete-body head answers "what does that
// code say?". Neither answers "what ARE the names that code uses?", and a patch that gets that
// wrong does not compile at all — it is not a worse fix, it is a zero.
//
// Measured on the 31 agentic SWE-bench instances where a no-tool baseline fixed the bug and the
// graph-assisted agent did not DESPITE finding the right file, three losses were exactly this:
//
//   - a block was deleted but the local it consumed was left behind, so Go's "declared and not
//     used" failed the whole package;
//   - a member was written in its bare form where the surrounding call needed the indexed
//     element (array vs element pointer);
//   - a field was addressed on the wrong receiver (`c.maxTime` for `c.prev.maxTime`).
//
// In each case the declaration that settles it was one graph hop from the body the agent was
// looking at, and in each case the agent never saw it — because a snippet shows USES, and a
// declaration is somewhere else.
//
// The card answers it from relations the graph already resolved, for ONE anchor:
//
//	field    a field/property the body reads, writes or accesses, at its declaration
//	type     a type the body takes, returns or constructs, at its declaration
//	call     a callee, at its signature — the parameter and return types of the call being made
//	binding  a name the body itself BINDS and then uses on another line: it has no declaration
//	         anywhere else, and it is the coupling an edit that deletes lines has to respect
//
// Plus one closure step: when an admitted entry's declaration NAMES an indexed type
// (`headChunks *memChunk`), that type's declaration is admitted straight after it. Without it
// the card says what a field is called and still leaves the agent guessing what it holds, which
// is the `c.prev.maxTime` failure verbatim.
//
// It is not a ranking, it is not a fix-site list, and it is bounded by BYTES, not by usefulness:
// the payload is replayed on every subsequent agent turn, so an entry that does not fit is
// simply not there. See searchTypeCardBytes.
const (
	// searchTypeCardBytes is the card's whole serialized allowance: 384 bytes, ~96 tokens, paid
	// once per search and replayed on every later turn of the session. It is a CEILING, never a
	// target — a card with one entry costs one entry, and the measured mean card is well under it.
	//
	// It was 320 first. At 320 the one pair that answers the measured `c.prev.maxTime` failure —
	// the field `headChunks *memChunk` plus the `memChunk` declaration that has `prev` in it —
	// came to 326 bytes and was rejected, so the card showed the field and silently withheld the
	// answer. 320 was not a budget, it was a 6-byte cliff in the commonest language in the corpus.
	searchTypeCardBytes = 384

	// searchTypeCardEntryLimit bounds the card independently of bytes, so a repo whose
	// declarations happen to be very short cannot turn the card into a symbol dump.
	searchTypeCardEntryLimit = 4

	// searchTypeCardDeclBytes caps ONE declaration. A struct flattened by the provider can be
	// hundreds of bytes; the head of it carries the fields, which is what the card is for.
	searchTypeCardDeclBytes = 160

	// searchTypeCardMinConfidence is the floor on the relation that produced a candidate. Below
	// it the graph is guessing, and a card entry that points at the wrong declaration is worse
	// than no card: the agent would edit against it.
	//
	// 0.65 admits every RESOLUTION the graph performs — the weakest it emits for a real call is
	// name-only at 0.68 — and excludes the heuristic tiers below them. A name-only resolution can
	// still name the wrong file, which is what resolveSearchTypeCardTargets exists to correct;
	// excluding it instead would have dropped the C and Java callees entirely.
	searchTypeCardMinConfidence = 0.65

	// searchTypeCardCandidateLimit bounds how many candidates of one kind are considered, and
	// with them the work the card can do on a symbol with hundreds of relations.
	searchTypeCardCandidateLimit = 8

	// searchTypeCardFileReads bounds files hydrated to read declarations verbatim.
	searchTypeCardFileReads = 4

	// searchTypeCardBindingUses caps the use-line list on a binding entry. Two lines say
	// "this is used elsewhere, twice at least"; twenty say nothing more and cost 60 bytes.
	searchTypeCardBindingUses = 2

	// searchTypeCardBlockLines caps how many lines of a multi-line declaration are assembled
	// before the byte cap clips it. 24 declaring lines is more than the cap can print.
	searchTypeCardBlockLines = 24

	// searchTypeCardClosureTierBonus promotes a candidate whose declaration names a type the
	// card can also show. It outweighs every within-kind tier (the widest is 4), because the
	// pair is a complete answer and a lone declaration naming an unknown type is half of one.
	searchTypeCardClosureTierBonus = 8
)

// TypeCardEntry is one declaration behind a name the anchor body uses.
//
// It is deliberately the cheapest record that can be acted on: a name to recognize, a
// `file:line` to open if the entry is not enough, and the declaration itself. No symbol id (a
// stable id is ~110 bytes in Java), no language, no confidence — the card exists to make an
// edit compile, not to be audited.
type TypeCardEntry struct {
	Name string `json:"name"`
	// There is deliberately no kind field. `"kind":"field",` is ~16 bytes per entry and the
	// declaration text says what the thing is more precisely than any label would — and 16 bytes
	// per entry is measurably the difference between a struct's field list fitting and not.
	FilePath string `json:"file_path,omitempty"`
	Line     int    `json:"line,omitempty"`
	// Decl is the declaration, preferred verbatim from the file. A declaration that spans
	// more than one line falls back to the provider's own flattened signature, because the
	// verbatim FIRST line of a struct is `type Foo struct {` — the one part of it that
	// carries no information.
	Decl string `json:"decl"`
	// UseLines are the other lines of the anchor's body that reference a name the body itself
	// binds. Present only on binding entries: such a name has no declaration anywhere else,
	// and the coupling is the whole point of listing it.
	UseLines []int `json:"use_lines,omitempty"`
}

// searchTypeCardKind groups candidates so the card can rotate through kinds instead of letting
// one of them fill every slot.
type searchTypeCardKind int

// The rotation order is the order the kinds get their first slot in, and it is ordered by what
// the measured failures needed: a member the body dereferences, then the type behind it, then
// the signature of what it calls. Bindings come last — a name the body creates is at least
// PRESENT in the body, so listing it adds a coupling rather than information the agent lacks.
const (
	searchTypeCardField searchTypeCardKind = iota
	searchTypeCardType
	searchTypeCardCall
	searchTypeCardBinding
	searchTypeCardKindCount
)

// searchTypeCardCandidate is one admissible entry before the byte budget is applied.
type searchTypeCardCandidate struct {
	kind searchTypeCardKind
	// tier orders candidates INSIDE a kind: for types, "the body takes or returns this" beats
	// "the body merely mentions it". Higher is better.
	tier     int
	strength float64
	symbol   SymbolRecord
	entry    TypeCardEntry
}

// searchTypeCardFieldRelation reports whether a relation means "the anchor's body touches this
// member". A member the body touches is a member an edit can get wrong.
func searchTypeCardFieldRelation(relation string) bool {
	switch relation {
	case "READS_FIELD", "WRITES_FIELD", "ACCESSES":
		return true
	}
	return false
}

// searchTypeCardTypeTier reports how directly a relation ties a type to the anchor's contract,
// or 0 when the relation is not a type relation at all. A type the anchor RETURNS is the type
// its caller writes against; a type it merely mentions is context.
func searchTypeCardTypeTier(relation string) int {
	switch relation {
	case "RETURNS_TYPE":
		return 4
	case "PARAM_TYPE":
		return 3
	case "CONSTRUCTS":
		return 2
	case "USES_TYPE":
		return 1
	}
	return 0
}

func searchTypeCardCallRelation(relation string) bool {
	switch relation {
	case "CALLS", "ASYNC_CALLS":
		return true
	}
	return false
}

// searchTypeCardContainerKind reports whether a symbol kind declares a set of members — the
// only kind of declaration whose body answers "what fields does this have?". It is the gate on
// the closure step, so the card cannot follow `int64` or a callee's name into noise.
func searchTypeCardContainerKind(kind string) bool {
	switch kind {
	case "type", "struct", "class", "interface", "enum", "trait", "record", "union", "protocol":
		return true
	}
	return false
}

// searchTypeCardAnchor picks the ONE symbol the card describes: the top hit's own symbol.
//
// The card is per-anchor by design — running it on every hit would multiply its cost by ten and
// describe nine bodies the agent is not editing. When the top hit carries no symbol the graph
// can say anything about (a one-line field stub, a region hit in an unparsed file), the walk
// continues down the head rather than returning an empty card, because "the first hit that has
// a contract" is still one anchor and still the code the caller is about to read.
//
// Docs-and-fixtures hits are skipped: they are not fix sites, so their names are not the names
// an edit has to compile against.
func searchTypeCardAnchor(
	results []SearchResult,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	limit int,
) []SymbolRecord {
	var anchors []SymbolRecord
	seen := map[string]bool{}
	for _, result := range results {
		if len(anchors) >= limit {
			break
		}
		if result.Section != searchSectionPrimary {
			continue
		}
		symbol, ok := searchTypeCardResultSymbol(result, symbolsByID, symbolsByFile)
		if !ok || seen[symbol.ID] {
			continue
		}
		seen[symbol.ID] = true
		anchors = append(anchors, symbol)
	}
	return anchors
}

func searchTypeCardResultSymbol(
	result SearchResult,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
) (SymbolRecord, bool) {
	if result.SymbolID != "" {
		if symbol, ok := symbolsByID[result.SymbolID]; ok && symbol.ID != "" {
			return symbol, true
		}
	}
	return enclosingCallableForResult(result, symbolsByID, symbolsByFile)
}

// selectSearchTypeCard builds the card for a payload. It walks the head anchors in order and
// returns the first non-empty card, so the result describes exactly one body.
func selectSearchTypeCard(
	results []SearchResult,
	anchors []SymbolRecord,
	relations []RelationRecord,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	read contentReader,
	byteCap int,
) []TypeCardEntry {
	if len(anchors) == 0 || byteCap <= 0 {
		return nil
	}
	cache := newSearchRelatedFileCache(read, searchTypeCardFileReads)
	for _, anchor := range anchors {
		card := searchTypeCardForAnchor(
			results, anchor, relations, symbolsByID, symbolsByFile, cache, byteCap,
		)
		if len(card) > 0 {
			return card
		}
	}
	return nil
}

func searchTypeCardForAnchor(
	results []SearchResult,
	anchor SymbolRecord,
	relations []RelationRecord,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	cache *searchRelatedFileCache,
	byteCap int,
) []TypeCardEntry {
	kinds := searchTypeCardCandidates(anchor, relations, symbolsByID, cache)
	kinds[searchTypeCardBinding] = searchTypeCardBindings(results, anchor, symbolsByFile, cache)
	// The closure index is computed BEFORE the ordering, because "this field's type is itself a
	// declaration we can show" is the strongest thing that can be said about a field: the pair
	// answers "what does this receiver hold", which is the failure the card exists to prevent.
	closures := searchTypeCardClosureIndex(kinds, anchor, symbolsByID)
	ordered := searchTypeCardOrder(kinds, closures)
	if len(ordered) == 0 {
		return nil
	}
	return fitSearchTypeCard(ordered, closures, results, cache, byteCap)
}

// searchTypeCardCandidates collects the field, type and call candidates for one anchor from the
// snapshot's relations. It is a single pass: an anchor with a thousand outgoing edges costs one
// scan, not one scan per kind.
func searchTypeCardCandidates(
	anchor SymbolRecord,
	relations []RelationRecord,
	symbolsByID map[string]SymbolRecord,
	cache *searchRelatedFileCache,
) [][]searchTypeCardCandidate {
	kinds := make([][]searchTypeCardCandidate, searchTypeCardKindCount)
	if anchor.ID == "" {
		return kinds
	}
	anchorInTest := searchTestArtifactPath(searchLowerPath(anchor.FilePath))
	seen := make([]map[string]int, searchTypeCardKindCount)
	add := func(kind searchTypeCardKind, tier int, relation *RelationRecord, target SymbolRecord) {
		if target.ID == "" || target.ID == anchor.ID {
			return
		}
		if seen[kind] == nil {
			seen[kind] = map[string]int{}
		}
		if existing, found := seen[kind][target.ID]; found {
			// The same target usually arrives twice (PARAM_TYPE and USES_TYPE for one
			// parameter). Keep the strongest claim rather than two identical entries.
			if candidate := &kinds[kind][existing]; tier > candidate.tier {
				candidate.tier = tier
			}
			return
		}
		if len(kinds[kind]) >= searchTypeCardCandidateLimit {
			return
		}
		seen[kind][target.ID] = len(kinds[kind])
		kinds[kind] = append(kinds[kind], searchTypeCardCandidate{
			kind: kind, tier: tier, strength: relation.Confidence, symbol: target,
		})
	}
	for index := range relations {
		relation := &relations[index]
		if relation.FromID != anchor.ID || relation.Confidence < searchTypeCardMinConfidence {
			continue
		}
		target, known := symbolsByID[relation.ToID]
		if !known || target.ID == "" {
			continue
		}
		// A production body does not depend on a test helper. When the graph says it does the
		// edge is a name-only misresolution, and the declaration it points at is one the agent's
		// patch can never have to compile against. (Measured: a Ruby plugin's card spent its only
		// slot on `TailInputTest.create_driver`.) An anchor that IS in a test tree keeps them.
		if !anchorInTest && searchTestArtifactPath(searchLowerPath(target.FilePath)) {
			continue
		}
		switch {
		case searchTypeCardFieldRelation(relation.Type):
			add(searchTypeCardField, searchTypeCardFieldTier(relation.Type), relation, target)
		case searchTypeCardCallRelation(relation.Type):
			add(searchTypeCardCall, 1, relation, target)
		default:
			if tier := searchTypeCardTypeTier(relation.Type); tier > 0 {
				add(searchTypeCardType, tier, relation, target)
			}
		}
	}
	resolveSearchTypeCardTargets(kinds, anchor, symbolsByID, cache)
	return kinds
}

// searchTypeCardFieldTier ranks a field relation. A field the body WRITES is part of what the
// body does; a field it reads is context.
//
// ACCESSES deliberately ranks with READS_FIELD, not above it: providers emit it ALONGSIDE the
// read for the same access, so scoring it higher counts one access as two pieces of evidence and
// promotes whichever field the provider happened to double-report.
func searchTypeCardFieldTier(relation string) int {
	if relation == "WRITES_FIELD" {
		return 2
	}
	return 1
}

// resolveSearchTypeCardTargets replaces each candidate with the BEST declaration of its own
// qualified name and renders it, dropping candidates that have no usable declaration.
//
// It does not simply trust the edge's target. Two things go wrong there, both measured:
// name-only resolution across a repo that declares one type in two packages picks a package at
// random (`memSeries` resolved to `tsdb/agent/series.go` for a body in `tsdb/`), and a provider
// can record a STATEMENT as a declaration (`struct client *target = lookupClientByID(id);`
// indexed as a `client` struct in C). A card entry that points at the wrong declaration is
// precisely the failure this feature exists to prevent — the agent would edit against it — so
// the pick is redone here, over every same-named declaration the graph holds.
func resolveSearchTypeCardTargets(
	kinds [][]searchTypeCardCandidate,
	anchor SymbolRecord,
	symbolsByID map[string]SymbolRecord,
	cache *searchRelatedFileCache,
) {
	wanted := map[string]bool{}
	for _, candidates := range kinds {
		for _, candidate := range candidates {
			if candidate.symbol.QualifiedName != "" {
				wanted[candidate.symbol.QualifiedName] = true
			}
		}
	}
	byQualifiedName := map[string][]SymbolRecord{}
	if len(wanted) > 0 {
		for _, symbol := range symbolsByID {
			if wanted[symbol.QualifiedName] {
				byQualifiedName[symbol.QualifiedName] = append(
					byQualifiedName[symbol.QualifiedName], symbol,
				)
			}
		}
	}
	anchorDir := searchPathDir(anchor.FilePath)
	for kind := range kinds {
		kept := kinds[kind][:0]
		for _, candidate := range kinds[kind] {
			if same := byQualifiedName[candidate.symbol.QualifiedName]; len(same) > 1 {
				candidate.symbol = searchTypeCardPickDeclaration(same, anchor.FilePath, anchorDir)
			}
			entry, ok := searchTypeCardDeclaration(candidate.symbol, cache)
			if !ok || !searchTypeCardEntryDeclares(entry, candidate.symbol) {
				continue
			}
			candidate.entry = entry
			kept = append(kept, candidate)
		}
		kinds[kind] = kept
	}
}

func searchPathDir(filePath string) string {
	slashed := filepath.ToSlash(filePath)
	index := strings.LastIndexByte(slashed, '/')
	if index < 0 {
		return ""
	}
	return slashed[:index]
}

// searchTypeCardDeclaration renders one symbol's declaration.
//
// Verbatim first: a single-line declaration read straight out of the file is exactly what the
// agent has to match, comments and all. A declaration that spans lines falls back to the
// provider's flattened signature, because the verbatim first line of a struct (`type Foo
// struct {`) carries none of the fields the card exists to show.
func searchTypeCardDeclaration(symbol SymbolRecord, cache *searchRelatedFileCache) (TypeCardEntry, bool) {
	entry := TypeCardEntry{
		Name:     searchTypeCardName(symbol),
		FilePath: symbol.FilePath,
		Line:     symbol.StartLine,
	}
	if symbol.StartLine > 0 && symbol.EndLine == symbol.StartLine {
		if lines, ok := cache.get(symbol.FilePath); ok && len(lines) >= symbol.StartLine {
			verbatim := strings.TrimSpace(lines[symbol.StartLine-1])
			// A one-line "container" whose text ASSIGNS is a statement the provider mistook for
			// a declaration (`struct client *target = lookupClientByID(id);`). Printing it as
			// the declaration of that type is the wrong-declaration failure, so the candidate is
			// dropped rather than downgraded: there is nothing true left to say about it.
			if verbatim != "" && searchTypeCardContainerKind(symbol.Kind) && searchLineAssigns(verbatim) {
				return TypeCardEntry{}, false
			}
			if verbatim != "" {
				entry.Decl = truncateSearchTypeCardDecl(verbatim)
				return entry, true
			}
		}
	}
	// A container declared over several lines is rendered from its own lines with the
	// comment-only ones dropped. The provider's flattened signature keeps the prose, and prose
	// is what the byte cap then spends itself on: one measured struct spent all 160 bytes on two
	// sentences of doc comment and showed not one field. The fields are the answer.
	if searchTypeCardContainerKind(symbol.Kind) && symbol.EndLine > symbol.StartLine {
		if lines, ok := cache.get(symbol.FilePath); ok {
			if decl := searchTypeCardBlockDecl(lines, symbol); decl != "" {
				entry.Decl = truncateSearchTypeCardDecl(decl)
				return entry, true
			}
		}
	}
	if signature := strings.TrimSpace(symbol.Signature); signature != "" {
		entry.Decl = truncateSearchTypeCardDecl(collapseSearchTypeCardSpace(signature))
		return entry, true
	}
	if symbol.StartLine > 0 {
		if lines, ok := cache.get(symbol.FilePath); ok && len(lines) >= symbol.StartLine {
			if verbatim := strings.TrimSpace(lines[symbol.StartLine-1]); verbatim != "" {
				entry.Decl = truncateSearchTypeCardDecl(verbatim)
				return entry, true
			}
		}
	}
	return TypeCardEntry{}, false
}

// searchTypeCardEntryDeclares is the card's own correctness check: the text it is about to print
// as the declaration of X has to NAME X.
//
// It catches every way a rendering can come out wrong at once — a provider that recorded `struct`
// as the signature of a `robj` type, a signature clipped before its own name, a block assembled
// from the wrong lines — without needing to know which of them happened. An entry that fails it
// is dropped: a declaration the agent cannot tie to the name it asked about is not a weaker
// entry, it is a misleading one.
func searchTypeCardEntryDeclares(entry TypeCardEntry, symbol SymbolRecord) bool {
	name := symbol.Name
	if name == "" {
		name = entry.Name
	}
	if name == "" || entry.Decl == "" {
		return false
	}
	// The name has to appear in DECLARING position — before the brace that opens the member
	// list, when there is one. Requiring it anywhere is not enough: `typedef struct { robj
	// *subject; … }` is an anonymous struct the provider recorded under the name `robj`, and it
	// mentions `robj` in its first FIELD. Shown as "the declaration of robj" that is a lie about
	// the type the agent is about to write against, and the whole card exists to prevent exactly
	// that kind of lie.
	head := entry.Decl
	if brace := strings.IndexByte(head, '{'); brace >= 0 {
		head = head[:brace]
	}
	if !containsIdentifierUsage(head, name) {
		return false
	}
	// For a container, the name must also be outside any PARAMETER LIST. A C provider records
	// `struct connection` out of `void rdbPipeWriteHandlerConnRemoved(struct connection *conn)`
	// and files it as a declaration of `connection`; the name is there, `struct` is even in front
	// of it, and the whole thing is a function. No container declaration in any language this tool
	// parses opens a parenthesis before its own name.
	if searchTypeCardContainerKind(symbol.Kind) {
		if paren := strings.IndexByte(head, '('); paren >= 0 {
			return containsIdentifierUsage(head[:paren], name)
		}
	}
	return true
}

// searchTypeCardName is the name the card prints. The qualified name is preferred for a member:
// `prev` alone does not say which type has it, and that ambiguity is the failure.
func searchTypeCardName(symbol SymbolRecord) string {
	if symbol.QualifiedName != "" {
		return symbol.QualifiedName
	}
	return symbol.Name
}

// searchTypeCardBlockDecl renders a multi-line declaration from the file's own lines, keeping
// every line that declares something and dropping the ones that only comment on it. Lines stay
// verbatim (trimmed); nothing is reworded, so what the card shows is what the file says.
func searchTypeCardBlockDecl(lines []string, symbol SymbolRecord) string {
	start, end := clampRegion(symbol.StartLine, symbol.EndLine, len(lines))
	if start == 0 {
		return ""
	}
	kept := make([]string, 0, end-start+1)
	for offset := start; offset <= end; offset++ {
		text := strings.TrimSpace(lines[offset-1])
		if text == "" || searchCommentOnlyLine(text) {
			continue
		}
		kept = append(kept, text)
		// One cap on top of the byte cap: a 400-field struct would otherwise be assembled in
		// full and then thrown away, and the first lines are the ones that survive truncation
		// anyway.
		if len(kept) >= searchTypeCardBlockLines {
			break
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return strings.Join(kept, " ")
}

// searchCommentOnlyLine reports whether a line's whole content is a comment, in the markers
// mainstream languages share. A trailing comment on a declaration is left alone: it is often the
// only statement of what a field means.
func searchCommentOnlyLine(text string) bool {
	for _, marker := range []string{"//", "/*", "*", "#", "--", ";;", "\"\"\""} {
		if strings.HasPrefix(text, marker) {
			return true
		}
	}
	return false
}

// searchLineAssigns reports whether a line contains an assignment, as opposed to a comparison
// (`==`, `!=`, `<=`, `>=`), an arrow (`=>`) or a default value inside a declaration list.
func searchLineAssigns(line string) bool {
	for index := 0; index < len(line); index++ {
		if line[index] != '=' {
			continue
		}
		if index+1 < len(line) && (line[index+1] == '=' || line[index+1] == '>') {
			index++
			continue
		}
		if index > 0 {
			switch line[index-1] {
			case '=', '!', '<', '>', '+', '-', '*', '/', '%', '&', '|', '^', ':':
				continue
			}
		}
		return true
	}
	return false
}

func collapseSearchTypeCardSpace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// truncateSearchTypeCardDecl clips a declaration to the per-entry cap on a rune boundary and
// marks the clip, so a reader can never mistake a clipped struct for a complete one.
func truncateSearchTypeCardDecl(value string) string {
	if len(value) <= searchTypeCardDeclBytes {
		return value
	}
	end := searchTypeCardDeclBytes
	for end > 0 && !utf8RuneStart(value[end]) {
		end--
	}
	return strings.TrimRight(value[:end], " \t") + " …"
}

// searchTypeCardBindings collects the names the anchor's body BINDS and then uses on another
// line of that same body.
//
// Such a name has no declaration anywhere else — it is not in the graph, by construction — so
// no relation can surface it, and the card is the only place it can appear. What it buys is the
// coupling: an edit that deletes the lines using the name has to delete the binding too, and
// the measured failure is exactly that (Go's "declared and not used", whole package red).
//
// It is only computed when the payload returns the anchor's COMPLETE body: with a partial
// snippet "used nowhere else" would be a guess, and a wrong claim here is worse than silence.
func searchTypeCardBindings(
	results []SearchResult,
	anchor SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	cache *searchRelatedFileCache,
) []searchTypeCardCandidate {
	if !searchTypeCardBodyComplete(results, anchor) {
		return nil
	}
	lines, ok := cache.get(anchor.FilePath)
	if !ok {
		return nil
	}
	start, end := clampRegion(anchor.StartLine, anchor.EndLine, len(lines))
	if start == 0 || end <= start {
		return nil
	}
	declared := map[string]bool{}
	for _, symbol := range symbolsByFile[anchor.FilePath] {
		if symbol.Name != "" {
			declared[symbol.Name] = true
		}
	}
	var candidates []searchTypeCardCandidate
	for offset := start; offset <= end; offset++ {
		name, ok := searchBoundIdentifier(lines[offset-1])
		if !ok || declared[name] {
			continue
		}
		uses := searchTypeCardBindingUseLines(lines, start, end, offset, name)
		if len(uses) == 0 {
			continue
		}
		candidates = append(candidates, searchTypeCardCandidate{
			kind: searchTypeCardBinding,
			// FEWER uses ranks HIGHER. A binding consumed on exactly one line is the one an edit
			// that deletes that line orphans, and an orphaned binding is a compile error in Go
			// and Rust — which is the measured failure. A binding used in five places survives
			// any single deletion and needs no warning.
			tier:     searchTypeCardBindingUses + 1 - len(uses),
			strength: 1,
			symbol:   SymbolRecord{ID: anchor.FilePath + ":binding:" + name, FilePath: anchor.FilePath},
			entry: TypeCardEntry{
				Name:     name,
				FilePath: anchor.FilePath,
				Line:     offset,
				Decl:     truncateSearchTypeCardDecl(strings.TrimSpace(lines[offset-1])),
				UseLines: uses,
			},
		})
		if len(candidates) >= searchTypeCardCandidateLimit {
			break
		}
	}
	return candidates
}

// searchTypeCardBodyComplete reports whether the payload returns the anchor's whole body, which
// is the precondition for claiming anything about where a local is used.
func searchTypeCardBodyComplete(results []SearchResult, anchor SymbolRecord) bool {
	for _, result := range results {
		if result.FilePath != anchor.FilePath || !hasSearchSignal(result, searchCompleteSymbolSignal) {
			continue
		}
		if result.SnippetStartLine <= anchor.StartLine && result.SnippetEndLine >= anchor.EndLine {
			return true
		}
	}
	return false
}

// searchBoundIdentifier reads the name a line DECLARES, or false when the line declares nothing.
//
// It is deliberately the narrow, unambiguous shape: one name at the start of a statement, either
// introduced by `:=` or preceded by a declaring keyword. `a, b := f()` is skipped (the name is
// not alone) and `if x := f(); …` is skipped (the line does not start with the name).
//
// A bare `x = …` is NOT a declaration and is deliberately excluded, even though it looks like one
// in Python and Ruby. It is measured to misfire on re-assignment of a variable declared earlier
// (`value = listTypePop(o,where);` in C, where `robj *value` is above it), and the failure this
// entry exists to warn about — an orphaned local — is a COMPILE error only in the languages that
// declare with `:=`, `var`, `let` or `const`. Missing a binding costs nothing; claiming that a
// line declares a name it only assigns is a false statement in the payload.
func searchBoundIdentifier(line string) (string, bool) {
	rest := strings.TrimLeft(line, " \t")
	declared := false
	for _, keyword := range []string{"var ", "let ", "const ", "final ", "my ", "local "} {
		if trimmed, found := strings.CutPrefix(rest, keyword); found {
			rest = strings.TrimLeft(trimmed, " \t")
			declared = true
			break
		}
	}
	end := 0
	for end < len(rest) && searchIdentifierByte(rest[end]) {
		end++
	}
	if end == 0 || rest[0] >= '0' && rest[0] <= '9' {
		return "", false
	}
	name := rest[:end]
	if len(name) < 3 {
		return "", false
	}
	operator := strings.TrimLeft(rest[end:], " \t")
	if strings.HasPrefix(operator, ":=") {
		return name, true
	}
	if declared && strings.HasPrefix(operator, "=") &&
		!(len(operator) > 1 && (operator[1] == '=' || operator[1] == '>')) {
		return name, true
	}
	return "", false
}

// searchTypeCardBindingUseLines lists the other lines of the body that reference a bound name,
// capped, so the entry says "and it is used here" without listing a loop's every iteration.
func searchTypeCardBindingUseLines(lines []string, start, end, declared int, name string) []int {
	var uses []int
	for offset := start; offset <= end; offset++ {
		if offset == declared {
			continue
		}
		if containsIdentifierUsage(lines[offset-1], name) {
			uses = append(uses, offset)
			if len(uses) >= searchTypeCardBindingUses {
				break
			}
		}
	}
	return uses
}

// searchTypeCardOrder spends the card's slots by rotating through the kinds, so a body with
// twenty callees cannot fill the card with signatures and hide the one field it dereferences.
// Inside a kind, the strongest claim on the contract goes first.
//
// A candidate whose declaration NAMES a type the card can also show ranks above one that does
// not: `headChunks *memChunk` plus the `memChunk` declaration answers "what does this receiver
// hold"; `dir *os.File` answers nothing, and the alphabet is not a reason to prefer it.
func searchTypeCardOrder(
	kinds [][]searchTypeCardCandidate,
	closures map[string]SymbolRecord,
) []searchTypeCardCandidate {
	for kind := range kinds {
		candidates := kinds[kind]
		for index := range candidates {
			if _, has := closures[candidates[index].symbol.ID]; has {
				candidates[index].tier += searchTypeCardClosureTierBonus
			}
		}
		sort.SliceStable(candidates, func(left, right int) bool {
			return searchTypeCardCandidateLess(candidates[left], candidates[right])
		})
	}
	var ordered []searchTypeCardCandidate
	for progress := true; progress; {
		progress = false
		for kind := range kinds {
			if len(kinds[kind]) == 0 {
				continue
			}
			ordered = append(ordered, kinds[kind][0])
			kinds[kind] = kinds[kind][1:]
			progress = true
		}
	}
	return ordered
}

func searchTypeCardCandidateLess(left, right searchTypeCardCandidate) bool {
	if left.tier != right.tier {
		return left.tier > right.tier
	}
	if left.strength != right.strength {
		return left.strength > right.strength
	}
	if left.entry.Name != right.entry.Name {
		return left.entry.Name < right.entry.Name
	}
	return left.symbol.ID < right.symbol.ID
}

// searchTypeCardClosureIndex resolves, for each ordered candidate, the indexed type its own
// declaration names — the closure step. `headChunks *memChunk` is only half an answer; the
// other half is what a `memChunk` holds.
//
// One scan of the symbol table serves every candidate, and only container declarations are
// admitted, so the card cannot follow `int64` or a callee's name into noise. When a name has
// several declarations the least ambiguous wins, preferring the anchor's own directory: the
// closure must not introduce the very wrong-declaration error the card exists to prevent.
func searchTypeCardClosureIndex(
	kinds [][]searchTypeCardCandidate,
	anchor SymbolRecord,
	symbolsByID map[string]SymbolRecord,
) map[string]SymbolRecord {
	var ordered []searchTypeCardCandidate
	for _, candidates := range kinds {
		ordered = append(ordered, candidates...)
	}
	wanted := map[string]bool{}
	for _, candidate := range ordered {
		if candidate.kind == searchTypeCardBinding {
			continue
		}
		for _, token := range searchDeclarationTokens(candidate.entry.Decl, candidate.symbol.Name) {
			wanted[token] = true
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	byName := map[string][]SymbolRecord{}
	for _, symbol := range symbolsByID {
		if symbol.Name == "" || !searchTypeCardContainerKind(symbol.Kind) {
			continue
		}
		if !wanted[symbol.Name] {
			continue
		}
		byName[symbol.Name] = append(byName[symbol.Name], symbol)
	}
	if len(byName) == 0 {
		return nil
	}
	anchorDir := searchPathDir(anchor.FilePath)
	closures := map[string]SymbolRecord{}
	for _, candidate := range ordered {
		if candidate.kind == searchTypeCardBinding {
			continue
		}
		best, found, ambiguity := SymbolRecord{}, false, 0
		for _, token := range searchDeclarationTokens(candidate.entry.Decl, candidate.symbol.Name) {
			symbols := byName[token]
			if len(symbols) == 0 {
				continue
			}
			pick := searchTypeCardPickDeclaration(symbols, anchor.FilePath, anchorDir)
			if pick.ID == candidate.symbol.ID || pick.ID == anchor.ID {
				continue
			}
			if !found || len(symbols) < ambiguity {
				best, found, ambiguity = pick, true, len(symbols)
			}
		}
		if found {
			closures[candidate.symbol.ID] = best
		}
	}
	return closures
}

// searchTypeCardPickDeclaration chooses one declaration among same-named candidates.
//
// The SPAN decides first, and only then locality. A container declared over several lines is the
// real declaration: a one-line record of the same name is a forward declaration, an `extern`, or
// a statement the provider mistook for one — none of which carry the members the card is for,
// and the last of which is actively wrong. Locality breaks the remaining ties (the anchor's own
// file, then its directory), then the path, so the pick is deterministic even though the caller
// iterated a map to get here.
func searchTypeCardPickDeclaration(symbols []SymbolRecord, anchorPath, anchorDir string) SymbolRecord {
	ordered := append([]SymbolRecord(nil), symbols...)
	sort.SliceStable(ordered, func(left, right int) bool {
		// A signature that names the symbol before its member list comes first: it is the one
		// candidate already known to be a real declaration of this name rather than an anonymous
		// block the provider filed under it. Checked on the signature because it needs no IO.
		leftNamed := searchTypeCardSignatureDeclares(ordered[left])
		rightNamed := searchTypeCardSignatureDeclares(ordered[right])
		if leftNamed != rightNamed {
			return leftNamed
		}
		leftSpan := searchTypeCardDeclarationSpan(ordered[left])
		rightSpan := searchTypeCardDeclarationSpan(ordered[right])
		// Having a MEMBER LIST at all comes before locality: a one-line record of a container's
		// name is a forward declaration, an `extern`, or a statement the provider mistook for
		// one, and none of them carry what the card is for — not even when it is the copy sitting
		// in the anchor's own file.
		if (leftSpan > 1) != (rightSpan > 1) {
			return leftSpan > 1
		}
		// Locality next: one name declared in two packages is the measured wrong-declaration case
		// (`memSeries` in `tsdb/` and in `tsdb/agent/`), and the body's own tree is the right one.
		leftRank := searchTypeCardLocalityRank(ordered[left], anchorPath, anchorDir)
		rightRank := searchTypeCardLocalityRank(ordered[right], anchorPath, anchorDir)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if leftSpan != rightSpan {
			return leftSpan > rightSpan
		}
		if ordered[left].FilePath != ordered[right].FilePath {
			return ordered[left].FilePath < ordered[right].FilePath
		}
		return ordered[left].ID < ordered[right].ID
	})
	return ordered[0]
}

// searchTypeCardDeclarationSpan is how many lines a container declaration covers, and 1 for
// everything else: span is evidence about a TYPE (a real struct spans its fields), while for a
// function or a field a long body says nothing about the quality of the declaration line.
// searchTypeCardSignatureDeclares reports whether a symbol's own signature names it in declaring
// position, i.e. before the brace that opens its member list.
func searchTypeCardSignatureDeclares(symbol SymbolRecord) bool {
	return searchTypeCardEntryDeclares(
		TypeCardEntry{Decl: collapseSearchTypeCardSpace(symbol.Signature)}, symbol,
	)
}

func searchTypeCardDeclarationSpan(symbol SymbolRecord) int {
	if !searchTypeCardContainerKind(symbol.Kind) || symbol.EndLine < symbol.StartLine {
		return 1
	}
	return symbol.EndLine - symbol.StartLine + 1
}

func searchTypeCardLocalityRank(symbol SymbolRecord, anchorPath, anchorDir string) int {
	switch {
	case symbol.FilePath == anchorPath:
		return 0
	case anchorDir != "" && searchPathDir(symbol.FilePath) == anchorDir:
		return 1
	default:
		return 2
	}
}

// searchDeclarationTokens lists the identifier-shaped tokens of a declaration, minus the
// declared name itself, in the order they appear. For `headChunks *memChunk` that is
// [`memChunk`]; for `func f(a Foo) Bar` it is [`func`, `a`, `Foo`, `Bar`], and only the ones
// that resolve to a container declaration survive the lookup.
func searchDeclarationTokens(decl, own string) []string {
	var tokens []string
	seen := map[string]bool{own: true}
	for offset := 0; offset < len(decl); {
		if !searchIdentifierByte(decl[offset]) || decl[offset] >= '0' && decl[offset] <= '9' {
			offset++
			continue
		}
		end := offset
		for end < len(decl) && searchIdentifierByte(decl[end]) {
			end++
		}
		token := decl[offset:end]
		offset = end
		if len(token) < 3 || seen[token] {
			continue
		}
		seen[token] = true
		tokens = append(tokens, token)
	}
	return tokens
}

// fitSearchTypeCard spends the byte allowance on the ordered candidates, admitting each
// candidate's closure entry immediately after it — the pair is what answers the question, and
// splitting it across a budget boundary would leave the half that does not.
//
// An entry whose declaration the payload ALREADY prints is skipped: the card's job is to bring
// in what is not in view, and re-printing a line the agent can already see spends a slot on
// nothing.
func fitSearchTypeCard(
	ordered []searchTypeCardCandidate,
	closures map[string]SymbolRecord,
	results []SearchResult,
	cache *searchRelatedFileCache,
	byteCap int,
) []TypeCardEntry {
	var card []TypeCardEntry
	admitted := map[string]bool{}
	for _, candidate := range ordered {
		if len(card) >= searchTypeCardEntryLimit {
			break
		}
		if admitted[candidate.symbol.ID] || searchTypeCardAlreadyInView(results, candidate.entry) {
			continue
		}
		next := append(append([]TypeCardEntry(nil), card...), candidate.entry)
		if serializedSearchResultBytes(next) > byteCap {
			continue
		}
		card = next
		admitted[candidate.symbol.ID] = true
		closure, has := closures[candidate.symbol.ID]
		if !has || admitted[closure.ID] || len(card) >= searchTypeCardEntryLimit {
			continue
		}
		entry, ok := searchTypeCardClosureEntry(closure, candidate.entry, cache)
		if !ok || searchTypeCardAlreadyInView(results, entry) {
			continue
		}
		withClosure := append(append([]TypeCardEntry(nil), card...), entry)
		if serializedSearchResultBytes(withClosure) > byteCap {
			continue
		}
		card = withClosure
		admitted[closure.ID] = true
	}
	return card
}

// searchTypeCardClosureEntry renders a closure target through the same declaration renderer as
// every other entry, and rejects one that would merely repeat its parent's text.
func searchTypeCardClosureEntry(
	symbol SymbolRecord,
	parent TypeCardEntry,
	cache *searchRelatedFileCache,
) (TypeCardEntry, bool) {
	if symbol.FilePath == "" {
		return TypeCardEntry{}, false
	}
	entry, ok := searchTypeCardDeclaration(symbol, cache)
	if !ok || entry.Decl == parent.Decl || !searchTypeCardEntryDeclares(entry, symbol) {
		return TypeCardEntry{}, false
	}
	return entry, true
}

// searchTypeCardAlreadyInView reports whether a returned snippet already prints an entry's
// declaration line.
//
// A BINDING entry is exempt: its line is inside the anchor's own body by construction, so it is
// always "in view", and what the entry adds is not the line but the use-line coupling that the
// body alone does not state.
func searchTypeCardAlreadyInView(results []SearchResult, entry TypeCardEntry) bool {
	if entry.Line <= 0 || len(entry.UseLines) > 0 {
		return false
	}
	for _, result := range results {
		if result.FilePath != entry.FilePath {
			continue
		}
		if result.SnippetStartLine <= entry.Line && result.SnippetEndLine >= entry.Line {
			return true
		}
	}
	return false
}
