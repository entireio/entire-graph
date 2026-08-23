//go:build !windows

package sem

import "os"

func pathMayRedirect(info os.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}
