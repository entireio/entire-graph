package agentsetup

import "os"

// These are also used by Graph's caller-selected output-path guard. Keeping its
// reparse interpretation and hop budget here prevents divergent protections.
const (
	ReparseOpaqueAlias = windowsReparseOpaqueAlias
	ReparseInert       = windowsReparseInert
	ReparseResolved    = windowsReparseResolved
)

func WindowsRawReparseTarget(root, name string, info os.FileInfo) (string, windowsReparseKind, error) {
	return windowsRawReparseTarget(root, name, info)
}
func LinkHopLimit(absolute bool) int { return containedLinkHopLimit(absolute) }
