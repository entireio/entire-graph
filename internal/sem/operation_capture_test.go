package sem

import (
	"context"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestOperationCaptureIdentityAndCoverage(t *testing.T) {
	build := func(body string, present bool, selected []string, profile Profile) *OperationInputManifest {
		store := newCapturedStore(t.Context(), func(path string) (string, bool) { return body, present }, 1024)
		defer store.close()
		if _, _, err := store.acquire("a.go"); err != nil {
			t.Fatal(err)
		}
		source := sourceContext{capture: store, captureIdentity: operationCaptureIdentity(ProviderSnapshotOptions{Worktree: true, captureInputs: true, Profile: profile}, "repo", "key", "commit", "tree", nil)}
		manifest, err := source.finishCapture(selected)
		if err != nil {
			t.Fatal(err)
		}
		return manifest
	}
	empty := build("", true, []string{"a.go", "not-read.go"}, ProfileFull)
	missing := build("", false, []string{"a.go", "not-read.go"}, ProfileFull)
	if empty.ID == missing.ID || empty.UnobservedSelected != 1 || missing.UnavailableInputs != 1 || empty.Observations[0].Digest == "" {
		t.Fatalf("missing/empty coverage: %+v %+v", empty, missing)
	}
	if !reflect.DeepEqual(empty, build("", true, []string{"a.go", "not-read.go"}, ProfileFull)) {
		t.Fatal("identity is nondeterministic")
	}
	if empty.ID == build("new", true, []string{"a.go", "not-read.go"}, ProfileFull).ID {
		t.Fatal("content not bound")
	}
	if empty.ID == build("", true, []string{"not-read.go", "a.go"}, ProfileFull).ID {
		t.Fatal("ordered scope not bound")
	}
	if empty.ID == build("", true, []string{"a.go", "not-read.go"}, ProfileFast).ID {
		t.Fatal("profile not bound")
	}
	policy := &capturedIgnorePolicy{graphIgnore: capturedIgnoreFile{path: ".graphignore", present: false}}
	opts := ProviderSnapshotOptions{captureInputs: true, cachePolicy: policy}
	absent := operationCaptureIdentity(opts, "repo", "key", "", "", nil)
	policy.graphIgnore.present = true
	if reflect.DeepEqual(absent, operationCaptureIdentity(opts, "repo", "key", "", "", nil)) {
		t.Fatal("missing versus empty policy not bound")
	}
}

func TestOperationCaptureLateBackingErrorIsFatal(t *testing.T) {
	store := newCapturedStore(t.Context(), func(string) (string, bool) { return "captured", true }, 0)
	defer store.close()
	first, ok, err := store.acquire("a.go")
	if err != nil || !ok || first.content != "captured" {
		t.Fatalf("first read: %v", err)
	}
	source := sourceContext{capture: store, captureIdentity: []string{"fixture"}}
	if _, err := source.finishCapture([]string{"a.go"}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	name := store.entries["a.go"].backing
	store.mu.Unlock()
	file, err := store.root.OpenFile(name, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("tampered")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, _, err = store.acquire("a.go"); err == nil {
		t.Fatal("late backing corruption accepted")
	}
	if _, err = source.finishCapture([]string{"a.go"}); err == nil || !strings.Contains(err.Error(), "E_CAPTURE_IO") {
		t.Fatalf("late failure swallowed: %v", err)
	}
}

func TestOperationCaptureManifestBoundAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store := newCapturedStore(ctx, func(string) (string, bool) { return "x", true }, 1024)
	defer store.close()
	paths := make([]string, operationManifestLimit+3)
	for i := range paths {
		paths[i] = strconv.Itoa(i) + ".go"
		if _, _, err := store.acquire(paths[i]); err != nil {
			t.Fatal(err)
		}
	}
	source := sourceContext{capture: store}
	manifest, err := source.finishCapture(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Observations) != operationManifestLimit || manifest.ObservationsOmitted != 3 || manifest.ObservedInputs != len(paths) {
		t.Fatalf("unbounded manifest: %+v", manifest)
	}
	cancel()
	if _, err := source.finishCapture(paths); err == nil {
		t.Fatal("cancelled operation published manifest")
	}
}

func TestOperationCaptureNativeAndDefault(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "a.go", "package p\nfunc Alpha() {}\n")
	baseline, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "fixture", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Header.OperationInputs != nil {
		t.Fatal("default contract changed")
	}
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "fixture", ProviderSnapshotOptions{Worktree: true, captureInputs: true})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Header.OperationInputs == nil || snapshot.Header.OperationInputs.ID == "" {
		t.Fatal("missing native provenance")
	}
	for _, file := range snapshot.Files {
		found := false
		for _, input := range snapshot.Header.OperationInputs.Observations {
			if input.Path == file.Path {
				found = true
				if input.Digest != file.Blob {
					t.Fatal("file and operation disagree")
				}
			}
		}
		if !found {
			t.Fatal("unrecorded source")
		}
	}
	response, err := SearchRepository(t.Context(), repo, "fixture", "Alpha", SearchOptions{Worktree: true, Ranking: "experimental-graph"})
	if err != nil {
		t.Fatal(err)
	}
	if response.OperationInputs == nil {
		t.Fatal("missing final search capture")
	}
}

// assertCaptureProvenance validates the opt-in additive field separately before
// baseline/reuse comparisons. No file/edge/source digest or failure is removed.
func assertCaptureProvenance(t *testing.T, snapshot ProviderSnapshot) {
	t.Helper()
	manifest := snapshot.Header.OperationInputs
	if manifest == nil || manifest.ID == "" || manifest.Version != 1 {
		t.Fatal("capture mode omitted input identity")
	}
	if manifest.ObservedInputs != len(manifest.Observations)+manifest.ObservationsOmitted {
		t.Fatal("manifest coverage count mismatch")
	}
	files := map[string]FileRecord{}
	for _, file := range snapshot.Files {
		files[file.Path] = file
	}
	for _, item := range manifest.Observations {
		if file, ok := files[item.Path]; ok && (item.Status == "captured" || item.Status == "oversized") && item.Digest != file.Blob {
			t.Fatalf("manifest digest disagrees with semantic file %q", item.Path)
		}
	}
}
