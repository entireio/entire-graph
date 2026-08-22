//go:build !linux

package sem

import (
	"io/fs"
	"os"
	"path/filepath"
)

type sweepDirectoryRoot struct {
	root *os.Root
}

func newSweepDirectoryRoot(repo string) (*sweepDirectoryRoot, error) {
	root, err := os.OpenRoot(repo)
	if err != nil {
		return nil, err
	}
	return &sweepDirectoryRoot{root: root}, nil
}

func (r *sweepDirectoryRoot) Close() error { return r.root.Close() }

func (r *sweepDirectoryRoot) Open(anchor pathTraversalAnchor, dir string, admitStep func() bool) (*os.File, error) {
	rel := filepath.FromSlash(dir)
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return nil, errSymlinkChainOffVolume
	}
	components := splitNativePathComponents(rel)
	current := r.root
	var owned *os.Root
	defer func() {
		if owned != nil {
			_ = owned.Close()
		}
	}()

	if len(components) == 0 && !admitStep() {
		return nil, errGitDirSweepHalted
	}
	for _, component := range components {
		if component == ".." {
			return nil, errSymlinkChainOffVolume
		}
		if !admitStep() {
			return nil, errGitDirSweepHalted
		}
		info, err := current.Lstat(component)
		if err != nil {
			return nil, err
		}
		// The sweep is built only from real directory entries and never needs
		// redirect semantics. Rejecting links and reparse points before opening
		// them keeps an ignored tree from steering the scan outside the held
		// repository root; the device check similarly refuses POSIX mount points.
		if pathMayRedirect(info) || !anchor.allows(info) {
			return nil, errSymlinkChainOffVolume
		}
		if !info.IsDir() {
			return nil, fs.ErrInvalid
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			return nil, err
		}
		openedInfo, err := next.Stat(".")
		if err != nil || !openedInfo.IsDir() || !anchor.allows(openedInfo) || !os.SameFile(info, openedInfo) {
			_ = next.Close()
			if err != nil {
				return nil, err
			}
			return nil, errSymlinkChainOffVolume
		}
		if owned != nil {
			_ = owned.Close()
		}
		owned = next
		current = next
	}

	opened, err := current.Open(".")
	if err != nil {
		return nil, err
	}
	info, err := opened.Stat()
	if err != nil || !info.IsDir() || !anchor.allows(info) {
		_ = opened.Close()
		if err != nil {
			return nil, err
		}
		return nil, errSymlinkChainOffVolume
	}
	return opened, nil
}
