package sem

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapturedWorktreeObserverSeesRegularAndOversizedBytesOnce(t *testing.T) {
	repo := t.TempDir()
	small := "small\n"
	large := strings.Repeat("line\n", 40)
	if err := os.WriteFile(filepath.Join(repo, "small.go"), []byte(small), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "large.go"), []byte(large), 0600); err != nil {
		t.Fatal(err)
	}
	seen := map[string]*bytes.Buffer{}
	finished := map[string]error{}
	opened, err := openSource(t.Context(), repo, "", sourceOptions{
		capture: true, maxReadBytes: 12,
		captureObserverFactory: func(path string) (io.Writer, func(error)) {
			buffer := new(bytes.Buffer)
			seen[path] = buffer
			return buffer, func(err error) { finished[path] = err }
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.close()
	if got, ok := opened.read("small.go"); !ok || got != small {
		t.Fatalf("small read = %q, %v", got, ok)
	}
	if got, ok := opened.read("large.go"); ok || got != "" {
		t.Fatalf("oversized read = %q, %v", got, ok)
	}
	if got := seen["small.go"].String(); got != small {
		t.Fatalf("small observer bytes = %q, want original bytes", got)
	}
	if got := seen["large.go"].String(); got != large {
		t.Fatalf("oversized observer bytes = %d, want %d", len(got), len(large))
	}
	if finished["small.go"] != nil || finished["large.go"] != nil {
		t.Fatalf("observer completion errors = %#v", finished)
	}
}

type failingObserverWriter struct{}

func (failingObserverWriter) Write([]byte) (int, error) { return 0, errors.New("observer failed") }

func TestCapturedWorktreeObserverReportsWriteErrorAndCancellation(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "file.go"), []byte("package p\nfunc F() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var observed error
	opened, err := openSource(t.Context(), repo, "", sourceOptions{
		capture: true,
		captureObserverFactory: func(string) (io.Writer, func(error)) {
			return failingObserverWriter{}, func(err error) { observed = err }
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := opened.read("file.go"); ok || observed == nil {
		t.Fatalf("observer write error was not reported: ok=%v err=%v", ok, observed)
	}
	_ = opened.close()

	ctx, cancel := context.WithCancel(context.Background())
	observed = nil
	opened, err = openSource(ctx, repo, "", sourceOptions{
		capture: true,
		captureObserverFactory: func(string) (io.Writer, func(error)) {
			return io.Discard, func(err error) { observed = err }
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, _ = opened.read("file.go")
	_ = opened.close()
	if !errors.Is(observed, context.Canceled) {
		t.Fatalf("cancellation = %v, want context canceled", observed)
	}
}

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
