package gitutil

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Git 2.45.0 is the first release that honours GIT_NO_LAZY_FETCH
// (git/git@2c206fc, "git --no-lazy-fetch"). From that release on, Git's
// promisor fetch path (promisor-remote.c fetch_objects) checks the variable
// before it spawns `git fetch`, so a missing promised object becomes a local
// read failure: no fetch process is started and the promisor URL is never
// opened or stat'd. Older Git ignores the variable and does spawn the fetch;
// only GIT_ALLOW_PROTOCOL then stops it, and that check runs after transport
// setup has already inspected a local or UNC promisor URL.
const (
	lazyFetchGuardMinMajor = 2
	lazyFetchGuardMinMinor = 45
)

// LazyFetchGuardMinimumVersion names the first Git release that honours
// GIT_NO_LAZY_FETCH, for diagnostics.
const LazyFetchGuardMinimumVersion = "2.45"

// LazyFetchGuardStatus reports whether the Git found on PATH honours
// GIT_NO_LAZY_FETCH, which every provider subprocess sets.
type LazyFetchGuardStatus struct {
	// Version is the version string Git printed ("2.54.0", "2.39.5", ...), or
	// empty when it could not be determined.
	Version string
	// Effective is true only when Version parsed as LazyFetchGuardMinimumVersion
	// or newer. Anything unknown or unparseable is false, so callers fail closed.
	Effective bool
}

// gitBinaryIdentity keys the probe cache. A different PATH resolution, or a
// binary replaced in place (an upgrade or a downgrade), is a different Git and
// is probed again.
type gitBinaryIdentity struct {
	path    string
	size    int64
	modTime int64
}

var lazyFetchGuardCache sync.Map // gitBinaryIdentity -> LazyFetchGuardStatus

const lazyFetchGuardProbeTimeout = 10 * time.Second

// LazyFetchGuard reports whether the Git that production subprocesses will run
// honours GIT_NO_LAZY_FETCH.
//
// It runs `git version`, a builtin that performs no repository discovery and
// reads no repository configuration, from the system temporary directory with
// the same sanitized environment as every other provider subprocess, so it can
// be asked before a repository's metadata has been accepted. Successful probes
// are cached per resolved Git binary; failures are not cached and report
// Effective=false.
func LazyFetchGuard() LazyFetchGuardStatus {
	path, err := exec.LookPath("git")
	if err != nil {
		return LazyFetchGuardStatus{}
	}
	info, err := os.Stat(path)
	if err != nil {
		return LazyFetchGuardStatus{}
	}
	identity := gitBinaryIdentity{path: path, size: info.Size(), modTime: info.ModTime().UnixNano()}
	if cached, ok := lazyFetchGuardCache.Load(identity); ok {
		return cached.(LazyFetchGuardStatus)
	}
	ctx, cancel := context.WithTimeout(context.Background(), lazyFetchGuardProbeTimeout)
	defer cancel()
	out, err := run(ctx, os.TempDir(), "git", "version")
	if err != nil {
		return LazyFetchGuardStatus{}
	}
	status, ok := parseLazyFetchGuardStatus(out)
	if !ok {
		return LazyFetchGuardStatus{}
	}
	lazyFetchGuardCache.Store(identity, status)
	return status
}

// parseLazyFetchGuardStatus reads `git version` output. It accepts the
// suffixed forms distributions ship ("git version 2.39.5 (Apple Git-154)",
// "git version 2.45.2.windows.1", "git version 2.45.0.rc1") and reports ok=false
// for anything it cannot read, which callers treat as an ineffective guard.
func parseLazyFetchGuardStatus(out string) (LazyFetchGuardStatus, bool) {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) < 3 || fields[0] != "git" || fields[1] != "version" {
		return LazyFetchGuardStatus{}, false
	}
	version := fields[2]
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return LazyFetchGuardStatus{}, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil || major < 0 {
		return LazyFetchGuardStatus{}, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil || minor < 0 {
		return LazyFetchGuardStatus{}, false
	}
	effective := major > lazyFetchGuardMinMajor ||
		(major == lazyFetchGuardMinMajor && minor >= lazyFetchGuardMinMinor)
	return LazyFetchGuardStatus{Version: version, Effective: effective}, true
}
