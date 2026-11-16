//go:build linux

package sem

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

type sweepDirectoryRoot struct {
	file *os.File

	mountOnce   sync.Once
	mountPoints map[string]struct{}
	mountErr    error
	// mountKeys folds mountPoints with mountPathKey, once, for OpenFile's
	// rejections.
	mountKeysOnce sync.Once
	mountKeys     map[string]struct{}
	// readMountTable reads the mount table; nil reads /proc/self/mountinfo.
	// Tests replace it to control what a fresh read sees.
	readMountTable func() (map[string]struct{}, error)
}

func newSweepDirectoryRoot(repo string) (*sweepDirectoryRoot, error) {
	file, err := os.Open(repo)
	if err != nil {
		return nil, err
	}
	return &sweepDirectoryRoot{file: file}, nil
}

func (r *sweepDirectoryRoot) Close() error { return r.file.Close() }

// Stat describes the held root directory itself.
func (r *sweepDirectoryRoot) Stat() (fs.FileInfo, error) { return r.file.Stat() }

type linuxOpenHow struct {
	flags   uint64
	mode    uint64
	resolve uint64
}

const (
	linuxResolveNoXDev      = 0x01
	linuxResolveNoMagicLink = 0x02
	linuxResolveNoSymlink   = 0x04
	linuxResolveBeneath     = 0x08
)

func linuxOpenat2Number() uintptr {
	switch runtime.GOARCH {
	case "mips", "mipsle":
		return 4437
	case "mips64", "mips64le":
		return 5437
	default:
		return 437
	}
}

const (
	linuxSweepDirectoryFlags = syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_DIRECTORY
	// A file read opens nonblocking so a FIFO put in a listed file's place cannot
	// stall the reader; callers check that the opened file is regular.
	linuxSweepFileFlags = syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
)

func (r *sweepDirectoryRoot) openat2(rel string, flags int) (*os.File, error) {
	name, err := syscall.BytePtrFromString(rel)
	if err != nil {
		return nil, err
	}
	how := linuxOpenHow{
		flags: uint64(flags),
		resolve: linuxResolveNoXDev | linuxResolveNoMagicLink |
			linuxResolveNoSymlink | linuxResolveBeneath,
	}
	fd, _, errno := syscall.Syscall6(
		linuxOpenat2Number(),
		r.file.Fd(),
		uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&how)),
		unsafe.Sizeof(how),
		0,
		0,
	)
	if errno != 0 {
		return nil, errno
	}
	opened := os.NewFile(fd, filepath.Join(r.file.Name(), rel))
	if opened == nil {
		_ = syscall.Close(int(fd))
		return nil, fs.ErrInvalid
	}
	return opened, nil
}

const (
	maxLinuxMountInfoBytes = 16 << 20
	maxLinuxMountInfoLines = 100_000
)

var linuxMountInfoEscapes = strings.NewReplacer(
	`\040`, " ",
	`\011`, "\t",
	`\012`, "\n",
	`\134`, `\`,
)

func readLinuxMountPoints() (map[string]struct{}, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	limited := &io.LimitedReader{R: file, N: maxLinuxMountInfoBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	mounts := make(map[string]struct{})
	for lines := 0; scanner.Scan(); lines++ {
		if lines >= maxLinuxMountInfoLines {
			return nil, errors.New("Linux mount table exceeded its line bound")
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 {
			return nil, errors.New("malformed Linux mount table entry")
		}
		mounts[filepath.Clean(linuxMountInfoEscapes.Replace(fields[4]))] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if limited.N <= 0 {
		return nil, errors.New("Linux mount table exceeded its byte bound")
	}
	return mounts, nil
}

func (r *sweepDirectoryRoot) readMountPoints() (map[string]struct{}, error) {
	if r.readMountTable != nil {
		return r.readMountTable()
	}
	return readLinuxMountPoints()
}

func (r *sweepDirectoryRoot) loadMountPoints() (map[string]struct{}, error) {
	r.mountOnce.Do(func() {
		r.mountPoints, r.mountErr = r.readMountPoints()
	})
	return r.mountPoints, r.mountErr
}

// heldRootMountPoints returns the mount inventory only while r.file's name still
// identifies the directory it holds. mountinfo identifies mount points by their
// current absolute paths, while the callers below reason about components
// relative to the held repository descriptor. If that repository was renamed
// after it was opened, its stale name could no longer match a mount-table entry
// and a walk could cross that mount. Every failure here is fail-closed.
func (r *sweepDirectoryRoot) heldRootMountPoints() (map[string]struct{}, error) {
	return r.heldRootMountPointsFrom(r.loadMountPoints)
}

// heldRootMountKeys is heldRootMountPoints folded by mountPathKey, for
// rejection only: a filesystem that folds case or Unicode normalization
// resolves an alias spelling into the mount point the table spells its own way.
func (r *sweepDirectoryRoot) heldRootMountKeys() (map[string]struct{}, error) {
	mountPoints, err := r.heldRootMountPoints()
	if err != nil {
		return nil, err
	}
	r.mountKeysOnce.Do(func() {
		r.mountKeys = make(map[string]struct{}, len(mountPoints))
		for mount := range mountPoints {
			r.mountKeys[mountPathKey(mount)] = struct{}{}
		}
	})
	return r.mountKeys, nil
}

func (r *sweepDirectoryRoot) heldRootMountPointsFrom(load func() (map[string]struct{}, error)) (map[string]struct{}, error) {
	mountPoints, err := load()
	if err != nil {
		return nil, errors.Join(errSymlinkChainOffVolume, err)
	}
	heldInfo, err := r.file.Stat()
	if err != nil {
		return nil, errors.Join(errSymlinkChainOffVolume, err)
	}
	namedInfo, err := os.Lstat(r.file.Name())
	if err != nil {
		return nil, errors.Join(errSymlinkChainOffVolume, err)
	}
	if !os.SameFile(heldInfo, namedInfo) {
		return nil, errSymlinkChainOffVolume
	}
	return mountPoints, nil
}

// classifyMountCrossing explains a refusal to cross a mount: an openat2
// RESOLVE_NO_XDEV refusal, where the kernel says only that resolution would
// have crossed a mount somewhere on the path, or the cached table naming the
// final component. Report errWorktreeMountPoint only when a FRESH read of the
// mount table names the final component and none of the components before it.
// The cached inventory is not enough to grant a skip: it may predate a mount
// placed on an ancestor since, which is exactly what a refusal here can be.
// Anything else (an ancestor mount, a mount the table does not name, an
// unreadable table, a renamed held root) stays errSymlinkChainOffVolume.
func (r *sweepDirectoryRoot) classifyMountCrossing(components []string) error {
	if len(components) == 0 {
		return errSymlinkChainOffVolume
	}
	mountPoints, err := r.heldRootMountPointsFrom(r.readMountPoints)
	if err != nil {
		return err
	}
	candidate := filepath.Clean(r.file.Name())
	for index, component := range components {
		candidate = filepath.Join(candidate, component)
		if _, mounted := mountPoints[candidate]; !mounted {
			continue
		}
		if index == len(components)-1 {
			return errWorktreeMountPoint
		}
		return errSymlinkChainOffVolume
	}
	return errSymlinkChainOffVolume
}

func (r *sweepDirectoryRoot) openWithoutOpenat2(
	anchor pathTraversalAnchor,
	components []string,
	admitStep func() bool,
) (*os.File, error) {
	current := r.file
	currentPath := filepath.Clean(r.file.Name())
	var owned *os.File
	closeOwned := func() {
		if owned != nil {
			_ = owned.Close()
		}
	}

	if len(components) == 0 {
		fd, err := syscall.Dup(int(r.file.Fd()))
		if err != nil {
			return nil, err
		}
		opened := os.NewFile(uintptr(fd), r.file.Name())
		if opened == nil {
			_ = syscall.Close(fd)
			return nil, fs.ErrInvalid
		}
		return opened, nil
	}
	mountPoints, err := r.heldRootMountPoints()
	if err != nil {
		return nil, err
	}
	for index, component := range components {
		if !admitStep() {
			closeOwned()
			return nil, errGitDirSweepHalted
		}
		candidate := filepath.Join(currentPath, component)
		if _, mounted := mountPoints[candidate]; mounted {
			closeOwned()
			// A match on the last component may be that directory being a mount
			// point itself, which a caller may skip without opening; a fresh
			// read of the table decides, as it does for an openat2 refusal.
			if index == len(components)-1 {
				return nil, r.classifyMountCrossing(components)
			}
			return nil, errSymlinkChainOffVolume
		}
		fd, err := syscall.Openat(
			int(current.Fd()),
			component,
			syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_DIRECTORY|syscall.O_NOFOLLOW,
			0,
		)
		if err != nil {
			closeOwned()
			return nil, err
		}
		next := os.NewFile(uintptr(fd), candidate)
		if next == nil {
			_ = syscall.Close(fd)
			closeOwned()
			return nil, fs.ErrInvalid
		}
		openedInfo, err := next.Stat()
		if err != nil || !openedInfo.IsDir() || !anchor.allows(openedInfo) {
			_ = next.Close()
			closeOwned()
			if err != nil {
				return nil, errors.Join(errSymlinkChainOffVolume, err)
			}
			return nil, errSymlinkChainOffVolume
		}
		closeOwned()
		owned = next
		current = next
		currentPath = candidate
	}
	return owned, nil
}

func linuxSweepOpenCanSkipLocalPathFailure(err error) bool {
	return errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ENOTDIR)
}

func linuxOpenat2NeedsGuardedFallback(err error) bool {
	return errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EPERM)
}

func (r *sweepDirectoryRoot) openAfterOpenat2Failure(
	anchor pathTraversalAnchor,
	components []string,
	admitStep func() bool,
	openat2Err error,
) (*os.File, error) {
	if errors.Is(openat2Err, syscall.EXDEV) {
		return nil, r.classifyMountCrossing(components)
	}
	if errors.Is(openat2Err, syscall.ELOOP) || errors.Is(openat2Err, syscall.EAGAIN) {
		return nil, errSymlinkChainOffVolume
	}
	if !linuxOpenat2NeedsGuardedFallback(openat2Err) {
		if linuxSweepOpenCanSkipLocalPathFailure(openat2Err) {
			return nil, openat2Err
		}
		return nil, errors.Join(errSymlinkChainOffVolume, openat2Err)
	}
	// EPERM commonly means a container's seccomp policy denied openat2 rather
	// than the path. The descriptor-relative fallback remains safe because it
	// requires a bounded mount inventory and validates the held root; any failure
	// in those guards is mapped back to errSymlinkChainOffVolume below.
	opened, err := r.openWithoutOpenat2(anchor, components, admitStep)
	if err == nil || errors.Is(err, errSymlinkChainOffVolume) || errors.Is(err, errGitDirSweepHalted) ||
		linuxSweepOpenCanSkipLocalPathFailure(err) {
		return opened, err
	}
	return nil, errors.Join(errSymlinkChainOffVolume, err)
}

// sweepRelativeComponents splits a path below the held root into its
// components, dropping `.` and refusing `..` and absolute paths.
func sweepRelativeComponents(path string) (string, []string, error) {
	rel := filepath.FromSlash(path)
	if rel == "" {
		rel = "."
	}
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", nil, errSymlinkChainOffVolume
	}
	rawComponents := splitNativePathComponents(rel)
	components := make([]string, 0, len(rawComponents))
	for _, component := range rawComponents {
		if component == "." {
			continue
		}
		if component == ".." {
			return "", nil, errSymlinkChainOffVolume
		}
		components = append(components, component)
	}
	return rel, components, nil
}

func (r *sweepDirectoryRoot) Open(anchor pathTraversalAnchor, dir string, admitStep func() bool) (*os.File, error) {
	rel, components, err := sweepRelativeComponents(dir)
	if err != nil {
		return nil, err
	}
	if !admitStep() {
		return nil, errGitDirSweepHalted
	}
	opened, err := r.openat2(rel, linuxSweepDirectoryFlags)
	if err == nil {
		return opened, nil
	}
	return r.openAfterOpenat2Failure(anchor, components, admitStep, err)
}

// OpenFile opens a file below the held root for reading, refusing what Open
// refuses on the way to it: `..`, every symlink and every mount crossing. That
// includes the file itself, which a bind mount can cover. The refusal is part
// of the open, so a directory swapped for a link after the caller last looked
// cannot steer it anywhere. Callers check that the result is a regular file.
func (r *sweepDirectoryRoot) OpenFile(anchor pathTraversalAnchor, path string) (*os.File, error) {
	rel, components, err := sweepRelativeComponents(path)
	if err != nil {
		return nil, err
	}
	if len(components) == 0 {
		return nil, fs.ErrInvalid
	}
	opened, err := r.openat2(rel, linuxSweepFileFlags)
	if err == nil || !linuxOpenat2NeedsGuardedFallback(err) {
		return opened, err
	}
	return r.openFileWithoutOpenat2(anchor, components)
}

// openFileWithoutOpenat2 is OpenFile where openat2 is unavailable. Every
// component, the file included, is first checked against the mount table with
// case and Unicode normalization folded, so a folding filesystem cannot
// resolve an alias spelling into a mount point. The directories are then
// opened as Open opens them without openat2, and the file is opened without
// following a link. The opened file has to be on the root's device as well.
func (r *sweepDirectoryRoot) openFileWithoutOpenat2(anchor pathTraversalAnchor, components []string) (*os.File, error) {
	mountKeys, err := r.heldRootMountKeys()
	if err != nil {
		return nil, err
	}
	candidate := filepath.Clean(r.file.Name())
	for _, component := range components {
		candidate = filepath.Join(candidate, component)
		if _, mounted := mountKeys[mountPathKey(candidate)]; mounted {
			return nil, errSymlinkChainOffVolume
		}
	}
	last := len(components) - 1
	parent, err := r.openWithoutOpenat2(anchor, components[:last], func() bool { return true })
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	fd, err := syscall.Openat(int(parent.Fd()), components[last], linuxSweepFileFlags, 0)
	if err != nil {
		return nil, err
	}
	opened := os.NewFile(uintptr(fd), candidate)
	if opened == nil {
		_ = syscall.Close(fd)
		return nil, fs.ErrInvalid
	}
	info, err := opened.Stat()
	if err != nil || !anchor.allows(info) {
		_ = opened.Close()
		return nil, errors.Join(errSymlinkChainOffVolume, err)
	}
	return opened, nil
}
