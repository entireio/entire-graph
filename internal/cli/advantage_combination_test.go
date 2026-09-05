package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// A single independent source fixture checks feature combinations and next-query
// freshness. Live positive compiler combinations run in the pinned Linux suite.
func TestAdvantageSearchCombinationsPreserveFreshSource(t *testing.T) {
	repo, cache := t.TempDir(), t.TempDir()
	write(t, repo, "a.go", "package p\nfunc Alpha() string { return \"before\" }\n")
	for _, extra := range [][]string{
		{"--extraction-cache", "on"},
		{"--compiler", "go"},
		{"--ranking", "experimental-graph"},
		{"--extraction-cache", "on", "--compiler", "go", "--ranking", "experimental-graph"},
	} {
		for _, body := range []string{"before", "after!"} {
			write(t, repo, "a.go", "package p\nfunc Alpha() string { return \""+body+"\" }\n")
			args := append([]string{"search", "--repo", repo, "--query", "Alpha", "--format", "json", "--cache-dir", cache}, extra...)
			var out bytes.Buffer
			if err := Run(context.Background(), Options{Stdout: &out}, args); err != nil {
				t.Fatal(err)
			}
			var response sem.SearchResponse
			if err := json.Unmarshal(out.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Results) == 0 || !strings.Contains(response.Results[0].Snippet, body) {
				t.Fatalf("stale combination %v: %s", extra, out.String())
			}
			if response.Stats.IndexCacheHit {
				t.Fatal("working-tree snapshot reused")
			}
		}
	}
}

func TestAdvantageImpactCombination(t *testing.T) {
	repo, cache := t.TempDir(), t.TempDir()
	write(t, repo, "a.go", "package p\nfunc A() {}\nfunc B(){A()}\nfunc C(){B()}\nfunc D(){C()}\n")
	var out bytes.Buffer
	if err := Run(context.Background(), Options{Stdout: &out}, []string{"impact", "--repo", repo, "--symbol", "A", "--depth", "all", "--extraction-cache", "on", "--compiler", "go", "--cache-dir", cache, "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["traversal"] == nil || result["compiler"] == nil {
		t.Fatalf("lost combination diagnostics: %s", out.String())
	}
}
