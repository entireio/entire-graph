package cli

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatsDuplicateWithLaterResult(t *testing.T) {
	for _, graph := range []bool{false, true} {
		name, input := "Grep", map[string]any{"pattern": "x"}
		if graph {
			name, input = "Bash", map[string]any{"command": "entire graph search --query x"}
		}
		for _, completeFirst := range []bool{false, true} {
			sessions := t.TempDir()
			now := statsTime(0)
			use := toolUseLine(t, now, name, "shared", input)
			result := toolResultLine(t, now, "shared", "actual result")
			first, second := []string{use}, []string{use, result}
			if completeFirst {
				first, second = second, first
			}
			dir := filepath.Join(sessions, "s1", "subagents")
			writeTranscript(t, dir, "agent-a.jsonl", first...)
			writeTranscript(t, dir, "agent-b.jsonl", second...)
			writeTranscript(t, dir, "agent-c.jsonl", use, result)
			r := runStatsJSON(t, "--sessions-dir", sessions, "--format", "json", "--since", "all", "--no-cache")
			calls, returned := r.ExplorationCalls, r.ExplorationReturnedBytes
			if graph {
				calls, returned = r.GraphCalls, r.GraphReturnedBytes
				if r.CreditedGraphCalls != 1 {
					t.Fatalf("credited calls=%d; want 1", r.CreditedGraphCalls)
				}
			}
			if calls != 1 || returned != 13 {
				t.Fatalf("graph=%v completeFirst=%v: calls=%d bytes=%d; want 1 and 13", graph, completeFirst, calls, returned)
			}
		}
	}
}

func TestStatsUsagePartialReplay(t *testing.T) {
	sessions := t.TempDir()
	now := statsTime(0)
	writeTranscript(t, filepath.Join(sessions, "s1", "subagents"), "agent-a.jsonl", usageBlocksLine(t, now, "msg", []any{textBlock("done")}, 1, 10, 100, 50))
	writeTranscript(t, filepath.Join(sessions, "s1", "subagents"), "agent-b.jsonl", usageBlocksLine(t, now, "msg", []any{textBlock("partial")}, 1, 10, 100, 5))
	r := runStatsJSON(t, "--sessions-dir", sessions, "--format", "json", "--since", "all", "--no-cache")
	if r.SessionTokens.Output != 50 {
		t.Fatalf("output=%d; want 50", r.SessionTokens.Output)
	}
}
func TestStatsTruncatedGzipCache(t *testing.T) {
	sessions := t.TempDir()
	cache := t.TempDir()
	writeStatsCacheFixture(t, sessions, 10)
	args := []string{"--sessions-dir", sessions, "--format", "json", "--since", "all", "--cache-dir", cache}
	runStatsJSON(t, args...)
	path := statsCacheArtifact(t, cache)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data[:len(data)-8], 0600); err != nil {
		t.Fatal(err)
	}
	r := runStatsJSON(t, args...)
	if r.TranscriptsFromCache != 0 {
		t.Fatalf("truncated gzip served %d cached transcripts", r.TranscriptsFromCache)
	}
	// A valid JSON payload with a bad gzip checksum must also be rejected.
	data[len(data)-8] ^= 1
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if r := runStatsJSON(t, args...); r.TranscriptsFromCache != 0 {
		t.Fatalf("bad gzip checksum served %d cached transcripts", r.TranscriptsFromCache)
	}
	if r := runStatsJSON(t, args...); r.TranscriptsFromCache != 10 {
		t.Fatalf("recomputed cache did not heal: %d cache hits", r.TranscriptsFromCache)
	}
}

func TestStatsCacheBoundsDecompressedData(t *testing.T) {
	payload := []byte(`{"schema":"v1","entries":{"` + strings.Repeat("a", 4096) + `":{}}}`)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, delta := range []int64{-1, 0, 1} {
		cache := &statsCache{}
		cache.loadFrom(bytes.NewReader(compressed.Bytes()), int64(len(payload))+delta)
		if got, want := len(cache.entries), 1; delta < 0 {
			if got != 0 {
				t.Fatalf("oversized decompressed memo accepted: %d entries", got)
			}
		} else if got != want {
			t.Fatalf("within-limit memo rejected at delta %d", delta)
		}
	}
}
