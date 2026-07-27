package sem

import (
	"sort"
	"strings"
	"testing"
)

// A type's public API is its member set, and every language writes that member
// set somewhere other than inside the type declaration: Rust in `impl` blocks,
// Go in receiver functions, C# in extension methods and further `partial`
// declarations, Kotlin in extension functions, PHP in traits, Ruby in modules.
// A graph that indexes the type and the members separately and never joins them
// cannot answer "what can I do with one of these", which is the single most
// common question asked of a type.
//
// These tests assert the join for each of those forms, per language, from source.

// memberJoinCase is one language's spelling of the same shape: a type `Edit`
// with an associated function `deletion`, an instance method, and a second type
// `Fix` whose member names deliberately share a substring with `Edit` so a
// name-based join would visibly mis-attribute them.
type memberJoinCase struct {
	name string
	path string
	src  string
	// wantMembers are qualified names that must be members of Edit.
	wantMembers []string
	// wantMemberEdges are member names that must reach Edit through a CONTAINS
	// edge even though their container_id points elsewhere (extension members).
	wantMemberEdges []string
	// wantTypeEdges are "<Type> <RELATION> <Super>" triples that must exist.
	wantTypeEdges []string
}

func memberJoinCases() []memberJoinCase {
	return []memberJoinCase{
		{
			name: "rust inherent and trait impl",
			path: "src/lib.rs",
			src: `pub trait Ranged {
    fn range(&self) -> u32;
}

pub struct Edit {
    range: u32,
}

impl Edit {
    pub const fn deletion(start: u32, end: u32) -> Self {
        Self { range: start + end }
    }
}

impl Ranged for Edit {
    fn range(&self) -> u32 {
        self.range
    }
}

pub struct Fix {
    edits: Vec<Edit>,
}

impl Fix {
    pub fn edits(&self) -> &[Edit] {
        &self.edits
    }
}
`,
			wantMembers:   []string{"Edit.deletion", "Edit.range"},
			wantTypeEdges: []string{"Edit IMPLEMENTS Ranged"},
		},
		{
			name: "go receiver methods",
			path: "edit.go",
			src: `package audit

type Edit struct {
	rng uint32
}

func (e *Edit) Deletion(start, end uint32) *Edit { return &Edit{rng: start + end} }

func (e Edit) Range() uint32 { return e.rng }

type Fix struct {
	edits []Edit
}

func (f *Fix) Edits() []Edit { return f.edits }
`,
			wantMembers: []string{"Edit.Deletion", "Edit.Range"},
		},
		{
			name: "csharp partial declarations and extension methods",
			path: "Edit.cs",
			src: `namespace Audit;

public partial class Edit
{
    private uint _range;
}

public partial class Edit
{
    public static Edit Deletion(uint start, uint end) => new Edit();

    public string Content() => "";
}

public static class EditExtensions
{
    public static bool IsEmpty(this Edit edit) => true;
}

public class Fix
{
    public Edit[] Edits() => System.Array.Empty<Edit>();
}
`,
			wantMembers:     []string{"Edit._range", "Edit.Deletion", "Edit.Content"},
			wantMemberEdges: []string{"IsEmpty"},
		},
		{
			name: "kotlin extension functions",
			path: "Edit.kt",
			src: `package audit

class Edit(private val rng: Int) {
    fun deletion(start: Int, end: Int): Edit = Edit(start + end)
}

fun Edit.isEmpty(): Boolean = false

class Fix(private val edits: List<Edit>) {
    fun edits(): List<Edit> = edits
}
`,
			wantMembers:     []string{"Edit.deletion"},
			wantMemberEdges: []string{"isEmpty"},
		},
		{
			name: "php trait composition",
			path: "Edit.php",
			src: `<?php

namespace Audit;

trait HasContent
{
    public function content(): ?string
    {
        return $this->content;
    }
}

class Edit
{
    use HasContent;

    public static function deletion(int $start, int $end): self
    {
        return new self();
    }
}

class Fix
{
    public function edits(): array
    {
        return [];
    }
}
`,
			wantMembers:   []string{"Edit.deletion", "HasContent.content"},
			wantTypeEdges: []string{"Edit INHERITS HasContent"},
		},
		{
			name: "ruby module mixin and singleton methods",
			path: "edit.rb",
			src: `module Ranged
  def range
    @rng
  end
end

class Edit
  include Ranged

  def self.deletion(start, finish)
    new(start + finish)
  end

  def content
    @content
  end
end

class Fix
  def edits
    @edits
  end
end
`,
			wantMembers:   []string{"Edit.deletion", "Edit.content", "Ranged.range"},
			wantTypeEdges: []string{"Edit INHERITS Ranged"},
		},
		{
			name: "swift extensions",
			path: "Edit.swift",
			src: `protocol Ranged {
    func range() -> UInt32
}

struct Edit {
    private var rng: UInt32

    static func deletion(start: UInt32, end: UInt32) -> Edit {
        return Edit(rng: start + end)
    }
}

extension Edit: Ranged {
    func range() -> UInt32 { return rng }
}

struct Fix {
    func editsValue() -> [Edit] { return [] }
}
`,
			wantMembers: []string{"Edit.deletion", "Edit.range"},
		},
	}
}

func TestMemberJoinAcrossLanguages(t *testing.T) {
	t.Parallel()
	for _, testCase := range memberJoinCases() {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			writeFile(t, repo, testCase.path, testCase.src)
			snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
			if err != nil {
				t.Fatal(err)
			}
			byID := map[string]SymbolRecord{}
			for _, symbol := range snapshot.Symbols {
				byID[symbol.ID] = symbol
			}
			qualifiedByContainer := map[string][]string{}
			for _, symbol := range snapshot.Symbols {
				if symbol.ContainerID == "" {
					continue
				}
				owner, ok := byID[symbol.ContainerID]
				if !ok {
					continue
				}
				qualifiedByContainer[owner.Name] = append(qualifiedByContainer[owner.Name], symbol.QualifiedName)
			}
			for _, want := range testCase.wantMembers {
				owner, _, _ := strings.Cut(want, ".")
				if !containsName(qualifiedByContainer[owner], want) {
					t.Errorf("member %q is not joined to %q; %s has %v",
						want, owner, owner, qualifiedByContainer[owner])
				}
			}
			// The member set of one type must never contain another type's
			// members. `Fix.edits` shares the substring `edit` with `Edit`, which
			// is exactly the mis-attribution a name-based join produces.
			for _, member := range qualifiedByContainer["Edit"] {
				if strings.HasPrefix(member, "Fix.") {
					t.Errorf("Edit claims Fix's member %q", member)
				}
			}
			extensionMembers := map[string]bool{}
			for _, relation := range snapshot.Relations {
				if relation.Type != "CONTAINS" {
					continue
				}
				owner, ok := byID[relation.FromID]
				if !ok || owner.Name != "Edit" {
					continue
				}
				if member, ok := byID[relation.ToID]; ok {
					extensionMembers[member.Name] = true
				}
			}
			for _, want := range testCase.wantMemberEdges {
				if !extensionMembers[want] {
					t.Errorf("extension member %q has no membership edge from Edit; Edit reaches %v",
						want, sortedNames(extensionMembers))
				}
			}
			edges := map[string]bool{}
			for _, relation := range snapshot.Relations {
				switch relation.Type {
				case "EXTENDS", "INHERITS", "IMPLEMENTS":
				default:
					continue
				}
				from, ok := byID[relation.FromID]
				if !ok {
					continue
				}
				name := ""
				if to, ok := byID[relation.ToID]; ok {
					name = to.Name
				}
				if name == "" {
					continue
				}
				edges[from.Name+" "+relation.Type+" "+name] = true
			}
			for _, want := range testCase.wantTypeEdges {
				if !edges[want] {
					t.Errorf("missing type edge %q; have %v", want, sortedNames(edges))
				}
			}
		})
	}
}

// TestCSharpPartialDeclarationsShareOneMemberSet pins the partial-type join: the
// members written in either declaration must belong to ONE type, so a lookup of
// the type returns its whole surface instead of the slice that happened to be
// written in the part the query landed on.
func TestCSharpPartialDeclarationsShareOneMemberSet(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "Edit.Core.cs", `namespace Audit;

public partial class Edit
{
    private uint _range;
}
`)
	writeFile(t, repo, "Edit.Factories.cs", `namespace Audit;

public partial class Edit
{
    public static Edit Deletion(uint start, uint end) => new Edit();
}
`)
	writeFile(t, repo, "Other.cs", `namespace Other;

public class Edit
{
    public void Unrelated() { }
}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]SymbolRecord{}
	for _, symbol := range snapshot.Symbols {
		byID[symbol.ID] = symbol
	}
	membersByOwnerID := map[string][]string{}
	for _, relation := range snapshot.Relations {
		if relation.Type != "CONTAINS" {
			continue
		}
		if member, ok := byID[relation.ToID]; ok {
			membersByOwnerID[relation.FromID] = append(membersByOwnerID[relation.FromID], member.Name)
		}
	}
	merged := ""
	for ownerID, members := range membersByOwnerID {
		if containsName(members, "Deletion") && containsName(members, "_range") {
			merged = ownerID
		}
	}
	if merged == "" {
		t.Fatalf("no single Edit declaration owns both partial parts' members: %v", membersByOwnerID)
	}
	// The unrelated same-named class in another namespace is a different type and
	// must keep its own member set.
	for ownerID, members := range membersByOwnerID {
		if containsName(members, "Unrelated") && ownerID == merged {
			t.Errorf("a non-partial same-named class was merged into the partial type")
		}
	}
}

func containsName(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sortedNames(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestMixinSupertypeEdges(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		language string
		body     string
		want     []string
	}{
		{
			name:     "php single trait",
			language: "PHP",
			body:     "class Edit\n{\n    use HasContent;\n}",
			want:     []string{"HasContent"},
		},
		{
			name:     "php multiple traits on one statement",
			language: "PHP",
			body:     "class Edit\n{\n    use HasContent, HasRange;\n}",
			want:     []string{"HasContent", "HasRange"},
		},
		{
			name:     "php namespaced trait keeps the last segment",
			language: "PHP",
			body:     "class Edit\n{\n    use \\App\\Concerns\\HasContent;\n}",
			want:     []string{"HasContent"},
		},
		{
			name:     "php trait conflict-resolution block",
			language: "PHP",
			body:     "class Edit\n{\n    use HasContent {\n        content as own;\n    }\n}",
			want:     []string{"HasContent"},
		},
		{
			name:     "php closure capture is not composition",
			language: "PHP",
			body:     "class Edit\n{\n    public function f($x) {\n        return function () use ($x) { return $x; };\n    }\n}",
			want:     nil,
		},
		{
			name:     "ruby include prepend extend",
			language: "Ruby",
			body:     "class Edit\n  include Ranged\n  prepend Loggable\n  extend Factory\nend",
			want:     []string{"Ranged", "Loggable", "Factory"},
		},
		{
			name:     "ruby include with a trailing comment",
			language: "Ruby",
			body:     "class Edit\n  include Ranged # positions\nend",
			want:     []string{"Ranged"},
		},
		{
			name:     "ruby method call named include is not a mixin",
			language: "Ruby",
			body:     "class Edit\n  def f(list)\n    list.include? 3\n  end\nend",
			want:     nil,
		},
		{
			name:     "language without body-stated mixins",
			language: "Go",
			body:     "type Edit struct {\n\tinclude Ranged\n}",
			want:     nil,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			edges := mixinSupertypeEdges(testCase.language, testCase.body)
			got := make([]string, 0, len(edges))
			for _, edge := range edges {
				if edge.Relation != "INHERITS" {
					t.Errorf("mixin edge relation = %q, want INHERITS", edge.Relation)
				}
				got = append(got, edge.Super)
			}
			if strings.Join(got, ",") != strings.Join(testCase.want, ",") {
				t.Errorf("mixinSupertypeEdges(%q) = %v, want %v", testCase.language, got, testCase.want)
			}
		})
	}
}

func TestCallableSignatureTail(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		symbol    string
		signature string
		want      string
	}{
		{
			name:      "go method skips its receiver",
			symbol:    "Deletion",
			signature: "func (e *Edit) Deletion(start, end uint32) *Edit",
			want:      "(start, end uint32) *Edit",
		},
		{
			name:      "rust associated function",
			symbol:    "deletion",
			signature: "pub const fn deletion(start: TextSize, end: TextSize) -> Self",
			want:      "(start: TextSize, end: TextSize) -> Self",
		},
		{
			name:      "name that is a substring of an earlier word",
			symbol:    "range",
			signature: "fn range_of(range: TextRange) -> TextRange",
			want:      "(range: TextRange) -> TextRange",
		},
		{
			name:      "generic parameter list",
			symbol:    "map",
			signature: "fun <T> map<T>(block: (T) -> T): Edit",
			want:      "<T>(block: (T) -> T): Edit",
		},
		{
			name:      "unknown name falls back to the first parameter list",
			symbol:    "missing",
			signature: "public void run(int times)",
			want:      "(int times)",
		},
		{
			name:      "no parameter list at all",
			symbol:    "value",
			signature: "val value: Int",
			want:      "",
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, ok := CallableSignatureTail(testCase.symbol, testCase.signature)
			if testCase.want == "" {
				if ok {
					t.Errorf("CallableSignatureTail(%q, %q) = %q, want no tail", testCase.symbol, testCase.signature, got)
				}
				return
			}
			if !ok || got != testCase.want {
				t.Errorf("CallableSignatureTail(%q, %q) = %q (%v), want %q",
					testCase.symbol, testCase.signature, got, ok, testCase.want)
			}
		})
	}
}

func TestExtensionReceiverTypeName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		language  string
		signature string
		want      string
	}{
		{"csharp extension method", "C#", "public static bool IsEmpty(this Edit edit)", "Edit"},
		{"csharp extension with in modifier", "C#", "public static uint Range(this in Edit edit)", "Edit"},
		{"csharp qualified receiver", "C#", "public static bool F(this Audit.Edit edit)", "Edit"},
		{"csharp instance method is not an extension", "C#", "public bool IsEmpty(Edit edit)", ""},
		{"csharp static without this is not an extension", "C#", "public static bool F(Edit edit)", ""},
		{"kotlin extension function", "Kotlin", "fun Edit.isEmpty(): Boolean", "Edit"},
		{"kotlin generic extension function", "Kotlin", "fun <T> Edit.map(block: (T) -> T): Edit", "Edit"},
		{"kotlin extension property", "Kotlin", "val Edit.isBlank: Boolean", "Edit"},
		{"kotlin member function is not an extension", "Kotlin", "fun isEmpty(): Boolean", ""},
		{"unsupported language", "Go", "func (e *Edit) Range() uint32", ""},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := extensionReceiverTypeName(testCase.language, testCase.signature); got != testCase.want {
				t.Errorf("extensionReceiverTypeName(%q, %q) = %q, want %q",
					testCase.language, testCase.signature, got, testCase.want)
			}
		})
	}
}
