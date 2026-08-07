package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// searchInSession runs one `search` the way a capped agent harness does: same repo, same session
// file, a different question each time.
func searchInSession(t *testing.T, repo, session, maxSearches, query string) string {
	t.Helper()
	var out bytes.Buffer
	err := Run(t.Context(), Options{
		Version: "0.1.0",
		Env:     EntireEnv{RepoRoot: repo, SearchSession: session, MaxSearches: maxSearches},
		Stdout:  &out,
	}, []string{
		"search", "--repo", repo, "--query", query, "--format", "text",
		"--profile", "syntax-only", "--worktree", "--top-k", "1",
	})
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	return out.String()
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
