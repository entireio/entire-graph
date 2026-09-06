package gitutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCapturedDiffAttributesUsesCapturedNestedRulesAndDriverConfig(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "diff.binary-driver.binary", "true")
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("[attr]binary -diff\n*.go diff=binary-driver\n*.custom diff=automatic-driver\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "nested", ".gitattributes"), []byte("*.bin binary\n*.txt diff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := []string{"main.go", "nested/data.bin", "nested/readme.txt", "automatic.custom", "plain.md"}
	for _, path := range paths {
		content := []byte("source")
		if path == "nested/readme.txt" || path == "automatic.custom" {
			content = []byte("source\x00")
		}
		if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(path)), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "attributes")

	requested := make([]string, 0)
	read := func(path string) (string, bool, error) {
		requested = append(requested, path)
		contents := map[string]string{
			".gitattributes":        "[attr]binary -diff\n*.go diff=binary-driver\n*.custom diff=automatic-driver\n",
			"nested/.gitattributes": "*.bin binary\n*.txt diff\n",
		}
		content, ok := contents[path]
		return content, ok, nil
	}
	got, err := CapturedDiffAttributes(context.Background(), repo, paths, read)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]CapturedDiffAttribute{
		"main.go":           {Value: "binary-driver", Driver: "binary-driver", Binary: true},
		"nested/data.bin":   {Value: "unset", Binary: true},
		"nested/readme.txt": {Value: "set", Text: true},
		"automatic.custom":  {Value: "automatic-driver", Driver: "automatic-driver"},
		"plain.md":          {Value: "unspecified"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("captured attributes = %#v, want %#v", got, want)
	}
	if !slices.Equal(requested, []string{".gitattributes", "nested/.gitattributes"}) {
		t.Fatalf("captured attribute reads = %#v, want only ancestor policies", requested)
	}
}

func TestCapturedDiffAttributesDoesNotFallBackToIndexOrMutableWorktree(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.go -diff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "indexed attributes")
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.go diff\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := CapturedDiffAttributes(context.Background(), repo, []string{"main.go"}, func(path string) (string, bool, error) {
		if path != ".gitattributes" {
			t.Fatalf("unexpected captured read %q", path)
		}
		return "", false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["main.go"].Value != "unset" || !got["main.go"].Binary {
		t.Fatalf("missing captured policy = %#v, want immutable index unset/binary", got["main.go"])
	}
}

func TestCapturedDiffAttributesPropagatesReaderErrorAndConfinement(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	readErr := errors.New("captured policy unavailable")
	_, err := CapturedDiffAttributes(context.Background(), repo, []string{"main.go"}, func(string) (string, bool, error) {
		return "", false, readErr
	})
	if !errors.Is(err, readErr) {
		t.Fatalf("reader error = %v, want %v", err, readErr)
	}
	called := false
	_, err = CapturedDiffAttributes(context.Background(), repo, []string{"../escape"}, func(string) (string, bool, error) {
		called = true
		return "", false, nil
	})
	if err == nil || called || !strings.Contains(err.Error(), "invalid Git tree path") {
		t.Fatalf("unsafe captured path error=%v called=%v", err, called)
	}
}

func TestCapturedDiffAttributesRejectsRepositorySubdirectoryWithoutRootCapture(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init")
	git(t, root, "config", "user.name", "Entire Graph Test")
	git(t, root, "config", "user.email", "graph@example.com")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("nested/*.go -diff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "main.go"), []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "nested")
	called := false
	_, err := CapturedDiffAttributes(context.Background(), filepath.Join(root, "nested"), []string{"main.go"}, func(path string) (string, bool, error) {
		called = true
		_ = path
		return "", false, nil
	})
	if err == nil || called || !strings.Contains(err.Error(), "selected subdirectory") {
		t.Fatalf("nested-repo result error=%v called=%v", err, called)
	}
}

func TestCapturedDiffAttributesHonorsCancellation(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := CapturedDiffAttributes(ctx, repo, []string{"main.go"}, func(string) (string, bool, error) {
		t.Fatal("cancelled operation called captured reader")
		return "", false, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled attributes error = %v", err)
	}
}
