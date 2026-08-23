//go:build !linux && !windows && !darwin && !dragonfly && !freebsd && !openbsd

package sem

import "errors"

func newPathMountGuard(string, string) (pathMountGuard, error) {
	// A post-lookup device comparison is insufficient on a platform where we
	// cannot inventory mount points without resolving the untrusted path.
	return pathMountGuard{}, errors.New("safe mount-point inventory is unavailable on this platform")
}
