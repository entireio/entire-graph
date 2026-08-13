package sem

import (
	"fmt"
	"sort"
	"strings"

	"github.com/entireio/entire-graph/internal/termsafe"
)

// FILE OUTLINE — what else is in the files the payload already named.
//
// Why it exists, measured over 79 real agent sessions: 77 targeted single-file greps landed on a file
// the payload had ALREADY named, and 35 of those were immediately followed by a Read of that same
// file — a grep/read pair costing two turns to answer "what else is in here". The reads that follow a
// payload file land a MEDIAN of 109 lines away from anything the payload printed, and 47 of 53 of them
// land inside another symbol the graph already indexes in that same file. The information was in the
// graph and simply was not emitted.
//
// It is an INDEX, never source: one line per symbol, no bodies. A payload that printed a body must not
// pay for a second copy of it, so the symbol the hit is in is marked rather than repeated. At ~45 bytes
// per row against a ~17,000-token turn, thirty rows cost about 2% of the turn they are meant to remove.
const (
	// searchOutlineMaxFiles is how many of the payload's files get an outline. The head is where the
	// edit lands: measured on sonnet, rank 1 was later read 62% of the time and edited 54%, rank 2
	// 27%/20%, and ranks 3+ fell to 0-11%. Three files covers the ranks an agent acts on.
	searchOutlineMaxFiles = 3

	// searchOutlineMaxRowsPerFile caps a single file's outline. A 3,000-line class with 200 members
	// would otherwise spend the whole block on one file.
	searchOutlineMaxRowsPerFile = 30

	// searchOutlineMaxBytes caps the block. Additive and separately capped, exactly like the literal
	// cluster and the verify command, so a payload that spent its budget on complete head bodies can
	// never lose one to buy a navigation aid.
	searchOutlineMaxBytes = 1536
)

// SearchOutlineRow is one symbol in an outlined file.
type SearchOutlineRow struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Kind  string `json:"kind,omitempty"`
	Name  string `json:"name"`
	// Hit marks the symbol that the ranked result itself lies in, so the outline says "you are here"
	// instead of making the agent re-derive it from line numbers.
	Hit bool `json:"hit,omitempty"`
}

// SearchFileOutline is the symbol index of one file the payload named.
type SearchFileOutline struct {
	FilePath string             `json:"file_path"`
	Lines    int                `json:"lines,omitempty"`
	Rows     []SearchOutlineRow `json:"rows"`
	// Omitted is how many symbols did not fit. Truncation is never silent: an outline that claims to
	// be the file's contents while hiding half of it would be worse than no outline.
	Omitted int `json:"omitted,omitempty"`
}

// buildSearchFileOutlines indexes the files of the highest-ranked results. It reads no file content —
// every field comes from symbol records the graph already holds — so it adds no file reads.
func buildSearchFileOutlines(
	results []SearchResult,
	symbolsByFile map[string][]SymbolRecord,
	maxFiles, maxRows, maxBytes int,
) []SearchFileOutline {
	if maxFiles <= 0 || maxRows <= 0 || maxBytes <= 0 {
		return nil
	}
	var outlines []SearchFileOutline
	seen := map[string]bool{}
	spent := 0
	for _, result := range results {
		if len(outlines) >= maxFiles {
			break
		}
		// Only the ranked answer is outlined. A related site or a docs-and-fixtures hit is not where
		// the edit lands, and outlining it would spend the cap on the wrong file.
		if result.Section != "" || seen[result.FilePath] {
			continue
		}
		symbols := symbolsByFile[result.FilePath]
		if len(symbols) <= 1 {
			// A file with one symbol tells the agent nothing it does not already have.
			continue
		}
		seen[result.FilePath] = true
		outline := outlineForFile(result, symbols, maxRows)
		if len(outline.Rows) == 0 {
			continue
		}
		cost := searchOutlineFileCost(outline)
		if spent+cost > maxBytes {
			// Shrink before dropping: half an outline of the rank-1 file beats a full outline of
			// nothing. Rows are already ordered by distance from the hit, so truncating keeps the
			// nearest ones.
			room := maxBytes - spent
			for len(outline.Rows) > 1 && searchOutlineFileCost(outline) > room {
				outline.Rows = outline.Rows[:len(outline.Rows)-1]
				outline.Omitted++
			}
			if searchOutlineFileCost(outline) > room {
				break
			}
			cost = searchOutlineFileCost(outline)
		}
		spent += cost
		outlines = append(outlines, outline)
	}
	return outlines
}

// outlineForFile builds one file's outline, ordered by distance from the hit so that truncation drops
// the least relevant rows and the agent reads the neighbourhood first.
func outlineForFile(result SearchResult, symbols []SymbolRecord, maxRows int) SearchFileOutline {
	type scored struct {
		row  SearchOutlineRow
		dist int
	}
	var rows []scored
	extent := 0
	for _, symbol := range symbols {
		if symbol.StartLine <= 0 || symbol.EndLine < symbol.StartLine || symbol.Name == "" {
			continue
		}
		// Only NAVIGABLE symbols. A struct field is not something an agent greps a file for and not
		// somewhere it can read to; listing each one spends the cap on rows that answer no question.
		// The measured need was "where is the OTHER function/type in this file", so that is what the
		// index carries.
		if !searchOutlineNavigableKind(symbol.Kind) {
			continue
		}
		if symbol.EndLine > extent {
			extent = symbol.EndLine
		}
		hit := result.FocusLine >= symbol.StartLine && result.FocusLine <= symbol.EndLine
		dist := result.FocusLine - symbol.StartLine
		if dist < 0 {
			dist = -dist
		}
		if hit {
			dist = -1
		}
		rows = append(rows, scored{
			row:  SearchOutlineRow{Start: symbol.StartLine, End: symbol.EndLine, Kind: symbol.Kind, Name: symbol.Name, Hit: hit},
			dist: dist,
		})
	}
	sort.SliceStable(rows, func(a, b int) bool { return rows[a].dist < rows[b].dist })
	omitted := 0
	if len(rows) > maxRows {
		omitted = len(rows) - maxRows
		rows = rows[:maxRows]
	}
	// Present in FILE ORDER: an index is read top-to-bottom, and line-ordered rows let an agent size
	// a range read directly. Relevance decided what survives; it does not decide the reading order.
	sort.SliceStable(rows, func(a, b int) bool { return rows[a].row.Start < rows[b].row.Start })
	out := SearchFileOutline{FilePath: result.FilePath, Lines: extent, Omitted: omitted}
	for _, r := range rows {
		out.Rows = append(out.Rows, r.row)
	}
	return out
}

// searchOutlineNavigableKind reports whether a symbol is a place an agent would navigate TO. Fields,
// parameters and variables are excluded: they are attributes of a symbol, not destinations.
func searchOutlineNavigableKind(kind string) bool {
	switch kind {
	case "field", "property", "variable", "parameter", "constant", "enum_member", "enum_constant":
		return false
	default:
		return true
	}
}

func searchOutlineFileCost(outline SearchFileOutline) int {
	return len(RenderSearchFileOutline([]SearchFileOutline{outline}))
}

// SearchFileOutlineCost is the rendered size of the whole block, for stats.
func SearchFileOutlineCost(outlines []SearchFileOutline) int {
	return len(RenderSearchFileOutline(outlines))
}

// RenderSearchFileOutline renders the block. Empty input renders nothing at all — an empty header is
// a claim that the file has no other symbols, which is not what silence here means.
func RenderSearchFileOutline(outlines []SearchFileOutline) []byte {
	if len(outlines) == 0 {
		return nil
	}
	var buffer strings.Builder
	buffer.WriteString(SearchFileOutlineHeader + "\n")
	for _, outline := range outlines {
		// The block is one file per line, so the path is a one-line value: a Git
		// pathname holding a newline would otherwise print as two outline entries.
		fmt.Fprintf(&buffer, "  %s", termsafe.Line(outline.FilePath))
		if outline.Lines > 0 {
			fmt.Fprintf(&buffer, " (%d lines)", outline.Lines)
		}
		if outline.Omitted > 0 {
			fmt.Fprintf(&buffer, " +%d more symbols", outline.Omitted)
		}
		buffer.WriteString("\n")
		for _, row := range outline.Rows {
			marker := " "
			if row.Hit {
				marker = "*"
			}
			fmt.Fprintf(&buffer, "   %s %d-%d", marker, row.Start, row.End)
			if row.Kind != "" {
				fmt.Fprintf(&buffer, " %s", row.Kind)
			}
			fmt.Fprintf(&buffer, " %s\n", row.Name)
		}
	}
	return []byte(buffer.String())
}

// SearchFileOutlineHeader is exported so the guide and its test cannot drift from what is printed —
// the same discipline the literal-cluster block name is under.
const SearchFileOutlineHeader = "FILE OUTLINE (other symbols in the files above; * = the hit; no source, an index)"
