package sem

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCompletenessFailureCountExcludesIntentionalSkips pins #4: exceeding the
// parser-input cap or detecting a minified blob is a deliberate skip (the file is
// still recorded), so it must not count toward the "degraded" level — only real
// gaps like parse errors or unsupported languages do.
func TestCompletenessFailureCountExcludesIntentionalSkips(t *testing.T) {
	failures := []PartialFailure{
		{Code: "E_FILE_TOO_LARGE"},
		{Code: "E_MINIFIED"},
		{Code: "E_PARSE_ERROR"},
		{Code: "E_UNSUPPORTED_LANGUAGE"},
	}
	if got := completenessFailureCount(failures); got != 2 {
		t.Fatalf("completenessFailureCount = %d, want 2 (only E_PARSE_ERROR + E_UNSUPPORTED_LANGUAGE)", got)
	}
	if got := completenessFailureCount([]PartialFailure{{Code: "E_FILE_TOO_LARGE"}, {Code: "E_MINIFIED"}}); got != 0 {
		t.Fatalf("only intentional skips should count 0, got %d", got)
	}
}

// TestGraphIgnoreFileHonored pins #3: a repo-root .graphignore excludes matching
// paths for both the worktree and committed-tree matchers, without excluding
// ordinary source.
func TestGraphIgnoreFileHonored(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, graphIgnoreFileName), []byte("# vendored\nparser.c\nscanner.c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaders := map[string]func(string, []string, []string) (ignoreMatcher, error){
		"worktree": loadWorktreeIgnoreMatcher,
		"explicit": loadExplicitIgnoreMatcher,
	}
	for name, load := range loaders {
		t.Run(name, func(t *testing.T) {
			m, err := load(repo, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !m.Ignored("internal/sem/grammars/julia/parser.c", false) {
				t.Error(".graphignore should exclude vendored parser.c")
			}
			if !m.Ignored("internal/sem/zsh/scanner.c", false) {
				t.Error(".graphignore should exclude vendored scanner.c")
			}
			if m.Ignored("internal/sem/provider.go", false) {
				t.Error(".graphignore must not exclude ordinary project source")
			}
		})
	}
}

// hardFailures builds n partial failures that count as real gaps, so the table
// below still reads as "n failures" now that completenessLevel takes records.
func hardFailures(n int) []PartialFailure {
	failures := make([]PartialFailure, 0, n)
	for i := 0; i < n; i++ {
		failures = append(failures, PartialFailure{Code: "E_PARSE_ERROR"})
	}
	return failures
}

func TestCompletenessLevel(t *testing.T) {
	cases := []struct {
		name                  string
		failures              []PartialFailure
		files, parsed, symbol int
		want                  string
	}{
		{"empty scope", nil, 0, 0, 0, "ok"},
		{"clean full parse", nil, 100, 100, 4000, "ok"},
		// The subdir/mis-scope bug: a stray config file parses fine (no failure)
		// but the real source was never discovered. Must NOT report "ok".
		{"parsed but no symbols", nil, 3, 3, 0, "degraded"},
		{"majority unparsed", nil, 100, 30, 500, "unsafe"},
		{"a few hard failures", hardFailures(2), 100, 98, 4000, "degraded"},
		{"mostly failures", hardFailures(30), 100, 70, 500, "unsafe"},
		// Intentional skips still do not count as gaps.
		{"intentional skips only", []PartialFailure{{Code: "E_FILE_TOO_LARGE"}, {Code: "E_MINIFIED"}}, 100, 100, 4000, "ok"},
		// A wall-clock truncation is never "ok" and never merely "degraded":
		// the unreached suffix of the repository is unknown and unbounded.
		{"truncated empty", []PartialFailure{{Code: AnalysisBudgetExceededCode}}, 0, 0, 0, "unsafe"},
		{"truncated large prefix", []PartialFailure{{Code: AnalysisBudgetExceededCode}}, 1000, 1000, 40000, "unsafe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := completenessLevel(tc.failures, tc.files, tc.parsed, tc.symbol)
			if got != tc.want {
				t.Fatalf("completenessLevel(f=%d files=%d parsed=%d sym=%d) = %q, want %q",
					len(tc.failures), tc.files, tc.parsed, tc.symbol, got, tc.want)
			}
		})
	}
}
