//go:build !windows

package gitutil

import "os/exec"

// HideConsoleWindow keeps cmd from opening a visible console window on
// Windows. Other platforms have no console windows, so it does nothing.
func HideConsoleWindow(cmd *exec.Cmd) {}
