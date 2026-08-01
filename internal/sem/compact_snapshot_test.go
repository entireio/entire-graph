package sem

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func compactFixtureRecords() []any {
	header := SnapshotHeader{
		SchemaVersion: "snapshot-test", Provider: "provider", ProviderVersion: "v1",
		RepoRoot: "/tmp/repo", RepoKey: "local/repo", Commit: "commit", Tree: "tree",
		Languages: []string{"Go"}, LanguageTiers: map[string]string{"Go": "semantic"},
		Capabilities: []string{"ndjson"}, SchemaFeatures: []string{"feature"},
		LanguageVersions: map[string]string{"parser": "v"}, Profile: "full",
		ProfileLimits: ProfileLimits{Evidence: "full", CallResolution: "full"}, RelationSet: []string{"CALLS"},
		SkippedRelations: []string{}, Warnings: []ProviderWarning{{Code: "W", Severity: "warning", FilePath: "main.go", EffectOnCompleteness: "partial", Detail: "detail"}},
		PartialFailures: []PartialFailure{{Code: "E", Severity: "error", FilePath: "bad.go", EffectOnCompleteness: "partial", Detail: "detail"}},
		Stats:           ProviderStats{Files: 1, ParsedFiles: 1, Symbols: 2, Relations: 1, CompletenessLevel: "ok"},
		Completeness:    CompletenessReport{Languages: map[string]LanguageCompleteness{"Go": {Files: 1, Symbols: 2}}, Relations: map[string]int{"CALLS": 1}},
	}
	return []any{
		header,
		FileRecord{RecordType: "file", ID: "file-id", Path: "main.go", Blob: "blob", Language: "Go", Bytes: 42},
		ExternalRecord{RecordType: "external", ID: "ext-id", Kind: "module", Value: "example", FilePath: "main.go", StartLine: 2, EndLine: 3, Signature: "sig", Language: "Go", External: true, SourceSymbol: "source", SourceDetails: "details"},
		SymbolRecord{RecordType: "symbol", ID: "symbol-id", StableIDVersion: "v1", Kind: "function", Name: "same", QualifiedName: "pkg.same", FilePath: "main.go", StartLine: 5, EndLine: 9, Signature: "func same()", BodyHash: "hash", Language: "Go", ContainerID: "container", Aliases: []string{"alias"}},
		SymbolRecord{RecordType: "symbol", ID: "symbol-id-2", StableIDVersion: "v1", Kind: "method", Name: "same", QualifiedName: "other.same", FilePath: "other.go", StartLine: 1, EndLine: 2, Signature: "func same()", BodyHash: "hash2", Language: "Go", ContainerID: "other", Aliases: []string{}},
		RelationRecord{RecordType: "relation", FromID: "symbol-id", ToID: "symbol-id-2", Type: "CALLS", Confidence: 0.75, Reason: "reason", RelationScope: "scope", Resolution: "resolved", TargetKind: "method", Evidence: []Evidence{{Kind: "call", FilePath: "main.go", StartLine: 6, EndLine: 6, Detail: "detail"}}, WarningCodes: []string{"W1"}},
		SnapshotSummary{RecordType: "summary", Languages: []string{"Go"}, LanguageTiers: map[string]string{"Go": "semantic"}, Warnings: []ProviderWarning{}, PartialFailures: []PartialFailure{}, Stats: ProviderStats{Files: 1, ParsedFiles: 1, Symbols: 2, Relations: 1, CompletenessLevel: "ok"}, Completeness: CompletenessReport{Languages: map[string]LanguageCompleteness{"Go": {Files: 1, Symbols: 2}}, Relations: map[string]int{"CALLS": 1}}},
	}
}

func encodeCompactFixture(t *testing.T, records []any) ([]byte, *CompactSnapshotEncoder) {
	t.Helper()
	var out bytes.Buffer
	encoder := NewCompactSnapshotEncoder(&out)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	return out.Bytes(), encoder
}

func publicRecordJSON(t *testing.T, records []any) []json.RawMessage {
	t.Helper()
	result := make([]json.RawMessage, 0, len(records))
	for _, record := range records {
		data, err := publicSnapshotRecordJSON(record)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, data)
	}
	return result
}

func decodedCompactRecords(t *testing.T, data []byte) []any {
	t.Helper()
	var records []any
	if err := DecodeCompactSnapshot(bytes.NewReader(data), func(record any) error { records = append(records, record); return nil }); err != nil {
		t.Fatal(err)
	}
	return records
}

func TestCompactSnapshotRoundTripPreservesPublicProjection(t *testing.T) {
	records := compactFixtureRecords()
	data, _ := encodeCompactFixture(t, records)
	decoded := decodedCompactRecords(t, data)
	if got, want := publicRecordJSON(t, decoded), publicRecordJSON(t, records); !reflect.DeepEqual(got, want) {
		t.Fatalf("public projection mismatch\n got=%s\nwant=%s", got, want)
	}
	if got, want := hashRecords(t, decoded), hashRecords(t, records); got != want {
		t.Fatalf("canonical hash mismatch: got %s want %s", got, want)
	}
}

func TestCompactSnapshotEncodingIsDeterministic(t *testing.T) {
	first, _ := encodeCompactFixture(t, compactFixtureRecords())
	second, _ := encodeCompactFixture(t, compactFixtureRecords())
	if !bytes.Equal(first, second) {
		t.Fatal("compact output differs across identical inputs")
	}
}

func TestCompactSnapshotDictionariesAreMeasuredInRawBytes(t *testing.T) {
	data, encoder := encodeCompactFixture(t, compactFixtureRecords())
	var dictionaryBytes int64
	for _, line := range bytes.Split(data, []byte("\n")) {
		if bytes.HasPrefix(line, []byte(`["d",`)) {
			dictionaryBytes += int64(len(line) + 1)
		}
	}
	if got := encoder.DictionaryBytes(); got != dictionaryBytes {
		t.Fatalf("dictionary bytes = %d, want %d", got, dictionaryBytes)
	}
	if int64(len(data)) < encoder.DictionaryBytes() {
		t.Fatalf("total bytes excludes dictionary bytes: total=%d dictionary=%d", len(data), encoder.DictionaryBytes())
	}
}

func TestCompactSnapshotCanonicalHashMatchesNativeNDJSON(t *testing.T) {
	records := compactFixtureRecords()
	data, _ := encodeCompactFixture(t, records)
	if got, want := hashRecords(t, decodedCompactRecords(t, data)), hashRecords(t, records); got != want {
		t.Fatalf("hash = %s, want %s", got, want)
	}
}

func TestCompactSnapshotPreservesNilWarningCodes(t *testing.T) {
	records := compactFixtureRecords()
	records[5] = RelationRecord{RecordType: "relation", FromID: "symbol-id", ToID: "symbol-id-2", Type: "CALLS", WarningCodes: nil}
	data, _ := encodeCompactFixture(t, records)
	decoded := decodedCompactRecords(t, data)
	if got, want := publicRecordJSON(t, decoded), publicRecordJSON(t, records); !reflect.DeepEqual(got, want) {
		t.Fatalf("nil warning codes changed public projection\n got=%s\nwant=%s", got, want)
	}
}

func TestCompactSnapshotIsSmallerThanNDJSONIncludingDictionaries(t *testing.T) {
	records := compactFixtureRecords()
	for i := 0; i < 20; i++ {
		records = append(records[:len(records)-1], SymbolRecord{RecordType: "symbol", ID: "repeat-id", StableIDVersion: "v1", Kind: "function", Name: "repeat", QualifiedName: "pkg.repeat", FilePath: "repeat.go", Signature: "func repeat()", BodyHash: "repeat-hash", Language: "Go"}, RelationRecord{RecordType: "relation", FromID: "repeat-id", ToID: "symbol-id", Type: "CALLS", Reason: "repeat", WarningCodes: []string{}}, records[len(records)-1])
	}
	compact, _ := encodeCompactFixture(t, records)
	var native bytes.Buffer
	for _, record := range records {
		data, err := publicSnapshotRecordJSON(record)
		if err != nil {
			t.Fatal(err)
		}
		native.Write(data)
		native.WriteByte('\n')
	}
	if float64(len(compact)) >= 0.80*float64(native.Len()) {
		t.Fatalf("compact size = %d, native size = %d", len(compact), native.Len())
	}
}

func TestCompactSnapshotDecoderRejectsUnknownVersion(t *testing.T) {
	if err := DecodeCompactSnapshot(strings.NewReader(`["h",2,{}]\n`), func(any) error { return nil }); err == nil {
		t.Fatal("expected version error")
	}
}
func TestCompactSnapshotDecoderRejectsWrongArity(t *testing.T) {
	if err := DecodeCompactSnapshot(strings.NewReader(`["h",1,{}]\n["d",1]\n`), func(any) error { return nil }); err == nil {
		t.Fatal("expected arity error")
	}
}
func TestCompactSnapshotDecoderRequiresHeaderDictionaryThenSummary(t *testing.T) {
	for _, text := range []string{`["d",1,["x"]]\n`, `["h",1,{}]\n["f",0,0,0,0,0]\n["m",{}]\n`, `["h",1,{}]\n["d",1,["x"]]\n`} {
		if err := DecodeCompactSnapshot(strings.NewReader(text), func(any) error { return nil }); err == nil {
			t.Fatalf("expected ordering error for %q", text)
		}
	}
}

func hashRecords(t *testing.T, records []any) string {
	t.Helper()
	h := NewSnapshotSemanticHasher()
	for _, record := range records {
		if err := h.Add(record); err != nil {
			t.Fatal(err)
		}
	}
	return h.SumHex()
}

func TestSnapshotHasherUsesSHA256(t *testing.T) {
	h := NewSnapshotSemanticHasher()
	_ = h.Add(SnapshotHeader{})
	if got := h.SumHex(); len(got) != hex.EncodedLen(sha256.Size) {
		t.Fatalf("hash length = %d", len(got))
	}
}
