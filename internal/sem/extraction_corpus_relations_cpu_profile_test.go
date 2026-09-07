package sem

// This opt-in diagnostic was added after the retained 120-second Kubernetes
// full-profile timeout. It is test-harness plumbing only and makes no
// performance or release claim.

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"
)

const extractionCorpusRelationsCPUProfileChildEnv = "ENTIRE_GRAPH_EXTRACTION_CORPUS_RELATIONS_CPU_PROFILE_TEST_CHILD"

const (
	extractionCorpusRelationsCPUProfileName       = "relations-cpu.pprof"
	extractionCorpusRelationsCPUProfileStatusName = "relations-cpu.pprof.json"
	extractionCorpusRelationsCPUProfileMaxBytes   = 8 << 20
)

type extractionCorpusCPUProfileBackend interface {
	Start(io.Writer) error
	Stop()
}

type runtimeCPUProfileBackend struct{}

func (runtimeCPUProfileBackend) Start(w io.Writer) error { return pprof.StartCPUProfile(w) }
func (runtimeCPUProfileBackend) Stop()                   { pprof.StopCPUProfile() }

type boundedCPUProfileWriter struct {
	file    *os.File
	written int64
	err     error
}

func (w *boundedCPUProfileWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	remaining := int64(extractionCorpusRelationsCPUProfileMaxBytes) - w.written
	if remaining <= 0 || int64(len(p)) > remaining {
		w.err = errors.New("relations CPU profile exceeded 8 MiB")
		return 0, w.err
	}
	n, err := w.file.Write(p)
	w.written += int64(n)
	if err != nil {
		w.err = err
	}
	return n, err
}

type extractionCorpusCPUProfileStatus struct {
	Status                 string `json:"status"`
	TriggerElapsedNS       int64  `json:"trigger_elapsed_ns,omitempty"`
	WindowNS               int64  `json:"window_ns"`
	ActualNS               int64  `json:"actual_ns,omitempty"`
	Bytes                  int64  `json:"bytes,omitempty"`
	SHA256                 string `json:"sha256,omitempty"`
	Error                  string `json:"error,omitempty"`
	DiagnosticOnly         bool   `json:"diagnostic_only"`
	NoPerformanceClaim     bool   `json:"no_performance_claim"`
	ProfileOverheadPresent bool   `json:"profile_overhead_present"`
}

type extractionCorpusRelationsCPUProfiler struct {
	mu                      sync.Mutex
	backend                 extractionCorpusCPUProfileBackend
	profilePath, statusPath string
	window, latestStart     time.Duration
	started                 time.Time
	triggerElapsed          time.Duration
	writer                  *boundedCPUProfileWriter
	stop                    chan struct{}
	done                    chan struct{}
	stopOnce                sync.Once
	finishing               bool
	closed                  bool
	err                     error
	createStatusTemp        func(string, string) (*os.File, error)
}

func extractionCorpusRelationsCPUProfileEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(extractionCorpusRelationsCPUProfileEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func newExtractionCorpusRelationsCPUProfiler(enabled bool, config extractionCorpusEvaluationConfig) (*extractionCorpusRelationsCPUProfiler, error) {
	if !enabled {
		return nil, nil
	}
	outputPath := config.OutputPath
	if !filepath.IsAbs(outputPath) {
		return nil, errors.New("relations CPU profile requires an absolute observation output path")
	}
	dir := filepath.Dir(outputPath)
	p := filepath.Join(dir, extractionCorpusRelationsCPUProfileName)
	s := filepath.Join(dir, extractionCorpusRelationsCPUProfileStatusName)
	configured := []struct{ name, path string }{{"observation", config.OutputPath}, {"manifest", config.ManifestPath}, {"diagnostics", config.DiagnosticsPath}}
	for _, artifact := range []struct{ name, path string }{{"profile", p}, {"profile status", s}} {
		for _, other := range configured {
			if other.path == "" {
				continue
			}
			equal, err := extractionCorpusArtifactPathsEqual(artifact.path, other.path)
			if err != nil {
				return nil, fmt.Errorf("resolve relations CPU profile and %s paths: %w", other.name, err)
			}
			if equal {
				return nil, fmt.Errorf("relations CPU %s path must differ from %s path", artifact.name, other.name)
			}
		}
	}
	if equal, err := extractionCorpusArtifactPathsEqual(p, s); err != nil {
		return nil, fmt.Errorf("resolve relations CPU profile paths: %w", err)
	} else if equal {
		return nil, errors.New("relations CPU profile and status paths must differ")
	}
	for _, path := range []string{p, s} {
		if _, err := os.Lstat(path); err == nil {
			return nil, errors.New("relations CPU profile artifact already exists")
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	if runtime.GOOS == "windows" {
		return nil, errors.New("relations CPU profile requires directory sync and is unsupported on Windows")
	}
	return newExtractionCorpusRelationsCPUProfilerForTest(p, s, 20*time.Second, 90*time.Second, runtimeCPUProfileBackend{}), nil
}

func newExtractionCorpusRelationsCPUProfilerForTest(profilePath, statusPath string, window, latest time.Duration, backend extractionCorpusCPUProfileBackend) *extractionCorpusRelationsCPUProfiler {
	return &extractionCorpusRelationsCPUProfiler{backend: backend, profilePath: profilePath, statusPath: statusPath, window: window, latestStart: latest}
}

func (p *extractionCorpusRelationsCPUProfiler) Observe(event ProgressEvent, elapsed time.Duration) {
	if p == nil || event.Phase != BuildPhaseRelations {
		return
	}
	p.mu.Lock()
	if p.started.IsZero() && !p.closed {
		if elapsed > p.latestStart {
			p.closed = true
			p.mu.Unlock()
			p.writeStatus(extractionCorpusCPUProfileStatus{Status: "missed_safe_window", TriggerElapsedNS: elapsed.Nanoseconds()})
			return
		}
		// Reserve the one start and persist intent before opening or starting the
		// process-global profiler. A kill in setup therefore leaves an explicit
		// non-complete state instead of an unexplained partial profile.
		p.closed = true
		p.triggerElapsed = elapsed
		p.mu.Unlock()
		p.writeStatus(extractionCorpusCPUProfileStatus{Status: "starting", TriggerElapsedNS: elapsed.Nanoseconds()})
		if p.Err() != nil {
			return
		}
		file, err := os.OpenFile(p.profilePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			p.setErr(err)
			p.writeStatus(extractionCorpusCPUProfileStatus{Status: "error", Error: err.Error()})
			return
		}
		writer := &boundedCPUProfileWriter{file: file}
		if err := p.backend.Start(writer); err != nil {
			_ = file.Close()
			p.setErr(err)
			p.writeStatus(extractionCorpusCPUProfileStatus{Status: "error", Error: err.Error()})
			return
		}
		p.mu.Lock()
		p.started = time.Now()
		p.writer = writer
		p.stop = make(chan struct{})
		p.done = make(chan struct{})
		p.closed = false
		stop, done, window := p.stop, p.done, p.window
		p.mu.Unlock()
		// This durable marker makes a hard kill during the sampling window
		// explicit: only finish replaces it with complete or incomplete.
		p.writeStatus(extractionCorpusCPUProfileStatus{Status: "active", TriggerElapsedNS: elapsed.Nanoseconds()})
		go func() {
			select {
			case <-time.After(window):
			case <-stop:
			}
			p.finish()
			close(done)
		}()
		return
	}
	p.mu.Unlock()
}

func (p *extractionCorpusRelationsCPUProfiler) finish() {
	p.mu.Lock()
	if p.closed || p.finishing {
		p.mu.Unlock()
		return
	}
	p.finishing = true
	backend, writer, started, trigger := p.backend, p.writer, p.started, p.triggerElapsed
	p.mu.Unlock()
	backend.Stop()
	err := writer.err
	if syncErr := writer.file.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := writer.file.Close(); err == nil {
		err = closeErr
	}
	status := extractionCorpusCPUProfileStatus{Status: "complete", TriggerElapsedNS: trigger.Nanoseconds(), ActualNS: time.Since(started).Nanoseconds(), Bytes: writer.written}
	if err != nil {
		status.Status = "incomplete"
		status.Error = err.Error()
		p.setErr(err)
	} else if data, readErr := os.ReadFile(p.profilePath); readErr != nil {
		status.Status = "incomplete"
		status.Error = readErr.Error()
		p.setErr(readErr)
	} else {
		sum := sha256.Sum256(data)
		status.SHA256 = hex.EncodeToString(sum[:])
	}
	p.writeStatus(status)
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
}

func (p *extractionCorpusRelationsCPUProfiler) writeStatus(s extractionCorpusCPUProfileStatus) {
	if p == nil {
		return
	}
	s.WindowNS = p.window.Nanoseconds()
	s.DiagnosticOnly = true
	s.NoPerformanceClaim = true
	s.ProfileOverheadPresent = true
	data, _ := json.MarshalIndent(s, "", "  ")
	data = append(data, '\n')
	dir := filepath.Dir(p.statusPath)
	createTemp := p.createStatusTemp
	if createTemp == nil {
		createTemp = os.CreateTemp
	}
	tmp, err := createTemp(dir, ".relations-cpu-status-*")
	if err != nil {
		p.setErr(err)
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	err = tmp.Chmod(0600)
	if err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmpPath, p.statusPath)
	}
	if err == nil {
		if directory, openErr := os.Open(dir); openErr != nil {
			err = openErr
		} else {
			err = directory.Sync()
			if closeErr := directory.Close(); err == nil {
				err = closeErr
			}
		}
	}
	if err != nil {
		p.setErr(err)
	}
}

func (p *extractionCorpusRelationsCPUProfiler) setErr(err error) {
	if p == nil || err == nil {
		return
	}
	p.mu.Lock()
	p.err = errors.Join(p.err, err)
	p.mu.Unlock()
}
func (p *extractionCorpusRelationsCPUProfiler) Err() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *extractionCorpusRelationsCPUProfiler) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.started.IsZero() {
		p.closed = true
		p.mu.Unlock()
		return
	}
	stop, done := p.stop, p.done
	p.stopOnce.Do(func() { close(stop) })
	p.mu.Unlock()
	<-done
}

type fakeCPUProfileBackend struct {
	mu            sync.Mutex
	starts, stops int
	writer        io.Writer
	startErr      error
	removeOnStop  bool
}

func (f *fakeCPUProfileBackend) Start(w io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	f.writer = w
	return f.startErr
}
func (f *fakeCPUProfileBackend) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	if f.writer != nil {
		_, _ = f.writer.Write([]byte("profile"))
		if f.removeOnStop {
			if writer, ok := f.writer.(*boundedCPUProfileWriter); ok {
				_ = os.Remove(writer.file.Name())
			}
		}
	}
}

func requireRelationsCPUProfilePlatform(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("relations CPU profiling requires durable directory sync")
	}
}

func TestExtractionCorpusRelationsCPUProfileTriggersOnceAndFlushesEarly(t *testing.T) {
	requireRelationsCPUProfilePlatform(t)
	d := t.TempDir()
	b := &fakeCPUProfileBackend{}
	p := newExtractionCorpusRelationsCPUProfilerForTest(filepath.Join(d, "p"), filepath.Join(d, "s"), time.Hour, 90*time.Second, b)
	p.Observe(ProgressEvent{Phase: BuildPhaseParse}, time.Second)
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations}, 2*time.Second)
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations}, 3*time.Second)
	p.Close()
	if b.starts != 1 || b.stops != 1 {
		t.Fatalf("start/stop=%d/%d", b.starts, b.stops)
	}
	var s extractionCorpusCPUProfileStatus
	data, _ := os.ReadFile(filepath.Join(d, "s"))
	if err := json.Unmarshal(data, &s); err != nil || s.Status != "complete" || s.Bytes == 0 || s.SHA256 == "" {
		t.Fatalf("status=%+v err=%v", s, err)
	}
}

func TestExtractionCorpusRelationsCPUProfileDisabledHasNoState(t *testing.T) {
	p, err := newExtractionCorpusRelationsCPUProfiler(false, extractionCorpusEvaluationConfig{OutputPath: "relative-is-ignored"})
	if err != nil || p != nil {
		t.Fatalf("disabled profiler=%v err=%v", p, err)
	}
}

func TestExtractionCorpusRelationsCPUProfileEnabledPlatformBoundary(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific constructor boundary")
	}
	d := t.TempDir()
	if _, err := newExtractionCorpusRelationsCPUProfiler(true, extractionCorpusEvaluationConfig{OutputPath: filepath.Join(d, "observation")}); err == nil || !strings.Contains(err.Error(), "unsupported on Windows") {
		t.Fatalf("error=%v", err)
	}
	if p, err := newExtractionCorpusRelationsCPUProfiler(false, extractionCorpusEvaluationConfig{OutputPath: "relative"}); err != nil || p != nil {
		t.Fatalf("disabled profiler=%v err=%v", p, err)
	}
}

func TestExtractionCorpusRelationsCPUProfileRefusesExistingArtifact(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, extractionCorpusRelationsCPUProfileName), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newExtractionCorpusRelationsCPUProfiler(true, extractionCorpusEvaluationConfig{OutputPath: filepath.Join(d, "observation.ndjson")}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error=%v", err)
	}
}

func TestExtractionCorpusRelationsCPUProfileRejectsConfiguredPathAliases(t *testing.T) {
	d := t.TempDir()
	cases := []extractionCorpusEvaluationConfig{
		{OutputPath: filepath.Join(d, extractionCorpusRelationsCPUProfileName)},
		{OutputPath: filepath.Join(d, "observation"), DiagnosticsPath: filepath.Join(d, extractionCorpusRelationsCPUProfileStatusName)},
		{OutputPath: filepath.Join(d, "observation"), ManifestPath: filepath.Join(d, extractionCorpusRelationsCPUProfileName)},
	}
	for _, config := range cases {
		if _, err := newExtractionCorpusRelationsCPUProfiler(true, config); err == nil || !strings.Contains(err.Error(), "must differ") {
			t.Fatalf("config=%+v error=%v", config, err)
		}
	}
}

func TestExtractionCorpusRelationsCPUProfileRefusesLateStart(t *testing.T) {
	requireRelationsCPUProfilePlatform(t)
	d := t.TempDir()
	b := &fakeCPUProfileBackend{}
	p := newExtractionCorpusRelationsCPUProfilerForTest(filepath.Join(d, "p"), filepath.Join(d, "s"), 20*time.Second, 90*time.Second, b)
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations}, 90*time.Second+time.Nanosecond)
	if b.starts != 0 {
		t.Fatal("late profile started")
	}
	var s extractionCorpusCPUProfileStatus
	data, _ := os.ReadFile(filepath.Join(d, "s"))
	_ = json.Unmarshal(data, &s)
	if s.Status != "missed_safe_window" {
		t.Fatalf("status=%+v", s)
	}
}

func TestExtractionCorpusRelationsCPUProfileAllowsLatestBoundary(t *testing.T) {
	requireRelationsCPUProfilePlatform(t)
	d := t.TempDir()
	b := &fakeCPUProfileBackend{}
	p := newExtractionCorpusRelationsCPUProfilerForTest(filepath.Join(d, "p"), filepath.Join(d, "s"), time.Hour, 90*time.Second, b)
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations}, 90*time.Second)
	p.Close()
	if b.starts != 1 || b.stops != 1 {
		t.Fatalf("start/stop=%d/%d", b.starts, b.stops)
	}
}

func TestExtractionCorpusRelationsCPUProfileStartFailureDoesNotStop(t *testing.T) {
	requireRelationsCPUProfilePlatform(t)
	d := t.TempDir()
	b := &fakeCPUProfileBackend{startErr: errors.New("start failed")}
	p := newExtractionCorpusRelationsCPUProfilerForTest(filepath.Join(d, "p"), filepath.Join(d, "s"), time.Second, 90*time.Second, b)
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations}, time.Second)
	p.Close()
	if b.stops != 0 {
		t.Fatal("stopped profiler whose start failed")
	}
}

func TestExtractionCorpusRelationsCPUProfileWriterIsBoundedAndSticky(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "profile")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := &boundedCPUProfileWriter{file: f, written: extractionCorpusRelationsCPUProfileMaxBytes}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Fatal("limit accepted write")
	}
	first := w.err
	if _, err := w.Write([]byte("y")); err != first {
		t.Fatalf("error not sticky: %v then %v", first, err)
	}
}

func TestExtractionCorpusRelationsCPUProfileReadFailureIsReported(t *testing.T) {
	requireRelationsCPUProfilePlatform(t)
	d := t.TempDir()
	b := &fakeCPUProfileBackend{removeOnStop: true}
	p := newExtractionCorpusRelationsCPUProfilerForTest(filepath.Join(d, "p"), filepath.Join(d, "s"), time.Hour, 90*time.Second, b)
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations}, time.Second)
	p.Close()
	if p.Err() == nil {
		t.Fatal("profile read failure was silent")
	}
	var status extractionCorpusCPUProfileStatus
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(d, "s")), &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != "incomplete" {
		t.Fatalf("status=%+v", status)
	}
}

func TestExtractionCorpusRelationsCPUProfileStatusWriteFailureIsNotPublished(t *testing.T) {
	d := t.TempDir()
	statusPath := filepath.Join(d, "status")
	p := newExtractionCorpusRelationsCPUProfilerForTest(filepath.Join(d, "profile"), statusPath, time.Second, 90*time.Second, &fakeCPUProfileBackend{})
	p.createStatusTemp = func(dir, pattern string) (*os.File, error) {
		f, err := os.CreateTemp(dir, pattern)
		if err != nil {
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
		return f, nil
	}
	p.writeStatus(extractionCorpusCPUProfileStatus{Status: "active"})
	if p.Err() == nil {
		t.Fatal("closed temporary file write failure was silent")
	}
	if _, err := os.Lstat(statusPath); !os.IsNotExist(err) {
		t.Fatalf("failed status was published: %v", err)
	}
}

func TestExtractionCorpusRelationsCPUProfileRealFlushIsGzip(t *testing.T) {
	requireRelationsCPUProfilePlatform(t)
	d := t.TempDir()
	p := newExtractionCorpusRelationsCPUProfilerForTest(filepath.Join(d, "p"), filepath.Join(d, "s"), 25*time.Millisecond, 90*time.Second, runtimeCPUProfileBackend{})
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations}, time.Second)
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		for i := 0; i < 10000; i++ {
			_ = i * i
		}
	}
	p.Close()
	f, err := os.Open(filepath.Join(d, "p"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(io.Discard, gz); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExtractionCorpusRelationsCPUProfileFlushSurvivesSubprocessKill(t *testing.T) {
	requireRelationsCPUProfilePlatform(t)
	if os.Getenv(extractionCorpusRelationsCPUProfileChildEnv) == "1" {
		d := os.Getenv(extractionCorpusRelationsCPUProfileChildEnv + "_DIR")
		p := newExtractionCorpusRelationsCPUProfilerForTest(filepath.Join(d, "p"), filepath.Join(d, "s"), 25*time.Millisecond, 90*time.Second, runtimeCPUProfileBackend{})
		p.Observe(ProgressEvent{Phase: BuildPhaseRelations}, time.Second)
		for {
			for i := 0; i < 10000; i++ {
				_ = i * i
			}
			if _, err := os.Stat(filepath.Join(d, "s")); err == nil {
				break
			}
		}
		time.Sleep(time.Hour)
		return
	}
	d := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestExtractionCorpusRelationsCPUProfileFlushSurvivesSubprocessKill$", "-test.count=1")
	cmd.Env = append(os.Environ(), extractionCorpusRelationsCPUProfileChildEnv+"=1", extractionCorpusRelationsCPUProfileChildEnv+"_DIR="+d)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, err := os.ReadFile(filepath.Join(d, "s")); err == nil {
			var status extractionCorpusCPUProfileStatus
			if json.Unmarshal(data, &status) == nil && status.Status == "complete" {
				break
			}
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("child did not flush profile")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("child unexpectedly exited cleanly")
	}
	data, err := os.ReadFile(filepath.Join(d, "p"))
	if err != nil {
		t.Fatal(err)
	}
	var status extractionCorpusCPUProfileStatus
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(d, "s")), &status); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if status.Status != "complete" || status.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("status=%+v", status)
	}
	gz, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, gz); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}
