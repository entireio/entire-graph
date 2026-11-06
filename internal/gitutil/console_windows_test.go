//go:build windows

package gitutil

import (
	"context"
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

// consoleProbeEnv turns this test binary into a console probe. A test copies
// the binary into a temporary directory as git.exe and puts that directory
// first on PATH. When a copy starts as "git" with the variable naming a verdict
// file, it records whether it has a visible console window there and exits.
const consoleProbeEnv = "ENTIRE_GRAPH_TEST_CONSOLE_PROBE"

// consoleHelperEnv names the directory where a helper process asks each probe
// to write its verdict.
const consoleHelperEnv = "ENTIRE_GRAPH_TEST_CONSOLE_HELPER"

const (
	// detachedProcess is DETACHED_PROCESS: the new process has no console at
	// all, like an entire-graph status line render that Claude Code starts
	// without one.
	detachedProcess = 0x00000008
	// createNewConsole is CREATE_NEW_CONSOLE: the new process gets a console of
	// its own, like entire-graph run from a terminal.
	createNewConsole = 0x00000010
)

var (
	probeKernel32             = syscall.NewLazyDLL("kernel32.dll")
	probeUser32               = syscall.NewLazyDLL("user32.dll")
	probeProcGetConsoleWindow = probeKernel32.NewProc("GetConsoleWindow")
	probeProcIsWindowVisible  = probeUser32.NewProc("IsWindowVisible")
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

// probeConsoleVerdict reports "hidden" when this process has no console
// window, or has one that is not visible, and "visible" otherwise.
func probeConsoleVerdict() string {
	window, _, _ := probeProcGetConsoleWindow.Call()
	if window == 0 {
		return "hidden"
	}
	if visible, _, _ := probeProcIsWindowVisible.Call(window); visible == 0 {
		return "hidden"
	}
	return "visible"
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

// assertConsoleProbesHidden re-runs the named test as a helper started with
// attrs. The helper starts the probe as git through a plain run and through
// the path-output launcher, and each probe must report no visible window.
func assertConsoleProbesHidden(t *testing.T, testName, caller string, attrs *syscall.SysProcAttr) {
	t.Helper()
	binDir := installConsoleProbeGit(t)
	verdictDir := t.TempDir()
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	helper := exec.CommandContext(ctx, testExecutable, "-test.run=^"+testName+"$", "-test.v")
	helper.Env = append(os.Environ(),
		consoleHelperEnv+"="+verdictDir,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	helper.SysProcAttr = attrs
	if output, err := helper.CombinedOutput(); err != nil {
		t.Fatalf("helper %s failed: %v\n%s", caller, err, output)
	}

	for _, launch := range []string{"plain-run", "path-output"} {
		verdict, err := os.ReadFile(filepath.Join(verdictDir, launch))
		if err != nil {
			t.Errorf("%s git started %s wrote no console verdict: %v", launch, caller, err)
			continue
		}
		if got := strings.TrimSpace(string(verdict)); got != "hidden" {
			t.Errorf("%s git console started %s = %q, want %q", launch, caller, got, "hidden")
		}
	}
}

// TestSubprocessesStartedWithoutAConsoleHaveNoVisibleWindow catches any
// subprocess that opens its own console window when entire-graph runs without
// a console, which is the window the user saw flash and steal focus. The test
// re-runs itself with DETACHED_PROCESS so the helper has no console.
func TestSubprocessesStartedWithoutAConsoleHaveNoVisibleWindow(t *testing.T) {
	if verdictDir := os.Getenv(consoleHelperEnv); verdictDir != "" {
		startConsoleProbes(t, verdictDir)
		return
	}
	assertConsoleProbesHidden(t,
		"TestSubprocessesStartedWithoutAConsoleHaveNoVisibleWindow",
		"without a console",
		&syscall.SysProcAttr{CreationFlags: detachedProcess},
	)
}

// TestSubprocessesStartedWithAConsoleStartAndHaveNoVisibleWindow covers
// entire-graph run with a console attached, as from a terminal or an agent
// that starts it on a windowless console. Each path-output git must start and
// show no window. It would not start without CREATE_NO_WINDOW: a child created
// under the suspended surrogate parent on the caller's console fails with
// STATUS_DLL_INIT_FAILED (0xc0000142).
func TestSubprocessesStartedWithAConsoleStartAndHaveNoVisibleWindow(t *testing.T) {
	if verdictDir := os.Getenv(consoleHelperEnv); verdictDir != "" {
		startConsoleProbes(t, verdictDir)
		return
	}
	const name = "TestSubprocessesStartedWithAConsoleStartAndHaveNoVisibleWindow"
	assertConsoleProbesHidden(t, name, "with a windowless console",
		&syscall.SysProcAttr{CreationFlags: createNoWindow})
	// HideWindow keeps this console's own window from flashing during the test;
	// the console is otherwise the one a terminal gives a program.
	assertConsoleProbesHidden(t, name, "with a hidden-window console",
		&syscall.SysProcAttr{CreationFlags: createNewConsole, HideWindow: true})
}

// startConsoleProbes runs in a helper. Each probe gets its own verdict file.
func startConsoleProbes(t *testing.T, verdictDir string) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()

	plain := newCmd(ctx, dir, "git", "ls-files", "-z")
	plain.Env = append(plain.Env, consoleProbeEnv+"="+filepath.Join(verdictDir, "plain-run"))
	if err := plain.Run(); err != nil {
		t.Fatalf("run console probe: %v", err)
	}

	pathOutput := newCmd(ctx, dir, "git", "ls-files", "-z")
	pathOutput.Env = append(pathOutput.Env, consoleProbeEnv+"="+filepath.Join(verdictDir, "path-output"))
	if err := visitBoundedNULPaths(pathOutput, func(string) bool { return true }); err != nil {
		t.Fatalf("run console probe through the path-output launcher: %v", err)
	}
}
