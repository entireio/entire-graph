//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows)

package sem

import (
	"errors"
	"os"
)

// Platforms without flock or LockFileEx build without the lock: every cold
// query builds on its own, which is the behaviour before the lock existed.
func tryLockBuildLockFile(*os.File) error {
	return errors.New("cache build lock is unsupported on this platform")
}

func unlockBuildLockFile(*os.File) error { return nil }
