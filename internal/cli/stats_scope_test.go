package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStatsCacheIncludesLargeSingleTranscript(t *testing.T) {
	t.Parallel()
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			sessions, cache := t.TempDir(), t.TempDir()
			for i := 0; i < count; i++ {
				writeTranscript(t, sessions, fmt.Sprintf("s%d.jsonl", i), usageBlocksLine(t, statsTime(0), "message", []any{textBlock(strings.Repeat("x", statsCacheMinTranscriptBytes/count))}, 1, 0, 0, 1))
			}
			scope := []string{"--sessions-dir", sessions}
			if count == 1 {
				scope = []string{"--transcript", filepath.Join(sessions, "s0.jsonl")}
			}
			args := append(scope, "--cache-dir", cache, "--format", "json", "--since", "all")
			cold := runStatsJSON(t, args...)
			warm := runStatsJSON(t, args...)
			if cold.TranscriptsFromCache != 0 || warm.TranscriptsFromCache != count || warm.SessionTokens != cold.SessionTokens {
				t.Fatalf("cold=%+v warm=%+v", cold, warm)
			}
			uncached := runStatsJSON(t, append(args, "--no-cache")...)
			if uncached.TranscriptsFromCache != 0 || uncached.SessionTokens != cold.SessionTokens {
				t.Fatalf("no-cache=%+v", uncached)
			}
		})
	}
}

func TestStatsSymlinkTargetIdentityAndMetadata(t *testing.T) {
	t.Parallel()
	for _, single := range []bool{false, true} {
		t.Run(fmt.Sprint(single), func(t *testing.T) {
			sessions, targets, cache := t.TempDir(), t.TempDir(), t.TempDir()
			first, second := filepath.Join(targets, "first.jsonl"), filepath.Join(targets, "second.jsonl")
			now := time.Now()
			for i, name := range []string{"first.jsonl", "second.jsonl"} {
				writeTranscript(t, targets, name, usageBlocksLine(t, statsTime(0), "msg", []any{textBlock(strings.Repeat("x", statsCacheMinTranscriptBytes))}, 1, 0, 0, int64(i+1)))
				if err := os.Chtimes(filepath.Join(targets, name), now, now); err != nil {
					t.Fatal(err)
				}
			}
			alias := filepath.Join(sessions, "alias.jsonl")
			if err := os.Symlink(first, alias); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			scope := []string{"--sessions-dir", sessions}
			if single {
				scope = []string{"--transcript", alias}
			}
			args := append(scope, "--cache-dir", cache, "--format", "json", "--since", "1d")
			cold := runStatsJSON(t, args...)
			warm := runStatsJSON(t, args...)
			if cold.Transcripts != 1 || cold.SessionTokens.Output != 1 || warm.TranscriptsFromCache != 1 {
				t.Fatalf("cold=%+v warm=%+v", cold, warm)
			}
			if err := os.Remove(alias); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(second, alias); err != nil {
				t.Fatal(err)
			}
			retargeted := runStatsJSON(t, args...)
			if retargeted.TranscriptsFromCache != 0 || retargeted.SessionTokens.Output != 2 {
				t.Fatalf("retargeted=%+v", retargeted)
			}
			// Changing only target mtime must invalidate the memo and drive window pruning.
			old := now.Add(-48 * time.Hour)
			if err := os.Chtimes(second, old, old); err != nil {
				t.Fatal(err)
			}
			pruned := runStatsJSON(t, args...)
			if pruned.Transcripts != 0 {
				t.Fatalf("old target was not pruned: %+v", pruned)
			}
		})
	}
}
