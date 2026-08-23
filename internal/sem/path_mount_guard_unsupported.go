//go:build !linux && !windows && !darwin && !dragonfly && !freebsd && !openbsd && !netbsd && !(aix && cgo) && !(solaris && cgo)

package sem

func newPathMountGuard(string, string) (pathMountGuard, error) {
	// A post-lookup device comparison is insufficient on a platform where we
	// cannot inventory mount points without resolving the untrusted path.
	return pathMountGuard{}, errPathMountGuardUnsupported
}
