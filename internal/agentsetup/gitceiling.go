package agentsetup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// The repository walks in this package look for .git themselves rather than
// invoking git. Git itself honours GIT_CEILING_DIRECTORIES, so a walk that
// does not answers with a repository git would refuse to see -- most often the
// enclosing home-folder repository the variable is set to fence off. These
// helpers apply git's rule to those walks.

// gitCeilingDirectories reads GIT_CEILING_DIRECTORIES the way git does: a
// list of absolute directories discovery must not step up into. Empty and
// relative entries are ignored, as git ignores them.
func gitCeilingDirectories() []string {
	var ceilings []string
	for _, entry := range filepath.SplitList(os.Getenv("GIT_CEILING_DIRECTORIES")) {
		if entry == "" || !filepath.IsAbs(entry) {
			continue
		}
		ceilings = append(ceilings, comparablePath(entry))
	}
	return ceilings
}

// isGitCeiling reports whether dir is one of the ceilings. The walks climb
// one directory at a time, so they reach the nearest ceiling above their start
// before anything higher; stopping there is git's "do not chdir up into" rule.
func isGitCeiling(dir string, ceilings []string) bool {
	if len(ceilings) == 0 {
		return false
	}
	d := comparablePath(dir)
	for _, c := range ceilings {
		if d == c {
			return true
		}
	}
	return false
}

// comparablePath is lexical on purpose: cleaned, and case-folded on Windows,
// where NTFS paths are case-insensitive. It never touches the filesystem,
// because a ceiling entry is caller-controlled and resolving one can reach a
// UNC path and perform network I/O. The cost is that an entry spelled
// differently from the walk (an 8.3 short name against the long one) does
// not match, which leaves the walk exactly as unfenced as it was before.
func comparablePath(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}
