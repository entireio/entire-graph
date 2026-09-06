package sem

import (
	"context"
	"strings"

	"github.com/entireio/entire-graph/internal/gitutil"
)

// capturedPreselectionMatches samples fixed-string matches from the operation's
// reader. It never asks Git to reopen source content. Like Git's -m option, the
// budget counts matching lines, not substrings; -o selects nonoverlapping,
// leftmost-longest matches. Only distinct matched strings need be retained for
// the downstream term-presence reduction.
func capturedPreselectionMatches(ctx context.Context, source sourceContext, tracked, patterns []string, maxLines int, observers ...*capturePreselectionObserver) ([]gitutil.GrepMatch, int, error) {
	if len(observers) > 0 && observers[0] != nil {
		observers[0].activate()
	}
	prototype, err := newCaptureMatchStream(ctx, patterns, maxLines, nil)
	if err != nil {
		return nil, 0, err
	}
	terms := make([]string, len(patterns))
	for index, pattern := range patterns {
		terms[index] = strings.ToLower(pattern)
	}
	if len(observers) > 0 && observers[0] != nil {
		terms = observers[0].terms
	}
	termMatcher := newSearchTermMatcher(terms)
	allowed := make(map[string]bool, len(source.paths))
	for _, path := range source.paths {
		allowed[path] = true
	}
	eligible := make([]string, 0, len(tracked))
	for _, path := range tracked {
		if allowed[path] {
			eligible = append(eligible, path)
		}
	}
	attributes, err := source.capturedDiffAttributes(ctx, eligible)
	if err != nil {
		return nil, 0, err
	}
	matches := make([]gitutil.GrepMatch, 0)
	reads := 0
	for _, path := range tracked {
		if err := ctx.Err(); err != nil {
			return nil, reads, err
		}
		if !allowed[path] {
			continue
		}
		content, ok := source.read(path)
		if len(observers) > 0 && observers[0] != nil {
			if evidence, observed := observers[0].evidence(path); observed {
				if evidence.err != nil {
					return nil, reads, evidence.err
				}
				reads++
				attribute := attributes[path]
				if !attribute.Binary && (attribute.Text || !evidence.binary) {
					if evidence.matched && len(evidence.terms) == 0 {
						matches = append(matches, gitutil.GrepMatch{Path: path})
					}
					for _, term := range evidence.terms {
						matches = append(matches, gitutil.GrepMatch{Path: path, Text: term})
					}
				}
				continue
			}
		}
		if !ok {
			continue
		}
		reads++
		stream := *prototype
		seen := make([]bool, len(terms))
		stream.emit = func(text string) {
			for index, matched := range termMatcher.match(text) {
				seen[index] = seen[index] || matched
			}
		}
		if _, err := stream.Write([]byte(content)); err != nil {
			return nil, reads, err
		}
		if err := stream.finish(); err != nil {
			return nil, reads, err
		}
		attribute := attributes[path]
		if !attribute.Binary && (attribute.Text || !stream.binary) {
			if stream.matched {
				matches = append(matches, gitutil.GrepMatch{Path: path})
			}
			for index, matched := range seen {
				if matched {
					matches = append(matches, gitutil.GrepMatch{Path: path, Text: terms[index]})
				}
			}
		}

	}
	return matches, reads, nil
}
