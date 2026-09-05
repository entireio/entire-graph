package sem

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAdvantageSchemaAdditionsRemainOptional(t *testing.T) {
	if SchemaVersion != "1.2" {
		t.Fatalf("review additive schema contract for %s", SchemaVersion)
	}
	for _, item := range []struct {
		value  any
		fields map[string]string
	}{
		{SnapshotHeader{}, map[string]string{"Compiler": "compiler,omitempty", "OperationInputs": "operation_inputs,omitempty"}},
		{SnapshotSummary{}, map[string]string{"OperationInputs": "operation_inputs,omitempty"}},
		{SearchResponse{}, map[string]string{"Compiler": "compiler,omitempty", "OperationInputs": "operation_inputs,omitempty"}},
		{SearchResult{}, map[string]string{"Ranking": "ranking,omitempty"}},
		{SearchStats{}, map[string]string{"Ranking": "ranking,omitempty", "Extraction": "extraction,omitempty"}},
		{ProviderStats{}, map[string]string{"Extraction": "extraction,omitempty"}},
	} {
		typ := reflect.TypeOf(item.value)
		encoded, err := json.Marshal(item.value)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &object); err != nil {
			t.Fatal(err)
		}
		for field, tag := range item.fields {
			got, ok := typ.FieldByName(field)
			if !ok || got.Tag.Get("json") != tag || got.Type.Kind() != reflect.Pointer {
				t.Fatalf("%s.%s lost optional pointer contract", typ, field)
			}
			name := tag[:len(tag)-len(",omitempty")]
			if _, present := object[name]; present {
				t.Fatalf("default emits %s.%s", typ, name)
			}
		}
	}
}
