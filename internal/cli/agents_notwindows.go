//go:build !windows

package cli

import "os"

func windowsLinkRequiresDirectory(os.FileInfo) (bool, bool) {
	return false, false
}

func windowsRawReparseTarget(string, string, os.FileInfo) (string, bool, error) {
	return "", false, nil
}

func windowsRawReparseTargetAtPath(string, os.FileInfo) (string, bool, error) {
	return "", false, nil
}
