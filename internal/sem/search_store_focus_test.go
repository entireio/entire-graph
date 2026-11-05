package sem

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type focusFixture struct {
	repo, cacheDir, version string
	options                 ProviderSnapshotOptions
}

// indexFocusFixture commits the multilang-relations fixture and indexes it
// with the sqlite sidecar, the state a focused lookup reads.
func indexFocusFixture(t *testing.T) focusFixture {
	t.Helper()
	t.Setenv(searchStoreEnv, "1")
	repo := filepath.Join(t.TempDir(), "multilang-relations")
	copyFixtureTree(t, filepath.Join("testdata", "fixtures", "multilang-relations"), repo)
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.name", "Focus Test")
	git(t, repo, "config", "user.email", "focus@example.invalid")
	git(t, repo, "add", "-A")
	git(t, repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "fixture")
	fixture := focusFixture{
		repo: repo, cacheDir: t.TempDir(), version: "focus-test",
		options: ProviderSnapshotOptions{NoNetwork: true, Profile: ProfileFull},
	}
	if _, _, err := PreindexProviderSnapshot(t.Context(), fixture.repo, fixture.version, fixture.options, fixture.cacheDir); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// leafCallsFocus asks for `grow` (buffer.c), a leaf that only resize calls,
// and its CALLS one hop out.
var leafCallsFocus = SnapshotFocus{
	Names: []string{"grow"},
	Hops:  []FocusHop{{Types: []string{"CALLS"}, In: true, Out: true}},
}

func loadLeafFocus(t *testing.T, fixture focusFixture) ProviderSnapshot {
	t.Helper()
	snapshot, focused, _, err := LoadFocusedProviderSnapshot(t.Context(), fixture.repo, fixture.version,
		fixture.options, fixture.cacheDir, false, leafCallsFocus)
	if err != nil {
		t.Fatal(err)
	}
	if !focused {
		t.Fatal("an exact sidecar was present, but the full snapshot was returned")
	}
	return snapshot
}

func hasSymbol(snapshot ProviderSnapshot, qualifiedName, filePath string) bool {
	for _, symbol := range snapshot.Symbols {
		if symbol.QualifiedName == qualifiedName && symbol.FilePath == filePath {
			return true
		}
	}
	return false
}

// Break caught: the loader silently returning everything.
func TestFocusedSnapshotNeverLoadsUnrelatedSymbols(t *testing.T) {
	snapshot := loadLeafFocus(t, indexFocusFixture(t))
	if !hasSymbol(snapshot, "grow", "buffer.c") {
		t.Fatal("the focus itself is missing from the focused snapshot")
	}
	if hasSymbol(snapshot, "Router.dispatch", "Router.scala") {
		t.Fatal("a symbol from an unrelated file was loaded")
	}
}

// Break caught: a re-sort, a wrong rowid-order assumption, or header counts
// taken from the subset rather than the whole snapshot.
func TestFocusedSnapshotIsAnOrderedSubsetOfTheFull(t *testing.T) {
	fixture := indexFocusFixture(t)
	focused := loadLeafFocus(t, fixture)
	full, _, err := LoadOrBuildProviderSnapshot(t.Context(), fixture.repo, fixture.version, fixture.options, fixture.cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(focused.Relations) == 0 {
		t.Fatal("the focus's CALLS edge is missing, so the subsequence checks would hold vacuously")
	}
	if !isSubsequence(focused.Symbols, full.Symbols) {
		t.Error("focused Symbols are not a subsequence of the full Symbols")
	}
	if !isSubsequence(focused.Relations, full.Relations) {
		t.Error("focused Relations are not a subsequence of the full Relations")
	}
	if !isSubsequence(focused.Files, full.Files) {
		t.Error("focused Files are not a subsequence of the full Files")
	}
	if !reflect.DeepEqual(focused.Header.Stats, full.Header.Stats) {
		t.Errorf("Stats differ:\nfocused %+v\nfull    %+v", focused.Header.Stats, full.Header.Stats)
	}
	if !reflect.DeepEqual(focused.Header.Completeness, full.Header.Completeness) {
		t.Errorf("Completeness differ:\nfocused %+v\nfull    %+v", focused.Header.Completeness, full.Header.Completeness)
	}
}

func isSubsequence[T any](sub, full []T) bool {
	next := 0
	for index := range full {
		if next < len(sub) && reflect.DeepEqual(sub[next], full[index]) {
			next++
		}
	}
	return next == len(sub)
}

// --file and --kind narrow the starting set the way exact resolution narrows
// its matches, and exact resolution takes a symbol named by its ID ahead of
// every filter, so a symbol named by ID stays whatever the filters say.
// Break caught: the filter vetoing an ID match, which sends every lookup by ID
// with a stale --file to the full load.
func TestFocusFileFilterKeepsASymbolNamedByID(t *testing.T) {
	fixture := indexFocusFixture(t)
	full, _, err := LoadOrBuildProviderSnapshot(t.Context(), fixture.repo, fixture.version, fixture.options, fixture.cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, symbol := range full.Symbols {
		if symbol.QualifiedName == "grow" && symbol.FilePath == "buffer.c" {
			id = symbol.ID
		}
	}
	if id == "" {
		t.Fatal("the fixture has no grow in buffer.c")
	}
	snapshot, focused, _, err := LoadFocusedProviderSnapshot(t.Context(), fixture.repo, fixture.version,
		fixture.options, fixture.cacheDir, false, SnapshotFocus{Names: []string{id}, File: "Router.scala"})
	if err != nil {
		t.Fatal(err)
	}
	if !focused {
		t.Fatal("an exact sidecar was present, but the full snapshot was returned")
	}
	if !hasSymbol(snapshot, "grow", "buffer.c") {
		t.Fatal("the symbol named by its ID was dropped by a --file filter it does not meet")
	}
}

// Break caught: the filter not narrowing at all, so impact walks the callers
// of every same-named symbol in the repository.
func TestFocusFileFilterDropsANameMatchInAnotherFile(t *testing.T) {
	fixture := indexFocusFixture(t)
	snapshot, focused, _, err := LoadFocusedProviderSnapshot(t.Context(), fixture.repo, fixture.version,
		fixture.options, fixture.cacheDir, false, SnapshotFocus{Names: []string{"grow"}, File: "Router.scala"})
	if err != nil {
		t.Fatal(err)
	}
	if !focused {
		t.Fatal("an exact sidecar was present, but the full snapshot was returned")
	}
	if hasSymbol(snapshot, "grow", "buffer.c") {
		t.Fatal("grow in buffer.c was loaded although --file names Router.scala")
	}
}

// A terminal hop's relations are retained, but the next hop expands the same
// frontier again rather than the symbols the terminal hop reached: impact
// reads the focus's own type edges, yet walks a second hop only from callers.
func TestTerminalHopIsNotExpanded(t *testing.T) {
	fixture := indexFocusFixture(t)
	resizeParamType := func(terminal bool) bool {
		t.Helper()
		snapshot, focused, _, err := LoadFocusedProviderSnapshot(t.Context(), fixture.repo, fixture.version,
			fixture.options, fixture.cacheDir, false, SnapshotFocus{
				Names: []string{"grow"},
				Hops: []FocusHop{
					{Types: []string{"CALLS"}, In: true, Terminal: terminal},
					{Types: []string{"PARAM_TYPE"}, Out: true},
				},
			})
		if err != nil {
			t.Fatal(err)
		}
		if !focused {
			t.Fatal("an exact sidecar was present, but the full snapshot was returned")
		}
		for _, relation := range snapshot.Relations {
			if relation.Type == "PARAM_TYPE" && strings.HasSuffix(relation.FromID, ":function:resize") {
				return true
			}
		}
		return false
	}
	if !resizeParamType(false) {
		t.Fatal("the caller's PARAM_TYPE edge is missing after an expanding hop, so the terminal check would hold vacuously")
	}
	if resizeParamType(true) {
		t.Error("a terminal hop's endpoint (the caller resize) was expanded by the next hop")
	}
}
