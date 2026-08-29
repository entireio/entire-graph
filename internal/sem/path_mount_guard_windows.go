//go:build windows

package sem

// Windows mount points and network redirects are reparse points. The resolver
// sees their metadata without following them, validates their raw target, and
// rejects a target whose volume differs before opening it.
func newPathMountGuard(string, string) (pathMountGuard, error) { return pathMountGuard{}, nil }
