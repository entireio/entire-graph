package sem

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestPathMountGuardRefreshesBetweenResolvers(t *testing.T) {
	t.Parallel()
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	base := filepath.Join(root, "workspace", "repo")
	nested := filepath.Join(base, "nested")
	rel, err := filepath.Rel(root, nested)
	if err != nil {
		t.Fatal(err)
	}
	points := map[string]struct{}{root: {}}
	read := func() (map[string]struct{}, error) { return points, nil }
	check := func(want error) {
		t.Helper()
		guard, err := readPathMountGuard(root, base, read)
		if err != nil {
			t.Fatal(err)
		}
		if err := guard.beforeLookup(rel); !errors.Is(err, want) {
			t.Fatalf("beforeLookup = %v, want %v", err, want)
		}
	}
	check(nil)
	points = map[string]struct{}{root: {}, nested: {}}
	check(errPathCrossesKnownMount)
	points = map[string]struct{}{root: {}}
	check(nil)
}

func TestPathMountGuardDoesNotReuseTableAfterReadFailure(t *testing.T) {
	t.Parallel()
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	failure := errors.New("mount table unavailable")
	var readErr error
	read := func() (map[string]struct{}, error) {
		return map[string]struct{}{root: {}}, readErr
	}
	if _, err := readPathMountGuard(root, root, read); err != nil {
		t.Fatal(err)
	}
	readErr = failure
	if _, err := readPathMountGuard(root, root, read); !errors.Is(err, failure) {
		t.Fatalf("readPathMountGuard = %v, want %v", err, failure)
	}
	readErr = nil
	if _, err := readPathMountGuard(root, root, read); err != nil {
		t.Fatalf("retry after read failure: %v", err)
	}
}
