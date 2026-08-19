package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSearchReplayEscapesC1 covers the two sinks of `search` that go through no
// encoder at all.
//
// Wrapping the encoders fixes the bytes THIS build produces. It does nothing for
// bytes an earlier build already produced, and `search` has two ways to print
// exactly those before any encoder runs:
//
//   - ENTIRE_GRAPH_PRESEARCH names a file whose contents are written to stdout
//     verbatim, ahead of the profile, the repo, the cache and the index. That file
//     is a payload some other process encoded, so nothing in this process escaped
//     it. See echoPresearchPayload.
//   - EG_SEARCH_SESSION persists the FIRST search of a task and replays it for
//     every later one. A state file written by a build older than the C1 rule
//     holds that build's raw output and keeps replaying it for the life of the
//     task. See searchSession.echo.
//
// Both are the failure TestSnapshotCacheReplayEscapesC1 pins for the provider
// record cache, in the same shape: stored bytes handed to stdout without passing
// the rule. And the format they replay is the one the rule is about — `search`
// defaults to --format json — so a warm replay of a stale payload is the default
// invocation emitting the raw control.
//
// The stale payload is not hand-written. It is this build's own escaped output
// with the escapes turned back into raw code points, which is what a pre-rule
// build wrote to the same file — the same poisoning idiom
// poisonProviderRecordsCache uses, for the same reason: it needs nothing but this
// tree, and the fixture cannot drift away from the real payload shape.
func TestSearchReplayEscapesC1(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	repo, _ := c1HostileRepo(t)
	cacheDir := t.TempDir()

	// The body channel — a C1 sequence inside main.go's Merge — reaches the search
	// payload on every platform, so nothing here is platform-conditional. See
	// c1HostileRepo.
	escaped, _ := runVerb(t, repo, cacheDir, []string{"search", "--query", "merge", "--format", "json"})
	assertNoRawC1(t, escaped)
	stale := stalePayload(t, escaped)

	t.Run("ENTIRE_GRAPH_PRESEARCH", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tf135-presearch.json")
		if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
			t.Fatal(err)
		}
		stdout, stderr := runVerbWithEnv(t, repo, EntireEnv{
			RepoRoot:      repo,
			PluginDataDir: cacheDir,
			PresearchPath: path,
		}, []string{"search", "--query", "merge"})
		assertNoRawC1(t, stdout)
		assertNoRawC1(t, stderr)
		assertCarriesC1AfterDecoding(t, stdout)
		if stdout != escaped {
			t.Errorf("replayed payload differs from the stream that was recorded:\n got  %q\n want %q", stdout, escaped)
		}
	})

	t.Run("EG_SEARCH_SESSION", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tf135-session.json")
		// Repo and Tree are what a real recorder stored. The echo refuses a payload
		// it cannot tie to the tree in front of it, so a state file without them
		// would exercise searchSessionState.matches, not the replay.
		absolute, err := filepath.Abs(repo)
		if err != nil {
			t.Fatal(err)
		}
		state, err := json.Marshal(searchSessionState{
			Searches: 1,
			Query:    "merge",
			Payload:  stale,
			Repo:     absolute,
			Tree:     rev(t, repo, "HEAD^{tree}"),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, state, 0o600); err != nil {
			t.Fatal(err)
		}
		stdout, stderr := runVerbWithEnv(t, repo, EntireEnv{
			RepoRoot:      repo,
			PluginDataDir: cacheDir,
			SearchSession: path,
			MaxSearches:   "1",
		}, []string{"search", "--query", "merge"})
		header, payload, found := strings.Cut(stdout, "\n")
		if !found || !strings.HasPrefix(header, "(one search per task:") {
			t.Fatalf("the echo did not fire, so this case proves nothing:\n%s", stdout)
		}
		assertNoRawC1(t, stdout)
		assertNoRawC1(t, stderr)
		// The header is a %q-quoted line rather than a record, so only the payload
		// below it is decodable JSON.
		assertCarriesC1AfterDecoding(t, payload)
		if payload != escaped {
			t.Errorf("replayed payload differs from the stream that was recorded:\n got  %q\n want %q", payload, escaped)
		}
	})
}

// stalePayload turns this build's escaped stream back into the bytes a build
// predating the C1 rule wrote: valid JSON whose values still carry the raw
// two-byte controls, because encoding/json copies them through. It fails rather
// than return an unpoisoned copy, so a case built on it can never pass vacuously.
func stalePayload(t *testing.T, escaped string) string {
	t.Helper()
	stale := strings.NewReplacer("\\u009d", c1OSC, "\\u009c", c1ST).Replace(escaped)
	if stale == escaped {
		t.Fatal("recorded stream held no escape to turn back into a raw control")
	}
	if indexC1(stale) < 0 {
		t.Fatal("stale payload holds no raw C1, so replaying it would prove nothing")
	}
	var record any
	if err := json.Unmarshal([]byte(strings.TrimRight(stale, "\n")), &record); err != nil {
		t.Fatalf("stale payload is not the valid JSON a pre-rule build wrote: %v", err)
	}
	return stale
}

// runVerbWithEnv is runVerb with the environment under test. Both replay paths are
// reachable only through EntireEnv, and runVerb hard-codes an empty one.
func runVerbWithEnv(t *testing.T, repo string, env EntireEnv, argv []string) (string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	argv = append(append([]string{}, argv...), "--repo", repo)
	if err := Run(t.Context(), Options{
		Version: "test-version",
		Env:     env,
		Stdout:  &stdout,
		Stderr:  &stderr,
		Stdin:   strings.NewReader(""),
	}, argv); err != nil {
		t.Fatalf("%v: %v\nstdout:\n%s\nstderr:\n%s", argv, err, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}
