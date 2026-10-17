//go:build windows

package sem

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLockBuildLockFile takes an exclusive LockFileEx byte-range lock without
// blocking. The lock belongs to the handle, so two opens conflict even inside
// one process, and Windows releases it when the handle closes or the process
// exits.
func tryLockBuildLockFile(file *os.File) error {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &overlapped,
	)
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return errBuildLockBusy
	}
	return err
}

func unlockBuildLockFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}
