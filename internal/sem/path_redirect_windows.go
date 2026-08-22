//go:build windows

package sem

import "os"

// Windows exposes symlinks as ModeSymlink, but directory junctions and mount
// points as ModeIrregular. Root.Readlink distinguishes name-surrogate reparses
// from data-like reparse tags; a ModeIrregular entry it cannot read as a link is
// refused by the caller instead of being followed opaquely.
func pathMayRedirect(info os.FileInfo) bool {
	return info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0
}
