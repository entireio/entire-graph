//go:build !pprof

package main

// atExit is a no-op in release builds; see pprof.go for the profiling hook.
var atExit = func() {}
