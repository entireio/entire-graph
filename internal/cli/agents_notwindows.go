//go:build !windows

package cli

import "os"

func windowsLinkRequiresDirectory(os.FileInfo) (bool, bool) {
	return false, false
}
