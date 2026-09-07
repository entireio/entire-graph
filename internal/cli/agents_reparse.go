package cli

import "strings"

// Windows reparse-point decisions that are PURE, and therefore live here rather than behind
// `//go:build windows`.
//
// Everything in agents_windows.go needs a Windows kernel to run: it opens handles, issues
// FSCTL_GET_REPARSE_POINT and reads the buffer that comes back. The DECISIONS taken about what
// comes back are ordinary arithmetic and string work, and keeping them here is what lets them be
// asserted from any machine — the alternative is a rule that only a Windows CI leg can ever
// contradict, on a code path whose whole job is to refuse a hostile alias.

// windowsReparseKind is what a Windows reparse point means to a path walk.
type windowsReparseKind int

const (
	// windowsReparseInert is a reparse point whose tag is NOT a name surrogate. Such a tag
	// stores data ABOUT the file — deduplication, WOF compression, cloud placeholders — and
	// Windows resolves the path straight through it, so the walk treats it as the ordinary
	// component it is.
	windowsReparseInert windowsReparseKind = iota
	// windowsReparseResolved is a reparse point this code decoded, meaning a symbolic link or a
	// mount point. Its substitute name is returned alongside it.
	windowsReparseResolved
	// windowsReparseOpaqueAlias is a NAME SURROGATE whose tag this code cannot decode. Windows
	// FOLLOWS it during path resolution, to somewhere this walk cannot name, so it is neither
	// an ordinary component nor a link that can be expanded.
	windowsReparseOpaqueAlias
)

// windowsReparseTagNameSurrogateBit is ntifs.h's IsReparseTagNameSurrogate: a tag with this bit
// set names ANOTHER named entity, and the object manager substitutes that entity while resolving
// the path. Every alias Windows follows carries it — IO_REPARSE_TAG_SYMLINK (0xA000000C),
// IO_REPARSE_TAG_MOUNT_POINT (0xA0000003), IO_REPARSE_TAG_LX_SYMLINK, IO_REPARSE_TAG_GLOBAL_REPARSE
// — and every tag that merely annotates a file lacks it: IO_REPARSE_TAG_DEDUP (0x80000013),
// IO_REPARSE_TAG_WOF (0x80000017), IO_REPARSE_TAG_HSM (0xC0000004), IO_REPARSE_TAG_APPEXECLINK
// (0x8000001B).
const windowsReparseTagNameSurrogateBit = 0x20000000

// windowsReparseKindForTag classifies a reparse tag this code did not decode.
//
// The distinction is load-bearing rather than tidy. Since Go 1.23 Windows reports EVERY reparse
// point as ModeIrregular unless it is one of the two Go itself recognises, so an undecoded tag and
// an ordinary directory look alike to the walk above. Treating them all as ordinary components was
// wrong in one direction only, and it is the dangerous one: an undecodable NAME SURROGATE is
// still followed by Windows, so its own target's links are neither expanded nor COUNTED, and a
// chain long enough for Windows to refuse with STATUS_REPARSE_POINT_NOT_RESOLVED could be
// translated into a repository-relative name and written to.
func windowsReparseKindForTag(tag uint32) windowsReparseKind {
	if tag&windowsReparseTagNameSurrogateBit != 0 {
		return windowsReparseOpaqueAlias
	}
	return windowsReparseInert
}

// splitWindowsVolumeGUIDPath splits a volume-GUID path into the volume root it is anchored at and
// the remainder beneath it, and reports whether it is one at all.
//
// This is the spelling Windows stores in a junction created against a VOLUME rather than a drive
// letter, and it is what a raw reparse buffer holds for one: `\??\Volume{GUID}\sub\file`. It names
// an ordinary local directory — the same directory `C:\sub\file` names when the GUID is that
// drive's volume — but it is spelled in the device namespace, where the confined relative
// operations that follow cannot be applied to it.
//
// The volume root is returned in the `\\?\Volume{GUID}\` form, with its trailing separator,
// because that is the spelling that opens the volume's ROOT DIRECTORY; without the separator the
// same name opens the volume device instead, which is not a directory and not what the identity
// comparison is asking about.
//
// Only the GUID form is recognised. Every other device name — GLOBALROOT, named pipes, network
// providers, `\\?\UNC` — is left to the refusal it already has, because none of them is a local
// directory that a drive spelling can name.
func splitWindowsVolumeGUIDPath(path string) (volumeRoot, suffix string, ok bool) {
	normalized := strings.ReplaceAll(path, "/", `\`)
	upper := strings.ToUpper(normalized)
	var rest string
	switch {
	case strings.HasPrefix(upper, `\??\`), strings.HasPrefix(upper, `\\?\`), strings.HasPrefix(upper, `\\.\`):
		rest = normalized[4:]
	default:
		return "", "", false
	}
	if !strings.HasPrefix(strings.ToUpper(rest), `VOLUME{`) {
		return "", "", false
	}
	end := strings.Index(rest, `}`)
	if end < 0 {
		return "", "", false
	}
	guid := rest[:end+1]
	suffix = rest[end+1:]
	if suffix != "" {
		if !strings.HasPrefix(suffix, `\`) {
			// `Volume{...}x` is a different device name, not this volume with a suffix.
			return "", "", false
		}
		suffix = strings.TrimPrefix(suffix, `\`)
	}
	return `\\?\` + guid + `\`, suffix, true
}
