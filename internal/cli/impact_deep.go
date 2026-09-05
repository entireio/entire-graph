package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

// impactTextLines commits whole lines only, reserving the omission notice
// before accepting entries. Oversized paths never accumulate unbounded text.
type impactTextLines struct {
	body, line strings.Builder
	budget     int
	marker     string
	truncated  bool
}

func (w *impactTextLines) Write(p []byte) (int, error) {
	n := len(p)
	if w.truncated {
		return n, nil
	}
	for len(p) > 0 {
		length := len(p)
		if i := strings.IndexByte(string(p), '\n'); i >= 0 {
			length = i + 1
		}
		if w.budget > 0 && w.body.Len()+w.line.Len()+length+len(w.marker) > w.budget {
			w.truncated = true
			w.line.Reset()
			return n, nil
		}
		w.line.Write(p[:length])
		if p[length-1] == '\n' {
			w.body.WriteString(w.line.String())
			w.line.Reset()
		}
		p = p[length:]
	}
	return n, nil
}
func (w *impactTextLines) finish(out io.Writer) error {
	text := w.body.String()
	if w.truncated {
		text += w.marker
	}
	if w.budget > 0 && len(text) > w.budget {
		text = text[:w.budget]
	}
	_, err := io.WriteString(out, text)
	return err
}

func writeDeepImpact(out io.Writer, response impactResponse, symbols []sem.SymbolRecord, budget int) error {
	report := response.Traversal
	marker := "!output-truncated; use --format json\n"
	if report.Truncated {
		marker = "!traversal-truncated; !output-truncated; use --format json\n"
	}
	if budget > 0 && len(marker) > budget {
		marker = "!truncated\n"
	}
	writer := &impactTextLines{budget: budget, marker: marker}
	names := map[string]string{}
	for _, symbol := range symbols {
		names[symbol.ID] = fmt.Sprintf("%s (%s:%d)", termsafe.Line(symbol.Name), termsafe.Line(symbol.FilePath), symbol.StartLine)
	}
	label := func(id string) string {
		if name := names[id]; name != "" {
			return name
		}
		return termsafe.Line(id)
	}
	depth := fmt.Sprint(response.Depth)
	if response.Depth == 0 {
		depth = "all (bounded)"
	}
	fmt.Fprintf(writer, "Impact %s; depth=%s; policy=%s\n", label(response.Focus.ID), depth, report.Policy)
	fmt.Fprintf(writer, "Discovered %d affected nodes; examined %d edges; counts_lower_bounds=%t\n", len(report.Results), report.ExaminedEdges, report.CountsLowerBounds)
	if report.PathAlternativesOmitted > 0 {
		fmt.Fprintf(writer, "Omitted %d discovered path alternatives\n", report.PathAlternativesOmitted)
	}
	if response.Compiler != nil {
		fmt.Fprintf(writer, "Compiler: %s; candidate paths are possible implementations, not observed runtime calls\n", response.Compiler.Report.Status)
	}
	if report.Truncated {
		fmt.Fprintf(writer, "!traversal-truncated: %s\n", strings.Join(report.StopReasons, ","))
	}
	for _, result := range report.Results {
		if writer.truncated {
			break
		}
		for index, path := range result.Paths {
			if writer.truncated {
				break
			}
			fmt.Fprintf(writer, "- %s; path=%d; category=%s; min_edge_confidence=%.2f: %s", label(result.ID), index+1, path.Category, path.WeakestConfidence, label(response.Focus.ID))
			for _, step := range path.Steps {
				if writer.truncated {
					break
				}
				target := step.ToID
				if step.Direction == "in" {
					target = step.FromID
				}
				fmt.Fprintf(writer, " --%s/%s--> %s", step.Relation, step.Direction, label(target))
			}
			fmt.Fprintln(writer)
		}
	}
	// Keep the existing contextual sections distinct from propagated paths.
	writeImpactSection(writer, "Callees (direct context)", response.Callees, false)
	writeImpactSection(writer, "Type consumers (direct context)", response.TypeConsumers, true)
	writeImpactSection(writer, "Data flows (direct context)", response.DataFlows, true)
	writeImpactSection(writer, "Historical co-change (not structural traversal)", response.CoChanges, false)
	writeImpactSection(writer, "Siblings (container context only)", response.Siblings, false)
	return writer.finish(out)
}
