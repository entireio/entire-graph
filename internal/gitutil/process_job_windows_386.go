//go:build windows && 386

package gitutil

import "unsafe"

// jobObjectBasicLimitInformation mirrors JOBOBJECT_BASIC_LIMIT_INFORMATION.
// Win32 gives LARGE_INTEGER eight-byte ABI alignment even though Go aligns an
// int64 to four bytes on 386, so the explicit tail padding is load-bearing for
// the following IO_COUNTERS field in JOBOBJECT_EXTENDED_LIMIT_INFORMATION.
// Only LimitFlags is populated; Windows treats every other zero as no limit.
type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
	_                       uint32
}

// jobObjectExtendedLimitInformation mirrors JOBOBJECT_EXTENDED_LIMIT_INFORMATION.
type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                [6]uint64
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

var (
	_ [48 - unsafe.Sizeof(jobObjectBasicLimitInformation{})]byte
	_ [unsafe.Sizeof(jobObjectBasicLimitInformation{}) - 48]byte
	_ [112 - unsafe.Sizeof(jobObjectExtendedLimitInformation{})]byte
	_ [unsafe.Sizeof(jobObjectExtendedLimitInformation{}) - 112]byte
)
