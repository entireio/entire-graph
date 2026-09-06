package gate

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// reviewOrderLimit is how many entities the review order names before it
// summarises the rest. An agent-written change set is routinely dozens of
// entities; naming all of them reproduces the problem Gate exists to solve.
const reviewOrderLimit = 5

// ReviewOrder ranks changed entities by how much they need a human's eyes:
// dependents descending, ties broken by unchecked first.
//
// Deliberately not dependents multiplied by uncheckedness. Uncheckedness is
// binary, so the product zeroes every verified entity and would sort a
// 9-dependent verified change below a 1-dependent unchecked one. Dependents are
// the risk; coverage breaks ties.
func ReviewOrder(entities []ChangedEntity) []ChangedEntity {
	ordered := make([]ChangedEntity, len(entities))
	copy(ordered, entities)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Dependents != b.Dependents {
			return a.Dependents > b.Dependents
		}
		if (a.Coverage == Unchecked) != (b.Coverage == Unchecked) {
			return a.Coverage == Unchecked
		}
		return a.Path < b.Path
	})
	return ordered
}

// WriteJSON emits the machine-readable report.
func WriteJSON(w io.Writer, report Report) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

// WriteText renders the human report: verdict first, then what to read, then
// the findings grouped by dimension, then the rule that produced the verdict.
func WriteText(w io.Writer, report Report, all bool) {
	fmt.Fprintf(w, "VERDICT  %s\n", report.Verdict)
	fmt.Fprintf(w, "         %s\n", Summarise(report.Entities))
	if note := DegradationNote(report.Available); note != "" {
		fmt.Fprintf(w, "         %s\n", note)
	}
	fmt.Fprintf(w, "         %s..%s\n", short(report.Base), short(report.Head))
	if report.Checkpoint != "" {
		fmt.Fprintf(w, "         checkpoint %s\n", report.Checkpoint)
	}

	writeReviewOrder(w, report.Entities, all)
	writeFindings(w, report.Findings)

	if report.VerifyCommand != "" {
		fmt.Fprintf(w, "\nVERIFY\n  %s\n", report.VerifyCommand)
	}
	writeWarnings(w, report.Warnings)

	fmt.Fprintf(w, "\n%s\n", Rule)
	fmt.Fprintf(w, "\nexit %d\n", report.Verdict.ExitCode())
}

// writeWarnings prints one line per warning, except that a code repeated across
// files is collapsed to a count. Five identical E_PARSE_ERROR lines about
// vendored grammar headers bury the one warning that is about the change under
// review.
func writeWarnings(w io.Writer, warnings []string) {
	seen := map[string]int{}
	var order []string
	for _, warning := range warnings {
		code, _, _ := strings.Cut(warning, " ")
		if seen[code] == 0 {
			order = append(order, warning)
		}
		seen[code]++
	}
	for _, warning := range order {
		code, _, _ := strings.Cut(warning, " ")
		fmt.Fprintf(w, "\nWARNING  %s", warning)
		if n := seen[code]; n > 1 {
			fmt.Fprintf(w, " (and %d more %s)", n-1, code)
		}
		fmt.Fprintln(w)
	}
}

func writeReviewOrder(w io.Writer, entities []ChangedEntity, all bool) {
	if len(entities) == 0 {
		return
	}
	ordered := ReviewOrder(entities)
	shown := ordered
	if !all && len(shown) > reviewOrderLimit {
		shown = shown[:reviewOrderLimit]
	}

	fmt.Fprintf(w, "\nREVIEW ORDER — %d entities changed, read these %d first\n\n",
		len(entities), len(shown))
	for i, e := range shown {
		fmt.Fprintf(w, " %d. %-24s @ %s:%d\n", i+1, e.Name, e.Path, e.Line)
		fmt.Fprintf(w, "    %s · %d dependents · %s%s\n",
			e.ChangeType, e.Dependents, e.Coverage, coveringTestSuffix(e))
	}

	if rest := ordered[len(shown):]; len(rest) > 0 {
		// Saying why the remainder does not need eyes is what makes the cut
		// trustworthy: Gate is not hiding them, it is accounting for them.
		fmt.Fprintf(w, "\n The remaining %d entities: %s\n", len(rest), remainderReason(rest))
		fmt.Fprintln(w, " Full list: --all")
	}
}

func writeFindings(w io.Writer, findings []Finding) {
	byDimension := map[Dimension][]Finding{}
	for _, f := range findings {
		byDimension[f.Dimension] = append(byDimension[f.Dimension], f)
	}
	for _, dim := range []Dimension{DimRisk, DimCoverage, DimCompanions, DimClones} {
		group := byDimension[dim]
		if len(group) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s\n", sectionTitle(dim))
		for _, f := range group {
			fmt.Fprintf(w, "  %s @ %s:%d\n", f.Subject.Name, f.Subject.Path, f.Subject.Line)
			fmt.Fprintf(w, "    %s\n", f.Summary)
			for _, e := range f.Evidence {
				fmt.Fprintf(w, "      - %s\n", e)
			}
		}
	}
}

func sectionTitle(dim Dimension) string {
	switch dim {
	case DimRisk:
		return "RISK — breaking changes with dependents"
	case DimCoverage:
		return "COVERAGE — changed and unchecked"
	case DimCompanions:
		return "COMPANION GAP — habitually changed together, not this time"
	case DimClones:
		return "CLONE DRIFT — near-duplicate siblings left behind"
	}
	return string(dim)
}

func coveringTestSuffix(e ChangedEntity) string {
	if len(e.CoveringTests) == 0 {
		return ""
	}
	return " (" + e.CoveringTests[0] + ")"
}

func remainderReason(rest []ChangedEntity) string {
	var unchecked int
	for _, e := range rest {
		if e.Coverage == Unchecked {
			unchecked++
		}
	}
	if unchecked == 0 {
		return "no dependents and no findings"
	}
	return fmt.Sprintf("%d still unchecked, none with dependents", unchecked)
}

func short(ref string) string {
	const shortSHALen = 12
	if len(ref) > shortSHALen {
		return ref[:shortSHALen]
	}
	return ref
}
