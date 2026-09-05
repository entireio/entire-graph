//go:build linux

package compiler

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The helper executes inside the production launcher, then spawns its own
// child. Connecting to a host listener and writing captured source must fail.
func TestLiveCompilerProcessBoundary(t *testing.T) {
	config := liveConfig(t)
	capsule, _, err := createCapsule(map[string]string{"source": "unchanged"})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(capsule)
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	script := `import os,socket,subprocess,sys
p=subprocess.run([sys.executable,"-c", "import socket; socket.create_connection(('127.0.0.1',"+sys.argv[1]+"),0.5)"],capture_output=True)
assert p.returncode != 0, 'descendant accessed host network'
p=subprocess.run([sys.executable,"-c", "import socket; socket.create_connection(('192.0.2.1',443),0.5)"],capture_output=True)
assert p.returncode != 0 and b'Network is unreachable' in p.stderr, p.stderr
try:
 open('/workspace/source','w').write('changed')
 raise AssertionError('source write succeeded')
except OSError as e:
 assert e.errno == 30, e
print('NETWORK_AND_SOURCE_DENIED')`
	cmd, err := isolatedCommand(context.Background(), config, capsule, "/usr/bin/python3", "-c", script, fmt.Sprint(port))
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "NETWORK_AND_SOURCE_DENIED") {
		t.Fatalf("isolation %s %v", out, err)
	}
	bytes, err := os.ReadFile(capsule + "/source")
	if err != nil || string(bytes) != "unchanged" {
		t.Fatal("source changed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd, err = isolatedCommand(ctx, config, capsule, "/usr/bin/python3", "-c", `import subprocess,time; subprocess.Popen(['/usr/bin/python3','-c','import time; time.sleep(60)']); print('READY',flush=True); time.sleep(60)`)
	if err != nil {
		t.Fatal(err)
	}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	go func() { b := make([]byte, 6); pipe.Read(b); close(ready) }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("process did not start")
	}
	start := time.Now()
	cancel()
	err = cmd.Wait()
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("process tree cancellation %v", err)
	}
	// PID namespace leader death kills the descendant; all inherited pipes close.
	if _, ok := err.(*exec.ExitError); !ok && ctx.Err() == nil {
		t.Fatal(err)
	}
}
