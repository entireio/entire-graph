package sem

import (
	"strings"
	"testing"
)

func TestProtocolBuffersProto2WithoutSyntaxParsesLegacyGroups(t *testing.T) {
	content := `package protoexample;

enum FOO {X=17;};

message Test {
   required string label = 1;
   optional int32 type = 2[default=77];
   repeated int64 reps = 3;
   optional group OptionalGroup = 4{
     required string RequiredField = 5;
   }
}
`
	entities, language, status := TreeSitterParser{}.ParseWithStatus("test.proto", content)
	if language != "Protocol Buffers" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("valid proto2 reported a parse failure: %+v", status)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		seen[entity.Name] = entity
	}
	for name, kind := range map[string]string{
		"FOO":                "enum",
		"Test":               "message",
		"Test.OptionalGroup": "message",
	} {
		entity, ok := seen[name]
		if !ok {
			t.Fatalf("missing %s entity %q in %#v", kind, name, entities)
		}
		if entity.Kind != kind {
			t.Fatalf("%s kind = %q, want %q", name, entity.Kind, kind)
		}
	}
	if seen["FOO"].StartLine != 3 || seen["Test"].StartLine != 5 || seen["Test.OptionalGroup"].StartLine != 9 {
		t.Fatalf("synthetic syntax line leaked into source locations: %#v", entities)
	}
	if !strings.Contains(seen["Test.OptionalGroup"].Signature, "optional group OptionalGroup") {
		t.Fatalf("group signature was rewritten instead of preserving source: %q", seen["Test.OptionalGroup"].Signature)
	}
}

func TestProtocolBuffersProto2GroupsRequireCanonicalNameAndPositiveIntLiteral(t *testing.T) {
	for _, test := range []struct {
		name string
		tag  string
	}{
		{name: "DecimalGroup", tag: "1"},
		{name: "OctalGroup", tag: "017"},
		{name: "HexGroup", tag: "0x1A"},
		{name: "UpperHexGroup", tag: "0Xf"},
	} {
		t.Run("valid_"+test.name, func(t *testing.T) {
			content := "syntax = \"proto2\";\nmessage Outer {\n  optional group " + test.name + " = " + test.tag + " {\n    required string value = 2;\n  }\n}\n"
			parseSource, entitySource, offset := prepareProtocolBuffersParseSource(content)
			if offset != 0 || entitySource != content || len(parseSource) != len(content) {
				t.Fatalf("compatibility view changed source binding: offset=%d parse=%q entity=%q", offset, parseSource, entitySource)
			}
			if strings.Contains(parseSource, "group "+test.name) {
				t.Fatalf("valid group %s=%s was not rewritten: %q", test.name, test.tag, parseSource)
			}

			entities, _, status := TreeSitterParser{}.ParseWithStatus("valid.proto", content)
			if status.ParseError {
				t.Fatalf("valid group %s=%s reported a parse failure: %+v", test.name, test.tag, status)
			}
			var group Entity
			for _, entity := range entities {
				if entity.Name == "Outer."+test.name {
					group = entity
					break
				}
			}
			if group.Kind != "message" || !strings.Contains(group.Signature, "optional group "+test.name+" = "+test.tag) {
				t.Fatalf("group entity did not preserve authored declaration: %#v (all=%#v)", group, entities)
			}
		})
	}

	for _, test := range []struct {
		name string
		tag  string
	}{
		{name: "lowerGroup", tag: "1"},
		{name: "_LeadingUnderscore", tag: "1"},
		{name: "ZeroDecimal", tag: "0"},
		{name: "ZeroOctal", tag: "00"},
		{name: "ZeroHex", tag: "0x0"},
		{name: "InvalidOctal", tag: "08"},
		{name: "Negative", tag: "-1"},
	} {
		t.Run("invalid_"+test.name, func(t *testing.T) {
			content := "syntax = \"proto2\";\nmessage Outer { optional group " + test.name + " = " + test.tag + " { required string value = 2; } }\n"
			parseSource, entitySource, offset := prepareProtocolBuffersParseSource(content)
			if offset != 0 || entitySource != content || len(parseSource) != len(content) {
				t.Fatalf("invalid declaration changed source binding: offset=%d parse=%q entity=%q", offset, parseSource, entitySource)
			}
			if !strings.Contains(parseSource, "group "+test.name) {
				t.Fatalf("invalid group was compatibility-rewritten: %q", parseSource)
			}
			_, _, status := TreeSitterParser{}.ParseWithStatus("invalid.proto", content)
			if !status.ParseError || status.Code != "E_PARSE_ERROR" {
				t.Fatalf("invalid group %s=%s failure = %+v, want visible E_PARSE_ERROR", test.name, test.tag, status)
			}
		})
	}
}

func TestProtocolBuffersExplicitProto2PreservesSourceAndRPCs(t *testing.T) {
	content := `syntax = "proto2";
package auth;
message Request { required string token = 1; }
message Reply { optional bool valid = 1 [default = true]; }
service Auth { rpc Validate(Request) returns (Reply); }
`
	parseSource, entitySource, offset := prepareProtocolBuffersParseSource(content)
	if offset != 0 || entitySource != content {
		t.Fatalf("explicit proto2 source binding changed: offset=%d source=%q", offset, entitySource)
	}
	if !strings.Contains(parseSource, `syntax = "proto3";`) || !strings.Contains(parseSource, "repeated string token") {
		t.Fatalf("proto3 compatibility view missing expected substitutions: %q", parseSource)
	}
	entities, _, status := TreeSitterParser{}.ParseWithStatus("auth.proto", content)
	if status.ParseError {
		t.Fatalf("valid explicit proto2 reported a parse failure: %+v", status)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		seen[entity.Name] = entity
	}
	for name, kind := range map[string]string{
		"Request":       "message",
		"Reply":         "message",
		"Auth":          "service",
		"Auth.Validate": "rpc",
	} {
		if seen[name].Kind != kind {
			t.Fatalf("%s = %#v, want kind %s (all=%#v)", name, seen[name], kind, entities)
		}
	}
	wantRequestBody := "message Request { required string token = 1; }"
	if seen["Request"].BodyHash != hash(normalize(wantRequestBody)) {
		t.Fatalf("original proto2 body was rewritten: hash=%q want=%q", seen["Request"].BodyHash, hash(normalize(wantRequestBody)))
	}
}

func TestProtocolBuffersSingleQuotedSyntaxNormalizesOnlyParseView(t *testing.T) {
	for _, test := range []struct {
		name        string
		content     string
		wantMessage string
		wantLegacy  bool
	}{
		{
			name:        "proto2",
			content:     "syntax = 'proto2';\nmessage Legacy { required string value = 1; }\n",
			wantMessage: "Legacy",
			wantLegacy:  true,
		},
		{
			name:        "proto3",
			content:     "syntax = 'proto3';\nmessage Current { string value = 1; }\n",
			wantMessage: "Current",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			parseSource, entitySource, offset := prepareProtocolBuffersParseSource(test.content)
			if offset != 0 || entitySource != test.content || len(parseSource) != len(test.content) {
				t.Fatalf("single-quoted syntax changed source binding: offset=%d parse=%q entity=%q", offset, parseSource, entitySource)
			}
			if !strings.HasPrefix(parseSource, `syntax = "proto3";`) {
				t.Fatalf("single-quoted syntax was not canonicalized for proto3 parser: %q", parseSource)
			}
			if test.wantLegacy && !strings.Contains(parseSource, "repeated string value") {
				t.Fatalf("single-quoted proto2 did not enable legacy compatibility: %q", parseSource)
			}

			entities, _, status := TreeSitterParser{}.ParseWithStatus("quoted.proto", test.content)
			if status.ParseError {
				t.Fatalf("valid single-quoted %s syntax reported a parse failure: %+v", test.name, status)
			}
			var message Entity
			for _, entity := range entities {
				if entity.Name == test.wantMessage {
					message = entity
					break
				}
			}
			if message.Kind != "message" || message.StartLine != 2 {
				t.Fatalf("single-quoted %s entity = %#v (all=%#v)", test.name, message, entities)
			}
			wantBody := strings.TrimSuffix(strings.SplitN(test.content, "\n", 2)[1], "\n")
			if message.BodyHash != hash(normalize(wantBody)) {
				t.Fatalf("single-quoted %s changed original entity body: hash=%q want=%q", test.name, message.BodyHash, hash(normalize(wantBody)))
			}
		})
	}
}

func TestProtocolBuffersProto2SyntaxAllowsBOMCommentsAndNewlines(t *testing.T) {
	content := "\uFEFF// generated legacy schema\n" + `syntax
/* before equals */
=
/* before value */
"proto2";
message Outer {
  optional
  /* between label and group */
  group Nested = 1 {
    required string value = 2;
  }
}
`
	parseSource, entitySource, offset := prepareProtocolBuffersParseSource(content)
	if offset != 0 || entitySource != content {
		t.Fatalf("trivia-rich explicit proto2 source changed: offset=%d source=%q", offset, entitySource)
	}
	if len(parseSource) != len(content) || strings.Contains(parseSource, "\uFEFF") {
		t.Fatalf("parse view did not preserve offsets while masking BOM: len=%d want=%d source=%q", len(parseSource), len(content), parseSource)
	}
	if !strings.Contains(parseSource, `"proto3"`) {
		t.Fatalf("multiline proto2 syntax literal was not rewritten: %q", parseSource)
	}

	entities, _, status := TreeSitterParser{}.ParseWithStatus("legacy.proto", content)
	if status.ParseError {
		t.Fatalf("valid trivia-rich proto2 reported a parse failure: %+v", status)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		seen[entity.Name] = entity
	}
	if seen["Outer"].StartLine != 7 {
		t.Fatalf("Outer location = %#v, want line 7", seen["Outer"])
	}
	if nested := seen["Outer.Nested"]; nested.Kind != "message" || nested.StartLine != 8 {
		t.Fatalf("multiline group = %#v, want qualified message on line 8 (all=%#v)", nested, entities)
	}

	omitted := "\uFEFFmessage Legacy { required string value = 1; }\n"
	omittedEntities, _, omittedStatus := TreeSitterParser{}.ParseWithStatus("omitted.proto", omitted)
	if omittedStatus.ParseError {
		t.Fatalf("BOM-prefixed omitted-syntax proto2 reported a parse failure: %+v", omittedStatus)
	}
	if len(omittedEntities) != 1 || omittedEntities[0].Name != "Legacy" || omittedEntities[0].StartLine != 1 {
		t.Fatalf("BOM/synthetic declaration shifted omitted-syntax entities: %#v", omittedEntities)
	}
}

func TestProtocolBuffersExplicitProto3DoesNotMaskLegacyOnlySyntax(t *testing.T) {
	for name, content := range map[string]string{
		"required field": `syntax = "proto3";
message Broken { required string value = 1; }
`,
		"group": `syntax = "proto3";
message Broken { optional group Legacy = 1 { string value = 2; } }
`,
	} {
		t.Run(name, func(t *testing.T) {
			parseSource, entitySource, offset := prepareProtocolBuffersParseSource(content)
			if offset != 0 || parseSource != content || entitySource != content {
				t.Fatalf("explicit proto3 was compatibility-rewritten: offset=%d parse=%q entity=%q", offset, parseSource, entitySource)
			}
			_, _, status := TreeSitterParser{}.ParseWithStatus("broken.proto", content)
			if !status.ParseError || status.Code != "E_PARSE_ERROR" {
				t.Fatalf("invalid explicit proto3 failure = %+v, want visible E_PARSE_ERROR", status)
			}
		})
	}
}

func TestProtocolBuffersNestedEnumsHaveStableQualifiedIDs(t *testing.T) {
	repo := t.TempDir()
	build := func(content string) ProviderSnapshot {
		t.Helper()
		writeFile(t, repo, "nested.proto", content)
		snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}

	before := build(`syntax = "proto3";
message A {
  enum State { UNKNOWN = 0; READY = 1; }
}
message B {
  enum State { UNKNOWN = 0; DONE = 1; }
}
`)
	after := build(`syntax = "proto3";
message A {
  enum State { UNKNOWN = 0; READY = 2; }
}
message B {
  enum State { UNKNOWN = 0; DONE = 1; }
}
`)

	index := func(snapshot ProviderSnapshot) map[string]SymbolRecord {
		out := map[string]SymbolRecord{}
		for _, symbol := range snapshot.Symbols {
			out[symbol.QualifiedName] = symbol
		}
		return out
	}
	beforeSymbols := index(before)
	afterSymbols := index(after)
	for _, qualified := range []string{"A.State", "B.State"} {
		beforeEnum, ok := beforeSymbols[qualified]
		if !ok || beforeEnum.Kind != "enum" {
			t.Fatalf("missing qualified enum %q in %#v", qualified, before.Symbols)
		}
		container := beforeSymbols[strings.TrimSuffix(qualified, ".State")]
		if beforeEnum.ContainerID != container.ID {
			t.Fatalf("%s container = %q, want %q", qualified, beforeEnum.ContainerID, container.ID)
		}
		if beforeEnum.Signature != "enum State" {
			t.Fatalf("%s signature includes enum body: %q", qualified, beforeEnum.Signature)
		}
		if beforeEnum.ID != afterSymbols[qualified].ID {
			t.Fatalf("%s compound id changed across body edit: before=%q after=%q", qualified, beforeEnum.ID, afterSymbols[qualified].ID)
		}
		contains := false
		for _, relation := range before.Relations {
			if relation.Type == "CONTAINS" && relation.FromID == container.ID && relation.ToID == beforeEnum.ID {
				contains = true
				break
			}
		}
		if !contains {
			t.Fatalf("missing %s -> %s CONTAINS relation in %#v", container.QualifiedName, qualified, before.Relations)
		}
	}
	if beforeSymbols["A.State"].ID == beforeSymbols["B.State"].ID {
		t.Fatalf("nested enums share an id: %#v", beforeSymbols)
	}
	if beforeSymbols["A.State"].BodyHash == afterSymbols["A.State"].BodyHash {
		t.Fatalf("A.State body hash did not change across enum value edit")
	}
	if beforeSymbols["B.State"].BodyHash != afterSymbols["B.State"].BodyHash {
		t.Fatalf("unchanged B.State body hash changed: before=%q after=%q", beforeSymbols["B.State"].BodyHash, afterSymbols["B.State"].BodyHash)
	}
}

func TestProtocolBuffersCompatibilityDoesNotRewriteCommentsOrStringOptions(t *testing.T) {
	content := `syntax = "proto2";
// optional group CommentOnly = 7 { required string nope = 1; }
message Note {
  optional string text = 1 [default = "required optional group"];
}
`
	parseSource, _, _ := prepareProtocolBuffersParseSource(content)
	if !strings.Contains(parseSource, "optional group CommentOnly") {
		t.Fatalf("comment text was rewritten: %q", parseSource)
	}
	if !strings.Contains(parseSource, `"required optional group"`) {
		t.Fatalf("string option was rewritten: %q", parseSource)
	}
	_, _, status := TreeSitterParser{}.ParseWithStatus("note.proto", content)
	if status.ParseError {
		t.Fatalf("valid proto2 string option reported a parse failure: %+v", status)
	}
}

func TestProtocolBuffersCompatibilityStillReportsMalformedProto2(t *testing.T) {
	content := `syntax = "proto2";
message Broken {
  required string value = 1;
`
	_, _, status := TreeSitterParser{}.ParseWithStatus("broken.proto", content)
	if !status.ParseError || status.Code != "E_PARSE_ERROR" {
		t.Fatalf("malformed proto2 failure = %+v, want visible E_PARSE_ERROR", status)
	}
}
