package sem

import (
	"reflect"
	"strings"
	"testing"
)

func selectiveFilterFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	// alpha.Run calls the package-local helper in alpha/helper.go. beta declares
	// a same-named helper: the complete graph resolves Run's call by package, but
	// a build that cannot see alpha/helper.go has only beta's left to guess with.
	write(t, repo, "alpha/run.go", `package alpha

import "fmt"

// Run frobnicates the ledger by delegating to the package helper.
func Run() string {
	fmt.Println("run")
	return helper() + Format()
}
`)
	write(t, repo, "alpha/helper.go", `package alpha

func helper() string { return "alpha" }
`)
	write(t, repo, "alpha/format.go", `package alpha

// Format renders the ledger entry for Run.
func Format() string { return "ledger" }
`)
	write(t, repo, "beta/helper.go", `package beta

func helper() string { return "beta" }

// Audit checks the ledger after Run.
func Audit() string { return helper() }
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

func relationKey(relation RelationRecord) string {
	return relation.Type + "|" + relation.FromID + "|" + relation.ToID
}

// TestSelectiveDerivationFiltersCompleteGraph pins what the warm selective view
// is: the complete graph's relations filtered to the selection, never a re-run
// of relation resolution against the selection alone.
//
// It holds both directions of selectiveSearchSnapshotFromFull's contract against
// a direct OnlyFiles extraction of the same selection:
//   - nothing is ADDED: every derived relation exists in the direct build;
//   - only the documented classes are DROPPED: edges a selection-only resolver
//     had to externalize or guess, never an exact edge between selected symbols.
//
// And it holds the reason the filter is not merely cheaper: the direct build
// resolves alpha.Run's helper() call to beta's same-named decoy, because the
// real target is outside the selection; the complete graph had resolved it to
// alpha's own helper, so the derived view carries no edge to the decoy.
func TestSelectiveDerivationFiltersCompleteGraph(t *testing.T) {
	repo := selectiveFilterFixture(t)
	full, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFull})
	if err != nil {
		t.Fatal(err)
	}
	options := ProviderSnapshotOptions{
		Profile:   ProfileFull,
		OnlyFiles: []string{"alpha/run.go", "alpha/format.go", "beta/helper.go"},
	}
	derived, err := selectiveSearchSnapshotFromFull(t.Context(), repo, "test-version", options, full)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(derived.Files, direct.Files) || !reflect.DeepEqual(derived.Symbols, direct.Symbols) {
		t.Fatalf("derived view selected different records:\nderived files=%v symbols=%v\ndirect files=%v symbols=%v",
			derived.Files, derived.Symbols, direct.Files, direct.Symbols)
	}
	if !reflect.DeepEqual(derived.Header.PartialFailures, direct.Header.PartialFailures) {
		t.Fatalf("derived failures %v, direct %v", derived.Header.PartialFailures, direct.Header.PartialFailures)
	}
	symbolIDs := map[string]string{}
	for _, symbol := range full.Symbols {
		symbolIDs[symbol.FilePath+"#"+symbol.Name] = symbol.ID
	}
	run, alphaHelper, betaHelper, format := symbolIDs["alpha/run.go#Run"], symbolIDs["alpha/helper.go#helper"], symbolIDs["beta/helper.go#helper"], symbolIDs["alpha/format.go#Format"]
	if run == "" || alphaHelper == "" || betaHelper == "" || format == "" {
		t.Fatalf("fixture symbols missing: %v", symbolIDs)
	}
	callsTo := func(snapshot ProviderSnapshot, from, to string) bool {
		for _, relation := range snapshot.Relations {
			if relation.Type == "CALLS" && relation.FromID == from && relation.ToID == to {
				return true
			}
		}
		return false
	}
	// Premises: the complete graph resolves the call to alpha's helper, and the
	// selection-only resolver, unable to see it, guesses beta's.
	if !callsTo(full, run, alphaHelper) || callsTo(full, run, betaHelper) {
		t.Fatalf("premise lost: complete graph no longer resolves Run -> alpha.helper exactly: %v", full.Relations)
	}
	if !callsTo(direct, run, betaHelper) {
		t.Fatalf("premise lost: a selection-only build no longer guesses beta.helper; this test no longer discriminates filter from re-extraction: %v", direct.Relations)
	}
	if callsTo(derived, run, betaHelper) {
		t.Fatal("derived view re-resolved the call against the selection and kept the decoy edge Run -> beta.helper")
	}
	if !callsTo(derived, run, format) {
		t.Fatal("derived view lost the exact edge between two selected symbols Run -> Format")
	}

	directByKey := map[string]RelationRecord{}
	for _, relation := range direct.Relations {
		directByKey[relationKey(relation)] = relation
	}
	derivedByKey := map[string]RelationRecord{}
	for _, relation := range derived.Relations {
		derivedByKey[relationKey(relation)] = relation
		if _, ok := directByKey[relationKey(relation)]; !ok {
			t.Errorf("derived view ADDED a relation a direct selective build lacks: %#v", relation)
		}
	}
	fullKeys := map[string]bool{}
	for _, relation := range full.Relations {
		fullKeys[relationKey(relation)] = true
	}
	keptExternal := false
	for key, relation := range directByKey {
		if _, ok := derivedByKey[key]; ok {
			keptExternal = keptExternal || strings.HasPrefix(relation.ToID, "external:")
			continue
		}
		if fullKeys[key] {
			t.Errorf("derived view dropped a relation BOTH graphs agree on: %#v", relation)
		}
		externalized := strings.HasPrefix(relation.ToID, "external:")
		guessed := relation.Resolution == "name_only" || relation.Resolution == "pattern"
		if !externalized && !guessed {
			t.Errorf("derived view dropped a relation outside the documented classes: %#v", relation)
		}
	}
	if !keptExternal {
		t.Error("derived view kept no external edge, though both graphs externalize Run's fmt.Println call")
	}
	if derived.Header.Stats.Relations != len(derived.Relations) {
		t.Fatalf("stats.relations = %d, want %d", derived.Header.Stats.Relations, len(derived.Relations))
	}
}

// TestSelectiveDerivationRanksLikeColdSearch is the ranking half of the filter's
// contract: on fixture queries whose cold and preindexed preselections choose the
// same files, the warm (filtered) and cold (directly extracted) searches return
// identical ranked results.
func TestSelectiveDerivationRanksLikeColdSearch(t *testing.T) {
	repo := selectiveFilterFixture(t)
	cacheDir := t.TempDir()
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFull}, cacheDir); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"Format ledger entry", "Audit ledger", "frobnicates ledger Format"} {
		base := SearchOptions{Profile: ProfileFull, TopK: 10, MaxIndexedFiles: 3}
		cold := base
		cold.DisableCache = true
		coldResponse, err := SearchRepository(t.Context(), repo, "test-version", query, cold)
		if err != nil {
			t.Fatal(err)
		}
		warm := base
		warm.CacheDir = cacheDir
		warmResponse, err := SearchRepository(t.Context(), repo, "test-version", query, warm)
		if err != nil {
			t.Fatal(err)
		}
		if !warmResponse.Stats.IndexCacheHit {
			t.Fatalf("%q: warm search did not derive from the preindex", query)
		}
		if warmResponse.Stats.FilesIndexed != coldResponse.Stats.FilesIndexed || warmResponse.Stats.FilesIndexed >= 4 {
			t.Fatalf("%q: premise lost: warm indexed %d files, cold %d; the selections must match and be selective",
				query, warmResponse.Stats.FilesIndexed, coldResponse.Stats.FilesIndexed)
		}
		if len(coldResponse.Results) == 0 {
			t.Fatalf("%q: cold search found nothing; the query no longer exercises ranking", query)
		}
		if !reflect.DeepEqual(warmResponse.Results, coldResponse.Results) {
			t.Fatalf("%q: filtered derivation changed ranked results:\nwarm=%#v\ncold=%#v", query, warmResponse.Results, coldResponse.Results)
		}
	}
}

// TestSelectiveDerivationRecountsUnmergedDataFlowEdges pins that the filtered
// view keeps the W_DATA_FLOW_EVIDENCE_UNMERGED disclosure for exactly the edges
// it retains, as a direct build of the selection reports it. The complete
// graph's own warning counts the whole tree; copying it would overstate, and
// dropping it hides one-sided evidence on edges the view still serves.
func TestSelectiveDerivationRecountsUnmergedDataFlowEdges(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "flow/selected.go", `package flow

func alpha(a int) int {
	return bravo(a)
}

func bravo(b int) int {
	value := alpha(b)
	return value
}
`)
	write(t, repo, "flow/unselected.go", `package flow

func charlie(c int) int {
	return delta(c)
}

func delta(d int) int {
	value := charlie(d)
	return value
}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	full, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFull})
	if err != nil {
		t.Fatal(err)
	}
	if got := unmergedWarningDetail(full.Header.Warnings); !strings.HasPrefix(got, "4 DATA_FLOWS edge(s)") {
		t.Fatalf("premise lost: complete graph unmerged warning = %q, want 4 edges", got)
	}
	options := ProviderSnapshotOptions{Profile: ProfileFull, OnlyFiles: []string{"flow/selected.go"}}
	direct, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", options)
	if err != nil {
		t.Fatal(err)
	}
	// Derive from the in-memory snapshot and from a cache round trip: both
	// routes serve warm queries.
	cacheDir := t.TempDir()
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFull}, cacheDir); err != nil {
		t.Fatal(err)
	}
	cached, hit, err := loadCachedCompleteSearchSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFull}, cacheDir)
	if err != nil || !hit {
		t.Fatalf("complete snapshot not cached: hit=%v err=%v", hit, err)
	}
	for name, source := range map[string]ProviderSnapshot{"in-memory": full, "cached": cached} {
		derived, err := selectiveSearchSnapshotFromFull(t.Context(), repo, "test-version", options, source)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(derived.Header.Warnings, direct.Header.Warnings) {
			t.Fatalf("%s: derived warnings %v, want the direct build's %v", name, derived.Header.Warnings, direct.Header.Warnings)
		}
		if got := unmergedWarningDetail(derived.Header.Warnings); !strings.HasPrefix(got, "2 DATA_FLOWS edge(s)") {
			t.Fatalf("%s: derived unmerged warning = %q, want the 2 retained edges", name, got)
		}
	}
}

func unmergedWarningDetail(warnings []ProviderWarning) string {
	for _, warning := range warnings {
		if warning.Code == "W_DATA_FLOW_EVIDENCE_UNMERGED" {
			return warning.Detail
		}
	}
	return ""
}
