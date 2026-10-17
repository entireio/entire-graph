package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestRecordCacheKeepsShallowHistorySeparate(t *testing.T) {
	source := t.TempDir()
	initDoctorRepo(t, source, "https://github.com/example/history-cache.git")
	for i := range 3 {
		for _, name := range []string{"a", "b"} {
			write(t, source, name+".go", fmt.Sprintf("package fixture\nfunc %s() int { return %d }\n", name, i))
		}
		git(t, source, "add", ".")
		git(t, source, "commit", "-q", "-m", fmt.Sprintf("paired change %d", i))
	}
	shallow := filepath.Join(t.TempDir(), "shallow")
	// --no-local makes depth effective even for this strictly local transport.
	git(t, source, "clone", "-q", "--depth=1", "--no-local", source, shallow)
	git(t, shallow, "remote", "set-url", "origin", "https://github.com/example/history-cache.git")
	run := func(repo, cache string, uncached bool) []sem.RelationRecord {
		t.Helper()
		args := []string{"snapshot", "--repo", repo, "--profile", "full", "--format", "ndjson", "--cache-dir", cache}
		if uncached {
			args = append(args, "--no-cache")
		}
		var out bytes.Buffer
		if err := Run(t.Context(), Options{Version: "history-test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, args); err != nil {
			t.Fatal(err)
		}
		var history []sem.RelationRecord
		decoder := json.NewDecoder(&out)
		for {
			var record sem.RelationRecord
			if err := decoder.Decode(&record); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if record.Type == "FILE_CHANGES_WITH" {
				history = append(history, record)
			}
		}
		return history
	}
	fullHistory := run(source, t.TempDir(), true)
	shallowHistory := run(shallow, t.TempDir(), true)
	if len(fullHistory) == 0 || len(shallowHistory) != 0 {
		t.Fatalf("fixture must expose different histories: full=%d shallow=%d", len(fullHistory), len(shallowHistory))
	}
	for _, order := range [][2]string{{source, shallow}, {shallow, source}} {
		cache := t.TempDir()
		run(order[0], cache, false)
		if got, want := run(order[1], cache, false), run(order[1], cache, true); !reflect.DeepEqual(got, want) {
			t.Fatalf("shared records reused another checkout's history: got=%+v want=%+v", got, want)
		}
	}
	cache := t.TempDir()
	run(shallow, cache, false)
	git(t, shallow, "fetch", "-q", "--unshallow", source)
	if got, want := run(shallow, cache, false), run(shallow, cache, true); !reflect.DeepEqual(got, want) || len(got) == 0 {
		t.Fatalf("deepening at the same HEAD replayed stale history: got=%+v want=%+v", got, want)
	}
}
