//go:build linux

package sem

// pathMountGuard snapshots Linux's mount table without resolving any
// repository-controlled candidate. sameVolumePathResolver consults it before
// each Lstat/Open, so an autofs or remote mount cannot be activated merely to
// discover that it lives on a different device.
//
// Each resolver takes a fresh snapshot, including independent metadata checks.
// A previous resolver's table can miss mounts added since that resolver began.
func newPathMountGuard(root, trustedBase string) (pathMountGuard, error) {
	return readPathMountGuard(root, trustedBase, readLinuxMountPoints)
}
