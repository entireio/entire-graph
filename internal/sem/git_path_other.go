//go:build !windows

package sem

import "path/filepath"

func gitAbsolutePath(_ string, value string) (string, bool) {
	return value, filepath.IsAbs(value)
}
