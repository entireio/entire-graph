//go:build windows

package gitutil

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// consoleProbeEnv turns this test binary into a console probe. A test copies
// the binary into a temporary directory as git.exe and puts that directory
// first on PATH. When a copy starts as "git" with the variable naming a report
// file, it records its console there and exits.
const consoleProbeEnv = "ENTIRE_GRAPH_TEST_CONSOLE_PROBE"

// consoleHelperEnv names the directory where a helper process asks each probe
// to write its report.
const consoleHelperEnv = "ENTIRE_GRAPH_TEST_CONSOLE_HELPER"

const (
	// detachedProcess is DETACHED_PROCESS: the new process has no console at
	// all, like an entire-graph status line render that Claude Code starts
	// without one.
	detachedProcess = 0x00000008
	// createNewConsole is CREATE_NEW_CONSOLE: the new process gets, and owns, a
	// console of its own. A program run from a terminal inherits the terminal's
	// console instead.
	createNewConsole = 0x00000010
)

var (
	probeKernel32                  = syscall.NewLazyDLL("kernel32.dll")
	probeUser32                    = syscall.NewLazyDLL("user32.dll")
	probeProcGetConsoleWindow      = probeKernel32.NewProc("GetConsoleWindow")
	probeProcGetConsoleProcessList = probeKernel32.NewProc("GetConsoleProcessList")
	probeProcIsWindowVisible       = probeUser32.NewProc("IsWindowVisible")
)

// TestMain checks the probe variable before m.Run: the probe is started with
// git-style argv ("ls-files -z"), which the test flag parser would reject.
func TestMain(m *testing.M) {
	if verdictFile := os.Getenv(consoleProbeEnv); verdictFile != "" {
		// The verdict goes to a file, never stdout, so the path-output NUL parser
		// that reads this process's stdout never sees it.
		_ = os.WriteFile(verdictFile, []byte(probeConsoleVerdict()), 0o600)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// probeConsoleVerdict reports this process's console in two lines. The first
// is "hidden" when the process has no console window, or has one that is not
// visible, and "visible" otherwise. The second lists the IDs of the processes
// attached to its console, comma-separated; it is empty when there is none.
func probeConsoleVerdict() string {
	return probeConsoleWindow() + "\n" + probeConsoleProcesses()
}

func probeConsoleWindow() string {
	window, _, _ := probeProcGetConsoleWindow.Call()
	if window == 0 {
		return "hidden"
	}
	if visible, _, _ := probeProcIsWindowVisible.Call(window); visible == 0 {
		return "hidden"
	}
	return "visible"
}

func probeConsoleProcesses() string {
	var processes [64]uint32
	count, _, _ := probeProcGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&processes[0])), uintptr(len(processes)))
	if count > uintptr(len(processes)) {
		return "more than 64 processes"
	}
	ids := make([]string, 0, count)
	for _, id := range processes[:count] {
		ids = append(ids, strconv.FormatUint(uint64(id), 10))
	}
	return strings.Join(ids, ",")
}

// installConsoleProbeGit copies this test binary into a new directory as
// git.exe and returns that directory.
func installConsoleProbeGit(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
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
		t.Fatalf("create console probe git: %v", err)
	}
	_, copyErr := io.Copy(destination, source)
	closeErr := errors.Join(destination.Close(), source.Close())
	if copyErr != nil || closeErr != nil {
		t.Fatalf("install console probe git: %v", errors.Join(copyErr, closeErr))
	}
	return binDir
}

// TestSubprocessConsoles re-runs itself as a helper in each console state
// entire-graph meets, and the helper starts the probe as git through every
// production launcher. No child may show a console window: with no console,
// that is the window the user saw flash and steal focus. A child started by a
// plain run, or with the caller's locale, must also share a caller's console;
// a console of its own costs a conhost.exe per call and misses the caller's
// Ctrl-C. A path-output child always gets a console of its own (see
// pathOutputChildSysProcAttr), so only its window is checked.
func TestSubprocessConsoles(t *testing.T) {
	if reportDir := os.Getenv(consoleHelperEnv); reportDir != "" {
		startConsoleProbes(t, reportDir)
		return
	}
	// One install serves every caller state; each copy is a whole test binary.
	binDir := installConsoleProbeGit(t)
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runThisTest := "-test.run=^" + regexp.QuoteMeta(t.Name()) + "$"
	for _, caller := range []struct {
		name  string
		attrs *syscall.SysProcAttr
		// sharesConsole: plain-run and caller-locale children must join the
		// caller's console. A caller without one has none to share.
		sharesConsole bool
	}{
		// A status line render started without a console.
		{"without a console", &syscall.SysProcAttr{CreationFlags: detachedProcess}, false},
		// A windowless console the caller owns: what a parent that starts
		// entire-graph with CREATE_NO_WINDOW (Node's spawn with windowsHide,
		// for one) gives it.
		{"with a windowless console", &syscall.SysProcAttr{CreationFlags: createNoWindow}, true},
		// A console the caller owns, with its window hidden so the test does not
		// flash one. A console with a visible window, and one inherited from a
		// shell or terminal, are not covered here.
		{"with a hidden-window console", &syscall.SysProcAttr{CreationFlags: createNewConsole, HideWindow: true}, true},
	} {
		t.Run(caller.name, func(t *testing.T) {
			reportDir := t.TempDir()
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			helper := exec.CommandContext(ctx, testExecutable, runThisTest, "-test.v")
			helper.Env = append(os.Environ(),
				consoleHelperEnv+"="+reportDir,
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			)
			helper.SysProcAttr = caller.attrs
			if output, err := helper.CombinedOutput(); err != nil {
				t.Fatalf("helper %s failed: %v\n%s", caller.name, err, output)
			}
			callerPID := strconv.Itoa(helper.Process.Pid)
			for _, launch := range []string{"plain-run", "caller-locale", "path-output"} {
				report, err := os.ReadFile(filepath.Join(reportDir, launch))
				if err != nil {
					t.Errorf("%s git started %s wrote no console report: %v", launch, caller.name, err)
					continue
				}
				window, processes, _ := strings.Cut(strings.TrimSpace(string(report)), "\n")
				if window != "hidden" {
					t.Errorf("%s git console window started %s = %q, want %q", launch, caller.name, window, "hidden")
				}
				if caller.sharesConsole && launch != "path-output" &&
					!slices.Contains(strings.Split(processes, ","), callerPID) {
					t.Errorf("%s git started %s is on a console of its own (attached processes %q), want the caller's console (process %s)",
						launch, caller.name, processes, callerPID)
				}
			}
		})
	}
}

// startConsoleProbes runs in a helper. It starts the probe as git through each
// production launcher; each probe writes its own report file.
func startConsoleProbes(t *testing.T, reportDir string) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	reportTo := func(launch string) string {
		return consoleProbeEnv + "=" + filepath.Join(reportDir, launch)
	}

	plain := newCmd(ctx, dir, "git", "ls-files", "-z")
	plain.Env = append(plain.Env, reportTo("plain-run"))
	if err := plain.Run(); err != nil {
		t.Fatalf("run console probe: %v", err)
	}

	callerLocale := newGitCmdWithCallerLocale(ctx, dir, "grep", "-e", "console-probe")
	callerLocale.Env = append(callerLocale.Env, reportTo("caller-locale"))
	if err := callerLocale.Run(); err != nil {
		t.Fatalf("run console probe with the caller's locale: %v", err)
	}

	pathOutput := newCmd(ctx, dir, "git", "ls-files", "-z")
	pathOutput.Env = append(pathOutput.Env, reportTo("path-output"))
	if err := visitBoundedNULPaths(pathOutput, func(string) bool { return true }); err != nil {
		t.Fatalf("run console probe through the path-output launcher: %v", err)
	}
}
