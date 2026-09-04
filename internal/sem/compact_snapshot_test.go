package sem

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	scippb "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func compactFixtureRecords() []any {
	header := SnapshotHeader{
		SchemaVersion: SchemaVersion, Provider: "provider", ProviderVersion: "v1",
		RepoRoot: "/tmp/repo", RepoKey: "local/repo", Commit: "commit", Tree: "tree",
		Languages: []string{"Go"}, LanguageTiers: map[string]string{"Go": "semantic"},
		Capabilities: []string{"ndjson"}, SchemaFeatures: []string{"feature"},
		LanguageVersions: map[string]string{"parser": "v"}, Profile: "full",
		ProfileLimits: ProfileLimits{Evidence: "full", CallResolution: "full"}, RelationSet: []string{"CALLS"},
		SkippedRelations: []string{}, Warnings: []ProviderWarning{{Code: "W", Severity: "warning", FilePath: "main.go", EffectOnCompleteness: "partial", Detail: "detail"}},
		PartialFailures:  []PartialFailure{{Code: "E", Severity: "error", FilePath: "bad.go", EffectOnCompleteness: "partial", Detail: "detail"}},
		Stats:            ProviderStats{Files: 1, ParsedFiles: 1, Symbols: 2, Relations: 2, CompletenessLevel: "ok"},
		Completeness:     CompletenessReport{Languages: map[string]LanguageCompleteness{"Go": {Files: 1, Symbols: 2}}, Relations: map[string]int{"CALLS": 1, "IMPORTS": 1}},
		BenchmarkProfile: "generic-profile",
	}
	return []any{
		header,
		FileRecord{RecordType: "file", ID: "file-id", Path: "main.go", Blob: "blob", Language: "Go", Bytes: 42},
		ExternalRecord{RecordType: "external", ID: "ext-id", Kind: "module", Value: "example", FilePath: "main.go", StartLine: 2, EndLine: 3, Signature: "sig", Language: "Go", External: true, SourceSymbol: "source", SourceDetails: "details"},
		SymbolRecord{RecordType: "symbol", ID: "symbol-id", StableIDVersion: "v1", Kind: "function", Name: "same", QualifiedName: "pkg.same", FilePath: "main.go", StartLine: 5, EndLine: 9, Signature: "func same()", BodyHash: "hash", Language: "Go", ContainerID: "container", Aliases: []string{"alias"}},
		SymbolRecord{RecordType: "symbol", ID: "symbol-id-2", StableIDVersion: "v1", Kind: "method", Name: "same", QualifiedName: "other.same", FilePath: "other.go", StartLine: 1, EndLine: 2, Signature: "func same()", BodyHash: "hash2", Language: "Go", ContainerID: "other", Aliases: []string{}},
		RelationRecord{RecordType: "relation", FromID: "symbol-id", ToID: "symbol-id-2", Type: "CALLS", Confidence: 0.75, Reason: "reason", RelationScope: "scope", Resolution: "resolved", TargetKind: "method", Evidence: []Evidence{{Kind: "call", FilePath: "main.go", StartLine: 6, EndLine: 6, Detail: "detail"}}, WarningCodes: []string{"EVIDENCE_TRUNCATED"}, EvidenceDropped: 2},
		RelationRecord{RecordType: "relation", FromID: "symbol-id", ToID: "external-target", Type: "IMPORTS", Confidence: 1, Reason: "import", WarningCodes: []string{}},
		SnapshotSummary{RecordType: "summary", Languages: []string{"Go"}, LanguageTiers: map[string]string{"Go": "semantic"}, Warnings: []ProviderWarning{}, PartialFailures: []PartialFailure{}, Stats: ProviderStats{Files: 1, ParsedFiles: 1, Symbols: 2, Relations: 2, CompletenessLevel: "ok"}, Completeness: CompletenessReport{Languages: map[string]LanguageCompleteness{"Go": {Files: 1, Symbols: 2}}, Relations: map[string]int{"CALLS": 1, "IMPORTS": 1}}},
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

func encodeSCIPFixture(t *testing.T, records []any) ([]byte, *SCIPSnapshotEncoder) {
	t.Helper()
	var out bytes.Buffer
	encoder := NewSCIPSnapshotEncoder(&out, "1.2.3")
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	return out.Bytes(), encoder
}

func decodeSCIPIndex(t *testing.T, data []byte) *scippb.Index {
	t.Helper()
	var index scippb.Index
	if err := proto.Unmarshal(data, &index); err != nil {
		t.Fatalf("scip output is not a valid Index protobuf: %v", err)
	}
	return &index
}

type scipShortWriter struct{}

func (scipShortWriter) Write(payload []byte) (int, error) {
	if len(payload) == 0 {
		return 0, nil
	}
	return len(payload) - 1, nil
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
	if _, err := DecodeCompactSnapshot(bytes.NewReader(data), func(record any) error { records = append(records, record); return nil }); err != nil {
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
	var headerBytes, dictionaryBytes, dataBytes, summaryBytes int64
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		size := int64(len(line) + 1)
		switch {
		case bytes.HasPrefix(line, []byte(`["h",`)):
			headerBytes += size
		case bytes.HasPrefix(line, []byte(`["d",`)):
			dictionaryBytes += size
		case bytes.HasPrefix(line, []byte(`["m",`)):
			summaryBytes += size
		default:
			dataBytes += size
		}
	}
	if got := encoder.DictionaryBytes(); got != dictionaryBytes {
		t.Fatalf("dictionary bytes = %d, want %d", got, dictionaryBytes)
	}
	if got, want := int64(len(data)), headerBytes+dictionaryBytes+dataBytes+summaryBytes; got != want {
		t.Fatalf("total bytes = %d, want component sum %d (header=%d dictionary=%d data=%d summary=%d)", got, want, headerBytes, dictionaryBytes, dataBytes, summaryBytes)
	}
}

func TestCompactSnapshotCanonicalHashMatchesNativeNDJSON(t *testing.T) {
	records := compactFixtureRecords()
	data, _ := encodeCompactFixture(t, records)
	if got, want := hashRecords(t, decodedCompactRecords(t, data)), hashRecords(t, records); got != want {
		t.Fatalf("hash = %s, want %s", got, want)
	}
}

func TestSCIPSnapshotEncoderEmitsDefinitionsReferencesAndOmissions(t *testing.T) {
	records := compactFixtureRecords()
	summary := records[len(records)-1]
	records = append(records[:len(records)-1], RelationRecord{
		RecordType: "relation",
		FromID:     "symbol-id",
		ToID:       "symbol-id-2",
		Type:       "DATA_FLOWS",
		Evidence:   []Evidence{{FilePath: "main.go", StartLine: 7, EndLine: 7}},
	}, summary)
	first, firstEncoder := encodeSCIPFixture(t, records)
	second, secondEncoder := encodeSCIPFixture(t, records)
	if !bytes.Equal(first, second) {
		t.Fatal("scip output differs across identical inputs")
	}
	if got, want := firstEncoder.OmissionNote(), secondEncoder.OmissionNote(); !reflect.DeepEqual(got, want) {
		t.Fatalf("scip omission notes differ\n got=%#v\nwant=%#v", got, want)
	}
	index := decodeSCIPIndex(t, first)
	if got := index.GetMetadata().GetToolInfo().GetName(); got != ProviderName {
		t.Fatalf("tool name = %q, want %q", got, ProviderName)
	}
	if got := index.GetMetadata().GetTextDocumentEncoding(); got != scippb.TextEncoding_UTF8 {
		t.Fatalf("text encoding = %v, want UTF8", got)
	}
	documents := map[string]*scippb.Document{}
	for _, doc := range index.GetDocuments() {
		documents[doc.GetRelativePath()] = doc
	}
	mainDoc := documents["main.go"]
	if mainDoc == nil {
		t.Fatalf("scip index omitted main.go: %#v", index.GetDocuments())
	}
	if got := mainDoc.GetLanguage(); got != "Go" {
		t.Fatalf("main.go language = %q, want Go", got)
	}
	if got := mainDoc.GetPositionEncoding(); got != scippb.PositionEncoding_UTF8CodeUnitOffsetFromLineStart {
		t.Fatalf("position encoding = %v, want UTF8 byte offsets", got)
	}
	definitions := 0
	references := 0
	displayNames := map[string]bool{}
	for _, doc := range index.GetDocuments() {
		for _, info := range doc.GetSymbols() {
			parsed, err := scippb.ParseSymbol(info.GetSymbol())
			if err != nil {
				t.Fatalf("invalid SCIP symbol %q: %v", info.GetSymbol(), err)
			}
			descriptors := parsed.GetDescriptors()
			if len(descriptors) == 0 || descriptors[len(descriptors)-1].GetSuffix() != scippb.Descriptor_Method {
				t.Fatalf("callable symbol has non-method descriptor: %#v", parsed)
			}
			displayNames[info.GetDisplayName()] = true
		}
		for _, occurrence := range doc.GetOccurrences() {
			sourceRange, ok := occurrence.SourceRange()
			if !ok {
				t.Fatalf("occurrence has no typed source range: %#v", occurrence)
			}
			if sourceRange.Start.Compare(sourceRange.End) >= 0 {
				t.Fatalf("occurrence has an empty or reversed source range: %#v", occurrence)
			}
			if _, err := scippb.ParseSymbol(occurrence.GetSymbol()); err != nil {
				t.Fatalf("occurrence has invalid SCIP symbol %q: %v", occurrence.GetSymbol(), err)
			}
			if occurrence.GetSymbolRoles()&int32(scippb.SymbolRole_Definition) != 0 {
				definitions++
			} else {
				references++
			}
		}
	}
	if !displayNames["same"] {
		t.Fatalf("scip index omitted symbol display name 'same': %#v", index.GetDocuments())
	}
	if definitions != 2 || references != 1 {
		t.Fatalf("occurrence counts definitions=%d references=%d, want 2/1", definitions, references)
	}
	if got := len(index.GetExternalSymbols()); got != 1 {
		t.Fatalf("external symbol count = %d, want 1", got)
	}
	for _, info := range index.GetExternalSymbols() {
		parsed, err := scippb.ParseSymbol(info.GetSymbol())
		if err != nil {
			t.Fatalf("invalid external SCIP symbol %q: %v", info.GetSymbol(), err)
		}
		descriptors := parsed.GetDescriptors()
		if len(descriptors) == 0 || descriptors[len(descriptors)-1].GetSuffix() != scippb.Descriptor_Namespace {
			t.Fatalf("module external has non-namespace descriptor: %#v", parsed)
		}
	}
	note := firstEncoder.OmissionNote()
	if note.EmittedDefinitions != 2 || note.EmittedReferences != 1 || note.MissingTargetRelations != 1 || note.MissingEvidenceRelations != 0 || note.UnsupportedRelationCounts["DATA_FLOWS"] != 1 {
		t.Fatalf("unexpected scip omission note: %#v", note)
	}
}

func TestSCIPSnapshotEncoderMarksWorktreeProvenance(t *testing.T) {
	records := compactFixtureRecords()
	summary := records[len(records)-1].(SnapshotSummary)
	summary.Warnings = append(summary.Warnings, ProviderWarning{Code: "W_WORKTREE_SNAPSHOT"})
	records[len(records)-1] = summary

	payload, encoder := encodeSCIPFixture(t, records)
	index := decodeSCIPIndex(t, payload)
	if got := index.GetMetadata().GetToolInfo().GetArguments(); !reflect.DeepEqual(got, []string{"snapshot", "--format", "scip", "--worktree"}) {
		t.Fatalf("worktree tool arguments = %#v", got)
	}
	if !encoder.OmissionNote().WorktreeSnapshot {
		t.Fatalf("worktree snapshot is absent from omission note: %#v", encoder.OmissionNote())
	}
	for _, doc := range index.GetDocuments() {
		for _, info := range doc.GetSymbols() {
			parsed, err := scippb.ParseSymbol(info.GetSymbol())
			if err != nil {
				t.Fatal(err)
			}
			// Provenance is carried by the omission note and Metadata
			// arguments asserted above, NOT by the package version. The
			// version is the project's own and must be unaffected by
			// worktree-ness, so a symbol keeps one identity whether it was
			// indexed from a commit or from the working tree.
			if got := parsed.GetPackage().GetVersion(); got != "1.2.3" {
				t.Fatalf("worktree symbol package version = %q, want the project version unchanged", got)
			}
		}
	}
}

func TestSCIPSnapshotEncoderMarksNoHEADFallbackProvenance(t *testing.T) {
	records := compactFixtureRecords()
	summary := records[len(records)-1].(SnapshotSummary)
	summary.Warnings = append(summary.Warnings, ProviderWarning{Code: "E_NO_GIT_HEAD"})
	records[len(records)-1] = summary

	payload, encoder := encodeSCIPFixture(t, records)
	index := decodeSCIPIndex(t, payload)
	if got := index.GetMetadata().GetToolInfo().GetArguments(); !reflect.DeepEqual(got, []string{"snapshot", "--format", "scip"}) {
		t.Fatalf("fallback tool arguments = %#v", got)
	}
	if !encoder.OmissionNote().WorktreeSnapshot {
		t.Fatalf("working-tree fallback is absent from omission note: %#v", encoder.OmissionNote())
	}
	for _, doc := range index.GetDocuments() {
		for _, info := range doc.GetSymbols() {
			parsed, err := scippb.ParseSymbol(info.GetSymbol())
			if err != nil {
				t.Fatal(err)
			}
			// Provenance is carried by the omission note and Metadata
			// arguments asserted above, NOT by the package version. The
			// version is the project's own and must be unaffected by
			// worktree-ness, so a symbol keeps one identity whether it was
			// indexed from a commit or from the working tree.
			if got := parsed.GetPackage().GetVersion(); got != "1.2.3" {
				t.Fatalf("fallback symbol package version = %q, want the project version unchanged", got)
			}
		}
	}
}

func TestSCIPLanguageUsesCanonicalNames(t *testing.T) {
	cases := map[string]string{
		"Bash":              "ShellScript",
		"C#":                "CSharp",
		"C++":               "CPP",
		"CoffeeScript":      "Coffeescript",
		"Common Lisp":       "CommonLisp",
		"F#":                "FSharp",
		"Go":                "Go",
		"INI":               "Ini",
		"JSON":              "JSON",
		"Just":              "Justfile",
		"Make":              "Makefile",
		"MATLAB":            "Matlab",
		"Objective-C":       "Objective_C",
		"Objective-C++":     "Objective_CPP",
		"Protocol Buffers":  "Protobuf",
		"reStructuredText":  "ReST",
		"Starlark":          "Skylark",
		"TypeScript":        "TypeScript",
		"Unknown Lang":      "Unknown Lang",
		"Visual Basic .NET": "VisualBasic",
	}
	for input, want := range cases {
		if got := scipLanguage(input); got != want {
			t.Fatalf("scipLanguage(%q) = %q, want %q", input, got, want)
		}
	}
	if got := scipSignatureDocumentation("Go", ""); got != nil {
		t.Fatalf("empty signature produced metadata: %#v", got)
	}
}

func TestSCIPKindsUseStandardKindsAndDescriptors(t *testing.T) {
	cases := []struct {
		kind       string
		wantKind   scippb.SymbolInformation_Kind
		wantSuffix scippb.Descriptor_Suffix
	}{
		{kind: "function", wantKind: scippb.SymbolInformation_Function, wantSuffix: scippb.Descriptor_Method},
		{kind: "macro", wantKind: scippb.SymbolInformation_Macro, wantSuffix: scippb.Descriptor_Macro},
		{kind: "message", wantKind: scippb.SymbolInformation_Message, wantSuffix: scippb.Descriptor_Type},
		{kind: "module", wantKind: scippb.SymbolInformation_Module, wantSuffix: scippb.Descriptor_Namespace},
		{kind: "rpc", wantKind: scippb.SymbolInformation_Method, wantSuffix: scippb.Descriptor_Method},
		{kind: "trait", wantKind: scippb.SymbolInformation_Trait, wantSuffix: scippb.Descriptor_Type},
		{kind: "type_alias", wantKind: scippb.SymbolInformation_TypeAlias, wantSuffix: scippb.Descriptor_Type},
		{kind: "union", wantKind: scippb.SymbolInformation_Union, wantSuffix: scippb.Descriptor_Type},
	}
	for _, test := range cases {
		if got := scipKind(test.kind); got != test.wantKind {
			t.Fatalf("scipKind(%q) = %v, want %v", test.kind, got, test.wantKind)
		}
		parsed, err := scippb.ParseSymbol("entire-graph . repo version " + scipDescriptor("name", test.kind, "abcdef123456"))
		if err != nil {
			t.Fatalf("parse descriptor for %q: %v", test.kind, err)
		}
		descriptors := parsed.GetDescriptors()
		if len(descriptors) != 1 || descriptors[0].GetSuffix() != test.wantSuffix {
			t.Fatalf("descriptor for %q = %#v, want suffix %v", test.kind, descriptors, test.wantSuffix)
		}
	}
}

func TestSCIPSymbolEscapingRoundTrips(t *testing.T) {
	header := SnapshotHeader{RepoKey: "owner/repo with space", Commit: "work tree"}
	// A version carrying a space still exercises the package-component doubling
	// that used to be covered through Commit, which is no longer the version.
	const projectVersion = "1.2.3 beta"
	record := SymbolRecord{
		ID:       "compound-v1:odd-symbol",
		Kind:     "function",
		Name:     "render`item/name",
		FilePath: "dir with space/source.go",
	}
	parsed, err := scippb.ParseSymbol(scipSymbol(header, projectVersion, record))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.GetPackage().GetName() != header.RepoKey || parsed.GetPackage().GetVersion() != projectVersion {
		t.Fatalf("escaped package did not round trip: %#v", parsed.GetPackage())
	}
	descriptors := parsed.GetDescriptors()
	if len(descriptors) != 2 || descriptors[0].GetName() != record.FilePath || descriptors[1].GetName() != record.Name {
		t.Fatalf("escaped descriptors did not round trip: %#v", descriptors)
	}
}

func TestSCIPSnapshotEncoderReportsShortWrites(t *testing.T) {
	encoder := NewSCIPSnapshotEncoder(scipShortWriter{}, "1.2.3")
	for _, record := range compactFixtureRecords() {
		err := encoder.Encode(record)
		if _, final := record.(SnapshotSummary); final {
			if !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("summary encode error = %v, want io.ErrShortWrite", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("record encode failed before summary: %v", err)
		}
	}
	t.Fatal("fixture omitted snapshot summary")
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
	}
	if float64(len(compact)) >= 0.80*float64(native.Len()) {
		t.Fatalf("compact size = %d, native size = %d", len(compact), native.Len())
	}
}

func TestCompactSnapshotDecoderRejectsUnknownVersion(t *testing.T) {
	requireCompactDecodeError(t, "[\"h\",2,{}]\n", "unsupported compact snapshot version 2")
}
func TestCompactSnapshotDecoderRejectsWrongArity(t *testing.T) {
	requireCompactDecodeError(t, compactHeaderLine()+"[\"d\",1]\n", "dictionary has invalid placement or arity")
}
func TestCompactSnapshotDecoderRejectsNegativeEvidenceDropped(t *testing.T) {
	requireCompactDecodeError(t, compactHeaderLine()+"[\"r\",0,0,0,0,0,0,0,0,[],[],-1]\n", "evidence_dropped -1 must be non-negative")
}
func TestCompactSnapshotDecoderRequiresHeaderDictionaryThenSummary(t *testing.T) {
	requireCompactDecodeError(t, "[\"d\",1,[\"x\"]]\n", "dictionary has invalid placement")
	requireCompactDecodeError(t, compactHeaderLine()+"[\"d\",1,[\"x\"]]\n", "missing summary")
}

func TestCompactSnapshotDecoderAllowsIndexZeroOnlyDataWithoutDictionaryLine(t *testing.T) {
	records := []any{SnapshotHeader{SchemaVersion: SchemaVersion}, FileRecord{RecordType: "file", Bytes: 7}, SnapshotSummary{RecordType: "summary"}}
	data, _ := encodeCompactFixture(t, records)
	if bytes.Contains(data, []byte(`["d",`)) {
		t.Fatalf("index-zero-only record unexpectedly emitted dictionary: %s", data)
	}
	decoded := decodedCompactRecords(t, data)
	if got, want := publicRecordJSON(t, decoded), publicRecordJSON(t, records); !reflect.DeepEqual(got, want) {
		t.Fatalf("index-zero round trip changed projection\n got=%s\nwant=%s", got, want)
	}
}

func TestCompactSnapshotDecoderRejectsUnknownTag(t *testing.T) {
	requireCompactDecodeError(t, compactHeaderLine()+"[\"z\"]\n", `unknown compact snapshot tag "z"`)
}
func TestCompactSnapshotDecoderRejectsOutOfBoundsDictionaryIndex(t *testing.T) {
	requireCompactDecodeError(t, compactHeaderLine()+"[\"f\",1,0,0,0,0]\n", "dictionary index 1 is out of range")
}
func TestCompactSnapshotDecoderRejectsDuplicateDictionaryString(t *testing.T) {
	requireCompactDecodeError(t, compactHeaderLine()+"[\"d\",1,[\"dup\"]]\n[\"d\",2,[\"dup\"]]\n", `dictionary duplicates "dup"`)
}
func TestCompactSnapshotDecoderRejectsNoncontiguousDictionaryBase(t *testing.T) {
	requireCompactDecodeError(t, compactHeaderLine()+"[\"d\",2,[\"value\"]]\n", "dictionary base 2 does not equal 1")
}
func TestCompactSnapshotDecoderRejectsDuplicateHeader(t *testing.T) {
	requireCompactDecodeError(t, compactHeaderLine()+"[\"h\",1,{}]\n", "header must be first")
}
func TestCompactSnapshotDecoderRejectsRecordAfterSummary(t *testing.T) {
	requireCompactDecodeError(t, compactHeaderLine()+"[\"m\",{}]\n[\"f\",0,0,0,0,0]\n", "record after summary")
}
func TestCompactSnapshotDecoderRejectsMissingSummary(t *testing.T) {
	requireCompactDecodeError(t, compactHeaderLine(), "missing summary")
}

// Every line but the summary is one record and keeps a fixed cap, so an
// unterminated or garbage line is refused on its length before it is accumulated.
func TestCompactSnapshotDecoderRejectsOversizedNonSummaryLine(t *testing.T) {
	requireCompactDecodeError(t, strings.Repeat("x", compactSnapshotRecordLineBytes+1), "compact snapshot line exceeds")
}

// compactHeaderLine is the minimal valid header line: envelope version plus a
// schema version this build can place against ADR 0001's compatibility
// boundary. Cases below vary the lines AFTER it, so the header must not be the
// thing that fails.
func compactHeaderLine() string {
	return "[\"h\",1,{\"schema_version\":\"" + SchemaVersion + "\"}]\n"
}

func requireCompactDecodeError(t *testing.T, input, want string) {
	t.Helper()
	_, err := DecodeCompactSnapshot(strings.NewReader(input), func(any) error { return nil })
	if err == nil {
		t.Fatalf("expected %q error", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("decode error = %q, want category containing %q", err, want)
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

// The encoder writes the whole SnapshotSummary as ONE line, and that summary
// carries a record per partial failure over a corpus of up to
// defaultMaxSourceFiles files. A decoder that caps a line at a constant chosen
// ahead of that cannot read every artifact this build can write: a repository
// with enough parse/size failures encoded cleanly and then failed
// snapshot-query with "bufio.Scanner: token too long".
func TestCompactSnapshotDecodesSummaryLargerThanLegacyLineCap(t *testing.T) {
	var buffer bytes.Buffer
	encoder := NewCompactSnapshotEncoder(&buffer)
	if err := encoder.Encode(SnapshotHeader{SchemaVersion: SchemaVersion}); err != nil {
		t.Fatal(err)
	}
	summary := SnapshotSummary{RecordType: "summary", Languages: []string{}, Warnings: []ProviderWarning{}}
	for index := 0; index < 90_000; index++ {
		summary.PartialFailures = append(summary.PartialFailures, PartialFailure{
			Code:                 "E_FILE_TOO_LARGE",
			Severity:             "warning",
			FilePath:             fmt.Sprintf("packages/vendor/bundle/chunk-%06d/dist/index.min.js", index),
			EffectOnCompleteness: "file skipped because it exceeds the parse byte limit",
			Detail:               "file exceeds the configured max parse bytes",
		})
	}
	if err := encoder.Encode(summary); err != nil {
		t.Fatal(err)
	}
	if buffer.Len() <= 16*1024*1024 {
		t.Fatalf("encoded %d bytes, want more than the 16 MiB the decoder used to cap a line at", buffer.Len())
	}

	decoded := 0
	var readBack SnapshotSummary
	if _, err := DecodeCompactSnapshot(bytes.NewReader(buffer.Bytes()), func(record any) error {
		decoded++
		if typed, ok := record.(SnapshotSummary); ok {
			readBack = typed
		}
		return nil
	}); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded != 2 {
		t.Fatalf("decoded %d records, want 2", decoded)
	}
	if len(readBack.PartialFailures) != len(summary.PartialFailures) {
		t.Fatalf("partial failures = %d, want %d", len(readBack.PartialFailures), len(summary.PartialFailures))
	}
	if readBack.PartialFailures[89_999].FilePath != summary.PartialFailures[89_999].FilePath {
		t.Fatalf("last partial failure path = %q", readBack.PartialFailures[89_999].FilePath)
	}
}

// A record split across many reads of the underlying buffer must come back
// byte-identical, and the surrounding line framing must be unchanged.
func TestCompactSnapshotLineReaderFraming(t *testing.T) {
	long := strings.Repeat("x", 300*1024)
	input := "alpha\r\n" + long + "\nomega"
	reader := bufio.NewReaderSize(strings.NewReader(input), 64)
	var lines []string
	for {
		line, err := readCompactSnapshotLine(reader, compactSnapshotRecordLineBytes)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		lines = append(lines, string(line))
	}
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(lines))
	}
	if lines[0] != "alpha" {
		t.Fatalf("line 1 = %q, want %q", lines[0], "alpha")
	}
	if lines[1] != long {
		t.Fatalf("line 2 length = %d, want %d", len(lines[1]), len(long))
	}
	if lines[2] != "omega" {
		t.Fatalf("line 3 = %q, want %q", lines[2], "omega")
	}
}

// The bound must sit above everything the encoder can write and still refuse an
// arbitrarily long line before it is accumulated.
func TestCompactSnapshotLineReaderRefusesLineOverBound(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 4096)), 64)
	if _, err := readCompactSnapshotLine(reader, 1024); err == nil {
		t.Fatal("expected an over-bound line to be refused")
	} else if !strings.Contains(err.Error(), "exceeds 1024 bytes") {
		t.Fatalf("err = %v", err)
	}
	// A line exactly at the bound is a record, not an overflow.
	reader = bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 1024)), 64)
	line, err := readCompactSnapshotLine(reader, 1024)
	if err != nil {
		t.Fatalf("line at the bound: %v", err)
	}
	if len(line) != 1024 {
		t.Fatalf("line length = %d, want 1024", len(line))
	}
}

// The summary gets a larger allowance than the per-record lines, because its
// size is a function of how many files the operator let the encoder admit.
func TestCompactSnapshotSummaryLineGetsTheLargerAllowance(t *testing.T) {
	summary := `["m",{"record_type":"summary","partial_failures":[`
	entry := `{"code":"E_FILE_TOO_LARGE","severity":"warning","file_path":"src/x.go","effect_on_semantic_completeness":"skipped"}`
	entries := make([]string, 0, 200_000)
	for len(summary)+len(entries)*(len(entry)+1) <= compactSnapshotRecordLineBytes {
		entries = append(entries, entry)
	}
	summary += strings.Join(entries, ",") + `]}]`
	if len(summary) <= compactSnapshotRecordLineBytes {
		t.Fatalf("fixture is %d bytes, want more than the %d-byte record cap", len(summary), compactSnapshotRecordLineBytes)
	}

	decoded := 0
	if _, err := DecodeCompactSnapshot(strings.NewReader(compactHeaderLine()+summary+"\n"), func(any) error {
		decoded++
		return nil
	}); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded != 2 {
		t.Fatalf("decoded %d records, want 2", decoded)
	}
}

// The summary allowance is spent at most once, so the leading bytes of a
// malformed artifact cannot request the large buffer line after line.
func TestCompactSnapshotSummaryAllowanceIsSpentOnce(t *testing.T) {
	if compactSnapshotSummaryLineBytes <= compactSnapshotRecordLineBytes {
		t.Fatalf("summary allowance %d is not larger than the record cap %d", compactSnapshotSummaryLineBytes, compactSnapshotRecordLineBytes)
	}
	second := `["m",` + strings.Repeat("x", compactSnapshotRecordLineBytes)
	input := compactHeaderLine() + `["m",{"record_type":"summary"}]` + "\n" + second + "\n"
	_, err := DecodeCompactSnapshot(strings.NewReader(input), func(any) error { return nil })
	if err == nil {
		t.Fatal("expected the second summary-prefixed line to be refused")
	}
	if !strings.Contains(err.Error(), "compact snapshot line exceeds") {
		t.Fatalf("err = %v, want a length refusal", err)
	}
}

// The allowance is granted only where a summary is legal, so a malformed file
// that merely STARTS with the summary prefix is refused on length.
func TestCompactSnapshotSummaryAllowanceRequiresAHeader(t *testing.T) {
	requireCompactDecodeError(t, `["m",`+strings.Repeat("x", compactSnapshotRecordLineBytes), "compact snapshot line exceeds")
}

// Nor is the allowance available once a summary has been read, however that
// summary was spelled: a line after it is never a legal summary.
func TestCompactSnapshotSummaryAllowanceStopsAfterTheSummary(t *testing.T) {
	// Spelled with a space so the prefix peek misses it and the allowance is
	// still unspent when the oversized line arrives.
	spelled := `["m" ,{"record_type":"summary"}]`
	oversized := `["m",` + strings.Repeat("x", compactSnapshotRecordLineBytes)
	requireCompactDecodeError(t, compactHeaderLine()+spelled+"\n"+oversized+"\n", "compact snapshot line exceeds")
}
