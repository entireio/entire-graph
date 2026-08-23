//go:build windows

package sem

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fakeGitMarkerEnv = "ENTIRE_GRAPH_TEST_FAKE_GIT_MARKER"

func TestMain(m *testing.M) {
	if marker := os.Getenv(fakeGitMarkerEnv); marker != "" {
		_ = os.WriteFile(marker, []byte("started"), 0o600)
		os.Exit(23)
	}
	os.Exit(m.Run())
}

func installFakeGitMarker(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	marker := filepath.Join(binDir, "git-was-started")
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(testExecutable)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := os.OpenFile(filepath.Join(binDir, "git.exe"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		_ = source.Close()
		t.Fatalf("create fake git executable: %v", err)
	}
	_, copyErr := io.Copy(destination, source)
	closeErr := errors.Join(destination.Close(), source.Close())
	if copyErr != nil || closeErr != nil {
		t.Fatalf("install fake git executable: %v", errors.Join(copyErr, closeErr))
	}
	t.Setenv("PATH", binDir)
	t.Setenv(fakeGitMarkerEnv, marker)
	return marker
}

func windowsJunction(t *testing.T, target, link string) {
	t.Helper()
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, target)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create junction %q -> %q: %v: %s", link, target, err, output)
	}
}

func windowsSymlinkOrSkip(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if errors.Is(err, syscall.Errno(1314)) { // ERROR_PRIVILEGE_NOT_HELD
			t.Skipf("symlink creation requires unavailable Windows privilege: %v", err)
		}
		t.Fatalf("create symlink: %v", err)
	}
}

func windowsSameFileAliasOrSkip(t *testing.T, physical, alias string) {
	t.Helper()
	physicalInfo, err := os.Lstat(physical)
	if err != nil {
		t.Fatal(err)
	}
	aliasInfo, err := os.Lstat(alias)
	if err != nil || !os.SameFile(physicalInfo, aliasInfo) {
		t.Skipf("directory is case-sensitive for %q and %q", physical, alias)
	}
}

// TestGitDirPointerTargetRejectsAUNCTargetWithoutTouchingTheNetwork
// reproduces the trail finding on gitDirPointerTarget's out-of-repo fallback:
// a `.git` pointer naming a UNC share reached filepath.EvalSymlinks on that
// target directly, which resolves it by talking to the named server over
// SMB — attacker-controlled network access, with ambient credentials, for a
// target that is provably outside the repository before any resolution is
// needed at all (a UNC share is never on the same volume as a local
// repository root). The fix rejects a different volume BEFORE EvalSymlinks
// runs, so this must return immediately instead of hanging or dialing a
// nonexistent host.
func TestGitDirPointerTargetRejectsAUNCTargetWithoutTouchingTheNetwork(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	// 203.0.113.0/24 is TEST-NET-3 (RFC 5737): reserved for documentation, so
	// this address is never routable and a hang here would be the missing
	// guard, not a fluke of a real host answering.
	writeFile(t, repo, ".git", `gitdir: \\203.0.113.1\share\repo`+"\n")

	done := make(chan struct{})
	var target string
	var ok, hidden bool
	go func() {
		target, ok, hidden = gitDirPointerTarget(repo, "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("gitDirPointerTarget did not return within 5s: a UNC target must be rejected by volume" +
			" before EvalSymlinks attempts to resolve it over the network")
	}
	if ok || hidden {
		t.Errorf("gitDirPointerTarget(repo, \"\") = (%q, ok=%v, hidden=%v), want (_, false, false): a UNC"+
			" target is never inside the repository", target, ok, hidden)
	}
}

// TestGitCommonDirRejectsAUNCTargetWithoutTouchingTheNetwork reproduces the
// sibling trail finding on gitCommonDir: unlike the `.git` pointer, this
// result feeds straight into hasObjectsAndRefs's os.Stat with no containment
// check downstream, so a `commondir` naming a UNC share made
// looksLikeGitDir/hasGitDirStructure open an SMB connection to a server the
// scanned repository's own committed content names. The fix rejects a
// different volume before that Stat ever runs, so this must return quickly
// with "not a git directory" rather than hanging or dialing a nonexistent
// host.
func TestGitCommonDirRejectsAUNCTargetWithoutTouchingTheNetwork(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "HEAD", "ref: refs/heads/main\n")
	// 203.0.113.0/24 is TEST-NET-3 (RFC 5737): reserved for documentation, so
	// this address is never routable and a hang here would be the missing
	// guard, not a fluke of a real host answering.
	writeFile(t, repo, "commondir", `\\203.0.113.1\share\repo`+"\n")

	done := make(chan struct{})
	var target string
	var ok, looksLikeGit bool
	go func() {
		target, ok = gitCommonDir(repo)
		looksLikeGit = looksLikeGitDir(repo)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("gitCommonDir/looksLikeGitDir did not return within 5s: a UNC commondir target must be" +
			" rejected by volume before os.Stat attempts to resolve it over the network")
	}
	if ok {
		t.Errorf("gitCommonDir(repo) = (%q, true), want (_, false): a UNC target is never on the repository's own volume", target)
	}
	if looksLikeGit {
		t.Error("looksLikeGitDir(repo) = true, want false: a refused commondir must not be treated as a git directory")
	}
}

// TestHasObjectsAndRefsRejectsAUNCSymlinkWithoutTouchingTheNetwork reproduces
// the trail finding on hasObjectsAndRefs: unlike the `.git` pointer and
// `commondir` files, an `objects` or `refs` ENTRY that is itself a symlink to
// a UNC share reached os.Stat directly with no volume check at all, and this
// probe runs over every directory the sweep observes — not only ones a
// `.git` pointer already named. A hang or a dial to the (nonexistent, per
// RFC 5737) host would be the missing guard, not a fluke.
func TestHasObjectsAndRefsRejectsAUNCSymlinkWithoutTouchingTheNetwork(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "refs/marker.txt", "refs\n")
	// 203.0.113.0/24 is TEST-NET-3 (RFC 5737): reserved for documentation, so
	// this address is never routable and a hang here would be the missing
	// guard, not a fluke of a real host answering.
	windowsSymlinkOrSkip(t, `\\203.0.113.1\share\repo\objects`, filepath.Join(repo, "objects"))

	done := make(chan struct{})
	var got bool
	go func() {
		got = hasObjectsAndRefs(repo)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("hasObjectsAndRefs did not return within 5s: a UNC symlink target must be rejected by" +
			" volume before os.Stat attempts to resolve it over the network")
	}
	if got {
		t.Error("hasObjectsAndRefs(repo) = true, want false: an objects/ symlink to a UNC share is never on the repository's own volume")
	}
}

// TestHasObjectsAndRefsAcceptsARelativeSymlinkOnTheSameVolume reproduces the
// trail finding on hasObjectsAndRefs' volume comparison: os.Readlink returns
// a RELATIVE target exactly as the link stores it, which carries no volume of
// its own (filepath.VolumeName is "" for every relative path), while repo is
// an absolute `C:\...` path. Comparing that raw target against repo's volume
// rejected every relative objects/refs symlink outright -- a layout git
// itself accepts -- misclassifying a real git directory as ordinary content
// and leaving its config indexable. The fix resolves a relative target
// against the entry's own parent (the same directory gitJoinRelative already
// resolves a `.git`/`commondir` pointer's own relative target against)
// before comparing volumes.
func TestHasObjectsAndRefsAcceptsARelativeSymlinkOnTheSameVolume(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "real-objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "refs/marker.txt", "refs\n")
	windowsSymlinkOrSkip(t, `real-objects`, filepath.Join(repo, "objects"))

	if !hasObjectsAndRefs(repo) {
		t.Error("hasObjectsAndRefs(repo) = false, want true: a relative objects/ symlink resolved" +
			" against its own parent directory must be accepted, not rejected on an empty-vs-drive" +
			" volume mismatch")
	}
}

// TestGitDirPointerTargetRejectsAUNCGitfileSymlinkWithoutTouchingTheNetwork
// reproduces the trail finding on gitDirPointerTarget's `.git`-itself check:
// unlike the pointer's TEXT content (already guarded above), the `.git`
// FILE was read with a bare os.Stat, which follows a symlink as part of the
// same syscall that reports its result. A `.git` that is itself a symlink to
// a UNC path would dial SMB with ambient credentials before this function
// ever got a chance to look at, let alone reject, the target. The fix reads
// the link and checks its volume first, exactly as hasObjectsAndRefs already
// does for an objects/refs entry, so this must return quickly instead of
// hanging or dialing a nonexistent host.
func TestGitDirPointerTargetRejectsAUNCGitfileSymlinkWithoutTouchingTheNetwork(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	// 203.0.113.0/24 is TEST-NET-3 (RFC 5737): reserved for documentation, so
	// this address is never routable and a hang here would be the missing
	// guard, not a fluke of a real host answering.
	windowsSymlinkOrSkip(t, `\\203.0.113.1\share\repo\.git`, filepath.Join(repo, ".git"))

	done := make(chan struct{})
	var target string
	var ok, hidden bool
	go func() {
		target, ok, hidden = gitDirPointerTarget(repo, "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("gitDirPointerTarget did not return within 5s: a .git symlink to a UNC target must be" +
			" rejected by volume before os.Stat attempts to resolve it over the network")
	}
	if ok || hidden {
		t.Errorf("gitDirPointerTarget(repo, \"\") = (%q, ok=%v, hidden=%v), want (_, false, false): a UNC"+
			" .git symlink target is never on the repository's own volume", target, ok, hidden)
	}
}

// TestGitDirPointerTargetFollowsAGitfileSymlinkOnTheSameVolume pins the other
// half: a `.git` symlink to an ordinary same-volume gitfile must still be
// followed and parsed, matching git's own read_gitfile_gently() fidelity
// (S_ISREG of the stat RESULT, not of the link itself) -- the volume guard
// must not turn into a blanket refusal of every symlinked `.git`.
func TestGitDirPointerTargetFollowsAGitfileSymlinkOnTheSameVolume(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".real-git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "elsewhere-gitfile", "gitdir: .real-git\n")
	windowsSymlinkOrSkip(t, filepath.Join(repo, "elsewhere-gitfile"), filepath.Join(repo, ".git"))

	target, ok, hidden := gitDirPointerTarget(repo, "")
	if !ok || hidden || target != ".real-git" {
		t.Errorf("gitDirPointerTarget(repo, \"\") = (%q, ok=%v, hidden=%v), want (\".real-git\", true, false)",
			target, ok, hidden)
	}
}

// TestGitInfoExcludePathAcceptsACaseDifferentCommonDirVolume reproduces the
// trail finding on gitInfoExcludePath's own volume comparison: unlike every
// other volume check in this package (already on sameVolume), this one
// compared filepath.VolumeName(common) against filepath.VolumeName(gitDir)
// with a raw `!=`. Windows drive letters are case-insensitive ("c:" and
// "C:" are the same volume), but VolumeName preserves whatever spelling the
// path carries, so a `commondir` written with a lowercase drive letter while
// gitDir's own path carries an uppercase one was rejected as "a different
// volume" even though Git itself accepts it -- silently skipping the shared
// info/exclude and letting files it excludes reach the index.
func TestGitInfoExcludePathAcceptsACaseDifferentCommonDirVolume(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".realgit")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, ".git", "gitdir: .realgit\n")
	// Same volume as gitDir, spelled with the opposite case.
	lowerVolume := strings.ToLower(filepath.VolumeName(gitDir)) + gitDir[len(filepath.VolumeName(gitDir)):]
	writeFile(t, repo, ".realgit/commondir", lowerVolume+"\n")

	got := gitInfoExcludePath(repo)
	if got == "" {
		t.Fatal("gitInfoExcludePath(repo) = empty: a commondir differing only in drive-letter case was treated as a different volume")
	}
	gotParent, err := os.Stat(filepath.Dir(filepath.Dir(got)))
	if err != nil {
		t.Fatalf("stat resolved common directory %q: %v", got, err)
	}
	wantParent, err := os.Stat(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(gotParent, wantParent) {
		t.Errorf("gitInfoExcludePath(repo) = %q: resolved a different common directory", got)
	}
}

// TestGitDirLinkTargetRejectsAUNCSymlinkWithoutTouchingTheNetwork reproduces
// the trail finding on gitDirLinkTarget: unlike gitDirPointerTarget's own
// `.git`-itself check (already guarded above), this sibling — which handles
// a `.git` that is a symlink to a DIRECTORY rather than to a gitfile — read
// the link's target with a bare os.Stat. That follows the whole symlink
// chain, including an off-volume hop, as part of the same syscall that
// reports the result, so a `.git` symlinked straight to a UNC directory
// would dial SMB with ambient credentials before this function ever got to
// look at, let alone reject, the target. The fix resolves the chain hop by
// hop through safeStatThroughSymlinks, exactly as gitDirPointerTarget
// already does for the gitfile form, so this must return quickly instead of
// hanging or dialing a nonexistent host.
func TestGitDirLinkTargetRejectsAUNCSymlinkWithoutTouchingTheNetwork(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	// 203.0.113.0/24 is TEST-NET-3 (RFC 5737): reserved for documentation, so
	// this address is never routable and a hang here would be the missing
	// guard, not a fluke of a real host answering.
	windowsSymlinkOrSkip(t, `\\203.0.113.1\share\repo`, filepath.Join(repo, ".git"))

	done := make(chan struct{})
	var target string
	var ok, hidden bool
	go func() {
		target, ok, hidden = gitDirLinkTarget(repo, "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("gitDirLinkTarget did not return within 5s: a .git symlink to a UNC directory must be" +
			" rejected by volume before os.Stat attempts to resolve it over the network")
	}
	if ok || hidden {
		t.Errorf("gitDirLinkTarget(repo, \"\") = (%q, ok=%v, hidden=%v), want (_, false, false): a UNC"+
			" .git symlink target is never on the repository's own volume", target, ok, hidden)
	}
}

// TestGitDirLinkTargetFollowsADirectorySymlinkOnTheSameVolume pins the other
// half of the previous fix: an ordinary same-volume `.git` symlink to a real
// git directory must still be followed and accepted, matching git's own
// fidelity — the volume guard must not turn into a blanket refusal of every
// symlinked `.git` directory.
func TestGitDirLinkTargetFollowsADirectorySymlinkOnTheSameVolume(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".real-git"), 0o755); err != nil {
		t.Fatal(err)
	}
	windowsSymlinkOrSkip(t, filepath.Join(repo, ".real-git"), filepath.Join(repo, ".git"))

	target, ok, hidden := gitDirLinkTarget(repo, "")
	if !ok || hidden || target != ".real-git" {
		t.Errorf("gitDirLinkTarget(repo, \"\") = (%q, ok=%v, hidden=%v), want (\".real-git\", true, false)",
			target, ok, hidden)
	}
}

// TestSameVolumeIsCaseInsensitive reproduces the trail finding on the volume
// comparisons throughout this file: filepath.VolumeName preserves whatever
// spelling a path carries, but Windows drive letters and UNC share roots are
// case-INSENSITIVE, so a raw `!=` on two differently-cased spellings of the
// SAME volume reported them as different.
func TestSameVolumeIsCaseInsensitive(t *testing.T) {
	t.Parallel()
	cases := []struct{ a, b string }{
		{`C:\repo\sub`, `c:\repo\other`},
		{`c:\repo`, `C:\elsewhere`},
		{`\\HOST\Share\a`, `\\host\share\b`},
		{`\\host\SHARE\a`, `\\HOST\share\b`},
	}
	for _, tc := range cases {
		if !sameVolume(tc.a, tc.b) {
			t.Errorf("sameVolume(%q, %q) = false, want true: same volume, different case", tc.a, tc.b)
		}
	}
	if sameVolume(`C:\repo`, `D:\repo`) {
		t.Error("sameVolume(C:, D:) = true, want false: genuinely different drives")
	}
	if sameVolume(`C:\repo`, `\\host\share\repo`) {
		t.Error("sameVolume(C:, UNC share) = true, want false: a local drive is never a network share")
	}
}

// TestSafeStatThroughSymlinksRejectsAChainThatLeavesTheVolumeOnASecondHop
// reproduces the trail finding on hasObjectsAndRefs/gitDirPointerTarget/
// gitCommonDir: each checked only the FIRST symlink hop's volume, so a
// same-volume local symlink pointing at a SECOND symlink that names a UNC
// share passed the check, and the eventual os.Stat/EvalSymlinks call
// resolved that second hop itself -- dialing SMB with ambient credentials
// through a hop the guard never looked at. safeStatThroughSymlinks walks
// every hop itself, so this must return quickly with a refusal instead of
// hanging or dialing a nonexistent host.
func TestSafeStatThroughSymlinksRejectsAChainThatLeavesTheVolumeOnASecondHop(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	// First hop: an ordinary same-volume symlink, which the naive single-hop
	// check would accept outright.
	firstHop := filepath.Join(repo, "first-hop")
	// 203.0.113.0/24 is TEST-NET-3 (RFC 5737): reserved for documentation, so
	// this address is never routable and a hang here would be the missing
	// guard, not a fluke of a real host answering.
	secondHop := `\\203.0.113.1\share\repo`
	windowsSymlinkOrSkip(t, secondHop, firstHop)
	entry := filepath.Join(repo, "entry")
	windowsSymlinkOrSkip(t, firstHop, entry)

	done := make(chan struct{})
	var err error
	go func() {
		_, err = safeStatThroughSymlinks(repo, entry)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("safeStatThroughSymlinks did not return within 5s: a second-hop UNC target must be rejected" +
			" by volume before the network is ever touched")
	}
	if err == nil {
		t.Error("safeStatThroughSymlinks(repo, entry) returned no error, want a refusal: the chain's second hop names a UNC share")
	}
}

// TestGitInfoExcludePathRejectsAUNCCommonDirWithoutTouchingTheNetwork
// reproduces the sibling trail finding on gitInfoExcludePath: unlike
// gitCommonDir in provider.go, this independent commondir reader had no
// volume guard at all, so a linked worktree's `.git` pointer plus a
// `commondir` naming a UNC share made the CALLER's os.Stat(info/exclude)
// dial an SMB share — reachable through the same NUL-aware pointer parser
// readGitDirPointer already has to defend the `.git` file itself against.
func TestGitInfoExcludePathRejectsAUNCCommonDirWithoutTouchingTheNetwork(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".realgit")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, ".git", "gitdir: .realgit\n")
	// 203.0.113.0/24 is TEST-NET-3 (RFC 5737): reserved for documentation, so
	// this address is never routable and a hang here would be the missing
	// guard, not a fluke of a real host answering.
	writeFile(t, repo, ".realgit/commondir", `\\203.0.113.1\share\repo`+"\n")

	done := make(chan struct{})
	var got string
	go func() {
		got = gitInfoExcludePath(repo)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("gitInfoExcludePath did not return within 5s: a UNC commondir target must be rejected by" +
			" volume before any caller's os.Stat attempts to resolve it over the network")
	}
	if got != "" {
		t.Errorf("gitInfoExcludePath(repo) = %q, want \"\": a UNC commondir target is never on the repository's own volume", got)
	}
}

func TestGitDirLinkTargetFollowsAHeadlessDirectoryJunction(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".real-git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".real-git", "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	windowsJunction(t, filepath.Join(repo, ".real-git"), filepath.Join(repo, ".git"))

	target, ok, hidden := gitDirLinkTarget(repo, "")
	if !ok || hidden || target != ".real-git" {
		t.Fatalf("gitDirLinkTarget(repo, empty) = (%q, %v, %v), want (.real-git, true, false)", target, ok, hidden)
	}
	if !hasGitDirStructure(filepath.Join(repo, filepath.FromSlash(target))) {
		t.Fatal("junction target lost its HEAD-less git-directory structure")
	}
}

func TestGitDirPointerTargetFollowsAnIntermediateDirectoryJunction(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "dep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "state", "admin", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "state", "admin", "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	windowsJunction(t, filepath.Join(repo, "state"), filepath.Join(repo, "admin-link"))
	writeFile(t, repo, "dep/.git", "gitdir: ../admin-link/admin\n")

	target, ok, hidden := gitDirPointerTarget(repo, "dep")
	if !ok || hidden || target != "state/admin" {
		t.Fatalf("gitDirPointerTarget(repo, dep) = (%q, %v, %v), want (state/admin, true, false)", target, ok, hidden)
	}
}

func TestGitAbsolutePathMatchesGitForWindowsRootGrammar(t *testing.T) {
	base := `C:\work\repo`
	tests := []struct {
		input string
		want  string
	}{
		{input: `\admin\git`, want: `C:\admin\git`},
		{input: `/admin/git`, want: `C:/admin/git`},
		{input: `C:admin\git`, want: `C:\admin\git`},
		{input: `C:\admin\git`, want: `C:\admin\git`},
		{input: `\\host\share\admin`, want: `\\host\share\admin`},
	}
	for _, test := range tests {
		got, absolute := gitAbsolutePath(base, test.input)
		if !absolute || got != test.want {
			t.Errorf("gitAbsolutePath(%q, %q) = (%q, %v), want (%q, true)", base, test.input, got, absolute, test.want)
		}
	}
	if got, absolute := gitAbsolutePath(base, `relative\admin`); absolute || got != `relative\admin` {
		t.Errorf("relative git path = (%q, %v), want unchanged and false", got, absolute)
	}
}

func TestGitTargetPathRejectsWin32TrimAliases(t *testing.T) {
	for _, target := range []string{`  `, `admin.`, `state\admin \objects`} {
		if gitTargetPathValid(target) {
			t.Errorf("gitTargetPathValid(%q) = true, want false", target)
		}
	}
	for _, target := range []string{`.`, `..`, `.repo-git`, `state\admin\objects`} {
		if !gitTargetPathValid(target) {
			t.Errorf("gitTargetPathValid(%q) = false, want true", target)
		}
	}
}

func TestGitDirPointerTargetReturnsPhysicalUnicodeCaseSpelling(t *testing.T) {
	repo := t.TempDir()
	const physical = `state\σ\.dep-git`
	const pointer = `state\Σ\.dep-git`
	writeHeadlessGitDirFixture(t, repo, filepath.ToSlash(physical))
	physicalInfo, err := os.Lstat(filepath.Join(repo, physical))
	if err != nil {
		t.Fatal(err)
	}
	pointerInfo, err := os.Lstat(filepath.Join(repo, pointer))
	if err != nil || !os.SameFile(physicalInfo, pointerInfo) {
		t.Skip("filesystem does not fold the Greek sigma spellings onto one directory")
	}
	writeFile(t, repo, ".git", "gitdir: "+pointer+"\n")

	target, ok, hidden := gitDirPointerTarget(repo, "")
	if !ok || hidden || target != filepath.ToSlash(physical) {
		t.Fatalf("gitDirPointerTarget = (%q, %v, %v), want (%q, true, false)", target, ok, hidden, filepath.ToSlash(physical))
	}
}

func TestGitDirExcluderDoesNotFoldDistinctFinalSigmaDirectory(t *testing.T) {
	repo := t.TempDir()
	const gitDir = `state/Σ/.dep-git`
	const ordinaryDir = `state/ς/.dep-git`
	writeHeadlessGitDirFixture(t, repo, gitDir)
	writeFile(t, repo, ordinaryDir+"/source.go", "package ordinary\n")

	gitInfo, err := os.Lstat(filepath.Join(repo, filepath.FromSlash(gitDir)))
	if err != nil {
		t.Fatal(err)
	}
	ordinaryInfo, err := os.Lstat(filepath.Join(repo, filepath.FromSlash(ordinaryDir)))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(gitInfo, ordinaryInfo) {
		t.Skip("filesystem folds capital and final sigma onto one directory")
	}

	excluder := newGitDirExcluder(t.Context(), repo)
	excluder.recordTarget(gitDir)
	if !excluder.excluded(gitDir + "/config") {
		t.Fatal("the exact git directory was not excluded")
	}
	if excluder.excluded(ordinaryDir + "/source.go") {
		t.Fatal("a distinct final-sigma source directory was excluded by generic Unicode folding")
	}
}

func TestGitDirExcluderCanonicalizesObservedIndexSpelling(t *testing.T) {
	repo := t.TempDir()
	const physical = `state/admin/.dep-git`
	const listed = `STATE/ADMIN/.DEP-GIT`
	writeHeadlessGitDirFixture(t, repo, physical)
	windowsSameFileAliasOrSkip(t,
		filepath.Join(repo, filepath.FromSlash(physical)),
		filepath.Join(repo, filepath.FromSlash(listed)))

	excluder := newGitDirExcluder(t.Context(), repo)
	excluder.recordTarget(physical)
	excluder.observeListedPaths([]string{listed + "/config"}, nil)
	if !excluder.excluded(listed + "/config") {
		t.Fatal("a differently-cased Git/index spelling did not match its exact physical git-directory target")
	}
}

func TestGitDirExcluderBoundsCanonicalObservedSpellings(t *testing.T) {
	repo := t.TempDir()
	const physical = `state/admin/.dep-git`
	const listed = `STATE/ADMIN/.DEP-GIT`
	writeHeadlessGitDirFixture(t, repo, physical)
	windowsSameFileAliasOrSkip(t,
		filepath.Join(repo, filepath.FromSlash(physical)),
		filepath.Join(repo, filepath.FromSlash(listed)))

	excluder := newGitDirExcluder(t.Context(), repo)
	excluder.canonicalObservedDirectoryBytes = maxListedDirectoryBytes
	excluder.observeListedPaths([]string{listed + "/config"}, nil)
	if !excluder.listedObservationExceeded {
		t.Fatal("canonical observed-directory bytes exceeded their aggregate bound without failing the listing")
	}
}

func TestGitRootIdentityProbeBoundFailsClosed(t *testing.T) {
	excluder := newGitDirExcluder(t.Context(), t.TempDir())
	excluder.gitDirRoot = true
	excluder.rootIdentityProbes = maxGitRootIdentityProbes
	if !excluder.excluded("ordinary.go") {
		t.Fatal("root identity probe exhaustion did not fail closed")
	}
	if !errors.Is(excluder.listedObservationError(), errGitDirListedObservationBound) {
		t.Fatalf("identity probe error = %v, want %v", excluder.listedObservationError(), errGitDirListedObservationBound)
	}
}

func TestGitDirRootExcludesPhysicalSharedIndexCase(t *testing.T) {
	repo := t.TempDir()
	const hash = "0123456789abcdef0123456789abcdef01234567"
	const physical = "SHAREDINDEX." + hash
	writeFile(t, repo, physical, "index state\n")
	windowsSameFileAliasOrSkip(t, filepath.Join(repo, physical), filepath.Join(repo, "sharedindex."+hash))

	excluder := newGitDirExcluder(t.Context(), repo)
	excluder.gitDirRoot = true
	if !excluder.excluded(physical) {
		t.Fatal("a physically uppercase shared-index file was not excluded")
	}
}

func TestGitDirRootExcludesUnicodeAliasSharedIndex(t *testing.T) {
	repo := t.TempDir()
	const hash = "0123456789abcdef0123456789abcdef01234567"
	const physical = "ſharedindex." + hash
	const canonical = "sharedindex." + hash
	writeFile(t, repo, physical, "index state\n")
	windowsSameFileAliasOrSkip(t, filepath.Join(repo, physical), filepath.Join(repo, canonical))

	excluder := newGitDirExcluder(t.Context(), repo)
	excluder.gitDirRoot = true
	if !excluder.excluded(physical) {
		t.Fatal("a Unicode-aliased shared-index file was not excluded after identity verification")
	}
}

func TestGitDirRootEntriesRecordPhysicalCase(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "CONFIG", gitDirConfigWithCredential)
	windowsSameFileAliasOrSkip(t, filepath.Join(repo, "CONFIG"), filepath.Join(repo, "config"))

	excluder := newGitDirExcluder(t.Context(), repo)
	excluder.recordGitDirRootEntries()
	if !excluder.excluded("CONFIG") {
		t.Fatal("a physically uppercase root git config was not excluded")
	}
}

func TestGitDirRootEntriesExcludePhysicalCaseDirectoryDescendants(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "HOOKS/post-commit.go", "package hooks\n")
	windowsSameFileAliasOrSkip(t, filepath.Join(repo, "HOOKS"), filepath.Join(repo, "hooks"))

	excluder := newGitDirExcluder(t.Context(), repo)
	excluder.recordGitDirRootEntries()
	if !excluder.excluded("HOOKS/post-commit.go") {
		t.Fatal("a descendant of a physically uppercase root Git metadata directory was not excluded")
	}
}

func TestGitDirRootEntriesExcludeUnicodeAliasDirectoryDescendants(t *testing.T) {
	repo := t.TempDir()
	const physical = "hooKs"
	writeFile(t, repo, physical+"/post-commit.go", "package hooks\n")
	windowsSameFileAliasOrSkip(t, filepath.Join(repo, physical), filepath.Join(repo, "hooks"))

	excluder := newGitDirExcluder(t.Context(), repo)
	excluder.recordGitDirRootEntries()
	if !excluder.excluded(physical + "/post-commit.go") {
		t.Fatal("a descendant of a Unicode-aliased root Git metadata directory was not excluded")
	}
}

func TestGitDirRootEntriesSupportExtendedLengthPath(t *testing.T) {
	dir := t.TempDir()
	for len(dir) <= 300 {
		dir = filepath.Join(dir, "deep-directory-segment")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "config", gitDirConfigWithCredential)
	excluder := newGitDirExcluder(t.Context(), dir)
	excluder.recordGitDirRootEntries()
	if !excluder.excluded("config") {
		t.Fatal("root config under an extended-length path was not excluded")
	}
}

func TestOpenSameVolumePathAcceptsSubstAlias(t *testing.T) {
	backing := t.TempDir()
	var drive string
	for letter := 'Z'; letter >= 'D'; letter-- {
		candidate := string(letter) + ":"
		if _, err := os.Stat(candidate + `\`); errors.Is(err, os.ErrNotExist) {
			drive = candidate
			break
		}
	}
	if drive == "" {
		t.Fatal("no unused drive letter available for SUBST regression")
	}
	if output, err := exec.Command("subst", drive, backing).CombinedOutput(); err != nil {
		t.Fatalf("create SUBST alias %s -> %q: %v: %s", drive, backing, err, output)
	}
	t.Cleanup(func() {
		if output, err := exec.Command("subst", drive, "/D").CombinedOutput(); err != nil {
			t.Errorf("remove SUBST alias %s: %v: %s", drive, err, output)
		}
	})

	repo := filepath.Join(drive+`\`, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	opened, resolved, err := openSameVolumePath(repo, repo)
	if err != nil {
		t.Fatalf("open repository through SUBST alias: %v", err)
	}
	_ = opened.Close()
	if strings.EqualFold(filepath.VolumeName(resolved), drive) {
		t.Fatalf("resolved path %q retained SUBST volume %s; test did not exercise physical-volume canonicalization", resolved, drive)
	}
}

func TestGitInfoExcludePathRejectsWin32TrimAlias(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".git", "gitdir:   \n")
	writeFile(t, repo, "info/exclude", "secret.go\n")
	if got := gitInfoExcludePath(repo); got != "" {
		t.Fatalf("gitInfoExcludePath = %q, want empty for Git-rejected Win32 trim alias", got)
	}
}

func TestGitMetadataGuardTreatsWin32TrimAliasAsRejectedPointer(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".git", "gitdir:   \n")
	writeFile(t, repo, "commondir", "missing\n")
	if !gitMetadataSafeForSubprocess(repo) {
		t.Fatal("Git-rejected Win32 trim alias was treated as the repository root's metadata")
	}
}

func TestUnsafeRootGitfileUsesWarnedFallbackBeforeStartingGit(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".git", `gitdir: \\203.0.113.1\share\repo`+"\n")
	writeFile(t, repo, "main.go", "package main\n")
	if gitMetadataSafeForSubprocess(repo) {
		t.Fatal("UNC root gitfile passed the pre-subprocess metadata guard")
	}

	marker := installFakeGitMarker(t)

	type result struct {
		snapshot ProviderSnapshot
		err      error
	}
	done := make(chan result, 1)
	go func() {
		snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
		done <- result{snapshot: snapshot, err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("warned filesystem fallback: %v", got.err)
		}
		warned := false
		for _, warning := range got.snapshot.Header.Warnings {
			warned = warned || warning.Code == "W_GIT_WORKTREE_FALLBACK"
		}
		if !warned {
			t.Fatalf("warnings = %+v, want W_GIT_WORKTREE_FALLBACK", got.snapshot.Header.Warnings)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unsafe root gitfile did not select the filesystem fallback promptly")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Git subprocess marker stat = %v, want not-exist", err)
	}
}

func TestAnalyzeEntryPointsRejectUnsafeMetadataBeforeStartingGit(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".git", `gitdir: \\203.0.113.1\share\repo`+"\n")
	marker := installFakeGitMarker(t)

	tests := map[string]func() error{
		"range": func() error {
			_, err := AnalyzeGitRangeWithOptions(t.Context(), repo, "base", "head", nil, AnalyzeOptions{})
			return err
		},
		"checkpoint": func() error {
			_, err := AnalyzeCheckpoint(t.Context(), repo, "checkpoint")
			return err
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- run() }()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "refuse Git subprocesses") {
					t.Fatalf("error = %v, want unsafe-metadata refusal", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("unsafe metadata did not fail before Git promptly")
			}
		})
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Git subprocess marker stat = %v, want not-exist", err)
	}
}

func TestRootGitDirectoryWithUNCObjectStoreIsUnsafeWithoutDialing(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, gitDir, "HEAD", "ref: refs/heads/main\n")
	windowsSymlinkOrSkip(t, `\\203.0.113.1\share\objects`, filepath.Join(gitDir, "objects"))

	done := make(chan bool, 1)
	go func() { done <- gitMetadataSafeForSubprocess(repo) }()
	select {
	case safe := <-done:
		if safe {
			t.Fatal("UNC .git/objects redirect passed the pre-subprocess metadata guard")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UNC .git/objects redirect was followed instead of rejected before network access")
	}
}

func TestRootGitDirectoryWithUNCReftableStoreIsUnsafeWithoutDialing(t *testing.T) {
	for name, redirect := range map[string]func(*testing.T, string){
		"directory": func(t *testing.T, gitDir string) {
			windowsSymlinkOrSkip(t, `\\203.0.113.1\share\reftable`, filepath.Join(gitDir, "reftable"))
		},
		"tables list": func(t *testing.T, gitDir string) {
			if err := os.Mkdir(filepath.Join(gitDir, "reftable"), 0o755); err != nil {
				t.Fatal(err)
			}
			windowsSymlinkOrSkip(t, `\\203.0.113.1\share\tables.list`, filepath.Join(gitDir, "reftable", "tables.list"))
		},
		"listed table": func(t *testing.T, gitDir string) {
			if err := os.Mkdir(filepath.Join(gitDir, "reftable"), 0o755); err != nil {
				t.Fatal(err)
			}
			const table = "0x000000000001-0x000000000002-test.ref"
			writeFile(t, filepath.Join(gitDir, "reftable"), "tables.list", table+"\n")
			windowsSymlinkOrSkip(t, `\\203.0.113.1\share\table.ref`, filepath.Join(gitDir, "reftable", table))
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			gitDir := filepath.Join(repo, ".git")
			if err := os.MkdirAll(filepath.Join(gitDir, "objects"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, gitDir, "HEAD", "ref: refs/heads/main\n")
			redirect(t, gitDir)

			done := make(chan bool, 1)
			go func() { done <- gitMetadataSafeForSubprocess(repo) }()
			select {
			case safe := <-done:
				if safe {
					t.Fatalf("UNC .git/reftable %s redirect passed the pre-subprocess metadata guard", name)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("UNC .git/reftable %s redirect was followed instead of rejected before network access", name)
			}
		})
	}
}

func TestGitMetadataGuardResolvesJunctionedRepoBeforeWalkingAncestors(t *testing.T) {
	repo := t.TempDir()
	checkout := filepath.Join(repo, "checkout")
	if err := os.MkdirAll(filepath.Join(checkout, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, checkout, ".git", `gitdir: \\203.0.113.1\share\repo`+"\n")
	alias := filepath.Join(repo, "repo-alias")
	windowsJunction(t, filepath.Join(checkout, "subdir"), alias)

	done := make(chan bool, 1)
	go func() { done <- gitMetadataSafeForSubprocess(alias) }()
	select {
	case safe := <-done:
		if safe {
			t.Fatal("junctioned --repo missed unsafe metadata in the physical checkout ancestor")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("junctioned --repo reached UNC metadata instead of rejecting it before network access")
	}
}

func TestGitMetadataGuardAllowsJunctionedRepoWithSafePhysicalAncestor(t *testing.T) {
	repo := t.TempDir()
	checkout := filepath.Join(repo, "checkout")
	gitDir := filepath.Join(checkout, ".git")
	for _, dir := range []string{
		filepath.Join(checkout, "subdir"),
		filepath.Join(gitDir, "objects"),
		filepath.Join(gitDir, "refs"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, gitDir, "HEAD", "ref: refs/heads/main\n")
	alias := filepath.Join(repo, "repo-alias")
	windowsJunction(t, filepath.Join(checkout, "subdir"), alias)
	if !gitMetadataSafeForSubprocess(alias) {
		t.Fatal("safe repository reached through a junction was refused")
	}
}

func TestGitMetadataGuardAllowsSubstSubdirectoryRepository(t *testing.T) {
	backing := t.TempDir()
	var drive string
	for letter := 'Z'; letter >= 'D'; letter-- {
		candidate := string(letter) + ":"
		if _, err := os.Stat(candidate + `\`); errors.Is(err, os.ErrNotExist) {
			drive = candidate
			break
		}
	}
	if drive == "" {
		t.Fatal("no unused drive letter available for SUBST regression")
	}
	if output, err := exec.Command("subst", drive, backing).CombinedOutput(); err != nil {
		t.Fatalf("create SUBST alias %s -> %q: %v: %s", drive, backing, err, output)
	}
	t.Cleanup(func() {
		if output, err := exec.Command("subst", drive, "/D").CombinedOutput(); err != nil {
			t.Errorf("remove SUBST alias %s: %v: %s", drive, err, output)
		}
	})

	checkout := filepath.Join(backing, "checkout")
	gitDir := filepath.Join(checkout, ".git")
	for _, dir := range []string{
		filepath.Join(checkout, "subdir"),
		filepath.Join(gitDir, "objects"),
		filepath.Join(gitDir, "refs"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, gitDir, "HEAD", "ref: refs/heads/main\n")
	alias := filepath.Join(drive+`\`, "checkout", "subdir")
	if !gitMetadataSafeForSubprocess(alias) {
		t.Fatal("safe repository discovered from a SUBST subdirectory was refused")
	}
}

func TestBareGitDirectoryWithUNCObjectStoreIsUnsafeWithoutDialing(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "HEAD", "ref: refs/heads/main\n")
	windowsSymlinkOrSkip(t, `\\203.0.113.1\share\objects`, filepath.Join(repo, "objects"))
	marker := installFakeGitMarker(t)

	done := make(chan error, 1)
	go func() {
		_, err := AnalyzeGitRangeWithOptions(t.Context(), repo, "base", "head", nil, AnalyzeOptions{})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "refuse Git subprocesses") {
			t.Fatalf("error = %v, want unsafe bare-metadata refusal", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UNC bare objects redirect was followed instead of rejected before network access")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Git subprocess marker stat = %v, want not-exist", err)
	}
}

func TestUNCAlternateObjectStoreIsUnsafeBeforeStartingGit(t *testing.T) {
	payloads := map[string]string{
		"plain":     `\\203.0.113.1\share\objects` + "\n",
		"C quoted":  `"\\\\203.0.113.1\\share\\objects"` + "\n",
		"octal":     `"\134\134203.0.113.1\134share\134objects"` + "\n",
		"multiline": `"\\\\203.0.113.1\\share` + "\n" + `\\objects"` + "\n",
	}
	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			gitDir := filepath.Join(repo, ".git")
			if err := os.MkdirAll(filepath.Join(gitDir, "objects", "info"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(gitDir, "refs"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, gitDir, "HEAD", "ref: refs/heads/main\n")
			writeFile(t, filepath.Join(gitDir, "objects", "info"), "alternates", payload)
			marker := installFakeGitMarker(t)

			done := make(chan error, 1)
			go func() {
				_, err := AnalyzeGitRangeWithOptions(t.Context(), repo, "base", "head", nil, AnalyzeOptions{})
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "refuse Git subprocesses") {
					t.Fatalf("error = %v, want unsafe alternates refusal", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("UNC alternates path was followed instead of rejected before network access")
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Git subprocess marker stat = %v, want not-exist", err)
			}
		})
	}
}

func TestPrunedTreeSweepRefusesAnExternalDirectoryJunctionAndWarns(t *testing.T) {
	repo := t.TempDir()
	external := t.TempDir()
	if err := os.Mkdir(filepath.Join(external, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	windowsJunction(t, external, filepath.Join(repo, "ignored"))

	excluder := newGitDirExcluder(t.Context(), repo)
	excluder.observePrunedSubtree("ignored")
	if excluder.directoriesRead != 0 {
		t.Fatalf("directoriesRead = %d, want 0: the sweep followed the junction", excluder.directoriesRead)
	}
	if excluder.hiddenEvidence == 0 {
		t.Fatal("junction refusal did not create fail-closed hidden evidence")
	}
	warnings := excluder.sweepUnreadableDirWarning()
	if len(warnings) != 1 || !strings.Contains(warnings[0].Detail, "ignored") {
		t.Fatalf("warnings = %+v, want one warning naming ignored", warnings)
	}
}
