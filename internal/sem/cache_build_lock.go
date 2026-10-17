package sem

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// completeBuildLockWait bounds how long a cold query waits for ANOTHER process
// that is already building the same complete snapshot. Past it the waiter builds
// on its own, exactly as it would have with no lock, so a wedged holder costs a
// duplicate build and never a hung query. A var so a test can shorten it.
var completeBuildLockWait = 60 * time.Second

// completeBuildLockPollMin and completeBuildLockPollMax are the first and the
// largest interval between attempts to take a contended build lock.
const (
	completeBuildLockPollMin = 10 * time.Millisecond
	completeBuildLockPollMax = 250 * time.Millisecond
)

// errBuildLockBusy is what a platform tryLock reports for a lock some other
// open file description holds. Every other failure means the lock is unusable
// here, and the caller builds without it.
var errBuildLockBusy = errors.New("build lock is held by another process")

// beforeColdCompleteBuild, when set, runs immediately before a query builds the
// complete snapshot on a miss. It is a test seam only: it lets a test count the
// builds that actually happen and widen the window in which concurrent queries
// would otherwise duplicate one.
var beforeColdCompleteBuild func()

// completeBuildLock is a held, advisory, cross-process lock on one complete
// snapshot cache key. A nil lock is valid and releases nothing: it is what a
// caller gets when the lock could not be taken and it proceeds unlocked.
type completeBuildLock struct {
	file *os.File
}

func (lock *completeBuildLock) release() {
	if lock == nil || lock.file == nil {
		return
	}
	_ = unlockBuildLockFile(lock.file)
	_ = lock.file.Close()
	lock.file = nil
}

// acquireCompleteBuildLock serialises the cold build of one complete snapshot
// across processes. Parallel agents each running `entire graph query --head`
// against the same uncached tree otherwise all miss, and each pays the whole
// build for an artifact only one of them needs to write.
//
// The lock is an OS file lock (flock, or LockFileEx on Windows) on a file
// beside the entry it guards, so the kernel drops it when its holder exits or
// crashes: a lock left by a killed process is simply free, with no pid file to
// judge stale. The file itself is never removed, because unlinking a lock file
// lets a late opener lock a fresh inode while an earlier holder still holds the
// old one.
//
// It is strictly an optimisation and never a requirement. It returns nil, and
// the caller builds unlocked as before, when the lock file cannot be created or
// locked (read-only cache, unsupported platform, a redirecting path) or when
// completeBuildLockWait elapses. The only error it returns is the context's,
// because a cancelled query must stop waiting rather than start a build.
func acquireCompleteBuildLock(ctx context.Context, cacheDir, family, version, key string) (*completeBuildLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := openCompleteBuildLockFile(cacheDir, family, version, key)
	if err != nil {
		return nil, nil
	}
	deadline := time.Now().Add(completeBuildLockWait)
	poll := completeBuildLockPollMin
	for {
		err := tryLockBuildLockFile(file)
		if err == nil {
			return &completeBuildLock{file: file}, nil
		}
		if !errors.Is(err, errBuildLockBusy) {
			_ = file.Close()
			return nil, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			_ = file.Close()
			return nil, nil
		}
		timer := time.NewTimer(min(poll, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
		poll = min(poll*2, completeBuildLockPollMax)
	}
}

// openCompleteBuildLockFile opens, creating if needed, the lock file for key
// through the same confined, non-redirecting directory walk that cache writes
// use. The final component is refused unless it is the regular file the open
// actually returned, so a link planted at the lock name cannot point the lock's
// O_CREATE anywhere the cache would not itself write.
func openCompleteBuildLockFile(cacheDir, family, version, key string) (*os.File, error) {
	entry, err := newCacheEntry(cacheDir, family, version, key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(entry.root, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(entry.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	directory, err := openCacheDirectory(root, filepath.Dir(entry.relative))
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	name := key + ".build.lock"
	if named, err := directory.Lstat(name); err == nil && !named.Mode().IsRegular() {
		return nil, errors.New("cache build lock is not a regular file")
	}
	file, err := openOrCreateBuildLockFile(directory, name)
	if err != nil {
		return nil, err
	}
	named, namedErr := directory.Lstat(name)
	opened, openedErr := file.Stat()
	if namedErr != nil || openedErr != nil || !named.Mode().IsRegular() || !os.SameFile(named, opened) {
		_ = file.Close()
		return nil, errors.New("cache build lock changed identity while it was opened")
	}
	return file, nil
}

// openOrCreateBuildLockFile opens the lock file, creating it if it is missing.
//
// It does not use a single O_CREATE open. On darwin an os.Root O_CREATE open
// (openat with O_CREAT|O_NOFOLLOW) racing another process creating the same
// name fails with ENOENT for most of the losers, measured at 166 of 240 opens
// across four concurrent processes, and a lock that fails to open is a lock
// that is silently skipped. An existing file is therefore opened plainly, and
// only a missing one is created, exclusively; losing that creation race means
// the file now exists, so the open is simply retried. The retries are bounded
// so a pathological filesystem degrades to building unlocked, never a spin.
func openOrCreateBuildLockFile(directory *os.Root, name string) (*os.File, error) {
	var lastErr error
	for range 8 {
		file, err := directory.OpenFile(name, os.O_RDWR, 0)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		file, err = directory.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, fs.ErrExist) && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}
