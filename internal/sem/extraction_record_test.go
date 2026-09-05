package sem

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Hand-authored metadata checklist: every Entity field must be assigned a
// preservation decision. A new private parser field fails this guard until its
// payload mapping and nonzero round-trip fixture are reviewed.
func TestExtractionEntityFieldChecklist(t *testing.T) {
	want := []string{"Kind", "Name", "Signature", "StartLine", "EndLine", "BodyHash", "Fingerprint", "Local", "bodyless", "cLinkage", "sourceStartByte", "sourceEndByte", "parameterNames", "parameterNamesKnown", "paramTypeText", "returnTypeText", "signatureTypesKnown"}
	typ := reflect.TypeFor[Entity]()
	var got []string
	for field := range typ.Fields() {
		got = append(got, field.Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("audit new Entity fields: got %v; checklist %v", got, want)
	}
}

func TestExtractionRecordRoundTrip(t *testing.T) {
	entity := Entity{Kind: "function", Name: "nested", Signature: "nested(value: T): U", StartLine: 2, EndLine: 5, BodyHash: "body", Fingerprint: "fingerprint", Local: true, bodyless: true, cLinkage: true, sourceStartByte: 12, sourceEndByte: 88, parameterNames: []string{"value"}, parameterNamesKnown: true, paramTypeText: "value: T", returnTypeText: "U", signatureTypesKnown: true}
	status := ParseStatus{ParseError: true, Partial: true, DepthExceeded: true, Code: "fixture", Detail: "bounded fixture diagnostic"}
	for _, names := range [][]string{nil, {}, {"value"}} {
		entity.parameterNames = names
		want := []Entity{entity}
		record := recordExtraction(want, "TypeScript", status)
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		var decoded extractionRecord
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Version != extractionFormatVersion || decoded.Language != "TypeScript" || decoded.Status != status || !reflect.DeepEqual(decoded.entities(), want) {
			t.Fatalf("round trip lost parser metadata: %s", encoded)
		}
	}
	for _, entities := range [][]Entity{nil, {}} {
		if got := recordExtraction(entities, "Go", ParseStatus{}).entities(); !reflect.DeepEqual(got, entities) {
			t.Fatal("lost nil/empty declaration distinction")
		}
	}
}

func TestExtractionRecordDetached(t *testing.T) {
	original := []Entity{{parameterNames: []string{"before"}}}
	record := recordExtraction(original, "TypeScript", ParseStatus{})
	original[0].parameterNames[0] = "mutated"
	first := record.entities()
	first[0].parameterNames[0] = "also mutated"
	if got := record.entities()[0].parameterNames[0]; got != "before" {
		t.Fatalf("payload shares mutable metadata: %q", got)
	}
}

// These fixtures were authored for this task from the plan's declaration and
// mutation contracts; they were not obtained from an external implementation.
func TestCapturedExtractionMatchesParser(t *testing.T) {
	fixtures := map[string]string{
		"shared.go":   "package example\nfunc Shared(v int) int { return v + 1 }\nfunc Caller() int { return Shared(2) }\n",
		"overload.ts": "function f(x: string): string;\nfunction f(x: number): number;\nfunction f(x: any): any { function local() { return x; } return local(); }\n",
		"closure.py":  "def outer(x):\n    def inner(y):\n        return x + y\n    return inner(1)\n",
		"linkage.hpp": "extern \"C\" { int call(int value); }\nint other(int value) { return value; }\n",
		"broken.go":   "package example\nfunc Broken( {\n",
	}
	for path, content := range fixtures {
		for _, profile := range []Profile{ProfileSyntaxOnly, ProfileFast, ProfileFull} {
			t.Run(path+"/"+string(profile), func(t *testing.T) {
				source := captureSource(path, content)
				lang, ok := languageForContent(path, content)
				if !ok {
					t.Fatal("fixture language unavailable")
				}
				spec := resolveProfile(profile)
				expected, name, status := parseWithProfile(TreeSitterParser{}, spec, lang, path, content)
				record := extractCapturedSource(spec, lang, source)
				encoded, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				var decoded extractionRecord
				if err := json.Unmarshal(encoded, &decoded); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(expected, decoded.entities()) || name != decoded.Language || status != decoded.Status {
					t.Fatal("pure extraction changed parser output")
				}
				before := entitySymbols("fixture", path, name, expected)
				after := entitySymbols("fixture", path, name, decoded.entities())
				if !reflect.DeepEqual(before, after) {
					t.Fatal("symbol IDs or internal resolution metadata changed")
				}
			})
		}
	}
}

func TestCapturedSourceMutationSchedule(t *testing.T) {
	a := "package example\nfunc A() {}\n"
	b := "package example\nfunc B() {}\n" // same size; stats cannot distinguish
	live := a
	captured := captureSource("fixture.go", live)
	live = b
	if captureSource("fixture.go", live).digest == captured.digest {
		t.Fatal("equal-size edit retained digest")
	}
	live = a // A -> B -> A does not change the bytes already acquired
	lang, _ := languageForContent(captured.path, captured.content)
	record := extractCapturedSource(resolveProfile(ProfileFull), lang, captured)
	if len(record.Declarations) == 0 || record.Declarations[0].Name != "A" {
		t.Fatal("extraction did not use captured bytes")
	}
	if captured.content != live || captured.digest != contentHash([]byte(a)) {
		t.Fatal("capture identity changed")
	}
}
