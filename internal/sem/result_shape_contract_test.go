package sem

import (
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"fmt"
	randv1 "math/rand"
	randv2 "math/rand/v2"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode"
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
// as canonical field descriptors in declaration order.
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
		`name="checkpoint" type="string" tagged=true anonymous=false options="omitempty"`,
		`name="base" type="string" tagged=true anonymous=false options=""`,
		`name="head" type="string" tagged=true anonymous=false options=""`,
		`name="files" type="[]github.com/entireio/entire-graph/internal/sem.FileChange" tagged=true anonymous=false options=""`,
		`name="warnings" type="[]github.com/entireio/entire-graph/internal/sem.ProviderWarning" tagged=true anonymous=false options="omitempty"`,
		`name="schema_version" type="string" tagged=true anonymous=false options=""`,
		`name="producer_version" type="string" tagged=true anonymous=false options="omitempty"`,
	},
	"FileChange": {
		`name="path" type="string" tagged=true anonymous=false options=""`,
		`name="old_path" type="string" tagged=true anonymous=false options="omitempty"`,
		`name="status" type="string" tagged=true anonymous=false options=""`,
		`name="language" type="string" tagged=true anonymous=false options="omitempty"`,
		`name="changes" type="[]github.com/entireio/entire-graph/internal/sem.EntityChange" tagged=true anonymous=false options=""`,
	},
	"EntityChange": {
		`name="type" type="string" tagged=true anonymous=false options=""`,
		`name="kind" type="string" tagged=true anonymous=false options=""`,
		`name="name" type="string" tagged=true anonymous=false options=""`,
		`name="old_name" type="string" tagged=true anonymous=false options="omitempty"`,
		`name="new_name" type="string" tagged=true anonymous=false options="omitempty"`,
		`name="old_signature" type="string" tagged=true anonymous=false options="omitempty"`,
		`name="new_signature" type="string" tagged=true anonymous=false options="omitempty"`,
		`name="old_path" type="string" tagged=true anonymous=false options="omitempty"`,
		`name="new_path" type="string" tagged=true anonymous=false options="omitempty"`,
		`name="before_start_line" type="int" tagged=true anonymous=false options="omitempty"`,
		`name="after_start_line" type="int" tagged=true anonymous=false options="omitempty"`,
		`name="dependents_count" type="int" tagged=true anonymous=false options=""`,
		`name="similarity" type="float64" tagged=true anonymous=false options="omitempty"`,
		`name="reconciliation" type="string" tagged=true anonymous=false options="omitempty"`,
	},
	// Result.Warnings' element type. It is persisted with the rest of the
	// payload and was frozen by nothing.
	"ProviderWarning": {
		`name="code" type="string" tagged=true anonymous=false options=""`,
		`name="severity" type="string" tagged=true anonymous=false options=""`,
		`name="file_path" type="string" tagged=true anonymous=false options="omitempty"`,
		`name="effect_on_semantic_completeness" type="string" tagged=true anonymous=false options=""`,
		`name="detail" type="string" tagged=true anonymous=false options="omitempty"`,
	},
}

func TestResultWireShapeIsFrozen(t *testing.T) {
	t.Parallel()
	liveTypes := persistedResultTypes()
	for _, mismatch := range wireShapeMismatches(liveTypes, resultWireShape) {
		if !mismatch.hasFrozenShape {
			t.Errorf("%s is reachable from the persisted Result but is frozen by nothing.\n"+
				"Add its shape to resultWireShape (and decide, per ADR 0001, what its arrival means for SchemaVersion %s).",
				mismatch.name, SchemaVersion)
			continue
		}
		t.Errorf(
			"%s wire shape changed.\n got: %v\nwant: %v\n\n"+
				"This type is persisted into checkpoint metadata and read back, so its shape is "+
				"the schema %s declares. Per ADR 0001: an added optional field is additive and "+
				"needs a SchemaVersion MINOR bump; a removed, renamed, retyped, or newly-required "+
				"field is breaking and needs a major bump plus a migration note. Decide which, "+
				"then update resultWireShape.",
			mismatch.name, mismatch.got, mismatch.want, SchemaVersion,
		)
	}

	// Check the reverse direction separately: a frozen entry that is no longer
	// reachable means a persisted type was removed, which is itself breaking.
	for name := range resultWireShape {
		if _, ok := liveTypes[name]; !ok {
			t.Errorf("%s is frozen but is no longer reachable from Result; removing a type from the payload is a BREAKING change under ADR 0001", name)
		}
	}
}

// jsonWireShape lists a struct's fields as "name type[ options/markers]" in
// declaration order, or a named non-struct's canonical underlying descriptor.
// Fields tagged exactly `json:"-"` are skipped because they never reach the
// wire. Unexported ordinary fields are likewise invisible; unexported anonymous
// struct fields remain visible for promotion.
//
// omitempty is part of the shape, not a formatting detail. Deleting it from a
// field changes what a consumer receives: the field stops being ABSENT for a
// zero value and starts arriving as "" / 0 / null. ADR 0001 treats
// optional -> required as breaking, and that is exactly the transition it
// describes. An earlier version of this helper cut the tag at the first comma
// and threw the option half away, so `old_path,omitempty` -> `old_path` was a
// wire change this test called identical.
func jsonWireShape(structType reflect.Type) []string {
	if structType.Kind() != reflect.Struct {
		return []string{fmt.Sprintf("underlying=%q", underlyingTypeDescriptor(structType))}
	}
	var shape []string
	for i := range structType.NumField() {
		field := structType.Field(i)
		if !jsonIncludesField(field) {
			continue
		}
		rawTag := field.Tag.Get("json")
		name, options, _ := strings.Cut(rawTag, ",")
		tagged := validJSONTagName(name)
		if !tagged {
			name = ""
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
		// Quote every free-form value and label every property so a legal tag
		// name or unknown option can never impersonate metadata.
		entry := fmt.Sprintf(
			"name=%q type=%q tagged=%t anonymous=%t options=%q",
			name, canonicalTypeReference(field.Type), tagged, field.Anonymous, jsonTagOptions(options),
		)
		shape = append(shape, entry)
	}
	return shape
}

// validJSONTagName mirrors encoding/json's tag-name validation. In particular,
// an invalid explicit name is treated as absent and does not win tagged-field
// dominance when promoted fields collide at equal depth.
func validJSONTagName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", r):
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			return false
		}
	}
	return true
}

// underlyingTypeDescriptor freezes the representation beneath a named
// non-struct type. reflect.Type.String reports only the defined type's name, so
// it cannot distinguish `type IDs []string` from `type IDs []int`.
func underlyingTypeDescriptor(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + canonicalTypeReference(t.Elem())
	case reflect.Slice:
		return "[]" + canonicalTypeReference(t.Elem())
	case reflect.Array:
		return fmt.Sprintf("[%d]%s", t.Len(), canonicalTypeReference(t.Elem()))
	case reflect.Map:
		return "map[" + canonicalTypeReference(t.Key()) + "]" + canonicalTypeReference(t.Elem())
	default:
		return t.Kind().String()
	}
}

func canonicalTypeReference(t reflect.Type) string {
	if t.Name() != "" && t.PkgPath() != "" {
		return t.PkgPath() + "." + t.Name()
	}
	if t.Name() != "" {
		return t.Name()
	}
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + canonicalTypeReference(t.Elem())
	case reflect.Slice:
		return "[]" + canonicalTypeReference(t.Elem())
	case reflect.Array:
		return fmt.Sprintf("[%d]%s", t.Len(), canonicalTypeReference(t.Elem()))
	case reflect.Map:
		return "map[" + canonicalTypeReference(t.Key()) + "]" + canonicalTypeReference(t.Elem())
	case reflect.Struct:
		fields := make([]string, 0, t.NumField())
		for i := range t.NumField() {
			field := t.Field(i)
			fields = append(fields, fmt.Sprintf(
				"name=%q pkg=%q type=%q anonymous=%t tag=%q",
				field.Name, field.PkgPath, canonicalTypeReference(field.Type), field.Anonymous, string(field.Tag),
			))
		}
		return "struct{" + strings.Join(fields, ";") + "}"
	default:
		return t.String()
	}
}

// jsonIncludesField mirrors encoding/json's first-stage field eligibility.
// Only the exact tag `json:"-"` suppresses a field: `json:"-,"` is a real field
// named "-". Unexported ordinary fields stay invisible, while unexported
// anonymous struct and *struct fields remain eligible because encoding/json
// promotes their exported children.
func jsonIncludesField(field reflect.StructField) bool {
	if field.Tag.Get("json") == "-" {
		return false
	}
	if field.IsExported() {
		return true
	}
	return isAnonymousStructField(field)
}

func isAnonymousStructField(field reflect.StructField) bool {
	if !field.Anonymous {
		return false
	}
	t := field.Type
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind() == reflect.Struct
}

type AnonymousShapeChildProbe struct {
	Value string `json:"value"`
}

type anonymousShapeFieldProbe struct {
	AnonymousShapeChildProbe
}

type namedShapeFieldProbe struct {
	AnonymousShapeChildProbe AnonymousShapeChildProbe
}

type explicitlyNamedAnonymousShapeFieldProbe struct {
	AnonymousShapeChildProbe `json:"AnonymousShapeChildProbe"`
}

type untaggedNamedShapeFieldProbe struct {
	Value string
}

type taggedNamedShapeFieldProbe struct {
	Value string `json:"Value"`
}

type invalidlyTaggedNamedShapeFieldProbe struct {
	Value string `json:"bad\\name"`
}

type metadataCollisionShapeFieldProbe struct {
	Value string `json:"tagged=true,go:anonymous"`
}

func TestJSONWireShapeFreezesAnonymousAndExplicitNameStatus(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		value any
		want  []string
	}{
		{
			name:  "anonymous",
			value: anonymousShapeFieldProbe{},
			want:  []string{`name="AnonymousShapeChildProbe" type="github.com/entireio/entire-graph/internal/sem.AnonymousShapeChildProbe" tagged=false anonymous=true options=""`},
		},
		{
			name:  "named",
			value: namedShapeFieldProbe{},
			want:  []string{`name="AnonymousShapeChildProbe" type="github.com/entireio/entire-graph/internal/sem.AnonymousShapeChildProbe" tagged=false anonymous=false options=""`},
		},
		{
			name:  "anonymous with explicit default name",
			value: explicitlyNamedAnonymousShapeFieldProbe{},
			want:  []string{`name="AnonymousShapeChildProbe" type="github.com/entireio/entire-graph/internal/sem.AnonymousShapeChildProbe" tagged=true anonymous=true options=""`},
		},
		{
			name:  "ordinary field without tag",
			value: untaggedNamedShapeFieldProbe{},
			want:  []string{`name="Value" type="string" tagged=false anonymous=false options=""`},
		},
		{
			name:  "ordinary field with explicit default name",
			value: taggedNamedShapeFieldProbe{},
			want:  []string{`name="Value" type="string" tagged=true anonymous=false options=""`},
		},
		{
			name:  "invalid explicit name is untagged",
			value: invalidlyTaggedNamedShapeFieldProbe{},
			want:  []string{`name="Value" type="string" tagged=false anonymous=false options=""`},
		},
		{
			name:  "tag text cannot impersonate metadata",
			value: metadataCollisionShapeFieldProbe{},
			want:  []string{`name="tagged=true" type="string" tagged=true anonymous=false options="go:anonymous"`},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := jsonWireShape(reflect.TypeOf(test.value)); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("jsonWireShape() = %v, want %v", got, test.want)
			}
		})
	}
}

type randV1TypeReferenceProbe struct {
	Value map[string][1][]*randv1.Rand
}

type randV2TypeReferenceProbe struct {
	Value map[string][1][]*randv2.Rand
}

func TestCanonicalTypeReferenceUsesFullImportPathsThroughWrappers(t *testing.T) {
	t.Parallel()
	v1Type := reflect.TypeOf(randV1TypeReferenceProbe{})
	v2Type := reflect.TypeOf(randV2TypeReferenceProbe{})
	if v1Type.Field(0).Type.String() != v2Type.Field(0).Type.String() {
		t.Fatalf("fixture no longer demonstrates reflect.Type.String collision: %q != %q", v1Type.Field(0).Type, v2Type.Field(0).Type)
	}
	v1Want := []string{`name="Value" type="map[string][1][]*math/rand.Rand" tagged=false anonymous=false options=""`}
	v2Want := []string{`name="Value" type="map[string][1][]*math/rand/v2.Rand" tagged=false anonymous=false options=""`}
	if got := jsonWireShape(v1Type); !reflect.DeepEqual(got, v1Want) {
		t.Fatalf("v1 jsonWireShape() = %v, want %v", got, v1Want)
	}
	if got := jsonWireShape(v2Type); !reflect.DeepEqual(got, v2Want) {
		t.Fatalf("v2 jsonWireShape() = %v, want %v", got, v2Want)
	}
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

// walkPersistedJSONTypes visits every type that encoding/json can reach from
// root through exported, non-ignored fields. Tracking every type (rather than
// only structs) matters because any named Go type can implement json.Marshaler
// or encoding.TextMarshaler and replace the bytes its reflected kind would
// otherwise produce.
func walkPersistedJSONTypes(root reflect.Type, visit func(reflect.Type)) {
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		visit(t)
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			if t.Kind() == reflect.Map {
				walk(t.Key())
			}
			walk(t.Elem())
		case reflect.Struct:
			for i := range t.NumField() {
				field := t.Field(i)
				// A field encoding/json ignores cannot expose its type on the
				// wire. Use the same predicate as jsonWireShape so reflection
				// and reachability cannot silently disagree.
				if !jsonIncludesField(field) {
					continue
				}
				walk(field.Type)
			}
		}
	}
	walk(root)
}

// persistedResultTypes are every user-defined named type reachable from Result.
// The contract is over the WHOLE persisted payload, and a type reachable from it
// but absent from resultWireShape is frozen by nothing — ProviderWarning was
// exactly that: it is Result.Warnings' element type, it is written into checkpoint
// metadata with everything else, and its fields could be renamed in silence.
var resultPackagePath = reflect.TypeOf(Result{}).PkgPath()

// persistedTypeIdentity keeps the existing local frozen keys stable while
// qualifying external named types by their full package path. That avoids the
// collisions possible with reflect.Type.String's package-name-only spelling.
func persistedTypeIdentity(t reflect.Type) string {
	if t.PkgPath() == resultPackagePath {
		return t.Name()
	}
	return t.PkgPath() + "." + t.Name()
}

func persistedResultTypes() map[string]reflect.Type {
	return persistedNamedTypes(reflect.TypeOf(Result{}))
}

func persistedNamedTypes(root reflect.Type) map[string]reflect.Type {
	found := map[string]reflect.Type{}
	walkPersistedJSONTypes(root, func(t reflect.Type) {
		// PkgPath distinguishes user-defined named types from predeclared
		// scalars such as string and int, which need no separate frozen entry.
		if t.Name() != "" && t.PkgPath() != "" {
			found[persistedTypeIdentity(t)] = t
		}
	})
	return found
}

type wireShapeMismatch struct {
	name           string
	got            []string
	want           []string
	hasFrozenShape bool
}

func wireShapeMismatches(liveTypes map[string]reflect.Type, frozen map[string][]string) []wireShapeMismatch {
	names := make([]string, 0, len(liveTypes))
	for name := range liveTypes {
		names = append(names, name)
	}
	sort.Strings(names)

	var mismatches []wireShapeMismatch
	for _, name := range names {
		got := jsonWireShape(liveTypes[name])
		want, ok := frozen[name]
		if !ok || !reflect.DeepEqual(got, want) {
			mismatches = append(mismatches, wireShapeMismatch{
				name:           name,
				got:            got,
				want:           want,
				hasFrozenShape: ok,
			})
		}
	}
	return mismatches
}

type newlyReachableShapeProbe struct {
	Value string `json:"value"`
}

type shapeReachabilityRootProbe struct {
	Nested newlyReachableShapeProbe `json:"nested"`
}

func TestWireShapeMismatchesChecksNewlyReachableTypes(t *testing.T) {
	t.Parallel()
	rootType := reflect.TypeOf(shapeReachabilityRootProbe{})
	nestedType := reflect.TypeOf(newlyReachableShapeProbe{})
	liveTypes := persistedNamedTypes(rootType)
	frozen := map[string][]string{
		rootType.Name():   jsonWireShape(rootType),
		nestedType.Name(): {`name="value" type="int" tagged=true anonymous=false options=""`}, // Present but deliberately wrong.
	}
	got := wireShapeMismatches(liveTypes, frozen)
	if len(got) != 1 || got[0].name != nestedType.Name() || !got[0].hasFrozenShape {
		t.Fatalf("wireShapeMismatches() = %+v, want one mismatch for %s", got, nestedType.Name())
	}
	if want := []string{`name="value" type="string" tagged=true anonymous=false options=""`}; !reflect.DeepEqual(got[0].got, want) {
		t.Fatalf("live shape = %v, want %v", got[0].got, want)
	}
}

type namedScalarShapeProbe string
type namedSliceShapeProbe []namedScalarShapeProbe
type namedMapShapeProbe map[namedScalarShapeProbe]namedSliceShapeProbe
type namedArrayShapeProbe [2]namedScalarShapeProbe
type namedPointerShapeProbe *namedScalarShapeProbe

type namedNonStructRootProbe struct {
	Scalar  namedScalarShapeProbe
	Slice   namedSliceShapeProbe
	Map     namedMapShapeProbe
	Array   namedArrayShapeProbe
	Pointer namedPointerShapeProbe
}

func TestNamedNonStructWireShapesAreFrozenByUnderlyingType(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		value any
		want  string
	}{
		{value: namedScalarShapeProbe(""), want: `underlying="string"`},
		{value: namedSliceShapeProbe(nil), want: `underlying="[]github.com/entireio/entire-graph/internal/sem.namedScalarShapeProbe"`},
		{value: namedMapShapeProbe(nil), want: `underlying="map[github.com/entireio/entire-graph/internal/sem.namedScalarShapeProbe]github.com/entireio/entire-graph/internal/sem.namedSliceShapeProbe"`},
		{value: namedArrayShapeProbe{}, want: `underlying="[2]github.com/entireio/entire-graph/internal/sem.namedScalarShapeProbe"`},
		{value: namedPointerShapeProbe(nil), want: `underlying="*github.com/entireio/entire-graph/internal/sem.namedScalarShapeProbe"`},
	} {
		if got := jsonWireShape(reflect.TypeOf(test.value)); !reflect.DeepEqual(got, []string{test.want}) {
			t.Errorf("jsonWireShape(%T) = %v, want [%s]", test.value, got, test.want)
		}
	}

	liveTypes := persistedNamedTypes(reflect.TypeOf(namedNonStructRootProbe{}))
	frozen := make(map[string][]string, len(liveTypes))
	for name, typ := range liveTypes {
		frozen[name] = jsonWireShape(typ)
	}
	frozen["namedScalarShapeProbe"] = []string{`underlying="int"`}
	got := wireShapeMismatches(liveTypes, frozen)
	if len(got) != 1 || got[0].name != "namedScalarShapeProbe" || !got[0].hasFrozenShape {
		t.Fatalf("wireShapeMismatches() = %+v, want the named scalar's incorrect frozen underlying type", got)
	}
}

var wireMarshalerInterfaces = []struct {
	name string
	typ  reflect.Type
}{
	{name: "json.Marshaler", typ: reflect.TypeOf((*json.Marshaler)(nil)).Elem()},
	{name: "encoding.TextMarshaler", typ: reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()},
}

func valueOrPointerImplementation(t, interfaceType reflect.Type) (reflect.Type, bool) {
	if t.Implements(interfaceType) {
		return t, true
	}
	if t.Kind() != reflect.Pointer {
		pointer := reflect.PointerTo(t)
		if pointer.Implements(interfaceType) {
			return pointer, true
		}
	}
	return nil, false
}

// reachableWireMarshalerTypes returns named types whose value or pointer method
// set can replace encoding/json's ordinary reflected representation. This also
// covers encoding.TextMarshaler on map keys. A value receiver is reported once
// as T (even though *T inherits that method); a pointer-only receiver is
// reported as *T.
func reachableWireMarshalerTypes(root reflect.Type) []string {
	var found []string
	walkPersistedJSONTypes(root, func(t reflect.Type) {
		if t.Name() == "" {
			return
		}
		for _, marshaler := range wireMarshalerInterfaces {
			implementation, ok := valueOrPointerImplementation(t, marshaler.typ)
			if !ok {
				continue
			}
			found = append(found, fmt.Sprintf("%s (%s)", implementation, marshaler.name))
		}
	})
	sort.Strings(found)
	return found
}

type jsonIsZeroer interface {
	IsZero() bool
}

var jsonIsZeroerType = reflect.TypeOf((*jsonIsZeroer)(nil)).Elem()

func jsonTagHasOption(field reflect.StructField, target string) bool {
	_, options, _ := strings.Cut(field.Tag.Get("json"), ",")
	for _, option := range strings.Split(options, ",") {
		if option == target {
			return true
		}
	}
	return false
}

// reachableOmitZeroHooks finds fields whose exact `omitzero` option makes
// encoding/json consult an IsZero() bool method that the reflected field shape
// cannot otherwise see.
func reachableOmitZeroHooks(root reflect.Type) []string {
	var found []string
	walkPersistedJSONTypes(root, func(t reflect.Type) {
		if t.Kind() != reflect.Struct {
			return
		}
		owner := t.String()
		if t.Name() != "" && t.PkgPath() != "" {
			owner = persistedTypeIdentity(t)
		}
		for i := range t.NumField() {
			field := t.Field(i)
			if !jsonIncludesField(field) || !jsonTagHasOption(field, "omitzero") {
				continue
			}
			implementation, ok := valueOrPointerImplementation(field.Type, jsonIsZeroerType)
			if ok {
				found = append(found, fmt.Sprintf("%s.%s: %s", owner, field.Name, implementation))
			}
		}
	})
	sort.Strings(found)
	return found
}

// reachableInterfaceTypes rejects both named and unnamed interfaces. Their
// runtime concrete values are not statically reachable and therefore cannot be
// included in the frozen type/shape set.
func reachableInterfaceTypes(root reflect.Type) []string {
	var found []string
	walkPersistedJSONTypes(root, func(t reflect.Type) {
		if t.Kind() != reflect.Interface {
			return
		}
		name := t.String()
		if t.Name() != "" && t.PkgPath() != "" {
			name = persistedTypeIdentity(t)
		}
		found = append(found, name)
	})
	sort.Strings(found)
	return found
}

type namedInterfaceProbe interface {
	probe()
}

type interfaceReachabilityProbe struct {
	Named   namedInterfaceProbe
	Unnamed any
}

func TestReachableInterfaceTypesFindsNamedAndUnnamedInterfaces(t *testing.T) {
	t.Parallel()
	got := reachableInterfaceTypes(reflect.TypeOf(interfaceReachabilityProbe{}))
	want := []string{"interface {}", "namedInterfaceProbe"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reachable interfaces = %v, want %v", got, want)
	}
}

type valueJSONMarshalerProbe struct{}

func (valueJSONMarshalerProbe) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

type pointerJSONMarshalerProbe struct{}

func (*pointerJSONMarshalerProbe) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

type valueTextMarshalerProbe struct{}

func (valueTextMarshalerProbe) MarshalText() ([]byte, error) { return nil, nil }

type pointerTextMarshalerProbe struct{}

func (*pointerTextMarshalerProbe) MarshalText() ([]byte, error) { return nil, nil }

type wireMarshalerReachabilityProbe struct {
	JSONValue       valueJSONMarshalerProbe
	JSONPointer     []pointerJSONMarshalerProbe
	TextValueKeys   map[valueTextMarshalerProbe]string
	TextPointerKeys map[*pointerTextMarshalerProbe]string
}

func TestReachableWireMarshalerTypesFindsValuePointerAndMapKeyTypes(t *testing.T) {
	t.Parallel()
	got := reachableWireMarshalerTypes(reflect.TypeOf(wireMarshalerReachabilityProbe{}))
	want := []string{
		"*sem.pointerJSONMarshalerProbe (json.Marshaler)",
		"*sem.pointerTextMarshalerProbe (encoding.TextMarshaler)",
		"sem.valueJSONMarshalerProbe (json.Marshaler)",
		"sem.valueTextMarshalerProbe (encoding.TextMarshaler)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reachable wire marshalers = %v, want %v", got, want)
	}
}

type valueIsZeroProbe struct{}

func (valueIsZeroProbe) IsZero() bool { return true }

type pointerIsZeroProbe struct{}

func (*pointerIsZeroProbe) IsZero() bool { return true }

type nonExactIsZeroProbe struct{}

func (nonExactIsZeroProbe) IsZero() bool { return true }

type omitZeroHookProbe struct {
	Value         valueIsZeroProbe    `json:"value,omitzero"`
	Pointer       pointerIsZeroProbe  `json:"pointer,omitempty,omitzero"`
	WithoutOption nonExactIsZeroProbe `json:"without_option"`
	NearOption    nonExactIsZeroProbe `json:"near_option,notomitzero"`
}

func TestReachableOmitZeroHooksRequiresExactOptionAndFindsBothReceivers(t *testing.T) {
	t.Parallel()
	got := reachableOmitZeroHooks(reflect.TypeOf(omitZeroHookProbe{}))
	want := []string{
		"omitZeroHookProbe.Pointer: *sem.pointerIsZeroProbe",
		"omitZeroHookProbe.Value: sem.valueIsZeroProbe",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reachable omitzero hooks = %v, want %v", got, want)
	}
}

type jsonDashFieldProbe struct {
	Ignored  pointerJSONMarshalerProbe `json:"-"`
	Included valueTextMarshalerProbe   `json:"-,"`
}

func TestJSONFieldInclusionDistinguishesDashFromDashComma(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(jsonDashFieldProbe{})
	if jsonIncludesField(typ.Field(0)) {
		t.Fatal(`json:"-" field is included`)
	}
	if !jsonIncludesField(typ.Field(1)) {
		t.Fatal(`json:"-," field is ignored`)
	}
	if got, want := jsonWireShape(typ), []string{`name="-" type="github.com/entireio/entire-graph/internal/sem.valueTextMarshalerProbe" tagged=true anonymous=false options=""`}; !reflect.DeepEqual(got, want) {
		t.Fatalf("jsonWireShape() = %v, want %v", got, want)
	}
	if got, want := reachableWireMarshalerTypes(typ), []string{"sem.valueTextMarshalerProbe (encoding.TextMarshaler)"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reachable wire marshalers = %v, want %v", got, want)
	}
}

type unexportedAnonymousValueProbe struct {
	Promoted valueTextMarshalerProbe `json:"promoted"`
}

type unexportedAnonymousPointerProbe struct {
	Pointed pointerTextMarshalerProbe `json:"pointed"`
}

type unexportedAnonymousReachabilityProbe struct {
	unexportedAnonymousValueProbe
	*unexportedAnonymousPointerProbe
}

func TestUnexportedAnonymousStructFieldsRemainJSONReachable(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(unexportedAnonymousReachabilityProbe{})
	if !jsonIncludesField(typ.Field(0)) || !jsonIncludesField(typ.Field(1)) {
		t.Fatal("unexported anonymous struct or *struct field is ignored")
	}
	gotTypes := reachableWireMarshalerTypes(typ)
	wantTypes := []string{
		"*sem.pointerTextMarshalerProbe (encoding.TextMarshaler)",
		"sem.valueTextMarshalerProbe (encoding.TextMarshaler)",
	}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("reachable wire marshalers = %v, want %v", gotTypes, wantTypes)
	}
}

// MarshalJSON and MarshalText are second wire schemas that reflection cannot
// see; IsZero is likewise wire-significant on an `omitzero` field. Adding any of
// them can change persisted bytes while leaving resultWireShape and its pinned
// digest unchanged, so reject value- and pointer-receiver forms.
func TestPersistedResultTypesDoNotCustomizeJSONEncoding(t *testing.T) {
	t.Parallel()
	if got := reachableWireMarshalerTypes(reflect.TypeOf(Result{})); len(got) != 0 {
		t.Fatalf(
			"reachable persisted types implement a custom JSON marshaler: %v\n\n"+
				"MarshalJSON and MarshalText replace the reflected wire representation that resultWireShape freezes. "+
				"Remove the custom marshaler, or replace this guard with an explicit serialized contract "+
				"and apply ADR 0001's schema-version rules.",
			got,
		)
	}
	if got := reachableOmitZeroHooks(reflect.TypeOf(Result{})); len(got) != 0 {
		t.Fatalf(
			"reachable persisted omitzero fields use IsZero hooks: %v\n\n"+
				"IsZero changes whether an omitzero field reaches the wire without changing its reflected shape. "+
				"Remove the hook, or replace this guard with an explicit serialized contract and apply ADR 0001's schema-version rules.",
			got,
		)
	}
	if got := reachableInterfaceTypes(reflect.TypeOf(Result{})); len(got) != 0 {
		t.Fatalf(
			"reachable persisted interface types cannot be frozen: %v\n\n"+
				"Runtime concrete values are outside the static reachable type set. Replace the interface with a closed concrete shape, or define an explicit serialized contract and apply ADR 0001's schema-version rules.",
			got,
		)
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
		pinnedSchemaVersion = "1.2"
		pinnedShapeDigest   = "9c02ab3256e93b77"
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
