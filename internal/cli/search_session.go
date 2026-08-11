package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The search echo: one real search per task. The second and later search of the same task returns
// the FIRST search's payload verbatim under a one-line header, instead of running a new query.
//
// Why a cap at all. Measured on agentic SWE-bench sessions, as the cost ratio against the same
// instance solved with no tool at all: sessions that made >=4 graph calls (n=16) cost 1.148 of
// baseline, sessions that made exactly ONE call (n=70) cost 0.975 — difference +0.173, bootstrap
// CI [+0.019,+0.324], which excludes zero. The gradient repeats per configuration: 1.00
// calls/session -> -12.7% (the cheapest Opus cell measured), 1.17 -> -0.7%, 1.63 -> +5.1%, and
// 4.45 calls/session (76% of its sessions multi-call) -> the worst retrieval of the set.
//
// The natural experiment is one instance, facebook__docusaurus-9183, under two configurations.
// One call: gold file at rank 1, $0.273, 8 turns, 3 edits. Eight calls: gold absent from the top
// five, $0.940, 20 turns, 6 edits. Note what the re-queries could not tell the agent — scores are
// not comparable ACROSS queries, so the eighth query's wrong rank-1 (71.3) outscored the first
// query's correct hit (35.4). Re-asking does not produce a better-judged answer, it produces a
// differently-scaled one.
//
// Why an echo rather than a refusal. The repeat call costs its message either way — the agent has
// already paid the turn by the time this code runs — so replaying the payload can only remove
// information, never add a turn. A bare refusal costs the same turn and invites the agent to ask
// again, which is worse than no cap. For the same reason the header is a single line that names no
// other subcommand: pointing a capped session at a different verb is how a cap turns into a
// fan-out.
//
// Why 1 is the default, and why it is still a knob. Graph usage is invariant at 1.33-1.70
// calls/session across six measured configurations, so a cap of 1 clips the tail and leaves the
// median session untouched. The tail is not uniform across models: 40% of Haiku sessions issue
// more than one search, at 2.03 calls/session, and if a second query is what rescues a first-call
// miss there, the cap removes resolves. So the cap ships as EG_MAX_SEARCHES, `0` disables it, and
// a tier that has not been replayed offline should run uncapped.

// searchSession is one task's search state. It lives in a file because the CLI is one-shot: a
// process that has already exited cannot count its successors, and nothing else in the environment
// distinguishes the second search of a task from the first one. EG_SEARCH_SESSION names the file;
// the caller (an agent harness) is what scopes it to a task.
type searchSession struct {
	path  string
	limit int
}

// searchSessionState is the whole persisted record: how many searches ran, the first one's
// question and answer, and the tree that answer describes.
type searchSessionState struct {
	Searches int    `json:"searches"`
	Query    string `json:"query"`
	Payload  string `json:"payload"`
	// Repo and Tree are the scope the payload was recorded against. See searchSessionScope: the
	// state file is what makes an echo possible, and these are what stop it answering for the
	// wrong repository.
	Repo string `json:"repo,omitempty"`
	Tree string `json:"tree,omitempty"`
}

// searchSessionScope identifies the tree a payload describes.
//
// EG_SEARCH_SESSION is scoped to a task BY THE CALLER — the file path is the only thing that says
// "this is the same task as last time", and nothing in the environment checks that claim. A harness
// that reuses one path across a whole run therefore hands every instance after the first the FIRST
// instance's payload, verbatim, under a header saying the question was not run. On a suite where
// consecutive instances are different repositories in different languages, that payload names files
// that do not exist in the tree the agent is looking at. An agent reading it concludes the tool is
// broken and stops calling it — which costs the retrieval, the resolve, and every token the tool
// was there to save, for the whole remainder of the run.
//
// So the payload carries the tree it answered for, and an echo only fires when that still matches.
// HEAD's tree hash is the right key: it is what the record cache already keys on
// (see the RevParse pair in the provider's snapshot path), it is stable across a task because
// agents edit the working tree without committing, and it differs across instances because they
// are different checkouts.
//
// Both fields degrade rather than fail. A repository git cannot describe still gets a scope from
// its resolved path, and a scope that cannot be computed at all compares equal to nothing — which
// refuses the echo and runs a real search. Every ambiguous case resolves toward answering the
// question that was asked.
type searchSessionScope struct {
	Repo string
	Tree string
}

// matches reports whether a recorded scope may answer for the live one.
//
// A state file written before this field existed has an empty scope, and that is treated as a
// mismatch rather than a wildcard: the whole point is that an unscoped payload is exactly the one
// that might belong to another repository. The cost of being wrong is one real search; the cost of
// the wildcard is a session answered from the wrong tree.
func (recorded searchSessionState) matches(live searchSessionScope) bool {
	if recorded.Repo == "" && recorded.Tree == "" {
		return false
	}
	if recorded.Tree != "" || live.Tree != "" {
		return recorded.Tree == live.Tree
	}
	return recorded.Repo == live.Repo
}

// newSearchSession returns nil when the echo is off, which is the default for every caller that
// does not set EG_SEARCH_SESSION — an interactive user's searches are unaffected by this file.
func newSearchSession(env EntireEnv, warn io.Writer) (*searchSession, error) {
	limit := 1
	raw := strings.TrimSpace(env.MaxSearches)
	if raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: want an integer (0 disables the cap), got %q", envMaxSearches, raw)
		}
		limit = parsed
	}
	if limit <= 0 {
		return nil, nil
	}
	path := strings.TrimSpace(env.SearchSession)
	if path == "" {
		// A cap asked for but not enforceable. This warns on stderr and runs the query rather than
		// failing, because a search verb that returns NOTHING is far more expensive than an
		// uncapped one — the harness this ships behind has already paid for that once, when an
		// empty array under `set -u` made its search verb emit nothing on every run of a whole
		// cell before anyone noticed. Stderr never reaches the agent's payload, so the warning
		// cannot cost a turn either.
		if raw != "" && warn != nil {
			fmt.Fprintf(warn, "%s is set but %s is not: the cap has no session file to count in, so this search runs uncapped\n",
				envMaxSearches, envSearchSession)
		}
		return nil, nil
	}
	return &searchSession{path: path, limit: limit}, nil
}

// echo reports the payload to replay when this task has already spent its searches. Every state
// error answers "no echo": a missing, truncated, unreadable, or out-of-scope session file must
// degrade to a real search, never to a failed one.
func (s *searchSession) echo(live searchSessionScope) (searchSessionState, bool) {
	state, err := s.load()
	if err != nil || state.Searches < s.limit || state.Payload == "" {
		return searchSessionState{}, false
	}
	if !state.matches(live) {
		return searchSessionState{}, false
	}
	return state, true
}

func (s *searchSession) load() (searchSessionState, error) {
	var state searchSessionState
	if strings.Contains(s.path, "..") {
		return searchSessionState{}, fmt.Errorf("invalid file path")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return searchSessionState{}, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return searchSessionState{}, err
	}
	return state, nil
}

// record counts a search that actually ran and keeps the FIRST payload: the echo replays the answer
// the ranking gave the original question, not the last rephrasing of it. Persisting is best-effort
// for the same reason echo is — a session file that cannot be written costs the cap, not the search.
func (s *searchSession) record(query string, payload []byte, live searchSessionScope) {
	state, _ := s.load()
	// A state file that belongs to another tree is replaced rather than counted into: its search
	// count describes a different task, and carrying it over would cap this one before it ran.
	if !state.matches(live) {
		state = searchSessionState{}
	}
	state.Searches++
	if state.Payload == "" {
		state.Query = query
		state.Payload = string(payload)
		state.Repo, state.Tree = live.Repo, live.Tree
	}
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	// Temp + rename: a crash between the two writes must not leave the next call reading half a
	// payload, which it would then echo as if it were the whole answer.
	tmp, err := os.CreateTemp(dir, ".eg-search-session-*")
	if err != nil {
		return
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		os.Remove(tmp.Name())
	}
}

// searchEchoHeader is the one line that precedes a replayed payload. It names both questions — the
// one that was not run, and the one the bytes below actually answer, so the agent is not left
// reading a payload as if it were a reply to the query it just typed. Then it stops: no alternative
// verb, no invitation to rephrase.
func searchEchoHeader(asked, answered string) string {
	return fmt.Sprintf("(one search per task: %q was not run — below is your first search %q, verbatim)\n", asked, answered)
}
