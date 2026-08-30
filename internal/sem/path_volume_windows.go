//go:build windows

package sem

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

type pathTraversalAnchor struct {
	root         string
	physicalRoot string
}

func newPathTraversalAnchor(baseAbs, value string) (pathTraversalAnchor, string, error) {
	if !sameVolume(value, baseAbs) {
		return pathTraversalAnchor{}, "", errSymlinkChainOffVolume
	}
	volume := filepath.VolumeName(baseAbs)
	root := string(filepath.Separator)
	if volume != "" {
		root = volume + string(filepath.Separator)
	}
	rootFile, err := os.Open(root)
	if err != nil {
		return pathTraversalAnchor{}, "", err
	}
	physicalRoot, physicalErr := windowsOpenedPath(rootFile)
	closeErr := rootFile.Close()
	if physicalErr != nil {
		return pathTraversalAnchor{}, "", physicalErr
	}
	if closeErr != nil {
		return pathTraversalAnchor{}, "", closeErr
	}
	return pathTraversalAnchor{root: root, physicalRoot: physicalRoot}, value, nil
}

func (a pathTraversalAnchor) components(value string) ([]string, bool) {
	if !sameVolume(value, a.root) {
		return nil, false
	}
	rest := value[len(filepath.VolumeName(value)):]
	return splitNativePathComponents(rest), true
}

func (a pathTraversalAnchor) allows(os.FileInfo) bool { return true }

var getFinalPathNameByHandle = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFinalPathNameByHandleW")

// windowsOpenedPath returns the physical spelling Windows assigned to an
// already-opened handle. Keeping the lexical spelling here is not just
// cosmetic: NTFS can resolve Unicode case aliases and DOS 8.3 names that Go's
// generic Unicode folding does not model, so a pointer and a directory listing
// can name the same git directory with keys that never compare equal.
//
// Asking the handle avoids a second pathname walk after the rooted, same-volume
// open. In particular, filepath.EvalSymlinks here would reintroduce a race in
// which a repository replaces a checked component with a UNC redirect before
// the canonicalization walk.
func windowsOpenedPath(file *os.File) (string, error) {
	const maxWindowsPathUTF16 = 32_768
	buffer := make([]uint16, 512)
	for {
		n, _, callErr := getFinalPathNameByHandle.Call(
			file.Fd(),
			uintptr(unsafe.Pointer(&buffer[0])),
			uintptr(len(buffer)),
			0,
		)
		if n == 0 {
			if callErr != nil && callErr != syscall.Errno(0) {
				return "", callErr
			}
			return "", fmt.Errorf("GetFinalPathNameByHandleW returned an empty path")
		}
		if n >= uintptr(len(buffer)) {
			if n >= maxWindowsPathUTF16 {
				return "", fmt.Errorf("GetFinalPathNameByHandleW path exceeds %d UTF-16 code units", maxWindowsPathUTF16-1)
			}
			buffer = make([]uint16, int(n)+1)
			continue
		}
		resolved := syscall.UTF16ToString(buffer[:n])
		switch {
		case strings.HasPrefix(resolved, `\\?\UNC\`):
			resolved = `\\` + resolved[len(`\\?\UNC\`):]
		case strings.HasPrefix(resolved, `\\?\`):
			resolved = resolved[len(`\\?\`):]
		default:
			return "", fmt.Errorf("GetFinalPathNameByHandleW returned an unexpected path %q", resolved)
		}
		if !filepath.IsAbs(resolved) {
			return "", fmt.Errorf("GetFinalPathNameByHandleW returned a non-absolute path %q", resolved)
		}
		return filepath.Clean(resolved), nil
	}
}

func canonicalOpenedPath(file *os.File, _ string, anchor pathTraversalAnchor) (string, error) {
	resolved, err := windowsOpenedPath(file)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(anchor.physicalRoot, resolved)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errSymlinkChainOffVolume
	}
	return resolved, nil
}
