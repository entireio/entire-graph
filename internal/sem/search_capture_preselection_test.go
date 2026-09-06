package sem

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Extraction reuse must keep the bounded candidate pool used by ordinary working-tree
// search. The captured reader is the source of bytes; Git must not be consulted for
// mutable content after the operation capture begins.
func TestSearchExtractionReusePreservesLargeWorktreePreselectionParity(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	for index := 0; index < minGitGrepPreselectionFiles+20; index++ {
		path := fmt.Sprintf("src/f%05d.go", index)
		body := fmt.Sprintf("package p\nfunc F%05d() { /* needle */ }\n", index)
		if index < 20 {
			body = fmt.Sprintf("package p\nfunc F%05d( { /* needle */\n", index)
		}
		write(t, repo, path, body)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "large captured preselection fixture")

	fresh, err := SearchRepository(context.Background(), repo, "fixture", "needle", SearchOptions{
		Worktree: true, Profile: ProfileSyntaxOnly, TopK: 8, MaxIndexedFiles: 4,
		CacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	reuse, err := SearchRepository(context.Background(), repo, "fixture", "needle", SearchOptions{
		Worktree: true, Profile: ProfileSyntaxOnly, TopK: 8, MaxIndexedFiles: 4,
		ExtractionReuse: true, CacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Stats.FilesIndexed != 4 || reuse.Stats.FilesIndexed != 4 {
		t.Fatalf("selected file counts fresh=%d reuse=%d, want four", fresh.Stats.FilesIndexed, reuse.Stats.FilesIndexed)
	}
	if !reflect.DeepEqual(fresh.PartialFailures, reuse.PartialFailures) {
		t.Fatalf("partial failure membership changed with capture reuse:\nfresh=%#v\nreuse=%#v", fresh.PartialFailures, reuse.PartialFailures)
	}
	if !reflect.DeepEqual(fresh.Results, reuse.Results) {
		t.Fatalf("result membership changed with capture reuse:\nfresh=%#v\nreuse=%#v", fresh.Results, reuse.Results)
	}
	if reuse.Stats.FilesContentRead != reuse.Stats.FilesScanned {
		t.Fatalf("captured preselection read %d of %d source files; expected one coherent source read per file", reuse.Stats.FilesContentRead, reuse.Stats.FilesScanned)
	}
}

// Once source capture has admitted the original bytes, mutating the backing file during
// the operation must not change either candidate selection or extracted result content.
func TestSearchExtractionReusePreselectionUsesCapturedBytesAfterMutation(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "src/target.go", "package p\nfunc CapturedNeedle() string { return \"before\" }\n")
	for index := 0; index < minGitGrepPreselectionFiles+20; index++ {
		write(t, repo, fmt.Sprintf("src/f%05d.go", index), fmt.Sprintf("package p\nfunc F%05d() {}\n", index))
	}
	options := SearchOptions{
		Worktree: true, ExtractionReuse: true, Profile: ProfileSyntaxOnly,
		TopK: 4, MaxIndexedFiles: 1, CacheDir: t.TempDir(),
	}
	options.afterSourceSelection = func() {
		write(t, repo, "src/target.go", "package p\nfunc ChangedNeedle() string { return \"after\" }\n")
	}
	got, err := SearchRepository(context.Background(), repo, "fixture", "CapturedNeedle", options)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(got.Results)
	if !strings.Contains(string(payload), "CapturedNeedle") || !strings.Contains(string(payload), "before") {
		t.Fatalf("search did not use captured bytes: %s", payload)
	}
	if strings.Contains(string(payload), "ChangedNeedle") || strings.Contains(string(payload), "after") {
		t.Fatalf("search observed mutated backing bytes: %s", payload)
	}
}
