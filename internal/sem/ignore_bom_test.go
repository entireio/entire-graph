package sem

import (
	"os/exec"
	"strings"
	"testing"
)

// TestIgnoreFilesDropOneLeadingUTF8BOM holds the ignore readers to git's own
// answer.
//
// Git accepts exactly one UTF-8 byte-order mark at byte zero of an exclude
// file and drops it before the first pattern. Without the same step the first
// rule of such a file is the mark followed by "zzcustom/", which matches
// nothing: the path it
// was written to exclude is indexed, nothing warns, and the repository's own
// statement about what to keep out is silently overruled. Windows PowerShell
// 5.1 writes that mark for Out-File and Set-Content -Encoding utf8, which is
// how a .graphignore created there arrives.
//
// The bar is agreement with git rather than a remembered rule, so each case is
// also put to `git check-ignore` where git is installed.
func TestIgnoreFilesDropOneLeadingUTF8BOM(t *testing.T) {
	const bom = "\xef\xbb\xbf"
	for _, tc := range []struct {
		name     string
		content  string
		excluded bool
	}{
		{"no mark", "zzcustom/\n", true},
		{"one mark at byte zero is dropped", bom + "zzcustom/\n", true},
		{"a second mark is pattern text", bom + bom + "zzcustom/\n", false},
		{"a mark on a later line is pattern text", "keep.js\n" + bom + "zzcustom/\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var matcher ignoreMatcher
			if err := matcher.loadContent(tc.content, false, repoIgnoreOrigin(".graphignore")); err != nil {
				t.Fatal(err)
			}
			_, got := matcher.repoExclusion("zzcustom/lib.js", false)
			if got != tc.excluded {
				t.Fatalf("zzcustom/lib.js excluded = %v, want %v, for %q", got, tc.excluded, tc.content)
			}
			if want := gitExcludes(t, tc.content, "zzcustom/lib.js"); want != nil && *want != tc.excluded {
				t.Fatalf("git disagrees with this case: git excludes = %v, this test expects %v", *want, tc.excluded)
			}
		})
	}
}

// gitExcludes asks the installed git whether it would exclude path under this
// exact .gitignore, or reports nil when there is no git to ask.
func gitExcludes(t *testing.T, ignore, path string) *bool {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		return nil
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", repo}, args...)...)
		command.Env = append(command.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	writeFile(t, repo, ".gitignore", ignore)
	command := exec.Command("git", "-C", repo, "check-ignore", "-q", path)
	command.Env = append(command.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	err := command.Run()
	excluded := err == nil
	return &excluded
}
