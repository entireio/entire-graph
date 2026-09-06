package sem

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type validationCustom string

func (validationCustom) MarshalJSON() ([]byte, error) { return []byte(`"custom"`), nil }

func validationFixtureRecord() extractionRecord {
	return extractionRecord{
		RawImports:       []string{"import/name"},
		RelationFamilies: extractionRawImports,
		Version:          extractionFormatVersion,
		Language:         "TypeScript",
		Status: ParseStatus{
			Code:   "E_PARSE",
			Detail: "bounded diagnostic",
		},
		Declarations: []extractedDeclaration{{
			Kind:           "function",
			Name:           "name",
			Signature:      "name(value: T): U",
			BodyHash:       "body",
			Fingerprint:    "fingerprint",
			ParameterNames: []string{"value"},
			ParamTypeText:  "value: T",
			ReturnTypeText: "U",
		}},
	}
}

func TestValidateExtractionRecordCoversEveryStringField(t *testing.T) {
	record := validationFixtureRecord()
	got := extractionStringPaths(reflect.ValueOf(record), "record")
	want := []string{
		"record.RawImports[0]", "record.Language", "record.Status.Code", "record.Status.Detail",
		"record.Declarations[0].Kind", "record.Declarations[0].Name", "record.Declarations[0].Signature",
		"record.Declarations[0].BodyHash", "record.Declarations[0].Fingerprint", "record.Declarations[0].ParameterNames[0]",
		"record.Declarations[0].ParamTypeText", "record.Declarations[0].ReturnTypeText",
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("string-field coverage changed: got %v, want %v; update validator and this test", got, want)
	}
	mutations := map[string]func(*extractionRecord){
		"raw import":              func(record *extractionRecord) { record.RawImports[0] = "\xff" },
		"language":                func(record *extractionRecord) { record.Language = "\xff" },
		"status code":             func(record *extractionRecord) { record.Status.Code = "\xff" },
		"status detail":           func(record *extractionRecord) { record.Status.Detail = "\xff" },
		"declaration kind":        func(record *extractionRecord) { record.Declarations[0].Kind = "\xff" },
		"declaration name":        func(record *extractionRecord) { record.Declarations[0].Name = "\xff" },
		"declaration signature":   func(record *extractionRecord) { record.Declarations[0].Signature = "\xff" },
		"declaration body hash":   func(record *extractionRecord) { record.Declarations[0].BodyHash = "\xff" },
		"declaration fingerprint": func(record *extractionRecord) { record.Declarations[0].Fingerprint = "\xff" },
		"parameter name":          func(record *extractionRecord) { record.Declarations[0].ParameterNames[0] = "\xff" },
		"parameter type":          func(record *extractionRecord) { record.Declarations[0].ParamTypeText = "\xff" },
		"return type":             func(record *extractionRecord) { record.Declarations[0].ReturnTypeText = "\xff" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			record := validationFixtureRecord()
			mutate(&record)
			if err := validateExtractionRecord(record); err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
				t.Fatalf("validation error = %v, want invalid UTF-8", err)
			}
		})
	}
}

func extractionStringPaths(value reflect.Value, path string) []string {
	switch value.Kind() {
	case reflect.String:
		return []string{path}
	case reflect.Struct:
		var paths []string
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			paths = append(paths, extractionStringPaths(value.Field(index), path+"."+field.Name)...)
		}
		return paths
	case reflect.Slice:
		var paths []string
		for index := 0; index < value.Len(); index++ {
			paths = append(paths, extractionStringPaths(value.Index(index), fmt.Sprintf("%s[%d]", path, index))...)
		}
		return paths
	default:
		return nil
	}
}

func TestValidateExtractionRecordAllowsNilAndEmptySlices(t *testing.T) {
	record := validationFixtureRecord()
	record.RawImports = nil
	record.Declarations = []extractedDeclaration{{ParameterNames: []string{}}}
	if err := validateExtractionRecord(record); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var decoded extractionRecord
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RawImports != nil || decoded.Declarations[0].ParameterNames == nil {
		t.Fatalf("nil/empty slice distinction changed: %#v", decoded)
	}
}

func TestValidateExtractionRecordValidPathAllocatesNothing(t *testing.T) {
	record := validationFixtureRecord()
	if err := validateExtractionRecord(record); err != nil {
		t.Fatal(err)
	}
	if allocations := testing.AllocsPerRun(100, func() {
		if err := validateExtractionRecord(record); err != nil {
			t.Fatal(err)
		}
	}); allocations != 0 {
		t.Fatalf("valid extraction validation allocations = %v, want zero", allocations)
	}
}

func TestValidateExtractionValueRejectsFutureLossyShapes(t *testing.T) {
	type tagged struct {
		Value string `json:"value,omitempty"`
	}
	// A future field with a custom marshaler must not silently change the cache
	// payload without an explicit wire-format review.
	funcValue := reflect.ValueOf(struct {
		Value map[string]string
	}{Value: map[string]string{"key": "value"}})
	if err := validateExtractionValue(reflect.ValueOf(tagged{}), "tagged"); err == nil || !strings.Contains(err.Error(), "JSON tag") {
		t.Fatalf("tagged shape error = %v", err)
	}
	if err := validateExtractionValue(funcValue, "map"); err == nil || !strings.Contains(err.Error(), "unsupported JSON shape") {
		t.Fatalf("map shape error = %v", err)
	}
	if err := validateExtractionValue(reflect.ValueOf(validationCustom("value")), "custom"); err == nil || !strings.Contains(err.Error(), "custom JSON/text marshaler") {
		t.Fatalf("custom marshaler error = %v", err)
	}
}
