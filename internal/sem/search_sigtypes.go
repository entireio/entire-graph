package sem

import (
	"regexp"
	"sort"
	"strings"
)

// Types in the anchor's own signature
// ===================================
//
// A located symbol is not usable on its own. `Edit::deletion(start: TextSize,
// end: TextSize) -> Self` tells an agent nothing about what a `TextSize` is or
// what else an `Edit` can be built from, so the next turn is spent reading a
// file to find out — and the measured cost of a turn is ~40x the whole search
// payload.
//
// This block closes exactly that gap and nothing wider: the types named in the
// ANCHOR SYMBOL'S OWN SIGNATURE, resolved to their declaration, rendered as
// fields plus the SIGNATURES of their associated functions. No bodies, no
// transitive expansion — in a language with deep generic types the transitive
// closure is hundreds of kilobytes and would be replayed on every later turn.
//
// It is funded, not added: the block is only emitted if redundant tail locators
// can be displaced to pay for it, so a payload carrying it is never larger than
// the same payload without it. That is the same rule the related-sites block
// follows, and it is why turning this on cannot cost byte budget.

const (
	searchSignatureTypeLimit        = 3
	searchSignatureTypeMemberLimit  = 15
	searchSignatureTypeFieldLimit   = 6
	searchSignatureTypeSigRunes     = 44
	searchSignatureTypeMaxNameRunes = 40
)

// SearchSignatureType is one type named in the anchor's signature, with the
// surface a caller needs to use it.
type SearchSignatureType struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind,omitempty"`
	FilePath     string   `json:"file_path"`
	StartLine    int      `json:"start_line"`
	Fields       []string `json:"fields,omitempty"`
	Methods      []string `json:"methods,omitempty"`
	MethodsTotal int      `json:"methods_total,omitempty"`
	FieldsTotal  int      `json:"fields_total,omitempty"`
}

var searchSignatureIdentifierRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// searchSignatureTypeKeywords are the words that appear in a declaration header
// in every language but never name a type the caller has to look up.
var searchSignatureTypeKeywords = map[string]bool{
	"abstract": true, "async": true, "await": true, "class": true, "const": true,
	"constructor": true, "def": true, "default": true, "dyn": true, "else": true,
	"enum": true, "export": true, "extends": true, "extern": true, "final": true,
	"fn": true, "for": true, "func": true, "function": true, "fun": true,
	"get": true, "impl": true, "implements": true, "import": true, "in": true,
	"inline": true, "interface": true, "internal": true, "let": true, "mut": true,
	"new": true, "open": true, "operator": true, "out": true, "override": true,
	"package": true, "partial": true, "private": true, "protected": true,
	"public": true, "pub": true, "readonly": true, "record": true, "ref": true,
	"return": true, "sealed": true, "set": true, "static": true, "struct": true,
	"suspend": true, "synchronized": true, "throws": true, "trait": true,
	"transient": true, "type": true, "typealias": true, "unsafe": true,
	"val": true, "var": true, "virtual": true, "void": true, "volatile": true,
	"where": true, "while": true, "with": true, "yield": true,
}

// searchSignatureTypeNames reads the candidate type names out of a declaration
// signature. It deliberately over-collects and lets RESOLUTION do the filtering:
// a name only survives if the graph actually holds a type declaration for it, so
// a heuristic that mistakes a parameter name for a type costs nothing.
//
// `Self`/`self`/`this` resolve to the declaring type, which is the single most
// useful entry for a method — it is the answer to "what else can I build one of
// these from", which is what a caller who found one constructor still needs.
func searchSignatureTypeNames(signature, ownName, ownerName string) []string {
	var names []string
	seen := map[string]bool{}
	add := func(name string) {
		if name == "" || seen[name] || name == ownName {
			return
		}
		seen[name] = true
		names = append(names, name)
	}
	// The declaring type leads. When only one entry can be afforded, the type the
	// located member BELONGS to is the one worth spending it on: it carries the
	// other constructors and accessors, which is the question a caller who found
	// one factory still has ("what else can build one of these?").
	add(ownerName)
	// Only the part of the signature after the declared name can hold parameter
	// and result types; a leading `pub const fn` cannot.
	scan := signature
	if ownName != "" {
		if index := strings.Index(signature, ownName); index >= 0 {
			scan = signature[index+len(ownName):]
		}
	}
	for _, token := range searchSignatureIdentifierRe.FindAllString(scan, -1) {
		lower := strings.ToLower(token)
		if searchSignatureTypeKeywords[lower] {
			continue
		}
		if lower == "self" || lower == "this" {
			add(ownerName)
			continue
		}
		if token[0] >= 'a' && token[0] <= 'z' {
			continue
		}
		add(token)
	}
	return names
}

// searchSignatureTypeSurface builds the block for one anchor. Types are resolved
// same-file first and then by unique short name across the workspace; a name with
// several candidate declarations is DROPPED rather than guessed, because an entry
// pointing at the wrong declaration is worse than no entry at all.
func searchSignatureTypeSurface(
	anchor SymbolRecord,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	relations []RelationRecord,
	limit int,
) []SearchSignatureType {
	if limit <= 0 || anchor.Signature == "" {
		return nil
	}
	ownerName := ""
	if owner, ok := symbolsByID[anchor.ContainerID]; ok {
		ownerName = owner.Name
	}
	candidates := searchSignatureTypeNames(anchor.Signature, anchor.Name, ownerName)
	if len(candidates) == 0 {
		return nil
	}
	byShortName := map[string][]SymbolRecord{}
	for _, symbol := range symbolsByID {
		if typeLikeKind(symbol.Kind) {
			byShortName[symbol.Name] = append(byShortName[symbol.Name], symbol)
		}
	}
	resolved := make([]SymbolRecord, 0, limit)
	seen := map[string]bool{}
	for _, name := range candidates {
		if len(resolved) == limit {
			break
		}
		symbol, ok := searchSignatureTypeDeclaration(name, anchor.FilePath, symbolsByFile, byShortName)
		if !ok || seen[symbol.ID] || symbol.ID == anchor.ID {
			continue
		}
		seen[symbol.ID] = true
		resolved = append(resolved, symbol)
	}
	if len(resolved) == 0 {
		return nil
	}
	members := searchSignatureTypeMembers(resolved, symbolsByID, relations)
	out := make([]SearchSignatureType, 0, len(resolved))
	for _, symbol := range resolved {
		entry := SearchSignatureType{
			Name:      symbol.Name,
			Kind:      symbol.Kind,
			FilePath:  symbol.FilePath,
			StartLine: symbol.StartLine,
		}
		for _, member := range members[symbol.ID] {
			if member.Kind == "field" || member.Kind == "property" || member.Kind == "constant" {
				entry.FieldsTotal++
				if len(entry.Fields) < searchSignatureTypeFieldLimit {
					entry.Fields = append(entry.Fields, searchSignatureFieldEntry(member))
				}
				continue
			}
			if typeLikeKind(member.Kind) {
				continue
			}
			entry.MethodsTotal++
			if len(entry.Methods) < searchSignatureTypeMemberLimit {
				entry.Methods = append(entry.Methods, searchSignatureCallableEntry(member))
			}
		}
		if len(entry.Fields) == 0 && len(entry.Methods) == 0 {
			continue
		}
		out = append(out, entry)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func searchSignatureTypeDeclaration(
	name, anchorFile string,
	symbolsByFile map[string][]SymbolRecord,
	byShortName map[string][]SymbolRecord,
) (SymbolRecord, bool) {
	for _, symbol := range symbolsByFile[anchorFile] {
		if typeLikeKind(symbol.Kind) && symbol.Name == name {
			return symbol, true
		}
	}
	candidates := byShortName[name]
	if len(candidates) != 1 {
		return SymbolRecord{}, false
	}
	return candidates[0], true
}

// searchSignatureTypeMembers collects the declared surface of the resolved types
// in declaration order. Membership comes from the graph (container_id plus the
// CONTAINS edges that carry extension and partial-part membership), never from a
// name resembling the type's name.
func searchSignatureTypeMembers(
	types []SymbolRecord,
	symbolsByID map[string]SymbolRecord,
	relations []RelationRecord,
) map[string][]SymbolRecord {
	wanted := make(map[string]bool, len(types))
	for _, symbol := range types {
		wanted[symbol.ID] = true
	}
	members := map[string][]SymbolRecord{}
	seen := map[string]bool{}
	add := func(ownerID string, member SymbolRecord) {
		if !wanted[ownerID] || member.ID == ownerID {
			return
		}
		key := ownerID + "\x00" + member.ID
		if seen[key] {
			return
		}
		seen[key] = true
		members[ownerID] = append(members[ownerID], member)
	}
	for _, symbol := range symbolsByID {
		if symbol.ContainerID != "" {
			add(symbol.ContainerID, symbol)
		}
	}
	for _, relation := range relations {
		if relation.Type != "CONTAINS" || relation.RelationScope != "extension" {
			continue
		}
		if member, ok := symbolsByID[relation.ToID]; ok {
			add(relation.FromID, member)
		}
	}
	for id := range members {
		list := members[id]
		sort.SliceStable(list, func(left, right int) bool {
			if list[left].FilePath != list[right].FilePath {
				return list[left].FilePath < list[right].FilePath
			}
			return list[left].StartLine < list[right].StartLine
		})
		members[id] = list
	}
	return members
}

// searchSignatureFieldEntry renders `name: Type` from a field record, whose
// signature the provider stores as `<name> <type>`.
func searchSignatureFieldEntry(member SymbolRecord) string {
	signature := strings.Join(strings.Fields(member.Signature), " ")
	rest := strings.TrimSpace(strings.TrimPrefix(signature, member.Name))
	if rest == "" || rest == signature {
		return searchSignatureTruncate(member.Name, searchSignatureTypeMaxNameRunes)
	}
	rest = strings.TrimSpace(strings.TrimLeft(rest, ":="))
	return member.Name + ": " + searchSignatureTruncate(rest, searchSignatureTypeSigRunes)
}

// searchSignatureCallableEntry renders `name(params) -> result`, dropping the
// modifiers every member repeats. A parameter list too long to keep collapses to
// `(..)`: knowing the member EXISTS and what it is called is most of the value,
// and the declaration's file:line is on the entry above it.
func searchSignatureCallableEntry(member SymbolRecord) string {
	signature := strings.Join(strings.Fields(member.Signature), " ")
	tail, ok := CallableSignatureTail(member.Name, signature)
	if !ok {
		return member.Name + "(..)"
	}
	if len([]rune(tail)) > searchSignatureTypeSigRunes {
		if close := strings.LastIndex(tail, ")"); close >= 0 && close+1 < len(tail) {
			return member.Name + "(..)" + searchSignatureTruncate(tail[close+1:], 16)
		}
		return member.Name + "(..)"
	}
	return member.Name + tail
}

func searchSignatureTruncate(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

// fundSearchSignatureTypes seats the block only if the payload can pay for it out
// of redundant tail locators, so a payload with the block is never bigger than
// the same payload without it. When nothing can be displaced the block is
// dropped: the byte budget is a ceiling that this feature must not raise.
func fundSearchSignatureTypes(
	results []SearchResult,
	block []SearchSignatureType,
	headRanks int,
) ([]SearchResult, []SearchSignatureType) {
	if len(block) == 0 || len(results) == 0 {
		return results, nil
	}
	baseline := serializedSearchResultBytes(results)
	floor := minInt(len(results), headRanks)
	order := searchRelatedDisplacementOrder(results, floor)
	// Displacement is minimised FIRST and the block sized second: a ranked
	// locator is worth more than a longer member list, so the cheapest plan that
	// seats any block at all wins, and the block then grows into whatever the one
	// displaced locator paid for.
	for drop := 0; drop <= len(order); drop++ {
		kept := searchRelatedMergedPlan(results, nil, order[:drop])
		available := baseline - serializedSearchResultBytes(kept)
		if available <= 0 {
			continue
		}
		for _, cap := range []int{searchSignatureTypeMemberLimit, 8, 5, 3, 2} {
			for count := len(block); count >= 1; count-- {
				trimmed := shrinkSearchSignatureTypes(block[:count], cap)
				if serializedSearchResultBytes(trimmed) <= available {
					return kept, trimmed
				}
			}
		}
	}
	return results, nil
}

// shrinkSearchSignatureTypes caps how many members each entry lists. The TOTAL
// counts are preserved, so a shrunk entry still reports how much it is hiding.
func shrinkSearchSignatureTypes(block []SearchSignatureType, cap int) []SearchSignatureType {
	out := make([]SearchSignatureType, 0, len(block))
	for _, entry := range block {
		if len(entry.Methods) > cap {
			entry.Methods = entry.Methods[:cap]
		}
		if fieldCap := minInt(cap, searchSignatureTypeFieldLimit); len(entry.Fields) > fieldCap {
			entry.Fields = entry.Fields[:fieldCap]
		}
		out = append(out, entry)
	}
	return out
}
