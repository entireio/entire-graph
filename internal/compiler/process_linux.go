//go:build linux

package compiler

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func isolatedCommand(ctx context.Context, config Config, capsule string, binary string, arguments ...string) (*exec.Cmd, error) {
	args := []string{"--unshare-all", "--die-with-parent", "--new-session", "--clearenv",
		"--ro-bind", "/usr", "/usr", "--ro-bind", "/lib", "/lib",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
		"--ro-bind", capsule, "/workspace", "--ro-bind", config.ServerPath, "/tools/gopls",
		"--ro-bind", config.ToolchainRoot, "/toolchain", "--chdir", "/workspace"}
	if _, err := os.Stat("/lib64"); err == nil {
		args = append(args, "--ro-bind", "/lib64", "/lib64")
	}
	for _, pair := range [][2]string{{"PATH", "/toolchain/bin:/usr/bin"}, {"GOROOT", "/toolchain"}, {"GOPATH", "/tmp/gopath"}, {"GOCACHE", "/tmp/gocache"}, {"XDG_CACHE_HOME", "/tmp/cache"}, {"XDG_CONFIG_HOME", "/tmp/config"}, {"GOPROXY", "off"}, {"GOSUMDB", "off"}, {"GOTOOLCHAIN", "local"}, {"GOTELEMETRY", "off"}, {"CGO_ENABLED", "0"}, {"GOENV", "off"}} {
		args = append(args, "--setenv", pair[0], pair[1])
	}
	args = append(args, "--", binary)
	args = append(args, arguments...)
	cmd := exec.CommandContext(ctx, config.BubblewrapPath, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return cmd, nil
}
