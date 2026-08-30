//go:build windows

package gitutil

import (
	"os/exec"
	"testing"
)

func gitSafeDirectoryAlias(t *testing.T, target, alias string) {
	t.Helper()
	cmd := exec.Command("cmd", "/c", "mklink", "/J", alias, target)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create directory junction %q -> %q: %v: %s", alias, target, err, output)
	}
}
