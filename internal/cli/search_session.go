package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
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

// searchSessionReplaySchema changes whenever the persisted replay-safety contract changes. A
// session written without the current schema must run a real search: its opaque payload cannot be
// upgraded or inspected safely after the fact.
//
// 4: the payload is bound to the binary that rendered it (Producer); see searchSessionProducer.
//
// 5: additionally, an exact-name agent answer (search_exact_name.go) is never stored for replay.
// A schema-4 file may hold one, written by a build that recorded it, whose omission line invites a
// phrase search that the replay would then answer with the same names-only payload.
const (
	searchSessionReplaySchema = 5
	// searchSessionReplaySchemaVerifyOmitted is the schema of a payload rendered with
	// `--verify-block off`. PRE-FIX COMPOSITION: 306 numbered the off mode 4, which collides with
	// 297's producer-bound schema 4. Repaired in the following schema-5 commit.
	searchSessionReplaySchemaVerifyOmitted = 4
	// A normal search payload is budgeted in kilobytes. Keep a generous ceiling for callers that
	// deliberately widen it, but never let an untrusted/stale session file allocate without bound.
	maxSearchSessionStateBytes = 8 << 20
)

// searchSessionState is the whole persisted record: how many searches ran, the first one's
// question and answer, the repository/tree and render format that answer describes, and the corpus
// policy that admitted every path contributing to it.
type searchSessionState struct {
	Searches          int      `json:"searches"`
	Query             string   `json:"query"`
	Payload           string   `json:"payload"`
	ReplaySchema      int      `json:"replay_schema,omitempty"`
	PolicyFingerprint string   `json:"policy_fingerprint,omitempty"`
	PayloadPaths      []string `json:"payload_paths"`
	Format            string   `json:"format,omitempty"`
	// VerifyOmitted records that the payload was rendered with the VERIFY block suppressed. Absent
	// (false) is exactly what every older state file means: before the switch existed, every payload
	// was rendered with the block on.
	VerifyOmitted bool `json:"verify_omitted,omitempty"`
	// Repo and Tree are the scope the payload was recorded against. See searchSessionScope: the
	// state file is what makes an echo possible, and these are what stop it answering for the
	// wrong repository.
	Repo string `json:"repo,omitempty"`
	Tree string `json:"tree,omitempty"`
	// Producer is the binary that rendered the payload; see searchSessionProducer.
	Producer string `json:"producer,omitempty"`
}

// searchSessionScope identifies the repository view and wire format a payload describes.
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
// So the payload carries both the resolved repository path and the tree it answered for, and an
// echo only fires when both still match. HEAD's tree hash is what the record cache already keys on
// (see the RevParse pair in the provider's snapshot path), and it is stable across ordinary
// worktree edits. It is not sufficient by itself: sibling --repo subdirectories share one root
// tree while interpreting every response path in a different namespace. The render format is part
// of the identity too because the persisted payload is already-rendered opaque bytes.
//
// These observations degrade rather than fail. A repository git cannot describe still gets a
// scope from its resolved path, and an identity that cannot be computed compares equal to nothing
// — which refuses the echo and runs a real search. Every ambiguous case resolves toward answering
// the question that was asked.
type searchSessionScope struct {
	Repo              string
	Tree              string
	PolicyFingerprint string
	Format            string
	// Producer identifies the binary answering now. The payload is rendered output, so its ranking,
	// snippets and budget are the RECORDING binary's; replaying it after an upgrade serves the old
	// ranking under a header saying the question was already answered.
	Producer string
	// OmitVerify is the resolved `--verify-block off`. It is part of the identity for the same
	// reason Format is: the payload is opaque rendered bytes, so a payload recorded with VERIFY must
	// never answer an omit-verify call (it would hand the no-VERIFY arm the block) and vice versa.
	OmitVerify bool
}

// matches reports whether a recorded scope may answer for the live one.
//
// A state file written before these fields existed has an incomplete scope, and that is treated as
// a mismatch rather than a wildcard: an unscoped payload is exactly the one that might belong to
// another repository or wire format. The cost of being wrong is one real search; the cost of the
// wildcard is serving opaque bytes under the wrong identity.
func (recorded searchSessionState) matches(live searchSessionScope) bool {
	if recorded.ReplaySchema != searchSessionReplaySchemaFor(live.OmitVerify) ||
		recorded.PolicyFingerprint == "" ||
		live.PolicyFingerprint == "" ||
		recorded.PolicyFingerprint != live.PolicyFingerprint ||
		recorded.Format == "" ||
		live.Format == "" ||
		recorded.Format != live.Format ||
		recorded.Producer == "" ||
		live.Producer == "" ||
		recorded.Producer != live.Producer ||
		recorded.VerifyOmitted != live.OmitVerify {
		return false
	}
	// The tree hash alone is not a repository identity: sibling --repo subdirectories share the
	// same root tree while interpreting every response path relative to different directories.
	if recorded.Repo == "" || live.Repo == "" || recorded.Repo != live.Repo {
		return false
	}
	if recorded.Tree != "" || live.Tree != "" {
		return recorded.Tree == live.Tree
	}
	return true
}

// searchSessionReplaySchemaFor is the replay schema a payload rendered in this VERIFY mode is
// recorded under and must match. See searchSessionReplaySchemaVerifyOmitted.
func searchSessionReplaySchemaFor(omitVerify bool) int {
	if omitVerify {
		return searchSessionReplaySchemaVerifyOmitted
	}
	return searchSessionReplaySchema
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
	// Nil means the JSON member was absent or null. A genuine zero-path response is recorded as an
	// explicit empty array, so omission cannot masquerade as complete provenance and bypass the path
	// gate for a non-empty opaque payload. Return the state so the caller replaces it after the live
	// search instead of leaving an unreplayable payload stuck forever.
	if state.PayloadPaths == nil {
		return state, false
	}
	return state, true
}

func (s *searchSession) load() (searchSessionState, error) {
	var state searchSessionState
	root, err := os.OpenRoot(filepath.Dir(s.path))
	if err != nil {
		return searchSessionState{}, err
	}
	defer root.Close()
	name := filepath.Base(s.path)
	info, err := root.Lstat(name)
	if err != nil {
		return searchSessionState{}, err
	}
	if !info.Mode().IsRegular() {
		return searchSessionState{}, fmt.Errorf("search session %q is not a regular file", s.path)
	}
	if info.Size() > maxSearchSessionStateBytes {
		return searchSessionState{}, fmt.Errorf("search session %q exceeds %d bytes", s.path, maxSearchSessionStateBytes)
	}
	file, err := root.Open(name)
	if err != nil {
		return searchSessionState{}, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return searchSessionState{}, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return searchSessionState{}, fmt.Errorf("search session %q changed while opening", s.path)
	}
	if openedInfo.Size() > maxSearchSessionStateBytes {
		return searchSessionState{}, fmt.Errorf("search session %q exceeds %d bytes", s.path, maxSearchSessionStateBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSearchSessionStateBytes+1))
	if err != nil {
		return searchSessionState{}, err
	}
	if len(data) > maxSearchSessionStateBytes {
		return searchSessionState{}, fmt.Errorf("search session %q exceeds %d bytes", s.path, maxSearchSessionStateBytes)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return searchSessionState{}, err
	}
	if state.Searches < 0 {
		return searchSessionState{}, fmt.Errorf("search session %q has a negative search count", s.path)
	}
	if state.PayloadPaths != nil {
		if err := sem.ValidateSearchReplayPaths(state.PayloadPaths); err != nil {
			return searchSessionState{}, err
		}
	}
	return state, nil
}

// record counts a search that actually ran and, when replayable is true, keeps the FIRST payload:
// the echo replays the answer the ranking gave the original question, not the last rephrasing of
// it. Counting is separate from storing bytes so a response that is too large or whose policy
// changed mid-search cannot leave a rejected old payload stuck on disk. forceReplace is set when a
// matching state failed its path-policy check or cannot fit the current agent byte budget.
// Persisting is best-effort for the same reason echo is
// — a session file that cannot be written costs the cap, not the search.
func (s *searchSession) record(
	query string,
	payload []byte,
	payloadPaths []string,
	live searchSessionScope,
	forceReplace bool,
	replayable bool,
) {
	state, _ := s.load()
	// A state file that belongs to another tree is replaced rather than counted into: its search
	// count describes a different task, and carrying it over would cap this one before it ran.
	if forceReplace || !state.matches(live) {
		state = searchSessionState{}
	}
	if state.Searches < math.MaxInt {
		state.Searches++
	}
	state.ReplaySchema = searchSessionReplaySchemaFor(live.OmitVerify)
	state.PolicyFingerprint = live.PolicyFingerprint
	state.Repo, state.Tree, state.Format = live.Repo, live.Tree, live.Format
	state.Producer = live.Producer
	state.VerifyOmitted = live.OmitVerify
	if state.Payload == "" && replayable && len(payload) <= maxSearchSessionStateBytes {
		state.Query = query
		state.Payload = string(payload)
		state.PayloadPaths = append([]string{}, payloadPaths...)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	if len(data) > maxSearchSessionStateBytes {
		// Preserve the bounded count/scope, but never a payload that cannot itself fit inside the
		// state ceiling. A later safe search can populate the empty replay slot.
		state.Query = ""
		state.Payload = ""
		state.PayloadPaths = nil
		data, err = json.Marshal(state)
		if err != nil || len(data) > maxSearchSessionStateBytes {
			return
		}
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

// searchReplayFitsByteBudget measures the entire terminal-safe replay, including attribution.
// Count emitted bytes, not source bytes: persisted input can expand during control escaping.
// An incompatible opaque response must be replaced, never truncated through source markers.
func searchReplayFitsByteBudget(state searchSessionState, asked string, budget int) bool {
	if budget <= 0 {
		return true
	}
	payload := []byte(searchEchoHeader(asked, state.Query) + state.Payload)
	return len(termsafe.Bytes(payload)) <= budget
}

// searchEchoHeader is the one line that precedes a replayed payload. It names both questions — the
// one that was not run, and the one the bytes below actually answer, so the agent is not left
// reading a payload as if it were a reply to the query it just typed. Then it stops: no alternative
// verb, no invitation to rephrase.
func searchEchoHeader(asked, answered string) string {
	return fmt.Sprintf("(one search per task: %q was not run — below is your first search %q, verbatim)\n", asked, answered)
}

// searchSessionBuildInfo reads the running binary's build metadata. A package variable so the test
// package can install an identifiable default (the test binary itself carries no VCS stamp).
var searchSessionBuildInfo = debug.ReadBuildInfo

// searchSessionProducerFrom identifies a binary for replay from its OWN build metadata only: a VCS
// revision stamped from an UNMODIFIED tree, "rev:<revision>". Anything else has no identity, which
// matches nothing, so the binary answers every question for real.
//
// Deliberately narrow. The CLI's version string is not used: Run turns an empty version into "dev",
// so unstamped development binaries all reported "dev" and replayed each other's output. A module
// version is not used either: two modules (or forks) can share a tag, and a binary built through a
// local `replace` keeps the same module path and version while its source — and its bytes — change.
// Anything but an explicit vcs.modified="false" is refused: two different modified builds of one
// revision are indistinguishable. This is a source-revision identity, not a binary attestation:
// builds of one clean revision with different flags or toolchains share it. A clean checkout
// built by scripts/release.sh carries the stamp; `go install module@version` binaries do not and
// never replay (a deliberate capability tradeoff).
func searchSessionProducerFrom(info *debug.BuildInfo, ok bool) string {
	if !ok || info == nil {
		return ""
	}
	revision, clean := "", false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			// Only the literal "false" is clean: "true", an empty value, or any other text is not.
			clean = setting.Value == "false"
		}
	}
	if !clean || revision == "" {
		return ""
	}
	return "rev:" + revision
}

// sessionProducer is the replay identity of the binary serving opts.
func (opts Options) sessionProducer() string {
	read := searchSessionBuildInfo
	if opts.buildInfo != nil {
		read = opts.buildInfo
	}
	return searchSessionProducerFrom(read())
}
