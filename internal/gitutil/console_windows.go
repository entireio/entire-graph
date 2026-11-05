//go:build windows

package gitutil

import (
	"os/exec"
	"syscall"
)

var procGetConsoleWindow = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow")

// HideConsoleWindow keeps cmd from opening a visible console window when
// entire-graph itself has none. Windows gives a console program a new, visible
// console when its parent has no console, so a status line render started
// without one would flash a window, and steal focus, for every subprocess.
//
// When the current process has a console window, cmd is left alone: a
// terminal user keeps one console shared with every child, so Ctrl-C still
// reaches the whole tree and no extra conhost.exe starts per call. Otherwise
// CREATE_NO_WINDOW is ORed into the creation flags and HideWindow is set; every
// other SysProcAttr field is kept.
func HideConsoleWindow(cmd *exec.Cmd) {
	if window, _, _ := procGetConsoleWindow.Call(); window != 0 {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
	cmd.SysProcAttr.HideWindow = true
}
