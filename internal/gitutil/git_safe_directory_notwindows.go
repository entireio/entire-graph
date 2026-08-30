//go:build !windows

package gitutil

import "path/filepath"

func gitPhysicalDirectory(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
