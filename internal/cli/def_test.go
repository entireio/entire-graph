package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// defFixtureSnapshot is the shape the reported defect had: a type `Edit` whose
// associated functions live in a separate `impl` block, and a DIFFERENT type
// `Fix` whose field and method are both spelled `edits` — a name-substring
// lookup for "Edit" returns Fix's members and none of Edit's impl block.
func defFixtureSnapshot() sem.ProviderSnapshot {
	symbol := func(id, kind, name, qualified, file string, line int, container, signature string) sem.SymbolRecord {
		return sem.SymbolRecord{
			ID: id, Kind: kind, Name: name, QualifiedName: qualified, FilePath: file,
			StartLine: line, EndLine: line + 2, ContainerID: container,
			Signature: signature, Language: "Rust",
		}
	}
	symbols := []sem.SymbolRecord{
		symbol("ranged", "trait", "Ranged", "Ranged", "src/traits.rs", 1, "", "pub trait Ranged"),
		symbol("ranged.range", "method", "range", "Ranged.range", "src/traits.rs", 2, "ranged", "fn range(&self) -> TextRange"),
		symbol("edit", "struct", "Edit", "Edit", "src/edit.rs", 12, "", "pub struct Edit"),
		symbol("edit.range", "field", "range", "Edit.range", "src/edit.rs", 14, "edit", "range TextRange"),
		symbol("edit.content_field", "field", "content", "Edit.content", "src/edit.rs", 16, "edit", "content Option<Box<str>>"),
		symbol("edit.deletion", "method", "deletion", "Edit.deletion", "src/edit.rs", 22, "edit",
			"pub const fn deletion(start: TextSize, end: TextSize) -> Self"),
		symbol("edit.range_method", "method", "range", "Edit.range", "src/edit.rs", 114, "edit", "fn range(&self) -> TextRange"),
		symbol("fix", "struct", "Fix", "Fix", "src/fix.rs", 20, "", "pub struct Fix"),
		symbol("fix.edits_field", "field", "edits", "Fix.edits", "src/fix.rs", 48, "fix", "edits Box<[Edit]>"),
		symbol("fix.edits_method", "method", "edits", "Fix.edits", "src/fix.rs", 146, "fix", "pub fn edits(&self) -> &[Edit]"),
	}
	relation := func(from, to, kind, scope string) sem.RelationRecord {
		return sem.RelationRecord{RecordType: "relation", FromID: from, ToID: to, Type: kind, RelationScope: scope}
	}
	relations := []sem.RelationRecord{
		relation("edit", "edit.range", "CONTAINS", "file"),
		relation("edit", "edit.content_field", "CONTAINS", "file"),
		relation("edit", "edit.deletion", "CONTAINS", "file"),
		relation("edit", "edit.range_method", "CONTAINS", "file"),
		relation("fix", "fix.edits_field", "CONTAINS", "file"),
		relation("fix", "fix.edits_method", "CONTAINS", "file"),
		relation("ranged", "ranged.range", "CONTAINS", "file"),
		relation("edit", "ranged", "IMPLEMENTS", "file"),
		relation("edit.range_method", "ranged.range", "OVERRIDES", "file"),
	}
	return sem.ProviderSnapshot{
		Header: sem.SnapshotHeader{RepoRoot: "/repo", Profile: "full"},
		Files: []sem.FileRecord{
			{Path: "src/edit.rs"}, {Path: "src/fix.rs"}, {Path: "src/traits.rs"},
		},
		Symbols:   symbols,
		Relations: relations,
	}
}

func defOne(t *testing.T, query string) defDeclaration {
	t.Helper()
	response := buildDefResponse(defFixtureSnapshot(), defFlags{Symbol: query, MemberLimit: defaultDefMemberLimit})
	if len(response.Declarations) == 0 {
		t.Fatalf("def %q returned no declarations", query)
	}
	return response.Declarations[0]
}

func defMemberNames(members []defMember) []string {
	names := make([]string, 0, len(members))
	for _, member := range members {
		names = append(names, member.QualifiedName)
	}
	return names
}

func TestDefTypeReturnsItsOwnMembersOnly(t *testing.T) {
	t.Parallel()
	declaration := defOne(t, "Edit")
	if declaration.Kind != "struct" || declaration.FilePath != "src/edit.rs" || declaration.StartLine != 12 {
		t.Fatalf("declaration = %#v", declaration)
	}
	wantFields := []string{"Edit.range", "Edit.content"}
	if got := defMemberNames(declaration.Fields); strings.Join(got, ",") != strings.Join(wantFields, ",") {
		t.Errorf("fields = %v, want %v", got, wantFields)
	}
	// The associated function that lives in the separate impl block is the whole
	// point: it was missing before, and it is the member a caller needs.
	wantMethods := []string{"Edit.deletion", "Edit.range"}
	if got := defMemberNames(declaration.Methods); strings.Join(got, ",") != strings.Join(wantMethods, ",") {
		t.Errorf("methods = %v, want %v", got, wantMethods)
	}
	for _, member := range append(defMemberNames(declaration.Fields), defMemberNames(declaration.Methods)...) {
		if strings.HasPrefix(member, "Fix.") {
			t.Errorf("Edit reported Fix's member %q", member)
		}
	}
}

func TestDefMethodReportsItsOwningType(t *testing.T) {
	t.Parallel()
	declaration := defOne(t, "deletion")
	if declaration.Owner == nil || declaration.Owner.Name != "Edit" {
		t.Fatalf("owner = %#v", declaration.Owner)
	}
	if got := defDisplayName(declaration); got != "Edit::deletion" {
		t.Errorf("display name = %q, want %q", got, "Edit::deletion")
	}
	var buffer bytes.Buffer
	response := buildDefResponse(defFixtureSnapshot(), defFlags{Symbol: "deletion", MemberLimit: defaultDefMemberLimit})
	if err := writeDefText(&buffer, response, defaultDefContextBytes); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buffer.String(), "Edit::deletion") {
		t.Errorf("rendered card does not name the owning type:\n%s", buffer.String())
	}
}

func TestDefTraitReportsItsImplementors(t *testing.T) {
	t.Parallel()
	declaration := defOne(t, "Ranged")
	if len(declaration.Implementors) != 1 || declaration.Implementors[0].Name != "Edit" {
		t.Fatalf("implementors = %#v", declaration.Implementors)
	}
	if declaration.Implementors[0].FilePath != "src/edit.rs" || declaration.Implementors[0].StartLine != 12 {
		t.Errorf("implementor location = %#v", declaration.Implementors[0])
	}
}

func TestDefLabelsTraitImplementationsOnTheType(t *testing.T) {
	t.Parallel()
	declaration := defOne(t, "Edit")
	origins := map[string]string{}
	for _, member := range declaration.Methods {
		origins[member.Name] = member.Origin
	}
	if origins["deletion"] != "declared" {
		t.Errorf("inherent impl member origin = %q, want declared", origins["deletion"])
	}
	if origins["range"] != "impl:Ranged" {
		t.Errorf("trait impl member origin = %q, want impl:Ranged", origins["range"])
	}
	// The trait's own declaration of the same member must not be listed a second
	// time as inherited: the type overrides it.
	seen := 0
	for _, member := range declaration.Methods {
		if member.Name == "range" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("member `range` listed %d times, want 1", seen)
	}
}

func TestDefAcceptsLanguageSeparators(t *testing.T) {
	t.Parallel()
	for _, query := range []string{"Edit.deletion", "Edit::deletion", "Edit#deletion", "Edit->deletion"} {
		response := buildDefResponse(defFixtureSnapshot(), defFlags{Symbol: query, MemberLimit: defaultDefMemberLimit})
		if len(response.Declarations) != 1 || response.Declarations[0].Name != "deletion" {
			t.Errorf("def %q resolved to %#v", query, response.Declarations)
		}
	}
}

func TestDefNeverMatchesASubstring(t *testing.T) {
	t.Parallel()
	// `edits` contains `edit`, and `Edi` is a prefix of `Edit`. Neither is a name
	// in this snapshot, so both must return nothing rather than the nearest
	// spelling — a lookup that guesses is how one type's members end up on
	// another type's card.
	for _, query := range []string{"Edi", "dele", "edit"} {
		response := buildDefResponse(defFixtureSnapshot(), defFlags{Symbol: query, MemberLimit: defaultDefMemberLimit})
		if query == "edit" {
			// Case-insensitive exact match on `Edit` is intended.
			if len(response.Declarations) != 1 || response.Declarations[0].Name != "Edit" {
				t.Errorf("def %q = %#v, want the Edit declaration", query, response.Declarations)
			}
			continue
		}
		if len(response.Declarations) != 0 {
			t.Errorf("def %q matched %#v, want no match", query, response.Declarations)
		}
	}
}

func TestDefNoMatchExplainsItself(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	response := buildDefResponse(defFixtureSnapshot(), defFlags{Symbol: "Nope", MemberLimit: defaultDefMemberLimit})
	if err := writeDefText(&buffer, response, defaultDefContextBytes); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buffer.String(), "No symbols matched") {
		t.Errorf("no-match output = %q", buffer.String())
	}
}

func TestDefRespectsTheByteCeiling(t *testing.T) {
	t.Parallel()
	response := buildDefResponse(defFixtureSnapshot(), defFlags{Symbol: "Edit", MemberLimit: defaultDefMemberLimit})
	for _, budget := range []int{40, 120, 200, 4096} {
		var buffer bytes.Buffer
		if err := writeDefText(&buffer, response, budget); err != nil {
			t.Fatal(err)
		}
		if buffer.Len() > budget {
			t.Errorf("budget %d produced %d bytes", budget, buffer.Len())
		}
		if budget >= 200 && !strings.Contains(buffer.String(), "src/edit.rs:12") {
			t.Errorf("budget %d dropped the identity line: %q", budget, buffer.String())
		}
	}
}

func TestDefMemberLimitTruncatesWithACount(t *testing.T) {
	t.Parallel()
	response := buildDefResponse(defFixtureSnapshot(), defFlags{Symbol: "Edit", MemberLimit: 1})
	declaration := response.Declarations[0]
	if len(declaration.Methods) != 1 || declaration.MethodsTotal != 2 {
		t.Fatalf("methods = %d shown of %d total", len(declaration.Methods), declaration.MethodsTotal)
	}
	var buffer bytes.Buffer
	if err := writeDefText(&buffer, response, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buffer.String(), "(+1 more)") {
		t.Errorf("truncated member list does not report the omitted count:\n%s", buffer.String())
	}
}

func TestParseDefFlags(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		args    []string
		wantErr bool
		check   func(defFlags) string
	}{
		{
			name: "positional name",
			args: []string{"Edit"},
			check: func(flags defFlags) string {
				if flags.Symbol != "Edit" || flags.Format != "text" || flags.Profile != "full" {
					return "unexpected defaults"
				}
				return ""
			},
		},
		{
			name: "symbol flag with selectors",
			args: []string{"--symbol", "range", "--file", "src/edit.rs", "--line", "114", "--kind", "method"},
			check: func(flags defFlags) string {
				if flags.Symbol != "range" || flags.File != "src/edit.rs" || flags.Line != 114 || flags.Kind != "method" {
					return "selectors not parsed"
				}
				return ""
			},
		},
		{name: "no name", args: nil, wantErr: true},
		// SUPERSEDED: `def A B` is now the multi-query form. Agents batch shell calls under the prompt's
		// batching rule (laravel chained three greps into one Bash call), and a tool that answers one
		// name per invocation cannot compete with that.
		{
			name: "two names is a multi-query", args: []string{"Edit", "Fix"}, wantErr: false,
			check: func(flags defFlags) string {
				if len(flags.Symbols) != 2 || flags.Symbols[0] != "Edit" || flags.Symbols[1] != "Fix" {
					return "both names must survive parsing"
				}
				if flags.Symbol != "Edit" {
					return "Symbol must stay the first name for single-query callers"
				}
				return ""
			},
		},
		{name: "unknown flag", args: []string{"Edit", "--nope"}, wantErr: true},
		{name: "missing value", args: []string{"--symbol"}, wantErr: true},
		{name: "zero members", args: []string{"Edit", "--members", "0"}, wantErr: true},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			flags, err := parseDefFlags(testCase.args)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("parseDefFlags(%v) = %#v, want error", testCase.args, flags)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if problem := testCase.check(flags); problem != "" {
				t.Errorf("%s: %#v", problem, flags)
			}
		})
	}
}

func TestDefMergesPartialDeclarationsButNotUnrelatedNamesakes(t *testing.T) {
	t.Parallel()
	snapshot := sem.ProviderSnapshot{
		Header: sem.SnapshotHeader{RepoRoot: "/repo", Profile: "full"},
		Files:  []sem.FileRecord{{Path: "Edit.Core.cs"}, {Path: "Edit.Factories.cs"}, {Path: "Other.cs"}},
		Symbols: []sem.SymbolRecord{
			{ID: "core", Kind: "class", Name: "Edit", QualifiedName: "Edit", FilePath: "Edit.Core.cs", StartLine: 3, EndLine: 6, Language: "C#", Signature: "public partial class Edit"},
			{ID: "factories", Kind: "class", Name: "Edit", QualifiedName: "Edit", FilePath: "Edit.Factories.cs", StartLine: 3, EndLine: 6, Language: "C#", Signature: "public partial class Edit"},
			{ID: "other", Kind: "class", Name: "Edit", QualifiedName: "Edit", FilePath: "Other.cs", StartLine: 3, EndLine: 6, Language: "C#", Signature: "public class Edit"},
			{ID: "range", Kind: "field", Name: "_range", QualifiedName: "Edit._range", FilePath: "Edit.Core.cs", StartLine: 5, EndLine: 5, ContainerID: "core", Language: "C#", Signature: "_range uint"},
			// The symbol record keeps the container it was WRITTEN in, while the
			// canonicalized CONTAINS edge names the part that stands for the type.
			// A member reachable both ways must still be listed once.
			{ID: "deletion", Kind: "method", Name: "Deletion", QualifiedName: "Edit.Deletion", FilePath: "Edit.Factories.cs", StartLine: 5, EndLine: 5, ContainerID: "factories", Language: "C#", Signature: "public static Edit Deletion(uint start)"},
			{ID: "unrelated", Kind: "method", Name: "Unrelated", QualifiedName: "Edit.Unrelated", FilePath: "Other.cs", StartLine: 5, EndLine: 5, ContainerID: "other", Language: "C#", Signature: "public void Unrelated()"},
		},
		Relations: []sem.RelationRecord{
			{RecordType: "relation", FromID: "core", ToID: "range", Type: "CONTAINS", RelationScope: "file"},
			{RecordType: "relation", FromID: "core", ToID: "deletion", Type: "CONTAINS", RelationScope: "module"},
			{RecordType: "relation", FromID: "other", ToID: "unrelated", Type: "CONTAINS", RelationScope: "file"},
		},
	}
	response := buildDefResponse(snapshot, defFlags{Symbol: "Edit", MemberLimit: defaultDefMemberLimit})
	if response.DeclarationTotal != 2 {
		t.Fatalf("declarations = %d, want 2 (one merged partial type + one namesake)", response.DeclarationTotal)
	}
	merged := response.Declarations[0]
	if len(merged.Parts) != 1 || merged.Parts[0].FilePath != "Edit.Factories.cs" {
		t.Errorf("partial parts = %#v", merged.Parts)
	}
	names := append(defMemberNames(merged.Fields), defMemberNames(merged.Methods)...)
	if strings.Join(names, ",") != "Edit._range,Edit.Deletion" {
		t.Errorf("merged member set = %v", names)
	}
	namesake := response.Declarations[1]
	if len(namesake.Parts) != 0 || len(namesake.Methods) != 1 || namesake.Methods[0].Name != "Unrelated" {
		t.Errorf("unrelated namesake was merged: %#v", namesake)
	}
}
