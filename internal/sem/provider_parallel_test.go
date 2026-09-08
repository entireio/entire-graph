package sem

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func writeProviderParallelFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, repo, "a.go", `package sample

type Thing struct{ Value string }
func NewThing(value string) Thing { return Thing{Value: value} }
`)
	writeFile(t, repo, "b.go", `package sample

func UseThing() string { return NewThing("ok").Value }
`)
	writeFile(t, repo, "broken.go", "package sample\nfunc Broken( {\n")
	writeFile(t, repo, "odd dir/雪 test.py", "def unusual_name(value):\n    return value\n")
	writeFile(t, repo, "big.ts", "export function huge() { return 1 }\n"+string(bytes.Repeat([]byte("// generated payload\n"), 80)))
	writeFile(t, repo, "unsupported.f90", "subroutine unsupported\nend subroutine unsupported\n")
	return repo
}

func TestProviderWorkerCountCapsGOMAXPROCSAtEight(t *testing.T) {
	for _, test := range []struct {
		maxProcs int
		want     int
	}{{0, 1}, {1, 1}, {2, 2}, {8, 8}, {9, 8}, {64, 8}} {
		if got := boundedProviderWorkerCount(test.maxProcs); got != test.want {
			t.Fatalf("boundedProviderWorkerCount(%d) = %d, want %d", test.maxProcs, got, test.want)
		}
	}
}

func snapshotBytesWithWorkers(t *testing.T, repo string, workers int) ([]byte, SnapshotSummary, []FileRecord) {
	t.Helper()
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	var summary SnapshotSummary
	var files []FileRecord
	err := streamSnapshotWithWorkerCount(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree:      true,
		MaxParseBytes: 512,
	}, workers, func(record any) error {
		switch typed := record.(type) {
		case SnapshotSummary:
			summary = typed
		case FileRecord:
			files = append(files, typed)
		}
		return encoder.Encode(record)
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes(), summary, files
}

// Changing worker count must be operational only: graph bytes, record order,
// failure accounting, and completeness all remain the serial contract.
func TestStreamSnapshotParallelWorkersPreserveExactOutput(t *testing.T) {
	repo := writeProviderParallelFixture(t)
	serial, serialSummary, serialFiles := snapshotBytesWithWorkers(t, repo, 1)
	for _, workers := range []int{2, 8} {
		parallel, summary, files := snapshotBytesWithWorkers(t, repo, workers)
		if !bytes.Equal(parallel, serial) {
			t.Fatalf("%d-worker snapshot differs from serial output\nserial:\n%s\nparallel:\n%s", workers, serial, parallel)
		}
		if !reflect.DeepEqual(summary, serialSummary) {
			t.Fatalf("%d-worker summary changed:\nserial=%#v\nparallel=%#v", workers, serialSummary, summary)
		}
		if !reflect.DeepEqual(files, serialFiles) {
			t.Fatalf("%d-worker file emission order changed:\nserial=%#v\nparallel=%#v", workers, serialFiles, files)
		}
	}
}

func TestStreamSnapshotParallelPreservesFailuresCountsAndUnusualPaths(t *testing.T) {
	repo := writeProviderParallelFixture(t)
	_, summary, files := snapshotBytesWithWorkers(t, repo, 8)

	failureCodes := map[string]bool{}
	for _, failure := range summary.PartialFailures {
		failureCodes[failure.Code] = true
	}
	for _, want := range []string{"E_PARSE_ERROR", "E_FILE_TOO_LARGE", "E_UNSUPPORTED_LANGUAGE"} {
		if !failureCodes[want] {
			t.Fatalf("missing %s in parallel failures: %#v", want, summary.PartialFailures)
		}
	}
	foundUnusual := false
	for _, file := range files {
		if file.Path == "odd dir/雪 test.py" {
			foundUnusual = true
		}
	}
	if !foundUnusual {
		t.Fatalf("unusual path missing from ordered file records: %#v", files)
	}
	if summary.Stats.Files != len(files) {
		t.Fatalf("summary files = %d, emitted files = %d", summary.Stats.Files, len(files))
	}
	if summary.Stats.PartialFailures != len(summary.PartialFailures) {
		t.Fatalf("summary failure count = %d, failures = %d", summary.Stats.PartialFailures, len(summary.PartialFailures))
	}
	if summary.Completeness.Languages["Go"].Files == 0 || summary.Completeness.Languages["Python"].Files != 1 {
		t.Fatalf("language completeness changed: %#v", summary.Completeness.Languages)
	}
}

func TestStreamSnapshotParallelEmissionIsRepeatable(t *testing.T) {
	repo := writeProviderParallelFixture(t)
	want, _, _ := snapshotBytesWithWorkers(t, repo, 8)
	for repeat := 0; repeat < 5; repeat++ {
		got, _, _ := snapshotBytesWithWorkers(t, repo, 8)
		if !bytes.Equal(got, want) {
			t.Fatalf("repeat %d changed streamed bytes\nwant:\n%s\ngot:\n%s", repeat, want, got)
		}
	}
}

func TestStreamSnapshotCancellationNeverEmitsSummary(t *testing.T) {
	for _, workers := range []int{1, 8} {
		for _, at := range []string{"after-parse", "during-relations", "after-relations"} {
			t.Run(fmt.Sprintf("%d-workers/%s", workers, at), func(t *testing.T) {
				repo := t.TempDir()
				// All relations are local: no external-record loop may rescue
				// cancellation that the relation scanner observes internally.
				writeFile(t, repo, "local.go", "package sample\nfunc Local() {}\n")
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				canceled, summarized := false, false
				err := streamSnapshotWithWorkerCount(ctx, repo, "test-version", ProviderSnapshotOptions{
					Worktree: true,
					Profile:  ProfileFull,
					Progress: func(event ProgressEvent) {
						if (at == "after-parse" && event.Phase == BuildPhaseParse && event.FilesDone == event.FilesTotal) ||
							(at == "after-relations" && event.Phase == BuildPhaseRelations) {
							canceled = true
							cancel()
						}
					},
				}, workers, func(record any) error {
					switch record := record.(type) {
					case RelationRecord:
						if at == "during-relations" && record.Type == "DEFINES" {
							canceled = true
							cancel()
						}
					case SnapshotSummary:
						summarized = true
					}
					return nil
				})
				if !canceled {
					t.Fatal("test never reached the cancellation point")
				}
				if !errors.Is(err, context.Canceled) || summarized {
					t.Fatalf("error = %v, emitted summary = %v; want context.Canceled and no summary", err, summarized)
				}
			})
		}
	}
}

func repositoryFilePaths(t *testing.T, repo string) []string {
	t.Helper()
	var paths []string
	if err := filepath.WalkDir(repo, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	return paths
}

func TestStreamSnapshotEmitterFailureStopsPipelineWithoutArtifacts(t *testing.T) {
	repo := writeProviderParallelFixture(t)
	before := repositoryFilePaths(t, repo)
	wantErr := errors.New("stop emitter")
	emittedFiles := 0
	emittedSummary := false
	err := streamSnapshotWithWorkerCount(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree:      true,
		MaxParseBytes: 512,
	}, 8, func(record any) error {
		switch record.(type) {
		case FileRecord:
			emittedFiles++
			return wantErr
		case SnapshotSummary:
			emittedSummary = true
		}
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("StreamSnapshot error = %v, want emitter error", err)
	}
	if emittedFiles != 1 || emittedSummary {
		t.Fatalf("emission continued after error: files=%d summary=%v", emittedFiles, emittedSummary)
	}
	if after := repositoryFilePaths(t, repo); !reflect.DeepEqual(after, before) {
		t.Fatalf("emitter failure left artifacts:\nbefore=%v\nafter=%v", before, after)
	}
}

func TestProviderFilePipelineReducerFailureJoinsWorkers(t *testing.T) {
	paths := make([]string, 100)
	for i := range paths {
		paths[i] = "file"
	}
	var active atomic.Int32
	var processed atomic.Int32
	wantErr := errors.New("stop reducer")
	err := runProviderFilePipeline(t.Context(), nil, paths, 8,
		func(_ context.Context, index int, path string) providerFileResult {
			active.Add(1)
			defer active.Add(-1)
			processed.Add(1)
			time.Sleep(time.Millisecond)
			return providerFileResult{index: index, path: path}
		},
		func(providerFileResult) error { return wantErr },
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("pipeline error = %v, want reducer error", err)
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("pipeline returned with %d active workers", got)
	}
	if got := processed.Load(); got >= int32(len(paths)) {
		t.Fatalf("reducer failure did not stop work: processed %d of %d", got, len(paths))
	}
}

// A reducer that consumes completion order instead of path order would make
// streamed graph bytes depend on scheduler timing. Staggering the workers makes
// that mutation deterministic: later indexes finish first, but the reducer must
// still see the original sequence.
func TestProviderFilePipelineReducesInOriginalOrder(t *testing.T) {
	paths := []string{"a.go", "b.go", "c.go", "d.go"}
	var reduced []int
	err := runProviderFilePipeline(t.Context(), nil, paths, 4,
		func(_ context.Context, index int, path string) providerFileResult {
			time.Sleep(time.Duration(len(paths)-index) * time.Millisecond)
			return providerFileResult{index: index, path: path}
		},
		func(result providerFileResult) error {
			reduced = append(reduced, result.index)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{0, 1, 2, 3}; !reflect.DeepEqual(reduced, want) {
		t.Fatalf("reducer order = %v, want original path order %v", reduced, want)
	}
}

// A canceled caller must not wait for the remaining path list, and returning
// from the pipeline is also the join point: no worker may still be active.
func TestProviderFilePipelineCancellationJoinsWorkers(t *testing.T) {
	paths := make([]string, 100)
	for i := range paths {
		paths[i] = "file"
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var active atomic.Int32
	started := make(chan struct{})
	var startedOnce sync.Once
	done := make(chan error, 1)
	go func() {
		done <- runProviderFilePipeline(ctx, nil, paths, 4,
			func(_ context.Context, index int, path string) providerFileResult {
				active.Add(1)
				startedOnce.Do(func() { close(started) })
				time.Sleep(2 * time.Millisecond)
				active.Add(-1)
				return providerFileResult{index: index, path: path}
			},
			func(providerFileResult) error { return nil },
		)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("workers did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("pipeline error = %v, want context.Canceled", err)
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("pipeline returned with %d active workers", got)
	}
}

// When ctx expires with a completed result already buffered, the cancel branch
// must reduce that result before returning so budget-truncated snapshots do not
// drop the next in-order file scheduler-dependently.
func TestProviderFilePipelineCancellationEmitsBufferedResult(t *testing.T) {
	paths := []string{"first.go", "second.go"}
	ctx, cancel := context.WithCancel(context.Background())
	allowFirst := make(chan struct{})
	releaseSecond := make(chan struct{})
	go func() {
		time.Sleep(5 * time.Millisecond)
		close(allowFirst)
	}()

	var reduced []int
	done := make(chan error, 1)
	go func() {
		done <- runProviderFilePipeline(ctx, nil, paths, 2,
			func(workerCtx context.Context, index int, path string) providerFileResult {
				if index == 0 {
					<-allowFirst
				}
				if index == 1 {
					<-releaseSecond
				}
				return providerFileResult{index: index, path: path}
			},
			func(result providerFileResult) error {
				reduced = append(reduced, result.index)
				if result.index == 0 {
					close(releaseSecond)
					time.Sleep(20 * time.Millisecond)
					cancel()
				}
				return nil
			},
		)
	}()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("pipeline error = %v, want nil or context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline did not return after cancel")
	}
	if want := []int{0, 1}; !reflect.DeepEqual(reduced, want) {
		t.Fatalf("reducer order = %v, want buffered completion %v before cancel return", reduced, want)
	}
}

// If admission is tied only to worker/channel capacity, an early slow path can
// let every later file finish and accumulate in memory. Holding index zero
// proves the scheduler never starts more than 2*workers unreduced files.
func TestProviderFilePipelineBoundsOutstandingResults(t *testing.T) {
	const workers = 3
	paths := make([]string, 40)
	for i := range paths {
		paths[i] = string(rune('a' + i))
	}

	var started atomic.Int32
	releaseFirst := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runProviderFilePipeline(t.Context(), nil, paths, workers,
			func(ctx context.Context, index int, path string) providerFileResult {
				started.Add(1)
				if index == 0 {
					select {
					case <-releaseFirst:
					case <-ctx.Done():
					}
				}
				return providerFileResult{index: index, path: path}
			},
			func(providerFileResult) error { return nil },
		)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for started.Load() < 2*workers && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if started.Load() < 2*workers {
		close(releaseFirst)
		t.Fatalf("pipeline started only %d files before deadline", started.Load())
	}
	time.Sleep(40 * time.Millisecond)
	beforeRelease := started.Load()
	close(releaseFirst)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if beforeRelease > 2*workers {
		t.Fatalf("started %d unreduced files, want at most %d", beforeRelease, 2*workers)
	}
}

func BenchmarkStreamSnapshotWorkerCounts(b *testing.B) {
	repo := b.TempDir()
	for fileIndex := 0; fileIndex < 600; fileIndex++ {
		var source bytes.Buffer
		source.WriteString("package bench\n\n")
		for symbolIndex := 0; symbolIndex < 40; symbolIndex++ {
			fmt.Fprintf(&source, "func F%d_%d(value int) int { return value + %d }\n", fileIndex, symbolIndex, symbolIndex)
		}
		path := filepath.Join(repo, fmt.Sprintf("pkg/f%04d.go", fileIndex))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, source.Bytes(), 0o644); err != nil {
			b.Fatal(err)
		}
	}

	for _, workers := range []int{1, 2, 8} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			b.ReportAllocs()
			var summary SnapshotSummary
			for range b.N {
				if err := streamSnapshotWithWorkerCount(b.Context(), repo, "bench-version", ProviderSnapshotOptions{
					Worktree: true,
					Profile:  ProfileSyntaxOnly,
				}, workers, func(record any) error {
					if typed, ok := record.(SnapshotSummary); ok {
						summary = typed
					}
					return nil
				}); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(summary.Stats.Files), "files/op")
			b.ReportMetric(float64(summary.Stats.Symbols), "symbols/op")
			b.ReportMetric(float64(summary.Stats.Relations), "relations/op")
		})
	}
}
