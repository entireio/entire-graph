package sem

import (
	"strings"
	"testing"
)

// A copied typedef entity may change its Name, but a positive nameLine must
// still name that alias. These expectations come from the literal C source,
// not declarationNameLine or any other production name-finding helper.
func TestDeclarationNameLineIndependentReviewTypedefAliasCoordinates(t *testing.T) {
	cases := []struct {
		name         string
		source       string
		wantLines    map[string]int
		requireKnown bool
	}{
		{
			name:         "single_name_positive_control",
			source:       "typedef int SingleAlias;\n",
			wantLines:    map[string]int{"SingleAlias": 1},
			requireKnown: true,
		},
		{
			name:      "separate_line_aliases_do_not_inherit_another_names_line",
			source:    "typedef int\n    FirstAlias,\n    SecondAlias;\n",
			wantLines: map[string]int{"FirstAlias": 2, "SecondAlias": 3},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entities, language := (TreeSitterParser{}).Parse("aliases.c", tc.source)
			if language != "C" {
				t.Fatalf("language = %q, want C", language)
			}
			seen := make(map[string]int)
			known := 0
			for _, entity := range entities {
				wantLine, wanted := tc.wantLines[entity.Name]
				if !wanted || entity.Kind != "type" {
					continue
				}
				seen[entity.Name]++
				if entity.nameLine == 0 {
					continue // Unknown is permitted; false positive provenance is not.
				}
				known++
				if entity.nameLine != wantLine {
					t.Errorf("type %q nameLine = %d, want unknown (0) or its own alias token on line %d", entity.Name, entity.nameLine, wantLine)
				}
			}
			for name := range tc.wantLines {
				if seen[name] != 1 {
					t.Errorf("type %q emitted %d times, want exactly once; entities = %#v", name, seen[name], entities)
				}
			}
			if tc.requireKnown && known == 0 {
				t.Error("single-name positive control produced no known name line")
			}
		})
	}
}

// An enclosure can name a different symbol from the ranked hit. Every return
// path must keep a nonzero name line bound to the identity it actually returns,
// including window and focus-elided early returns. Retaining the inner identity
// or replacing it with the outer identity are both acceptable if coherent.
func TestDeclarationNameLineIndependentReviewEnclosureIdentityBinding(t *testing.T) {
	lines := []string{
		"def outer():",         // 1: only outer's declaration identifier.
		"    value = 1",        // 2
		"    def inner():",     // 3: only inner's declaration identifier.
		"        return value", // 4: ranked focus.
		"    return inner()",   // 5: a use, not a declaration.
	}
	outer := SymbolRecord{
		RecordType: "symbol", ID: "outer-id", Kind: "function", Name: "outer",
		QualifiedName: "outer", FilePath: "nested.py", Language: "Python",
		StartLine: 1, EndLine: 5, Signature: "def outer():", nameLine: 1,
	}
	inner := SymbolRecord{
		RecordType: "symbol", ID: "inner-id", Kind: "function", Name: "inner",
		QualifiedName: "outer.inner", FilePath: "nested.py", Language: "Python",
		StartLine: 3, EndLine: 4, Signature: "def inner():", nameLine: 3,
	}
	cases := []struct {
		name         string
		enclosure    searchEnclosure
		wantStart    int
		wantEnd      int
		requireKnown bool
	}{
		{
			name:         "whole_enclosure_positive_control",
			enclosure:    searchEnclosure{start: 1, end: 5, lines: lines, symbol: outer},
			wantStart:    1,
			wantEnd:      5,
			requireKnown: true,
		},
		{
			name:         "same_symbol_window_positive_control",
			enclosure:    searchEnclosure{start: 3, end: 4, lines: lines, symbol: inner, window: true},
			wantStart:    3,
			wantEnd:      4,
			requireKnown: true,
		},
		{
			name:      "different_symbol_window_keeps_name_line_bound_to_identity",
			enclosure: searchEnclosure{start: 2, end: 5, lines: lines, symbol: outer, window: true},
			wantStart: 2,
			wantEnd:   5,
		},
		{
			name:      "focus_elided_return_keeps_name_line_bound_to_identity",
			enclosure: searchEnclosure{start: 1, end: 2, lines: lines, symbol: outer},
			wantStart: 1,
			wantEnd:   2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := SearchResult{
				Rank: 1, FilePath: "nested.py", Language: "Python", Kind: "function",
				StartLine: 3, EndLine: 4, FocusLine: 4,
				SnippetStartLine: 3, SnippetEndLine: 4, Snippet: strings.Join(lines[2:4], "\n"),
				SymbolID: "inner-id", SymbolName: "inner", QualifiedName: "outer.inner",
				Signature: "def inner():", SymbolStartLine: 3, SymbolEndLine: 4, SymbolNameLine: 3,
				Signals: []string{"body"},
			}
			got := widenSearchResultToEnclosure(result, tc.enclosure)
			if got.SnippetStartLine != tc.wantStart || got.SnippetEndLine != tc.wantEnd {
				t.Fatalf("snippet span = %d-%d, want exercised enclosure %d-%d", got.SnippetStartLine, got.SnippetEndLine, tc.wantStart, tc.wantEnd)
			}
			if got.SymbolNameLine == 0 {
				if tc.requireKnown {
					t.Error("positive control lost the known declaration name line")
				}
				return // Withholding unknown metadata is a valid fail-closed choice.
			}
			wantLine := 0
			switch {
			case got.SymbolID == "inner-id" && got.SymbolName == "inner":
				wantLine = 3
			case got.SymbolID == "outer-id" && got.SymbolName == "outer":
				wantLine = 1
			default:
				t.Fatalf("name line %d has an incoherent identity: id=%q name=%q", got.SymbolNameLine, got.SymbolID, got.SymbolName)
			}
			if got.SymbolNameLine != wantLine {
				t.Errorf("id=%q name=%q reports name line %d, but its declaration identifier is on line %d", got.SymbolID, got.SymbolName, got.SymbolNameLine, wantLine)
			}
		})
	}
}
