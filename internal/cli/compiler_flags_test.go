package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestExtractionReuseCLIReachesSearch(t *testing.T) {
	repo, cache := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package p\nfunc Alpha() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for run := 0; run < 2; run++ {
		var out bytes.Buffer
		err := Run(context.Background(), Options{Stdout: &out}, []string{"search", "--repo", repo, "--query", "Alpha", "--extraction-cache", "on", "--cache-dir", cache, "--format", "json"})
		if err != nil {
			t.Fatal(err)
		}
		var response sem.SearchResponse
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Stats.Extraction == nil {
			t.Fatal("flag did not reach actual search")
		}
		if run == 1 && (response.Stats.Extraction.FilesParsed != 0 || response.Stats.Extraction.FilesReused != 1) {
			t.Fatalf("warm %#v", response.Stats.Extraction)
		}
	}
}
func TestCompilerCLIExplicitFallback(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package p\nfunc Alpha() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	args := []string{"search", "--repo", repo, "--query", "Alpha", "--compiler", "go", "--format", "json"}
	if err := Run(context.Background(), Options{Stdout: &out}, args); err != nil {
		t.Fatal(err)
	}
	var response sem.SearchResponse
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Compiler == nil || response.Compiler.Report.Status != "unavailable" || len(response.Results) == 0 {
		t.Fatal("compiler fallback not explicit")
	}
	if err := Run(context.Background(), Options{}, append(args, "--require-compiler")); err == nil {
		t.Fatal("required compiler fell back")
	}
}

func TestCompilerIndexEvidenceDoesNotEnterStaticCache(t *testing.T) {
	repo, cache := t.TempDir(), t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Fixture")
	git(t, repo, "config", "user.email", "fixture@example.invalid")
	write(t, repo, "a.go", "package p\nfunc Alpha() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "fixture")
	for _, mode := range []string{"go", "off"} {
		var out bytes.Buffer
		if err := Run(context.Background(), Options{Stdout: &out}, []string{"index", "--repo", repo, "--cache-dir", cache, "--compiler", mode, "--format", "json"}); err != nil {
			t.Fatal(err)
		}
		var response indexResponse
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if mode == "go" && (response.Compiler == nil || response.Compiler.Report.Status != "unavailable") {
			t.Fatal("missing explicit compiler fallback")
		}
		if mode == "off" && (response.Compiler != nil || !response.IndexCacheHit) {
			t.Fatal("static cache contaminated or not reused")
		}
	}
}

func TestCompilerNoHitAndNativeHeader(t *testing.T) {
	repo := t.TempDir()
	for _, format := range []string{"json", "ndjson"} {
		var out bytes.Buffer
		args := []string{"search", "--repo", repo, "--query", "missing", "--compiler", "go", "--format", format}
		if err := Run(context.Background(), Options{Stdout: &out}, args); err != nil {
			t.Fatal(err)
		}
		var record map[string]any
		if err := json.NewDecoder(&out).Decode(&record); err != nil {
			t.Fatal(err)
		}
		if record["compiler"] == nil {
			t.Fatal("missing no-hit compiler status")
		}
		if err := Run(context.Background(), Options{}, append(args, "--require-compiler")); err == nil {
			t.Fatal("required compiler allowed no-source success")
		}
	}
}
