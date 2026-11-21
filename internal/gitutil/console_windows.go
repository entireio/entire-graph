//go:build windows

package gitutil

import (
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

var procGetConsoleProcessList = modkernel32.NewProc("GetConsoleProcessList")

// hideConsoleWindow keeps cmd from opening a visible console window when
// entire-graph itself has no console attached. Windows gives a console program
// a new, visible console when its parent has none, so a status line render
// started without one would flash a window, and steal focus, for every
// subprocess.
//
// When the current process has any console attached (visible, windowless or a
// pseudoconsole), cmd is left alone: the child shares that console, so Ctrl-C
// still reaches the whole tree and no extra conhost.exe starts per call.
// Otherwise CREATE_NO_WINDOW is ORed into the creation flags and HideWindow is
// set; every other SysProcAttr field is kept.
func hideConsoleWindow(cmd *exec.Cmd) {
	if consoleAttached() {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
	cmd.SysProcAttr.HideWindow = true
}

// consoleAttached reports whether this process is attached to a console.
// GetConsoleProcessList counts the processes attached to the caller's console,
// which includes the caller, and returns 0 only when there is no console. A
// windowless console has no window but still answers here, so it is not
// mistaken for "no console" the way GetConsoleWindow() == 0 would be.
//
// The answer is read once per process. entire-graph never allocates,
// attaches to or frees a console, so it cannot change during a run, and
// every Git command a run builds would otherwise repeat the call.
var consoleAttached = sync.OnceValue(func() bool {
	var processes [1]uint32
	count, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&processes[0])), uintptr(len(processes)))
	return count != 0
})
