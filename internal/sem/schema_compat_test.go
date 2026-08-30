package sem

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// compactWithSchemaVersion re-encodes the standard compact fixture with the
// header's schema_version replaced. It edits the SERIALIZED line rather than the
// Go struct because the case that matters most — the field being absent — cannot
// be expressed by a struct whose field is a plain string.
func compactWithSchemaVersion(t *testing.T, declared string, present bool) []byte {
	t.Helper()
	encoded, _ := encodeCompactFixture(t, compactFixtureRecords())
	lines := bytes.Split(bytes.TrimRight(encoded, "\n"), []byte("\n"))
	var header []json.RawMessage
	if err := json.Unmarshal(lines[0], &header); err != nil {
		t.Fatalf("decode fixture header line: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(header[2], &object); err != nil {
		t.Fatalf("decode fixture header: %v", err)
	}
	if present {
		object["schema_version"] = declared
	} else {
		delete(object, "schema_version")
	}
	rewritten, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("encode fixture header: %v", err)
	}
	header[2] = rewritten
	line, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("encode fixture header line: %v", err)
	}
	lines[0] = line
	return append(bytes.Join(lines, []byte("\n")), '\n')
}

// ADR 0001 makes the MAJOR the compatibility boundary and requires a consumer to
// refuse an unknown one. entire-graph is such a consumer whenever it reads a
// compact snapshot back off disk (snapshot-query, the bench preflight), and the
// compact envelope version does not cover this: it versions the array encoding,
// not the record schema the header declares, so the two move independently.
//
// Before this gate every row below loaded silently and answered the query, so a
// snapshot written by a schema-2.x producer was decoded into 1.x structs with
// each renamed field arriving as a zero value.
func TestLoadCompactSnapshotEnforcesSchemaMajor(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		declared  string
		present   bool
		wantError string
		wantWarn  bool
	}{
		{name: "this build's schema", declared: SchemaVersion, present: true},
		{name: "older minor of this major", declared: "1.0", present: true},
		{name: "newer minor of this major", declared: "1.99", present: true, wantWarn: true},
		{name: "newer major", declared: "2.0", present: true, wantError: `unsupported schema version "2.0"`},
		{name: "older major", declared: "0.9", present: true, wantError: `unsupported schema version "0.9"`},
		{name: "absent", present: false, wantError: "schema version is missing"},
		{name: "empty", declared: "", present: true, wantError: "schema version is missing"},
		{name: "not major.minor", declared: "abc", present: true, wantError: `is not major.minor`},
		{name: "three components", declared: "1.1.0", present: true, wantError: `is not major.minor`},
		{name: "unreadable minor", declared: "1.x", present: true, wantError: "unreadable minor"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			encoded := compactWithSchemaVersion(t, testCase.declared, testCase.present)
			index, err := LoadCompactSnapshot(bytes.NewReader(encoded))
			if testCase.wantError != "" {
				if err == nil {
					t.Fatalf("loaded a snapshot declaring %q; want refusal", testCase.declared)
				}
				if !strings.Contains(err.Error(), testCase.wantError) {
					t.Fatalf("error = %q, want it to contain %q", err, testCase.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("readable schema %q was refused: %v", testCase.declared, err)
			}
			// The contract's other half: a newer minor of a readable major LOADS,
			// and says so, because additive facts from that minor were skipped.
			warned := len(index.SchemaWarnings) == 1 &&
				index.SchemaWarnings[0].Code == "W_NEWER_SCHEMA_MINOR"
			if warned != testCase.wantWarn {
				t.Fatalf("schema warnings = %+v, want newer-minor warning = %v",
					index.SchemaWarnings, testCase.wantWarn)
			}
		})
	}
}

// The decoder gate is separate from the loader's, so pin it directly: a consumer
// streaming records through DecodeCompactSnapshot must get the same refusal.
func TestDecodeCompactSnapshotEnforcesSchemaMajor(t *testing.T) {
	t.Parallel()
	encoded := compactWithSchemaVersion(t, "2.0", true)
	_, err := DecodeCompactSnapshot(bytes.NewReader(encoded), func(any) error { return nil })
	if err == nil {
		t.Fatal("decoder streamed records from a snapshot declaring schema 2.0")
	}
	if !strings.Contains(err.Error(), `unsupported schema version "2.0"`) {
		t.Fatalf("error = %q", err)
	}
}

func TestCheckReadableSchemaVersionClassifiesAgainstThisBuild(t *testing.T) {
	t.Parallel()
	// Guard the constant itself: the whole gate is built on it parsing.
	major, minor, err := schemaMajorMinor(SchemaVersion)
	if err != nil {
		t.Fatalf("package SchemaVersion %q is not major.minor: %v", SchemaVersion, err)
	}
	if newerMinor, err := CheckReadableSchemaVersion(SchemaVersion); err != nil || newerMinor {
		t.Fatalf("this build's own schema must read clean: newerMinor=%v err=%v", newerMinor, err)
	}
	newer := formatSchemaVersion(major, minor+1)
	if newerMinor, err := CheckReadableSchemaVersion(newer); err != nil || !newerMinor {
		t.Fatalf("schema %s must load with a newer-minor signal: newerMinor=%v err=%v", newer, newerMinor, err)
	}
	other := formatSchemaVersion(major+1, 0)
	if _, err := CheckReadableSchemaVersion(other); err == nil {
		t.Fatalf("schema %s must be refused as another major", other)
	}
}

func formatSchemaVersion(major, minor int) string {
	return strconv.Itoa(major) + "." + strconv.Itoa(minor)
}

// TestDecodeCompactSnapshotReturnsTheNewerMinorWarning pins the half of ADR 0001
// clause 3 the streaming decoder used to drop.
//
// The decoder computed the newer-minor signal in order to decide whether the
// artifact was readable at all, and then discarded it — so every caller that
// owed the reader a warning had to re-derive it from the header. LoadCompactSnapshot
// did; a direct caller had nothing to remind it. Returning the warnings makes
// the obligation part of the signature.
func TestDecodeCompactSnapshotReturnsTheNewerMinorWarning(t *testing.T) {
	t.Parallel()
	major, minor, err := schemaMajorMinor(SchemaVersion)
	if err != nil {
		t.Fatalf("this build's SchemaVersion must parse: %v", err)
	}
	newerMinor := strconv.Itoa(major) + "." + strconv.Itoa(minor+1)

	encoded := compactWithSchemaVersion(t, newerMinor, true)
	warnings, err := DecodeCompactSnapshot(bytes.NewReader(encoded), func(any) error { return nil })
	if err != nil {
		t.Fatalf("a newer minor of a readable major must still decode: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("want exactly one tolerant-reader warning for a newer minor, got %#v", warnings)
	}
	if !strings.Contains(warnings[0].Detail, newerMinor) {
		t.Fatalf("the warning must name the version it saw: %#v", warnings[0])
	}

	// The current version is not newer than itself, so it must warn about nothing.
	same := compactWithSchemaVersion(t, SchemaVersion, true)
	warnings, err = DecodeCompactSnapshot(bytes.NewReader(same), func(any) error { return nil })
	if err != nil {
		t.Fatalf("this build's own version must decode: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("this build's own version must produce no warning, got %#v", warnings)
	}
}

// TestLoadCompactSnapshotSurfacesTheDecoderWarning keeps the two paths agreeing:
// the index's SchemaWarnings must be exactly what the decoder returned, now that
// LoadCompactSnapshot consumes them instead of recomputing the condition.
func TestLoadCompactSnapshotSurfacesTheDecoderWarning(t *testing.T) {
	t.Parallel()
	major, minor, err := schemaMajorMinor(SchemaVersion)
	if err != nil {
		t.Fatalf("this build's SchemaVersion must parse: %v", err)
	}
	newerMinor := strconv.Itoa(major) + "." + strconv.Itoa(minor+1)
	encoded := compactWithSchemaVersion(t, newerMinor, true)

	index, err := LoadCompactSnapshot(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(index.SchemaWarnings) != 1 || !strings.Contains(index.SchemaWarnings[0].Detail, newerMinor) {
		t.Fatalf("the loader must surface the decoder's warning: %#v", index.SchemaWarnings)
	}
}
