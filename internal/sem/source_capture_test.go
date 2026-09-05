package sem

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureOpenedSourceSingleObservation(t *testing.T) {
	reads := 0
	opened := captureOpenedSource(context.Background(), openedSource{read: func(string) (string, bool) {
		reads++
		if reads == 1 {
			return "#!/bin/sh\necho A\n", true
		}
		return "#!/bin/sh\necho B\n", true
	}})
	defer opened.close()
	prefix, ok := opened.readPrefix("run", 9)
	if !ok || prefix != "#!/bin/sh" {
		t.Fatalf("prefix %q %v", prefix, ok)
	}
	a, _ := opened.read("run")
	b, _ := opened.read("run")
	if a != b || !strings.Contains(a, "A") || reads != 1 {
		t.Fatalf("mixed observations %q %q reads=%d", a, b, reads)
	}
}

func TestReadCapturedFileOversizeSingleStream(t *testing.T) {
	input := "#!/bin/sh\n" + strings.Repeat("x\n", 100)
	content, over, err := readCapturedFile(strings.NewReader(input), 12)
	if err != nil || content != "" || over == nil || over.Hash != contentHash([]byte(input)) || over.Bytes != int64(len(input)) || over.Lines != sourceLineCount(input) || over.Prefix != input {
		t.Fatalf("oversize %#v %v", over, err)
	}
	_, _, err = readCapturedFile(io.MultiReader(strings.NewReader("abc"), failingCaptureReader{}), 12)
	if err == nil {
		t.Fatal("read error lost")
	}
}

type failingCaptureReader struct{}

func (failingCaptureReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestCapturedSourceBorrowedMutationAndFreshOperation(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "a.go")
	first := "package p\nfunc Alpha() {}\n"
	second := "package p\nfunc Bravo() {}\n"
	if err := os.WriteFile(path, []byte(first), 0600); err != nil {
		t.Fatal(err)
	}
	options := ProviderSnapshotOptions{Worktree: true, ExtractionReuse: true}
	source, err := prepareSource(context.Background(), repo, options)
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	got, ok := source.read("a.go")
	if !ok || got != first {
		t.Fatal("first capture")
	}
	if err := os.WriteFile(path, []byte(second), 0600); err != nil {
		t.Fatal(err)
	}
	options.captured = &source
	borrowed, err := prepareSource(context.Background(), repo, options)
	if err != nil {
		t.Fatal(err)
	}
	got, ok = borrowed.read("a.go")
	if !ok || got != first || borrowed.close != nil {
		t.Fatal("borrowed capture drifted")
	}
	options.captured = nil
	fresh, err := prepareSource(context.Background(), repo, options)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.close()
	got, ok = fresh.read("a.go")
	if !ok || got != second {
		t.Fatal("new operation is stale")
	}
}
