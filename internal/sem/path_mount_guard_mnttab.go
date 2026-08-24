//go:build solaris && cgo

package sem

/*
#include <errno.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mnttab.h>

typedef struct {
	FILE *file;
} entire_graph_mnttab_reader;

static entire_graph_mnttab_reader *entire_graph_mnttab_open(int *problem) {
	entire_graph_mnttab_reader *reader = calloc(1, sizeof (*reader));
	if (reader == NULL) {
		*problem = ENOMEM;
		return NULL;
	}
	errno = 0;
	reader->file = fopen("/etc/mnttab", "r");
	if (reader->file == NULL) {
		*problem = errno != 0 ? errno : EIO;
		free(reader);
		return NULL;
	}
	*problem = 0;
	return reader;
}

static void entire_graph_mnttab_close(entire_graph_mnttab_reader *reader) {
	if (reader == NULL) return;
	if (reader->file != NULL) (void)fclose(reader->file);
	free(reader);
}

// Returns 1 for an entry, 0 for EOF, -1 for an I/O error, -2 for a
// malformed mnttab record, and -3 when the caller's bounded path buffer is
// too small.
static int entire_graph_mnttab_next(
	entire_graph_mnttab_reader *reader,
	char *path,
	size_t capacity,
	size_t *length,
	int *problem
) {
	struct mnttab entry;
	errno = 0;
	int result = getmntent(reader->file, &entry);
	if (result == -1) {
		if (errno != 0 || ferror(reader->file)) {
			*problem = errno != 0 ? errno : EIO;
			return -1;
		}
		*problem = 0;
		return 0;
	}
	if (result != 0) {
		*problem = result;
		return -2;
	}
	if (entry.mnt_mountp == NULL) {
		*problem = EINVAL;
		return -2;
	}
	size_t needed = strlen(entry.mnt_mountp);
	if (needed >= capacity) {
		*problem = ENAMETOOLONG;
		return -3;
	}
	memcpy(path, entry.mnt_mountp, needed + 1);
	*length = needed;
	*problem = 0;
	return 1;
}
*/
import "C"

import (
	"errors"
	"path/filepath"
	"syscall"
	"unsafe"
)

const (
	maxSolarisMountTableRows  = 100_000
	maxSolarisMountTableBytes = 16 << 20
	maxSolarisMountPathBytes  = 1 << 20
)

func newPathMountGuard(root, trustedBase string) (pathMountGuard, error) {
	mountPoints, err := readSolarisMountPoints()
	if err != nil {
		return pathMountGuard{}, err
	}
	return makePathMountGuard(root, trustedBase, mountPoints), nil
}

// Solaris and illumos expose the current zone's kernel mount snapshot through
// MNTFS. Use getmntent instead of splitting /etc/mnttab text: libc preserves
// mount points containing field delimiters via the kernel-provided offsets.
func readSolarisMountPoints() (map[string]struct{}, error) {
	var problem C.int
	reader := C.entire_graph_mnttab_open(&problem)
	if reader == nil {
		return nil, syscall.Errno(problem)
	}
	defer C.entire_graph_mnttab_close(reader)

	path := make([]byte, maxSolarisMountPathBytes+1)
	mounts := make(map[string]struct{})
	totalBytes := 0
	for rows := 0; ; {
		var length C.size_t
		result := C.entire_graph_mnttab_next(
			reader,
			(*C.char)(unsafe.Pointer(&path[0])),
			C.size_t(len(path)),
			&length,
			&problem,
		)
		switch result {
		case 0:
			if _, rooted := mounts[string(filepath.Separator)]; !rooted {
				return nil, errors.New("Solaris mount table omitted the filesystem root")
			}
			return mounts, nil
		case -1:
			return nil, syscall.Errno(problem)
		case -2:
			return nil, errors.New("Solaris mount table contained a malformed entry")
		case -3:
			return nil, errors.New("Solaris mount table contained an overlong mount point")
		case 1:
			if rows >= maxSolarisMountTableRows {
				return nil, errors.New("Solaris mount table exceeded its row bound")
			}
			rows++
		default:
			return nil, errors.New("Solaris mount table reader returned an invalid status")
		}
		if uint64(length) > uint64(len(path)-1) {
			return nil, errors.New("Solaris mount table returned an invalid mount-point length")
		}
		totalBytes += int(length)
		if totalBytes > maxSolarisMountTableBytes {
			return nil, errors.New("Solaris mount table exceeded its byte bound")
		}
		mount := string(path[:int(length)])
		if mount == "" || !filepath.IsAbs(mount) {
			return nil, errors.New("Solaris mount table contained an empty or relative mount point")
		}
		mounts[filepath.Clean(mount)] = struct{}{}
	}
}
