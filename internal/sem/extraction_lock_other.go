//go:build !darwin && !linux && !windows

package sem

import "os"

// Unverified locking semantics must not weaken persistent quota admission.
func tryExtractionLock(*os.File) error { return os.ErrPermission }
