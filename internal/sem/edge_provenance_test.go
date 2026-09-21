package sem

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every relation already carried the evidence for how it was established.
// What it lacked was the conclusion, as one field a caller can filter on. These
// tests hold that label to the two things that make it worth trusting: it never
// promotes a guess to a fact, and it cannot quietly go stale as the providers
// change underneath it.

func TestEdgeProvenanceNeverPromotesAGuessToAFact(t *testing.T) {
	cases := []struct {
		relation   string
		resolution string
		want       string
		why        string
	}{
		{"CALLS", "exact", ProvenanceExtracted, "a resolved call is a resolved call"},
		{"IMPORTS", "import_resolved", ProvenanceExtracted, "the import graph identified the target"},
		{"CALLS", "type_inferred", ProvenanceInferred, "a definite target reached by inference is not a direct reference"},
		{"CALLS", "name_only", ProvenanceAmbiguous, "a bare name is not a target"},
		{"CALLS", "import_external", ProvenanceAmbiguous, "the definition is outside this repository"},
		{"CALLS", "", ProvenanceAmbiguous, "no stated resolution is not evidence"},
		{"CALLS", "a_value_invented_later", ProvenanceAmbiguous, "an unknown resolution must fail toward ambiguous"},

		// The judgement this label exists for: a heuristic edge is INFERRED
		// even when its resolution reads exact. HANDLES_ROUTE exists because a
		// route string matched; calling it EXTRACTED would promote a pattern
		// match to a resolved reference.
		{"HANDLES_ROUTE", "exact", ProvenanceInferred, "a route match is a pattern, however exact the target"},
		{"HTTP_CALLS", "exact", ProvenanceInferred, "shape-matched HTTP calls are patterns"},
		{"TESTS", "exact", ProvenanceInferred, "test association is inferred"},
		{"SIMILAR_TO", "full", ProvenanceInferred, "similarity is never extraction"},
	}
	for _, tc := range cases {
		if got := EdgeProvenance(tc.relation, tc.resolution); got != tc.want {
			t.Fatalf("%s/%s = %s, want %s — %s", tc.relation, tc.resolution, got, tc.want, tc.why)
		}
	}
}

// The heuristic list here decides whether an edge reads INFERRED. The same list
// is advertised to callers as HeuristicRelationTypes. If they drift, the label
// and the documented capability disagree about the same edge — so they are
// asserted equal rather than maintained in parallel by hand.
func TestHeuristicListMatchesTheAdvertisedCapability(t *testing.T) {
	advertised := Capabilities().HeuristicRelationTypes
	if len(advertised) == 0 {
		t.Fatal("capabilities advertise no heuristic relation types")
	}
	local := make([]string, 0, len(heuristicEdgeTypes))
	for name := range heuristicEdgeTypes {
		local = append(local, name)
	}
	sort.Strings(local)
	shipped := append([]string(nil), advertised...)
	sort.Strings(shipped)
	if strings.Join(local, ",") != strings.Join(shipped, ",") {
		t.Fatalf("provenance heuristics and advertised capability disagree:\n  provenance: %v\n  advertised: %v", local, shipped)
	}
}

// A resolution value added to any language backend must be classified here
// deliberately. Without this test a new value inherits AMBIGUOUS by falling off
// the end of a lookup, which is how a trust label stops being trustworthy: it
// would keep answering, and keep being wrong, with nothing failing.
func TestEveryResolutionValueInTheTreeIsClassified(t *testing.T) {
	pattern := regexp.MustCompile(`Resolution:\s*"([a-z_]+)"`)
	found := map[string]bool{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(".", entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(data), -1) {
			found[match[1]] = true
		}
	}
	if len(found) == 0 {
		t.Fatal("found no Resolution values at all; this test has stopped testing anything")
	}
	var unclassified []string
	for value := range found {
		if _, ok := knownResolutions[value]; !ok {
			unclassified = append(unclassified, value)
		}
	}
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Fatalf("resolution values emitted but not classified in knownResolutions: %v\n"+
			"Classify each deliberately — inheriting AMBIGUOUS by default is how this label goes stale.", unclassified)
	}
}

func TestProvenanceValuesAreTheThreeDocumentedOnes(t *testing.T) {
	// A consumer filters on these strings. Adding a fourth silently would break
	// every filter that enumerates them.
	allowed := map[string]bool{ProvenanceExtracted: true, ProvenanceInferred: true, ProvenanceAmbiguous: true}
	for _, provenance := range knownResolutions {
		if !allowed[provenance] {
			t.Fatalf("knownResolutions maps to %q, which is not one of the three documented labels", provenance)
		}
	}
	for _, resolution := range []string{"exact", "name_only", "", "nonsense"} {
		for _, relation := range []string{"CALLS", "HANDLES_ROUTE", "DEFINES"} {
			if got := EdgeProvenance(relation, resolution); !allowed[got] {
				t.Fatalf("EdgeProvenance(%q,%q) = %q, outside the documented set", relation, resolution, got)
			}
		}
	}
}
