package cli

import (
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// Every lookup verb answers an exactly resolved name from a focused subgraph,
// so it never decodes the unrelated rest of the repository, and answers an
// exact miss from the whole snapshot, because the fuzzy ladder it falls to
// ranks over every symbol. The equivalence guard
// (TestLookupsAnswerIdenticallyFromSidecarAndFullLoad) pins that both answer
// the same; this pins which graph each one reads.
func TestLookupVerbsReadTheWholeGraphOnlyForAnExactMiss(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_SEARCH_STORE", "1")
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "widget/widget.go", "package widget\n\ntype Widget struct{ size int }\n\n"+
		"func (w *Widget) Grow() { w.size = double(w.size) }\n\nfunc double(n int) int { return 2 * n }\n")
	write(t, repo, "audit/audit.go", "package audit\n\nfunc Unrelated() int { return 1 }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	cacheDir := t.TempDir()
	runVerb(t, repo, cacheDir, []string{"index", "--head", "--cache-dir", cacheDir, "--format", "json"})

	options := sem.ProviderSnapshotOptions{NoNetwork: true, Profile: sem.ProfileFull}
	load := func(lookup lookupFocus) sem.ProviderSnapshot {
		t.Helper()
		snapshot, _, err := loadLookupSnapshot(t.Context(), repo, "test-version", options, cacheDir, false, lookup)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	holdsUnrelated := func(snapshot sem.ProviderSnapshot) bool {
		for _, symbol := range snapshot.Symbols {
			if symbol.Name == "Unrelated" {
				return true
			}
		}
		return false
	}

	verbs := []struct {
		name   string
		lookup func(symbol string) lookupFocus
		// fuzzy is false for a verb without a fuzzy ladder.
		fuzzy bool
	}{
		{"neighbors", func(symbol string) lookupFocus {
			return neighborLookupFocus(neighborFlags{Symbol: symbol, Relation: "CALLS", Direction: "both", Depth: 2})
		}, true},
		{"impact", func(symbol string) lookupFocus {
			return impactLookupFocus(impactFlags{Symbol: symbol, Depth: 2})
		}, true},
		{"def", func(symbol string) lookupFocus {
			return defLookupFocus(defFlags{}, []string{symbol})
		}, true},
		{"explain", func(symbol string) lookupFocus {
			return explainLookupFocus([]explainCandidate{{Name: symbol}})
		}, false},
	}
	for _, verb := range verbs {
		t.Run(verb.name, func(t *testing.T) {
			exact := load(verb.lookup("Grow"))
			if len(exact.Symbols) == 0 {
				t.Fatal("the exact lookup loaded no symbols")
			}
			if holdsUnrelated(exact) {
				t.Error("an exactly resolved name loaded an unrelated file's symbols")
			}
			if verb.fuzzy && !holdsUnrelated(load(verb.lookup("Groww"))) {
				t.Error("an exact miss did not reload the whole graph for the fuzzy ladder")
			}
		})
	}
}
