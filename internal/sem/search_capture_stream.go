package sem

import (
	"bytes"
	"context"
	"io"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// captureMatchStream observes a descriptor without retaining the file. The
// caller reduces emitted substrings to query-term presence, so storage is
// independent of source length and the number of matches on one long line.
type captureMatchStream struct {
	ctx             context.Context
	matcher         *regexp.Regexp
	emit            func(string)
	window          int
	buffer          string
	lines, maxLines int
	lineMatched     bool
	sniffed         int
	binary          bool
}

func newCaptureMatchStream(ctx context.Context, patterns []string, maxLines int, emit func(string)) (*captureMatchStream, error) {
	quoted := make([]string, 0, len(patterns))
	window := 1
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		quoted = append(quoted, regexp.QuoteMeta(pattern))
		// Case folding can change UTF-8 width. Four bytes per rune bounds
		// every spelling accepted by the fixed-string regular expression.
		window = max(window, utf8.RuneCountInString(pattern)*utf8.UTFMax)
	}
	matcher, err := regexp.Compile("(?i)(?:" + strings.Join(quoted, "|") + ")")
	if err != nil {
		return nil, err
	}
	matcher.Longest()
	if maxLines <= 0 {
		maxLines = 32
	}
	stream := &captureMatchStream{ctx: ctx, matcher: matcher, emit: emit, window: window, maxLines: maxLines}
	if len(quoted) == 0 {
		stream.lines = maxLines
	}
	return stream, nil
}

func (stream *captureMatchStream) Write(data []byte) (int, error) {
	count := len(data)
	if stream.sniffed < 8000 {
		prefix := data[:min(len(data), 8000-stream.sniffed)]
		stream.binary = stream.binary || bytes.IndexByte(prefix, 0) >= 0
		stream.sniffed += len(prefix)
	}
	for len(data) > 0 {
		if err := stream.ctx.Err(); err != nil {
			return count - len(data), err
		}
		if stream.lines >= stream.maxLines {
			return count, nil
		}
		part := data[:min(len(data), 32<<10)]
		data = data[len(part):]
		for len(part) > 0 {
			end := bytes.IndexByte(part, '\n')
			if end < 0 {
				stream.buffer += string(part)
				stream.drain(false)
				break
			}
			stream.buffer += string(part[:end])
			stream.drain(true)
			if stream.lineMatched {
				stream.lines++
			}
			stream.lineMatched = false
			part = part[end+1:]
			if stream.lines >= stream.maxLines {
				stream.buffer = ""
				break
			}
		}
	}
	return count, nil
}

func (stream *captureMatchStream) drain(final bool) {
	for len(stream.buffer) > 0 {
		safe := len(stream.buffer) - stream.window
		if !final && safe < 0 {
			return
		}
		match := stream.matcher.FindStringIndex(stream.buffer)
		if match == nil {
			if final {
				stream.buffer = ""
			} else {
				stream.buffer = stream.buffer[safe:]
			}
			return
		}
		if !final && match[0] > safe {
			stream.buffer = stream.buffer[match[0]:]
			return
		}
		stream.lineMatched = true
		stream.emit(stream.buffer[match[0]:match[1]])
		stream.buffer = stream.buffer[match[1]:]
	}
}

func (stream *captureMatchStream) finish() error {
	if err := stream.ctx.Err(); err != nil {
		return err
	}
	if stream.lines < stream.maxLines {
		stream.drain(true)
	}
	return nil
}

type capturedMatchEvidence struct {
	terms  []string
	binary bool
	err    error
}

// The observer retains at most one bit per query term per acquired file. In
// particular, arbitrary case variants on an oversized line cannot grow memory.
type capturePreselectionObserver struct {
	enabled   bool
	mu        sync.Mutex
	prototype captureMatchStream
	terms     []string
	matcher   searchTermMatcher
	records   map[string]capturedMatchEvidence
}

func newCapturePreselectionObserver(ctx context.Context, patterns, terms []string) (*capturePreselectionObserver, error) {
	stream, err := newCaptureMatchStream(ctx, patterns, 32, nil)
	if err != nil {
		return nil, err
	}
	return &capturePreselectionObserver{prototype: *stream, terms: append([]string(nil), terms...), matcher: newSearchTermMatcher(terms), records: map[string]capturedMatchEvidence{}}, nil
}

func (observer *capturePreselectionObserver) factory(path string) (io.Writer, func(error)) {
	observer.mu.Lock()
	enabled := observer.enabled
	observer.mu.Unlock()
	if !enabled {
		return nil, nil
	}
	stream := observer.prototype
	seen := make([]bool, len(observer.terms))
	stream.emit = func(text string) {
		for index, matched := range observer.matcher.match(text) {
			seen[index] = seen[index] || matched
		}
	}
	return &stream, func(err error) {
		if err == nil {
			err = stream.finish()
		}
		evidence := capturedMatchEvidence{binary: stream.binary, err: err}
		for index, matched := range seen {
			if matched {
				evidence.terms = append(evidence.terms, observer.terms[index])
			}
		}
		observer.mu.Lock()
		observer.records[path] = evidence
		observer.mu.Unlock()
	}
}

func (observer *capturePreselectionObserver) evidence(path string) (capturedMatchEvidence, bool) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	evidence, ok := observer.records[path]
	return evidence, ok
}

func (observer *capturePreselectionObserver) activate() {
	observer.mu.Lock()
	observer.enabled = true
	observer.mu.Unlock()
}
