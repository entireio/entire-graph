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
		"checkpoint string omitempty",
		"base string",
		"head string",
		"files []sem.FileChange",
		"warnings []sem.ProviderWarning omitempty",
		"schema_version string",
		"producer_version string omitempty",
	},
	"FileChange": {
		"path string",
		"old_path string omitempty",
		"status string",
		"language string omitempty",
		"changes []sem.EntityChange",
	},
	"EntityChange": {
		"type string",
		"kind string",
		"name string",
		"old_name string omitempty",
		"new_name string omitempty",
		"old_signature string omitempty",
		"new_signature string omitempty",
		"old_path string omitempty",
		"new_path string omitempty",
		"before_start_line int omitempty",
		"after_start_line int omitempty",
		"dependents_count int",
		"similarity float64 omitempty",
		"reconciliation string omitempty",
	},
	// Result.Warnings' element type. It is persisted with the rest of the
	// payload and was frozen by nothing.
	"ProviderWarning": {
		"code string",
		"severity string",
		"file_path string omitempty",
		"effect_on_semantic_completeness string",
		"detail string omitempty",
	},
}

func TestResultWireShapeIsFrozen(t *testing.T) {
	t.Parallel()
	for name, value := range map[string]any{
		"Result":          Result{},
		"FileChange":      FileChange{},
		"EntityChange":    EntityChange{},
		"ProviderWarning": ProviderWarning{},
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

// jsonWireShape lists the exported fields a type serializes, as
// "name type[ omitempty]", in declaration order. Fields tagged `json:"-"` are
// skipped because they never reach the wire; unexported fields are invisible to
// encoding/json and are likewise not part of the schema.
//
// omitempty is part of the shape, not a formatting detail. Deleting it from a
// field changes what a consumer receives: the field stops being ABSENT for a
// zero value and starts arriving as "" / 0 / null. ADR 0001 treats
// optional -> required as breaking, and that is exactly the transition it
// describes. An earlier version of this helper cut the tag at the first comma
// and threw the option half away, so `old_path,omitempty` -> `old_path` was a
// wire change this test called identical.
func jsonWireShape(structType reflect.Type) []string {
	var shape []string
	for i := range structType.NumField() {
		field := structType.Field(i)
		if !field.IsExported() {
			continue
		}
		name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" && options == "" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		entry := name + " " + field.Type.String()
		for _, option := range strings.Split(options, ",") {
			if option == "omitempty" {
				entry += " omitempty"
			}
		}
		shape = append(shape, entry)
	}
	return shape
}

// persistedResultTypes are every named struct type reachable from Result. The
// contract is over the WHOLE persisted payload, and a type reachable from it but
// absent from resultWireShape is frozen by nothing — ProviderWarning was exactly
// that: it is Result.Warnings' element type, it is written into checkpoint
// metadata with everything else, and its fields could be renamed in silence.
func persistedResultTypes() map[string]reflect.Type {
	found := map[string]reflect.Type{}
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			if t.Kind() == reflect.Map {
				walk(t.Key())
			}
			walk(t.Elem())
		case reflect.Struct:
			name := t.Name()
			if name == "" || found[name] != nil {
				return
			}
			found[name] = t
			for i := range t.NumField() {
				if t.Field(i).IsExported() {
					walk(t.Field(i).Type)
				}
			}
		}
	}
	walk(reflect.TypeOf(Result{}))
	return found
}

// A new named struct type reaching the persisted payload must be frozen with the
// rest of it, not left to be renamed in silence because nobody remembered to add
// it to the list. This is what makes the freeze complete rather than a snapshot
// of whichever three types someone thought of.
func TestEveryPersistedTypeReachableFromResultIsFrozen(t *testing.T) {
	t.Parallel()
	for name := range persistedResultTypes() {
		if _, ok := resultWireShape[name]; !ok {
			t.Errorf("%s is reachable from the persisted Result but is frozen by nothing.\n"+
				"Add its shape to resultWireShape (and decide, per ADR 0001, what its arrival means for SchemaVersion %s).",
				name, SchemaVersion)
		}
	}
	for name := range resultWireShape {
		if _, ok := persistedResultTypes()[name]; !ok {
			t.Errorf("%s is frozen but is no longer reachable from Result; removing a type from the payload is a BREAKING change under ADR 0001", name)
		}
	}
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
