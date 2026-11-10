package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A partial clone stays offline only if Git refuses to start a lazy fetch. The
// one place Git starts one is promisor-remote.c fetch_objects(), which every
// caller reaches through promisor_remote_get_direct() -- including the blob
// prefetch rename detection does in `log`, `diff` and `show`. git/git@7b70e9efb18c
// ("upload-pack: disable lazy-fetching by default") made fetch_objects() return
// before it spawns `git fetch` when GIT_NO_LAZY_FETCH is true. That commit
// shipped in the April 2024 security releases: 2.39.4, 2.40.2, 2.41.1, 2.42.2,
// 2.43.4, 2.44.1 and 2.45.1, and every release from 2.46.0 on.
//
// Git 2.45.0 is NOT among them. Its GIT_NO_LAZY_FETCH (from `git
// --no-lazy-fetch`) only clears fetch_if_missing, which the rename prefetch
// does not consult, so 2.45.0 still spawns the fetch; only GIT_ALLOW_PROTOCOL
// then stops it, after transport setup has already stat'd a local or UNC
// promisor URL. The same holds for every earlier release without the commit.
//
// lazyFetchGuardFirstPatch maps each 2.x release line that received the
// backport to its first patch release carrying it (checked against
// promisor-remote.c at every listed tag and the tag before it). Lines below
// 2.39 never received it; lines from 2.46 always have it.
var lazyFetchGuardFirstPatch = map[int]int{
	39: 4,
	40: 2,
	41: 1,
	42: 2,
	43: 4,
	44: 1,
	45: 1,
}

const lazyFetchGuardAlwaysFromMinor = 46

// LazyFetchGuardRequirement names the Git releases that honour
// GIT_NO_LAZY_FETCH for every lazy fetch, for diagnostics.
const LazyFetchGuardRequirement = "Git 2.45.1 or newer (or, on an older release line, 2.39.4+, 2.40.2+, 2.41.1+, 2.42.2+, 2.43.4+ or 2.44.1+)"

// LazyFetchGuardStatus reports whether a Git honours GIT_NO_LAZY_FETCH, which
// every provider subprocess sets.
type LazyFetchGuardStatus struct {
	// Version is the version string Git printed ("2.54.0", "2.39.5", ...), or
	// empty when it could not be determined.
	Version string
	// Effective is true only when Version is a release known to carry the
	// fetch_objects check. Anything unknown or unparseable is false, so
	// callers fail closed.
	Effective bool
}

// lazyFetchGuardKey keys the probe cache. Production Git commands run with the
// repository path as their working directory (and PWD), and a version-manager
// shim (asdf, mise) can pick a different Git by that path, so the probe runs in
// exactly the same directory string and the result is keyed by it. The key is
// deliberately not symlink-resolved: two spellings of one directory can sit
// under different .tool-versions files, so an answer for real/repo must not be
// reused for link/repo. A different PATH resolution, or a binary replaced in
// place (an upgrade or a downgrade), is a different Git and is probed again.
type lazyFetchGuardKey struct {
	dir     string
	path    string
	size    int64
	modTime int64
}

var lazyFetchGuardCache sync.Map // lazyFetchGuardKey -> LazyFetchGuardStatus

const lazyFetchGuardProbeTimeout = 10 * time.Second

// LazyFetchGuard reports whether the Git that production subprocesses will run
// in dir honours GIT_NO_LAZY_FETCH.
//
// dir must be the directory string the provider's Git commands use. It runs
// `git version` there with the same sanitized environment as every other
// provider subprocess. `git version` is a builtin that performs no
// repository discovery and reads no repository configuration, so it can be
// asked while a repository's metadata is still being checked; running it in
// dir only makes a directory-sensitive shim answer for the Git it will really
// run there.
//
// Every outcome is cached for the process, failures included, so a refusal and
// the diagnosis that explains it always see the same answer: a probe that timed
// out once stays "version could not be determined" rather than flipping
// between the safety decision and its message.
func LazyFetchGuard(dir string) LazyFetchGuardStatus {
	path, err := exec.LookPath("git")
	if err != nil {
		return LazyFetchGuardStatus{}
	}
	info, err := os.Stat(path)
	if err != nil {
		return LazyFetchGuardStatus{}
	}
	key := lazyFetchGuardKey{dir: lazyFetchGuardCommandDir(dir), path: path, size: info.Size(), modTime: info.ModTime().UnixNano()}
	if cached, ok := lazyFetchGuardCache.Load(key); ok {
		return cached.(LazyFetchGuardStatus)
	}
	ctx, cancel := context.WithTimeout(context.Background(), lazyFetchGuardProbeTimeout)
	defer cancel()
	status := LazyFetchGuardStatus{}
	if out, err := run(ctx, key.dir, "git", "version"); err == nil {
		if parsed, ok := parseLazyFetchGuardStatus(out); ok {
			status = parsed
		}
	}
	actual, _ := lazyFetchGuardCache.LoadOrStore(key, status)
	return actual.(LazyFetchGuardStatus)
}

// lazyFetchGuardCommandDir spells the probe directory the way os/exec spells a
// command's PWD: filepath.Abs, which is lexical. It must never resolve
// symlinks, or the probe would run at the physical path while the commands run
// at the logical one, and a shim keyed on PWD would answer for a different Git.
func lazyFetchGuardCommandDir(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return filepath.Clean(dir)
}

// parseLazyFetchGuardStatus reads `git version` output. It accepts the
// suffixed forms distributions ship ("git version 2.39.5 (Apple Git-154)",
// "git version 2.45.2.windows.1", "git version 2.46.0.rc1") and reports ok=false
// for anything it cannot read. A version it can read but cannot place on a
// release with the fetch_objects check (2.45.0, 2.45.0.rc2, "2.45.GIT")
// parses with Effective=false.
func parseLazyFetchGuardStatus(out string) (LazyFetchGuardStatus, bool) {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) < 3 || fields[0] != "git" || fields[1] != "version" {
		return LazyFetchGuardStatus{}, false
	}
	version := fields[2]
	parts := strings.SplitN(version, ".", 4)
	if len(parts) < 2 {
		return LazyFetchGuardStatus{}, false
	}
	major, ok := parseVersionNumber(parts[0])
	if !ok {
		return LazyFetchGuardStatus{}, false
	}
	minor, ok := parseVersionNumber(parts[1])
	if !ok {
		return LazyFetchGuardStatus{}, false
	}
	patch, hasPatch := -1, false
	if len(parts) > 2 {
		patch, hasPatch = parseVersionNumber(parts[2])
	}
	effective := false
	switch {
	case major > 2:
		effective = true
	case major < 2:
	case minor >= lazyFetchGuardAlwaysFromMinor:
		effective = true
	default:
		first, backported := lazyFetchGuardFirstPatch[minor]
		effective = backported && hasPatch && patch >= first
	}
	return LazyFetchGuardStatus{Version: version, Effective: effective}, true
}

// parseVersionNumber accepts only a plain decimal component, so "0-rc1",
// "GIT" and "" are not numbers.
func parseVersionNumber(component string) (int, bool) {
	if component == "" || strings.TrimLeft(component, "0123456789") != "" {
		return 0, false
	}
	value, err := strconv.Atoi(component)
	if err != nil {
		return 0, false
	}
	return value, true
}
