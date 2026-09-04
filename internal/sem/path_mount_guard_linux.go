//go:build linux

package sem

// pathMountGuard snapshots Linux's mount table without resolving any
// repository-controlled candidate. sameVolumePathResolver consults it before
// each Lstat/Open, so an autofs or remote mount cannot be activated merely to
// discover that it lives on a different device.
//
// The snapshot is taken once per operation and shared, not taken per resolver.
// See mountTableCache for why sharing preserves the property above.
func newPathMountGuard(root, trustedBase string) (pathMountGuard, error) {
	mountPoints, err := cachedMountPoints(readLinuxMountPoints)
	if err != nil {
		return pathMountGuard{}, err
	}
	return makePathMountGuard(root, trustedBase, mountPoints), nil
}
