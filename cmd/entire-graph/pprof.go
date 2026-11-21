//go:build pprof

// Dev-only profiling hook, compiled in only with `-tags pprof`.
//
// When ENTIRE_GRAPH_PPROF=<dir> is set, the process writes:
//   - <dir>/cpu-<pid>.pprof        CPU profile for the process lifetime
//   - <dir>/heap-peak-<pid>.pprof  heap profile near peak live heap
//   - <dir>/heap-exit-<pid>.pprof  heap profile at exit (useful for alloc_space)
//
// A heap profile taken only at exit would show memory after the work is done,
// so a sampler watches /memory/classes/heap/objects:bytes every 250ms and
// rewrites the peak profile whenever it grows by more than 10%.
//
// Known limitation: panics and OOM kills skip atExit, so the CPU profile is
// lost in those cases. Acceptable for a local dev tool.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"runtime/pprof"
	"sync"
	"time"
)

var atExit = func() {}

func init() {
	dir := os.Getenv("ENTIRE_GRAPH_PPROF")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "pprof: mkdir:", err)
		return
	}
	pid := os.Getpid()
	cpuFile, err := os.Create(filepath.Join(dir, fmt.Sprintf("cpu-%d.pprof", pid)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "pprof: cpu profile:", err)
		return
	}
	if err := pprof.StartCPUProfile(cpuFile); err != nil {
		fmt.Fprintln(os.Stderr, "pprof: start cpu:", err)
		cpuFile.Close()
		return
	}

	writeHeap := func(name string) {
		f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%d.pprof", name, pid)))
		if err != nil {
			fmt.Fprintln(os.Stderr, "pprof: heap profile:", err)
			return
		}
		defer f.Close()
		if err := pprof.Lookup("heap").WriteTo(f, 0); err != nil {
			fmt.Fprintln(os.Stderr, "pprof: write heap:", err)
		}
	}

	var mu sync.Mutex // serializes peak writes against exit
	peakWritten := false
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
		var peak uint64
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			metrics.Read(sample)
			if sample[0].Value.Kind() != metrics.KindUint64 {
				continue
			}
			cur := sample[0].Value.Uint64()
			if cur > peak+peak/10 {
				peak = cur
				mu.Lock()
				runtime.GC() // heap profile reflects the last completed GC
				writeHeap("heap-peak")
				peakWritten = true
				mu.Unlock()
			}
		}
	}()

	var once sync.Once
	atExit = func() {
		once.Do(func() {
			close(stop)
			<-done
			pprof.StopCPUProfile()
			cpuFile.Close()
			mu.Lock()
			defer mu.Unlock()
			runtime.GC()
			if !peakWritten { // short runs never reach a sample tick
				writeHeap("heap-peak")
			}
			writeHeap("heap-exit")
		})
	}
}
