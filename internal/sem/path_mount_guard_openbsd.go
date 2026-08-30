//go:build openbsd

package sem

import "syscall"

func bsdMountedOn(row syscall.Statfs_t) []int8 { return row.F_mntonname[:] }
