package sem

import (
	"os"
	"path/filepath"
	"testing"
)

// Independently authored P1-C: enumeration policy and later source consumers
// must share the same bytes across an edit, including the HEAD source view.
func TestPolicyCaptureEnumerationAndSource(t *testing.T) {
	for _, worktree := range []bool{true, false} {
		t.Run(map[bool]string{true: "worktree", false: "head"}[worktree], func(t *testing.T) {
			repo := t.TempDir()
			initRepo(t, repo)
			for path, content := range map[string]string{".gitignore": "# root before\n", "sub/.gitignore": "# nested before\n", "sub/a.go": "package p\nfunc A(){}\n", ".git/info/exclude": "# private before\n"} {
				writeFile(t, repo, path, content)
			}
			git(t, repo, "add", ".")
			git(t, repo, "commit", "-m", "independent policy fixture")
			options := ProviderSnapshotOptions{Worktree: worktree, ExtractionReuse: true, ExtractionCacheDir: t.TempDir()}
			source, err := prepareSource(t.Context(), repo, options)
			if err != nil {
				t.Fatal(err)
			}
			defer source.close()
			for path, before := range map[string]string{".gitignore": "# root before\n", "sub/.gitignore": "# nested before\n"} {
				writeFile(t, repo, path, "# after mutation\n")
				got, ok := source.read(path)
				if !ok || got != before {
					t.Fatalf("%s mixed enumeration/source bytes: %q", path, got)
				}
			}
			manifest, err := source.finishCapture(source.paths)
			if err != nil {
				t.Fatal(err)
			}
			observations := map[string]OperationInputObservation{}
			for _, input := range manifest.Observations {
				observations[input.Path] = input
			}
			if observations["sub/.gitignore"].Digest != contentHash([]byte("# nested before\n")) {
				t.Fatal("nested policy identity absent")
			}
			if worktree && observations[".git/info/exclude"].Digest != contentHash([]byte("# private before\n")) {
				t.Fatal("private policy identity absent")
			}
		})
	}
}

func TestPolicyCaptureExplicitOverlap(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".gitignore", "b.go\n")
	writeFile(t, repo, "sub/.gitignore", "# nested before\n")
	writeFile(t, repo, "a.go", "package p\nfunc A(){}\n")
	writeFile(t, repo, "b.go", "package p\nfunc B(){}\n")
	options, err := CaptureProviderCachePolicy(repo, ProviderSnapshotOptions{Worktree: true, ExtractionReuse: true, IgnoreFiles: []string{".gitignore", "sub/.gitignore"}})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, ".gitignore", "a.go\n")
	writeFile(t, repo, "sub/.gitignore", "# nested after\n")
	source, err := prepareSource(t.Context(), repo, options)
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	if source.ignores.Ignored("a.go", false) || !source.ignores.Ignored("b.go", false) {
		t.Fatal("overlapping policy recaptured after initial capture")
	}
	for path, want := range map[string]string{".gitignore": "b.go\n", "sub/.gitignore": "# nested before\n"} {
		if got, ok := source.read(path); !ok || got != want {
			t.Fatalf("overlap %s reread: %q", path, got)
		}
	}
}

func TestPolicyCaptureSpillMissingAndFailure(t *testing.T) {
	store := newCapturedStore(t.Context(), nil, 0)
	first, ok, err := capturePolicyRead(store, "repo", "sub/.gitignore", func() (string, bool, error) { return "# policy\n", true, nil })
	if err != nil || !ok {
		t.Fatal(err)
	}
	if _, _, err := capturePolicyRead(store, "repo", "sub/.gitignore", func() (string, bool, error) { t.Fatal("reread"); return "", false, nil }); err != nil {
		t.Fatal(err)
	}
	directory := store.directory
	if directory == "" || store.memory != 0 {
		t.Fatal("policy did not spill within retained bound")
	}
	if got, _, err := store.acquireFrom("sub/.gitignore", func(string) (string, bool) { t.Fatal("source reread"); return "", false }); err != nil || got.content != first {
		t.Fatal("spill changed captured source", err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("policy backing retained")
	}
	missing := newCapturedStore(t.Context(), nil, 0)
	defer missing.close()
	capturePolicyRead(missing, "repo", "missing", func() (string, bool, error) { return "", false, nil })
	manifest, err := (sourceContext{capture: missing}).finishCapture(nil)
	if err != nil || manifest.Observations[0].Status != "absent-policy" || manifest.UnavailableInputs != 0 {
		t.Fatal("optional absence reported as failed source", err)
	}
	failed := newCapturedStore(t.Context(), nil, 0)
	defer failed.close()
	capturePolicyRead(failed, "repo", filepath.Join("sub", ".gitignore"), func() (string, bool, error) { return "", false, os.ErrPermission })
	if _, err := (sourceContext{capture: failed}).finishCapture(nil); err == nil {
		t.Fatal("policy read failure published manifest")
	}
}
