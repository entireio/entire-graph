//go:build windows

package sem

import (
	"os"
	"path/filepath"
)

type pathTraversalAnchor struct {
	root string
}

func newPathTraversalAnchor(baseAbs, value string) (pathTraversalAnchor, string, error) {
	if !sameVolume(value, baseAbs) {
		return pathTraversalAnchor{}, "", errSymlinkChainOffVolume
	}
	volume := filepath.VolumeName(baseAbs)
	root := string(filepath.Separator)
	if volume != "" {
		root = volume + string(filepath.Separator)
	}
	return pathTraversalAnchor{root: root}, value, nil
}

func (a pathTraversalAnchor) components(value string) ([]string, bool) {
	if !sameVolume(value, a.root) {
		return nil, false
	}
	rest := value[len(filepath.VolumeName(value)):]
	return splitNativePathComponents(rest), true
}

func (a pathTraversalAnchor) allows(os.FileInfo) bool { return true }
