package cli

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The matching salt is constructed, not evidence about any installed binary or
// normal upgrade. No real memo/session discovery or executable identity is used.
func TestReview304CacheClassifierCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name       string
		transcript string
		oldSummary string
		want       review304CacheCounts
	}{
		{
			name: "old_d_error_at_eof",
			transcript: `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"g1","name":"Bash","input":{"command":"entire graph query --query run"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"g1","content":"synthetic error","is_error":true}]}}
`,
			oldSummary: `{"calls":[{"i":"g1","v":"query","b":15,"r":true,"d":"d"}]}`,
			want: review304CacheCounts{
				Sessions: 1, Locate: 1, Results: 1, LocateBytes: 15, Errors: 1,
			},
		},
		{
			name: "old_q_parallel_request_before_delivery",
			// Both requests precede Graph delivery; EOF follows delivery. A
			// request-relative q must not become a post-delivery re-query.
			transcript: `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"g1","name":"Bash","input":{"command":"entire graph query --query run"}},{"type":"tool_use","id":"q1","name":"Grep","input":{"pattern":"run","path":"src/a.go"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"q1","content":"hit"}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"g1","content":"src/a.go:10","is_error":false}]}}
`,
			oldSummary: `{"calls":[{"i":"g1","v":"query","b":11,"r":true,"d":"q"},{"i":"q1","k":"Grep","b":3,"r":true}]}`,
			want: review304CacheCounts{
				Sessions: 1, Locate: 1, Results: 1, LocateBytes: 11, Exploration: 1, Censored: 1,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "authored.jsonl")
			if err := os.WriteFile(path, []byte(tc.transcript), 0o600); err != nil {
				t.Fatalf("write authored fixture: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat authored fixture: %v", err)
			}
			file := transcriptFile{path: path, identity: path, session: "review304", size: info.Size(), modTime: info.ModTime()}
			fresh, ok := summariseTranscript(path)
			if !ok || fresh.Malformed != 0 {
				t.Fatalf("fresh synthetic parse failed: ok=%v malformed=%d", ok, fresh.Malformed)
			}
			// A real, correct fresh classification is a prerequisite. This
			// prevents parser failures or empty reports hiding a cache defect.
			if got := review304CacheReport(t, fresh); got != tc.want {
				t.Fatalf("fresh parser prerequisite: got %+v, want %+v", got, tc.want)
			}

			const sameSalt = "review304-constructed-same-salt"
			keyer := &statsCache{salt: sameSalt}
			key := keyer.entryKey(file)
			// Literal historic envelope and classes: do NOT substitute the
			// current schema or current enum constants in this old payload.
			oldJSON := []byte(fmt.Sprintf(`{"schema":"v2","entries":{%q:%s}}`, key, tc.oldSummary))
			currentJSON, err := json.Marshal(statsCacheDocument{
				Schema: statsCacheSchema, Entries: map[string]fileSummary{key: fresh},
			})
			if err != nil {
				t.Fatalf("encode current-schema control: %v", err)
			}

			t.Run("old_v2_same_salt", func(t *testing.T) {
				got, hits := review304CacheCollect(t, file, sameSalt, oldJSON)
				if hits == 0 {
					if got != tc.want {
						t.Errorf("cache miss did not recover fresh partition: got %+v, want %+v", got, tc.want)
					}
					return
				}
				// Safe migration to an explicit conservative outcome is also
				// allowed. Obsolete d dropping out of the partition and q
				// retaining a false causal interpretation are both failures.
				classified := got.NoFollowUp + got.Requery + got.Read + got.Censored + got.Errors + got.Ineligible
				if hits != 1 || got.Sessions != 1 || got.Locate != 1 || got.Results != 1 ||
					got.Exploration != tc.want.Exploration || got.LocateBytes != tc.want.LocateBytes ||
					classified != got.Results || got.NoFollowUp != 0 || got.Requery != 0 || got.Read != 0 {
					t.Errorf("old v2 hit lacks a safe complete outcome partition: hits=%d counts=%+v", hits, got)
				}
			})
			t.Run("current_schema_current_class_hit", func(t *testing.T) {
				got, hits := review304CacheCollect(t, file, sameSalt, currentJSON)
				if hits != 1 || got != tc.want {
					t.Errorf("valid current memo must hit intact: hits=%d got %+v, want %+v", hits, got, tc.want)
				}
			})
			t.Run("changed_salt_miss", func(t *testing.T) {
				got, hits := review304CacheCollect(t, file, sameSalt+"-changed", currentJSON)
				if hits != 0 || got != tc.want {
					t.Errorf("changed salt must reparse: hits=%d got %+v, want %+v", hits, got, tc.want)
				}
			})
		})
	}
}

// Use public JSON names rather than the draft's renamed/reverted Go aliases.
type review304CacheCounts struct {
	Sessions    int   `json:"sessions"`
	Locate      int   `json:"graph_locate_calls"`
	Results     int   `json:"credited_graph_calls"`
	LocateBytes int64 `json:"graph_locate_returned_bytes"`
	Exploration int   `json:"exploration_calls"`
	NoFollowUp  int   `json:"graph_locate_no_follow_up"`
	Requery     int   `json:"graph_locate_requery"`
	Read        int   `json:"graph_locate_follow_up_read"`
	Censored    int   `json:"graph_locate_censored"`
	Errors      int   `json:"graph_locate_error"`
	Ineligible  int   `json:"graph_locate_ineligible"`
}

func review304CacheReport(t *testing.T, summary fileSummary) review304CacheCounts {
	t.Helper()
	collector := newStatsCollector()
	collector.session("review304").merge(summary)
	var response statsResponse
	collector.finish(&response, time.Time{})
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("encode real report: %v", err)
	}
	var counts review304CacheCounts
	if err := json.Unmarshal(encoded, &counts); err != nil {
		t.Fatalf("read report counters: %v", err)
	}
	return counts
}

func review304CacheCollect(t *testing.T, file transcriptFile, salt string, document []byte) (review304CacheCounts, int) {
	t.Helper()
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(document); err != nil {
		t.Fatalf("compress authored memo: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("finish authored memo: %v", err)
	}
	cache := &statsCache{salt: salt, entries: map[string]fileSummary{}}
	cache.loadFrom(bytes.NewReader(compressed.Bytes()), statsCacheMaxBytes)
	collector := newStatsCollector()
	collector.cache = cache
	summary, ok := collector.summarise(file)
	if !ok || summary.Malformed != 0 {
		t.Fatalf("collector failed synthetic input: ok=%v malformed=%d", ok, summary.Malformed)
	}
	return review304CacheReport(t, summary), collector.fromCache
}
