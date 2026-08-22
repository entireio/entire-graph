//go:build windows

package gitutil

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

const (
	jobObjectExtendedLimitInformationClass = 9
	jobObjectLimitKillOnJobClose           = 0x00002000
	createSuspended                        = 0x00000004
	createNoWindow                         = 0x08000000
	processQueryLimitedInformation         = 0x00001000
)

var (
	modkernel32Job               = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW         = modkernel32Job.NewProc("CreateJobObjectW")
	procAssignProcessToJobObject = modkernel32Job.NewProc("AssignProcessToJobObject")
	procSetInformationJobObject  = modkernel32Job.NewProc("SetInformationJobObject")
	procTerminateJobObject       = modkernel32Job.NewProc("TerminateJobObject")
	procIsProcessInJob           = modkernel32Job.NewProc("IsProcessInJob")
)

// pathOutputJob is a Windows Job Object that contains a launched process and
// every descendant it spawns for as long as the job handle stays open. The
// actual command is created with a suspended member of this job as its
// PROC_THREAD_ATTRIBUTE_PARENT_PROCESS. Windows inherits the designated
// parent's job membership while creating the child, so there is no interval in
// which a fast launcher can create an uncontained worker.
type pathOutputJob struct {
	handle            syscall.Handle
	creationParentPID uint32
}

// pathOutputLaunch establishes containment before callers create os/exec pipe
// handles. A CREATE_SUSPENDED surrogate is assigned to a KILL_ON_JOB_CLOSE job,
// then used as cmd's creation parent. The surrogate never executes user code
// and is retired immediately after start returns.
//
// Containment setup is mandatory on Windows. Returning a nil job after starting
// the command would silently restore the launcher/assignment race this helper
// exists to close, so any setup failure prevents the command and is returned to
// the caller.
type pathOutputLaunch struct {
	job              pathOutputJob
	creationParent   *pathOutputCreationParent
	originalProcAttr *syscall.SysProcAttr
}

func preparePathOutputCommand(cmd *exec.Cmd) (pathOutputLaunch, error) {
	if cmd == nil {
		return pathOutputLaunch{}, errors.New("prepare path-output command: nil command")
	}
	originalSysProcAttr := cmd.SysProcAttr
	if originalSysProcAttr != nil && originalSysProcAttr.ParentProcess != 0 {
		return pathOutputLaunch{}, errors.New("prepare path-output command: caller-supplied Windows parent process is incompatible with job containment")
	}
	childAttrs := pathOutputChildSysProcAttr(originalSysProcAttr, 0)

	job, err := createPathOutputJob()
	if err != nil {
		return pathOutputLaunch{}, err
	}
	creationParent, err := newPathOutputCreationParent(
		job,
		len(childAttrs.AdditionalInheritedHandles) > 0 && !childAttrs.NoInheritHandles,
	)
	if err != nil {
		job.close()
		return pathOutputLaunch{}, err
	}
	return pathOutputLaunch{
		job:              job,
		creationParent:   creationParent,
		originalProcAttr: originalSysProcAttr,
	}, nil
}

func (launch *pathOutputLaunch) start(cmd *exec.Cmd) (pathOutputJob, error) {
	if launch == nil || cmd == nil || launch.job.handle == 0 || launch.creationParent == nil {
		return pathOutputJob{}, errors.New("start path-output command: containment is unavailable")
	}
	childAttrs := pathOutputChildSysProcAttr(launch.originalProcAttr, launch.creationParent.process)
	cmd.SysProcAttr = &childAttrs
	// The job's handle is already valid (createPathOutputJob ran inside
	// preparePathOutputCommand, before this method was ever called), so it can
	// be captured here and installed as cmd.Cancel BEFORE Start. That matters
	// because os/exec's context-cancellation watchdog is armed INSIDE Start
	// when cmd is built with CommandContext, and every streaming caller in
	// this package blocks reading cmd's stdout pipe before it ever calls
	// Wait — so WaitDelay's own forced pipe-close, which runs only once Wait
	// executes, never gets a chance to fire. Without this, a canceled context
	// killed only cmd.Process (the default Cancel) while a descendant Git
	// spawned inside this job kept the pipe's write end open, and the blocked
	// reader hung until whatever later, explicit stopPathOutputCommand call
	// happened to run — which never happens on this exact path, since nothing
	// is reading to notice the cancellation in the first place.
	job := launch.job
	cmd.Cancel = func() error {
		jobErr := job.terminate()
		if jobErr == nil {
			// Returning nil tells os/exec that cancellation succeeded. If the
			// launcher already exited 0, Wait will then still return the context
			// error instead of accepting the deliberately truncated output.
			return nil
		}
		if cmd.Process == nil {
			return jobErr
		}
		killErr := cmd.Process.Kill()
		if killErr == nil || errors.Is(killErr, os.ErrProcessDone) {
			return jobErr
		}
		return errors.Join(jobErr, killErr)
	}
	startErr := cmd.Start()
	cmd.SysProcAttr = launch.originalProcAttr
	if startErr != nil {
		launch.close()
		return pathOutputJob{}, startErr
	}
	job.creationParentPID = launch.creationParent.pid
	launch.creationParent.close()
	launch.creationParent = nil
	launch.job = pathOutputJob{}
	return job, nil
}

func (launch *pathOutputLaunch) close() {
	if launch == nil {
		return
	}
	launch.creationParent.close()
	launch.creationParent = nil
	launch.job.close()
	launch.job = pathOutputJob{}
}

func pathOutputChildSysProcAttr(original *syscall.SysProcAttr, parent syscall.Handle) syscall.SysProcAttr {
	attributes := syscall.SysProcAttr{}
	if original != nil {
		attributes = *original
	}
	attributes.ParentProcess = parent
	return attributes
}

func createPathOutputJob() (pathOutputJob, error) {
	handle, _, callErr := procCreateJobObjectW.Call(0, 0)
	if handle == 0 {
		return pathOutputJob{}, windowsJobCallError("CreateJobObjectW", callErr)
	}
	job := pathOutputJob{handle: syscall.Handle(handle)}
	info := jobObjectExtendedLimitInformation{
		BasicLimitInformation: jobObjectBasicLimitInformation{
			LimitFlags: jobObjectLimitKillOnJobClose,
		},
	}
	ret, _, callErr := procSetInformationJobObject.Call(
		uintptr(job.handle),
		jobObjectExtendedLimitInformationClass,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	if ret == 0 {
		job.close()
		return pathOutputJob{}, windowsJobCallError("SetInformationJobObject", callErr)
	}
	return job, nil
}

type pathOutputCreationParent struct {
	process syscall.Handle
	thread  syscall.Handle
	pid     uint32
}

func newPathOutputCreationParent(job pathOutputJob, inheritHandles bool) (*pathOutputCreationParent, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve path-output creation parent executable: %w", err)
	}
	application, err := syscall.UTF16PtrFromString(executable)
	if err != nil {
		return nil, fmt.Errorf("encode path-output creation parent executable: %w", err)
	}
	startup := syscall.StartupInfo{Cb: uint32(unsafe.Sizeof(syscall.StartupInfo{}))}
	var process syscall.ProcessInformation
	// SysProcAttr requires AdditionalInheritedHandles to exist in ParentProcess.
	// Windows handle inheritance preserves their numeric values, so let the
	// suspended surrogate inherit them before Go supplies the exact handle list
	// for the real child. The surrogate executes no instructions, and the real
	// child still inherits only the handles os.StartProcess explicitly lists.
	if err := syscall.CreateProcess(
		application,
		nil,
		nil,
		nil,
		inheritHandles,
		createSuspended|createNoWindow,
		nil,
		nil,
		&startup,
		&process,
	); err != nil {
		return nil, fmt.Errorf("create suspended path-output parent: %w", err)
	}
	parent := &pathOutputCreationParent{
		process: process.Process,
		thread:  process.Thread,
		pid:     process.ProcessId,
	}
	ret, _, callErr := procAssignProcessToJobObject.Call(uintptr(job.handle), uintptr(parent.process))
	if ret == 0 {
		parent.close()
		return nil, windowsJobCallError("AssignProcessToJobObject", callErr)
	}
	return parent, nil
}

func (parent *pathOutputCreationParent) close() {
	if parent == nil {
		return
	}
	if parent.process != 0 {
		_ = syscall.TerminateProcess(parent.process, 1)
		_, _ = syscall.WaitForSingleObject(parent.process, 5_000)
	}
	if parent.thread != 0 {
		_ = syscall.CloseHandle(parent.thread)
		parent.thread = 0
	}
	if parent.process != 0 {
		_ = syscall.CloseHandle(parent.process)
		parent.process = 0
	}
}

func (job pathOutputJob) contains(cmd *exec.Cmd) (bool, error) {
	if job.handle == 0 || cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return false, errors.New("job or process is unavailable")
	}
	return job.containsPID(uint32(cmd.Process.Pid))
}

func (job pathOutputJob) containsPID(pid uint32) (bool, error) {
	if job.handle == 0 || pid == 0 {
		return false, errors.New("job or process is unavailable")
	}
	process, err := syscall.OpenProcess(processQueryLimitedInformation, false, pid)
	if err != nil {
		return false, err
	}
	defer syscall.CloseHandle(process)
	var contained int32
	ret, _, callErr := procIsProcessInJob.Call(
		uintptr(process),
		uintptr(job.handle),
		uintptr(unsafe.Pointer(&contained)),
	)
	if ret == 0 {
		return false, windowsJobCallError("IsProcessInJob", callErr)
	}
	return contained != 0, nil
}

func windowsJobCallError(name string, err error) error {
	if err == nil || errors.Is(err, syscall.Errno(0)) {
		return fmt.Errorf("%s failed", name)
	}
	return fmt.Errorf("%s: %w", name, err)
}

// terminate kills every process still in the job: current members and any
// descendant spawned after the point-in-time snapshot capture fallback.
func (job pathOutputJob) terminate() error {
	if job.handle == 0 {
		return errors.New("terminate Windows job: handle is unavailable")
	}
	ret, _, callErr := procTerminateJobObject.Call(uintptr(job.handle), 1)
	if ret == 0 {
		return windowsJobCallError("TerminateJobObject", callErr)
	}
	return nil
}

func (job pathOutputJob) close() {
	if job.handle == 0 {
		return
	}
	_ = syscall.CloseHandle(job.handle)
}
