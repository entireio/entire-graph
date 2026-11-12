package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The cache root may be a link, but its statusline family must never redirect reads, writes,
// resume checkpoints, stats memos or cleanup into another directory (including one inside it).
func TestStatuslineCacheRefusesRedirectingFamily(t *testing.T) {
	for _, inside := range []bool{false, true} {
		name := "outside"
		if inside {
			name = "inside"
		}
		t.Run(name, func(t *testing.T) {
			cache := t.TempDir()
			target := t.TempDir()
			if inside {
				target = filepath.Join(cache, ".git")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			victim := filepath.Join(target, "unrelated-document.txt")
			if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-8 * 24 * time.Hour)
			if err := os.Chtimes(victim, old, old); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "record.json"), []byte(`{"lookups":999}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(cache, "statusline")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}

			var cached statuslineFigures
			if loadStatuslineJSON(cache, "record.json", &cached) {
				t.Fatal("read followed redirecting family")
			}
			saveStatuslineJSON(cache, "written.json", statuslineFigures{Lookups: 1})
			transcript := writeGrossCommandTranscript(t, "", "entire graph query --repo .", "1. a.go:1 A",
				grossRecord(t, "user", "", []any{map[string]any{"type": "text", "text": strings.Repeat("f", 1<<20)}}))
			if _, ok := statuslineFiguresFor(transcript, t.TempDir(), "tree", cache, "test"); !ok {
				t.Fatal("cache refusal prevented rendering figures")
			}
			sweepStatuslineCache(cache, time.Now())
			content, err := os.ReadFile(victim)
			if err != nil || string(content) != "keep" {
				t.Fatalf("cleanup touched outside document: %q, %v", content, err)
			}
			entries, err := os.ReadDir(target)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 2 {
				t.Fatalf("cache wrote through redirect: %v", entries)
			}
		})
	}
}

func TestStatuslineMemoRefusesRedirectingDescendants(t *testing.T) {
	for _, component := range []string{"stats", filepath.Join("stats", statsCacheSchema)} {
		t.Run(component, func(t *testing.T) {
			cache, target := t.TempDir(), t.TempDir()
			link := filepath.Join(cache, "statusline", component)
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			files := []transcriptFile{{path: "session.jsonl", size: statsCacheMinTranscriptBytes}}
			memo := openStatsCacheIn(cache, "statusline", "session", "test", files)
			memo.store(files[0], fileSummary{})
			memo.save()
			entries, err := os.ReadDir(target)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("memo escaped through %s: %v", component, entries)
			}
		})
	}
}

func TestStatuslineCacheAllowsLinkedTrustRoot(t *testing.T) {
	backing := t.TempDir()
	cache := filepath.Join(t.TempDir(), "cache")
	if err := os.Symlink(backing, cache); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	saveStatuslineJSON(cache, "record.json", statuslineFigures{Lookups: 7})
	var cached statuslineFigures
	if !loadStatuslineJSON(cache, "record.json", &cached) || cached.Lookups != 7 {
		t.Fatalf("linked cache root did not round trip: %+v", cached)
	}
}

func TestStatuslineSweepDoesNotTruncateLinkedStamp(t *testing.T) {
	for _, hard := range []bool{false, true} {
		name := "symlink"
		if hard {
			name = "hardlink"
		}
		t.Run(name, func(t *testing.T) {
			cache := t.TempDir()
			dir := filepath.Join(cache, "statusline")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			victim := filepath.Join(t.TempDir(), "document")
			if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-8 * 24 * time.Hour)
			if err := os.Chtimes(victim, old, old); err != nil {
				t.Fatal(err)
			}
			link := os.Symlink
			if hard {
				link = os.Link
			}
			if err := link(victim, filepath.Join(dir, statuslineSweepStamp)); err != nil {
				t.Skipf("links unavailable: %v", err)
			}
			sweepStatuslineCache(cache, time.Now())
			content, err := os.ReadFile(victim)
			if err != nil || string(content) != "keep" {
				t.Fatalf("sweep truncated linked document: %q, %v", content, err)
			}
		})
	}
}
