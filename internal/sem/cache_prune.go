package sem

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"
)

// cacheVersionGracePeriod keeps an older version directory that was used
// recently. Another process running an older build may still depend on it, so
// only a directory with no recorded use for this long is reclaimed.
const cacheVersionGracePeriod = 24 * time.Hour

const (
	// cacheVersionUseMarkerName is the file a build touches inside its own
	// version directory to record that it used the cache. The directory's own
	// mtime cannot say that: it moves only when a top-level entry is added,
	// renamed or removed, so a build that is served nothing but hits leaves it
	// untouched and would look idle to a newer build sharing the cache root.
	cacheVersionUseMarkerName = "last-used.marker"
	// cacheVersionUseMarkInterval bounds how often the marker is rewritten. It
	// only has to be much shorter than the grace period; within it a use costs
	// one Lstat and no write.
	cacheVersionUseMarkInterval = time.Hour
)

// cacheVersionFamilies lists the family directories under a cache root, the
// version directory in each that this build addresses, and the prefix every
// version directory of that family carries.
var cacheVersionFamilies = []struct {
	family  string
	current string
	prefix  string
}{
	{family: "search", current: searchSnapshotCacheVersion, prefix: "search-snapshot-"},
	{family: "records", current: providerRecordsCacheVersion, prefix: "provider-records-"},
}

// cacheVersionMaintenance throttles marking and pruning inside one process, so
// a command that reaches the cache through several entry points (a preindex
// that also runs the query pipeline, a query that also streams records) pays
// for each once, and a long-lived process repeats them once per interval.
var cacheVersionMaintenance sync.Map // string -> time.Time

func cacheVersionMaintenanceDue(key string, now time.Time) bool {
	if last, ok := cacheVersionMaintenance.Load(key); ok && now.Sub(last.(time.Time)) < cacheVersionUseMarkInterval {
		return false
	}
	cacheVersionMaintenance.Store(key, now)
	return true
}

// noteCacheVersionRead records that this build read from its version directory
// without pruning anything. It is for the paths that resolve an existing
// artifact and never build (`graph store`, `graph context`): they are exactly
// the hit-only use a directory mtime does not show.
func noteCacheVersionRead(cacheDir, family, version string) {
	if cacheDir == "" {
		return
	}
	now := time.Now()
	if cacheVersionMaintenanceDue("mark\x00"+cacheDir+"\x00"+family+"\x00"+version, now) {
		markCacheVersionUsed(cacheDir, family, version, now)
	}
}

// noteCacheVersionUse is called once a command has committed to the cache for a
// family: it records the use and then reclaims older, idle version directories.
// Both halves are best-effort and throttled per process.
func noteCacheVersionUse(cacheDir, family, version string) {
	if cacheDir == "" {
		return
	}
	noteCacheVersionRead(cacheDir, family, version)
	if cacheVersionMaintenanceDue("prune\x00"+cacheDir, time.Now()) {
		PruneStaleCacheVersions(cacheDir)
	}
}

// markCacheVersionUsed touches the use marker in an existing version directory,
// at most once per cacheVersionUseMarkInterval across processes. It never
// creates the directory: a version with no entries yet has nothing to protect,
// and the write that creates it gives it a fresh mtime.
func markCacheVersionUsed(cacheDir, family, version string, now time.Time) {
	root, err := os.OpenRoot(cacheDir)
	if err != nil {
		return
	}
	defer root.Close()
	familyDirectory, err := openExistingCacheComponent(root, family)
	if err != nil {
		return
	}
	defer familyDirectory.Close()
	directory, err := openExistingCacheComponent(familyDirectory, version)
	if err != nil {
		return
	}
	defer directory.Close()
	info, err := directory.Lstat(cacheVersionUseMarkerName)
	switch {
	case err == nil && info.Mode().IsRegular():
		if now.Sub(info.ModTime()) >= cacheVersionUseMarkInterval {
			_ = directory.Chtimes(cacheVersionUseMarkerName, now, now)
		}
	case errors.Is(err, fs.ErrNotExist):
		// O_EXCL: a link planted at the marker name is refused, not followed.
		file, err := directory.OpenFile(cacheVersionUseMarkerName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_ = file.Close()
		}
	}
}

// openExistingCacheComponent opens the directory name beneath parent under the
// same redirect rules cache writes apply (see cacheEntry.write), but without
// creating it: maintenance must not materialize a cache that is not there. A
// symlink, a Windows junction, or anything that is not a directory is refused.
func openExistingCacheComponent(parent *os.Root, name string) (*os.Root, error) {
	named, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if err := cacheComponentRedirectError(name, named); err != nil {
		return nil, err
	}
	if !named.IsDir() {
		return nil, fmt.Errorf("cache directory component %q is not a directory", name)
	}
	held, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	if err := refuseRedirectingCacheComponent(parent, name, held); err != nil {
		_ = held.Close()
		return nil, err
	}
	return held, nil
}

// cacheVersionOrder is the comparable part of a version directory name:
// "<prefix>v<generation>-<identity revision>".
type cacheVersionOrder struct {
	generation int
	revision   int
}

// parseCacheVersion reads the two counters out of a version directory name.
// Names written before the identity revision existed are "<prefix>v<N>" and
// read as revision 0. Anything else is not a name this code can order.
func parseCacheVersion(name, prefix string) (cacheVersionOrder, bool) {
	rest, ok := strings.CutPrefix(name, prefix+"v")
	if !ok {
		return cacheVersionOrder{}, false
	}
	generationText, revisionText, hasRevision := strings.Cut(rest, "-")
	generation, ok := parseCacheVersionCounter(generationText)
	if !ok {
		return cacheVersionOrder{}, false
	}
	if !hasRevision {
		return cacheVersionOrder{generation: generation}, true
	}
	revision, ok := parseCacheVersionCounter(revisionText)
	if !ok {
		return cacheVersionOrder{}, false
	}
	return cacheVersionOrder{generation: generation, revision: revision}, true
}

func parseCacheVersionCounter(text string) (int, bool) {
	if text == "" || len(text) > 9 {
		return 0, false
	}
	value := 0
	for index := 0; index < len(text); index++ {
		if text[index] < '0' || text[index] > '9' {
			return 0, false
		}
		value = value*10 + int(text[index]-'0')
	}
	return value, true
}

// olderThan reports whether a version is provably older than current: neither
// counter is ahead and at least one is behind. Both counters only ever move
// forward on a line of development, so a name with either one ahead was written
// by a newer build or a divergent branch, and is not this build's to judge.
func (version cacheVersionOrder) olderThan(current cacheVersionOrder) bool {
	return version.generation <= current.generation && version.revision <= current.revision && version != current
}

// PruneStaleCacheVersions removes version directories that are older than the
// one this build addresses and show no use within cacheVersionGracePeriod. A
// cache version is hashed into every entry key, so bumping it moves new entries
// to a fresh directory and this build never reads the old one again; without
// this, every bump keeps its predecessor's bytes forever (an observed cache
// root was 95% dead versions).
//
// "Older and idle" is deliberately narrower than "not this build's":
//
//   - A directory whose name is not provably older (a newer version, a
//     divergent branch's, a name that does not parse) is never removed. A build
//     sharing the cache root with a newer one (an installed plugin beside a dev
//     build, a shared --cache-dir, a downgrade) must not delete the newer
//     build's live cache.
//   - Use is the later of the directory's mtime and its use marker. A build
//     that carries this code touches the marker whenever it uses the cache, so
//     a hit-only older build is seen. A build that predates the marker is seen
//     only when it writes a top-level entry; one that has done nothing but hit
//     for the whole grace period loses its entries and rebuilds them.
//   - A directory holding a lock file another process has locked is skipped:
//     that process is mid-build or mid-publish there, and unlinking a held lock
//     file breaks the lock (see acquireCompleteBuildLock).
//
// Every directory is opened beneath the cache root with the redirect checks
// cache writes use, so a symlinked or junction family or version component is
// refused rather than followed. The remaining window is a process of an older
// build that starts using its directory between these checks and the removal:
// it finds its entries gone, which every cache reader already treats as a miss.
//
// Pruning is best-effort: a cache is an optimization, so an unreadable root, a
// file that cannot be removed, or a race with another process is ignored rather
// than reported.
func PruneStaleCacheVersions(cacheDir string) {
	if cacheDir == "" {
		return
	}
	root, err := os.OpenRoot(cacheDir)
	if err != nil {
		return
	}
	defer root.Close()
	now := time.Now()
	for _, family := range cacheVersionFamilies {
		current, ok := parseCacheVersion(family.current, family.prefix)
		if !ok {
			continue
		}
		directory, err := openExistingCacheComponent(root, family.family)
		if err != nil {
			continue
		}
		for _, name := range cacheDirectoryNames(directory) {
			version, ok := parseCacheVersion(name, family.prefix)
			if !ok || !version.olderThan(current) || !cacheVersionIdle(directory, name, now) {
				continue
			}
			// The version handle is closed again by now (Windows does not remove
			// an open directory). RemoveAll on an os.Root does not follow a link
			// swapped in at name since the check; it removes the link.
			_ = directory.RemoveAll(name)
		}
		_ = directory.Close()
	}
}

func cacheDirectoryNames(directory *os.Root) []string {
	handle, err := directory.Open(".")
	if err != nil {
		return nil
	}
	names, _ := handle.Readdirnames(-1)
	_ = handle.Close()
	return names
}

// cacheVersionIdle reports whether the version directory name beneath family
// is a real directory with no use inside the grace period and no held lock.
// Whatever it cannot establish counts as in use.
func cacheVersionIdle(family *os.Root, name string, now time.Time) bool {
	directory, err := openExistingCacheComponent(family, name)
	if err != nil {
		return false
	}
	defer directory.Close()
	info, err := directory.Stat(".")
	if err != nil || now.Sub(info.ModTime()) < cacheVersionGracePeriod {
		return false
	}
	marker, err := directory.Lstat(cacheVersionUseMarkerName)
	if err == nil && now.Sub(marker.ModTime()) < cacheVersionGracePeriod {
		return false
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	return !cacheVersionHoldsLiveLock(directory)
}

// cacheVersionHoldsLiveLock probes every lock file in a version directory: the
// per-key build locks and the search store retention lock all live at its top
// level. A lock is live exactly when taking it reports errBuildLockBusy, which
// is the lock's own definition of held; the kernel frees a dead holder's lock,
// so a leftover file from a finished or crashed build is not live. Each probe
// is released at once. A lock that cannot be opened counts as live; one whose
// platform has no locking cannot be held by anyone.
func cacheVersionHoldsLiveLock(directory *os.Root) bool {
	for _, name := range cacheDirectoryNames(directory) {
		if !strings.HasSuffix(name, ".lock") {
			continue
		}
		info, err := directory.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		file, err := directory.OpenFile(name, os.O_RDWR, 0)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return true
		}
		lockErr := tryLockBuildLockFile(file)
		if lockErr == nil {
			_ = unlockBuildLockFile(file)
		}
		_ = file.Close()
		if errors.Is(lockErr, errBuildLockBusy) {
			return true
		}
	}
	return false
}
