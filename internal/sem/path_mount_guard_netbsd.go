//go:build netbsd

package sem

import (
	"bytes"
	"errors"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const maxNetBSDMountTableRows = 100_000

func newPathMountGuard(root, trustedBase string) (pathMountGuard, error) {
	mountPoints, err := readNetBSDMountPoints()
	if err != nil {
		return pathMountGuard{}, err
	}
	return makePathMountGuard(root, trustedBase, mountPoints), nil
}

// Getvfsstat reads the kernel's mount inventory without looking up any
// repository-controlled pathname. ST_NOWAIT also avoids refreshing remote
// filesystem statistics while taking the snapshot.
func readNetBSDMountPoints() (map[string]struct{}, error) {
	for attempt := 0; attempt < 3; attempt++ {
		count, err := unix.Getvfsstat(nil, unix.ST_NOWAIT)
		if err != nil {
			return nil, err
		}
		if count < 0 || count > maxNetBSDMountTableRows {
			return nil, errors.New("NetBSD mount table exceeded its row bound")
		}
		// Leave slack so a mount added between the sizing and snapshot calls
		// causes a retry instead of a silently incomplete inventory.
		rows := make([]unix.Statvfs_t, count+16)
		got, err := unix.Getvfsstat(rows, unix.ST_NOWAIT)
		if err != nil {
			return nil, err
		}
		if got < 0 || got > maxNetBSDMountTableRows {
			return nil, errors.New("invalid NetBSD mount table row count")
		}
		if got >= len(rows) {
			continue
		}
		mounts := make(map[string]struct{}, got)
		for _, row := range rows[:got] {
			end := bytes.IndexByte(row.Mntonname[:], 0)
			if end <= 0 {
				return nil, errors.New("NetBSD mount table contained an unterminated or empty mount point")
			}
			mount := string(row.Mntonname[:end])
			if !filepath.IsAbs(mount) {
				return nil, errors.New("NetBSD mount table contained a relative mount point")
			}
			mounts[filepath.Clean(mount)] = struct{}{}
		}
		if _, rooted := mounts[string(filepath.Separator)]; !rooted {
			return nil, errors.New("NetBSD mount table omitted the filesystem root")
		}
		return mounts, nil
	}
	return nil, errors.New("NetBSD mount table changed too quickly to snapshot")
}
