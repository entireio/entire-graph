//go:build !windows

package bench

import (
	"runtime"
	"syscall"
)

func maxRSSBytesCurrent() uint64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	rss := uint64(ru.Maxrss)
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	return rss
}
