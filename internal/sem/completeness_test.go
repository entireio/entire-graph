package sem

import (
	"os"
	"path/filepath"
	"strings"
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

func TestCompletenessLevel(t *testing.T) {
	cases := []struct {
		name                            string
		failures, files, parsed, symbol int
		want                            string
	}{
		{"empty scope", 0, 0, 0, 0, "ok"},
		{"clean full parse", 0, 100, 100, 4000, "ok"},
		// The subdir/mis-scope bug: a stray config file parses fine (no failure)
		// but the real source was never discovered. Must NOT report "ok".
		{"parsed but no symbols", 0, 3, 3, 0, "degraded"},
		{"majority unparsed", 0, 100, 30, 500, "unsafe"},
		{"a few hard failures", 2, 100, 98, 4000, "degraded"},
		{"mostly failures", 30, 100, 70, 500, "unsafe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := completenessLevel(tc.failures, tc.files, tc.parsed, tc.symbol)
			if got != tc.want {
				t.Fatalf("completenessLevel(f=%d files=%d parsed=%d sym=%d) = %q, want %q",
					tc.failures, tc.files, tc.parsed, tc.symbol, got, tc.want)
			}
		})
	}
}

// TestCompletenessLevelDegradesOnALargeShareOfSkips is the backstop the
// intentional-skip exclusion always claimed to have. Skips are removed from the
// failure count on purpose, and the parsed-file ratio was supposed to catch a
// corpus that is genuinely mostly unread — but that guard only fires past a
// strict majority, so a repository whose whole dist/ tree was skipped (200 of
// 500 files never opened) still reported "ok" and every trust gate keyed on that
// level stayed silent.
//
// The other direction is the constraint that makes this safe: a handful of
// vendored bundles among hundreds of sources must keep reporting "ok", or the
// exclusion is undone.
func TestCompletenessLevelDegradesOnALargeShareOfSkips(t *testing.T) {
	cases := []struct {
		name                            string
		failures, files, parsed, symbol int
		want                            string
	}{
		{"one vendored bundle among many", 0, 500, 499, 4000, "ok"},
		{"a tenth skipped", 0, 500, 450, 4000, "ok"},
		{"a quarter skipped", 0, 500, 300, 4000, "degraded"},
		{"half skipped", 0, 2, 1, 20, "degraded"},
		// A ratio downgrade must never soften a louder verdict.
		{"majority unparsed stays unsafe", 0, 100, 30, 500, "unsafe"},
		{"mostly failures stays unsafe", 30, 100, 70, 500, "unsafe"},
		{"empty scope", 0, 0, 0, 0, "ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := completenessLevel(tc.failures, tc.files, tc.parsed, tc.symbol)
			if got != tc.want {
				t.Fatalf("completenessLevel(f=%d files=%d parsed=%d sym=%d) = %q, want %q",
					tc.failures, tc.files, tc.parsed, tc.symbol, got, tc.want)
			}
		})
	}
}

// TestOversizeSkipCannotReportOkOnASmallCorpus is the end-to-end half: half the
// repository is never opened, so the snapshot must not call itself complete.
func TestOversizeSkipCannotReportOkOnASmallCorpus(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "huge.py", "def big_one():\n    return 1\n"+strings.Repeat("# padding to exceed the cap\n", 200))
	writeFile(t, repo, "plain.py", "def helper(value):\n    return value\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	snapshot, err := BuildProviderSnapshotWithOptions(
		t.Context(), repo, "test", ProviderSnapshotOptions{MaxParseBytes: 1024},
	)
	if err != nil {
		t.Fatal(err)
	}
	stats := snapshot.Header.Stats
	if stats.ParsedFiles >= stats.Files {
		t.Fatalf("fixture parsed every file (%d of %d); the oversize skip did not happen",
			stats.ParsedFiles, stats.Files)
	}
	if stats.CompletenessLevel == "ok" {
		t.Fatalf("completeness = ok with %d of %d files parsed", stats.ParsedFiles, stats.Files)
	}
}
