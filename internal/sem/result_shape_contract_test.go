package sem

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
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
		// EVERY option is recorded, not just omitempty. An earlier version
		// looked only for omitempty and silently dropped the rest, which left
		// the same hole one level down: `,string` makes a numeric or boolean
		// field marshal as a QUOTED string, so adding or removing it changes
		// what every consumer parses, and `,omitzero` changes when the field is
		// emitted. Both are wire changes the contract must see.
		entry := name + " " + field.Type.String()
		if recorded := jsonTagOptions(options); recorded != "" {
			entry += " " + recorded
		}
		shape = append(shape, entry)
	}
	return shape
}

// jsonTagOptions renders a json tag's option list in a stable order, so the
// frozen shape is a property of the tag rather than of the order someone wrote
// its options in.
func jsonTagOptions(options string) string {
	if options == "" {
		return ""
	}
	seen := map[string]bool{}
	var recorded []string
	for _, option := range strings.Split(options, ",") {
		if option == "" || seen[option] {
			continue
		}
		seen[option] = true
		recorded = append(recorded, option)
	}
	sort.Strings(recorded)
	return strings.Join(recorded, ",")
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
				field := t.Field(i)
				if !field.IsExported() {
					continue
				}
				// A field tagged `json:"-"` never reaches the wire, so a type
				// reachable ONLY through one is not part of the contract.
				// Walking into it would demand a shape entry for a type no
				// consumer can observe, and the freeze would then fail for a
				// change that cannot break anyone.
				if fieldName, fieldOptions, _ := strings.Cut(field.Tag.Get("json"), ","); fieldName == "-" && fieldOptions == "" {
					continue
				}
				walk(field.Type)
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

// resultWireShapeDigest is a stable content hash of the frozen shape above. It
// is what BINDS the shape to the version.
//
// Freezing the shape and stamping a version are two separate assertions, and on
// their own they do not add up to a contract: resultWireShape could be edited
// and SchemaVersion left at "1.1", and everything stayed green -- a reader would
// then receive 1.1 bytes whose shape is not the 1.1 shape. Pinning the digest
// NEXT TO the exact version string means the shape cannot move without someone
// editing the version line in the same test, which is the moment ADR 0001's
// question gets asked: additive (minor bump) or breaking (major bump plus a
// migration note)?
func resultWireShapeDigest() string {
	names := make([]string, 0, len(resultWireShape))
	for name := range resultWireShape {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		fmt.Fprintf(h, "%s\n", name)
		for _, field := range resultWireShape[name] {
			fmt.Fprintf(h, "\t%s\n", field)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// TestResultWireShapeIsBoundToTheSchemaVersion is the half that makes the
// version stamp mean something. See ADR 0001: the persisted Result is
// INTERCHANGE, so its shape is governed by the major and may only grow within it.
//
// Note that this is a deliberately DIFFERENT rule from the one the on-disk
// caches apply to the same constant. A cache entry is bytes this build wrote for
// its own later reuse: there is no compatibility promise to keep and no
// migration path, and the cheap always-correct answer to "was this written by a
// different schema" is to rebuild. So cache validity is EXACT equality, while
// interchange readability is per-major with a warning on a newer minor
// (CheckReadableSchemaVersion). One string, two questions, two answers, both
// intentional -- see the amendment in docs/adr/0001-ga-schema-contract.md.
func TestResultWireShapeIsBoundToTheSchemaVersion(t *testing.T) {
	t.Parallel()
	const (
		pinnedSchemaVersion = "1.1"
		pinnedShapeDigest   = "a6dd2da89fa5a369"
	)
	if SchemaVersion != pinnedSchemaVersion || resultWireShapeDigest() != pinnedShapeDigest {
		t.Fatalf(
			"the persisted Result wire shape and SchemaVersion have moved apart.\n"+
				"  SchemaVersion: have %q, pinned %q\n"+
				"  shape digest:  have %q, pinned %q\n\n"+
				"Per ADR 0001, decide which happened and update BOTH constants here:\n"+
				"  - an added OPTIONAL field is additive -> bump the MINOR of SchemaVersion\n"+
				"  - a removed, renamed, retyped, or newly-required field is BREAKING -> major bump (2.0) + migration note\n"+
				"  - a pure SchemaVersion bump with an unchanged shape is fine; just re-pin the version here.",
			SchemaVersion, pinnedSchemaVersion, resultWireShapeDigest(), pinnedShapeDigest)
	}
}
