package cli

import "testing"

// TestGraphVerbsCoverEveryCommand is the drift guard on stats.go's classification table.
//
// stats reads a transcript and sorts every tool call into "graph call" or "exploration call".
// A command the binary dispatches but graphVerbs does not list falls into NEITHER bucket: it
// is silently dropped, so the reported adoption rate is lower than the real one and the gap
// is invisible. Five shipped commands — def, explain, verify, health, snapshot-query — were
// missing this way while graph adoption was being diagnosed from these very numbers.
//
// The list being wrong once is a bug; the list being maintainable only by memory is the
// defect. commandDocs is the authoritative registry (TestRegistryMatchesDispatch already pins
// it to root.go's dispatch switch), so adding a command without classifying it fails here.
func TestGraphVerbsCoverEveryCommand(t *testing.T) {
	t.Parallel()

	documented := map[string]bool{}
	for _, doc := range commandDocs {
		documented[doc.name] = true
		if !graphVerbs[doc.name] {
			t.Errorf("command %q is dispatched but missing from graphVerbs: calls to it count as "+
				"neither graph nor exploration, so its usage is invisible in stats", doc.name)
		}
	}

	for verb := range graphVerbs {
		if !documented[verb] {
			t.Errorf("graphVerbs lists %q, which is not a command this binary dispatches (stale entry)", verb)
		}
	}

	// Locate verbs are the credited subset, so they must be real verbs too. This does not
	// assert which verbs are credited — that is a savings-model question, deliberately left
	// alone here — only that the subset cannot name something the classifier will never see.
	for verb := range graphLocateVerbs {
		if !graphVerbs[verb] {
			t.Errorf("graphLocateVerbs credits %q, which graphVerbs does not recognise", verb)
		}
	}
}
