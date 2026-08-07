package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// polyglotSnapshot is a Rust project that also contains a large corpus of
// deliberately invalid Python fixtures — the shape that made a Rust relation
// answer open with "degraded ... 273 partial failures" about Python files.
func polyglotSnapshot(rustFailures int, pythonFailures int) sem.ProviderSnapshot {
	snapshot := sem.ProviderSnapshot{
		Files: []sem.FileRecord{
			{ID: "f:strings.rs", Path: "crates/linter/src/strings.rs", Language: "Rust"},
			{ID: "f:fixes.rs", Path: "crates/linter/src/fixes.rs", Language: "Rust"},
		},
	}
	snapshot.Header.Stats = sem.ProviderStats{Files: 2 + pythonFailures, ParsedFiles: 2 + pythonFailures}
	snapshot.Header.Completeness = sem.CompletenessReport{
		Languages: map[string]sem.LanguageCompleteness{
			"Rust":   {Files: 2, Symbols: 8},
			"Python": {Files: pythonFailures, Symbols: 0},
		},
	}
	for index := 0; index < pythonFailures; index++ {
		path := "resources/test/corpus/case_" + strings.Repeat("x", index%3) + string(rune('a'+index%26)) + ".py"
		snapshot.Files = append(snapshot.Files, sem.FileRecord{
			ID: "f:" + path, Path: path, Language: "Python",
		})
		snapshot.Header.PartialFailures = append(snapshot.Header.PartialFailures, sem.PartialFailure{
			Code: "E_PARSE_ERROR", Severity: "warning", FilePath: path,
		})
	}
	for index := 0; index < rustFailures; index++ {
		path := "crates/linter/src/broken_" + string(rune('a'+index)) + ".rs"
		snapshot.Files = append(snapshot.Files, sem.FileRecord{
			ID: "f:" + path, Path: path, Language: "Rust",
		})
		snapshot.Header.PartialFailures = append(snapshot.Header.PartialFailures, sem.PartialFailure{
			Code: "E_PARSE_ERROR", Severity: "warning", FilePath: path,
		})
	}
	snapshot.Header.Warnings = []sem.ProviderWarning{{
		Code: worktreeProvenanceWarning, Severity: "warning",
		EffectOnCompleteness: "snapshot records are read from the working tree because --worktree was requested",
	}}
	snapshot.Header.Stats.PartialFailures = len(snapshot.Header.PartialFailures)
	snapshot.Header.Stats.CompletenessLevel = "degraded"
	return snapshot
}

func TestScopedCompletenessCollapsesOutOfScopeFailures(t *testing.T) {
	t.Parallel()
	snapshot := polyglotSnapshot(0, 273)
	scope := buildCompletenessScope(snapshot, "Rust")
	if scope.LanguageFailed != 0 {
		t.Fatalf("in-scope failures = %d, want 0", scope.LanguageFailed)
	}
	if scope.OtherFailures != 273 {
		t.Fatalf("out-of-scope failures = %d, want 273", scope.OtherFailures)
	}
	var out bytes.Buffer
	writeScopedCompletenessBlock(&out, scope,
		snapshot.Header.Warnings, snapshot.Header.PartialFailures, snapshot.Header.Stats)
	rendered := out.String()

	if lines := strings.Count(strings.TrimRight(rendered, "\n"), "\n") + 1; lines != 1 {
		t.Fatalf("out-of-scope report used %d lines, want 1:\n%s", lines, rendered)
	}
	for _, forbidden := range []string{".py", "degraded", "E_PARSE_ERROR"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("report still leads with %q:\n%s", forbidden, rendered)
		}
	}
	for _, want := range []string{"no parse failures in Rust", "273", "Python 273", "cannot affect this answer"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("report omitted %q:\n%s", want, rendered)
		}
	}
	// Honest reporting is not deleted: the full lists are untouched.
	if len(snapshot.Header.PartialFailures) != 273 {
		t.Fatalf("scoping mutated the failure list: %d", len(snapshot.Header.PartialFailures))
	}
}

func TestScopedCompletenessItemizesInScopeFailures(t *testing.T) {
	t.Parallel()
	snapshot := polyglotSnapshot(5, 273)
	scope := buildCompletenessScope(snapshot, "Rust")
	if scope.LanguageFailed != 5 || scope.OtherFailures != 273 {
		t.Fatalf("scope = %#v", scope)
	}
	var out bytes.Buffer
	writeScopedCompletenessBlock(&out, scope,
		snapshot.Header.Warnings, snapshot.Header.PartialFailures, snapshot.Header.Stats)
	rendered := out.String()
	for _, want := range []string{
		"degraded for Rust",
		// The fixture has 5 failures against 2 successfully-parsed files, so there IS no honest
		// fraction: "5 of 2 ... failed to parse" is a number that cannot be true and it discredits
		// every other count on the line (measured as "35 of 0" on three.js, "6 of 0" on terraform).
		// The count alone is the truthful form.
		"5 Rust files failed to parse",
		"broken_a.rs",
		"... 2 more in Rust",
		"plus 273 diagnostics in other languages",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("in-scope report omitted %q:\n%s", want, rendered)
		}
	}
	if strings.Count(rendered, "E_PARSE_ERROR") > maxScopedDiagnostics {
		t.Fatalf("itemized more than %d in-scope failures:\n%s", maxScopedDiagnostics, rendered)
	}
}

// With no resolved focus there is no language to scope by, so nothing may be
// ruled out: every diagnostic stays in scope.
func TestScopedCompletenessWithoutQueryLanguageKeepsEverything(t *testing.T) {
	t.Parallel()
	snapshot := polyglotSnapshot(1, 4)
	scope := buildCompletenessScope(snapshot, "")
	if scope.LanguageFailed != 5 || scope.OtherFailures != 0 {
		t.Fatalf("unscoped report ruled something out: %#v", scope)
	}
}

// A diagnostic about a file the snapshot has no record for has an unknown
// language, so it cannot be ruled out either.
func TestScopedCompletenessKeepsUnknownLanguageDiagnostics(t *testing.T) {
	t.Parallel()
	snapshot := polyglotSnapshot(0, 1)
	snapshot.Header.PartialFailures = append(snapshot.Header.PartialFailures,
		sem.PartialFailure{Code: "E_UNSUPPORTED", FilePath: "mystery.qqq"},
		sem.PartialFailure{Code: "E_REPO_WIDE"},
	)
	scope := buildCompletenessScope(snapshot, "Rust")
	if scope.LanguageFailed != 2 {
		t.Fatalf("unknown-language and repo-wide diagnostics were ruled out: %#v", scope)
	}
}

// Reading the working tree is provenance, not lost coverage: on its own it must
// not produce a coverage banner at all.
func TestWorktreeProvenanceWarningDoesNotReportAsDegraded(t *testing.T) {
	t.Parallel()
	snapshot := sem.ProviderSnapshot{
		Files: []sem.FileRecord{{ID: "f:a.rs", Path: "a.rs", Language: "Rust"}},
	}
	snapshot.Header.Warnings = []sem.ProviderWarning{{Code: worktreeProvenanceWarning}}
	snapshot.Header.Stats = sem.ProviderStats{Files: 1, ParsedFiles: 1, CompletenessLevel: "ok"}
	scope := buildCompletenessScope(snapshot, "Rust")
	if !scope.Worktree {
		t.Fatal("worktree provenance was not recorded")
	}
	var out bytes.Buffer
	writeScopedCompletenessBlock(&out, scope,
		snapshot.Header.Warnings, snapshot.Header.PartialFailures, snapshot.Header.Stats)
	if out.Len() != 0 {
		t.Fatalf("clean run still printed a coverage banner:\n%s", out.String())
	}
}

// Scoping decides presentation and must never be able to silently drop coverage
// reporting because a response was assembled without a scope.
func TestCompletenessScopeFallbackKeepsAllDiagnostics(t *testing.T) {
	t.Parallel()
	warnings := []sem.ProviderWarning{{Code: "W_DIRTY", FilePath: "a.go"}}
	failures := []sem.PartialFailure{{Code: "E_PARSE_ERROR", FilePath: "b.go"}}
	stats := sem.ProviderStats{Files: 5, ParsedFiles: 4, CompletenessLevel: "degraded"}
	scope := completenessScopeOrAll(completenessScope{}, warnings, failures, stats)
	if scope.LanguageFailed != 1 || len(scope.InScopeWarnings) != 1 {
		t.Fatalf("fallback scope = %#v", scope)
	}
	var out bytes.Buffer
	writeScopedCompletenessBlock(&out, scope, warnings, failures, stats)
	for _, want := range []string{"degraded", "W_DIRTY", "E_PARSE_ERROR"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("fallback report omitted %q:\n%s", want, out.String())
		}
	}
	// An explicitly built scope is never overwritten by the fallback.
	built := completenessScope{Language: "Rust", OtherFailures: 9}
	if got := completenessScopeOrAll(built, warnings, failures, stats); got.Language != "Rust" || got.OtherFailures != 9 {
		t.Fatalf("fallback overwrote a real scope: %#v", got)
	}
}

func TestCompactCompletenessLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		scope completenessScope
		stats sem.ProviderStats
		want  string
	}{
		{name: "nothing to report", scope: completenessScope{Language: "Rust"}, want: ""},
		{
			name:  "clean in scope, failures elsewhere",
			scope: completenessScope{Language: "Rust", LanguageFiles: 1496, OtherFailures: 273},
			want:  "C:Rust clean F273-other\n",
		},
		{
			name:  "failures in scope",
			scope: completenessScope{Language: "Rust", LanguageFiles: 1496, LanguageFailed: 2, OtherFailures: 271},
			stats: sem.ProviderStats{CompletenessLevel: "degraded"},
			want:  "C:degraded Rust 2/1496 W0 F271-other\n",
		},
		{
			name:  "no query language",
			scope: completenessScope{LanguageFiles: 5, LanguageFailed: 1},
			stats: sem.ProviderStats{CompletenessLevel: "degraded"},
			want:  "C:degraded 1/5 W0 F0-other\n",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := compactCompletenessLine(testCase.scope, testCase.stats); got != testCase.want {
				t.Fatalf("compactCompletenessLine = %q, want %q", got, testCase.want)
			}
		})
	}
}

// The cost of a cold relation query is invisible from the CLI surface, so it is
// stated exactly when it is actionable: a miss with nowhere to cache.
func TestWriteIndexCostNotice(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		cacheHit      bool
		cacheDisabled bool
		wantNotice    bool
	}{
		{name: "cold with no cache dir", cacheHit: false, cacheDisabled: true, wantNotice: true},
		{name: "cold but cacheable", cacheHit: false, cacheDisabled: false, wantNotice: false},
		{name: "warm hit", cacheHit: true, cacheDisabled: false, wantNotice: false},
		{name: "warm hit with cache disabled", cacheHit: true, cacheDisabled: true, wantNotice: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			writeIndexCostNotice(&out, testCase.cacheHit, testCase.cacheDisabled)
			if got := strings.Contains(out.String(), "--cache-dir"); got != testCase.wantNotice {
				t.Fatalf("notice printed = %v, want %v: %q", got, testCase.wantNotice, out.String())
			}
		})
	}
}
