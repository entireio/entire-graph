//go:build darwin || dragonfly || freebsd

package sem

import "syscall"

func bsdMountedOn(row syscall.Statfs_t) []int8 { return row.Mntonname[:] }
