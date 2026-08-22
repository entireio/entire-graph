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
}

func newSweepDirectoryRoot(repo string) (*sweepDirectoryRoot, error) {
	file, err := os.Open(repo)
	if err != nil {
		return nil, err
	}
	return &sweepDirectoryRoot{file: file}, nil
}

func (r *sweepDirectoryRoot) Close() error { return r.file.Close() }

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

func (r *sweepDirectoryRoot) openat2(rel string) (*os.File, error) {
	name, err := syscall.BytePtrFromString(rel)
	if err != nil {
		return nil, err
	}
	how := linuxOpenHow{
		flags: uint64(syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_DIRECTORY),
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

func (r *sweepDirectoryRoot) loadMountPoints() (map[string]struct{}, error) {
	r.mountOnce.Do(func() {
		r.mountPoints, r.mountErr = readLinuxMountPoints()
	})
	return r.mountPoints, r.mountErr
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
	mountPoints, err := r.loadMountPoints()
	if err != nil {
		return nil, err
	}
	// mountinfo identifies mount points by their current absolute paths, while
	// the fallback below opens components relative to the held repository
	// descriptor. If that repository was renamed after it was opened, its stale
	// name could no longer match a mount-table entry and the fallback could walk
	// across that mount. Fail closed unless the name still identifies the exact
	// directory held by r.file.
	heldInfo, err := r.file.Stat()
	if err != nil {
		return nil, err
	}
	namedInfo, err := os.Lstat(r.file.Name())
	if err != nil {
		return nil, err
	}
	if !os.SameFile(heldInfo, namedInfo) {
		return nil, errSymlinkChainOffVolume
	}
	for _, component := range components {
		if !admitStep() {
			closeOwned()
			return nil, errGitDirSweepHalted
		}
		candidate := filepath.Join(currentPath, component)
		if _, mounted := mountPoints[candidate]; mounted {
			closeOwned()
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
				return nil, err
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

func (r *sweepDirectoryRoot) Open(anchor pathTraversalAnchor, dir string, admitStep func() bool) (*os.File, error) {
	rel := filepath.FromSlash(dir)
	if rel == "" {
		rel = "."
	}
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return nil, errSymlinkChainOffVolume
	}
	rawComponents := splitNativePathComponents(rel)
	components := make([]string, 0, len(rawComponents))
	for _, component := range rawComponents {
		if component == "." {
			continue
		}
		if component == ".." {
			return nil, errSymlinkChainOffVolume
		}
		components = append(components, component)
	}
	if !admitStep() {
		return nil, errGitDirSweepHalted
	}
	opened, err := r.openat2(rel)
	if err == nil {
		return opened, nil
	}
	if !errors.Is(err, syscall.ENOSYS) {
		return nil, err
	}
	return r.openWithoutOpenat2(anchor, components, admitStep)
}
