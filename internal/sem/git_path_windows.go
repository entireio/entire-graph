//go:build windows

package sem

import (
	"os"
	"path/filepath"
	"strings"
)

// gitAbsolutePath applies Git-for-Windows' path grammar. Git accepts a leading
// separator as rooted on the current drive and treats a DOS drive prefix as a
// root prefix even without a following separator; filepath.IsAbs accepts
// neither spelling.
func gitAbsolutePath(base, value string) (string, bool) {
	if value == "" {
		return value, false
	}
	if filepath.IsAbs(value) {
		return value, true
	}
	if os.IsPathSeparator(value[0]) {
		volume := filepath.VolumeName(base)
		if volume == "" {
			return value, false
		}
		return volume + value, true
	}
	if volume := filepath.VolumeName(value); volume != "" {
		rest := strings.TrimLeftFunc(value[len(volume):], func(r rune) bool {
			return r == '/' || r == '\\'
		})
		return volume + string(filepath.Separator) + rest, true
	}
	return value, false
}

// gitTargetPathValid rejects Win32 path components Git for Windows refuses
// before attempting to resolve a gitfile or commondir target. Win32 otherwise
// aliases a trailing space or period away, so `gitdir:   ` can accidentally
// resolve to the worktree root even though Git rejects the pointer.
func gitTargetPathValid(value string) bool {
	rest := value[len(filepath.VolumeName(value)):]
	for _, component := range strings.FieldsFunc(rest, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if component == "." || component == ".." || component == "" {
			continue
		}
		last := component[len(component)-1]
		if last == ' ' || last == '.' {
			return false
		}
	}
	return true
}
