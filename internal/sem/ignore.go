package sem

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

type ignoreMatcher struct {
	rules []ignoreRule
}

type ignoreRule struct {
	negated      bool
	directory    bool
	basenameOnly bool
	expression   *regexp.Regexp
}

func loadWorktreeIgnoreMatcher(repo string, ignoreFiles []string) (ignoreMatcher, error) {
	var matcher ignoreMatcher
	if err := matcher.loadOptional(filepath.Join(repo, ".gitignore")); err != nil {
		return ignoreMatcher{}, err
	}
	for _, ignoreFile := range ignoreFiles {
		resolved := ignoreFile
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(repo, resolved)
		}
		if err := matcher.loadRequired(resolved); err != nil {
			return ignoreMatcher{}, err
		}
	}
	return matcher, nil
}

func (m *ignoreMatcher) loadOptional(file string) error {
	info, err := os.Stat(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read ignore file %q: %w", file, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("ignore file %q is not a regular file", file)
	}
	return m.loadFile(file)
}

func (m *ignoreMatcher) loadRequired(file string) error {
	info, err := os.Stat(file)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("ignore file %q does not exist", file)
	}
	if err != nil {
		return fmt.Errorf("read ignore file %q: %w", file, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("ignore file %q is not a regular file", file)
	}
	return m.loadFile(file)
}

func (m *ignoreMatcher) loadFile(file string) error {
	content, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read ignore file %q: %w", file, err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		rule, ok := parseIgnoreRule(scanner.Text())
		if ok {
			m.rules = append(m.rules, rule)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read ignore file %q: %w", file, err)
	}
	return nil
}

func parseIgnoreRule(line string) (ignoreRule, bool) {
	line = strings.TrimRight(line, "\r")
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ignoreRule{}, false
	}
	if strings.HasPrefix(line, `\#`) {
		line = line[1:]
	}
	negated := false
	if strings.HasPrefix(line, "!") {
		negated = true
		line = strings.TrimSpace(line[1:])
		if line == "" {
			return ignoreRule{}, false
		}
	}
	line = filepath.ToSlash(line)
	line = strings.TrimPrefix(line, "./")
	anchored := strings.HasPrefix(line, "/")
	line = strings.TrimLeft(line, "/")
	directory := strings.HasSuffix(line, "/")
	line = strings.TrimRight(line, "/")
	line = cleanIgnorePath(line)
	if line == "" {
		return ignoreRule{}, false
	}

	basenameOnly := !anchored && !strings.Contains(line, "/")
	return ignoreRule{
		negated:      negated,
		directory:    directory,
		basenameOnly: basenameOnly,
		expression:   regexp.MustCompile(globPatternExpression(line)),
	}, true
}

func (m ignoreMatcher) Ignored(rel string, isDir bool) bool {
	rel = cleanIgnorePath(rel)
	if rel == "" {
		return false
	}
	ignored := false
	for _, rule := range m.rules {
		if rule.matches(rel, isDir) {
			ignored = !rule.negated
		}
	}
	return ignored
}

func (r ignoreRule) matches(rel string, isDir bool) bool {
	if r.basenameOnly {
		return r.matchesBasename(rel, isDir)
	}
	return r.matchesPath(rel, isDir)
}

func (r ignoreRule) matchesBasename(rel string, isDir bool) bool {
	segments := strings.Split(rel, "/")
	last := len(segments) - 1
	if r.directory {
		for i, segment := range segments {
			if i == last && !isDir {
				continue
			}
			if r.expression.MatchString(segment) {
				return true
			}
		}
		return false
	}
	for _, segment := range segments {
		if r.expression.MatchString(segment) {
			return true
		}
	}
	return false
}

func (r ignoreRule) matchesPath(rel string, isDir bool) bool {
	if !r.directory && r.expression.MatchString(rel) {
		return true
	}
	if r.directory && isDir && r.expression.MatchString(rel) {
		return true
	}
	for _, ancestor := range ancestorPaths(rel) {
		if r.expression.MatchString(ancestor) {
			return true
		}
	}
	return false
}

func ancestorPaths(rel string) []string {
	parts := strings.Split(rel, "/")
	if len(parts) <= 1 {
		return nil
	}
	out := make([]string, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		out = append(out, strings.Join(parts[:i], "/"))
	}
	return out
}

func cleanIgnorePath(value string) string {
	value = filepath.ToSlash(value)
	value = strings.TrimPrefix(value, "./")
	cleaned := path.Clean(value)
	if cleaned == "." {
		return ""
	}
	return strings.TrimPrefix(cleaned, "/")
}

func globPatternExpression(pattern string) string {
	var out strings.Builder
	out.WriteString("^")
	for i := 0; i < len(pattern); {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				if i+2 < len(pattern) && pattern[i+2] == '/' {
					out.WriteString("(?:.*/)?")
					i += 3
					continue
				}
				out.WriteString(".*")
				i += 2
				continue
			}
			out.WriteString(`[^/]*`)
			i++
		case '?':
			out.WriteString(`[^/]`)
			i++
		default:
			out.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	out.WriteString("$")
	return out.String()
}
