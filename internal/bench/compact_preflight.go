package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/entireio/entire-graph/internal/sem"
)

// compactPreflight is the artifact validation result collected after the cold
// measurement. It is deliberately separate from RepoMetrics so the boolean
// projection proof remains an internal invariant rather than a reported claim.
type compactPreflight struct {
	Files                        int
	Externals                    int
	Symbols                      int
	Relations                    int
	NDJSONRawBytes               int
	CompactRawBytes              int
	CompactDictionaryBytes       int
	ProjectedFacts               int
	NDJSONBytesPerProjectedFact  float64
	CompactBytesPerProjectedFact float64
	CanonicalSemanticHash        string
	ProjectionMatchesNative      bool
}

type snapshotProjection struct {
	Records   []any
	Header    sem.SnapshotHeader
	Files     []sem.FileRecord
	Externals []sem.ExternalRecord
	Symbols   []sem.SymbolRecord
	Relations []sem.RelationRecord
	Summary   sem.SnapshotSummary
}

func (projection *snapshotProjection) add(record any) error {
	projection.Records = append(projection.Records, record)
	switch typed := record.(type) {
	case sem.SnapshotHeader:
		projection.Header = typed
	case sem.FileRecord:
		projection.Files = append(projection.Files, typed)
	case sem.ExternalRecord:
		projection.Externals = append(projection.Externals, typed)
	case sem.SymbolRecord:
		projection.Symbols = append(projection.Symbols, typed)
	case sem.RelationRecord:
		projection.Relations = append(projection.Relations, typed)
	case sem.SnapshotSummary:
		projection.Summary = typed
	default:
		return fmt.Errorf("unsupported snapshot record %T", record)
	}
	return nil
}

func (projection snapshotProjection) factCount() int {
	return len(projection.Files) + len(projection.Externals) + len(projection.Symbols) + len(projection.Relations)
}

func runCompactPreflight(ctx context.Context, dir, providerVersion string, profile sem.Profile) (compactPreflight, error) {
	var native bytes.Buffer
	var compact bytes.Buffer
	compactEncoder := sem.NewCompactSnapshotEncoder(&compact)
	nativeHasher := sem.NewSnapshotSemanticHasher()
	projection := snapshotProjection{}
	nativeEncoder := json.NewEncoder(&native)
	nativeEncoder.SetEscapeHTML(false)

	err := sem.StreamSnapshot(ctx, dir, providerVersion, sem.ProviderSnapshotOptions{NoNetwork: true, Profile: profile}, func(record any) error {
		if err := nativeEncoder.Encode(record); err != nil {
			return fmt.Errorf("write native preflight snapshot: %w", err)
		}
		if err := nativeHasher.Add(record); err != nil {
			return fmt.Errorf("hash native preflight snapshot: %w", err)
		}
		if err := projection.add(record); err != nil {
			return err
		}
		if err := compactEncoder.Encode(record); err != nil {
			return fmt.Errorf("write compact preflight snapshot: %w", err)
		}
		return nil
	})
	if err != nil {
		return compactPreflight{}, err
	}
	return verifyCompactPreflight(
		projection,
		compact.Bytes(),
		nativeHasher.SumHex(),
		native.Len(),
		int(compactEncoder.DictionaryBytes()),
	)
}

func verifyCompactPreflight(native snapshotProjection, compact []byte, nativeHash string, ndjsonBytes, dictionaryBytes int) (compactPreflight, error) {
	loaded, err := sem.LoadCompactSnapshot(bytes.NewReader(compact))
	if err != nil {
		return compactPreflight{}, fmt.Errorf("load compact preflight snapshot: %w", err)
	}
	if loaded.CanonicalSemanticHash != nativeHash {
		return compactPreflight{}, fmt.Errorf("compact canonical semantic hash %s does not match native %s", loaded.CanonicalSemanticHash, nativeHash)
	}
	var decoded []any
	if err := sem.DecodeCompactSnapshot(bytes.NewReader(compact), func(record any) error {
		decoded = append(decoded, record)
		return nil
	}); err != nil {
		return compactPreflight{}, fmt.Errorf("decode compact preflight snapshot: %w", err)
	}
	if !samePublicProjection(native.Records, decoded) {
		return compactPreflight{}, fmt.Errorf("compact decoded public projection does not match native snapshot")
	}
	if err := verifyCompactQueries(loaded, native); err != nil {
		return compactPreflight{}, err
	}
	facts := native.factCount()
	denominator := max(1, facts)
	return compactPreflight{
		Files:                        len(native.Files),
		Externals:                    len(native.Externals),
		Symbols:                      len(native.Symbols),
		Relations:                    len(native.Relations),
		NDJSONRawBytes:               ndjsonBytes,
		CompactRawBytes:              len(compact),
		CompactDictionaryBytes:       dictionaryBytes,
		ProjectedFacts:               facts,
		NDJSONBytesPerProjectedFact:  round2(float64(ndjsonBytes) / float64(denominator)),
		CompactBytesPerProjectedFact: round2(float64(len(compact)) / float64(denominator)),
		CanonicalSemanticHash:        nativeHash,
		ProjectionMatchesNative:      true,
	}, nil
}

func samePublicProjection(left, right []any) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		var leftJSON, rightJSON bytes.Buffer
		leftEncoder, rightEncoder := json.NewEncoder(&leftJSON), json.NewEncoder(&rightJSON)
		leftEncoder.SetEscapeHTML(false)
		rightEncoder.SetEscapeHTML(false)
		if leftEncoder.Encode(left[i]) != nil || rightEncoder.Encode(right[i]) != nil || !bytes.Equal(leftJSON.Bytes(), rightJSON.Bytes()) {
			return false
		}
	}
	return true
}

func verifyCompactQueries(index *sem.CompactSnapshotIndex, native snapshotProjection) error {
	if len(native.Symbols) > 0 {
		symbols := append([]sem.SymbolRecord(nil), native.Symbols...)
		sort.Slice(symbols, func(i, j int) bool { return symbols[i].ID < symbols[j].ID })
		got := index.Query(sem.CompactSnapshotQuery{Symbol: symbols[0].ID}).Symbols
		if len(got) != 1 || !samePublicProjection([]any{got[0]}, []any{symbols[0]}) {
			return fmt.Errorf("compact symbol query does not exactly match native symbol %q", symbols[0].ID)
		}
	}
	if len(native.Relations) > 0 {
		relations := append([]sem.RelationRecord(nil), native.Relations...)
		sort.Slice(relations, func(i, j int) bool {
			if relations[i].FromID != relations[j].FromID {
				return relations[i].FromID < relations[j].FromID
			}
			if relations[i].Type != relations[j].Type {
				return relations[i].Type < relations[j].Type
			}
			return relations[i].ToID < relations[j].ToID
		})
		want := relations[0]
		got := index.Query(sem.CompactSnapshotQuery{FromID: want.FromID, Relation: want.Type}).Relations
		matched := false
		for _, relation := range got {
			if samePublicProjection([]any{relation}, []any{want}) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("compact relation query does not exactly match native relation %q/%q/%q", want.FromID, want.Type, want.ToID)
		}
	}
	return nil
}
