package sem

import (
	"bytes"
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

// TestSCIPCountsUnrepresentedContainment covers memberships SCIP cannot carry.
//
// DEFINES and CONTAINS are normally redundant, because symbol metadata already
// expresses the membership through EnclosingSymbol. That holds only when the
// relation agrees with ContainerID. An extension member -- a method attached to
// a receiver declared elsewhere -- produces a CONTAINS whose parent is NOT the
// symbol's container, and that membership survives in no other field. Skipping
// it silently let the note report a completeness the protobuf did not have.
func TestSCIPCountsUnrepresentedContainment(t *testing.T) {
	base := []any{
		SnapshotHeader{RepoKey: "local/demo", Commit: "abc"},
		FileRecord{Path: "a.go", Language: "Go"},
		SymbolRecord{ID: "owner", Kind: "struct", Name: "Owner", FilePath: "a.go", StartLine: 1, EndLine: 2},
		SymbolRecord{ID: "elsewhere", Kind: "struct", Name: "Elsewhere", FilePath: "a.go", StartLine: 4, EndLine: 5},
		SymbolRecord{ID: "member", Kind: "method", Name: "Member", FilePath: "a.go", ContainerID: "owner", StartLine: 7, EndLine: 8},
	}

	// Agrees with ContainerID: EnclosingSymbol already carries it, so it is
	// genuinely redundant and must not be counted.
	_, note := encodeSCIP(t, append(append([]any{}, base...),
		RelationRecord{Type: "CONTAINS", FromID: "owner", ToID: "member"},
		SnapshotSummary{})...)
	if got := note.UnsupportedRelationCounts["CONTAINS"]; got != 0 {
		t.Errorf("redundant CONTAINS counted %d times, want 0", got)
	}

	// Disagrees with ContainerID: nothing else expresses it, so dropping it
	// silently would overstate what the index contains.
	_, note = encodeSCIP(t, append(append([]any{}, base...),
		RelationRecord{Type: "CONTAINS", FromID: "elsewhere", ToID: "member"},
		SnapshotSummary{})...)
	if got := note.UnsupportedRelationCounts["CONTAINS"]; got != 1 {
		t.Errorf("unrepresented CONTAINS counted %d times, want 1", got)
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
		name         string
		kind         string
		relation     string
		wantIsRefSet bool
	}{
		{"type implements", "struct", "IMPLEMENTS", false},
		{"type extends", "class", "EXTENDS", false},
		{"type inherits", "interface", "INHERITS", false},
		{"method overrides", "method", "OVERRIDES", true},
		{"function implements", "function", "IMPLEMENTS", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			index, _ := encodeSCIP(t,
				SnapshotHeader{RepoKey: "local/demo", Commit: "abc"},
				FileRecord{Path: "a.go", Language: "Go"},
				SymbolRecord{ID: "target", Kind: "interface", Name: "Target", FilePath: "a.go", StartLine: 1, EndLine: 2},
				SymbolRecord{ID: "src", Kind: test.kind, Name: "Source", FilePath: "a.go", StartLine: 4, EndLine: 5},
				RelationRecord{Type: test.relation, FromID: "src", ToID: "target",
					Evidence: []Evidence{{FilePath: "a.go", StartLine: 4, EndLine: 4}}},
				SnapshotSummary{},
			)
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

// TestSCIPDoesNotCountDefines guards the over-count this fix nearly shipped.
//
// DEFINES is always represented -- the definition occurrence and the symbol
// record carry it -- but a file DEFINES a symbol whose ContainerID is not the
// file, so a containment check written for CONTAINS also fires for every
// DEFINES. On this repository that reported 9,877 of them as lost.
func TestSCIPDoesNotCountDefines(t *testing.T) {
	_, note := encodeSCIP(t,
		SnapshotHeader{RepoKey: "local/demo", Commit: "abc"},
		FileRecord{Path: "a.go", Language: "Go"},
		SymbolRecord{ID: "sym", Kind: "function", Name: "Fn", FilePath: "a.go", StartLine: 1, EndLine: 2},
		RelationRecord{Type: "DEFINES", FromID: "file:a.go", ToID: "sym"},
		SnapshotSummary{},
	)
	if got := note.UnsupportedRelationCounts["DEFINES"]; got != 0 {
		t.Fatalf("DEFINES counted %d times, want 0 -- it is represented by the definition occurrence", got)
	}
}
