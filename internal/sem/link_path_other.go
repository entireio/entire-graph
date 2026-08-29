//go:build !windows

package sem

import "path/filepath"

func linkAbsolutePath(_ string, value string) (string, bool) {
	return value, filepath.IsAbs(value)
}
