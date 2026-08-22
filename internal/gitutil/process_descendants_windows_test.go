//go:build windows

package gitutil

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const pathOutputDescendantHelperEnv = "ENTIRE_GRAPH_PATH_OUTPUT_DESCENDANT_HELPER"

func TestStopPathOutputCommandTerminatesWindowsDescendants(t *testing.T) {
	switch os.Getenv(pathOutputDescendantHelperEnv) {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestStopPathOutputCommandTerminatesWindowsDescendants$")
		child.Env = append(os.Environ(), pathOutputDescendantHelperEnv+"=child")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		_ = child.Run()
		return
	case "child":
		fmt.Fprintln(os.Stdout, os.Getpid())
		time.Sleep(time.Hour)
		return
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := newCmd(ctx, "", os.Args[0], "-test.run=^TestStopPathOutputCommandTerminatesWindowsDescendants$")
	cmd.Env = append(cmd.Env, pathOutputDescendantHelperEnv+"=parent")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	job, err := startPathOutputCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer job.close()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read descendant PID: %v; stderr: %s", err, stderr.String())
	}
	pid, err := strconv.ParseUint(strings.TrimSpace(line), 10, 32)
	if err != nil {
		t.Fatalf("parse descendant PID %q: %v", line, err)
	}
	child, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatalf("open descendant process %d: %v", pid, err)
	}
	defer syscall.CloseHandle(child)

	started := time.Now()
	stopPathOutputCommand(cmd, stdout, job)
	if elapsed := time.Since(started); elapsed >= 5*time.Second {
		t.Fatalf("descendant cleanup took %s, want under 5s", elapsed)
	}
	if cmd.ProcessState == nil {
		t.Fatal("root helper was not reaped")
	}
	state, err := syscall.WaitForSingleObject(child, 0)
	if err != nil {
		t.Fatalf("query descendant process %d: %v", pid, err)
	}
	if state != syscall.WAIT_OBJECT_0 {
		t.Fatalf("descendant process %d remains active after cleanup (wait state %#x)", pid, state)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("descendant cleanup exceeded its context: %v", err)
	}
}
