//go:build windows

package gitutil

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

const pathOutputJobHelperEnv = "ENTIRE_GRAPH_PATH_OUTPUT_JOB_HELPER"

func TestWindowsJobObjectInformationABILayout(t *testing.T) {
	wantBasic := uintptr(64)
	wantExtended := uintptr(144)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		wantBasic = 48
		wantExtended = 112
	}
	if got := unsafe.Sizeof(jobObjectBasicLimitInformation{}); got != wantBasic {
		t.Fatalf("JOBOBJECT_BASIC_LIMIT_INFORMATION size = %d, want %d", got, wantBasic)
	}
	if got := unsafe.Sizeof(jobObjectExtendedLimitInformation{}); got != wantExtended {
		t.Fatalf("JOBOBJECT_EXTENDED_LIMIT_INFORMATION size = %d, want %d", got, wantExtended)
	}
}

func TestPathOutputChildSysProcAttrPreservesEveryCallerField(t *testing.T) {
	processSecurity := &syscall.SecurityAttributes{Length: 11, InheritHandle: 1}
	threadSecurity := &syscall.SecurityAttributes{Length: 22, InheritHandle: 1}
	original := &syscall.SysProcAttr{
		HideWindow:                 true,
		CmdLine:                    `custom command line`,
		CreationFlags:              0x12345678,
		Token:                      syscall.Token(41),
		ProcessAttributes:          processSecurity,
		ThreadAttributes:           threadSecurity,
		NoInheritHandles:           true,
		AdditionalInheritedHandles: []syscall.Handle{51, 52},
	}
	parent := syscall.Handle(61)
	want := *original
	want.ParentProcess = parent
	if got := pathOutputChildSysProcAttr(original, parent); !reflect.DeepEqual(got, want) {
		t.Fatalf("child SysProcAttr = %#v, want exact caller copy plus parent %#v", got, want)
	}
	if original.ParentProcess != 0 {
		t.Fatalf("caller SysProcAttr was mutated: %#v", original)
	}
}

func TestPathOutputCommandIsBornInJobAndDirectTerminationWorks(t *testing.T) {
	if os.Getenv(pathOutputJobHelperEnv) == "child" {
		fmt.Fprintln(os.Stdout, "stdout-ready")
		fmt.Fprintln(os.Stderr, "stderr-ready")
		time.Sleep(time.Hour)
		return
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPathOutputCommandIsBornInJobAndDirectTerminationWorks$")
	cmd.Env = append(os.Environ(), pathOutputJobHelperEnv+"=child")
	originalAttrs := &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	cmd.SysProcAttr = originalAttrs
	launch, err := preparePathOutputCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer launch.close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}

	job, err := launch.start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	cleaned := false
	t.Cleanup(func() {
		job.terminate()
		job.close()
		if !cleaned {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	if job.handle == 0 {
		t.Fatal("startPathOutputCommand returned a zero job handle")
	}
	if job.creationParentPID == 0 {
		t.Fatal("startPathOutputCommand did not record its creation parent")
	}
	if cmd.SysProcAttr != originalAttrs || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags != createNoWindow || cmd.SysProcAttr.ParentProcess != 0 {
		t.Fatalf("command SysProcAttr was not preserved: %#v", cmd.SysProcAttr)
	}

	contained, err := job.contains(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !contained {
		t.Fatal("actual command is not a member of its returned job")
	}
	parentPID, err := windowsProcessParentPID(uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	if parentPID != job.creationParentPID {
		t.Fatalf("actual command parent PID = %d, want suspended job member %d", parentPID, job.creationParentPID)
	}

	stdoutLine, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read child stdout: %v", err)
	}
	stderrLine, err := bufio.NewReader(stderr).ReadString('\n')
	if err != nil {
		t.Fatalf("read child stderr: %v", err)
	}
	if strings.TrimSpace(stdoutLine) != "stdout-ready" || strings.TrimSpace(stderrLine) != "stderr-ready" {
		t.Fatalf("child standard handles = (%q, %q), want readiness lines", stdoutLine, stderrLine)
	}

	done := make(chan error, 1)
	job.terminate()
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		cleaned = true
	case <-time.After(5 * time.Second):
		t.Fatal("direct TerminateJobObject did not retire the actual command")
	}
	if cmd.ProcessState == nil {
		t.Fatal("direct job termination did not reap the actual command")
	}
}

// TestPathOutputCommandCancelTerminatesDescendantsHoldingTheStdoutPipe
// reproduces the trail finding: a canceled context must free a caller
// blocked reading cmd's stdout pipe even when the descendant STILL HOLDING
// the pipe's write end is not cmd.Process itself. The "launcher" helper
// starts a "child" grandchild inheriting its own stdout and then exits
// immediately, exactly the launcher/worker shape a real Git subprocess tree
// can take; killing only cmd.Process (the default Cancel, or a job
// terminated solely at an explicit stop point after the blocked read
// returns) leaves the grandchild running and the pipe open.
func TestPathOutputCommandCancelTerminatesDescendantsHoldingTheStdoutPipe(t *testing.T) {
	switch os.Getenv(pathOutputJobHelperEnv) {
	case "launcher":
		grandchild := exec.Command(os.Args[0], "-test.run=^TestPathOutputCommandCancelTerminatesDescendantsHoldingTheStdoutPipe$")
		grandchild.Env = append(os.Environ(), pathOutputJobHelperEnv+"=child")
		grandchild.Stdout = os.Stdout
		grandchild.Stderr = os.Stderr
		if err := grandchild.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "launcher: start grandchild:", err)
			os.Exit(1)
		}
		// Exit well before the grandchild does, so cmd.Process (this
		// launcher) is already gone by the time the context is canceled and
		// only the grandchild is left holding the pipe — the shape that
		// defeats a fix which kills cmd.Process alone.
		os.Exit(0)
	case "child":
		fmt.Fprintln(os.Stdout, "stdout-ready")
		time.Sleep(time.Hour)
		return
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPathOutputCommandCancelTerminatesDescendantsHoldingTheStdoutPipe$")
	cmd.Env = append(os.Environ(), pathOutputJobHelperEnv+"=launcher")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	launch, err := preparePathOutputCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer launch.close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	job, err := launch.start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer job.close()
	t.Cleanup(job.terminate)

	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "stdout-ready" {
		t.Fatalf("read grandchild readiness line: line=%q err=%v", line, err)
	}

	cancel()

	done := make(chan struct{})
	go func() {
		_, _ = reader.ReadByte()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("read from the stdout pipe did not unblock within 10s of context cancellation:" +
			" cmd.Cancel must terminate the whole job, not only cmd.Process, to free a descendant" +
			" that is holding the pipe open when cmd.Process itself has already exited")
	}
}

func TestPathOutputCommandRejectsCallerSuppliedCreationParent(t *testing.T) {
	current, err := syscall.GetCurrentProcess()
	if err != nil {
		t.Fatal(err)
	}
	attrs := &syscall.SysProcAttr{ParentProcess: current, HideWindow: true}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.SysProcAttr = attrs
	launch, err := preparePathOutputCommand(cmd)
	if err == nil || !strings.Contains(err.Error(), "caller-supplied Windows parent process") {
		t.Fatalf("preparePathOutputCommand = (%#v, %v), want explicit parent-process refusal", launch, err)
	}
	if launch.job.handle != 0 || cmd.Process != nil || cmd.SysProcAttr != attrs {
		t.Fatalf("refused command mutated or started: launch=%#v process=%#v attrs=%#v", launch, cmd.Process, cmd.SysProcAttr)
	}
}

func windowsProcessParentPID(pid uint32) (uint32, error) {
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer syscall.CloseHandle(snapshot)

	var entry syscall.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := syscall.Process32First(snapshot, &entry); err != nil {
		return 0, err
	}
	for {
		if entry.ProcessID == pid {
			return entry.ParentProcessID, nil
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		if err := syscall.Process32Next(snapshot, &entry); err != nil {
			return 0, fmt.Errorf("find process %d in Windows snapshot: %w", pid, err)
		}
	}
}
