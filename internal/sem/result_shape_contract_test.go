package sem

import (
	"reflect"
	"strings"
	"testing"
)

// Result is the diff/analyze/checkpoint payload. It is PERSISTED — a copy is
// written into Entire checkpoint metadata and read back later, which is why it
// carries schema_version at all — so ADR 0001 governs its shape: minors are
// additive only, a field may never be removed, renamed, or retyped, and any
// break requires a major bump.
//
// Nothing enforced that. The NDJSON snapshot records are shape-pinned by the
// checked-in goldens in testdata/fixtures (TestProviderGoldenSnapshots), but
// Result is not part of a snapshot and appears in no golden. Measured on
// origin/main: adding `AuditProbe string \`json:"audit_probe"\`` to Result and
// running the whole suite produced ZERO failures, while the field appeared in
// `entire graph diff --json` output — the shape moved, the wire changed, and
// SchemaVersion stayed 1.1. The only existing assertions on Result are
// self-referential (`result.SchemaVersion == SchemaVersion`), which cannot
// detect a shape change because the constant travels with it.
//
// This test is the missing half of the version stamp: the stamp says which
// schema the bytes are, and this says the bytes did not change without someone
// deciding what that means for the stamp.

// resultWireShape is the frozen JSON shape of the persisted diff/analyze payload,
// as `name type` per field in declaration order.
//
// CHANGING THIS LIST IS A SCHEMA CHANGE. Before you update it, apply ADR 0001
// (docs/adr/0001-ga-schema-contract.md):
//
//   - adding an OPTIONAL field is additive -> allowed within 1.x, bump the MINOR
//     of SchemaVersion so consumers can detect the new field
//   - removing, renaming, retyping a field, or making an optional field required
//     is BREAKING -> requires a major bump (2.0) and a migration note
var resultWireShape = map[string][]string{
	"Result": {
		"checkpoint string",
		"base string",
		"head string",
		"files []sem.FileChange",
		"warnings []sem.ProviderWarning",
		"schema_version string",
		"producer_version string",
	},
	"FileChange": {
		"path string",
		"old_path string",
		"status string",
		"language string",
		"changes []sem.EntityChange",
	},
	"EntityChange": {
		"type string",
		"kind string",
		"name string",
		"old_name string",
		"new_name string",
		"old_signature string",
		"new_signature string",
		"old_path string",
		"new_path string",
		"before_start_line int",
		"after_start_line int",
		"dependents_count int",
		"similarity float64",
		"reconciliation string",
	},
}

func TestResultWireShapeIsFrozen(t *testing.T) {
	t.Parallel()
	for name, value := range map[string]any{
		"Result":       Result{},
		"FileChange":   FileChange{},
		"EntityChange": EntityChange{},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := jsonWireShape(reflect.TypeOf(value))
			want := resultWireShape[name]
			if !reflect.DeepEqual(got, want) {
				t.Fatalf(
					"%s wire shape changed.\n got: %v\nwant: %v\n\n"+
						"This type is persisted into checkpoint metadata and read back, so its shape is "+
						"the schema %s declares. Per ADR 0001: an added optional field is additive and "+
						"needs a SchemaVersion MINOR bump; a removed, renamed, retyped, or newly-required "+
						"field is breaking and needs a major bump plus a migration note. Decide which, "+
						"then update resultWireShape.",
					name, got, want, SchemaVersion,
				)
			}
		})
	}
}

// jsonWireShape lists the exported fields a type serializes, as "name type", in
// declaration order. Fields tagged `json:"-"` are skipped because they never
// reach the wire; unexported fields are invisible to encoding/json and are
// likewise not part of the schema.
func jsonWireShape(structType reflect.Type) []string {
	var shape []string
	for i := range structType.NumField() {
		field := structType.Field(i)
		if !field.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		shape = append(shape, name+" "+field.Type.String())
	}
	return shape
}

// The stamp and the shape have to agree about which schema this build speaks.
// A Result carrying a version the package does not declare would defeat the
// point of stamping it.
func TestResultCarriesThisBuildsSchemaVersion(t *testing.T) {
	t.Parallel()
	if !strings.HasPrefix(SchemaVersion, "1.") {
		t.Fatalf("SchemaVersion = %q; resultWireShape is the frozen 1.x shape and a major bump must revisit it", SchemaVersion)
	}
}
