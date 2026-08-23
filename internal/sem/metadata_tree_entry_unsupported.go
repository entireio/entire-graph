//go:build !windows && !cgo

package sem

import (
	"errors"
	"os"
)

func readGitMetadataDirectory(
	*os.File,
	string,
	*sameVolumePathResolver,
	int,
	func(string) bool,
) ([]gitMetadataDirectoryEntry, error) {
	return nil, errors.New("safe descriptor-relative metadata enumeration is unavailable on this platform")
}
