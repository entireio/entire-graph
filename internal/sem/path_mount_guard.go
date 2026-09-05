package sem

import (
	"errors"
	"path/filepath"
	"strings"
)

var errPathMountGuardUnsupported = errors.New("safe mount-point inventory is unavailable on this platform")

// pathMountGuard confines candidate lookups to the deepest mounted filesystem
// that already contains the explicitly selected repository. Reaching that
// mount is trusted; entering a nested mount, or walking sideways back out of
// it, is not.
type pathMountGuard struct {
	enabled       bool
	root          string
	selectedMount string
	mountPoints   map[string]struct{}
	mountKeys     map[string]struct{}
}

// readPathMountGuard takes a fresh inventory for each resolver. A table retained
// from an earlier resolver can miss a newly added mount, allowing Lstat/Open to
// touch it before the device check can reject it. In particular, independent
// Git metadata validations have no surrounding snapshot/search operation.
// Keep the reader explicit so freshness and failure handling can be tested
// without mounting a filesystem or changing process-global state.
func readPathMountGuard(root, trustedBase string, read func() (map[string]struct{}, error)) (pathMountGuard, error) {
	mountPoints, err := read()
	if err != nil {
		return pathMountGuard{}, err
	}
	return makePathMountGuard(root, trustedBase, mountPoints), nil
}

func makePathMountGuard(root, trustedBase string, mountPoints map[string]struct{}) pathMountGuard {
	root = filepath.Clean(root)
	trustedBase = filepath.Clean(trustedBase)
	guard := pathMountGuard{
		enabled:       true,
		root:          root,
		selectedMount: root,
		mountPoints:   make(map[string]struct{}, len(mountPoints)),
		mountKeys:     make(map[string]struct{}, len(mountPoints)),
	}
	for mount := range mountPoints {
		mount = filepath.Clean(mount)
		guard.addMountPoint(mount)
		if mountPathContainsExact(mount, trustedBase) && len(splitNativePathComponents(mount)) > len(splitNativePathComponents(guard.selectedMount)) {
			guard.selectedMount = mount
		}
	}
	return guard
}

func (g *pathMountGuard) addMountPoint(mount string) {
	if g.mountPoints == nil {
		g.mountPoints = make(map[string]struct{})
	}
	if g.mountKeys == nil {
		g.mountKeys = make(map[string]struct{})
	}
	mount = filepath.Clean(mount)
	g.mountPoints[mount] = struct{}{}
	g.mountKeys[mountPathKey(mount)] = struct{}{}
}

func (g pathMountGuard) beforeLookup(rel string) error {
	if !g.enabled {
		return nil
	}
	candidate := filepath.Clean(filepath.Join(g.root, rel))
	if mountPathContainsExact(candidate, g.selectedMount) {
		// These are the already-trusted ancestors needed to reach the selected
		// mount, including the selected mount itself.
		return nil
	}
	if !mountPathContainsExact(g.selectedMount, candidate) {
		return errSymlinkChainOffVolume
	}
	if _, mounted := g.mountKeys[mountPathKey(candidate)]; mounted {
		return errPathCrossesKnownMount
	}
	return nil
}

// Mount tables use the filesystem's canonical spelling. Conservatively fold
// case and Unicode normalization for REJECTION so an alias accepted by a
// case-insensitive or normalizing filesystem cannot bypass a pre-lookup check.
// Trust selection and containment stay exact: folding must never make a
// distinct case-sensitive path look like the explicitly selected mount.
func mountPathKey(path string) string {
	return filepath.ToSlash(unicodeFoldedNFDKey(filepath.Clean(path)))
}

func mountPathContainsExact(root, target string) bool {
	root = strings.TrimSuffix(filepath.ToSlash(filepath.Clean(root)), "/")
	target = filepath.ToSlash(filepath.Clean(target))
	return target == root || strings.HasPrefix(target, root+"/")
}
