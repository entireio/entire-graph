//go:build !windows

package sem

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// pathTraversalAnchor confines an untrusted pointer walk to the filesystem
// that contains base. On POSIX, filepath.VolumeName is empty even across mount
// boundaries, so a root anchored at / alone would still let a repository name
// an autofs/NFS/SMB mount and trigger network access.
type pathTraversalAnchor struct {
	root         string
	device       uint64
	lexicalBase  string
	resolvedBase string
}

func newPathTraversalAnchor(baseAbs, value string) (pathTraversalAnchor, string, error) {
	resolvedBase, err := filepath.EvalSymlinks(baseAbs)
	if err != nil {
		return pathTraversalAnchor{}, "", err
	}
	baseInfo, err := os.Stat(resolvedBase)
	if err != nil {
		return pathTraversalAnchor{}, "", err
	}
	device, ok := fileSystemDevice(baseInfo)
	if !ok {
		return pathTraversalAnchor{}, "", errSymlinkChainOffVolume
	}

	root := resolvedBase
	for {
		parent := filepath.Dir(root)
		if parent == root {
			break
		}
		parentInfo, statErr := os.Stat(parent)
		if statErr != nil {
			break
		}
		parentDevice, parentOK := fileSystemDevice(parentInfo)
		if !parentOK || parentDevice != device {
			break
		}
		root = parent
	}

	anchor := pathTraversalAnchor{
		root:         root,
		device:       device,
		lexicalBase:  trimTrailingPathSeparators(baseAbs),
		resolvedBase: trimTrailingPathSeparators(resolvedBase),
	}
	mapped := anchor.mapBase(value)
	if !anchor.contains(mapped) {
		return pathTraversalAnchor{}, "", errSymlinkChainOffVolume
	}
	return anchor, mapped, nil
}

func (a pathTraversalAnchor) mapBase(value string) string {
	if value == a.lexicalBase {
		return a.resolvedBase
	}
	prefix := a.lexicalBase + string(filepath.Separator)
	if strings.HasPrefix(value, prefix) {
		return a.resolvedBase + string(filepath.Separator) + value[len(prefix):]
	}
	return value
}

func (a pathTraversalAnchor) contains(value string) bool {
	if value == a.root {
		return true
	}
	if a.root == string(filepath.Separator) {
		return filepath.IsAbs(value)
	}
	return strings.HasPrefix(value, trimTrailingPathSeparators(a.root)+string(filepath.Separator))
}

func (a pathTraversalAnchor) components(value string) ([]string, bool) {
	value = a.mapBase(value)
	if !a.contains(value) {
		return nil, false
	}
	rest := value[len(trimTrailingPathSeparators(a.root)):]
	return splitNativePathComponents(rest), true
}

func (a pathTraversalAnchor) allows(info os.FileInfo) bool {
	device, ok := fileSystemDevice(info)
	return ok && device == a.device
}

func fileSystemDevice(info os.FileInfo) (uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(stat.Dev), true
}
