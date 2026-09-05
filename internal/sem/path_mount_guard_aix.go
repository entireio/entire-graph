//go:build aix && cgo

package sem

/*
#include <errno.h>
#include <stddef.h>
#include <string.h>
#include <sys/types.h>
// Some AIX sys/types.h modes omit the uint alias required by sys/vmount.h.
typedef uint_t uint;
#include <sys/mntctl.h>
#include <sys/vmount.h>

static int entire_graph_aix_mount_size(int *required) {
	int probe = 0;
	errno = 0;
	int count = mntctl(MCTL_QUERY, sizeof (probe), (char *)&probe);
	if (count < 0) return errno != 0 ? errno : EIO;
	if (count != 0 || probe <= 0) return EINVAL;
	*required = probe;
	return 0;
}

// A zero count means a mount was added after sizing. mntctl writes the new
// required byte count into the first word of the buffer in that case.
static int entire_graph_aix_mount_read(
	char *buffer,
	int capacity,
	int *count,
	int *required
) {
	errno = 0;
	int result = mntctl(MCTL_QUERY, capacity, buffer);
	if (result < 0) return errno != 0 ? errno : EIO;
	if (result == 0) {
		memcpy(required, buffer, sizeof (*required));
		return ENOSPC;
	}
	*count = result;
	*required = capacity;
	return 0;
}

static int entire_graph_aix_mount_entry(
	const char *buffer,
	size_t capacity,
	size_t offset,
	char *path,
	size_t path_capacity,
	size_t *path_length,
	size_t *next_offset
) {
	if (offset > capacity || capacity - offset < sizeof (struct vmount)) return EINVAL;
	const struct vmount *entry = (const struct vmount *)(buffer + offset);
	if (entry->vmt_length < sizeof (struct vmount) ||
	    (entry->vmt_length & 3) != 0 ||
	    (size_t)entry->vmt_length > capacity - offset) return EINVAL;

	int stub_offset = entry->vmt_data[VMT_STUB].vmt_off;
	int stub_size = entry->vmt_data[VMT_STUB].vmt_size;
	if (stub_offset < (int)sizeof (struct vmount) || stub_size <= 0 ||
	    stub_offset > entry->vmt_length ||
	    stub_size > entry->vmt_length - stub_offset) return EINVAL;

	const char *stub = (const char *)entry + stub_offset;
	const char *terminator = memchr(stub, '\0', (size_t)stub_size);
	if (terminator == NULL) return EINVAL;
	size_t length = (size_t)(terminator - stub);
	if (length == 0) return EINVAL;
	if (length >= path_capacity) return ENAMETOOLONG;
	memcpy(path, stub, length + 1);
	*path_length = length;
	*next_offset = offset + (size_t)entry->vmt_length;
	return 0;
}
*/
import "C"

import (
	"errors"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	maxAIXMountTableRows  = 100_000
	maxAIXMountTableBytes = 16 << 20
	maxAIXMountPathBytes  = 1 << 20
)

func newPathMountGuard(root, trustedBase string) (pathMountGuard, error) {
	return readPathMountGuard(root, trustedBase, readAIXMountPoints)
}

// mntctl returns the live kernel VFS inventory. Parsing the bounded vmount
// records avoids both repository path lookups and the incomplete static
// /etc/filesystems configuration.
func readAIXMountPoints() (map[string]struct{}, error) {
	for attempt := 0; attempt < 3; attempt++ {
		var required C.int
		if problem := C.entire_graph_aix_mount_size(&required); problem != 0 {
			return nil, syscall.Errno(problem)
		}
		if required <= 0 || int64(required) > maxAIXMountTableBytes {
			return nil, errors.New("AIX mount table exceeded its byte bound")
		}
		buffer := make([]byte, int(required))
		var count, nextRequired C.int
		problem := C.entire_graph_aix_mount_read(
			(*C.char)(unsafe.Pointer(&buffer[0])),
			C.int(len(buffer)),
			&count,
			&nextRequired,
		)
		runtime.KeepAlive(buffer)
		if problem == C.ENOSPC {
			if nextRequired <= required || int64(nextRequired) > maxAIXMountTableBytes {
				return nil, errors.New("AIX mount table changed with an invalid size")
			}
			continue
		}
		if problem != 0 {
			return nil, syscall.Errno(problem)
		}
		if count <= 0 || int64(count) > maxAIXMountTableRows {
			return nil, errors.New("AIX mount table exceeded its row bound")
		}

		mounts := make(map[string]struct{}, int(count))
		path := make([]byte, maxAIXMountPathBytes+1)
		var offset C.size_t
		for row := 0; row < int(count); row++ {
			var length, next C.size_t
			problem := C.entire_graph_aix_mount_entry(
				(*C.char)(unsafe.Pointer(&buffer[0])),
				C.size_t(len(buffer)),
				offset,
				(*C.char)(unsafe.Pointer(&path[0])),
				C.size_t(len(path)),
				&length,
				&next,
			)
			runtime.KeepAlive(buffer)
			if problem == C.ENAMETOOLONG {
				return nil, errors.New("AIX mount table contained an overlong mount point")
			}
			if problem != 0 {
				return nil, errors.New("AIX mount table contained a malformed entry")
			}
			if uint64(length) > uint64(len(path)-1) || next <= offset {
				return nil, errors.New("AIX mount table returned invalid entry bounds")
			}
			mount := string(path[:int(length)])
			if !filepath.IsAbs(mount) {
				return nil, errors.New("AIX mount table contained a relative mount point")
			}
			mounts[filepath.Clean(mount)] = struct{}{}
			offset = next
		}
		if _, rooted := mounts[string(filepath.Separator)]; !rooted {
			return nil, errors.New("AIX mount table omitted the filesystem root")
		}
		return mounts, nil
	}
	return nil, errors.New("AIX mount table changed too quickly to snapshot")
}
