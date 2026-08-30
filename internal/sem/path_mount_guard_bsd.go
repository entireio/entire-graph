//go:build darwin || dragonfly || freebsd || openbsd

package sem

import (
	"errors"
	"path/filepath"
	"syscall"
)

const (
	bsdMountNowait       = 2
	maxBSDMountTableRows = 100_000
)

func newPathMountGuard(root, trustedBase string) (pathMountGuard, error) {
	mountPoints, err := readBSDMountPoints()
	if err != nil {
		return pathMountGuard{}, err
	}
	return makePathMountGuard(root, trustedBase, mountPoints), nil
}

func readBSDMountPoints() (map[string]struct{}, error) {
	for attempt := 0; attempt < 3; attempt++ {
		count, err := syscall.Getfsstat(nil, bsdMountNowait)
		if err != nil {
			return nil, err
		}
		if count < 0 || count > maxBSDMountTableRows {
			return nil, errors.New("BSD mount table exceeded its row bound")
		}
		// Leave slack so a mount added between the sizing and snapshot calls
		// does not turn a silently truncated table into a permissive guard.
		rows := make([]syscall.Statfs_t, count+16)
		got, err := syscall.Getfsstat(rows, bsdMountNowait)
		if err != nil {
			return nil, err
		}
		if got < 0 || got > len(rows) {
			return nil, errors.New("invalid BSD mount table row count")
		}
		if got == len(rows) {
			continue
		}
		mounts := make(map[string]struct{}, got)
		for _, row := range rows[:got] {
			mount, terminated := bsdMountPath(bsdMountedOn(row))
			if !terminated || mount == "" || !filepath.IsAbs(mount) {
				return nil, errors.New("BSD mount table contained an invalid mount point")
			}
			mounts[filepath.Clean(mount)] = struct{}{}
		}
		return mounts, nil
	}
	return nil, errors.New("BSD mount table changed too quickly to snapshot")
}

func bsdMountPath(value []int8) (string, bool) {
	bytes := make([]byte, 0, len(value))
	for _, character := range value {
		if character == 0 {
			return string(bytes), true
		}
		bytes = append(bytes, byte(character))
	}
	return "", false
}
