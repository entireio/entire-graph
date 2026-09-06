package sem

import (
	"context"
	"regexp"
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
	quoted := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		if pattern != "" {
			quoted = append(quoted, regexp.QuoteMeta(pattern))
		}
	}
	if len(quoted) == 0 {
		return nil, 0, nil
	}
	matcher, err := regexp.Compile("(?i)(?:" + strings.Join(quoted, "|") + ")")
	if err != nil {
		return nil, 0, err
	}
	matcher.Longest()
	if maxLines <= 0 {
		maxLines = 32
	}
	allowed := make(map[string]bool, len(source.paths))
	for _, path := range source.paths {
		allowed[path] = true
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
				if !evidence.binary {
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
		// Git's automatic binary sniff uses the first 8000 bytes. The scorer later
		// rejects any NUL-containing source independently.
		if strings.IndexByte(content[:min(len(content), 8000)], 0) >= 0 {
			continue
		}
		seen := make(map[string]bool, len(patterns))
		matchingLines := 0
		for remaining := content; remaining != "" && matchingLines < maxLines; {
			line, rest, _ := strings.Cut(remaining, "\n")
			remaining = rest
			matched := false
			for offset := 0; offset < len(line); {
				location := matcher.FindStringIndex(line[offset:])
				if location == nil {
					break
				}
				matched = true
				text := line[offset+location[0] : offset+location[1]]
				key := strings.ToLower(text)
				if !seen[key] {
					matches = append(matches, gitutil.GrepMatch{Path: path, Text: text})
					seen[key] = true
				}
				offset += location[1]
			}
			if matched {
				matchingLines++
			}
		}
	}
	return matches, reads, nil
}
