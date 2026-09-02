package sem

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	scippb "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func encodeSCIP(t *testing.T, records ...any) (*scippb.Index, SCIPOmissionNote) {
	t.Helper()
	var out bytes.Buffer
	encoder := NewSCIPSnapshotEncoder(&out, "1.0.0")
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatalf("Encode(%T): %v", record, err)
		}
	}
	index := &scippb.Index{}
	if err := proto.Unmarshal(out.Bytes(), index); err != nil {
		t.Fatal(err)
	}
	return index, encoder.OmissionNote()
}

func requireSCIPRelationAccounting(t *testing.T, note SCIPOmissionNote, missingSource, missingTarget, missingEvidence int, unsupported map[string]int) {
	t.Helper()
	if note.MissingSourceRelations != missingSource || note.MissingTargetRelations != missingTarget || note.MissingEvidenceRelations != missingEvidence {
		t.Fatalf("relation omissions source=%d target=%d evidence=%d, want %d/%d/%d: %#v",
			note.MissingSourceRelations, note.MissingTargetRelations, note.MissingEvidenceRelations,
			missingSource, missingTarget, missingEvidence, note)
	}
	if !reflect.DeepEqual(note.UnsupportedRelationCounts, unsupported) {
		t.Fatalf("unsupported relations = %#v, want %#v", note.UnsupportedRelationCounts, unsupported)
	}
}

// TestSCIPCountsUnrepresentedContainment covers memberships SCIP cannot carry.
//
// CONTAINS is normally redundant because symbol metadata expresses the
// membership through EnclosingSymbol. That holds only when the relation agrees
// with ContainerID. An extension member -- a method attached to a receiver
// declared elsewhere -- produces a CONTAINS whose parent is NOT the symbol's
// container, and that membership survives in no other field. Skipping it
// silently let the note report a completeness the protobuf did not have.
func TestSCIPCountsUnrepresentedContainment(t *testing.T) {
	base := []any{
		SnapshotHeader{RepoKey: "local/demo", Commit: "abc"},
		FileRecord{Path: "a.go", Language: "Go"},
		SymbolRecord{ID: "owner", Kind: "struct", Name: "Owner", FilePath: "a.go", StartLine: 1, EndLine: 2},
		SymbolRecord{ID: "elsewhere", Kind: "struct", Name: "Elsewhere", FilePath: "a.go", StartLine: 4, EndLine: 5},
		SymbolRecord{ID: "member", Kind: "method", Name: "Member", FilePath: "a.go", ContainerID: "owner", StartLine: 7, EndLine: 8},
	}

	for _, test := range []struct {
		name          string
		relation      RelationRecord
		missingSource int
		missingTarget int
		unsupported   map[string]int
	}{
		{
			name:        "matching parent is redundant",
			relation:    RelationRecord{Type: "CONTAINS", FromID: "owner", ToID: "member"},
			unsupported: map[string]int{},
		},
		{
			name:        "different existing parent is unsupported",
			relation:    RelationRecord{Type: "CONTAINS", FromID: "elsewhere", ToID: "member"},
			unsupported: map[string]int{"CONTAINS": 1},
		},
		{
			name:          "missing parent is a missing source",
			relation:      RelationRecord{Type: "CONTAINS", FromID: "missing", ToID: "member"},
			missingSource: 1,
			unsupported:   map[string]int{},
		},
		{
			name:          "missing child is a missing target",
			relation:      RelationRecord{Type: "CONTAINS", FromID: "owner", ToID: "missing"},
			missingTarget: 1,
			unsupported:   map[string]int{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			records := append(append([]any{}, base...), test.relation, SnapshotSummary{})
			_, note := encodeSCIP(t, records...)
			requireSCIPRelationAccounting(t, note, test.missingSource, test.missingTarget, 0, test.unsupported)
		})
	}
}

// TestSCIPIsReferenceOnlyForMemberRelationships pins what is_reference means.
//
// It does not mark "this is a reference". It tells a consumer to MERGE the
// related symbol's references into this one's. That is right for a member
// override -- Find References on a base method should reach the override's
// callers -- and wrong for a type relationship, where it makes Find References
// on a base type report every subtype's definition as a reference to it.
func TestSCIPIsReferenceOnlyForMemberRelationships(t *testing.T) {
	for _, test := range []struct {
		name           string
		sourceKind     string
		targetKind     string
		targetExternal bool
		relation       string
		wantIsRefSet   bool
	}{
		{name: "type implements type", sourceKind: "struct", targetKind: "interface", relation: "IMPLEMENTS"},
		{name: "type extends type", sourceKind: "class", targetKind: "class", relation: "EXTENDS"},
		{name: "type inherits type", sourceKind: "interface", targetKind: "interface", relation: "INHERITS"},
		{name: "method overrides method", sourceKind: "method", targetKind: "method", relation: "OVERRIDES", wantIsRefSet: true},
		{name: "function implements function", sourceKind: "function", targetKind: "function", relation: "IMPLEMENTS", wantIsRefSet: true},
		{name: "method to type", sourceKind: "method", targetKind: "interface", relation: "OVERRIDES"},
		{name: "method to unknown kind", sourceKind: "method", targetKind: "unknown", relation: "OVERRIDES"},
		{name: "method to external method", sourceKind: "method", targetKind: "method", targetExternal: true, relation: "OVERRIDES", wantIsRefSet: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			records := []any{
				SnapshotHeader{RepoKey: "local/demo", Commit: "abc"},
				FileRecord{Path: "a.go", Language: "Go"},
			}
			if test.targetExternal {
				records = append(records, ExternalRecord{ID: "target", Kind: test.targetKind, Value: "Target", External: true})
			} else {
				records = append(records, SymbolRecord{ID: "target", Kind: test.targetKind, Name: "Target", FilePath: "a.go", StartLine: 1, EndLine: 2})
			}
			records = append(records,
				SymbolRecord{ID: "src", Kind: test.sourceKind, Name: "Source", FilePath: "a.go", StartLine: 4, EndLine: 5},
				RelationRecord{Type: test.relation, FromID: "src", ToID: "target",
					Evidence: []Evidence{{FilePath: "a.go", StartLine: 4, EndLine: 4}}},
				SnapshotSummary{},
			)
			index, _ := encodeSCIP(t, records...)
			var found *scippb.Relationship
			for _, doc := range index.GetDocuments() {
				for _, info := range doc.GetSymbols() {
					if strings.Contains(info.GetSymbol(), "Source") {
						for _, rel := range info.GetRelationships() {
							found = rel
						}
					}
				}
			}
			if found == nil {
				t.Fatal("no relationship emitted")
			}
			if !found.GetIsImplementation() {
				t.Error("is_implementation should always be set for this family")
			}
			if got := found.GetIsReference(); got != test.wantIsRefSet {
				t.Errorf("is_reference = %v, want %v", got, test.wantIsRefSet)
			}
		})
	}
}

func TestSCIPCountsMissingImplementationSource(t *testing.T) {
	index, note := encodeSCIP(t,
		SnapshotHeader{RepoKey: "local/demo", Commit: "abc"},
		FileRecord{Path: "a.go", Language: "Go"},
		SymbolRecord{ID: "target", Kind: "method", Name: "Target", FilePath: "a.go", StartLine: 1, EndLine: 2},
		RelationRecord{Type: "OVERRIDES", FromID: "missing", ToID: "target",
			Evidence: []Evidence{{FilePath: "a.go", StartLine: 4, EndLine: 4}}},
		SnapshotSummary{},
	)
	requireSCIPRelationAccounting(t, note, 1, 0, 0, map[string]int{})
	if note.EmittedImplementations != 0 || note.EmittedReferences != 1 {
		t.Fatalf("emitted implementations/references = %d/%d, want 0/1: %#v", note.EmittedImplementations, note.EmittedReferences, note)
	}
	for _, doc := range index.GetDocuments() {
		for _, info := range doc.GetSymbols() {
			if len(info.GetRelationships()) != 0 {
				t.Fatalf("relationship emitted without a local source: %#v", info.GetRelationships())
			}
		}
	}
}

func TestSCIPMissingSourceAccountingIsOptionalAndReset(t *testing.T) {
	encoded, err := json.Marshal(SCIPOmissionNote{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("missing_source_relations")) {
		t.Fatalf("zero omission note carries optional missing-source field: %s", encoded)
	}

	encoder := NewSCIPSnapshotEncoder(&bytes.Buffer{}, "1.0.0")
	for _, record := range []any{
		SnapshotHeader{RepoKey: "local/demo", Commit: "abc"},
		FileRecord{Path: "a.go", Language: "Go"},
		SymbolRecord{ID: "target", Kind: "method", Name: "Target", FilePath: "a.go", StartLine: 1, EndLine: 2},
		RelationRecord{Type: "OVERRIDES", FromID: "missing", ToID: "target",
			Evidence: []Evidence{{FilePath: "a.go", StartLine: 4, EndLine: 4}}},
	} {
		if err := encoder.Encode(record); err != nil {
			t.Fatalf("Encode(%T): %v", record, err)
		}
	}
	if _, err := encoder.marshalIndex(); err != nil {
		t.Fatal(err)
	}
	if got := encoder.OmissionNote().MissingSourceRelations; got != 1 {
		t.Fatalf("first marshal missing_source_relations = %d, want 1", got)
	}
	encoder.relations = nil
	if _, err := encoder.marshalIndex(); err != nil {
		t.Fatal(err)
	}
	if got := encoder.OmissionNote().MissingSourceRelations; got != 0 {
		t.Fatalf("second marshal retained missing_source_relations = %d, want 0", got)
	}
}

// TestSCIPDoesNotCountDefines guards both the native redundant form and
// malformed structural sources that the definition occurrence cannot prove.
//
// DEFINES is always represented -- the definition occurrence and the symbol
// record carry it -- only when the relation starts at the native file id for
// that symbol's non-empty FilePath.
func TestSCIPDoesNotCountDefines(t *testing.T) {
	base := []any{
		SnapshotHeader{RepoKey: "local/demo", Commit: "abc"},
		FileRecord{Path: "a.go", Language: "Go"},
		SymbolRecord{ID: "sym", Kind: "function", Name: "Fn", FilePath: "a.go", StartLine: 1, EndLine: 2},
	}
	for _, test := range []struct {
		name          string
		relation      RelationRecord
		missingTarget int
		unsupported   map[string]int
	}{
		{
			name:        "native file source is redundant",
			relation:    RelationRecord{Type: "DEFINES", FromID: fileID("local/demo", "a.go"), ToID: "sym"},
			unsupported: map[string]int{},
		},
		{
			name:        "mismatched file source is unsupported",
			relation:    RelationRecord{Type: "DEFINES", FromID: fileID("local/demo", "other.go"), ToID: "sym"},
			unsupported: map[string]int{"DEFINES": 1},
		},
		{
			name:          "missing child is a missing target",
			relation:      RelationRecord{Type: "DEFINES", FromID: fileID("local/demo", "a.go"), ToID: "missing"},
			missingTarget: 1,
			unsupported:   map[string]int{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			records := append(append([]any{}, base...), test.relation, SnapshotSummary{})
			_, note := encodeSCIP(t, records...)
			requireSCIPRelationAccounting(t, note, 0, test.missingTarget, 0, test.unsupported)
		})
	}

	t.Run("empty child path is unsupported", func(t *testing.T) {
		_, note := encodeSCIP(t,
			SnapshotHeader{RepoKey: "local/demo", Commit: "abc"},
			SymbolRecord{ID: "sym", Kind: "function", Name: "Fn", StartLine: 1, EndLine: 2},
			RelationRecord{Type: "DEFINES", FromID: fileID("local/demo", ""), ToID: "sym"},
			SnapshotSummary{},
		)
		requireSCIPRelationAccounting(t, note, 0, 0, 0, map[string]int{"DEFINES": 1})
	})
}
