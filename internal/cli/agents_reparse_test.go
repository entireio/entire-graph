package cli

import "testing"

// These pin the two PLATFORM decisions that no test on this machine can otherwise reach.
//
// Both rules are about Windows and AIX, and neither can be executed here: one needs a Windows
// kernel to produce a reparse point, the other needs AIX to refuse a symlink chain. What CAN be
// asserted anywhere is the decision itself, because both were deliberately written as pure
// functions of a tag and of a GOOS. That is the whole point of them being separated out — the
// alternative is a rule about hostile input that only a CI leg on that platform could ever
// contradict, on code whose job is to refuse.
//
// The limit is stated rather than hidden: these prove the DECISION, not the kernel behaviour it
// is a model of. The syscall path around them stays covered only by the Windows CI leg.

// TestLinkHopLimitStaysAtOrBelowEachHostsOwnLimit is the regression for the AIX entry.
//
// The limit is not a tuning knob. resolveContainedLanding STRIPS the links it accepted before
// handing the result to os.Root, so a chain this function allows is one os.Root will never see as
// a chain at all — it becomes a direct in-repository name and is written to. Allowing more hops
// than the host's own pathname resolution therefore converts that host's ELOOP into a write.
//
// AIX resolves at most MAXSYMLINKS = 20 links, the System V figure illumos and Solaris also carry,
// not the 32 the BSDs allow. It was grouped with Darwin and the BSDs at 31, so a 20-to-31-link
// AGENTS.md alias that AIX itself rejects was accepted here.
func TestLinkHopLimitStaysAtOrBelowEachHostsOwnLimit(t *testing.T) {
	for _, tt := range []struct {
		goos              string
		fullyQualified    bool
		want              int
		hostPathnameLimit string
	}{
		{goos: "aix", want: 19, hostPathnameLimit: "MAXSYMLINKS 20"},
		{goos: "illumos", want: 19, hostPathnameLimit: "MAXSYMLINKS 20"},
		{goos: "solaris", want: 19, hostPathnameLimit: "MAXSYMLINKS 20"},
		{goos: "darwin", want: 31, hostPathnameLimit: "MAXSYMLINKS 32"},
		{goos: "freebsd", want: 31, hostPathnameLimit: "MAXSYMLINKS 32"},
		{goos: "netbsd", want: 31, hostPathnameLimit: "MAXSYMLINKS 32"},
		{goos: "openbsd", want: 31, hostPathnameLimit: "MAXSYMLINKS 32"},
		{goos: "dragonfly", want: 31, hostPathnameLimit: "MAXSYMLINKS 32"},
		{goos: "linux", want: 40, hostPathnameLimit: "SYMLOOP_MAX 40"},
		{goos: "windows", want: 63, hostPathnameLimit: "63 reparse points"},
		{goos: "windows", fullyQualified: true, want: 31, hostPathnameLimit: "31 fully qualified"},
	} {
		name := tt.goos
		if tt.fullyQualified {
			name += " (fully qualified target)"
		}
		t.Run(name, func(t *testing.T) {
			if got := linkHopLimitFor(tt.goos, tt.fullyQualified); got != tt.want {
				t.Fatalf("linkHopLimitFor(%q, %v) = %d, want %d; %s allows %s",
					tt.goos, tt.fullyQualified, got, tt.want, tt.goos, tt.hostPathnameLimit)
			}
		})
	}
}

// TestWindowsNameSurrogatesAreNotOrdinaryComponents is the regression for the reparse-tag entry.
//
// Since Go 1.23 Windows reports every reparse point Go does not itself recognise as ModeIrregular,
// so an undecodable tag and an ordinary directory arrive at the walk looking the same. They are
// not the same: a NAME SURROGATE is an alias the object manager substitutes while resolving the
// path, so counting one as an ordinary component leaves its own chain neither expanded nor counted
// against the reparse budget, and a chain Windows would refuse can be translated into a
// repository-relative name and written to.
//
// The tag values below are the documented ones; what is being asserted is the ntifs.h rule they
// illustrate — IsReparseTagNameSurrogate is bit 0x20000000 — rather than the list itself.
func TestWindowsNameSurrogatesAreNotOrdinaryComponents(t *testing.T) {
	for _, tt := range []struct {
		name string
		tag  uint32
		want windowsReparseKind
	}{
		{name: "IO_REPARSE_TAG_SYMLINK", tag: 0xA000000C, want: windowsReparseOpaqueAlias},
		{name: "IO_REPARSE_TAG_MOUNT_POINT", tag: 0xA0000003, want: windowsReparseOpaqueAlias},
		{name: "IO_REPARSE_TAG_LX_SYMLINK", tag: 0xA000001D, want: windowsReparseOpaqueAlias},
		{name: "IO_REPARSE_TAG_GLOBAL_REPARSE", tag: 0xA0000019, want: windowsReparseOpaqueAlias},
		{name: "IO_REPARSE_TAG_DEDUP", tag: 0x80000013, want: windowsReparseInert},
		{name: "IO_REPARSE_TAG_NFS", tag: 0x80000014, want: windowsReparseInert},
		{name: "IO_REPARSE_TAG_WOF", tag: 0x80000017, want: windowsReparseInert},
		{name: "IO_REPARSE_TAG_APPEXECLINK", tag: 0x8000001B, want: windowsReparseInert},
		{name: "IO_REPARSE_TAG_HSM", tag: 0xC0000004, want: windowsReparseInert},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := windowsReparseKindForTag(tt.tag); got != tt.want {
				t.Fatalf("windowsReparseKindForTag(%#x) = %d, want %d; the name-surrogate bit is %v",
					tt.tag, got, tt.want, tt.tag&windowsReparseTagNameSurrogateBit != 0)
			}
		})
	}
}

// TestSplitWindowsVolumeGUIDPathFindsTheVolumeToIdentityCheck is the regression for the
// volume-GUID entry, and it pins the half of that rule that is a decision rather than a stat.
//
// repoRelativeName already says local device and volume-GUID spellings are "left to the identity
// check because they can name the same directory as an ordinary drive path", and that check could
// never be reached: the raw reparse target was refused for its spelling first. Splitting the path
// is what lets the check be asked at all — the volume ROOT it returns is the thing os.Stat compares
// with the drive, and it carries its trailing separator because without one the same name opens the
// volume DEVICE rather than its root directory.
//
// Only the GUID form is recognised here. Every other device namespace keeps the refusal it has.
func TestSplitWindowsVolumeGUIDPathFindsTheVolumeToIdentityCheck(t *testing.T) {
	const guid = `Volume{7f2b1c34-0000-0000-0000-100000000000}`
	for _, tt := range []struct {
		name   string
		path   string
		root   string
		suffix string
		ok     bool
	}{
		{name: "NT namespace", path: `\??\` + guid + `\repo\shared.md`, root: `\\?\` + guid + `\`, suffix: `repo\shared.md`, ok: true},
		{name: "Win32 device namespace", path: `\\?\` + guid + `\repo\shared.md`, root: `\\?\` + guid + `\`, suffix: `repo\shared.md`, ok: true},
		{name: "dot device namespace", path: `\\.\` + guid + `\shared.md`, root: `\\?\` + guid + `\`, suffix: `shared.md`, ok: true},
		{name: "the volume root itself", path: `\??\` + guid + `\`, root: `\\?\` + guid + `\`, ok: true},
		{name: "drive letter", path: `\??\C:\repo\shared.md`, ok: false},
		{name: "UNC share", path: `\??\UNC\server\share\shared.md`, ok: false},
		{name: "another device", path: `\??\GLOBALROOT\Device\HarddiskVolume1\shared.md`, ok: false},
		{name: "unterminated GUID", path: `\??\Volume{7f2b1c34`, ok: false},
		{name: "a longer device name that merely starts with the GUID", path: `\??\` + guid + `x\shared.md`, ok: false},
		{name: "an ordinary path", path: `C:\repo\shared.md`, ok: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, suffix, ok := splitWindowsVolumeGUIDPath(tt.path)
			if ok != tt.ok || root != tt.root || suffix != tt.suffix {
				t.Fatalf("splitWindowsVolumeGUIDPath(%q) = %q, %q, %v; want %q, %q, %v",
					tt.path, root, suffix, ok, tt.root, tt.suffix, tt.ok)
			}
		})
	}
}
