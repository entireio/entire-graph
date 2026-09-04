package sem

import (
	"sync"
	"sync/atomic"
)

// mountTableCache holds one platform mount-table snapshot for the operation
// that is running, so the resolvers built while it runs share a read instead of
// repeating one.
//
// Sharing is safe because of what the guard the snapshot feeds actually decides.
// Reading the table takes no repository-controlled input, so a shared snapshot
// still satisfies the ordering pathMountGuard depends on: the table is read
// before any repository-controlled path is resolved. And the table only decides
// whether a candidate is looked at AT ALL. Every hop that survives it is still
// device-checked against the base by pathTraversalAnchor.allows, so a shared
// snapshot cannot make a resolver accept a path a fresh one would reject.
//
// What it gives up is freshness within one operation. A mount created after the
// snapshot is absent from it, so its trigger point can be looked at once; the
// device check still refuses to cross it. Against that: newSameVolumePathResolver
// read and parsed the whole table per resolver, and the git-directory walk builds
// one per path, so a single worktree search of a 1,589-file repository read
// /proc/self/mountinfo 24,948 times and spent 2.9 of 9.9 seconds doing it.
//
// A failed read is deliberately not remembered. Caching it would let one
// transient failure refuse every path for the rest of the operation, where the
// per-resolver read this replaces simply tried again on the next path.
type mountTableCache struct {
	mu     sync.Mutex
	loaded bool
	points map[string]struct{}
}

// load returns the scope's mount points, taking the snapshot on first use.
//
// The lock is held across read so that the workers starting a parallel phase
// take one snapshot between them rather than one each.
//
// The returned set is shared by every caller in the scope and must not be
// mutated. makePathMountGuard, its only consumer, copies what it keeps.
func (c *mountTableCache) load(read func() (map[string]struct{}, error)) (map[string]struct{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		return c.points, nil
	}
	points, err := read()
	if err != nil {
		return nil, err
	}
	c.loaded = true
	c.points = points
	return points, nil
}

// activeMountTable is the scope every platform's newPathMountGuard reads
// through. It is never nil.
var activeMountTable atomic.Pointer[mountTableCache]

func init() {
	activeMountTable.Store(&mountTableCache{})
}

// beginMountTableScope discards the current snapshot so the next resolver takes
// a fresh one. Provider operations call it as they start, which is the point
// before which nothing repository-controlled has been resolved.
//
// A caller that declares no scope reuses whatever snapshot the process last
// took. That is never newer than the one its own first resolver would have read,
// and the device check backing the guard does not depend on it.
func beginMountTableScope() {
	activeMountTable.Store(&mountTableCache{})
}

// cachedMountPoints serves the active scope's snapshot, taking it with read on
// first use. Each platform passes its own mount-table reader.
func cachedMountPoints(read func() (map[string]struct{}, error)) (map[string]struct{}, error) {
	return activeMountTable.Load().load(read)
}
