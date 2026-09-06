//go:build !windows

package gitutil

import (
	"os"
	"testing"
)

func gitSafeDirectoryAlias(t *testing.T, target, alias string) {
	t.Helper()
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("directory symlink is unavailable: %v", err)
	}
}
