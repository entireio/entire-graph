//go:build windows

package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// verifyConsoleProbeEnv makes TestVerifyShellStartedWithoutAConsoleHasNoVisibleWindow
// act as a probe: it writes whether its console window is visible to the named
// file and returns.
const verifyConsoleProbeEnv = "ENTIRE_GRAPH_TEST_VERIFY_CONSOLE_PROBE"

// verifyConsoleHelperEnv makes the same test act as a helper with no console.
// Its value is the probe's report file.
const verifyConsoleHelperEnv = "ENTIRE_GRAPH_TEST_VERIFY_CONSOLE_HELPER"

// verifyDetachedProcess is DETACHED_PROCESS: the helper has no console at all,
// like a status line render that Claude Code starts without one.
const verifyDetachedProcess = 0x00000008

var (
	verifyProbeGetConsoleWindow = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow")
	verifyProbeIsWindowVisible  = syscall.NewLazyDLL("user32.dll").NewProc("IsWindowVisible")
)

// TestVerifyShellStartedWithoutAConsoleHasNoVisibleWindow runs the verify
// shell from a helper with no console. The shell starts this test binary again
// as a probe, which shares the shell's console and reports whether that console
// has a visible window. Windows gives a console program started without one a
// new, visible console, so the shell would flash a window there.
func TestVerifyShellStartedWithoutAConsoleHasNoVisibleWindow(t *testing.T) {
	if report := os.Getenv(verifyConsoleProbeEnv); report != "" {
		if err := os.WriteFile(report, []byte(verifyProbeConsoleWindow()), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := verifyShellFor(runtime.GOOS, exec.LookPath); err != nil {
		t.Skipf("no POSIX shell for verify: %v", err)
	}
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runThisTest := "-test.run=^" + regexp.QuoteMeta(t.Name()) + "$"
	if report := os.Getenv(verifyConsoleHelperEnv); report != "" {
		t.Setenv(verifyConsoleProbeEnv, report)
		command := "'" + filepath.ToSlash(testExecutable) + "' '" + runThisTest + "'"
		output, code, err := runVerifyShell(t.Context(), t.TempDir(), command)
		if err != nil || code != 0 {
			t.Fatalf("verify shell running the probe: exit %d, err %v\n%s", code, err, output)
		}
		return
	}

	report := filepath.Join(t.TempDir(), "console")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	helper := exec.CommandContext(ctx, testExecutable, runThisTest, "-test.v")
	helper.Env = append(os.Environ(), verifyConsoleHelperEnv+"="+report)
	helper.SysProcAttr = &syscall.SysProcAttr{CreationFlags: verifyDetachedProcess}
	if output, err := helper.CombinedOutput(); err != nil {
		t.Fatalf("helper without a console failed: %v\n%s", err, output)
	}
	got, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("probe started by the verify shell wrote no report: %v", err)
	}
	if verdict := strings.TrimSpace(string(got)); verdict != "hidden" {
		t.Fatalf("verify shell console window started without a console = %q, want %q", verdict, "hidden")
	}
}

// verifyProbeConsoleWindow reports "hidden" when this process has no console
// window, or has one that is not visible, and "visible" otherwise.
func verifyProbeConsoleWindow() string {
	window, _, _ := verifyProbeGetConsoleWindow.Call()
	if window == 0 {
		return "hidden"
	}
	if visible, _, _ := verifyProbeIsWindowVisible.Call(window); visible == 0 {
		return "hidden"
	}
	return "visible"
}
