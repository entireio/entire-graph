package cli

import (
	"bytes"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// This exercises the actual outer payload, not just the block fitter. A raw
// string's file line 6 spells exactly the renderer-owned gap for omitted file
// lines 4 and 5. No suffix blocks are supplied, so the one ranked block is the
// final part of the payload and can use the existing header-recovery test helper.
func TestReview303GapPayloadDisclosureAndCoordinates(t *testing.T) {
	const (
		path        = "src/gap_payload.go"
		declaration = "func payloadProbe() {"
		gap         = "... 2 lines elided"
		neighbor    = "gap payload neighbor"
		notice      = "UNTRUSTED FILE CONTENT:"
	)
	for _, tc := range []struct {
		name    string
		marker  string
		rewrite bool
	}{
		{name: "literal_gap_source", marker: gap, rewrite: true},
		{name: "ordinary_source_control", marker: "ordinary gap-like source"},
		{name: "already_indented_source_control", marker: " " + gap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Authored file lines: declaration 3; long assignment 4; long raw
			// string opening 5; literal marker 6; neighbor 7; closes 8 and 9.
			lines := []string{
				declaration,
				"\t_ = 0 // " + strings.Repeat("x", 2000),
				"\t_ = `" + strings.Repeat("x", 2000),
				tc.marker,
				neighbor,
				"`",
				"}",
			}
			snippet := strings.Join(lines, "\n") + "\n"
			if _, err := parser.ParseFile(token.NewFileSet(), path, "package fixture\n\n"+snippet, parser.AllErrors); err != nil {
				t.Fatalf("fixture must be valid Go: %v", err)
			}
			response := sem.SearchResponse{
				Query: "gap marker payload",
				Results: []sem.SearchResult{{
					Rank: 1, Score: 40, FilePath: path, Language: "go",
					StartLine: 3, EndLine: 9, FocusLine: 6,
					SnippetStartLine: 3, SnippetEndLine: 9,
					SymbolStartLine: 3, SymbolEndLine: 9, SymbolNameLine: 3,
					SymbolName: "payloadProbe", QualifiedName: "payloadProbe",
					Signals: []string{"body"}, Snippet: snippet,
				}},
			}
			printedMarker := tc.marker
			if tc.rewrite {
				printedMarker = " " + tc.marker
			}
			expected := append([]string(nil), lines...)
			expected[3] = printedMarker

			budgets := []int{1, 32, 64, 128, 256, 384, 512, 768, 1024, 8192}
			if tc.rewrite {
				// One-byte steps cover notice/no-notice transitions. Controls
				// use selected caps; this is not a whole-renderer fuzz sweep.
				budgets = nil
				for budget := 1; budget <= 512; budget++ {
					budgets = append(budgets, budget)
				}
				budgets = append(budgets, 768, 1024, 8192)
			}
			retained, omitted := 0, 0
			for _, budget := range budgets {
				var buf bytes.Buffer
				if err := writeAgentSearch(&buf, response, budget); err != nil {
					t.Fatalf("budget %d: render: %v", budget, err)
				}
				payload := buf.String()
				if len(payload) > budget {
					t.Fatalf("budget %d: final payload over cap: %d bytes\n%s", budget, len(payload), payload)
				}
				hasMarker := strings.Contains(payload, "\n"+printedMarker+"\n")
				if hasMarker {
					retained++
					if tc.rewrite && !strings.HasPrefix(payload, notice) {
						t.Errorf("budget %d: rewritten literal survives without mandatory leading disclosure:\n%s", budget, payload)
					}
				} else {
					omitted++
				}
				if !tc.rewrite && strings.Contains(payload, notice) {
					t.Errorf("budget %d: ordinary/already-indented source or a renderer-owned gap caused a false disclosure:\n%s", budget, payload)
				}

				parts := strings.Split(payload, "\n")
				headerAt := -1
				for i, line := range parts {
					if strings.HasPrefix(line, "1. "+path+":") || strings.HasPrefix(line, path+":") {
						headerAt = i
						break
					}
				}
				markerLine, neighborLine, declarationLine, gapRecords := -1, -1, -1, 0
				if headerAt >= 0 {
					block := []byte(strings.Join(parts[headerAt:], "\n"))
					numbers, sources, last, ok := declRecoverLineNumbers(block)
					if !ok {
						t.Fatalf("budget %d: actual payload header cannot recover source coordinates:\n%s", budget, payload)
					}
					for i, source := range sources {
						line := numbers[i]
						if line < 3 || line > 9 || source != expected[line-3] {
							t.Errorf("budget %d: delivered source %q recovers to wrong file line %d:\n%s", budget, source, line, payload)
						}
						switch source {
						case printedMarker:
							markerLine = line
						case neighbor:
							neighborLine = line
						case declaration:
							declarationLine = line
						}
					}
					if last >= 0 && len(numbers) > 0 && last != numbers[len(numbers)-1] {
						t.Errorf("budget %d: range end %d differs from last delivered source line %d", budget, last, numbers[len(numbers)-1])
					}
					for i, line := range parts[headerAt+1:] {
						if line == gap {
							gapRecords++
							if i == 0 {
								t.Errorf("budget %d: gap-shaped literal appears before any printed source as tool metadata:\n%s", budget, payload)
							}
						}
						if line == "  "+gap {
							t.Errorf("budget %d: literal marker was indented twice:\n%s", budget, payload)
						}
					}
					if gapRecords > 1 {
						t.Errorf("budget %d: source and renderer gaps are still byte-indistinguishable:\n%s", budget, payload)
					}
				}
				if hasMarker && markerLine != 6 {
					t.Errorf("budget %d: retained literal marker recovers to %d, want file line 6:\n%s", budget, markerLine, payload)
				}
				if strings.Contains(payload, "\n"+neighbor+"\n") && neighborLine != 7 {
					t.Errorf("budget %d: retained neighbor recovers to %d, want file line 7:\n%s", budget, neighborLine, payload)
				}
				if budget == 1024 || budget == 8192 {
					if budget == 1024 {
						t.Logf("adequate cap %d: %d bytes\n%s", budget, len(payload), payload)
					} else {
						t.Logf("adequate cap %d: %d bytes; marker/neighbor/declaration=%d/%d/%d", budget, len(payload), markerLine, neighborLine, declarationLine)
					}
					if !hasMarker || markerLine != 6 || neighborLine != 7 || declarationLine != 3 {
						t.Errorf("budget %d: nonvacuity failed: marker/neighbor/declaration must survive at 6/7/3, got %d/%d/%d", budget, markerLine, neighborLine, declarationLine)
					}
					if budget == 1024 && gapRecords != 1 {
						t.Errorf("adequate bounded payload must retain the actual renderer-owned two-line gap, got %d:\n%s", gapRecords, payload)
					}
				}
			}
			if retained == 0 || omitted == 0 {
				t.Fatalf("fixture did not exercise retained and omitted literal paths: retained=%d omitted=%d", retained, omitted)
			}
		})
	}
}
