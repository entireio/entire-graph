package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// --- gross covered-files fixture -------------------------------------------------------------

// grossRepo is a temp directory holding files of exactly the given sizes.
func grossRepo(t *testing.T, sizes map[string]int) string {
	t.Helper()
	dir := t.TempDir()
	for name, size := range sizes {
		writeGrossFile(t, dir, name, size)
	}
	return dir
}

func writeGrossFile(t *testing.T, dir, name string, size int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Repeat("x", size)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// grossResult is a renderer-shaped result: the given lines, then a trailing comment line padding
// the whole text to exactly size bytes.
func grossResult(t *testing.T, size int, lines ...string) string {
	t.Helper()
	head := ""
	if len(lines) > 0 {
		head = strings.Join(lines, "\n") + "\n"
	}
	pad := size - len(head) - len("// ")
	if pad < 0 {
		t.Fatalf("result lines exceed %d bytes", size)
	}
	text := head + "// " + strings.Repeat("p", pad)
	if len(text) != size {
		t.Fatalf("result is %d bytes, want %d", len(text), size)
	}
	return text
}

// grossRecord is one Claude Code JSONL record, carrying the cwd and session fields real
// transcripts do.
func grossRecord(t *testing.T, recordType, cwd string, content []any) string {
	t.Helper()
	return encodeLine(t, map[string]any{
		"type":      recordType,
		"timestamp": statsTime(0),
		"cwd":       cwd,
		"sessionId": "gross-session",
		"message":   map[string]any{"role": recordType, "content": content},
	})
}

// writeGrossTranscript writes one session whose only call is a Bash `entire graph query` against
// callRepo, answered by result. extra lines are appended after it.
func writeGrossTranscript(t *testing.T, cwd, callRepo, result string, extra ...string) string {
	t.Helper()
	command := "entire graph query --repo " + filepath.ToSlash(callRepo) + " --query 'where is A'"
	return writeGrossCommandTranscript(t, cwd, command, result, extra...)
}

// writeGrossCommandTranscript is writeGrossTranscript with the Bash command spelled out.
func writeGrossCommandTranscript(t *testing.T, cwd, command, result string, extra ...string) string {
	t.Helper()
	dir := t.TempDir()
	lines := append([]string{
		grossRecord(t, "assistant", cwd, []any{map[string]any{
			"type": "tool_use", "id": "toolu_gross", "name": "Bash",
			"input": map[string]any{"command": command},
		}}),
		grossRecord(t, "user", cwd, []any{map[string]any{
			"type": "tool_result", "tool_use_id": "toolu_gross", "content": result,
		}}),
	}, extra...)
	writeTranscript(t, dir, "gross-session.jsonl", lines...)
	return filepath.Join(dir, "gross-session.jsonl")
}

func runGrossStats(t *testing.T, reportRepo, transcript, cacheDir string) map[string]any {
	t.Helper()
	return runStatsRawJSON(t, "--repo", reportRepo, "--transcript", transcript, "--since", "all",
		"--format", "json", "--cache-dir", cacheDir)
}

func assertGross(t *testing.T, report map[string]any, wantBytes, wantTokens int64) {
	t.Helper()
	if got := rawInt(t, report, "gross_covered_bytes"); got != wantBytes {
		t.Fatalf("gross_covered_bytes = %d, want %d", got, wantBytes)
	}
	if got := rawInt(t, report, "gross_covered_est_tokens"); got != wantTokens {
		t.Fatalf("gross_covered_est_tokens = %d, want %d", got, wantTokens)
	}
}

// --- tests -----------------------------------------------------------------------------------

// TestGrossCountsNamedFilesMinusResultBytes catches a wrong sum: the files a result names
// (4,000 + 2,000 bytes) minus the result's own 600 bytes is 5,400 bytes, 1,350 tokens at 4 B/token.
func TestGrossCountsNamedFilesMinusResultBytes(t *testing.T) {
	t.Parallel()
	repo := grossRepo(t, map[string]int{"a.go": 4000, "b.go": 2000})
	result := grossResult(t, 600, "1. a.go:10 func A()", "2. b.go:3 func B()")
	report := runGrossStats(t, repo, writeGrossTranscript(t, repo, repo, result), t.TempDir())
	assertGross(t, report, 5400, 1350)
}

// TestGrossIgnoresResultsNamingNoFile catches counting a result that names no file: prose
// mentioning a.go:10 is not a result row, so nothing is covered even though a.go exists.
func TestGrossIgnoresResultsNamingNoFile(t *testing.T) {
	t.Parallel()
	repo := grossRepo(t, map[string]int{"a.go": 4000, "b.go": 2000})
	result := grossResult(t, 600, "The symbol was found in a.go:10 and b.go:3.")
	report := runGrossStats(t, repo, writeGrossTranscript(t, repo, repo, result), t.TempDir())
	assertGross(t, report, 0, 0)
}

// TestGrossNeverGoesNegative catches a negative leak: a 500-byte result naming one 100-byte file
// covers nothing, rather than subtracting 400 bytes from the total.
func TestGrossNeverGoesNegative(t *testing.T) {
	t.Parallel()
	repo := grossRepo(t, map[string]int{"a.go": 100})
	result := grossResult(t, 500, "1. a.go:1 func A()")
	report := runGrossStats(t, repo, writeGrossTranscript(t, repo, repo, result), t.TempDir())
	assertGross(t, report, 0, 0)
}

// TestGrossCountsEachFileOncePerResult catches double-counting: two rows in the same 4,000-byte
// file cover it once, so a 600-byte result covers 3,400 bytes (850 tokens), not 7,400.
func TestGrossCountsEachFileOncePerResult(t *testing.T) {
	t.Parallel()
	repo := grossRepo(t, map[string]int{"a.go": 4000})
	result := grossResult(t, 600, "1. a.go:1 func A()", "2. a.go:50 func B()")
	report := runGrossStats(t, repo, writeGrossTranscript(t, repo, repo, result), t.TempDir())
	assertGross(t, report, 3400, 850)
}

// TestGrossFollowsTheCallsOwnRepo catches the wrong root and sizes frozen into the memo. The
// report and the record's cwd point at A, which has no a.go; the call ran with --repo B, where
// a.go is 4,000 bytes, so a 600-byte result covers 3,400 bytes. Growing B's a.go to 8,000 bytes
// moves the figure to 7,400 on a re-run that serves the unchanged transcript from the memo
// (padded past the memo's size threshold so it is actually cached).
func TestGrossFollowsTheCallsOwnRepo(t *testing.T) {
	t.Parallel()
	repoA := grossRepo(t, nil)
	repoB := grossRepo(t, map[string]int{"a.go": 4000})
	result := grossResult(t, 600, "1. a.go:10 func A()")
	filler := grossRecord(t, "user", repoA, []any{map[string]any{"type": "text", "text": strings.Repeat("f", 1<<20)}})
	transcript := writeGrossTranscript(t, repoA, repoB, result, filler)
	cacheDir := t.TempDir()

	assertGross(t, runGrossStats(t, repoA, transcript, cacheDir), 3400, 850)

	writeGrossFile(t, repoB, "a.go", 8000)
	report := runGrossStats(t, repoA, transcript, cacheDir)
	if got := rawInt(t, report, "transcripts_from_cache"); got != 1 {
		t.Fatalf("transcripts_from_cache = %d, want 1: the re-run must be served from the memo", got)
	}
	assertGross(t, report, 7400, 1850)
}

// TestGrossResolvesACwdCallAgainstItsCheckoutRoot catches pricing a call with no --repo against
// its raw cwd. Run from <repo>/sub, the graph resolves the checkout root and names
// `sub/a.go`; that 4,000-byte file minus the 600-byte result covers 3,400 bytes. The report's own
// --repo is an unrelated empty directory, so only the checkout root can find the file.
func TestGrossResolvesACwdCallAgainstItsCheckoutRoot(t *testing.T) {
	t.Parallel()
	repo := grossRepo(t, nil)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeGrossFile(t, filepath.Join(repo, "sub"), "a.go", 4000)
	result := grossResult(t, 600, "1. sub/a.go:10 func A()")
	transcript := writeGrossCommandTranscript(t, filepath.Join(repo, "sub"), "entire graph query --query 'where is A'", result)
	assertGross(t, runGrossStats(t, grossRepo(t, nil), transcript, t.TempDir()), 3400, 850)
}

// TestGrossReadsOnlyTheCallsOwnRepoFlag catches quote-unaware --repo parsing: "--repo" inside a
// quoted query is query text, and a quoted --repo value is one word with its quotes removed,
// spaces included. Every spelling resolves to B (a.go 4,000 bytes; the cwd A has none), so a
// 600-byte result covers 3,400 bytes.
func TestGrossReadsOnlyTheCallsOwnRepoFlag(t *testing.T) {
	t.Parallel()
	repoA := grossRepo(t, nil)
	repoB := filepath.Join(t.TempDir(), "My Repo")
	if err := os.MkdirAll(repoB, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGrossFile(t, repoB, "a.go", 4000)
	b := filepath.ToSlash(repoB)
	result := grossResult(t, 600, "1. a.go:10 func A()")
	for name, command := range map[string]string{
		"flag inside a quoted query": `entire graph query --query 'how does --repo work' --repo "` + b + `"`,
		"double-quoted equals value": `entire graph query --repo="` + b + `" --query 'where is A'`,
		"single-quoted value":        `entire graph query --repo '` + b + `' --query 'where is A'`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			transcript := writeGrossCommandTranscript(t, repoA, command, result)
			assertGross(t, runGrossStats(t, repoA, transcript, t.TempDir()), 3400, 850)
		})
	}
}

// TestGrossSizesACallAgainstTheDirectoryALeadingCdMovedTo catches a leading `cd` ignored. From the
// record's cwd, `cd sub && entire graph query --repo .` prints paths relative to <cwd>/sub, where
// a.go is 4,000 bytes, so a 600-byte result covers 3,400 bytes; on Windows the Git Bash spelling
// of the same directory moves there too. A `cd` to a directory the command line does not spell out
// leaves the call unpriced, though <cwd>/a.go exists. The report's --repo is an unrelated empty
// directory, so no fallback can find the file.
func TestGrossSizesACallAgainstTheDirectoryALeadingCdMovedTo(t *testing.T) {
	t.Parallel()
	const call = " && entire graph query --repo . --query 'where is A'"
	result := grossResult(t, 600, "1. a.go:10 func A()")
	cwd := grossRepo(t, nil)
	sub := filepath.Join(cwd, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGrossFile(t, sub, "a.go", 4000)
	moves := map[string]string{"relative": "cd sub" + call}
	if runtime.GOOS == "windows" {
		volume := filepath.VolumeName(sub)
		gitBash := "/" + strings.ToLower(strings.TrimSuffix(volume, ":")) + filepath.ToSlash(strings.TrimPrefix(sub, volume))
		moves["git bash absolute"] = "cd '" + gitBash + "'" + call
	}
	for name, command := range moves {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			transcript := writeGrossCommandTranscript(t, cwd, command, result)
			assertGross(t, runGrossStats(t, grossRepo(t, nil), transcript, t.TempDir()), 3400, 850)
		})
	}
	t.Run("unknown target", func(t *testing.T) {
		t.Parallel()
		unmoved := grossRepo(t, map[string]int{"a.go": 4000})
		transcript := writeGrossCommandTranscript(t, unmoved, `cd "$D"`+call, result)
		assertGross(t, runGrossStats(t, grossRepo(t, nil), transcript, t.TempDir()), 0, 0)
	})
}
