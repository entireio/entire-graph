package gate

import "sort"

// Symbol and Relation are this package's own view of the graph records. They
// deliberately do not reuse sem.SymbolRecord and sem.RelationRecord: importing
// sem pulls in the tree-sitter bindings and with them CGO, and the point of
// this package is that its tests run without either. The collect layer projects
// the real records onto these.
type Symbol struct {
	ID   string
	Name string
	Path string
	Line int
	Kind string
}

type Relation struct {
	FromID string
	ToID   string
	Type   string
}

// dependencyRelations are the edge types where "A -> B" means changing B can
// break A, so reversing them answers "who depends on this".
//
// Two families are deliberately absent.
//
// CONTAINS and DEFINES are structural, not behavioural: a file containing a
// symbol is not a caller of it, and counting them would make every symbol in a
// file a dependent of every other.
//
// DATA_FLOWS encodes the direction data travels, which is not the direction
// dependency travels. The provider emits `resolveRepo -> runStats` with the
// reason "callee return value assigned to local and returned by caller" — the
// callee pointing at its caller. Reversed, that reads as "resolveRepo depends
// on runStats", the exact inverse of the truth, and it put unrelated callees
// into the dependent list of anything that returned their values.
var dependencyRelations = map[string]bool{
	"CALLS":        true,
	"ASYNC_CALLS":  true,
	"CONSTRUCTS":   true,
	"USES_TYPE":    true,
	"PARAM_TYPE":   true,
	"RETURNS_TYPE": true,
	"READS_FIELD":  true,
	"WRITES_FIELD": true,
	"EXTENDS":      true,
	"IMPLEMENTS":   true,
	"OVERRIDES":    true,
	"INHERITS":     true,
}

// Index answers "who depends on this symbol" by holding the dependency edges
// reversed. Building it once per run costs one pass; asking the question
// without it costs a full scan per changed entity.
type Index struct {
	symbols  map[string]Symbol
	incoming map[string][]string
	// byName resolves a bare symbol name to its definitions, because the
	// semantic diff reports entity names while the graph is keyed by
	// compound-v1 IDs.
	byName map[string][]string
}

func NewIndex(symbols []Symbol, relations []Relation) *Index {
	ix := &Index{
		symbols:  make(map[string]Symbol, len(symbols)),
		incoming: make(map[string][]string),
		byName:   make(map[string][]string),
	}
	for _, s := range symbols {
		ix.symbols[s.ID] = s
		ix.byName[s.Name] = append(ix.byName[s.Name], s.ID)
	}
	for _, r := range relations {
		if dependencyRelations[r.Type] {
			ix.incoming[r.ToID] = append(ix.incoming[r.ToID], r.FromID)
		}
	}
	// The snapshot builds relations in parallel, so the slice arrives in
	// whatever order the workers finished. Sorting the adjacency once here is
	// what makes every downstream walk reproducible: without it two runs of the
	// same commit emit different bytes, and a verdict nobody can reproduce is
	// not a gate.
	for id := range ix.incoming {
		sort.Strings(ix.incoming[id])
	}
	for name := range ix.byName {
		sort.Strings(ix.byName[name])
	}
	return ix
}

// Resolve maps an entity name from the semantic diff onto graph symbol IDs.
// A name defined in several files returns several IDs; path narrows it to the
// file the change was reported in, which is the common case and the only one
// where a dependent count is meaningful.
func (ix *Index) Resolve(name, path string) []string {
	candidates := ix.byName[name]
	if len(candidates) <= 1 || path == "" {
		return candidates
	}
	var inFile []string
	for _, id := range candidates {
		if ix.symbols[id].Path == path {
			inFile = append(inFile, id)
		}
	}
	if len(inFile) > 0 {
		return inFile
	}
	return candidates
}

// Dependents walks incoming dependency edges up to hops levels and returns
// every symbol found, in one total order (see sortSymbols) rather than by
// distance: the count is what the verdict uses, and a stable listing is what
// makes two runs comparable. The starting symbols are never in their own result.
//
// hops is capped by the caller (see risk.go). Without a cap, one utility
// function pulls in most of the repository and the report becomes noise.
func (ix *Index) Dependents(ids []string, hops int) []Symbol {
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}

	var found []Symbol
	frontier := ids
	for hop := 0; hop < hops && len(frontier) > 0; hop++ {
		var next []string
		for _, id := range frontier {
			for _, from := range ix.incoming[id] {
				if seen[from] {
					continue
				}
				seen[from] = true
				next = append(next, from)
				// An unresolved endpoint has an ID but no definition record —
				// an external or unparsed target. It still counts as a
				// dependent, so synthesise enough of a Symbol to name it.
				if s, ok := ix.symbols[from]; ok {
					found = append(found, s)
				} else {
					found = append(found, Symbol{ID: from, Name: from})
				}
			}
		}
		frontier = next
	}
	sortSymbols(found)
	return dedupeByLocation(found)
}

// dedupeByLocation collapses symbols that name the same place. A symbol can be
// reached through more than one compound-v1 ID — a name resolved in two files,
// or a re-declaration the parser records twice — and reporting it twice both
// inflates the dependent count and prints the same line under one finding.
func dedupeByLocation(symbols []Symbol) []Symbol {
	if len(symbols) < 2 {
		return symbols
	}
	out := symbols[:1]
	for _, s := range symbols[1:] {
		last := out[len(out)-1]
		if s.Name == last.Name && s.Path == last.Path && s.Line == last.Line {
			continue
		}
		out = append(out, s)
	}
	return out
}

// sortSymbols gives the result one total order — path, then name, then ID —
// so the same graph always yields the same list. ID is the final tiebreak
// because two symbols can share a name and a file (an overload, a generic
// instantiation) and only the compound-v1 ID separates them.
func sortSymbols(symbols []Symbol) {
	sort.Slice(symbols, func(i, j int) bool {
		a, b := symbols[i], symbols[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
}
