package cli

import (
	"path/filepath"
	"testing"
)

const combinedReplayIdentityReviewSchema = 6

func combinedReplayIdentityReviewScope(producer string, omitVerify bool) searchSessionScope {
	return searchSessionScope{
		Repo:              "/review/repo",
		Tree:              "review-tree",
		PolicyFingerprint: "review-policy",
		Format:            "agent",
		Producer:          producer,
		OmitVerify:        omitVerify,
	}
}

func combinedReplayIdentityReviewState(scope searchSessionScope) searchSessionState {
	return searchSessionState{
		Searches:          1,
		Query:             "recorded query",
		Payload:           "recorded payload\n",
		PayloadPaths:      []string{},
		ReplaySchema:      combinedReplayIdentityReviewSchema,
		PolicyFingerprint: scope.PolicyFingerprint,
		Format:            scope.Format,
		Repo:              scope.Repo,
		Tree:              scope.Tree,
		Producer:          scope.Producer,
		VerifyOmitted:     scope.OmitVerify,
	}
}

func TestCombinedReplayIdentityReviewMatchesProducerAndVerifyMode(t *testing.T) {
	onA := combinedReplayIdentityReviewScope("rev:a", false)
	offA := combinedReplayIdentityReviewScope("rev:a", true)
	onB := combinedReplayIdentityReviewScope("rev:b", false)
	offB := combinedReplayIdentityReviewScope("rev:b", true)

	for _, test := range []struct {
		name     string
		recorded searchSessionState
		live     searchSessionScope
		want     bool
	}{
		{"same producer on", combinedReplayIdentityReviewState(onA), onA, true},
		{"same producer off", combinedReplayIdentityReviewState(offA), offA, true},
		{"producer a record cannot answer producer b on", combinedReplayIdentityReviewState(onA), onB, false},
		{"producer a record cannot answer producer b off", combinedReplayIdentityReviewState(offA), offB, false},
		{"on record cannot answer off", combinedReplayIdentityReviewState(onA), offA, false},
		{"off record cannot answer on", combinedReplayIdentityReviewState(offA), onA, false},
		{"empty recorded producer refuses", combinedReplayIdentityReviewState(combinedReplayIdentityReviewScope("", false)), onA, false},
		{"empty live producer refuses", combinedReplayIdentityReviewState(onA), combinedReplayIdentityReviewScope("", false), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.recorded.matches(test.live); got != test.want {
				t.Fatalf("matches = %t, want %t; recorded=%#v live=%#v", got, test.want, test.recorded, test.live)
			}
		})
	}

	// Schemas 3 and 4 had different meanings on the two parent branches, and
	// the assembled pre-VERIFY baseline used 5 without a verify-mode identity.
	// Reject every earlier value even when all newly combined fields are present
	// and internally consistent.
	for schema := 0; schema < combinedReplayIdentityReviewSchema; schema++ {
		for _, live := range []searchSessionScope{onA, offA} {
			recorded := combinedReplayIdentityReviewState(live)
			recorded.ReplaySchema = schema
			if recorded.matches(live) {
				t.Fatalf("legacy schema %d matched omitVerify=%t: %#v", schema, live.OmitVerify, recorded)
			}
		}
	}
}

func TestCombinedReplayIdentityReviewRecordEchoRearmsEveryIdentity(t *testing.T) {
	session := &searchSession{path: filepath.Join(t.TempDir(), "session.json"), limit: 1}
	identities := []struct {
		name    string
		scope   searchSessionScope
		payload string
	}{
		{"producer a on", combinedReplayIdentityReviewScope("rev:a", false), "payload-a-on\n"},
		{"producer a off", combinedReplayIdentityReviewScope("rev:a", true), "payload-a-off\n"},
		{"producer b off", combinedReplayIdentityReviewScope("rev:b", true), "payload-b-off\n"},
		{"producer b on", combinedReplayIdentityReviewScope("rev:b", false), "payload-b-on\n"},
	}

	for i, identity := range identities {
		if i > 0 {
			if _, ok := session.echo(identity.scope); ok {
				t.Fatalf("%s: prior identity echoed before re-record", identity.name)
			}
		}
		session.record(identity.name, []byte(identity.payload), []string{}, identity.scope, false, true)
		state, err := session.load()
		if err != nil {
			t.Fatalf("%s: load: %v", identity.name, err)
		}
		if state.Searches != 1 {
			t.Fatalf("%s: searches = %d, want reset count 1", identity.name, state.Searches)
		}
		if state.ReplaySchema != combinedReplayIdentityReviewSchema ||
			state.Producer != identity.scope.Producer ||
			state.VerifyOmitted != identity.scope.OmitVerify ||
			state.Payload != identity.payload {
			t.Fatalf("%s: persisted state = %#v", identity.name, state)
		}
		if echoed, ok := session.echo(identity.scope); !ok || echoed.Payload != identity.payload {
			t.Fatalf("%s: exact identity did not echo: ok=%t state=%#v", identity.name, ok, echoed)
		}

		replacement := "replacement-" + identity.payload
		session.record(identity.name+" repeated", []byte(replacement), []string{}, identity.scope, false, true)
		state, err = session.load()
		if err != nil {
			t.Fatalf("%s repeated: load: %v", identity.name, err)
		}
		if state.Searches != 2 || state.Payload != identity.payload || state.Query != identity.name {
			t.Fatalf("%s repeated: want count 2 and first payload retained, got %#v", identity.name, state)
		}
		if echoed, ok := session.echo(identity.scope); !ok || echoed.Payload != identity.payload {
			t.Fatalf("%s repeated: first payload was not replayed: ok=%t state=%#v", identity.name, ok, echoed)
		}
		for previous := 0; previous < i; previous++ {
			if _, ok := session.echo(identities[previous].scope); ok {
				t.Fatalf("%s: re-armed state still echoes prior identity %s", identity.name, identities[previous].name)
			}
		}
	}
}

func TestCombinedReplayIdentityReviewUnknownProducerNeverMatches(t *testing.T) {
	for _, mode := range []struct {
		name       string
		omitVerify bool
	}{
		{"on", false},
		{"off", true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			session := &searchSession{path: filepath.Join(t.TempDir(), "session.json"), limit: 1}
			unknown := combinedReplayIdentityReviewScope("", mode.omitVerify)
			for attempt, payload := range []string{"unknown-first\n", "unknown-second\n"} {
				session.record("unknown", []byte(payload), []string{}, unknown, false, true)
				if _, ok := session.echo(unknown); ok {
					t.Fatalf("attempt %d: empty producer replayed", attempt+1)
				}
				state, err := session.load()
				if err != nil {
					t.Fatalf("attempt %d: load: %v", attempt+1, err)
				}
				if state.Searches != 1 || state.Producer != "" || state.Payload != payload ||
					state.VerifyOmitted != mode.omitVerify {
					t.Fatalf("attempt %d: unknown identity did not reset/re-record: %#v", attempt+1, state)
				}
			}
		})
	}
}
