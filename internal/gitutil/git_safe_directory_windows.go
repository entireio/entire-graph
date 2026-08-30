//go:build windows

package gitutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// gitPhysicalDirectory returns the final path assigned to a followed directory
// handle. filepath.EvalSymlinks intentionally stopped resolving Windows mount
// points, including directory junctions, in Go 1.23; Git still canonicalizes a
// checkout reached through one before applying safe.directory.
func gitPhysicalDirectory(path string) (string, error) {
	opened, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer opened.Close()

	const maxWindowsPathUTF16 = 32_768
	buffer := make([]uint16, 512)
	for {
		n, err := windows.GetFinalPathNameByHandle(
			windows.Handle(opened.Fd()),
			&buffer[0],
			uint32(len(buffer)),
			0,
		)
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "", fmt.Errorf("GetFinalPathNameByHandleW returned an empty path")
		}
		if n >= uint32(len(buffer)) {
			if n >= maxWindowsPathUTF16 {
				return "", fmt.Errorf("GetFinalPathNameByHandleW path exceeds %d UTF-16 code units", maxWindowsPathUTF16-1)
			}
			buffer = make([]uint16, int(n)+1)
			continue
		}

		resolved := windows.UTF16ToString(buffer[:n])
		switch {
		case strings.HasPrefix(resolved, `\\?\UNC\`):
			resolved = `\\` + resolved[len(`\\?\UNC\`):]
		case strings.HasPrefix(resolved, `\\?\`):
			resolved = resolved[len(`\\?\`):]
		default:
			return "", fmt.Errorf("GetFinalPathNameByHandleW returned an unexpected path %q", resolved)
		}
		if !filepath.IsAbs(resolved) {
			return "", fmt.Errorf("GetFinalPathNameByHandleW returned a non-absolute path %q", resolved)
		}
		return filepath.Clean(resolved), nil
	}
}
