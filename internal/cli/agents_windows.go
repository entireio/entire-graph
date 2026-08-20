//go:build windows

package cli

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	windowsReparseTagMountPoint = 0xa0000003
	windowsSymlinkFlagRelative  = 1
)

// windowsLinkRequiresDirectory returns the target type encoded into a Windows
// symbolic link or name-surrogate reparse point. Windows preserves this bit
// independently of the current target.
func windowsLinkRequiresDirectory(info os.FileInfo) (bool, bool) {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return false, false
	}
	return data.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0, true
}

// windowsRawReparseTarget returns the authoritative substitute name from one
// FSCTL_GET_REPARSE_POINT snapshot. os.Root.Readlink normalizes that name (for
// example, \??\C:\path becomes C:\path), which loses whether lexical cleaning
// would change the target Windows actually traverses. PrintName cannot restore
// that information: it is informational, may be empty, and need not match the
// substitute name the kernel resolves.
func windowsRawReparseTarget(rootPath, name string, expected os.FileInfo) (string, bool, error) {
	return windowsRawReparseTargetAtPath(filepath.Join(rootPath, name), expected)
}

func windowsRawReparseTargetAtPath(fullPath string, expected os.FileInfo) (string, bool, error) {
	path, err := syscall.UTF16PtrFromString(fullPath)
	if err != nil {
		return "", false, err
	}
	handle, err := syscall.CreateFile(
		path,
		0,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS|syscall.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return "", false, err
	}
	file := os.NewFile(uintptr(handle), fullPath)
	if file == nil {
		syscall.CloseHandle(handle)
		return "", false, fmt.Errorf("open raw Windows reparse target: invalid handle")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", false, err
	}
	if !os.SameFile(expected, opened) {
		return "", false, fmt.Errorf("Windows reparse point changed while its target was inspected")
	}
	expectedData, expectedOK := expected.Sys().(*syscall.Win32FileAttributeData)
	openedData, openedOK := opened.Sys().(*syscall.Win32FileAttributeData)
	const relevantAttributes = syscall.FILE_ATTRIBUTE_DIRECTORY | syscall.FILE_ATTRIBUTE_REPARSE_POINT
	if !expectedOK || !openedOK ||
		expectedData.FileAttributes&relevantAttributes != openedData.FileAttributes&relevantAttributes {
		return "", false, fmt.Errorf("Windows reparse point type changed while its target was inspected")
	}

	buffer := make([]byte, syscall.MAXIMUM_REPARSE_DATA_BUFFER_SIZE)
	var returned uint32
	if err := syscall.DeviceIoControl(
		handle,
		syscall.FSCTL_GET_REPARSE_POINT,
		nil,
		0,
		&buffer[0],
		uint32(len(buffer)),
		&returned,
		nil,
	); err != nil {
		return "", false, err
	}
	if returned < 8 {
		return "", false, fmt.Errorf("malformed Windows reparse buffer header")
	}
	total := 8 + int(binary.LittleEndian.Uint16(buffer[4:6]))
	if total < 8 || total > int(returned) || total > len(buffer) {
		return "", false, fmt.Errorf("malformed Windows reparse buffer length")
	}

	tag := binary.LittleEndian.Uint32(buffer[:4])
	pathBufferStart := 0
	relative := false
	switch tag {
	case syscall.IO_REPARSE_TAG_SYMLINK:
		if total < 20 {
			return "", false, fmt.Errorf("malformed Windows symbolic-link reparse buffer")
		}
		flags := binary.LittleEndian.Uint32(buffer[16:20])
		if flags & ^uint32(windowsSymlinkFlagRelative) != 0 {
			return "", false, fmt.Errorf("unsupported Windows symbolic-link flags %#x", flags)
		}
		relative = flags&windowsSymlinkFlagRelative != 0
		pathBufferStart = 20
	case windowsReparseTagMountPoint:
		if total < 16 {
			return "", false, fmt.Errorf("malformed Windows mount-point reparse buffer")
		}
		pathBufferStart = 16
	default:
		return "", false, nil
	}

	substituteOffset := int(binary.LittleEndian.Uint16(buffer[8:10]))
	substituteLength := int(binary.LittleEndian.Uint16(buffer[10:12]))
	substitute, ok := decodeWindowsReparseName(buffer, total, pathBufferStart, substituteOffset, substituteLength)
	if !ok {
		return "", false, fmt.Errorf("malformed Windows reparse substitute name")
	}
	if relative {
		if strings.Contains(substitute, "/") || windowsPathDisablesCleaning(substitute) {
			return "", false, fmt.Errorf("%w: invalid relative Windows reparse target", errUnresolvableAlias)
		}
		if len(substitute) > 0 && os.IsPathSeparator(substitute[0]) {
			if len(substitute) > 1 && os.IsPathSeparator(substitute[1]) {
				return "", false, fmt.Errorf("%w: invalid relative Windows reparse target", errUnresolvableAlias)
			}
			normalized, ok := normalizeWindowsAbsoluteReparseTarget(substitute, true)
			if !ok {
				return "", false, fmt.Errorf("%w: invalid rooted Windows reparse target", errUnresolvableAlias)
			}
			return normalized, true, nil
		}
		if filepath.IsAbs(substitute) || filepath.VolumeName(substitute) != "" {
			return "", false, fmt.Errorf("%w: invalid relative Windows reparse target", errUnresolvableAlias)
		}
		return substitute, true, nil
	}
	normalized, ok := normalizeWindowsAbsoluteReparseTarget(substitute, false)
	if !ok {
		return "", false, fmt.Errorf("%w: unsupported Windows reparse target", errUnresolvableAlias)
	}
	return normalized, true, nil
}

func decodeWindowsReparseName(buffer []byte, total, pathBufferStart, offset, length int) (string, bool) {
	start := pathBufferStart + offset
	end := start + length
	if length == 0 || offset < 0 || offset%2 != 0 || length%2 != 0 ||
		start < pathBufferStart || end < start || end > total || end > len(buffer) {
		return "", false
	}
	name := make([]uint16, length/2)
	for i := range name {
		part := binary.LittleEndian.Uint16(buffer[start+2*i : start+2*i+2])
		if part == 0 {
			return "", false
		}
		name[i] = part
	}
	for i := 0; i < len(name); i++ {
		switch {
		case name[i] >= 0xd800 && name[i] <= 0xdbff:
			i++
			if i >= len(name) || name[i] < 0xdc00 || name[i] > 0xdfff {
				return "", false
			}
		case name[i] >= 0xdc00 && name[i] <= 0xdfff:
			return "", false
		}
	}
	return syscall.UTF16ToString(name), true
}

// normalizeWindowsAbsoluteReparseTarget accepts only absolute substitute names
// whose exact NT spelling can be represented by the ordinary Win32 path that
// os.Root receives. Raw dot components, alternate separators, and trailing
// dots/spaces can have different meanings in the NT namespace, so translating
// any of them would risk authorizing a write the original link cannot perform.
func normalizeWindowsAbsoluteReparseTarget(target string, allowRootRelative bool) (string, bool) {
	const ntPrefix = `\??\`
	upper := strings.ToUpper(target)
	switch {
	case strings.HasPrefix(upper, ntPrefix+`UNC\`):
		target = `\\` + target[len(ntPrefix+`UNC\`):]
	case strings.HasPrefix(upper, ntPrefix):
		target = target[len(ntPrefix):]
		if len(target) < 3 || target[1] != ':' || !os.IsPathSeparator(target[2]) {
			return "", false
		}
	case allowRootRelative && len(target) > 0 && os.IsPathSeparator(target[0]) && !strings.HasPrefix(target, `\\`):
		// A drive-rooted target (\path) is anchored to the link's volume.
	default:
		return "", false
	}
	if strings.Contains(target, "/") || windowsPathDisablesCleaning(target) {
		return "", false
	}
	if cleanWindowsPathPreservingDirectory(target) != target {
		return "", false
	}
	for _, component := range strings.Split(target, `\`) {
		if component == "." || component == ".." ||
			strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
			return "", false
		}
	}
	return target, true
}
