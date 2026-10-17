//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package sem

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLockBuildLockFile takes an exclusive flock without blocking. flock locks
// belong to the open file description, so two opens conflict even inside one
// process, and the kernel releases the lock when the last descriptor closes,
// including on process death.
func tryLockBuildLockFile(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return errBuildLockBusy
		}
		return err
	}
}

func unlockBuildLockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
