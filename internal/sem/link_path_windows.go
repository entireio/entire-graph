//go:build windows

package sem

import (
	"os"
	"path/filepath"
)

// linkAbsolutePath applies Win32 link-target semantics, which differ from
// Git's gitfile grammar for drive-relative strings such as C:foo. A leading
// separator is rooted on the current volume; a drive-relative target remains
// relative and is refused by the rooted component walker if it cannot be
// represented safely.
func linkAbsolutePath(base, value string) (string, bool) {
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
	return value, false
}
