package sem

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

// Run with -race and -count to sample more schedules. All fixtures share one
// repository so their files actually contend for the relation-phase indexes;
// snapshotting single-file fixture directories would not exercise a worker pool.
func TestStreamSnapshotParallelMultilanguageMatrix(t *testing.T) {
	repo := t.TempDir()
	for _, name := range goldenFixtures {
		copyFixtureTree(t, filepath.Join("testdata", "fixtures", name), filepath.Join(repo, name))
	}
	writeParallelSharedStateFixture(t, repo)
	writeFile(t, repo, "failures/broken.go", "package broken\nfunc Broken( {\n")
	writeFile(t, repo, "failures/large.ts", string(bytes.Repeat([]byte("// large source\n"), 1024)))
	writeFile(t, repo, "failures/unsupported.f90", "subroutine unsupported\nend subroutine unsupported\n")
	writeFile(t, repo, "odd dir/雪 test.py", "def unusual(value):\n    return value\n")
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "parallel parity corpus")
	// The worktree view must really differ from HEAD, rather than exercising
	// two readers against accidentally identical source trees.
	const dirtyPath = "untracked.py"
	writeFile(t, repo, dirtyPath, "def dirty_only(value):\n    return value\n")

	for _, worktree := range []bool{false, true} {
		for _, profile := range []Profile{ProfileSyntaxOnly, ProfileFast, ProfileFull} {
			t.Run(fmt.Sprintf("worktree=%t/%s", worktree, profile), func(t *testing.T) {
				options := ProviderSnapshotOptions{Worktree: worktree, Profile: profile, MaxParseBytes: 4096}
				serial := captureParallelMatrixSnapshot(t, repo, options, 1)
				if serial.files[dirtyPath] != worktree {
					t.Fatalf("untracked file present = %t, worktree = %t", serial.files[dirtyPath], worktree)
				}
				if len(serial.files) <= 16 || len(serial.summary.Languages) < 15 {
					t.Fatalf("insufficient corpus coverage: %d files, languages %v", len(serial.files), serial.summary.Languages)
				}
				failures := map[string]bool{}
				for _, failure := range serial.summary.PartialFailures {
					failures[failure.Code] = true
				}
				for _, code := range []string{"E_PARSE_ERROR", "E_FILE_TOO_LARGE", "E_UNSUPPORTED_LANGUAGE"} {
					if !failures[code] {
						t.Fatalf("fixture did not exercise %s", code)
					}
				}
				if profile == ProfileFull {
					for _, kind := range []string{"CALLS", "OVERRIDES", "DATA_FLOWS", "READS_FIELD", "WRITES_FIELD", "HANDLES_ROUTE", "HTTP_CALLS", "IMPORTS"} {
						if serial.summary.Completeness.Relations[kind] == 0 {
							t.Fatalf("fixture did not exercise %s", kind)
						}
					}
					if serial.externals == 0 {
						t.Fatal("fixture produced no external records")
					}
					for i := range 16 {
						if !hasRelationBySymbolNameAndFile(serial.graph, "CALLS", fmt.Sprintf("caller%02d", i), fmt.Sprintf("shared-ts/caller%02d.ts", i), "helper", "shared-ts/helper.ts") {
							t.Fatalf("TypeScript shared namespace caller %d did not resolve", i)
						}
						if !hasRelationBySymbolNameAndFile(serial.graph, "CALLS", fmt.Sprintf("caller%02d", i), fmt.Sprintf("parity-callers/caller%02d.py", i), "helper", "paritypkg/sub/worker.py") {
							t.Fatalf("Python shared submodule caller %d did not resolve", i)
						}
						if !hasRelationToExternalRoute(serial.graph.Relations, "HANDLES_ROUTE", fmt.Sprintf("handler%02d", i), fmt.Sprintf("/parity/%02d", i)) {
							t.Fatalf("Go shared constant route %d did not resolve", i)
						}
					}
				}
				t.Logf("files=%d languages=%d symbols=%d relations=%d externals=%d native_sha256=%x compact_sha256=%x",
					len(serial.files), len(serial.summary.Languages), serial.summary.Stats.Symbols,
					serial.summary.Stats.Relations, serial.externals, sha256.Sum256(serial.native), sha256.Sum256(serial.compact))
				for _, workers := range []int{2, 8} {
					for repeat := range 3 {
						parallel := captureParallelMatrixSnapshot(t, repo, options, workers)
						// Do not normalize, sort or omit headers, evidence, failures,
						// external records or summaries: these are the wire bytes.
						if !bytes.Equal(parallel.native, serial.native) {
							t.Fatalf("native NDJSON differs: workers=%d repeat=%d; %s", workers, repeat, firstParallelMatrixDifference(serial.native, parallel.native))
						}
						if !bytes.Equal(parallel.compact, serial.compact) {
							t.Fatalf("compact NDJSON differs: workers=%d repeat=%d; %s", workers, repeat, firstParallelMatrixDifference(serial.compact, parallel.compact))
						}
					}
				}
			})
		}
	}
}

type parallelMatrixSnapshot struct {
	native, compact []byte
	files           map[string]bool
	summary         SnapshotSummary
	externals       int
	graph           ProviderSnapshot
}

func captureParallelMatrixSnapshot(t *testing.T, repo string, options ProviderSnapshotOptions, workers int) parallelMatrixSnapshot {
	t.Helper()
	var native, compact bytes.Buffer
	encoder := json.NewEncoder(&native)
	encoder.SetEscapeHTML(false)
	compactEncoder := NewCompactSnapshotEncoder(&compact)
	result := parallelMatrixSnapshot{files: map[string]bool{}}
	err := streamSnapshotWithWorkerCount(t.Context(), repo, "parallel-matrix-test", options, workers, func(record any) error {
		switch record := record.(type) {
		case FileRecord:
			result.files[record.Path] = true
		case SnapshotSummary:
			result.summary = record
		case ExternalRecord:
			result.externals++
		case SymbolRecord:
			result.graph.Symbols = append(result.graph.Symbols, record)
		case RelationRecord:
			result.graph.Relations = append(result.graph.Relations, record)
		}
		if err := encoder.Encode(record); err != nil {
			return err
		}
		return compactEncoder.Encode(record)
	})
	if err != nil {
		t.Fatal(err)
	}
	result.native, result.compact = native.Bytes(), compact.Bytes()
	return result
}

func firstParallelMatrixDifference(want, got []byte) string {
	a, b := bytes.Split(want, []byte{'\n'}), bytes.Split(got, []byte{'\n'})
	for i := 0; i < len(a) && i < len(b); i++ {
		if !bytes.Equal(a[i], b[i]) {
			return fmt.Sprintf("record %d: want %.512s; got %.512s", i+1, a[i], b[i])
		}
	}
	return fmt.Sprintf("record counts differ: want %d, got %d", len(a), len(b))
}

func writeParallelSharedStateFixture(t *testing.T, repo string) {
	t.Helper()
	writeFile(t, repo, "shared-ts/helper.ts", "export namespace Shared { export function helper() {} }\n")
	writeFile(t, repo, "paritypkg/__init__.py", "")
	writeFile(t, repo, "paritypkg/sub/__init__.py", "from . import worker\n")
	writeFile(t, repo, "paritypkg/sub/worker.py", "def helper(value):\n    return value\n")
	for i := range 16 {
		// Merged class/namespace fallback asks the shared namespace memo about
		// helper.ts from many independent workers.
		writeFile(t, repo, fmt.Sprintf("shared-ts/caller%02d.ts", i), fmt.Sprintf(`export class Shared { static create() {} }
export namespace Shared { export const marker = 1; }
export function caller%02d() { Shared.helper(); }
`, i))
		writeFile(t, repo, fmt.Sprintf("parity-callers/caller%02d.py", i), fmt.Sprintf("from paritypkg import sub\ndef caller%02d(value):\n    return sub.worker.helper(value)\n", i))
		// Both the Go route pass and per-file resolution derive constants from
		// these files; their results also feed route bridge reduction.
		writeFile(t, repo, fmt.Sprintf("shared-go/routes%02d.go", i), fmt.Sprintf(`package parity
import "net/http"
const route%02[1]d = "/parity/%02[1]d"
func handler%02[1]d(w http.ResponseWriter, r *http.Request) {}
func register%02[1]d() { http.HandleFunc(route%02[1]d, handler%02[1]d) }
func request%02[1]d() { http.Get(route%02[1]d) }
`, i))
	}
}
