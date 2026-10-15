package sem

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// This pins the custom JSON hook's serialized contract independently of the
// classifier and fixture goldens. Any additional wire field needs the schema
// decision required by ADR 0001, even though it is invisible to reflection.
func TestRelationProvenanceSerializedContract(t *testing.T) {
	relation := RelationRecord{RecordType: "relation", FromID: "from", ToID: "to", Type: "CALLS", Confidence: 0.75, Reason: "resolved <call>", RelationScope: "file", Resolution: "package", TargetKind: "symbol", Evidence: []Evidence{{Kind: "call", FilePath: "main.go", StartLine: 3, EndLine: 4, Detail: "a -> b"}}, WarningCodes: []string{"EVIDENCE_TRUNCATED"}, EvidenceDropped: 2}
	want := `{"record_type":"relation","from_id":"from","to_id":"to","type":"CALLS","confidence":0.75,"reason":"resolved <call>","relation_scope":"file","resolution":"package","target_kind":"symbol","evidence":[{"kind":"call","file_path":"main.go","start_line":3,"end_line":4,"detail":"a -> b"}],"warning_codes":["EVIDENCE_TRUNCATED"],"evidence_dropped":2,"provenance":"EXTRACTED"}`
	got, err := json.Marshal(relation)
	if err != nil {
		t.Fatal(err)
	}
	var actual, expected map[string]any
	if err := json.Unmarshal(got, &actual); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("relation JSON = %s, want %s", got, want)
	}
	pointer, err := json.Marshal(&relation)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pointer) {
		t.Fatalf("value/pointer JSON differ: %s / %s", got, pointer)
	}
	// Older tolerant readers discard the additive field and preserve every input.
	var decoded RelationRecord
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, relation) {
		t.Fatalf("wire round trip changed relation: %#v", decoded)
	}
}

func TestRelationProvenanceSerializationUsesFinalInputs(t *testing.T) {
	relation := RelationRecord{RecordType: "relation", Type: "CALLS", Resolution: "exact"}
	for _, tc := range []struct{ kind, resolution, want string }{
		{"CALLS", "exact", "EXTRACTED"}, {"CALLS", "type_inferred", "INFERRED"},
		{"CALLS", "name_only", "AMBIGUOUS"}, {"HANDLES_GRPC", "exact", "INFERRED"},
		{"CALLS", "future-resolution", "AMBIGUOUS"}, {"CALLS", "", "AMBIGUOUS"},
	} {
		relation.Type, relation.Resolution = tc.kind, tc.resolution
		got, err := json.Marshal(relation)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(got, &fields); err != nil {
			t.Fatal(err)
		}
		if string(fields["provenance"]) != `"`+tc.want+`"` {
			t.Errorf("%s/%s serialized as %s", tc.kind, tc.resolution, got)
		}
		if _, ok := fields["evidence"]; ok {
			t.Fatal("empty evidence must remain omitted")
		}
		if string(fields["warning_codes"]) != "null" {
			t.Fatal("nil warning codes must remain null")
		}
	}
	// A wire-supplied label cannot override the authoritative inputs on re-export.
	if err := json.Unmarshal([]byte(`{"type":"CALLS","resolution":"none","provenance":"EXTRACTED"}`), &relation); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(relation)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(`"provenance":"AMBIGUOUS"`)) {
		t.Fatalf("trusted stored label: %s", got)
	}
}

func TestCompactSnapshotPreservesDerivedRelationProvenance(t *testing.T) {
	for _, tc := range []struct{ kind, resolution, want string }{
		{"CALLS", "package", "EXTRACTED"}, {"HANDLES_GRAPHQL", "exact", "INFERRED"}, {"CALLS", "name_only", "AMBIGUOUS"},
	} {
		records := compactFixtureRecords()
		relation := records[5].(RelationRecord)
		relation.Type, relation.Resolution = tc.kind, tc.resolution
		records[5] = relation
		compact, _ := encodeCompactFixture(t, records)
		decoded := decodedCompactRecords(t, compact)
		if !reflect.DeepEqual(publicRecordJSON(t, decoded), publicRecordJSON(t, records)) {
			t.Fatal("compact/native public projections differ")
		}
		if hashRecords(t, decoded) != hashRecords(t, records) {
			t.Fatal("compact/native canonical hashes differ")
		}
		got, err := json.Marshal(decoded[5])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(got, []byte(`"provenance":"`+tc.want+`"`)) {
			t.Fatalf("decoded relation lacks expected provenance: %s", got)
		}
	}
}

func TestSCIPProjectionDoesNotInventRelationProvenance(t *testing.T) {
	records := compactFixtureRecords()
	baseline, note := encodeSCIPFixture(t, records)
	for _, resolution := range []string{"exact", "type_inferred", "name_only"} {
		relation := records[5].(RelationRecord)
		relation.Resolution = resolution
		records[5] = relation
		data, encoder := encodeSCIPFixture(t, records)
		if !bytes.Equal(data, baseline) || !reflect.DeepEqual(encoder.OmissionNote(), note.OmissionNote()) {
			t.Fatalf("SCIP navigation projection changed for resolution %s", resolution)
		}
	}
	index := decodeSCIPIndex(t, baseline)
	if len(index.Documents) == 0 {
		t.Fatal("SCIP probe emitted no documents")
	}
}

func BenchmarkRelationProvenanceSerialization(b *testing.B) {
	relation := RelationRecord{RecordType: "relation", FromID: "symbol-from", ToID: "symbol-to", Type: "CALLS", Resolution: "package", Confidence: 1, Reason: "same package", WarningCodes: []string{}}
	type withoutHook RelationRecord
	for _, tc := range []struct {
		name   string
		record any
	}{{"without-provenance", withoutHook(relation)}, {"with-provenance", relation}} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := json.Marshal(tc.record); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
