package sem

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cacheVersionGracePeriod keeps a stale version directory that was written
// recently. Another process running an older build may still be using it: the
// directory's mtime moves whenever that build adds an entry, so a directory
// untouched for this long is not in active use. It also keeps this binary from
// deleting a sibling version that a concurrently starting older process just
// created.
const cacheVersionGracePeriod = 24 * time.Hour

// cacheVersionFamilies lists the family directories under a cache root and the
// one version directory in each that this build can address. Every other
// directory matching the family's prefix is unreachable: the version is hashed
// into every entry key, so no lookup can ever name an entry under another
// version.
var cacheVersionFamilies = []struct {
	family  string
	current string
	prefix  string
}{
	{family: "search", current: searchSnapshotCacheVersion, prefix: "search-snapshot-"},
	{family: "records", current: providerRecordsCacheVersion, prefix: "provider-records-"},
}

// PruneStaleCacheVersions removes cache version directories this build cannot
// address. A cache version is hashed into every entry key, so bumping it moves
// new entries to a fresh directory and leaves the old one unreachable; without
// this, every bump keeps its predecessor's bytes forever (an observed cache
// root was 95% dead versions). Pruning is best-effort: a cache is an
// optimization, so an unreadable root, a locked file, or a race with another
// process is ignored rather than reported.
func PruneStaleCacheVersions(cacheDir string) {
	if cacheDir == "" {
		return
	}
	now := time.Now()
	for _, family := range cacheVersionFamilies {
		dir := filepath.Join(cacheDir, family.family)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			// entry.IsDir is false for a symlink, so a link planted in the cache
			// is never followed by RemoveAll.
			if !entry.IsDir() || name == family.current || !strings.HasPrefix(name, family.prefix) {
				continue
			}
			info, err := entry.Info()
			if err != nil || now.Sub(info.ModTime()) < cacheVersionGracePeriod {
				continue
			}
			_ = os.RemoveAll(filepath.Join(dir, name))
		}
	}
}
