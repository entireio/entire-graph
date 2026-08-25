package sem

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	scippb "github.com/scip-code/scip/bindings/go/scip"
)

// TestSCIPSymbolIdentityIsStableAcrossCommits is the regression test for the
// reason the package version is not the commit.
//
// The version field used to carry header.Commit, so an unchanged symbol got a
// new SCIP identity on every commit: two indexes of the same repository one
// unrelated commit apart shared no symbol string at all, and nothing downstream
// could match a symbol across commits. Symbol identity must depend on what the
// symbol IS, not on when it was indexed.
func TestSCIPSymbolIdentityIsStableAcrossCommits(t *testing.T) {
	record := SymbolRecord{
		ID:       "compound-v1:pkg/a.go:Helper",
		Kind:     "function",
		Name:     "Helper",
		FilePath: "a.go",
	}
	first := SnapshotHeader{RepoKey: "local/demo", Commit: "d26959da4faac2613a8827bb32472c226220bd62"}
	second := SnapshotHeader{RepoKey: "local/demo", Commit: "3ec3b14828324bf7dac68ba0ff6b0206ed63fbc7"}

	one := scipSymbol(first, "1.2.3", record)
	two := scipSymbol(second, "1.2.3", record)
	if one != two {
		t.Fatalf("symbol identity changed across commits:\n  %s\n  %s", one, two)
	}
	parsed, err := scippb.ParseSymbol(one)
	if err != nil {
		t.Fatalf("ParseSymbol(%q): %v", one, err)
	}
	if got := parsed.GetPackage().GetVersion(); got != "1.2.3" {
		t.Fatalf("package version = %q, want the project version", got)
	}
	// A different project version is a different package, which is the one case
	// where the identity is SUPPOSED to change.
	if bumped := scipSymbol(first, "1.2.4", record); bumped == one {
		t.Fatalf("a version bump did not change symbol identity: %s", bumped)
	}
	// Worktree exports carry no commit at all and must still agree.
	if worktree := scipSymbol(SnapshotHeader{RepoKey: "local/demo"}, "1.2.3", record); worktree != one {
		t.Fatalf("worktree symbol identity differs from committed:\n  %s\n  %s", worktree, one)
	}
}

func TestScipProjectVersionReadsRootManifests(t *testing.T) {
	tests := []struct {
		name      string
		manifests map[string]string
		want      string
	}{
		{"package.json", map[string]string{"package.json": `{"name":"demo","version":"4.5.6"}`}, "4.5.6"},
		{"cargo", map[string]string{"Cargo.toml": "[package]\nname = \"demo\"\nversion = \"0.9.1\"\n"}, "0.9.1"},
		{"pep621", map[string]string{"pyproject.toml": "[project]\nname = \"demo\"\nversion = \"2.0.0rc1\"\n"}, "2.0.0rc1"},
		{"poetry", map[string]string{"pyproject.toml": "[tool.poetry]\nname = \"demo\"\nversion = \"3.1.4\"\n"}, "3.1.4"},
		{"package.json wins over cargo", map[string]string{
			"package.json": `{"version":"1.0.0"}`,
			"Cargo.toml":   "[package]\nversion = \"2.0.0\"\n",
		}, "1.0.0"},
		{"version in another table is ignored", map[string]string{
			"Cargo.toml": "[dependencies]\nversion = \"9.9.9\"\n[package]\nname = \"demo\"\n",
		}, ""},
		{"comment and blank lines", map[string]string{
			"Cargo.toml": "# a comment\n\n[package]\n# another\nversion = \"1.1.1\"\n",
		}, "1.1.1"},
		{"malformed json falls through", map[string]string{"package.json": "{not json"}, ""},
		{"dynamic version is not guessed", map[string]string{
			"pyproject.toml": "[project]\ndynamic = [\"version\"]\n",
		}, ""},
		{"no manifest", map[string]string{}, ""},
		{"go.mod is not consulted", map[string]string{"go.mod": "module example.com/demo\n\ngo 1.26\n"}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ScipProjectVersion(func(name string) (string, bool) {
				content, ok := test.manifests[name]
				return content, ok
			})
			if got != test.want {
				t.Fatalf("ScipProjectVersion = %q, want %q", got, test.want)
			}
		})
	}
}

func TestScipProjectVersionUnknownIsUsedWhenNoneDeclared(t *testing.T) {
	record := SymbolRecord{ID: "compound-v1:a", Kind: "function", Name: "Helper", FilePath: "a.go"}
	symbol := scipSymbol(SnapshotHeader{RepoKey: "local/demo"}, "", record)
	parsed, err := scippb.ParseSymbol(symbol)
	if err != nil {
		t.Fatalf("ParseSymbol(%q): %v", symbol, err)
	}
	if got := parsed.GetPackage().GetVersion(); got != ScipProjectVersionUnknown {
		t.Fatalf("package version = %q, want %q", got, ScipProjectVersionUnknown)
	}
}

func TestNewSCIPSnapshotEncoderDefaultsBlankProjectVersion(t *testing.T) {
	for _, blank := range []string{"", "   "} {
		if got := NewSCIPSnapshotEncoder(nil, blank).projectVersion; got != ScipProjectVersionUnknown {
			t.Fatalf("projectVersion for %q = %q, want %q", blank, got, ScipProjectVersionUnknown)
		}
	}
}

// TestSCIPNoteCountsRecordsItCouldNotCarry pins the contract that this format
// never drops input silently. A record with no usable identity cannot be keyed,
// and a symbol with no path cannot be located; both used to vanish without a
// trace, leaving an index that looked complete.
func TestSCIPNoteCountsRecordsItCouldNotCarry(t *testing.T) {
	var out bytes.Buffer
	encoder := NewSCIPSnapshotEncoder(&out, "1.0.0")
	mustEncode := func(record any) {
		t.Helper()
		if err := encoder.Encode(record); err != nil {
			t.Fatalf("Encode(%T): %v", record, err)
		}
	}
	mustEncode(SnapshotHeader{SchemaVersion: SchemaVersion, Provider: ProviderName, RepoKey: "local/demo"})
	mustEncode(FileRecord{Path: "", Language: "Go"})                   // unkeyable
	mustEncode(ExternalRecord{ID: "", Value: "orphan"})                // unkeyable
	mustEncode(SymbolRecord{ID: "", Name: "orphan", FilePath: "a.go"}) // unkeyable
	mustEncode(FileRecord{Path: "a.go", Language: "Go"})
	// Keyed, but with nowhere to live: emitted into the synthetic document.
	mustEncode(SymbolRecord{ID: "compound-v1:floating", Kind: "function", Name: "Floating", StartLine: 1, EndLine: 1})
	mustEncode(SnapshotSummary{})

	note := encoder.OmissionNote()
	if note.UnidentifiedRecords != 3 {
		t.Errorf("unidentified_records = %d, want 3", note.UnidentifiedRecords)
	}
	if note.UnlocatedSymbols != 1 {
		t.Errorf("unlocated_symbols = %d, want 1", note.UnlocatedSymbols)
	}
	// A clean stream must not carry either counter, so the JSON stays quiet
	// when there is nothing to report.
	encoded, err := json.Marshal(SCIPOmissionNote{RecordType: "scip_omissions"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "unidentified_records") || strings.Contains(string(encoded), "unlocated_symbols") {
		t.Errorf("clean note carries the counters: %s", encoded)
	}
}

// TestSCIPOmissionNoteVersionIsPinned makes a change to the note's contract
// version deliberate rather than incidental.
//
// The note is contract, not debug output: a consumer decides per language
// whether to trust the feed from it. Fields may be added within v1 on tolerant-
// reader terms, so this pins the version string itself -- renaming a field,
// removing one, or changing what one counts must fail here and bump to v2.
func TestSCIPOmissionNoteVersionIsPinned(t *testing.T) {
	note := NewSCIPSnapshotEncoder(nil, "1.0.0").OmissionNote()
	if note.Version != "entire-graph-scip-omissions/v1" {
		t.Fatalf("omission note version = %q; a change here is a contract change", note.Version)
	}
	if note.RecordType != "scip_omissions" || note.Format != "scip" {
		t.Fatalf("omission note identity changed: %#v", note)
	}
}

// TestSCIPProjectRootIsAResolvableFileURI covers the platforms CI runs on but
// whose path shapes no test asserted. url.URL takes Path verbatim, so a Windows
// path used to go out as "file://C:%5Crepo" -- drive read as authority,
// separators escaped -- which no consumer can resolve back to a directory.
func TestSCIPProjectRootIsAResolvableFileURI(t *testing.T) {
	tests := []struct{ root, want string }{
		{"", ""},
		{"/home/u/repo", "file:///home/u/repo"},
		{`C:\repo`, "file:///C:/repo"},
		{"C:/repo", "file:///C:/repo"},
		{`c:\Users\me\src`, "file:///c:/Users/me/src"},
		{`\\server\share\repo`, "file://server/share/repo"},
		{"//server/share/repo", "file://server/share/repo"},
		// Not UNC: no share component, so it stays an ordinary path.
		{"//server", "file:////server"},
	}
	for _, test := range tests {
		if got := scipProjectRoot(test.root); got != test.want {
			t.Errorf("scipProjectRoot(%q) = %q, want %q", test.root, got, test.want)
		}
	}
	// Whatever the shape, the result must parse and must not smuggle a
	// backslash through as an escaped path byte.
	for _, root := range []string{"/home/u/repo", `C:\repo`, `\\server\share\repo`} {
		parsed, err := url.Parse(scipProjectRoot(root))
		if err != nil {
			t.Errorf("scipProjectRoot(%q) is not a URL: %v", root, err)
			continue
		}
		if parsed.Scheme != "file" || strings.Contains(parsed.Path, `\`) {
			t.Errorf("scipProjectRoot(%q) -> %#v", root, parsed)
		}
	}
}

// TestSCIPNoteCarriesTiersAndFailureRecords pins the two facts SCIP itself
// cannot express: which languages were only inventoried, and which files failed
// to parse. Every discovered file becomes a Document either way, so without
// these the protobuf looks uniformly semantic.
func TestSCIPNoteCarriesTiersAndFailureRecords(t *testing.T) {
	note := SCIPOmissionNote{}
	if note.LanguageTiers != nil || note.PartialFailures != nil {
		t.Fatalf("zero note is not empty: %#v", note)
	}
	encoded, err := json.Marshal(note)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "language_tiers") || strings.Contains(string(encoded), "partial_failures") {
		t.Errorf("empty note carries the new fields: %s", encoded)
	}
	note.LanguageTiers = map[string]string{"Go": "semantic", "Zig": "inventory-only"}
	note.PartialFailures = []PartialFailure{{Code: "E_UNPARSEABLE", FilePath: "weird.zig"}}
	encoded, err = json.Marshal(note)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"inventory-only", "E_UNPARSEABLE", "weird.zig"} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("note lost %q: %s", want, encoded)
		}
	}
}
