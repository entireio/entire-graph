package sem

import (
	"path/filepath"
	"testing"
)

func windowsSeparator(c byte) bool { return c == '/' || c == '\\' }
func unixSeparator(c byte) bool    { return c == '/' }

// TestInSlashDirMatchesFilepathOnBothSeparatorRules pins inSlashDirWith to
// what filepath.ToSlash(filepath.Dir(path)) returns on each platform. The
// Windows column is written out by hand because this test also runs on Unix,
// where filepath cannot be asked how Windows would answer.
func TestInSlashDirMatchesFilepathOnBothSeparatorRules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path       string
		windowsDir string
		unixDir    string
	}{
		{"gen/pkg0007/file.pb.go", "gen/pkg0007", "gen/pkg0007"},
		{`gen\pkg0007\file.pb.go`, "gen/pkg0007", "."},
		{`gen/pkg0007\file.pb.go`, "gen/pkg0007", "gen"},
		{"file.go", ".", "."},
		{"a/file.go", "a", "a"},
		{"a/b/c/file.go", "a/b/c", "a/b/c"},
		// Paths the fast path declines must still get filepath's answer.
		{"a//b/file.go", "a/b", "a/b"},
		{"a/./b/file.go", "a/b", "a/b"},
		{"a/../b/file.go", "b", "b"},
		{"./a/file.go", "a", "a"},
		{"../a/file.go", "../a", "../a"},
		{"a/b/", "a/b", "a/b"},
		{"/abs/file.go", "/abs", "/abs"},
		{"", ".", "."},
	}
	for _, tc := range cases {
		for _, rule := range []struct {
			name  string
			isSep func(byte) bool
			want  string
		}{{"windows", windowsSeparator, tc.windowsDir}, {"unix", unixSeparator, tc.unixDir}} {
			if !inSlashDirWith(tc.path, rule.want, rule.isSep) {
				t.Errorf("%s: inSlashDirWith(%q, %q) = false, want true", rule.name, tc.path, rule.want)
			}
			for _, other := range []string{rule.want + "x", "zz", rule.want + "/x"} {
				if inSlashDirWith(tc.path, other, rule.isSep) {
					t.Errorf("%s: inSlashDirWith(%q, %q) = true, want false", rule.name, tc.path, other)
				}
			}
		}
		// On the host the answer must agree with filepath itself, whichever
		// platform that is, including for the paths that fall back.
		hostDir := filepath.ToSlash(filepath.Dir(tc.path))
		if !inSlashDir(tc.path, hostDir) {
			t.Errorf("host: inSlashDir(%q, %q) = false, want true", tc.path, hostDir)
		}
	}
}

// TestInSlashDirWithDoesNotAllocateForIndexPaths is the platform-independent
// half of TestResolveTypeReferenceCostDoesNotScaleWithSameNameDeclarations:
// that test only failed on windows-latest, where filepath.Dir and ToSlash
// together allocated 48 bytes per candidate. Injecting the Windows separator
// rule reproduces that code path on any host.
func TestInSlashDirWithDoesNotAllocateForIndexPaths(t *testing.T) {
	for _, rule := range []struct {
		name  string
		isSep func(byte) bool
	}{{"windows", windowsSeparator}, {"unix", unixSeparator}} {
		for _, path := range []string{"gen/pkg0007/file.pb.go", "other/file.pb.go", "file.go"} {
			allocs := testing.AllocsPerRun(200, func() {
				_ = inSlashDirWith(path, "gen/pkg0007", rule.isSep)
			})
			if allocs != 0 {
				t.Errorf("%s: inSlashDirWith(%q) allocated %.0f times per call, want 0", rule.name, path, allocs)
			}
		}
	}
}
