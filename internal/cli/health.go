package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

func runHealth(ctx context.Context, opts Options, args []string) error {
	var indexArgs []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			indexArgs = append(indexArgs, "--format", "json")
		case "--refresh":
			indexArgs = append(indexArgs, "--force")
		case "--head", "--no-network":
			indexArgs = append(indexArgs, args[i])
		case "--repo", "--profile", "--cache-dir", "--format", "--ignore-file", "--include-file":
			value, next, err := searchFlagValue(args, i)
			if err != nil {
				return err
			}
			indexArgs = append(indexArgs, args[i], value)
			i = next
		default:
			return unexpectedArgumentsError("health", opts.Version, []string{args[i]})
		}
	}
	return runIndexCommand(ctx, opts, indexArgs, true)
}

type healthCategory struct {
	Diagnostics     int  `json:"diagnostics"`
	Files           int  `json:"files"`
	IntentionalSkip bool `json:"intentional_skip"`
}

type healthResponse struct {
	indexResponse
	CacheFreshness   string                    `json:"cache_freshness"`
	Categories       map[string]healthCategory `json:"diagnostic_categories"`
	IntentionalSkips []sem.PartialFailure      `json:"intentional_skips"`
	Interpretation   string                    `json:"interpretation"`
}

const healthInterpretation = "Parser diagnostics describe analysis limitations; they do not establish that source code is invalid. Diagnostic locations, when available, are in detail."

func writeHealth(out io.Writer, index indexResponse, text, refresh bool) error {
	r := healthResponse{
		indexResponse:    index,
		CacheFreshness:   "built",
		Categories:       make(map[string]healthCategory),
		IntentionalSkips: []sem.PartialFailure{},
		Interpretation:   healthInterpretation,
	}
	if index.IndexCacheHit {
		r.CacheFreshness = "matching_index"
	} else if refresh {
		r.CacheFreshness = "rebuilt"
	}
	filesByCategory := make(map[string]map[string]bool)
	for _, failure := range index.PartialFailures {
		category := r.Categories[failure.Code]
		category.Diagnostics++
		category.IntentionalSkip = sem.IsIntentionalSkip(failure.Code)
		if filesByCategory[failure.Code] == nil {
			filesByCategory[failure.Code] = make(map[string]bool)
		}
		if failure.FilePath != "" {
			filesByCategory[failure.Code][failure.FilePath] = true
		}
		category.Files = len(filesByCategory[failure.Code])
		r.Categories[failure.Code] = category
		if category.IntentionalSkip {
			r.IntentionalSkips = append(r.IntentionalSkips, failure)
		}
	}
	if !text {
		encoder := json.NewEncoder(termsafe.NewJSONWriter(out))
		encoder.SetEscapeHTML(false)
		return encoder.Encode(r)
	}
	h := r.Completeness.Health
	fmt.Fprintf(out, "Graph health: %s\n", h.Status)
	fmt.Fprintf(out, "  Source files: %d; flagged: %d (%.4f%%); degradation threshold: %d%%\n",
		h.SourceFiles, h.FlaggedFiles, h.FlaggedPercentage, h.ThresholdPercentage)
	fmt.Fprintf(out, "  Revision: %s; tree: %s; profile: %s\n", termsafe.Line(r.Commit), termsafe.Line(r.Tree), termsafe.Line(r.Profile))
	fmt.Fprintf(out, "  Cache freshness: %s (%d ms)\n", r.CacheFreshness, r.IndexLatencyMS)
	fmt.Fprintf(out, "  Intentional source skips: %d\n", h.IntentionalSkippedFiles)
	fmt.Fprintln(out, "Languages (eligible source files):")
	languages := make([]string, 0, len(h.Languages))
	for language := range h.Languages {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	for _, language := range languages {
		c := h.Languages[language]
		fmt.Fprintf(out, "  %s: %d source; %d flagged (%.4f%%); %d intentional skips\n",
			termsafe.Line(language), c.SourceFiles, c.FlaggedFiles, c.FlaggedPercentage, c.IntentionalSkippedFiles)
	}
	fmt.Fprintln(out, "Diagnostic categories:")
	codes := make([]string, 0, len(r.Categories))
	for code := range r.Categories {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		c := r.Categories[code]
		fmt.Fprintf(out, "  %s: %d diagnostics in %d files; intentional skip: %t\n",
			termsafe.Line(code), c.Diagnostics, c.Files, c.IntentionalSkip)
	}
	fmt.Fprintln(out, healthInterpretation)
	fmt.Fprintln(out, "Affected files and diagnostics:")
	for _, failure := range r.PartialFailures {
		if !sem.IsIntentionalSkip(failure.Code) {
			writeHealthDiagnostic(out, failure)
		}
	}
	fmt.Fprintln(out, "Intentional skips:")
	for _, failure := range r.IntentionalSkips {
		writeHealthDiagnostic(out, failure)
	}
	for _, warning := range r.Warnings {
		fmt.Fprintf(out, "  Warning %s %s: %s %s\n", termsafe.Line(warning.Code), termsafe.Line(warning.FilePath), termsafe.Line(warning.EffectOnCompleteness), termsafe.Line(warning.Detail))
	}
	return nil
}

func writeHealthDiagnostic(out io.Writer, failure sem.PartialFailure) {
	fmt.Fprintf(out, "  %s (%s): %s\n", termsafe.Line(failure.FilePath), termsafe.Line(failure.Code), termsafe.Line(failure.EffectOnCompleteness))
	if failure.Detail != "" {
		fmt.Fprintf(out, "    %s\n", termsafe.Line(failure.Detail))
	}
}
