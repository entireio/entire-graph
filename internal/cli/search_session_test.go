package cli

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

// searchInSession runs one `search` the way a capped agent harness does: same repo, same session
// file, a different question each time.
func searchInSession(t *testing.T, repo, session, maxSearches, query string, extraArgs ...string) string {
	t.Helper()
	return searchInSessionView(t, repo, session, maxSearches, query, true, extraArgs...)
}

func searchInHeadSession(t *testing.T, repo, session, maxSearches, query string, extraArgs ...string) string {
	t.Helper()
	return searchInSessionView(t, repo, session, maxSearches, query, false, extraArgs...)
}

func searchInSessionView(
	t *testing.T,
	repo, session, maxSearches, query string,
	worktree bool,
	extraArgs ...string,
) string {
	t.Helper()
	return searchInSessionViewFormat(t, repo, session, maxSearches, query, worktree, "text", extraArgs...)
}

func searchInSessionViewFormat(
	t *testing.T,
	repo, session, maxSearches, query string,
	worktree bool,
	format string,
	extraArgs ...string,
) string {
	t.Helper()
	var out bytes.Buffer
	args := []string{
		"search", "--repo", repo, "--query", query, "--format", format,
		"--profile", "syntax-only", "--top-k", "1",
	}
	if worktree {
		args = append(args, "--worktree")
	} else {
		args = append(args, "--head")
	}
	args = append(args, extraArgs...)
	err := Run(t.Context(), Options{
		Version: "0.1.0",
		Env:     EntireEnv{RepoRoot: repo, SearchSession: session, MaxSearches: maxSearches},
		Stdout:  &out,
	}, args)
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	return out.String()
}

// Machine-readable responses always expose corpus-wide counters whose complete file provenance is
// intentionally not materialized into a bounded session record. They must run live instead of
// replaying an opaque payload that may describe files newly excluded by dynamic worktree policy.
func TestSearchEchoDoesNotPersistAggregateMachineFormats(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"json", "ndjson"} {
		format := format
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			write(t, repo, "safe.py", "def aggregate_safe():\n    return True\n")
			write(t, repo, "fresh.py", "def aggregate_fresh():\n    return True\n")
			session := filepath.Join(t.TempDir(), "session.json")

			first := searchInSessionViewFormat(t, repo, session, "", "aggregate_safe", true, format)
			if !strings.Contains(first, "safe.py") {
				t.Fatalf("first %s search missed its positive control: %q", format, first)
			}
			second := searchInSessionViewFormat(t, repo, session, "", "aggregate_fresh", true, format)
			if strings.Contains(second, "not run") || !strings.Contains(second, "fresh.py") {
				t.Fatalf("aggregate %s response was replayed instead of rerun: %q", format, second)
			}
			state, err := (&searchSession{path: session, limit: 1}).load()
			if err != nil {
				t.Fatal(err)
			}
			if state.Payload != "" {
				t.Fatalf("aggregate %s payload was persisted for replay", format)
			}
		})
	}
}

func TestSearchResponseCanReplayRejectsAggregateAgentDiagnostics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		format   string
		response sem.SearchResponse
		want     bool
	}{
		{name: "text", format: "text", want: true},
		{name: "clean agent", format: "agent", want: true},
		{
			name:     "agent warning",
			format:   "agent",
			response: sem.SearchResponse{Warnings: []sem.ProviderWarning{{Code: "W_FILE_LIMIT"}}},
		},
		{
			name:     "agent partial failure",
			format:   "agent",
			response: sem.SearchResponse{PartialFailures: []sem.PartialFailure{{Code: "E_PARSE"}}},
		},
		{name: "json", format: "json"},
		{name: "ndjson", format: "ndjson"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := searchResponseCanReplay(test.format, test.response); got != test.want {
				t.Fatalf("searchResponseCanReplay(%q) = %t, want %t", test.format, got, test.want)
			}
		})
	}
}

func rewriteSearchSessionState(t *testing.T, session string, rewrite func(map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(session)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	rewrite(state)
	data, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The second search of a task must replay the first one's payload, not run a new query. The
// measurement behind the cap is in search_session.go: >=4-call sessions cost 1.148 of the no-tool
// baseline against 0.975 for one-call sessions, +0.173 with a bootstrap CI of [+0.019,+0.324].
func TestSearchEchoesFirstPayloadOnRepeatSearch(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "alpha.py", "def alpha_widget():\n    return True\n")
	write(t, repo, "beta.py", "def beta_gadget():\n    return False\n")
	session := filepath.Join(t.TempDir(), "session.json")

	first := searchInSession(t, repo, session, "", "alpha_widget")
	if !strings.Contains(first, "alpha.py") {
		t.Fatalf("first search did not answer its own question: %q", first)
	}

	second := searchInSession(t, repo, session, "", "beta_gadget")
	header, replayed, ok := strings.Cut(second, "\n")
	if !ok {
		t.Fatalf("echo is not header + payload: %q", second)
	}
	// Verbatim: the echo must hand back the exact bytes of the first payload, so a session that
	// re-asks is left in precisely the state its first search put it in.
	if replayed != first {
		t.Fatalf("echoed payload is not the first payload verbatim:\n got: %q\nwant: %q", replayed, first)
	}
	// The point of the cap: the second query never ran.
	if strings.Contains(replayed, "beta.py") {
		t.Fatalf("second query was executed: %q", replayed)
	}
	// One line that names both questions: the one that was skipped, and the one the bytes below
	// answer — an echo read as a reply to the query just typed is worse than no echo.
	if !strings.Contains(header, "beta_gadget") || !strings.Contains(header, "not run") {
		t.Fatalf("header does not say the query was skipped: %q", header)
	}
	if !strings.Contains(header, "alpha_widget") {
		t.Fatalf("header does not say which question the payload answers: %q", header)
	}
	// One line, and no other subcommand named in it. A refusal that points somewhere else is how a
	// capped session turns into a fan-out, which is the behaviour the cap exists to remove.
	for _, verb := range []string{"neighbors", "impact", "symbols", "edges", "grep", "def "} {
		if strings.Contains(header, verb) {
			t.Fatalf("echo header advertises %q: %q", verb, header)
		}
	}
}

// Persisted session state is caller-owned input. Even when its replay schema,
// policy, repository, tree, format, and provenance are all current, its opaque
// payload must cross the same terminal-safe sink as a freshly rendered text or
// agent response.
func TestSearchEchoEscapesPersistedTerminalControls(t *testing.T) {
	for _, format := range []string{"text", "agent"} {
		t.Run(format, func(t *testing.T) {
			repo := t.TempDir()
			git(t, repo, "init")
			git(t, repo, "config", "user.name", "Entire Graph Tests")
			git(t, repo, "config", "user.email", "tests@entire.local")
			write(t, repo, "safe.py", "def terminal_safe_replay_control():\n    return True\n")
			git(t, repo, "add", ".")
			git(t, repo, "commit", "-m", "terminal-safe replay fixture")
			session := filepath.Join(t.TempDir(), "session.json")

			first := searchInSessionViewFormat(
				t, repo, session, "", "terminal_safe_replay_control", false, format, "--no-cache",
			)
			if !strings.Contains(first, "safe.py") {
				t.Fatalf("first %s search did not establish its positive control: %q", format, first)
			}
			state, err := (&searchSession{path: session, limit: 1}).load()
			if err != nil {
				t.Fatal(err)
			}
			if state.Payload == "" || state.PayloadPaths == nil || state.Format != format {
				t.Fatalf("first %s search did not store valid replay state: %#v", format, state)
			}

			const rawPayload = "stored prefix \x1b[2J stored suffix\n"
			rewriteSearchSessionState(t, session, func(state map[string]any) {
				state["query"] = "stored terminal-control query"
				state["payload"] = rawPayload
			})

			got := searchInSessionViewFormat(
				t, repo, session, "", "second terminal-safe query", false, format, "--no-cache",
			)
			header, replayed, ok := strings.Cut(got, "\n")
			if !ok || !strings.Contains(header, "not run") {
				t.Fatalf("valid %s session payload was not replayed: %q", format, got)
			}
			if strings.Contains(got, "\x1b") {
				t.Fatalf("raw ESC reached replayed %s output: %q", format, got)
			}
			want := string(termsafe.Bytes([]byte(rawPayload)))
			if replayed != want {
				t.Fatalf("replayed %s payload = %q, want terminal-safe %q", format, replayed, want)
			}
		})
	}
}

// A session payload is valid only for the exact bounded source set that produced
// it. Changing the source-file cap, or changing which worktree paths occupy that
// cap, must run a live search and replace the old payload before the cap re-arms.
// This test is intentionally not parallel because ENTIRE_GRAPH_MAX_FILES is a
// process environment variable.
func TestSearchEchoRefusesChangedBoundedSourceSet(t *testing.T) {
	t.Run("HEAD cap change", func(t *testing.T) {
		repo := t.TempDir()
		git(t, repo, "init")
		git(t, repo, "config", "user.name", "Entire Graph Tests")
		git(t, repo, "config", "user.email", "tests@entire.local")
		const (
			oldMarker   = "old_head_source_cap_marker"
			freshMarker = "fresh_head_source_cap_marker"
		)
		write(t, repo, "a.py", "def "+freshMarker+"():\n    return True\n")
		write(t, repo, "b.py", "def "+oldMarker+"():\n    return 'OLD_HEAD_SOURCE_CAP_SENTINEL'\n")
		git(t, repo, "add", ".")
		git(t, repo, "commit", "-m", "source cap fixture")
		session := filepath.Join(t.TempDir(), "session.json")

		t.Setenv("ENTIRE_GRAPH_MAX_FILES", "2")
		first := searchInHeadSession(t, repo, session, "", oldMarker, "--no-cache")
		if strings.Contains(first, "not run") || !strings.Contains(first, "b.py") ||
			!strings.Contains(first, oldMarker) {
			t.Fatalf("cap-2 search did not establish the b.py positive control: %q", first)
		}

		t.Setenv("ENTIRE_GRAPH_MAX_FILES", "1")
		second := searchInHeadSession(t, repo, session, "", freshMarker, "--no-cache")
		if strings.Contains(second, "not run") || strings.Contains(second, "b.py") ||
			strings.Contains(second, oldMarker) {
			t.Fatalf("cap-1 search replayed the cap-2 payload or retained displaced b.py: %q", second)
		}
		if !strings.Contains(second, "a.py") || !strings.Contains(second, freshMarker) {
			t.Fatalf("cap-1 search did not run the live a.py query: %q", second)
		}

		third := searchInHeadSession(t, repo, session, "", "another bounded HEAD question", "--no-cache")
		requireSearchSessionReplay(t, third, second)
	})

	t.Run("worktree lexical displacement", func(t *testing.T) {
		repo := t.TempDir()
		git(t, repo, "init")
		git(t, repo, "config", "user.name", "Entire Graph Tests")
		git(t, repo, "config", "user.email", "tests@entire.local")
		const (
			oldMarker   = "old_worktree_source_cap_marker"
			freshMarker = "fresh_worktree_source_cap_marker"
		)
		write(t, repo, "a.py", "def "+freshMarker+"():\n    return True\n")
		write(t, repo, "b.py", "def "+oldMarker+"():\n    return 'OLD_WORKTREE_SOURCE_CAP_SENTINEL'\n")
		git(t, repo, "add", ".")
		git(t, repo, "commit", "-m", "worktree source cap fixture")
		session := filepath.Join(t.TempDir(), "session.json")

		t.Setenv("ENTIRE_GRAPH_MAX_FILES", "2")
		first := searchInSession(t, repo, session, "", oldMarker, "--no-cache")
		if strings.Contains(first, "not run") || !strings.Contains(first, "b.py") ||
			!strings.Contains(first, oldMarker) {
			t.Fatalf("initial cap-2 worktree search did not establish the b.py positive control: %q", first)
		}

		write(t, repo, "0.py", "def lexically_earlier_untracked_file():\n    return True\n")
		second := searchInSession(t, repo, session, "", freshMarker, "--no-cache")
		if strings.Contains(second, "not run") || strings.Contains(second, "b.py") ||
			strings.Contains(second, oldMarker) {
			t.Fatalf("changed cap-2 worktree replayed the old payload or retained displaced b.py: %q", second)
		}
		if !strings.Contains(second, "a.py") || !strings.Contains(second, freshMarker) {
			t.Fatalf("changed cap-2 worktree did not run the live a.py query: %q", second)
		}

		third := searchInSession(t, repo, session, "", "another bounded worktree question", "--no-cache")
		requireSearchSessionReplay(t, third, second)
	})
}

func requireSearchSessionReplay(t *testing.T, got, wantPayload string) {
	t.Helper()
	header, replayed, ok := strings.Cut(got, "\n")
	if !ok || !strings.Contains(header, "not run") || replayed != wantPayload {
		t.Fatalf("fresh bounded-source result did not re-arm the cap:\n got %q\nwant replay of %q", got, wantPayload)
	}
}

// Session files predate the corpus-policy fields that make a replay safe. A legacy state may have
// the right repository and tree and still have been recorded by a binary that admitted credential
// stores, so a missing policy fingerprint is a mismatch rather than a wildcard.
func TestSearchEchoRefusesLegacyStateWithoutPolicy(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "safe.py", "def safe_control():\n    return True\n")
	write(t, repo, "fresh.py", "def fresh_control():\n    return True\n")
	session := filepath.Join(t.TempDir(), "session.json")

	first := searchInSession(t, repo, session, "", "safe_control")
	if !strings.Contains(first, "safe.py") {
		t.Fatalf("first search did not establish a live session: %q", first)
	}
	const legacySentinel = "LEGACY-SESSION-PAYLOAD-MUST-NOT-REPLAY"
	rewriteSearchSessionState(t, session, func(state map[string]any) {
		delete(state, "policy_fingerprint")
		state["query"] = "legacy query"
		state["payload"] = legacySentinel + "\n"
	})

	got := searchInSession(t, repo, session, "", "fresh_control")
	if strings.Contains(got, legacySentinel) || strings.Contains(got, "not run") {
		t.Fatalf("legacy session state was replayed instead of rejected:\n%s", got)
	}
	if !strings.Contains(got, "fresh.py") {
		t.Fatalf("legacy-state rejection did not run the live query: %q", got)
	}
}

// An explicit include is a deliberate authority expansion. Removing it on the next invocation
// must run a fresh search: replaying the first payload would carry credential material across the
// policy boundary even though the live default excludes the path.
func TestSearchEchoRefusesIncludeToDefaultPolicyReplay(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	const secret = "placeholder-session-policy-secret"
	write(t, repo, ".env", "SESSION_POLICY_SECRET="+secret+"\n")
	write(t, repo, "safe.py", "def safe_policy_control():\n    return True\n")
	includeDir := t.TempDir()
	write(t, includeDir, "include.txt", ".env\n")
	includeFile := filepath.Join(includeDir, "include.txt")
	session := filepath.Join(t.TempDir(), "session.json")

	first := searchInSession(t, repo, session, "", "SESSION_POLICY_SECRET", "--include-file", includeFile)
	if !strings.Contains(first, ".env") || !strings.Contains(first, secret) {
		t.Fatalf("explicit include did not establish the credential-bearing positive control: %q", first)
	}

	second := searchInSession(t, repo, session, "", "safe_policy_control")
	if strings.Contains(second, "not run") || strings.Contains(second, ".env") || strings.Contains(second, secret) {
		t.Fatalf("default policy replayed the explicitly included payload:\n%s", second)
	}
	if !strings.Contains(second, "safe.py") {
		t.Fatalf("default policy did not run its live query: %q", second)
	}

	third := searchInSession(t, repo, session, "", "another question")
	header, replayed, ok := strings.Cut(third, "\n")
	if !ok || !strings.Contains(header, "not run") || replayed != second {
		t.Fatalf("fresh default-policy result did not re-arm the cap:\n got %q\nwant replay of %q", third, second)
	}
}

// A Git tree identifies the whole checkout, not the --repo-relative namespace. Sibling
// subdirectories can both contain item.py under the same tree, so repository identity must remain
// part of the replay scope even when the tree hash is available.
func TestSearchEchoRefusesSiblingRepoSubdirectoryReplay(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	git(t, root, "init")
	git(t, root, "config", "user.name", "Entire Graph Tests")
	git(t, root, "config", "user.email", "tests@entire.local")
	const firstSentinel = "sibling_subdir_a_private_value"
	write(t, root, "a/item.py", "def sibling_subdir_a():\n    return \""+firstSentinel+"\"\n")
	write(t, root, "b/item.py", "def sibling_subdir_b():\n    return \"safe-b\"\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "sibling repo scopes")
	session := filepath.Join(t.TempDir(), "session.json")

	first := searchInSession(t, filepath.Join(root, "a"), session, "", "sibling_subdir_a")
	if !strings.Contains(first, firstSentinel) {
		t.Fatalf("first subdirectory search missed its positive control: %q", first)
	}
	second := searchInSession(t, filepath.Join(root, "b"), session, "", "sibling_subdir_b")
	if strings.Contains(second, "not run") || strings.Contains(second, firstSentinel) {
		t.Fatalf("sibling --repo subdirectory replayed the first namespace:\n%s", second)
	}
	if !strings.Contains(second, "sibling_subdir_b") {
		t.Fatalf("second subdirectory did not run its live query: %q", second)
	}
}

// The payload is already rendered bytes. Replaying text as agent output (or the reverse) violates
// the caller's wire contract even when repository and corpus identities are otherwise unchanged.
func TestSearchEchoRefusesTextAgentFormatChange(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		firstFormat  string
		secondFormat string
	}{
		{name: "text to agent", firstFormat: "text", secondFormat: "agent"},
		{name: "agent to text", firstFormat: "agent", secondFormat: "text"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			git(t, repo, "init")
			git(t, repo, "config", "user.name", "Entire Graph Tests")
			git(t, repo, "config", "user.email", "tests@entire.local")
			write(t, repo, "safe.py", "def format_scope_safe():\n    return True\n")
			write(t, repo, "fresh.py", "def format_scope_fresh():\n    return True\n")
			git(t, repo, "add", ".")
			git(t, repo, "commit", "-m", "format replay fixture")
			session := filepath.Join(t.TempDir(), "session.json")

			first := searchInSessionViewFormat(
				t, repo, session, "", "format_scope_safe", false, test.firstFormat,
			)
			if !strings.Contains(first, "safe.py") {
				t.Fatalf("first %s search missed its positive control: %q", test.firstFormat, first)
			}
			second := searchInSessionViewFormat(
				t, repo, session, "", "format_scope_fresh", false, test.secondFormat,
			)
			if strings.Contains(second, "not run") || !strings.Contains(second, "fresh.py") {
				t.Fatalf("%s payload crossed into %s output: %q", test.firstFormat, test.secondFormat, second)
			}
			third := searchInSessionViewFormat(
				t, repo, session, "", "another format question", false, test.secondFormat,
			)
			header, replayed, ok := strings.Cut(third, "\n")
			if !ok || !strings.Contains(header, "not run") || replayed != second {
				t.Fatalf("same-format result did not re-arm the cap:\n got %q\nwant replay of %q", third, second)
			}
		})
	}
}

// Binding replay to policy must not disable the cap. Two calls with the same nonempty explicit
// policy still replay the first rendered payload byte-for-byte.
func TestSearchEchoReusesIdenticalNonemptyPolicy(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "alpha.py", "def alpha_policy_widget():\n    return True\n")
	write(t, repo, "beta.py", "def beta_policy_widget():\n    return True\n")
	ignoreDir := t.TempDir()
	write(t, ignoreDir, "ignore.txt", "unrelated.py\n")
	ignoreFile := filepath.Join(ignoreDir, "ignore.txt")
	session := filepath.Join(t.TempDir(), "session.json")

	first := searchInSession(t, repo, session, "", "alpha_policy_widget", "--ignore-file", ignoreFile)
	if !strings.Contains(first, "alpha.py") {
		t.Fatalf("first policy-bound search missed its positive control: %q", first)
	}
	second := searchInSession(t, repo, session, "", "beta_policy_widget", "--ignore-file", ignoreFile)
	header, replayed, ok := strings.Cut(second, "\n")
	if !ok || !strings.Contains(header, "not run") || replayed != first {
		t.Fatalf("identical nonempty policy did not replay verbatim:\n got %q\nwant replay of %q", second, first)
	}
}

func TestSearchEchoRefusesChangedOrReorderedPolicyInputs(t *testing.T) {
	t.Parallel()

	t.Run("changed file content", func(t *testing.T) {
		repo := t.TempDir()
		const secret = "placeholder-changed-policy-secret"
		write(t, repo, ".env", "CHANGED_POLICY_SECRET="+secret+"\n")
		write(t, repo, "safe.py", "def changed_policy_control():\n    return True\n")
		policyDir := t.TempDir()
		write(t, policyDir, "include.txt", ".env\n")
		policyFile := filepath.Join(policyDir, "include.txt")
		session := filepath.Join(t.TempDir(), "session.json")

		first := searchInSession(t, repo, session, "", "CHANGED_POLICY_SECRET", "--include-file", policyFile)
		if !strings.Contains(first, secret) {
			t.Fatalf("first policy content did not expose its positive control: %q", first)
		}
		write(t, policyDir, "include.txt", "safe.py\n")

		got := searchInSession(t, repo, session, "", "changed_policy_control", "--include-file", policyFile)
		if strings.Contains(got, "not run") || strings.Contains(got, secret) || !strings.Contains(got, "safe.py") {
			t.Fatalf("changed policy-file content reused the old payload:\n%s", got)
		}
	})

	t.Run("reversed ordered inputs", func(t *testing.T) {
		repo := t.TempDir()
		write(t, repo, "target.py", "def ordered_policy_target():\n    return True\n")
		write(t, repo, "control.py", "def ordered_policy_control():\n    return True\n")
		policyDir := t.TempDir()
		write(t, policyDir, "ignore.txt", "target.py\n")
		write(t, policyDir, "reinclude.txt", "!target.py\n")
		ignoreFile := filepath.Join(policyDir, "ignore.txt")
		reincludeFile := filepath.Join(policyDir, "reinclude.txt")
		session := filepath.Join(t.TempDir(), "session.json")

		first := searchInSession(t, repo, session, "", "ordered_policy_target",
			"--ignore-file", ignoreFile, "--ignore-file", reincludeFile)
		if !strings.Contains(first, "target.py") {
			t.Fatalf("later re-inclusion did not establish the order-sensitive positive control: %q", first)
		}

		got := searchInSession(t, repo, session, "", "ordered_policy_control",
			"--ignore-file", reincludeFile, "--ignore-file", ignoreFile)
		if strings.Contains(got, "not run") || strings.Contains(got, "target.py") || !strings.Contains(got, "control.py") {
			t.Fatalf("reversed order-sensitive inputs reused the old payload:\n%s", got)
		}
	})
}

// Even a state carrying the current schema and policy must be checked against the paths its
// rendered payload came from. The stored bytes are opaque, so rejection must happen before Run
// writes any of them; substring redaction after the write cannot make a replay safe.
func TestSearchEchoRejectsDisallowedPayloadPathBeforeWriting(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, ".env", "PATH_PROVENANCE_SECRET=placeholder-path-provenance-secret\n")
	write(t, repo, "safe.py", "def path_policy_control():\n    return True\n")
	write(t, repo, "fresh.py", "def path_policy_fresh():\n    return True\n")
	session := filepath.Join(t.TempDir(), "session.json")

	first := searchInSession(t, repo, session, "", "path_policy_control")
	if !strings.Contains(first, "safe.py") {
		t.Fatalf("first search did not establish current replay metadata: %q", first)
	}
	const storedSentinel = "STORED-DISALLOWED-PAYLOAD-MUST-NEVER-BE-WRITTEN"
	rewriteSearchSessionState(t, session, func(state map[string]any) {
		state["query"] = "stored credential query"
		state["payload"] = storedSentinel + "\nPATH_PROVENANCE_SECRET=placeholder-path-provenance-secret\n"
		state["payload_paths"] = []string{".env"}
	})

	got := searchInSession(t, repo, session, "", "path_policy_fresh")
	if strings.Contains(got, storedSentinel) || strings.Contains(got, "PATH_PROVENANCE_SECRET") || strings.Contains(got, "not run") {
		t.Fatalf("disallowed stored path reached stdout before replay rejection:\n%s", got)
	}
	if !strings.Contains(got, "fresh.py") {
		t.Fatalf("path-provenance rejection did not run the live query: %q", got)
	}
}

// The path list is mandatory provenance, including for a response that happened to name no files.
// Omitting the field must not be interpreted as an authenticated empty list: otherwise removing one
// JSON member from a current-schema state bypasses every replay-time path check.
func TestSearchEchoRefusesCurrentSchemaStateWithoutPathProvenance(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "safe.py", "def missing_provenance_safe():\n    return True\n")
	write(t, repo, "fresh.py", "def missing_provenance_fresh():\n    return True\n")
	session := filepath.Join(t.TempDir(), "session.json")

	first := searchInSession(t, repo, session, "", "missing_provenance_safe")
	if !strings.Contains(first, "safe.py") {
		t.Fatalf("first search did not establish current replay metadata: %q", first)
	}
	const storedSentinel = "MISSING-PATH-PROVENANCE-MUST-NOT-REPLAY"
	rewriteSearchSessionState(t, session, func(state map[string]any) {
		state["query"] = "stored query without provenance"
		state["payload"] = storedSentinel + "\n"
		delete(state, "payload_paths")
	})

	got := searchInSession(t, repo, session, "", "missing_provenance_fresh")
	if strings.Contains(got, storedSentinel) || strings.Contains(got, "not run") {
		t.Fatalf("state without path provenance reached stdout instead of being rejected:\n%s", got)
	}
	if !strings.Contains(got, "fresh.py") {
		t.Fatalf("missing-provenance rejection did not run the live query: %q", got)
	}
}

func TestSearchSessionRecordSaturatesSearchCount(t *testing.T) {
	t.Parallel()
	sessionPath := filepath.Join(t.TempDir(), "session.json")
	session := &searchSession{path: sessionPath, limit: 1}
	scope := searchSessionScope{Repo: t.TempDir(), PolicyFingerprint: "policy", Format: "text"}
	state := searchSessionState{
		Searches:          math.MaxInt,
		ReplaySchema:      searchSessionReplaySchema,
		PolicyFingerprint: scope.PolicyFingerprint,
		PayloadPaths:      []string{},
		Repo:              scope.Repo,
		Format:            scope.Format,
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	session.record("fresh", []byte("fresh payload\n"), []string{}, scope, false, true)
	got, err := session.load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Searches != math.MaxInt {
		t.Fatalf("search count wrapped or changed: got %d, want %d", got.Searches, math.MaxInt)
	}
	if got.Payload != "fresh payload\n" {
		t.Fatalf("saturated state did not retain the fresh replayable payload: %q", got.Payload)
	}
}

func TestSearchSessionLoadRejectsNegativeSearchCount(t *testing.T) {
	t.Parallel()
	sessionPath := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(sessionPath, []byte(`{"searches":-1,"payload_paths":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&searchSession{path: sessionPath, limit: 1}).load(); err == nil {
		t.Fatal("negative persisted search count was accepted")
	}
}

func TestSearchSessionLoadRejectsOverlongPayloadPath(t *testing.T) {
	t.Parallel()
	sessionPath := filepath.Join(t.TempDir(), "session.json")
	state := searchSessionState{
		PayloadPaths: []string{strings.Repeat("a", sem.SearchReplayMaxPathBytes+1)},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&searchSession{path: sessionPath, limit: 1}).load(); err == nil {
		t.Fatal("overlong persisted payload path was accepted")
	}
}

// Nested Git excludes are evaluated by Git at replay time rather than folded into the root-policy
// fingerprint. A path that was eligible when recorded can therefore become ineligible without HEAD
// or the fingerprint changing; the path gate must still reject it before any stored byte is written.
func TestSearchEchoRefusesPathNewlyExcludedByNestedGitignore(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Tests")
	git(t, repo, "config", "user.email", "tests@entire.local")
	write(t, repo, "safe.py", "def nested_ignore_safe():\n    return True\n")
	write(t, repo, "fresh.py", "def nested_ignore_fresh():\n    return True\n")
	git(t, repo, "add", "safe.py", "fresh.py")
	git(t, repo, "commit", "-m", "nested ignore replay fixture")
	const excludedSentinel = "placeholder-newly-nested-ignored-secret"
	write(t, repo, "nested/exposed.py", "NESTED_REPLAY_SECRET = \""+excludedSentinel+"\"\n")
	session := filepath.Join(t.TempDir(), "session.json")

	first := searchInSession(t, repo, session, "", "NESTED_REPLAY_SECRET")
	if !strings.Contains(first, "nested/exposed.py") || !strings.Contains(first, excludedSentinel) {
		t.Fatalf("first search did not establish the formerly eligible positive control: %q", first)
	}
	write(t, repo, "nested/.gitignore", "exposed.py\n")

	second := searchInSession(t, repo, session, "", "nested_ignore_fresh")
	if strings.Contains(second, "not run") || strings.Contains(second, "nested/exposed.py") ||
		strings.Contains(second, excludedSentinel) {
		t.Fatalf("new nested Git exclude did not reject the stored payload before output:\n%s", second)
	}
	if !strings.Contains(second, "fresh.py") {
		t.Fatalf("nested-ignore rejection did not run the live query: %q", second)
	}

	third := searchInSession(t, repo, session, "", "another nested-ignore question")
	header, replayed, ok := strings.Cut(third, "\n")
	if !ok || !strings.Contains(header, "not run") || replayed != second {
		t.Fatalf("nested-ignore recovery did not re-arm the cap:\n got %q\nwant replay of %q", third, second)
	}
}

// A session file is caller-owned state, so it must be bounded before JSON decoding can allocate a
// payload proportional to an arbitrary file on disk. An over-limit state is the same as a broken
// one: run the live search, replace it with bounded current state, and re-arm the cap.
func TestSearchEchoRejectsOversizedSessionStateAndRearms(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "safe.py", "def bounded_session_control():\n    return True\n")
	write(t, repo, "fresh.py", "def bounded_session_fresh():\n    return True\n")
	session := filepath.Join(t.TempDir(), "session.json")

	first := searchInSession(t, repo, session, "", "bounded_session_control")
	if !strings.Contains(first, "safe.py") {
		t.Fatalf("first search did not establish current replay metadata: %q", first)
	}
	const storedSentinel = "OVERSIZED-SESSION-PAYLOAD-MUST-NOT-REPLAY"
	rewriteSearchSessionState(t, session, func(state map[string]any) {
		state["query"] = "oversized stored query"
		// Just over the 8 MiB persisted-state ceiling, including ample room for the
		// surrounding JSON fields without turning this into an unbounded test fixture.
		state["payload"] = storedSentinel + strings.Repeat("x", (8<<20)+1)
	})

	second := searchInSession(t, repo, session, "", "bounded_session_fresh")
	if strings.Contains(second, storedSentinel) || strings.Contains(second, "not run") {
		t.Fatalf("oversized session state reached stdout (output bytes %d)", len(second))
	}
	if !strings.Contains(second, "fresh.py") {
		t.Fatalf("oversized-state rejection did not run the live query: %q", second)
	}
	third := searchInSession(t, repo, session, "", "another bounded question")
	header, replayed, ok := strings.Cut(third, "\n")
	if !ok || !strings.Contains(header, "not run") || replayed != second {
		t.Fatalf("oversized-state recovery did not re-arm the cap:\n got %q\nwant replay of %q", third, second)
	}
}

// The path list is untrusted persisted input too. Each case exceeds one bound while remaining
// below the other path-list and whole-file ceilings. Every repeated path is an actual HEAD member,
// so the replay must fail on the intended bound rather than incidentally on tree membership.
func TestSearchEchoRejectsUnboundedPayloadPathMetadataAndRearms(t *testing.T) {
	t.Parallel()
	aggregatePath := strings.Repeat("b", 129)

	tests := []struct {
		name  string
		paths func() []string
	}{
		{
			name: "excessive path count",
			paths: func() []string {
				// One more than the 1,024-entry ceiling; duplicates are intentional
				// hostile JSON and must consume the bound before any deduplication.
				paths := make([]string, 1025)
				for i := range paths {
					paths[i] = "safe.py"
				}
				return paths
			},
		},
		{
			name: "excessive aggregate path bytes",
			paths: func() []string {
				// 1,024 * 129 = 132,096 bytes: above the 128 KiB aggregate
				// ceiling, with every individual path and the count still valid.
				paths := make([]string, sem.SearchReplayMaxPathCount)
				for i := range paths {
					paths[i] = aggregatePath
				}
				return paths
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			write(t, repo, "safe.py", "def bounded_paths_control():\n    return True\n")
			write(t, repo, "fresh.py", "def bounded_paths_fresh():\n    return True\n")
			write(t, repo, aggregatePath, "aggregate provenance member\n")
			git(t, repo, "init")
			git(t, repo, "config", "user.name", "Entire Graph Tests")
			git(t, repo, "config", "user.email", "tests@entire.local")
			git(t, repo, "add", ".")
			git(t, repo, "commit", "-m", "bounded replay fixture")
			session := filepath.Join(t.TempDir(), "session.json")

			first := searchInHeadSession(t, repo, session, "", "bounded_paths_control")
			if !strings.Contains(first, "safe.py") {
				t.Fatalf("first search did not establish current HEAD replay metadata: %q", first)
			}
			const storedSentinel = "UNBOUNDED-PAYLOAD-PATHS-MUST-NOT-REPLAY"
			rewriteSearchSessionState(t, session, func(state map[string]any) {
				state["query"] = "unbounded stored paths query"
				state["payload"] = storedSentinel + "\n"
				state["payload_paths"] = test.paths()
			})

			second := searchInHeadSession(t, repo, session, "", "bounded_paths_fresh")
			if strings.Contains(second, storedSentinel) || strings.Contains(second, "not run") {
				t.Fatalf("%s metadata reached stdout instead of being rejected", test.name)
			}
			if !strings.Contains(second, "fresh.py") {
				t.Fatalf("%s rejection did not run the live query: %q", test.name, second)
			}
			third := searchInHeadSession(t, repo, session, "", "another bounded paths question")
			header, replayed, ok := strings.Cut(third, "\n")
			if !ok || !strings.Contains(header, "not run") || replayed != second {
				t.Fatalf("%s recovery did not re-arm the cap:\n got %q\nwant replay of %q",
					test.name, third, second)
			}
		})
	}
}

// The cap is a knob, not a constant: 40% of Haiku sessions issue more than one search (2.03
// calls/session), and if the second query is what rescues a first-call miss there, capping costs
// resolves. `EG_MAX_SEARCHES=0` and "no session file at all" must both run every query.
func TestSearchWithoutCapRunsEveryQuery(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "alpha.py", "def alpha_widget():\n    return True\n")
	write(t, repo, "beta.py", "def beta_gadget():\n    return False\n")

	for _, tc := range []struct{ name, session, maxSearches string }{
		{name: "cap disabled", session: filepath.Join(t.TempDir(), "session.json"), maxSearches: "0"},
		{name: "no session file", session: "", maxSearches: "1"},
		{name: "unconfigured", session: "", maxSearches: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			searchInSession(t, repo, tc.session, tc.maxSearches, "alpha_widget")
			second := searchInSession(t, repo, tc.session, tc.maxSearches, "beta_gadget")
			if !strings.Contains(second, "beta.py") {
				t.Fatalf("second query did not run: %q", second)
			}
			if strings.Contains(second, "not run") {
				t.Fatalf("uncapped search echoed: %q", second)
			}
		})
	}
}

// A session file that is missing, truncated, or not JSON must degrade to a real search. The echo is
// an optimisation; failing the search instead would cost the whole task.
func TestSearchEchoFailsOpenOnBrokenSessionFile(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "alpha.py", "def alpha_widget():\n    return True\n")
	session := filepath.Join(t.TempDir(), "session.json")
	write(t, filepath.Dir(session), filepath.Base(session), "{not json")

	got := searchInSession(t, repo, session, "", "alpha_widget")
	if !strings.Contains(got, "alpha.py") {
		t.Fatalf("broken session file suppressed the search: %q", got)
	}
	// ...and the search it did run becomes the session's first payload.
	second := searchInSession(t, repo, session, "", "beta_gadget")
	if !strings.Contains(second, "not run") {
		t.Fatalf("session did not recover after the broken file: %q", second)
	}
}

// The echo must never answer for a repository it did not search.
//
// EG_SEARCH_SESSION is scoped to a task by the CALLER, and nothing used to check that claim. A
// harness that reuses one path across a run therefore handed every instance after the first the
// FIRST instance's payload — naming files that do not exist in the tree the agent is looking at —
// under a header saying its question was not run. An agent reading that stops calling the tool, and
// the rest of the run measures a graph arm that never touches the graph.
func TestSearchEchoRefusesAnotherRepositorysPayload(t *testing.T) {
	t.Parallel()
	first := t.TempDir()
	write(t, first, "alpha.py", "def alpha_widget():\n    return True\n")
	second := t.TempDir()
	write(t, second, "beta.py", "def beta_gadget():\n    return False\n")
	// One session file, two repositories — the reuse this guards against.
	session := filepath.Join(t.TempDir(), "session.json")

	if got := searchInSession(t, first, session, "", "alpha_widget"); !strings.Contains(got, "alpha.py") {
		t.Fatalf("first repository's search did not answer its own question: %q", got)
	}

	got := searchInSession(t, second, session, "", "beta_gadget")
	if strings.Contains(got, "not run") || strings.Contains(got, "alpha.py") {
		t.Fatalf("second repository was answered with the first repository's payload:\n%s", got)
	}
	if !strings.Contains(got, "beta.py") {
		t.Fatalf("second repository did not get a real search: %q", got)
	}
	// The refusal re-scopes rather than merely skipping once: this repository's own second query
	// still echoes, so the cap is intact for the task that actually owns the file now.
	if repeat := searchInSession(t, second, session, "", "gamma_thing"); !strings.Contains(repeat, "not run") {
		t.Fatalf("the cap did not re-arm for the new repository: %q", repeat)
	}
}

// An unparseable EG_MAX_SEARCHES is an error, not a silent no-op: a knob that quietly does nothing
// is how a measurement gets attributed to the wrong build.
func TestSearchRejectsNonNumericMaxSearches(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "alpha.py", "def alpha_widget():\n    return True\n")
	err := Run(t.Context(), Options{
		Version: "0.1.0",
		Env:     EntireEnv{RepoRoot: repo, SearchSession: filepath.Join(t.TempDir(), "s.json"), MaxSearches: "yes"},
		Stdout:  &bytes.Buffer{},
	}, []string{"search", "--repo", repo, "--query", "alpha_widget", "--worktree"})
	if err == nil || !strings.Contains(err.Error(), envMaxSearches) {
		t.Fatalf("err = %v, want a complaint about %s", err, envMaxSearches)
	}
}
