//go:build windows

package gitutil

import (
	"os/exec"
	"syscall"
	"time"
	"unsafe"
)

const (
	maxPathOutputProcessSnapshotEntries = 65_536
	maxPathOutputDescendants            = 64
)

type pathOutputProcessRelation struct {
	pid    uint32
	parent uint32
}

type pathOutputDescendant struct {
	handle syscall.Handle
	depth  int
}

type pathOutputDescendants struct {
	processes []pathOutputDescendant
}

// capturePathOutputDescendants snapshots and opens stable handles while Git is
// still alive and producing output. A PID-only cleanup after killing Git's
// launcher can race PID reuse and loses the worker's parent relationship.
func capturePathOutputDescendants(cmd *exec.Cmd) pathOutputDescendants {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return pathOutputDescendants{}
	}
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return pathOutputDescendants{}
	}
	defer syscall.CloseHandle(snapshot)

	relations := make([]pathOutputProcessRelation, 0, 256)
	var entry syscall.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := syscall.Process32First(snapshot, &entry); err != nil {
		return pathOutputDescendants{}
	}
	for len(relations) < maxPathOutputProcessSnapshotEntries {
		relations = append(relations, pathOutputProcessRelation{
			pid:    entry.ProcessID,
			parent: entry.ParentProcessID,
		})
		entry.Size = uint32(unsafe.Sizeof(entry))
		if err := syscall.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}

	type queuedProcess struct {
		pid   uint32
		depth int
	}
	rootPID := uint32(cmd.Process.Pid)
	queue := []queuedProcess{{pid: rootPID}}
	seen := map[uint32]struct{}{rootPID: {}}
	result := pathOutputDescendants{
		processes: make([]pathOutputDescendant, 0, 2),
	}
	for len(queue) > 0 && len(result.processes) < maxPathOutputDescendants {
		parent := queue[0]
		queue = queue[1:]
		for _, relation := range relations {
			if relation.parent != parent.pid || relation.pid == 0 {
				continue
			}
			if _, duplicate := seen[relation.pid]; duplicate {
				continue
			}
			seen[relation.pid] = struct{}{}
			child := queuedProcess{pid: relation.pid, depth: parent.depth + 1}
			queue = append(queue, child)
			handle, err := syscall.OpenProcess(
				syscall.SYNCHRONIZE|syscall.PROCESS_TERMINATE,
				false,
				relation.pid,
			)
			if err != nil {
				continue
			}
			result.processes = append(result.processes, pathOutputDescendant{
				handle: handle,
				depth:  child.depth,
			})
			if len(result.processes) >= maxPathOutputDescendants {
				break
			}
		}
	}
	return result
}

// terminateAndWait retires deepest descendants first, then waits against one
// shared deadline. Stable handles prevent PID reuse from redirecting cleanup at
// an unrelated process, and the fixed capture cap plus shared deadline keep the
// malformed-output path bounded.
func (descendants pathOutputDescendants) terminateAndWait(timeout time.Duration) {
	for depth := maxPathOutputDescendants; depth > 0; depth-- {
		for _, process := range descendants.processes {
			if process.depth != depth {
				continue
			}
			state, err := syscall.WaitForSingleObject(process.handle, 0)
			if err == nil && state == syscall.WAIT_TIMEOUT {
				_ = syscall.TerminateProcess(process.handle, 1)
			}
		}
	}

	deadline := time.Now().Add(timeout)
	for i := len(descendants.processes) - 1; i >= 0; i-- {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return
		}
		milliseconds := uint32((remaining + time.Millisecond - 1) / time.Millisecond)
		_, _ = syscall.WaitForSingleObject(descendants.processes[i].handle, milliseconds)
	}
}

func (descendants pathOutputDescendants) close() {
	for _, process := range descendants.processes {
		_ = syscall.CloseHandle(process.handle)
	}
}
