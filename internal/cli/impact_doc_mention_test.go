package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// docMentionSnapshot models the shape the defect was reported against:
//
//	Combobox.tsx --CALLS(import_resolved)--> buildGroupIndex   (a real caller)
//	App.tsx      --CALLS(import_resolved)--> Combobox          (a real transitive caller)
//	plans/*.md   --CALLS(name_only, file-level)--> buildGroupIndex  (a mention)
//	DESIGN.md    --CALLS(name_only, file-level)--> Combobox         (a mention)
//
// DESIGN.md contains no occurrence of buildGroupIndex at all, so traversing its edge a
// second hop invents a caller. A doc mention must never be an intermediate.
func docMentionSnapshot() sem.ProviderSnapshot {
	return sem.ProviderSnapshot{
		Files: []sem.FileRecord{
			{ID: "f-plan", Path: "plans/grouping-plan.md", Language: "Markdown"},
			{ID: "f-design", Path: "DESIGN.md", Language: "Markdown"},
			{ID: "f-changelog", Path: "CHANGELOG.md", Language: "Markdown"},
			{ID: "f-combobox", Path: "src/Combobox.tsx", Language: "TypeScript"},
			{ID: "f-group", Path: "src/groupIndex.ts", Language: "TypeScript"},
			{ID: "f-app", Path: "src/App.tsx", Language: "TypeScript"},
			{ID: "f-doctool", Path: "docs/gen.ts", Language: "TypeScript"},
		},
		Symbols: []sem.SymbolRecord{
			{ID: "s-group", Name: "buildGroupIndex", QualifiedName: "buildGroupIndex",
				Kind: "function", FilePath: "src/groupIndex.ts", StartLine: 6, EndLine: 12,
				Language: "TypeScript"},
			{ID: "s-combobox", Name: "Combobox", QualifiedName: "Combobox",
				Kind: "function", FilePath: "src/Combobox.tsx", StartLine: 3, EndLine: 6,
				Language: "TypeScript"},
			{ID: "s-app", Name: "App", QualifiedName: "App",
				Kind: "function", FilePath: "src/App.tsx", StartLine: 3, EndLine: 5,
				Language: "TypeScript"},
			{ID: "s-doctool", Name: "renderDocs", QualifiedName: "renderDocs",
				Kind: "function", FilePath: "docs/gen.ts", StartLine: 2, EndLine: 8,
				Language: "TypeScript"},
		},
		Relations: []sem.RelationRecord{
			{FromID: "s-combobox", ToID: "s-group", Type: "CALLS", Resolution: "import_resolved"},
			{FromID: "s-app", ToID: "s-combobox", Type: "CALLS", Resolution: "import_resolved"},
			{FromID: "f-plan", ToID: "s-group", Type: "CALLS", Resolution: "name_only",
				RelationScope: "workspace", Confidence: 0.68},
			{FromID: "f-design", ToID: "s-combobox", Type: "CALLS", Resolution: "name_only",
				RelationScope: "workspace", Confidence: 0.68},
			{FromID: "f-changelog", ToID: "s-group", Type: "CALLS", Resolution: "name_only",
				RelationScope: "workspace", Confidence: 0.68},
			// A doc-tool SCRIPT that lives under docs/ but really does call the symbol.
			// Resolved, so it is a real caller no matter where it lives.
			{FromID: "s-doctool", ToID: "s-group", Type: "CALLS", Resolution: "import_resolved"},
		},
	}
}

func TestImpactExcludesDocMentionsFromTransitiveExpansion(t *testing.T) {
	t.Parallel()
	response := buildImpactResponse(docMentionSnapshot(), impactFlags{
		Symbol: "buildGroupIndex", Depth: 2, Limit: defaultImpactSectionLimit,
	})
	if response.Focus == nil || response.Focus.ID != "s-group" {
		t.Fatalf("focus = %#v", response.Focus)
	}
	ids := make([]string, 0, len(response.Callers.Entries))
	for _, entry := range response.Callers.Entries {
		ids = append(ids, entry.Endpoint.ID)
	}
	for _, id := range ids {
		if id == "f-design" {
			t.Fatalf("DESIGN.md was reported as a caller of buildGroupIndex, which it never "+
				"mentions; callers = %v", ids)
		}
	}
	// Resolved chain survives at both depths, including the doc-tool script.
	for _, want := range []string{"s-combobox", "s-app", "s-doctool"} {
		found := false
		for _, id := range ids {
			if id == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("resolved caller %q was dropped; callers = %v", want, ids)
		}
	}
}

func TestImpactRanksDocMentionsLastAndLabelsThem(t *testing.T) {
	t.Parallel()
	response := buildImpactResponse(docMentionSnapshot(), impactFlags{
		Symbol: "buildGroupIndex", Depth: 2, Limit: defaultImpactSectionLimit,
	})
	sawResolvedAfterMention := false
	mentions := 0
	for _, entry := range response.Callers.Entries {
		if entry.Detail == impactMentionDetail {
			mentions++
			continue
		}
		if mentions > 0 {
			sawResolvedAfterMention = true
		}
	}
	if mentions != 2 {
		t.Fatalf("want 2 direct doc mentions (plan + changelog), got %d: %#v",
			mentions, response.Callers.Entries)
	}
	if sawResolvedAfterMention {
		t.Fatalf("a resolved caller sorted behind a doc mention: %#v", response.Callers.Entries)
	}

	var out bytes.Buffer
	writeImpactText(&out, response)
	text := out.String()
	if !strings.Contains(text, "grouping-plan.md (plans/grouping-plan.md) ["+impactMentionDetail+"]") {
		t.Fatalf("doc mention is not labelled:\n%s", text)
	}
	if strings.Contains(text, "DESIGN.md") {
		t.Fatalf("phantom transitive doc caller present:\n%s", text)
	}
}

// TestImpactDocMentionsDoNotCrowdOutRealCallersUnderLimit is the cost the reporter named:
// on a well-documented symbol the mentions consume the whole --limit.
func TestImpactDocMentionsDoNotCrowdOutRealCallersUnderLimit(t *testing.T) {
	t.Parallel()
	snapshot := sem.ProviderSnapshot{
		Files: []sem.FileRecord{{ID: "f-src", Path: "src/target.ts", Language: "TypeScript"}},
		Symbols: []sem.SymbolRecord{
			{ID: "focus", Name: "Target", QualifiedName: "Target", Kind: "function",
				FilePath: "src/target.ts", StartLine: 1, EndLine: 3, Language: "TypeScript"},
		},
	}
	// Ten documentation mentions sorting ahead of the one real caller by path.
	for index := 0; index < 10; index++ {
		id := string(rune('a' + index))
		snapshot.Files = append(snapshot.Files, sem.FileRecord{
			ID: "f-doc-" + id, Path: "docs/" + id + ".md", Language: "Markdown",
		})
		snapshot.Relations = append(snapshot.Relations, sem.RelationRecord{
			FromID: "f-doc-" + id, ToID: "focus", Type: "CALLS", Resolution: "name_only",
		})
	}
	snapshot.Files = append(snapshot.Files,
		sem.FileRecord{ID: "f-zz", Path: "src/zz.ts", Language: "TypeScript"})
	snapshot.Symbols = append(snapshot.Symbols, sem.SymbolRecord{
		ID: "real", Name: "ZzCaller", QualifiedName: "ZzCaller", Kind: "function",
		FilePath: "src/zz.ts", StartLine: 1, EndLine: 3, Language: "TypeScript",
	})
	snapshot.Relations = append(snapshot.Relations, sem.RelationRecord{
		FromID: "real", ToID: "focus", Type: "CALLS", Resolution: "import_resolved",
	})

	response := buildImpactResponse(snapshot, impactFlags{Symbol: "Target", Depth: 2, Limit: 3})
	if len(response.Callers.Entries) != 3 {
		t.Fatalf("entries = %#v", response.Callers.Entries)
	}
	if response.Callers.Entries[0].Endpoint.ID != "real" {
		t.Fatalf("the real caller did not survive the cap: %#v", response.Callers.Entries)
	}
}

func TestImpactNonCodeMention(t *testing.T) {
	t.Parallel()
	languages := map[string]string{
		"md":    "Markdown",
		"go":    "Go",
		"cobol": "COBOL",
	}
	cases := []struct {
		name       string
		resolution string
		path       string
		id         string
		want       bool
	}{
		{name: "name_only on markdown", resolution: "name_only", path: "DESIGN.md", id: "md", want: true},
		{name: "name_only in a docs tree", resolution: "name_only", path: "docs/api.txt", id: "md", want: true},
		{name: "name_only on a recorded fixture", resolution: "name_only", path: "testdata/case.json", id: "md", want: true},
		{name: "resolved on markdown is not a mention", resolution: "import_resolved", path: "DESIGN.md", id: "md", want: false},
		{name: "name_only between source files is a real edge", resolution: "name_only", path: "src/a.go", id: "go", want: false},
		{name: "name_only on an inventory-only language", resolution: "name_only", path: "src/legacy.cbl", id: "cobol", want: true},
		{name: "empty resolution is not a mention", resolution: "", path: "DESIGN.md", id: "md", want: false},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := impactNonCodeMention(
				sem.RelationRecord{Resolution: testCase.resolution},
				neighborEndpoint{ID: testCase.id, FilePath: testCase.path},
				languages,
			)
			if got != testCase.want {
				t.Fatalf("impactNonCodeMention = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestNonProgramTextPathAndInventoryOnlyLanguage(t *testing.T) {
	t.Parallel()
	nonProgram := []string{
		"DESIGN.md", "docs/guide.rst", "CHANGELOG.md", "README", "docs/api.txt",
		"fixtures/a.json", "config/app.yaml", "settings.toml", "data/rows.csv",
		"package-lock.json", "go.sum",
		// Generated PROSE and generated DATA still hold no program text.
		"docs/generated/api.md", "internal/gen/schema.json",
	}
	for _, path := range nonProgram {
		if !sem.NonProgramTextPath(path) {
			t.Fatalf("%q should be non-program text", path)
		}
	}
	program := []string{
		"src/a.go", "internal/sem/search.go", "src/Combobox.tsx",
		// Generated CODE can break when behavior changes, so it must stay a real caller.
		"internal/gen/client.go", "dist/bundle.js", "api/v1_generated.go",
		"src/generated/types.ts", "build/main.c",
	}
	for _, path := range program {
		if sem.NonProgramTextPath(path) {
			t.Fatalf("%q should be program text", path)
		}
	}
	if !sem.InventoryOnlyLanguage("COBOL") {
		t.Fatalf("COBOL should be inventory-only")
	}
	for _, language := range []string{"Go", "TypeScript", "Markdown", ""} {
		if sem.InventoryOnlyLanguage(language) {
			t.Fatalf("%q should not be inventory-only", language)
		}
	}
}
