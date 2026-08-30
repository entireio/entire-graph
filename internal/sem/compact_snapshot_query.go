package sem

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// CompactSnapshotIndex is the materialized consumer for a compact snapshot.
type CompactSnapshotIndex struct {
	Snapshot              ProviderSnapshot
	Summary               SnapshotSummary
	CanonicalSemanticHash string
	// SchemaWarnings carries the ADR 0001 tolerant-reader warnings for this
	// artifact. An unreadable major is an error from LoadCompactSnapshot; a
	// readable major written under a NEWER minor loads, and says so here, because
	// the additive facts that minor introduced were not read.
	SchemaWarnings  []ProviderWarning
	symbolMatches   map[string][]int
	relationsByFrom map[string][]int
}

type CompactSnapshotQuery struct {
	Symbol   string
	FromID   string
	Relation string
}
type CompactSnapshotQueryResult struct {
	Symbols   []SymbolRecord
	Relations []RelationRecord
}

func LoadCompactSnapshot(in io.Reader) (*CompactSnapshotIndex, error) {
	index := &CompactSnapshotIndex{symbolMatches: map[string][]int{}, relationsByFrom: map[string][]int{}}
	hasher := NewSnapshotSemanticHasher()
	seenHeader, seenSummary := false, false
	warnings, err := DecodeCompactSnapshot(in, func(record any) error {
		if err := hasher.Add(record); err != nil {
			return err
		}
		switch typed := record.(type) {
		case SnapshotHeader:
			index.Snapshot.Header = typed
			seenHeader = true
		case FileRecord:
			index.Snapshot.Files = append(index.Snapshot.Files, typed)
		case ExternalRecord:
			index.Snapshot.Externals = append(index.Snapshot.Externals, typed)
		case SymbolRecord:
			index.Snapshot.Symbols = append(index.Snapshot.Symbols, typed)
		case RelationRecord:
			index.Snapshot.Relations = append(index.Snapshot.Relations, typed)
		case SnapshotSummary:
			index.Summary = typed
			seenSummary = true
		default:
			return fmt.Errorf("unsupported compact snapshot record %T", record)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// The decoder is the single place the tolerant-reader warnings are derived,
	// so this consumes them rather than recomputing the same condition from the
	// header a second time.
	index.SchemaWarnings = append(index.SchemaWarnings, warnings...)
	if !seenHeader || !seenSummary {
		return nil, errorsNewCompactLoad()
	}
	for i, symbol := range index.Snapshot.Symbols {
		for _, key := range []string{symbol.ID, symbol.Name, symbol.QualifiedName} {
			matches := index.symbolMatches[key]
			if len(matches) == 0 || matches[len(matches)-1] != i {
				index.symbolMatches[key] = append(matches, i)
			}
		}
	}
	for i, relation := range index.Snapshot.Relations {
		index.relationsByFrom[relation.FromID] = append(index.relationsByFrom[relation.FromID], i)
	}
	index.CanonicalSemanticHash = hasher.SumHex()
	return index, nil
}

func errorsNewCompactLoad() error {
	return fmt.Errorf("compact snapshot loader requires header and summary")
}

func (index *CompactSnapshotIndex) Query(query CompactSnapshotQuery) CompactSnapshotQueryResult {
	result := CompactSnapshotQueryResult{Symbols: []SymbolRecord{}, Relations: []RelationRecord{}}
	if query.Symbol != "" {
		for _, position := range index.symbolMatches[query.Symbol] {
			result.Symbols = append(result.Symbols, index.Snapshot.Symbols[position])
		}
	}
	for _, position := range index.relationsByFrom[query.FromID] {
		relation := index.Snapshot.Relations[position]
		if query.Relation != "" && relation.Type != strings.ToUpper(query.Relation) {
			continue
		}
		result.Relations = append(result.Relations, relation)
	}
	sort.Slice(result.Symbols, func(i, j int) bool { return result.Symbols[i].ID < result.Symbols[j].ID })
	sort.Slice(result.Relations, func(i, j int) bool {
		left, right := result.Relations[i], result.Relations[j]
		if left.Type != right.Type {
			return left.Type < right.Type
		}
		if left.FromID != right.FromID {
			return left.FromID < right.FromID
		}
		return left.ToID < right.ToID
	})
	return result
}
