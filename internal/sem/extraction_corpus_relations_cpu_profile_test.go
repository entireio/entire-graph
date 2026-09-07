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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const extractionCorpusRelationsCPUProfileChildEnv = "ENTIRE_GRAPH_EXTRACTION_CORPUS_RELATIONS_CPU_PROFILE_TEST_CHILD"

const (
	extractionCorpusRelationsCPUProfileName        = "relations-cpu.pprof"
	extractionCorpusRelationsCPUProfileStatusName  = "relations-cpu.pprof.json"
	extractionCorpusRelationsCPUProfileMaxBytes    = 8 << 20
	extractionCorpusRelationsCPUProfileWindow      = 20 * time.Second
	extractionCorpusRelationsCPUProfileLatestStart = 90 * time.Second
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
	RequestedStartAfterNS  int64  `json:"requested_start_after_ns"`
	ActualStartElapsedNS   *int64 `json:"actual_start_elapsed_ns,omitempty"`
	WindowNS               int64  `json:"window_ns"`
	ActualNS               int64  `json:"actual_ns"`
	Bytes                  int64  `json:"bytes,omitempty"`
	SHA256                 string `json:"sha256,omitempty"`
	Reason                 string `json:"reason,omitempty"`
	Error                  string `json:"error,omitempty"`
	RelationsFirst         int    `json:"relations_first"`
	RelationsLatest        int    `json:"relations_latest"`
	RelationProgressEvents int    `json:"relation_progress_events"`
	DiagnosticOnly         bool   `json:"diagnostic_only"`
	NoPerformanceClaim     bool   `json:"no_performance_claim"`
	ProfileOverheadPresent bool   `json:"profile_overhead_present"`
}

type extractionCorpusCPUProfileWait func(time.Duration, <-chan struct{}) bool

type extractionCorpusRelationsCPUProfiler struct {
	mu                      sync.Mutex
	backend                 extractionCorpusCPUProfileBackend
	profilePath, statusPath string
	window, latestStart     time.Duration
	startAfter              time.Duration
	now                     func() time.Time
	wait                    extractionCorpusCPUProfileWait
	started                 time.Time
	triggerElapsed          time.Duration
	writer                  *boundedCPUProfileWriter
	cancel                  chan struct{}
	done                    chan struct{}
	startupDone             chan struct{}
	cancelOnce              sync.Once
	startupOnce             sync.Once
	claimed                 bool
	closed                  bool
	relationsFirst          int
	relationsLatest         int
	relationProgressEvents  int
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

func extractionCorpusRelationsCPUProfileStartAfter() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(extractionCorpusRelationsCPUProfileStartAfterEnv))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("relations CPU profile start-after must be a non-negative decimal integer in nanoseconds")
	}
	startAfter := time.Duration(value)
	if startAfter > extractionCorpusRelationsCPUProfileLatestStart {
		return 0, errors.New("relations CPU profile start-after exceeds the 90-second latest start")
	}
	return startAfter, nil
}

func newExtractionCorpusRelationsCPUProfiler(enabled bool, config extractionCorpusEvaluationConfig) (*extractionCorpusRelationsCPUProfiler, error) {
	if !enabled {
		return nil, nil
	}
	startAfter, err := extractionCorpusRelationsCPUProfileStartAfter()
	if err != nil {
		return nil, err
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
	profiler := newExtractionCorpusRelationsCPUProfilerForTest(
		p, s, extractionCorpusRelationsCPUProfileWindow,
		extractionCorpusRelationsCPUProfileLatestStart, runtimeCPUProfileBackend{},
	)
	profiler.startAfter = startAfter
	return profiler, nil
}

func newExtractionCorpusRelationsCPUProfilerForTest(profilePath, statusPath string, window, latest time.Duration, backend extractionCorpusCPUProfileBackend) *extractionCorpusRelationsCPUProfiler {
	return &extractionCorpusRelationsCPUProfiler{
		backend: backend, profilePath: profilePath, statusPath: statusPath,
		window: window, latestStart: latest, now: time.Now,
		wait: extractionCorpusCPUProfileWaitDuration,
	}
}

func (p *extractionCorpusRelationsCPUProfiler) Observe(event ProgressEvent, elapsed time.Duration) {
	if p == nil || event.Phase != BuildPhaseRelations {
		return
	}
	p.mu.Lock()
	if p.claimed {
		if !p.closed {
			p.relationsLatest = event.Relations
			p.relationProgressEvents++
		}
		p.mu.Unlock()
		return
	}
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.relationsFirst = event.Relations
	p.relationsLatest = event.Relations
	p.relationProgressEvents = 1
	if elapsed > p.latestStart {
		p.closed = true
		p.mu.Unlock()
		p.writeStatus(p.status("missed_safe_window", elapsed, nil,
			"relations phase was first observed after the latest safe profile start"))
		return
	}
	p.claimed = true
	delay := p.startAfter - elapsed
	if delay < 0 {
		delay = 0
	}
	p.cancel = make(chan struct{})
	p.done = make(chan struct{})
	p.startupDone = make(chan struct{})
	observedAt := p.now()
	cancel, startupDone := p.cancel, p.startupDone
	p.mu.Unlock()

	if delay > 0 {
		p.writeStatus(p.status("scheduled", 0, nil, ""))
		if p.Err() != nil {
			p.closeWithoutLaunch()
			return
		}
		delay = p.startAfter - (elapsed + p.now().Sub(observedAt))
		if delay < 0 {
			delay = 0
		}
	}
	go p.runProfileLifecycle(elapsed, observedAt, delay, cancel)
	if delay == 0 {
		<-startupDone
	}
}

func extractionCorpusCPUProfileWaitDuration(wait time.Duration, cancel <-chan struct{}) bool {
	if wait <= 0 {
		return true
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-cancel:
		return false
	}
}

func (p *extractionCorpusRelationsCPUProfiler) runProfileLifecycle(observedElapsed time.Duration, observedAt time.Time, delay time.Duration, cancel <-chan struct{}) {
	defer close(p.done)
	if (delay > 0 && !p.wait(delay, cancel)) || extractionCorpusCPUProfileCancelled(cancel) {
		p.writeStatus(p.status("cancelled", 0, nil, "request ended before the scheduled profile start"))
		p.markClosed()
		p.signalStartup()
		return
	}
	actualStart := observedElapsed
	if delay > 0 {
		actualStart += p.now().Sub(observedAt)
	}
	if actualStart > p.latestStart {
		p.writeStatus(p.status("missed_safe_window", actualStart, nil,
			"scheduled profile start woke after the latest safe profile start"))
		p.markClosed()
		p.signalStartup()
		return
	}
	p.writeStatus(p.status("starting", actualStart, nil, ""))
	if p.Err() != nil {
		p.markClosed()
		p.signalStartup()
		return
	}
	if extractionCorpusCPUProfileCancelled(cancel) {
		p.writeStatus(p.status("cancelled", 0, nil, "request ended before the scheduled profile start"))
		p.markClosed()
		p.signalStartup()
		return
	}
	actualStart = observedElapsed + p.now().Sub(observedAt)
	if actualStart > p.latestStart {
		p.writeStatus(p.status("missed_safe_window", actualStart, nil,
			"profile setup reached the backend after the latest safe profile start"))
		p.markClosed()
		p.signalStartup()
		return
	}
	file, err := os.OpenFile(p.profilePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		p.setErr(err)
		p.writeStatus(p.status("error", actualStart, nil, "", err))
		p.markClosed()
		p.signalStartup()
		return
	}
	writer := &boundedCPUProfileWriter{file: file}
	if extractionCorpusCPUProfileCancelled(cancel) {
		closeErr := file.Close()
		removeErr := os.Remove(p.profilePath)
		if closeErr != nil || removeErr != nil {
			err := errors.Join(closeErr, removeErr)
			p.setErr(err)
			p.writeStatus(p.status("error", actualStart, nil, "", err))
		} else {
			p.writeStatus(p.status("cancelled", 0, nil, "request ended before the scheduled profile start"))
		}
		p.markClosed()
		p.signalStartup()
		return
	}
	if err := p.backend.Start(writer); err != nil {
		_ = file.Close()
		p.setErr(err)
		p.writeStatus(p.status("error", actualStart, nil, "", err))
		p.markClosed()
		p.signalStartup()
		return
	}
	started := p.now()
	actualStart = observedElapsed + started.Sub(observedAt)
	actualStartNS := actualStart.Nanoseconds()
	p.mu.Lock()
	p.started = started
	p.triggerElapsed = actualStart
	p.writer = writer
	p.mu.Unlock()
	if actualStart > p.latestStart {
		p.finishOwned("missed_safe_window", "profile backend started after the latest safe profile start")
		p.signalStartup()
		return
	}
	// This durable marker makes a hard kill during the sampling window
	// explicit: only finish replaces it with complete or incomplete.
	p.writeStatus(p.status("active", actualStart, &actualStartNS, ""))
	p.signalStartup()
	if p.Err() == nil {
		p.wait(p.window, cancel)
	}
	p.finishOwned("complete", "")
}

func extractionCorpusCPUProfileCancelled(cancel <-chan struct{}) bool {
	select {
	case <-cancel:
		return true
	default:
		return false
	}
}

func (p *extractionCorpusRelationsCPUProfiler) finishOwned(state, reason string) {
	p.mu.Lock()
	backend, writer, started, trigger := p.backend, p.writer, p.started, p.triggerElapsed
	p.mu.Unlock()
	backend.Stop()
	priorErr := p.Err()
	err := writer.err
	if syncErr := writer.file.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := writer.file.Close(); err == nil {
		err = closeErr
	}
	actualStartNS := trigger.Nanoseconds()
	status := p.status(state, trigger, &actualStartNS, reason)
	status.ActualNS = p.now().Sub(started).Nanoseconds()
	status.Bytes = writer.written
	if combinedErr := errors.Join(priorErr, err); combinedErr != nil {
		status.Status = "incomplete"
		status.Error = boundedExtractionCorpusCPUProfileError(combinedErr)
		if err != nil {
			p.setErr(err)
		}
	} else if data, readErr := os.ReadFile(p.profilePath); readErr != nil {
		status.Status = "incomplete"
		status.Error = boundedExtractionCorpusCPUProfileError(readErr)
		p.setErr(readErr)
	} else {
		sum := sha256.Sum256(data)
		status.SHA256 = hex.EncodeToString(sum[:])
	}
	p.writeStatus(status)
	p.markClosed()
}

func (p *extractionCorpusRelationsCPUProfiler) status(state string, trigger time.Duration, actualStartNS *int64, reason string, statusErr ...error) extractionCorpusCPUProfileStatus {
	p.mu.Lock()
	relationsFirst, relationsLatest, relationProgressEvents := p.relationsFirst, p.relationsLatest, p.relationProgressEvents
	p.mu.Unlock()
	status := extractionCorpusCPUProfileStatus{
		Status: state, TriggerElapsedNS: trigger.Nanoseconds(),
		RequestedStartAfterNS: p.startAfter.Nanoseconds(), ActualStartElapsedNS: actualStartNS,
		Reason: reason, RelationsFirst: relationsFirst, RelationsLatest: relationsLatest,
		RelationProgressEvents: relationProgressEvents,
	}
	if len(statusErr) > 0 {
		status.Error = boundedExtractionCorpusCPUProfileError(statusErr[0])
	}
	return status

}

func boundedExtractionCorpusCPUProfileError(err error) string {
	if err == nil {
		return ""
	}
	const maxBytes = 512
	text := err.Error()
	if len(text) > maxBytes {
		return text[:maxBytes]
	}
	return text

}

func (p *extractionCorpusRelationsCPUProfiler) signalStartup() {
	p.startupOnce.Do(func() { close(p.startupDone) })
}

func (p *extractionCorpusRelationsCPUProfiler) markClosed() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
}

func (p *extractionCorpusRelationsCPUProfiler) closeWithoutLaunch() {
	p.markClosed()
	p.signalStartup()
	close(p.done)
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
	if !p.claimed {
		p.closed = true
		p.mu.Unlock()
		return
	}
	cancel, done := p.cancel, p.done
	p.cancelOnce.Do(func() { close(cancel) })
	p.mu.Unlock()
	<-done
}

type fakeCPUProfileBackend struct {
	mu            sync.Mutex
	starts, stops int
	writer        io.Writer
	startErr      error
	removeOnStop  bool
	startEntered  chan struct{}
	startRelease  chan struct{}
	startOnce     sync.Once
	onStart       func()
}

func (f *fakeCPUProfileBackend) Start(w io.Writer) error {
	if f.startEntered != nil {
		f.startOnce.Do(func() { close(f.startEntered) })
	}
	if f.startRelease != nil {
		<-f.startRelease
	}
	if f.onStart != nil {
		f.onStart()
	}
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

func (f *fakeCPUProfileBackend) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops
}

type fakeCPUProfileClock struct {
	mu  sync.Mutex
	now time.Time
}

func (f *fakeCPUProfileClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeCPUProfileClock) Advance(elapsed time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(elapsed)
	f.mu.Unlock()
}

type fakeCPUProfileWaiter struct {
	requests chan time.Duration
	release  chan struct{}
}

func newFakeCPUProfileWaiter() *fakeCPUProfileWaiter {
	return &fakeCPUProfileWaiter{requests: make(chan time.Duration, 4), release: make(chan struct{}, 4)}
}

func (f *fakeCPUProfileWaiter) Wait(wait time.Duration, cancel <-chan struct{}) bool {
	select {
	case f.requests <- wait:
	case <-cancel:
		return false
	}
	select {
	case <-f.release:
		return true
	case <-cancel:
		return false
	}
}

func (f *fakeCPUProfileWaiter) next(t *testing.T) time.Duration {
	t.Helper()
	select {
	case wait := <-f.requests:
		return wait
	case <-time.After(time.Second):
		t.Fatal("profile lifecycle did not reach injected wait")
		return 0
	}
}

func waitCPUProfileDone(t *testing.T, p *extractionCorpusRelationsCPUProfiler) {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(time.Second):
		t.Fatal("profile lifecycle did not finish")
	}
}

func readCPUProfileStatus(t *testing.T, path string) extractionCorpusCPUProfileStatus {
	t.Helper()
	var status extractionCorpusCPUProfileStatus
	if err := json.Unmarshal(mustReadFile(t, path), &status); err != nil {
		t.Fatal(err)
	}
	return status
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
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations, Relations: 512}, 2*time.Second)
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations, Relations: 1024}, 3*time.Second)
	p.Close()
	if b.starts != 1 || b.stops != 1 {
		t.Fatalf("start/stop=%d/%d", b.starts, b.stops)
	}
	var s extractionCorpusCPUProfileStatus
	data, _ := os.ReadFile(filepath.Join(d, "s"))
	if err := json.Unmarshal(data, &s); err != nil || s.Status != "complete" || s.Bytes == 0 || s.SHA256 == "" {
		t.Fatalf("status=%+v err=%v", s, err)
	}
	if s.RequestedStartAfterNS != 0 || s.ActualStartElapsedNS == nil || *s.ActualStartElapsedNS < int64(2*time.Second) || *s.ActualStartElapsedNS > int64(3*time.Second) {
		t.Fatalf("immediate start metadata=%+v", s)
	}
	if s.RelationsFirst != 512 || s.RelationsLatest != 1024 || s.RelationProgressEvents != 2 {
		t.Fatalf("immediate progress metadata=%+v", s)
	}
}

func TestExtractionCorpusRelationsCPUProfileStartAfterValidation(t *testing.T) {
	valid := map[string]time.Duration{
		"":  0,
		"0": 0,
		strconv.FormatInt(int64(88*time.Second), 10):                                 88 * time.Second,
		strconv.FormatInt(int64(extractionCorpusRelationsCPUProfileLatestStart), 10): extractionCorpusRelationsCPUProfileLatestStart,
	}
	for value, want := range valid {
		t.Run("valid-"+value, func(t *testing.T) {
			t.Setenv(extractionCorpusRelationsCPUProfileStartAfterEnv, value)
			got, err := extractionCorpusRelationsCPUProfileStartAfter()
			if err != nil || got != want {
				t.Fatalf("start-after=%v err=%v, want %v", got, err, want)
			}
		})
	}
	for _, value := range []string{"-1", "1.5", "not-a-number", strconv.FormatInt(int64(extractionCorpusRelationsCPUProfileLatestStart)+1, 10)} {
		t.Run("invalid", func(t *testing.T) {
			t.Setenv(extractionCorpusRelationsCPUProfileStartAfterEnv, value)
			if _, err := extractionCorpusRelationsCPUProfileStartAfter(); err == nil || strings.Contains(err.Error(), value) {
				t.Fatalf("error=%q", err)
			}
		})
	}
}

func TestExtractionCorpusRelationsCPUProfileDelayedLifecycle(t *testing.T) {
	requireRelationsCPUProfilePlatform(t)
	d := t.TempDir()
	clock := &fakeCPUProfileClock{now: time.Unix(100, 0)}
	waits := newFakeCPUProfileWaiter()
	backend := &fakeCPUProfileBackend{startEntered: make(chan struct{}), startRelease: make(chan struct{})}
	p := newExtractionCorpusRelationsCPUProfilerForTest(filepath.Join(d, "p"), filepath.Join(d, "s"), 20*time.Second, 90*time.Second, backend)
	p.startAfter, p.now, p.wait = 10*time.Second, clock.Now, waits.Wait
	var delayStatusOnce sync.Once
	p.createStatusTemp = func(dir, pattern string) (*os.File, error) {
		delayStatusOnce.Do(func() { clock.Advance(3 * time.Second) })
		return os.CreateTemp(dir, pattern)
	}

	p.Observe(ProgressEvent{Phase: BuildPhaseRelations, Relations: 512}, 2*time.Second)
	if got := waits.next(t); got != 5*time.Second {
		t.Fatalf("scheduled wait=%v", got)
	}
	status := readCPUProfileStatus(t, filepath.Join(d, "s"))
	if status.Status != "scheduled" || status.RequestedStartAfterNS != int64(10*time.Second) || status.ActualStartElapsedNS != nil {
		t.Fatalf("scheduled status=%+v", status)
	}

	clock.Advance(5 * time.Second)
	waits.release <- struct{}{}
	select {
	case <-backend.startEntered:
	case <-time.After(time.Second):
		t.Fatal("backend start was not reached")
	}
	status = readCPUProfileStatus(t, filepath.Join(d, "s"))
	if status.Status != "starting" || status.RequestedStartAfterNS != int64(10*time.Second) || status.ActualStartElapsedNS != nil {
		t.Fatalf("starting status=%+v", status)
	}
	close(backend.startRelease)
	if got := waits.next(t); got != 20*time.Second {
		t.Fatalf("profile window=%v", got)
	}
	status = readCPUProfileStatus(t, filepath.Join(d, "s"))
	if status.Status != "active" || status.ActualStartElapsedNS == nil || *status.ActualStartElapsedNS != int64(10*time.Second) {
		t.Fatalf("active status=%+v", status)
	}
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations, Relations: 1024}, 15*time.Second)

	clock.Advance(20 * time.Second)
	waits.release <- struct{}{}
	waitCPUProfileDone(t, p)
	status = readCPUProfileStatus(t, filepath.Join(d, "s"))
	if status.Status != "complete" || status.ActualNS != int64(20*time.Second) || status.WindowNS != int64(20*time.Second) || status.RelationsFirst != 512 || status.RelationsLatest != 1024 || status.RelationProgressEvents != 2 {
		t.Fatalf("complete status=%+v", status)
	}
	if starts, stops := backend.counts(); starts != 1 || stops != 1 {
		t.Fatalf("start/stop=%d/%d", starts, stops)
	}
	p.Close()
}

func TestExtractionCorpusRelationsCPUProfileDelayedObserveClaimsOnceAndCancels(t *testing.T) {
	requireRelationsCPUProfilePlatform(t)
	d := t.TempDir()
	waits := newFakeCPUProfileWaiter()
	backend := &fakeCPUProfileBackend{}
	p := newExtractionCorpusRelationsCPUProfilerForTest(filepath.Join(d, "p"), filepath.Join(d, "s"), 20*time.Second, 90*time.Second, backend)
	p.startAfter, p.wait = 10*time.Second, waits.Wait
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations, Relations: 512}, 2*time.Second)
	if got := waits.next(t); got <= 0 || got > 8*time.Second {
		t.Fatalf("scheduled wait=%v", got)
	}
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations, Relations: 1024}, 3*time.Second)
	select {
	case wait := <-waits.requests:
		t.Fatalf("duplicate Observe scheduled wait %v", wait)
	default:
	}
	p.Close()
	status := readCPUProfileStatus(t, filepath.Join(d, "s"))
	if status.Status != "cancelled" || status.Reason != "request ended before the scheduled profile start" || status.ActualStartElapsedNS != nil || status.RelationsFirst != 512 || status.RelationsLatest != 1024 || status.RelationProgressEvents != 2 {
		t.Fatalf("cancelled status=%+v", status)
	}
	if starts, stops := backend.counts(); starts != 0 || stops != 0 {
		t.Fatalf("start/stop=%d/%d", starts, stops)
	}
}

func TestExtractionCorpusRelationsCPUProfileCancelWinsReadyTimer(t *testing.T) {
	requireRelationsCPUProfilePlatform(t)
	d := t.TempDir()
	entered, release, closeDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	backend := &fakeCPUProfileBackend{}
	p := newExtractionCorpusRelationsCPUProfilerForTest(filepath.Join(d, "p"), filepath.Join(d, "s"), 20*time.Second, 90*time.Second, backend)
	p.startAfter = 10 * time.Second
	p.wait = func(time.Duration, <-chan struct{}) bool {
		close(entered)
		<-release
		return true
	}
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations}, 2*time.Second)
	<-entered
	go func() {
		p.Close()
		close(closeDone)
	}()
	<-p.cancel
	close(release)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not join cancelled scheduler")
	}
	if starts, _ := backend.counts(); starts != 0 {
		t.Fatalf("cancelled ready timer started backend %d times", starts)
	}
}

func TestExtractionCorpusRelationsCPUProfileDelayedWakePastLatestStart(t *testing.T) {
	requireRelationsCPUProfilePlatform(t)
	d := t.TempDir()
	clock := &fakeCPUProfileClock{now: time.Unix(100, 0)}
	waits := newFakeCPUProfileWaiter()
	backend := &fakeCPUProfileBackend{}
	p := newExtractionCorpusRelationsCPUProfilerForTest(filepath.Join(d, "p"), filepath.Join(d, "s"), 20*time.Second, 90*time.Second, backend)
	p.startAfter, p.now, p.wait = 80*time.Second, clock.Now, waits.Wait
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations}, 70*time.Second)
	if got := waits.next(t); got != 10*time.Second {
		t.Fatalf("scheduled wait=%v", got)
	}
	clock.Advance(20*time.Second + time.Nanosecond)
	waits.release <- struct{}{}
	waitCPUProfileDone(t, p)
	status := readCPUProfileStatus(t, filepath.Join(d, "s"))
	if status.Status != "missed_safe_window" || status.Reason != "scheduled profile start woke after the latest safe profile start" || status.TriggerElapsedNS != int64(90*time.Second+time.Nanosecond) {
		t.Fatalf("late status=%+v", status)
	}
	if starts, _ := backend.counts(); starts != 0 {
		t.Fatalf("late scheduler started backend %d times", starts)
	}
	p.Close()
}

func TestExtractionCorpusRelationsCPUProfileSetupCannotClaimLateStart(t *testing.T) {
	requireRelationsCPUProfilePlatform(t)
	d := t.TempDir()
	clock := &fakeCPUProfileClock{now: time.Unix(100, 0)}
	waits := newFakeCPUProfileWaiter()
	backend := &fakeCPUProfileBackend{onStart: func() { clock.Advance(11 * time.Second) }}
	p := newExtractionCorpusRelationsCPUProfilerForTest(filepath.Join(d, "p"), filepath.Join(d, "s"), 20*time.Second, 90*time.Second, backend)
	p.startAfter, p.now, p.wait = 80*time.Second, clock.Now, waits.Wait
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations}, 70*time.Second)
	_ = waits.next(t)
	clock.Advance(10 * time.Second)
	waits.release <- struct{}{}
	waitCPUProfileDone(t, p)
	status := readCPUProfileStatus(t, filepath.Join(d, "s"))
	if status.Status != "missed_safe_window" || status.Reason != "profile backend started after the latest safe profile start" || status.ActualStartElapsedNS == nil || *status.ActualStartElapsedNS != int64(91*time.Second) {
		t.Fatalf("late backend status=%+v", status)
	}
	if starts, stops := backend.counts(); starts != 1 || stops != 1 {
		t.Fatalf("start/stop=%d/%d", starts, stops)
	}
	p.Close()
}

func TestExtractionCorpusRelationsCPUProfileScheduledStatusFailurePreventsStart(t *testing.T) {
	d := t.TempDir()
	waits := newFakeCPUProfileWaiter()
	backend := &fakeCPUProfileBackend{}
	p := newExtractionCorpusRelationsCPUProfilerForTest(filepath.Join(d, "p"), filepath.Join(d, "s"), 20*time.Second, 90*time.Second, backend)
	p.startAfter, p.wait = 10*time.Second, waits.Wait
	p.createStatusTemp = func(string, string) (*os.File, error) { return nil, errors.New("status unavailable") }
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations}, 2*time.Second)
	p.Close()
	if p.Err() == nil {
		t.Fatal("scheduled status failure was not retained")
	}
	select {
	case wait := <-waits.requests:
		t.Fatalf("status failure launched scheduler with wait %v", wait)
	default:
	}
	if starts, _ := backend.counts(); starts != 0 {
		t.Fatalf("status failure started backend %d times", starts)
	}
}

func TestExtractionCorpusRelationsCPUProfileCloseJoinsBlockedStart(t *testing.T) {
	requireRelationsCPUProfilePlatform(t)
	d := t.TempDir()
	clock := &fakeCPUProfileClock{now: time.Unix(100, 0)}
	waits := newFakeCPUProfileWaiter()
	backend := &fakeCPUProfileBackend{startEntered: make(chan struct{}), startRelease: make(chan struct{})}
	p := newExtractionCorpusRelationsCPUProfilerForTest(filepath.Join(d, "p"), filepath.Join(d, "s"), 20*time.Second, 90*time.Second, backend)
	p.startAfter, p.now, p.wait = 10*time.Second, clock.Now, waits.Wait
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations}, 2*time.Second)
	_ = waits.next(t)
	clock.Advance(8 * time.Second)
	waits.release <- struct{}{}
	<-backend.startEntered
	closeDone := make(chan struct{})
	go func() {
		p.Close()
		close(closeDone)
	}()
	<-p.cancel
	select {
	case <-closeDone:
		t.Fatal("Close returned while backend Start still owned the profiler")
	default:
	}
	close(backend.startRelease)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not join backend Start and Stop")
	}
	if starts, stops := backend.counts(); starts != 1 || stops != 1 {
		t.Fatalf("start/stop=%d/%d", starts, stops)
	}
	status := readCPUProfileStatus(t, filepath.Join(d, "s"))
	if status.Status != "complete" || status.ActualNS >= status.WindowNS {
		t.Fatalf("shortened complete status=%+v", status)
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
	p.now = (&fakeCPUProfileClock{now: time.Unix(100, 0)}).Now
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations}, 90*time.Second)
	p.Close()
	if b.starts != 1 || b.stops != 1 {
		t.Fatalf("start/stop=%d/%d", b.starts, b.stops)
	}
}

func TestExtractionCorpusRelationsCPUProfileStartFailureDoesNotStop(t *testing.T) {
	requireRelationsCPUProfilePlatform(t)
	d := t.TempDir()
	b := &fakeCPUProfileBackend{startErr: errors.New(strings.Repeat("x", 600))}
	p := newExtractionCorpusRelationsCPUProfilerForTest(filepath.Join(d, "p"), filepath.Join(d, "s"), time.Second, 90*time.Second, b)
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations}, time.Second)
	p.Close()
	if b.stops != 0 {
		t.Fatal("stopped profiler whose start failed")
	}
	if status := readCPUProfileStatus(t, filepath.Join(d, "s")); status.Status != "error" || len(status.Error) != 512 {
		t.Fatalf("bounded start error status=%+v", status)
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

func TestExtractionCorpusRelationsCPUProfileTransientActiveStatusFailureCannotPublishComplete(t *testing.T) {
	requireRelationsCPUProfilePlatform(t)
	d := t.TempDir()
	backend := &fakeCPUProfileBackend{}
	p := newExtractionCorpusRelationsCPUProfilerForTest(filepath.Join(d, "profile"), filepath.Join(d, "status"), 20*time.Second, 90*time.Second, backend)
	statusWrites := 0
	p.createStatusTemp = func(dir, pattern string) (*os.File, error) {
		statusWrites++
		if statusWrites == 2 {
			return nil, errors.New("transient active status failure")
		}
		return os.CreateTemp(dir, pattern)
	}
	p.Observe(ProgressEvent{Phase: BuildPhaseRelations}, time.Second)
	p.Close()
	status := readCPUProfileStatus(t, filepath.Join(d, "status"))
	if status.Status != "incomplete" || !strings.Contains(status.Error, "transient active status failure") || status.ActualNS >= status.WindowNS {
		t.Fatalf("final status=%+v", status)
	}
	if starts, stops := backend.counts(); starts != 1 || stops != 1 {
		t.Fatalf("start/stop=%d/%d", starts, stops)
	}
	if p.Err() == nil {
		t.Fatal("transient active status failure was not retained")
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
