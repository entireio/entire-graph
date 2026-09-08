package gitutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The size ceiling bounds what the reader HOLDS. An oversized blob is still
// transferred in full so its digest can be recorded, which is the right trade
// for the callers that read those records and the wrong one for a caller that
// only wants source lines to quote: for it the whole object crosses the pipe and
// is hashed only to be discarded, so one generated multi-gigabyte file turns a
// bounded navigation answer into a stall.
//
// The recorded digest is the evidence: it exists only if the body was streamed.
// With bodies skipped there must be no record, because no contents request was
// ever issued.
func TestBatchFileReaderSkipsOversizeBodiesWhenAsked(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	if err := os.WriteFile(filepath.Join(repo, "big.txt"), []byte(strings.Repeat("x", 8192)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "small.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "oversize fixture")

	open := func(skip bool) *BatchFileReader {
		reader, err := NewBatchFileReader(t.Context(), repo, "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reader.Close() })
		reader.SetMaxBytes(1024)
		reader.SetSkipOversizeBodies(skip)
		return reader
	}

	skipping := open(true)
	content, ok, err := skipping.ReadFile("big.txt")
	if err != nil || ok || content != "" {
		t.Fatalf("oversized read = (%d bytes, ok=%v, err=%v), want refused", len(content), ok, err)
	}
	if blob, recorded := skipping.OversizeBlob("big.txt"); recorded {
		t.Fatalf("the oversized body was streamed anyway: %+v", blob)
	}
	// The batch protocol must still be in step: a request that was never issued
	// cannot have left a response behind for the next read to trip over.
	if content, ok, err := skipping.ReadFile("small.go"); err != nil || !ok || content != "package a\n" {
		t.Fatalf("read after a skipped oversized blob = (%q, ok=%v, err=%v)", content, ok, err)
	}

	// The default is unchanged: callers that consume the digest still get it.
	draining := open(false)
	if _, ok, err := draining.ReadFile("big.txt"); err != nil || ok {
		t.Fatalf("oversized read without the opt-out = (ok=%v, err=%v), want refused", ok, err)
	}
	blob, recorded := draining.OversizeBlob("big.txt")
	if !recorded || blob.Bytes != 8192 || blob.Hash == "" {
		t.Fatalf("default oversized record = (%+v, recorded=%v), want a digest", blob, recorded)
	}
}
