package cli

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeStatsCacheFixture builds a transcript directory big enough to clear
// statsCacheMinTranscripts, so the memo actually engages.
func writeStatsCacheFixture(t *testing.T, sessions string, count int) {
	t.Helper()
	now := statsTime(0)
	for index := 0; index < count; index++ {
		writeTranscript(t, sessions, fmt.Sprintf("s%02d.jsonl", index),
			toolUseLine(t, now, "Bash", fmt.Sprintf("g%d", index),
				map[string]any{"command": `entire graph search --query "x"`}),
			toolResultLine(t, now, fmt.Sprintf("g%d", index), strings.Repeat("s", 20)),
			toolUseLine(t, now, "Grep", fmt.Sprintf("e%d", index), map[string]any{"pattern": "x"}),
			toolResultLine(t, now, fmt.Sprintf("e%d", index), strings.Repeat("m", 420)),
		)
	}
}

func statsCacheArtifact(t *testing.T, cacheDir string) string {
	t.Helper()
	var found string
	if err := filepath.WalkDir(cacheDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json.gz") {
			return nil //nolint:nilerr // walking a test directory best-effort
		}
		found = path
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if found == "" {
		t.Fatalf("no cache artifact was written under %s", cacheDir)
	}
	return found
}

// TestStatsCacheServesRepeatRunsAndInvalidatesOnChange is the third leg of the performance fix:
// a transcript that has not changed is never re-parsed, and one that HAS changed always is.
func TestStatsCacheServesRepeatRunsAndInvalidatesOnChange(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	cacheDir := t.TempDir()
	const count = 10
	writeStatsCacheFixture(t, sessions, count)

	args := []string{"--repo", repo, "--sessions-dir", sessions, "--format", "json",
		"--since", "all", "--cache-dir", cacheDir}

	cold := runStatsJSON(t, args...)
	if cold.TranscriptsFromCache != 0 || cold.Transcripts != count {
		t.Fatalf("cold run: %d/%d from cache, want 0/%d", cold.TranscriptsFromCache, cold.Transcripts, count)
	}
	warm := runStatsJSON(t, args...)
	if warm.TranscriptsFromCache != count {
		t.Fatalf("warm run served %d of %d transcripts from cache, want all %d",
			warm.TranscriptsFromCache, warm.Transcripts, count)
	}
	if warm.EstimatedSavingsBytes != cold.EstimatedSavingsBytes ||
		warm.ExplorationCalls != cold.ExplorationCalls ||
		warm.GraphCalls != cold.GraphCalls ||
		warm.SessionTokens != cold.SessionTokens {
		t.Fatalf("cache changed the answer:\ncold %+v\nwarm %+v", cold, warm)
	}

	// Append to one transcript. Its identity changes, so it must be re-parsed and the totals
	// must move — a memo keyed on the name alone would serve the stale summary here.
	changed := filepath.Join(sessions, "s00.jsonl")
	handle, err := os.OpenFile(changed, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	now := statsTime(0)
	extra := toolUseLine(t, now, "Grep", "extra", map[string]any{"pattern": "y"}) + "\n" +
		toolResultLine(t, now, "extra", strings.Repeat("z", 99)) + "\n"
	if _, err := handle.WriteString(extra); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}

	after := runStatsJSON(t, args...)
	if after.TranscriptsFromCache != count-1 {
		t.Fatalf("after edit: %d of %d from cache, want %d (only the edited file re-parsed)",
			after.TranscriptsFromCache, after.Transcripts, count-1)
	}
	if after.ExplorationCalls != cold.ExplorationCalls+1 {
		t.Fatalf("exploration calls = %d, want %d; the cache served a stale summary",
			after.ExplorationCalls, cold.ExplorationCalls+1)
	}

	// --no-cache is an escape hatch that must actually bypass the memo.
	bypass := runStatsJSON(t, append(append([]string{}, args...), "--no-cache")...)
	if bypass.TranscriptsFromCache != 0 {
		t.Fatalf("--no-cache still served %d transcripts from cache", bypass.TranscriptsFromCache)
	}
	if bypass.ExplorationCalls != after.ExplorationCalls {
		t.Fatalf("--no-cache disagreed with the cached run: %d vs %d",
			bypass.ExplorationCalls, after.ExplorationCalls)
	}
}

// TestStatsCacheFailsOpenOnCorruption: a memo that cannot be read is an empty memo. It is never
// an error, and it is never a reason to serve whatever the bytes happened to decode to.
func TestStatsCacheFailsOpenOnCorruption(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	cacheDir := t.TempDir()
	const count = 10
	writeStatsCacheFixture(t, sessions, count)

	args := []string{"--repo", repo, "--sessions-dir", sessions, "--format", "json",
		"--since", "all", "--cache-dir", cacheDir}
	cold := runStatsJSON(t, args...)

	artifact := statsCacheArtifact(t, cacheDir)
	if err := os.WriteFile(artifact, []byte("not gzip, not json, not anything"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered := runStatsJSON(t, args...)
	if recovered.TranscriptsFromCache != 0 {
		t.Fatalf("a corrupt memo served %d entries; it must be treated as empty",
			recovered.TranscriptsFromCache)
	}
	if recovered.EstimatedSavingsBytes != cold.EstimatedSavingsBytes ||
		recovered.ExplorationCalls != cold.ExplorationCalls {
		t.Fatalf("corrupt memo changed the answer:\nwant %+v\ngot  %+v", cold, recovered)
	}
	// And the run must have healed the artifact rather than leaving it broken.
	if healed := runStatsJSON(t, args...); healed.TranscriptsFromCache != count {
		t.Fatalf("the run after a corrupt memo did not rewrite it: %d of %d from cache",
			healed.TranscriptsFromCache, healed.Transcripts)
	}
}

// TestStatsCacheKeyBindsToTheBinary is the sibling bug this repo already shipped once: a cache
// keyed on a file's NAME served a stale digest after a rebuild. The salt must move when the
// parsing code could have.
func TestStatsCacheKeyBindsToTheBinary(t *testing.T) {
	t.Parallel()
	first := statsCacheSalt("1.2.3")
	if first == statsCacheSalt("1.2.4") {
		t.Fatal("cache salt ignored the CLI version")
	}
	if !strings.Contains(first, statsCacheSchema) {
		t.Fatalf("cache salt does not carry the summary schema: %q", first)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Skipf("no executable path on this platform: %v", err)
	}
	if !strings.Contains(first, executable) {
		t.Fatalf("cache salt does not carry the running binary's identity: %q", first)
	}
}

// TestStatsCacheIsWindowIndependent: the memo holds whole-file facts, so one warm cache serves
// every --since. If the window ever leaked into a summary this would return the narrow answer.
func TestStatsCacheIsWindowIndependent(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	cacheDir := t.TempDir()
	const count = 10
	writeStatsCacheFixture(t, sessions, count)

	base := []string{"--repo", repo, "--sessions-dir", sessions, "--format", "json", "--cache-dir", cacheDir}
	narrow := runStatsJSON(t, append(append([]string{}, base...), "--since", "1h")...)
	wide := runStatsJSON(t, append(append([]string{}, base...), "--since", "all")...)
	fresh := runStatsJSON(t, append(append([]string{}, base...), "--since", "all", "--no-cache")...)

	if narrow.Sessions != count {
		t.Fatalf("narrow window = %d sessions, want %d", narrow.Sessions, count)
	}
	if wide.TranscriptsFromCache != count {
		t.Fatalf("the wide window did not reuse the narrow window's memo: %d of %d",
			wide.TranscriptsFromCache, wide.Transcripts)
	}
	if wide.EstimatedSavingsBytes != fresh.EstimatedSavingsBytes || wide.GraphCalls != fresh.GraphCalls {
		t.Fatalf("cached --since all disagreed with an uncached one:\ncached %+v\nfresh  %+v", wide, fresh)
	}
}

// TestStatsCacheDropsEntriesForDeletedTranscripts keeps the memo bounded by what is on disk.
func TestStatsCacheDropsEntriesForDeletedTranscripts(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	cacheDir := t.TempDir()
	const count = 12
	writeStatsCacheFixture(t, sessions, count)

	args := []string{"--repo", repo, "--sessions-dir", sessions, "--format", "json",
		"--since", "all", "--cache-dir", cacheDir}
	runStatsJSON(t, args...)
	if got := statsCacheEntryCount(t, cacheDir); got != count {
		t.Fatalf("memo holds %d entries, want %d", got, count)
	}
	for index := 0; index < 4; index++ {
		if err := os.Remove(filepath.Join(sessions, fmt.Sprintf("s%02d.jsonl", index))); err != nil {
			t.Fatal(err)
		}
	}
	runStatsJSON(t, args...)
	if got := statsCacheEntryCount(t, cacheDir); got != count-4 {
		t.Fatalf("memo holds %d entries after deleting 4 transcripts, want %d", got, count-4)
	}
}

func statsCacheEntryCount(t *testing.T, cacheDir string) int {
	t.Helper()
	raw, err := os.ReadFile(statsCacheArtifact(t, cacheDir))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	var document statsCacheDocument
	if err := json.NewDecoder(reader).Decode(&document); err != nil {
		t.Fatal(err)
	}
	return len(document.Entries)
}

// TestStatsCacheEngagesAtTheDocumentedThreshold pins statsCacheMinTranscripts in both
// directions: at the threshold the memo works, one transcript below it there is no memo at all.
func TestStatsCacheEngagesAtTheDocumentedThreshold(t *testing.T) {
	t.Parallel()
	at := t.TempDir()
	atSessions := t.TempDir()
	writeStatsCacheFixture(t, atSessions, statsCacheMinTranscripts)
	atArgs := []string{"--repo", t.TempDir(), "--sessions-dir", atSessions, "--format", "json",
		"--since", "all", "--cache-dir", at}
	runStatsJSON(t, atArgs...)
	if warm := runStatsJSON(t, atArgs...); warm.TranscriptsFromCache != statsCacheMinTranscripts {
		t.Fatalf("at the threshold the memo served %d of %d transcripts, want all %d",
			warm.TranscriptsFromCache, warm.Transcripts, statsCacheMinTranscripts)
	}

	below := t.TempDir()
	belowSessions := t.TempDir()
	writeStatsCacheFixture(t, belowSessions, statsCacheMinTranscripts-1)
	belowArgs := []string{"--repo", t.TempDir(), "--sessions-dir", belowSessions, "--format", "json",
		"--since", "all", "--cache-dir", below}
	runStatsJSON(t, belowArgs...)
	if warm := runStatsJSON(t, belowArgs...); warm.TranscriptsFromCache != 0 {
		t.Fatalf("below the threshold the memo served %d transcripts, want 0", warm.TranscriptsFromCache)
	}
	entries, err := os.ReadDir(below)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("below the threshold nothing should have been written, found %d entries", len(entries))
	}
}

// TestStatsCacheRejectsAForeignSchema: a memo written by a different summary schema must be
// ignored wholesale, not decoded field-by-field into whatever happens to line up.
func TestStatsCacheRejectsAForeignSchema(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	cacheDir := t.TempDir()
	const count = 10
	writeStatsCacheFixture(t, sessions, count)

	args := []string{"--repo", repo, "--sessions-dir", sessions, "--format", "json",
		"--since", "all", "--cache-dir", cacheDir}
	cold := runStatsJSON(t, args...)
	if warm := runStatsJSON(t, args...); warm.TranscriptsFromCache != count {
		t.Fatalf("precondition: the memo did not warm up (%d of %d)", warm.TranscriptsFromCache, warm.Transcripts)
	}

	artifact := statsCacheArtifact(t, cacheDir)
	raw, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var document statsCacheDocument
	if err := json.NewDecoder(reader).Decode(&document); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	document.Schema = "from-the-future"
	var rewritten bytes.Buffer
	writer := gzip.NewWriter(&rewritten)
	if err := json.NewEncoder(writer).Encode(document); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, rewritten.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	after := runStatsJSON(t, args...)
	if after.TranscriptsFromCache != 0 {
		t.Fatalf("a memo tagged with a foreign schema served %d entries, want 0",
			after.TranscriptsFromCache)
	}
	if after.EstimatedSavingsBytes != cold.EstimatedSavingsBytes {
		t.Fatalf("rejecting the foreign memo changed the answer: %d vs %d",
			after.EstimatedSavingsBytes, cold.EstimatedSavingsBytes)
	}
}
