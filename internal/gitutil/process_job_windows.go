//go:build windows

package gitutil

import (
	"os/exec"
	"syscall"
	"unsafe"
)

// processSetQuota is PROCESS_SET_QUOTA. It is not exposed by the standard
// syscall package for windows, unlike the PROCESS_TERMINATE and SYNCHRONIZE
// rights used elsewhere in this file.
const processSetQuota = 0x0100

const (
	jobObjectExtendedLimitInformationClass = 9
	jobObjectLimitKillOnJobClose           = 0x00002000
)

// jobObjectBasicLimitInformation mirrors JOBOBJECT_BASIC_LIMIT_INFORMATION.
// Only LimitFlags is populated; the rest keep their zero value, which Windows
// treats as "no limit" for each field.
type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

// jobObjectExtendedLimitInformation mirrors JOBOBJECT_EXTENDED_LIMIT_INFORMATION.
type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                [6]uint64
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

var (
	modkernel32Job               = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW         = modkernel32Job.NewProc("CreateJobObjectW")
	procAssignProcessToJobObject = modkernel32Job.NewProc("AssignProcessToJobObject")
	procSetInformationJobObject  = modkernel32Job.NewProc("SetInformationJobObject")
	procTerminateJobObject       = modkernel32Job.NewProc("TerminateJobObject")
)

// pathOutputJob is a Windows Job Object that contains a launched process and
// every descendant it spawns for as long as the job handle stays open. Unlike
// a point-in-time process-tree snapshot (pathOutputDescendants), a job closes
// the race a snapshot has against a descendant spawned after the snapshot was
// taken: Windows places a new child process into its parent's job
// automatically whenever the parent is itself a job member, so a
// launcher/worker pair spawned after cleanup begins is still caught.
type pathOutputJob struct {
	handle syscall.Handle
}

// startPathOutputCommand starts cmd and, best-effort, contains it (and its
// future descendants) in a job object established immediately after start --
// before the process has had a chance to do anything beyond being created.
// Job creation or assignment failure is not fatal: it only means cleanup
// falls back to the pre-existing point-in-time descendant snapshot taken by
// stopPathOutputCommand.
func startPathOutputCommand(cmd *exec.Cmd) (pathOutputJob, error) {
	if err := cmd.Start(); err != nil {
		return pathOutputJob{}, err
	}
	return newPathOutputJob(cmd), nil
}

func newPathOutputJob(cmd *exec.Cmd) pathOutputJob {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return pathOutputJob{}
	}
	handle, _, _ := procCreateJobObjectW.Call(0, 0)
	if handle == 0 {
		return pathOutputJob{}
	}
	jobHandle := syscall.Handle(handle)
	info := jobObjectExtendedLimitInformation{
		BasicLimitInformation: jobObjectBasicLimitInformation{
			LimitFlags: jobObjectLimitKillOnJobClose,
		},
	}
	ret, _, _ := procSetInformationJobObject.Call(
		uintptr(jobHandle),
		jobObjectExtendedLimitInformationClass,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	if ret == 0 {
		_ = syscall.CloseHandle(jobHandle)
		return pathOutputJob{}
	}
	job := pathOutputJob{handle: jobHandle}
	if !job.assign(cmd) {
		job.close()
		return pathOutputJob{}
	}
	return job
}

// assign places the already-started process into the job. This runs as the
// very next step after cmd.Start() returns in startPathOutputCommand, so the
// window in which a fast-forking launcher could spawn a worker outside the
// job's containment is a scheduling quantum, not however long the command
// runs before something goes wrong and cleanup begins.
func (job pathOutputJob) assign(cmd *exec.Cmd) bool {
	if job.handle == 0 || cmd == nil || cmd.Process == nil {
		return false
	}
	processHandle, err := syscall.OpenProcess(
		syscall.PROCESS_TERMINATE|processSetQuota,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(processHandle)
	ret, _, _ := procAssignProcessToJobObject.Call(uintptr(job.handle), uintptr(processHandle))
	return ret != 0
}

// terminate kills every process still in the job: current members and any
// descendant spawned after the job was created, including ones that spawned
// after the point-in-time snapshot capturePathOutputDescendants takes as a
// fallback for descendants that already existed when a job could not be
// created.
func (job pathOutputJob) terminate() {
	if job.handle == 0 {
		return
	}
	_, _, _ = procTerminateJobObject.Call(uintptr(job.handle), 1)
}

func (job pathOutputJob) close() {
	if job.handle == 0 {
		return
	}
	_ = syscall.CloseHandle(job.handle)
}
