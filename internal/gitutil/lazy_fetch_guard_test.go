package gitutil

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestParseLazyFetchGuardStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		out       string
		ok        bool
		version   string
		effective bool
	}{
		{"git version 2.45.1\n", true, "2.45.1", true},
		{"git version 2.45.1.windows.1", true, "2.45.1.windows.1", true},
		{"git version 2.45.2.windows.1", true, "2.45.2.windows.1", true},
		{"git version 2.46.0", true, "2.46.0", true},
		{"git version 2.46.0.rc0", true, "2.46.0.rc0", true},
		{"git version 2.54.0", true, "2.54.0", true},
		{"git version 2.50.1 (Apple Git-155)", true, "2.50.1", true},
		{"git version 3.0.0", true, "3.0.0", true},
		{"git version 2.100.1", true, "2.100.1", true},
		// 2.45.0 and its vendor and pre-release forms predate the fetch_objects
		// check: GIT_NO_LAZY_FETCH there does not stop the rename prefetch.
		{"git version 2.45.0", true, "2.45.0", false},
		{"git version 2.45.0.windows.1", true, "2.45.0.windows.1", false},
		{"git version 2.45.0.rc0", true, "2.45.0.rc0", false},
		{"git version 2.45.0.rc2", true, "2.45.0.rc2", false},
		{"git version 2.45.GIT", true, "2.45.GIT", false},
		{"git version 2.45", true, "2.45", false},
		// The April 2024 backports, and the patch release before each.
		{"git version 2.44.1", true, "2.44.1", true},
		{"git version 2.44.0", true, "2.44.0", false},
		{"git version 2.43.4", true, "2.43.4", true},
		{"git version 2.43.3", true, "2.43.3", false},
		{"git version 2.43.0", true, "2.43.0", false},
		{"git version 2.42.2", true, "2.42.2", true},
		{"git version 2.42.1", true, "2.42.1", false},
		{"git version 2.41.1", true, "2.41.1", true},
		{"git version 2.41.0", true, "2.41.0", false},
		{"git version 2.40.2", true, "2.40.2", true},
		{"git version 2.40.1", true, "2.40.1", false},
		{"git version 2.39.5 (Apple Git-154)", true, "2.39.5", true},
		{"git version 2.39.4", true, "2.39.4", true},
		{"git version 2.39.3", true, "2.39.3", false},
		{"git version 2.38.9", true, "2.38.9", false},
		{"git version 2.36.0", true, "2.36.0", false},
		{"git version 1.99.0", true, "1.99.0", false},
		// Anything unreadable must report ok=false so callers fail closed.
		{"", false, "", false},
		{"git version", false, "", false},
		{"git version 2", false, "", false},
		{"git version two.45", false, "", false},
		{"git version 2.x", false, "", false},
		{"git version +2.46.0", false, "", false},
		{"hub version 2.45.1", false, "", false},
	}
	for _, tc := range cases {
		got, ok := parseLazyFetchGuardStatus(tc.out)
		if ok != tc.ok || got.Version != tc.version || got.Effective != tc.effective {
			t.Errorf("parseLazyFetchGuardStatus(%q) = %+v, ok %v; want version %q effective %v ok %v",
				tc.out, got, ok, tc.version, tc.effective, tc.ok)
		}
	}
}

// writeFakeGit installs a POSIX `git` built from body (a shell fragment that
// handles `version`) and returns a PATH with it first; any other subcommand
// fails so a probe cannot accidentally run something else.
func writeFakeGit(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n[ \"$GIT_NO_LAZY_FETCH\" = 1 ] || exit 97\n[ \"$1\" = version ] || exit 98\n" + body
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

func TestLazyFetchGuardProbesTheGitOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake Git is a POSIX shell script")
	}
	for _, tc := range []struct {
		version   string
		effective bool
	}{
		{"2.45.0", false},
		{"2.45.1", true},
	} {
		ran := filepath.Join(t.TempDir(), "ran")
		t.Setenv("PATH", writeFakeGit(t, ": > "+strconv.Quote(ran)+"; echo 'git version "+tc.version+"'\n"))
		got := LazyFetchGuard(t.TempDir())
		if got.Version != tc.version || got.Effective != tc.effective {
			t.Fatalf("LazyFetchGuard() with Git %s on PATH = %+v, want effective %v", tc.version, got, tc.effective)
		}
		if _, err := os.Stat(ran); err != nil {
			t.Fatalf("Git %s probe did not run the Git on PATH: %v", tc.version, err)
		}
	}

	t.Setenv("PATH", t.TempDir())
	if got := LazyFetchGuard(t.TempDir()); got.Effective || got.Version != "" {
		t.Fatalf("LazyFetchGuard() with no Git on PATH = %+v, want an ineffective guard", got)
	}
}

// A version-manager shim (asdf, mise) picks the Git by the directory it runs
// in. The provider runs Git in the repository, so the probe must ask there:
// probing anywhere else can vouch for a Git the repository never runs.
func TestLazyFetchGuardProbesInTheRepositoryDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake Git is a POSIX shell script")
	}
	pinned := t.TempDir()
	if err := os.WriteFile(filepath.Join(pinned, ".tool-versions"), []byte("git 2.44.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", writeFakeGit(t,
		"if grep -q '^git 2.44.0' \"$(pwd -P)/.tool-versions\" 2>/dev/null; then echo 'git version 2.44.0'; else echo 'git version 2.54.0'; fi\n"))

	if got := LazyFetchGuard(pinned); got.Version != "2.44.0" || got.Effective {
		t.Fatalf("probe in a directory that pins Git 2.44.0 = %+v, want the pinned, ineffective Git", got)
	}
	if got := LazyFetchGuard(t.TempDir()); got.Version != "2.54.0" || !got.Effective {
		t.Fatalf("probe in an unpinned directory = %+v, want Git 2.54.0", got)
	}
}

// A shim like asdf walks up from $PWD, the logical path. The provider runs Git
// at the --repo path as given, so the probe must run at that same string: a
// repository reached as evil/link -> real/repo, with evil/.tool-versions
// pinning an old Git, runs the old Git, and probing at the physical real/repo
// would vouch for the wrong one.
func TestLazyFetchGuardProbesTheLogicalPathNotTheSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake Git is a POSIX shell script")
	}
	base := t.TempDir()
	real := filepath.Join(base, "real", "repo")
	evil := filepath.Join(base, "evil")
	for _, dir := range []string{real, evil} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(evil, ".tool-versions"), []byte("git 2.44.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(evil, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", writeFakeGit(t, `d="$PWD"; v=2.54.0
while :; do
  if grep -q '^git 2.44.0' "$d/.tool-versions" 2>/dev/null; then v=2.44.0; break; fi
  [ "$d" = / ] && break; d=$(dirname "$d")
done
echo "git version $v"
`))
	if got := LazyFetchGuard(link); got.Version != "2.44.0" || got.Effective {
		t.Fatalf("probe for %s (a symlink under a directory pinning Git 2.44.0) = %+v; it asked about the symlink target", link, got)
	}
	if got := LazyFetchGuard(real); got.Version != "2.54.0" {
		t.Fatalf("probe for the physical path %s = %+v, want Git 2.54.0", real, got)
	}
	// The logical answer must not leak into the physical path's cache entry or back.
	if got := LazyFetchGuard(link); got.Version != "2.44.0" {
		t.Fatalf("second probe for %s = %+v, want the cached 2.44.0 answer for that spelling", link, got)
	}
}

// A failed probe is cached like a successful one, so the safety decision and
// the diagnosis that explains it cannot see different answers.
func TestLazyFetchGuardCachesAFailedProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake Git is a POSIX shell script")
	}
	counter := filepath.Join(t.TempDir(), "calls")
	t.Setenv("PATH", writeFakeGit(t,
		"echo x >> "+strconv.Quote(counter)+"\n[ \"$(wc -l < "+strconv.Quote(counter)+")\" -gt 1 ] || exit 1\necho 'git version 2.54.0'\n"))
	dir := t.TempDir()
	first, second := LazyFetchGuard(dir), LazyFetchGuard(dir)
	if first != (LazyFetchGuardStatus{}) || second != first {
		t.Fatalf("probes after one failure = %+v then %+v, want the failure reported both times", first, second)
	}
}

// partialRenameClone builds a clone Git treats as a blob:none partial clone
// whose HEAD renames old.go to new.go with an edit, and which lacks the parent's
// old.go blob. Rename detection in `log` and `diff` then prefetches that blob,
// the lazy-fetch path Git 2.45.0 does not guard. The source stays reachable,
// so a started fetch would succeed.
func partialRenameClone(t *testing.T) (clone, missing string) {
	t.Helper()
	fixture := func(dir, stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	base := t.TempDir()
	src := filepath.Join(base, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture(src, "", "init", "-q")
	write(t, src, "old.go", "package pkg\n\nfunc Beta() int { return 1 }\n\nfunc Extra() int { return 2 }\n")
	write(t, src, "a.go", "package pkg\n\nfunc Alpha() int { return Beta() }\n")
	fixture(src, "", "add", ".")
	fixture(src, "", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "one")
	missing = fixture(src, "", "rev-parse", "HEAD:old.go")
	fixture(src, "", "mv", "old.go", "new.go")
	write(t, src, "new.go", "package pkg\n\nfunc Beta() int { return 3 }\n\nfunc Extra() int { return 2 }\n")
	fixture(src, "", "add", "-A")
	fixture(src, "", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "two")

	clone = filepath.Join(base, "clone")
	fixture(base, "", "clone", "-q", "--no-local", src, clone)
	var keep strings.Builder
	for _, line := range strings.Split(fixture(clone, "", "rev-list", "--objects", "--all"), "\n") {
		if oid, _, _ := strings.Cut(line, " "); oid != "" && oid != missing {
			keep.WriteString(oid + "\n")
		}
	}
	staging := t.TempDir()
	fixture(clone, keep.String(), "pack-objects", "-q", filepath.Join(staging, "pack"))
	packDir := filepath.Join(clone, ".git", "objects", "pack")
	old, _ := filepath.Glob(filepath.Join(packDir, "pack-*"))
	for _, path := range old {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	fresh, _ := filepath.Glob(filepath.Join(staging, "pack-*"))
	for _, path := range fresh {
		if err := os.Rename(path, filepath.Join(packDir, filepath.Base(path))); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(clone, ".git", "objects", missing[:2], missing[2:])); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	fixture(clone, "", "config", "remote.origin.promisor", "true")
	fixture(clone, "", "config", "remote.origin.partialclonefilter", "blob:none")
	if listed := fixture(clone, "", "rev-list", "--objects", "--all", "--missing=print"); !strings.Contains(listed, "?"+missing) {
		t.Fatalf("fixture: blob %s is present; the partial clone has nothing to lazily fetch", missing)
	}
	return clone, missing
}

// TestLazyFetchGuardMatchesObservedGitBehaviour is the empirical check behind
// the version table: for every Git it is given, a Git the guard calls
// effective must start no lazy fetch with GIT_NO_LAZY_FETCH=1, on the rename
// prefetch path as well as on a direct object read. It always checks the Git
// on PATH; ENTIRE_GRAPH_TEST_GITS adds more executables (a PATH-list), for
// example source builds of 2.44.0, 2.45.0 and 2.45.1.
//
// GIT_EXEC_PATH points Git's child commands at a spy `git`, and a lazy fetch
// is a child `git fetch`. A control run without GIT_NO_LAZY_FETCH proves the
// spy sees that Git's fetch at all, so silence means something.
func TestLazyFetchGuardMatchesObservedGitBehaviour(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the spy Git is a POSIX shell script")
	}
	pathGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no Git on PATH")
	}
	gits := []string{pathGit}
	for _, extra := range filepath.SplitList(os.Getenv("ENTIRE_GRAPH_TEST_GITS")) {
		if extra != "" {
			gits = append(gits, extra)
		}
	}
	spyDir := t.TempDir()
	spyLog := filepath.Join(t.TempDir(), "child-git.log")
	if err := os.WriteFile(filepath.Join(spyDir, "git"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SPY_LOG\"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	pristine, missing := partialRenameClone(t)
	attempts := func(gitBinary string, noLazyFetch bool, args ...string) int {
		t.Helper()
		work := filepath.Join(t.TempDir(), "clone")
		if err := os.CopyFS(work, os.DirFS(pristine)); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(spyLog)
		cmd := exec.Command(gitBinary, args...)
		cmd.Dir = work
		cmd.Env = append(withoutEnvKeys(cmd.Environ(), "GIT_NO_LAZY_FETCH", "GIT_ALLOW_PROTOCOL", "GIT_EXEC_PATH"),
			"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_ALLOW_PROTOCOL=",
			"GIT_EXEC_PATH="+spyDir, "SPY_LOG="+spyLog)
		if noLazyFetch {
			cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1")
		}
		_ = cmd.Run()
		data, err := os.ReadFile(spyLog)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		fetches := 0
		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, " fetch ") {
				fetches++
			}
		}
		return fetches
	}
	operations := [][]string{
		{"log", "--name-only", "-n", "5", "HEAD"},
		{"diff", "--raw", "--find-renames", "HEAD~1", "HEAD"},
		{"cat-file", "-p", missing},
	}
	for _, gitBinary := range gits {
		out, err := exec.Command(gitBinary, "version").Output()
		if err != nil {
			t.Fatalf("%s version: %v", gitBinary, err)
		}
		status, ok := parseLazyFetchGuardStatus(string(out))
		if !ok {
			t.Fatalf("%s: unparseable version %q", gitBinary, out)
		}
		if attempts(gitBinary, false, operations[0]...) == 0 {
			t.Fatalf("control: the spy saw no lazy fetch from Git %s without GIT_NO_LAZY_FETCH", status.Version)
		}
		for _, operation := range operations {
			fetches := attempts(gitBinary, true, operation...)
			t.Logf("Git %s (guard effective %v): `git %s` started %d lazy fetches with GIT_NO_LAZY_FETCH=1",
				status.Version, status.Effective, strings.Join(operation, " "), fetches)
			if status.Effective && fetches != 0 {
				t.Errorf("Git %s is trusted to honour GIT_NO_LAZY_FETCH, but `git %s` started a lazy fetch",
					status.Version, strings.Join(operation, " "))
			}
		}
	}
}

func withoutEnvKeys(env []string, keys ...string) []string {
	kept := env[:0:0]
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		drop := false
		for _, key := range keys {
			drop = drop || strings.EqualFold(name, key)
		}
		if !drop {
			kept = append(kept, entry)
		}
	}
	return kept
}

// launchFunctions are the process-creation entry points, by import path.
var launchFunctions = map[string]map[string]bool{
	"os/exec":                  {"Command": true, "CommandContext": true},
	"os":                       {"StartProcess": true},
	"syscall":                  {"Exec": true, "ForkExec": true, "StartProcess": true, "CreateProcess": true, "CreateProcessAsUser": true},
	"golang.org/x/sys/unix":    {"Exec": true, "ForkExec": true},
	"golang.org/x/sys/windows": {"CreateProcess": true, "CreateProcessAsUser": true, "ShellExecute": true},
}

// processCFunctions are the libc and Win32 entry points that create or replace
// a process; cgo code (Go calls through C., preambles and C sources) may not
// use them.
var processCFunctionNames = []string{
	"system", "popen", "fork", "vfork", "execl", "execlp", "execle", "execv", "execve", "execvp",
	"execvpe", "fexecve", "posix_spawn", "posix_spawnp", "WinExec", "CreateProcess", "CreateProcessA",
	"CreateProcessW", "CreateProcessAsUserA", "CreateProcessAsUserW", "ShellExecute", "ShellExecuteA",
	"ShellExecuteW", "ShellExecuteExA", "ShellExecuteExW",
}

var processCFunctions = func() map[string]bool {
	names := make(map[string]bool, len(processCFunctionNames))
	for _, name := range processCFunctionNames {
		names[name] = true
	}
	return names
}()

// findProcessCFunctionCall returns the first process-creating C function called
// in source (a name on an identifier boundary followed by optional blanks and
// "("), or "". It uses bytes.Index rather than a regexp because the vendored
// grammar sources are ~200 MB and a regexp pass takes minutes under -race.
func findProcessCFunctionCall(source []byte) string {
	identifier := func(c byte) bool {
		return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
	}
	for _, name := range processCFunctionNames {
		needle := []byte(name)
		for offset := 0; ; {
			index := bytes.Index(source[offset:], needle)
			if index < 0 {
				break
			}
			start := offset + index
			end := start + len(needle)
			offset = end
			if start > 0 && identifier(source[start-1]) {
				continue
			}
			for end < len(source) && (source[end] == ' ' || source[end] == '\t') {
				end++
			}
			if end < len(source) && source[end] == '(' {
				return name
			}
		}
	}
	return ""
}

var cSourceExtensions = map[string]bool{".c": true, ".h": true, ".cc": true, ".cpp": true, ".m": true, ".mm": true}

// launchSyscallNumbers are raw system-call numbers that create or replace a
// process, so syscall.Syscall(SYS_EXECVE, ...) cannot bypass launchFunctions.
var launchSyscallNumbers = map[string]bool{
	"SYS_EXECVE": true, "SYS_EXECVEAT": true, "SYS_FORK": true, "SYS_VFORK": true,
	"SYS_CLONE": true, "SYS_CLONE3": true, "SYS_POSIX_SPAWN": true, "SYS_POSIX_SPAWNP": true,
}

// TestEveryGitSubprocessIsBuiltByTheGuardedConstructors is the static half of
// the no-egress argument for partial clones: GIT_NO_LAZY_FETCH and
// GIT_ALLOW_PROTOCOL protect a command only if it was built by newCmd or
// newGitCmdWithCallerLocale, the two constructors that apply
// gitSubprocessEnvironment. It walks every non-test Go file in the module and
// fails on any reference -- call, function value or package-level initializer
// -- to a process-creation function outside the reviewed allowlist, resolving
// the package by import path so an import alias cannot hide one. It also fails
// on an exec.Cmd literal, a dot import of a launch package, a
// newCmd/run/runWithStderr call whose program is not the literal "git"
// (newCmd applies the Git environment only to "git"), and any use of those
// wrappers as a value instead of a direct call.
func TestEveryGitSubprocessIsBuiltByTheGuardedConstructors(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found from %s: %v", root, err)
	}
	launches, wrappedCalls := scanProcessLaunches(t, root, t.Errorf)
	// Guard against a vacuous scan: the constructors and their callers exist.
	if launches < 3 || wrappedCalls < 20 {
		t.Fatalf("scan found %d launches and %d wrapped Git calls; the walk is not seeing the sources", launches, wrappedCalls)
	}
}

// The scanner must catch the evasions a name-based match misses.
func TestProcessLaunchScanCatchesAliasedAndValueUses(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/evasion\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	evasion := `package gitutil

import (
	ex "os/exec"
	. "syscall"
)

var launch = ex.CommandContext

func fetch() { _ = ex.Command("git", "fetch").Run(); _ = launch; _ = Getpid }

func indirect() { f := newCmd; _ = f; _ = run(nil, "", program, "fetch") }

func newCmd() {}
func run(...any) error { return nil }

var program = "git"
var _ = ex.Cmd{}

var zero ex.Cmd

type wrapped struct{ ex.Cmd }

func pointerIsFine(c *ex.Cmd) *ex.Cmd { return c }
`
	if err := os.MkdirAll(filepath.Join(root, "internal", "gitutil"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "gitutil", "evasion.go"), []byte(evasion), 0o644); err != nil {
		t.Fatal(err)
	}
	// Packages the go tool still compiles even though they sit in bin/, plus a
	// raw execve and a cgo file.
	rawExec := "package helper\n\nimport (\n\t\"syscall\"\n\t\"unsafe\"\n)\n\nfunc spawn(p *byte) { syscall.Syscall(syscall.SYS_EXECVE, uintptr(unsafe.Pointer(p)), 0, 0) }\n"
	cgo := "package helper\n\n// #include <stdlib.h>\n// static void run(void) { system(\"git fetch\"); }\nimport \"C\"\n\nfunc shell() { C.system(nil) }\n"
	if err := os.MkdirAll(filepath.Join(root, "bin", "helper"), 0o755); err != nil {
		t.Fatal(err)
	}
	cSource := "#include <spawn.h>\nint go_spawn(pid_t *p, char **a) { return posix_spawn(p, a[0], 0, 0, a, 0); }\n"
	for name, body := range map[string]string{"raw.go": rawExec, "cgo.go": cgo, "spawn.c": cSource} {
		if err := os.WriteFile(filepath.Join(root, "bin", "helper", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var found []string
	scanProcessLaunches(t, root, func(format string, args ...any) {
		found = append(found, strings.TrimSpace(strings.SplitN(fmt.Sprintf(format, args...), ": ", 2)[1]))
	})
	joined := strings.Join(found, "\n")
	for _, entry := range found {
		if strings.Contains(entry, "pointerIsFine") {
			t.Errorf("scan flagged a *exec.Cmd parameter, which only a constructor can produce: %s", entry)
		}
	}
	for _, want := range []string{
		"package-level declaration references os/exec.CommandContext",
		"fetch references os/exec.Command",
		"dot-imports syscall",
		"indirect uses newCmd as a value",
		"indirect calls run with a program other than the literal \"git\"",
		"package-level declaration builds an exec.Cmd literal",
		"package-level declaration uses exec.Cmd as a value type",
		"spawn references the process-creating system call syscall.SYS_EXECVE",
		"shell calls the process-creating C function system through cgo",
		"bin/helper/cgo.go has a cgo preamble that calls the process-creating C function system",
		"bin/helper/spawn.c is C source that calls the process-creating C function posix_spawn",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("scan missed %q; it reported:\n%s", want, joined)
		}
	}
}

func scanProcessLaunches(t *testing.T, root string, report func(string, ...any)) (launches, wrappedCalls int) {
	t.Helper()
	// Launches that are not provider Git subprocesses, each reviewed:
	allowedLaunchers := map[string]string{
		"internal/gitutil/git.go:newCmd":                                      "the guarded constructor",
		"internal/gitutil/git.go:newGitCmdWithCallerLocale":                   "the guarded caller-locale constructor",
		"internal/gitutil/process_job_windows.go:newPathOutputCreationParent": "a suspended surrogate of this executable used as a job parent; it executes nothing",
		"internal/cli/verify.go:runVerifyShell":                               "runs the user's own test command through their shell",
		"internal/bench/worker.go:":                                           "benchmark harness, not shipped provider code",
		"cmd/graph-bench/main.go:":                                            "benchmark harness that clones its corpus by design",
	}
	gitProgramWrappers := map[string]bool{"newCmd": true, "run": true, "runWithStderr": true}
	passThroughWrappers := map[string]bool{"run": true, "runWithStderr": true}
	valueCheckedWrappers := map[string]bool{"newCmd": true, "newGitCmdWithCallerLocale": true, "run": true, "runWithStderr": true}

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			// testdata and dot-directories are ignored by the go tool; bin/ and
			// node_modules/ are not, so an imported package there still compiles.
			if path != root && (strings.HasPrefix(name, ".") || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if cSourceExtensions[filepath.Ext(path)] {
			// cgo compiles these into the binary; none may start a process.
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if match := findProcessCFunctionCall(data); match != "" {
				report("%s: %s is C source that calls the process-creating C function %s", rel, rel, match)
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		allowed := func(function string) bool {
			_, whole := allowedLaunchers[rel+":"]
			_, named := allowedLaunchers[rel+":"+function]
			return whole || (function != "" && named)
		}
		// Map each file-local package name to its import path.
		imports := map[string]string{}
		for _, spec := range file.Imports {
			importPath, _ := strconv.Unquote(spec.Path.Value)
			name := importPath[strings.LastIndex(importPath, "/")+1:]
			if spec.Name != nil {
				name = spec.Name.Name
			}
			if name == "." && launchFunctions[importPath] != nil && !allowed("") {
				report("%s: %s dot-imports %s, which hides its process-creation functions from this check", fset.Position(spec.Pos()), rel, importPath)
			}
			imports[name] = importPath
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.IMPORT {
				continue
			}
			for _, spec := range gen.Specs {
				importSpec := spec.(*ast.ImportSpec)
				if importSpec.Path.Value != `"C"` {
					continue
				}
				// The cgo preamble is C compiled into this package.
				preamble := ""
				for _, group := range []*ast.CommentGroup{gen.Doc, importSpec.Doc} {
					if group != nil {
						preamble += group.Text()
					}
				}
				if match := findProcessCFunctionCall([]byte(preamble)); match != "" && !allowed("") {
					report("%s: %s has a cgo preamble that calls the process-creating C function %s", fset.Position(importSpec.Pos()), rel, match)
				}
			}
		}
		isGitutil := strings.HasPrefix(rel, "internal/gitutil/")
		for _, decl := range file.Decls {
			function := ""
			where := "package-level declaration"
			var body ast.Node = decl
			if fn, ok := decl.(*ast.FuncDecl); ok {
				function, where = fn.Name.Name, fn.Name.Name
				if fn.Body == nil {
					continue
				}
				body = fn.Body
			}
			calledWrappers := map[*ast.Ident]bool{}
			selectedNames := map[*ast.Ident]bool{} // x.Name: a field or method, never a wrapper
			pointerToCmd := map[*ast.SelectorExpr]bool{}
			isExecCmd := func(expr ast.Expr) bool {
				selector, ok := expr.(*ast.SelectorExpr)
				if !ok {
					return false
				}
				pkg, ok := selector.X.(*ast.Ident)
				return ok && imports[pkg.Name] == "os/exec" && selector.Sel.Name == "Cmd"
			}
			ast.Inspect(body, func(node ast.Node) bool {
				switch node := node.(type) {
				case *ast.StarExpr:
					if isExecCmd(node.X) {
						pointerToCmd[node.X.(*ast.SelectorExpr)] = true // *exec.Cmd only comes from a constructor
					}
				case *ast.SelectorExpr:
					selectedNames[node.Sel] = true
					pkg, ok := node.X.(*ast.Ident)
					if !ok {
						return true
					}
					importPath := imports[pkg.Name]
					if launchFunctions[importPath][node.Sel.Name] {
						launches++
						if !allowed(function) {
							report("%s: %s references %s.%s outside the guarded Git constructors", fset.Position(node.Pos()), where, importPath, node.Sel.Name)
						}
					}
					if (importPath == "syscall" || importPath == "golang.org/x/sys/unix") && launchSyscallNumbers[node.Sel.Name] && !allowed(function) {
						report("%s: %s references the process-creating system call %s.%s", fset.Position(node.Pos()), where, importPath, node.Sel.Name)
					}
					if importPath == "C" && processCFunctions[node.Sel.Name] && !allowed(function) {
						report("%s: %s calls the process-creating C function %s through cgo", fset.Position(node.Pos()), where, node.Sel.Name)
					}
					if isExecCmd(node) && !pointerToCmd[node] && !allowed(function) {
						// A zero exec.Cmd value (a variable, an embedded field, a
						// reflect.New of its type) can be filled in and started
						// without any constructor; only *exec.Cmd is safe to name.
						report("%s: %s uses exec.Cmd as a value type; only *exec.Cmd from newCmd is allowed", fset.Position(node.Pos()), where)
					}
				case *ast.CompositeLit:
					if isExecCmd(node.Type) {
						report("%s: %s builds an exec.Cmd literal; build Git commands with newCmd", fset.Position(node.Pos()), where)
					}
				case *ast.CallExpr:
					ident, ok := node.Fun.(*ast.Ident)
					if ok && ident.Name == "new" && len(node.Args) == 1 && isExecCmd(node.Args[0]) {
						report("%s: %s allocates an exec.Cmd; build Git commands with newCmd", fset.Position(node.Pos()), where)
					}
					if ok && valueCheckedWrappers[ident.Name] {
						calledWrappers[ident] = true
					}
					if !isGitutil || !ok || !gitProgramWrappers[ident.Name] {
						return true
					}
					if len(node.Args) < 3 {
						return true
					}
					wrappedCalls++
					if lit, ok := node.Args[2].(*ast.BasicLit); ok && lit.Kind == token.STRING && lit.Value == `"git"` {
						return true
					}
					if passThroughWrappers[function] || function == "newCmd" {
						return true // run/runWithStderr forward their own name argument to newCmd
					}
					report("%s: %s calls %s with a program other than the literal \"git\"; newCmd would skip the Git environment",
						fset.Position(node.Pos()), where, ident.Name)
				case *ast.Ident:
					if isGitutil && valueCheckedWrappers[node.Name] && !calledWrappers[node] && !selectedNames[node] {
						if fn, ok := decl.(*ast.FuncDecl); !ok || fn.Name != node {
							report("%s: %s uses %s as a value; call it directly so the program argument stays checkable", fset.Position(node.Pos()), where, node.Name)
						}
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return launches, wrappedCalls
}

// A repository may set i18n.logOutputEncoding; HEAD metadata must still parse.
func TestHeadCommitAndTreeIgnoresRepositoryLogOutputEncoding(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	write(t, repo, "a.go", "package p\n")
	git(t, repo, "add", ".")
	git(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "one")
	git(t, repo, "config", "i18n.logOutputEncoding", "UTF-16")
	commit, tree, err := HeadCommitAndTree(t.Context(), repo)
	if err != nil {
		t.Fatalf("HeadCommitAndTree with a UTF-16 log output encoding: %v", err)
	}
	if commit != gitOutput(t, repo, "rev-parse", "HEAD") || tree != gitOutput(t, repo, "rev-parse", "HEAD^{tree}") {
		t.Fatalf("HeadCommitAndTree = %q/%q, want HEAD's commit and tree", commit, tree)
	}
}

// Co-change and checkpoint lookup read `git log` output too; a UTF-16
// i18n.logOutputEncoding must not re-encode it into silence.
func TestHistoryReadsIgnoreRepositoryLogOutputEncoding(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	commit := func(message string) {
		t.Helper()
		git(t, repo, "add", ".")
		git(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", message)
	}
	write(t, repo, "a.go", "package p\n")
	write(t, repo, "b.go", "package p\n")
	commit("one")
	write(t, repo, "a.go", "package p\n\nvar A = 1\n")
	write(t, repo, "b.go", "package p\n\nvar B = 1\n")
	commit("two\n\nEntire-Checkpoint: abc123def456")
	want := gitOutput(t, repo, "rev-parse", "HEAD")
	git(t, repo, "config", "i18n.logOutputEncoding", "UTF-16")

	pairs, err := FileCochanges(t.Context(), repo, "HEAD", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 || pairs[0].Left != "a.go" || pairs[0].Right != "b.go" {
		t.Fatalf("FileCochanges with a UTF-16 log output encoding = %#v, want the a.go/b.go pair", pairs)
	}
	got, err := FindCommitWithCheckpoint(t.Context(), repo, "abc123def456")
	if err != nil || got != want {
		t.Fatalf("FindCommitWithCheckpoint with a UTF-16 log output encoding = %q, %v; want %q", got, err, want)
	}
}

func TestFindProcessCFunctionCallMatchesWholeCallsOnly(t *testing.T) {
	t.Parallel()
	for source, want := range map[string]string{
		`system("git fetch");`:            "system",
		"r = popen\t(cmd, \"r\");":        "popen",
		"pid_t p = fork();":               "fork",
		"subsystem(x); my_fork(); a.b":    "",
		"systematic(1); forked (2);":      "",
		`"system" is a keyword`:           "",
		"ShellExecuteW(h, 0, f, 0, 0, 1)": "ShellExecuteW",
	} {
		if got := findProcessCFunctionCall([]byte(source)); got != want {
			t.Errorf("findProcessCFunctionCall(%q) = %q, want %q", source, got, want)
		}
	}
}
