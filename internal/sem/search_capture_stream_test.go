package sem

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCaptureMatchStreamChunkBoundaries(t *testing.T) {
	input := "none\nNeedle need Éclair\nneedle\nlast\nforbidden"
	for _, chunk := range []int{1, 2, 3, 7, 32, len(input)} {
		var got []string
		stream, err := newCaptureMatchStream(context.Background(), []string{"need", "needle", "éclair", "last", "forbidden"}, 3, func(s string) { got = append(got, s) })
		if err != nil {
			t.Fatal(err)
		}
		for rest := input; rest != ""; {
			n := min(chunk, len(rest))
			if _, err := stream.Write([]byte(rest[:n])); err != nil {
				t.Fatal(err)
			}
			rest = rest[n:]
		}
		if err := stream.finish(); err != nil {
			t.Fatal(err)
		}
		want := []string{"Needle", "need", "Éclair", "needle", "last"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("chunk %d: %q, want %q", chunk, got, want)
		}
	}
}

func TestCaptureMatchStreamBoundsLongLineAndKeepsBinarySniffing(t *testing.T) {
	count := 0
	stream, err := newCaptureMatchStream(context.Background(), []string{"needle"}, 1, func(string) { count++ })
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1024; i++ {
		if _, err := stream.Write([]byte(strings.Repeat("x", 4096))); err != nil {
			t.Fatal(err)
		}
		if len(stream.buffer) > stream.window {
			t.Fatalf("retained %d bytes", len(stream.buffer))
		}
	}
	_, _ = stream.Write([]byte("needle\nneedle"))
	if err := stream.finish(); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("got %d matches", count)
	}
	stream, err = newCaptureMatchStream(context.Background(), []string{"needle"}, 1, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = stream.Write([]byte("needle\n"))
	_, _ = stream.Write([]byte("\x00"))
	if !stream.binary {
		t.Fatal("binary detection stopped at match budget")
	}
}

func TestCaptureMatchStreamCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := newCaptureMatchStream(ctx, []string{"needle"}, 32, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := stream.Write([]byte("needle")); !errors.Is(err, context.Canceled) {
		t.Fatalf("write: %v", err)
	}
	if err := stream.finish(); !errors.Is(err, context.Canceled) {
		t.Fatalf("finish: %v", err)
	}
}

func TestCapturedPreselectionObservesOversizeWithoutRereading(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	content := strings.Repeat("x", 100000) + "needle\n"
	write(t, repo, "large.go", content)
	observer, err := newCapturePreselectionObserver(context.Background(), []string{"needle"}, []string{"needle"})
	if err != nil {
		t.Fatal(err)
	}
	source, err := prepareSource(context.Background(), repo, ProviderSnapshotOptions{Worktree: true, ExtractionReuse: true, MaxParseBytes: 64, captureObserverFactory: observer.factory})
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	for attempt := 0; attempt < 2; attempt++ {
		matches, _, err := capturedPreselectionMatches(context.Background(), source, []string{"large.go"}, []string{"needle"}, 32, observer)
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 1 || matches[0].Text != "needle" {
			t.Fatalf("attempt %d: %v", attempt, matches)
		}
		if _, ok := source.read("large.go"); ok {
			t.Fatal("oversized parse limit was widened")
		}
		over, ok := source.oversize("large.go")
		if !ok || over.Bytes != int64(len(content)) {
			t.Fatalf("lost oversized identity: %#v", over)
		}
		write(t, repo, "large.go", "changed")
	}
}
