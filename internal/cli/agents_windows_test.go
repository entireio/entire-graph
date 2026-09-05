//go:build windows

package cli

import (
	"encoding/binary"
	"testing"
)

func TestDecodeWindowsReparseNameRejectsUnrepresentableUTF16(t *testing.T) {
	tests := []struct {
		name  string
		units []uint16
		want  string
		ok    bool
	}{
		{name: "ordinary", units: []uint16{'a'}, want: "a", ok: true},
		{name: "valid surrogate pair", units: []uint16{0xd83d, 0xde00}, want: "😀", ok: true},
		{name: "embedded NUL", units: []uint16{'a', 0}, ok: false},
		{name: "unpaired high surrogate", units: []uint16{0xd800}, ok: false},
		{name: "high surrogate before ordinary code unit", units: []uint16{0xd800, 'a'}, ok: false},
		{name: "unpaired low surrogate", units: []uint16{0xdc00}, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buffer := make([]byte, 20+2*len(tt.units))
			for i, unit := range tt.units {
				binary.LittleEndian.PutUint16(buffer[20+2*i:], unit)
			}
			got, ok := decodeWindowsReparseName(buffer, len(buffer), 20, 0, 2*len(tt.units))
			if ok != tt.ok || got != tt.want {
				t.Fatalf("decodeWindowsReparseName() = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestNormalizeWindowsAbsoluteReparseTargetRejectsRawCleaning(t *testing.T) {
	tests := []struct {
		name              string
		target            string
		allowRootRelative bool
		want              string
		ok                bool
	}{
		{name: "drive absolute", target: `\??\C:\repo\shared.md`, want: `C:\repo\shared.md`, ok: true},
		{name: "UNC", target: `\??\UNC\server\share\shared.md`, want: `\\server\share\shared.md`, ok: true},
		{name: "drive rooted relative link", target: `\repo\shared.md`, allowRootRelative: true, want: `\repo\shared.md`, ok: true},
		{name: "drive rooted non-relative link", target: `\repo\shared.md`, ok: false},
		{name: "raw parent traversal", target: `\??\C:\repo\missing\..\victim.md`, ok: false},
		{name: "repeated separator", target: `\??\C:\repo\\victim.md`, ok: false},
		{name: "alternate separator", target: `\??\C:\repo/victim.md`, ok: false},
		{name: "trailing dot", target: `\??\C:\repo\victim.md.`, ok: false},
		{name: "trailing space", target: `\??\C:\repo\victim.md `, ok: false},
		{name: "device namespace", target: `\??\GLOBALROOT\Device\HarddiskVolume1\victim.md`, ok: false},
		// A volume-GUID target is no longer refused for its SPELLING, but it is still
		// refused unless the filesystem says the GUID names the root of the link's own
		// drive. No such volume exists here, so the refusal stands and the identity
		// check is the only thing that could ever lift it.
		{name: "volume GUID that names no volume", target: `\??\Volume{00000000-0000-0000-0000-000000000000}\repo\shared.md`, ok: false},
		{name: "volume GUID naming the volume root itself", target: `\??\Volume{00000000-0000-0000-0000-000000000000}\`, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizeWindowsAbsoluteReparseTarget(tt.target, tt.allowRootRelative, `C:`)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("normalizeWindowsAbsoluteReparseTarget() = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}
