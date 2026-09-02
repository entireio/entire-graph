package sem

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	scippb "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
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
		{"overlong version is not a version", map[string]string{
			"package.json": `{"version":"` + strings.Repeat("9", ScipProjectVersionMaxLen+1) + `"}`,
		}, ""},
		{"version at the limit is kept", map[string]string{
			"package.json": `{"version":"` + strings.Repeat("9", ScipProjectVersionMaxLen) + `"}`,
		}, strings.Repeat("9", ScipProjectVersionMaxLen)},
		{"overlong falls through to the next manifest", map[string]string{
			"package.json": `{"version":"` + strings.Repeat("9", ScipProjectVersionMaxLen+1) + `"}`,
			"Cargo.toml":   "[package]\nversion = \"1.0.0\"\n",
		}, "1.0.0"},
		{"cargo workspace inheritance, dotted key", map[string]string{
			"Cargo.toml": "[workspace.package]\nversion = \"3.3.3\"\n\n[package]\nname = \"demo\"\nversion.workspace = true\n",
		}, "3.3.3"},
		{"cargo workspace inheritance, inline table", map[string]string{
			"Cargo.toml": "[workspace.package]\nversion = \"4.4.4\"\n\n[package]\nname = \"demo\"\nversion = { workspace = true }\n",
		}, "4.4.4"},
		{"literal package version still wins over workspace", map[string]string{
			"Cargo.toml": "[workspace.package]\nversion = \"3.3.3\"\n\n[package]\nversion = \"1.1.1\"\n",
		}, "1.1.1"},
		{"inheritance declared but workspace has no version", map[string]string{
			"Cargo.toml": "[package]\nversion.workspace = true\n",
		}, ""},
		{"workspace version present but not inherited", map[string]string{
			"Cargo.toml": "[workspace.package]\nversion = \"3.3.3\"\n\n[package]\nname = \"demo\"\n",
		}, ""},
		{"toml literal string", map[string]string{"Cargo.toml": "[package]\nversion = '2.5.0'\n"}, "2.5.0"},
		{"literal string with trailing comment", map[string]string{"Cargo.toml": "[package]\nversion = '2.5.0' # pinned\n"}, "2.5.0"},
		{"basic string with trailing comment", map[string]string{"Cargo.toml": "[package]\nversion = \"2.5.0\" # pinned\n"}, "2.5.0"},
		{"unterminated string", map[string]string{"Cargo.toml": "[package]\nversion = \"2.5.0\n"}, ""},
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

// TestSCIPSetProjectVersionRejectsAnAmplifyingVersion guards the exported
// setter independently of the source. The version is copied into every symbol,
// so an overlong one is not one bad field, it is the whole index: a 200 KB
// version across 200 symbols produced an 80 MB export before this bound.
func TestSCIPSetProjectVersionRejectsAnAmplifyingVersion(t *testing.T) {
	encoder := NewSCIPSnapshotEncoder(nil, "1.2.3")
	encoder.SetProjectVersion(strings.Repeat("9", ScipProjectVersionMaxLen+1))
	if encoder.projectVersion != "1.2.3" {
		t.Fatalf("overlong version was accepted: %d chars", len(encoder.projectVersion))
	}
	encoder.SetProjectVersion("4.5.6")
	if encoder.projectVersion != "4.5.6" {
		t.Fatalf("ordinary version was rejected: %q", encoder.projectVersion)
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
		// Extended-length and device prefixes must not be read as a UNC host.
		{`\\?\C:\repo`, "file:///C:/repo"},
		{`\\.\C:\repo`, "file:///C:/repo"},
		{`\\?\UNC\server\share\repo`, "file://server/share/repo"},
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

// TestSCIPNoteCarriesRevisionProvenance pins the revision to the note.
//
// The commit used to ride inside every symbol as the package version. Removing
// it from the moniker was right, but it then survived nowhere: SCIP Metadata has
// no revision field, so two committed exports of the same repository at the same
// project version were indistinguishable from the artifacts alone.
func TestSCIPNoteCarriesRevisionProvenance(t *testing.T) {
	encode := func(header SnapshotHeader, summary SnapshotSummary) SCIPOmissionNote {
		t.Helper()
		encoder := NewSCIPSnapshotEncoder(&bytes.Buffer{}, "1.0.0")
		if err := encoder.Encode(header); err != nil {
			t.Fatal(err)
		}
		if err := encoder.Encode(summary); err != nil {
			t.Fatal(err)
		}
		return encoder.OmissionNote()
	}
	committed := encode(SnapshotHeader{RepoKey: "local/demo", Commit: "abc123", Tree: "def456"}, SnapshotSummary{})
	if committed.Commit != "abc123" || committed.Tree != "def456" {
		t.Fatalf("committed export lost its revision: %#v", committed)
	}
	// A worktree export describes no commit, and says so instead of inventing one.
	worktree := encode(
		SnapshotHeader{RepoKey: "local/demo", Commit: "abc123", Tree: "def456"},
		SnapshotSummary{Warnings: []ProviderWarning{{Code: "W_WORKTREE_SNAPSHOT"}}},
	)
	if worktree.Commit != "" || worktree.Tree != "" || !worktree.WorktreeSnapshot {
		t.Fatalf("worktree export carried a revision: %#v", worktree)
	}
}

// TestSCIPEmitsImplementationRelationships covers the navigation SCIP answers
// from SymbolInformation relationships rather than from occurrences. Emitting
// the inheritance family only as reference occurrences made Find
// Implementations return nothing.
func TestSCIPEmitsImplementationRelationships(t *testing.T) {
	var out bytes.Buffer
	encoder := NewSCIPSnapshotEncoder(&out, "1.0.0")
	push := func(record any) {
		t.Helper()
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	push(SnapshotHeader{RepoKey: "local/demo", Commit: "abc"})
	push(FileRecord{Path: "a.go", Language: "Go"})
	push(SymbolRecord{ID: "iface", Kind: "interface", Name: "Animal", FilePath: "a.go", StartLine: 1, EndLine: 3})
	push(SymbolRecord{ID: "impl", Kind: "struct", Name: "Dog", FilePath: "a.go", StartLine: 5, EndLine: 9})
	push(RelationRecord{Type: "IMPLEMENTS", FromID: "impl", ToID: "iface",
		Evidence: []Evidence{{FilePath: "a.go", StartLine: 5, EndLine: 5}}})
	push(SnapshotSummary{})

	index := &scippb.Index{}
	if err := proto.Unmarshal(out.Bytes(), index); err != nil {
		t.Fatal(err)
	}
	var dog *scippb.SymbolInformation
	for _, doc := range index.GetDocuments() {
		for _, info := range doc.GetSymbols() {
			if strings.Contains(info.GetSymbol(), "Dog") {
				dog = info
			}
		}
	}
	if dog == nil {
		t.Fatal("Dog symbol missing from the index")
	}
	if len(dog.GetRelationships()) != 1 {
		t.Fatalf("Dog has %d relationships, want 1: %#v", len(dog.GetRelationships()), dog.GetRelationships())
	}
	rel := dog.GetRelationships()[0]
	if !rel.GetIsImplementation() || !strings.Contains(rel.GetSymbol(), "Animal") {
		t.Fatalf("relationship is not an implementation of Animal: %#v", rel)
	}
	if got := encoder.OmissionNote().EmittedImplementations; got != 1 {
		t.Errorf("emitted_implementations = %d, want 1", got)
	}
}

// TestSCIPDefinitionOccurrenceMarksTheDeclaration keeps a definition from
// overlapping every reference inside its own body, which made positional
// lookups inside a multi-line function ambiguous.
func TestSCIPDefinitionOccurrenceMarksTheDeclaration(t *testing.T) {
	var out bytes.Buffer
	encoder := NewSCIPSnapshotEncoder(&out, "1.0.0")
	for _, record := range []any{
		SnapshotHeader{RepoKey: "local/demo", Commit: "abc"},
		FileRecord{Path: "a.go", Language: "Go"},
		SymbolRecord{ID: "fn", Kind: "function", Name: "Caller", FilePath: "a.go", StartLine: 10, EndLine: 20},
		SnapshotSummary{},
	} {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	index := &scippb.Index{}
	if err := proto.Unmarshal(out.Bytes(), index); err != nil {
		t.Fatal(err)
	}
	var def *scippb.Occurrence
	for _, doc := range index.GetDocuments() {
		for _, occ := range doc.GetOccurrences() {
			if occ.GetSymbolRoles()&int32(scippb.SymbolRole_Definition) != 0 {
				def = occ
			}
		}
	}
	if def == nil {
		t.Fatal("no definition occurrence emitted")
	}
	span, ok := def.SourceRange()
	if !ok {
		t.Fatalf("definition has no range: %#v", def)
	}
	// Declaration line only: 0-based start 9, exclusive end 10.
	if span.Start.Line != 9 || span.End.Line != 10 {
		t.Errorf("definition range = %v, want the declaration line only", span)
	}
	enclosing, ok := def.EnclosingSourceRange()
	if !ok {
		t.Fatalf("definition carries no enclosing range: %#v", def)
	}
	if enclosing.Start.Line != 9 || enclosing.End.Line != 20 {
		t.Errorf("enclosing range = %v, want the full declaration-through-body span", enclosing)
	}
}

// TestSCIPLocalSymbolsAreInjectivePerDocument pins the property a hash could
// only approximate. A collision would merge two unrelated closures' definitions,
// references and relationships into one symbol, and both sides would still be
// valid SCIP, so nothing downstream would notice.
func TestSCIPLocalSymbolsAreInjectivePerDocument(t *testing.T) {
	var out bytes.Buffer
	encoder := NewSCIPSnapshotEncoder(&out, "1.0.0")
	push := func(r any) {
		t.Helper()
		if err := encoder.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	push(SnapshotHeader{RepoKey: "local/demo", Commit: "abc"})
	push(FileRecord{Path: "a.go", Language: "Go"})
	push(FileRecord{Path: "b.go", Language: "Go"})
	for i, spec := range []struct {
		id, file string
	}{
		{"l1", "a.go"}, {"l2", "a.go"}, {"l3", "a.go"}, {"l4", "b.go"},
	} {
		push(SymbolRecord{ID: spec.id, Kind: "function", Name: "closure", FilePath: spec.file, Local: true, StartLine: i + 1, EndLine: i + 1})
	}
	push(SymbolRecord{ID: "g1", Kind: "function", Name: "Exported", FilePath: "a.go", StartLine: 9, EndLine: 9})
	push(SnapshotSummary{})

	index := &scippb.Index{}
	if err := proto.Unmarshal(out.Bytes(), index); err != nil {
		t.Fatal(err)
	}
	for _, doc := range index.GetDocuments() {
		seen := map[string]bool{}
		locals := 0
		for _, info := range doc.GetSymbols() {
			symbol := info.GetSymbol()
			if !strings.HasPrefix(symbol, "local ") {
				continue
			}
			locals++
			if seen[symbol] {
				t.Errorf("%s: duplicate local symbol %q", doc.GetRelativePath(), symbol)
			}
			seen[symbol] = true
			if _, err := scippb.ParseSymbol(symbol); err != nil {
				t.Errorf("%s: %q is not a valid SCIP symbol: %v", doc.GetRelativePath(), symbol, err)
			}
		}
		switch doc.GetRelativePath() {
		case "a.go":
			if locals != 3 {
				t.Errorf("a.go has %d locals, want 3", locals)
			}
		case "b.go":
			if locals != 1 {
				t.Errorf("b.go has %d locals, want 1", locals)
			}
		}
	}
	// Numbering restarts per document, which is what makes it document-scoped.
	if !strings.Contains(out.String(), "local 0") {
		t.Error("no local 0 emitted")
	}
}

// TestCargoWorkspaceInheritanceSurvivesATrailingComment pins the comment strip on the
// inheritance check. A '#' opens a comment anywhere TOML allows a value, and the raw
// value used to carry it: `version.workspace = true # inherit` compared "true # inherit"
// against "true", read as "does not inherit", and fell back to the unknown version. Every
// crate in a commented workspace then shared one SCIP package identity -- the identity
// collapse parseCargoPackageVersion exists to prevent -- while parsing cleanly and
// reporting no error.
//
// The quoted case is here because the strip must not run inside a string: a version may
// legitimately contain a hash, and truncating it would corrupt the identity it sets.
func TestCargoWorkspaceInheritanceSurvivesATrailingComment(t *testing.T) {
	t.Parallel()

	const workspace = "[workspace.package]\nversion = \"4.2.0\"\n"

	for _, testCase := range []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "dotted key with a trailing comment",
			content: workspace + "[package]\nversion.workspace = true # inherit\n",
			want:    "4.2.0",
		},
		{
			name:    "inline table with a trailing comment",
			content: workspace + "[package]\nversion = { workspace = true } # inherit\n",
			want:    "4.2.0",
		},
		{
			name:    "dotted key without a comment still works",
			content: workspace + "[package]\nversion.workspace = true\n",
			want:    "4.2.0",
		},
		{
			name:    "a literal version keeps a hash inside its quotes",
			content: "[package]\nversion = \"1.0#rc1\"\n",
			want:    "1.0#rc1",
		},
		{
			name:    "a commented-out inheritance is not inheritance",
			content: workspace + "[package]\n# version.workspace = true\n",
			want:    "",
		},
		{
			// An unterminated inline table is not a declaration. Accepting it
			// handed an invalid manifest the workspace's version, which is the
			// opposite of the fallback this file relies on.
			name:    "an unterminated inline table is not inheritance",
			content: workspace + "[package]\nversion = { workspace = true\n",
			want:    "",
		},
		{
			name:    "an inline table closed after a comment is still not inheritance",
			content: workspace + "[package]\nversion = { workspace = true # }\n",
			want:    "",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := parseCargoPackageVersion(testCase.content); got != testCase.want {
				t.Fatalf("parseCargoPackageVersion() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestCargoVirtualWorkspaceRootUsesTheWorkspaceVersion pins the shape the inheritance
// support exists to serve and did not reach.
//
// A virtual workspace root has no [package] of its own: it declares
// [workspace.package].version and lists members, and `version.workspace = true` appears
// only in the member manifests, which this reader never opens. Requiring the marker in
// the root meant the standard layout exported the unknown version and collapsed every
// crate in the workspace onto one SCIP identity -- silently, since the manifest parses.
//
// The narrowness is the point of the last two cases: a root that declares [package] and
// says nothing about version is left alone, because inventing a version corrupts the
// identity of every symbol under it.
func TestCargoVirtualWorkspaceRootUsesTheWorkspaceVersion(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "virtual workspace root with no package table",
			content: "[workspace]\nmembers = [\"crates/*\"]\n\n[workspace.package]\nversion = \"4.2.0\"\n",
			want:    "4.2.0",
		},
		{
			name:    "root package that inherits still resolves",
			content: "[workspace.package]\nversion = \"4.2.0\"\n\n[package]\nversion.workspace = true\n",
			want:    "4.2.0",
		},
		{
			name:    "an explicit package version still wins",
			content: "[workspace.package]\nversion = \"4.2.0\"\n\n[package]\nversion = \"1.0.0\"\n",
			want:    "1.0.0",
		},
		{
			name:    "a package table silent about version is not handed one",
			content: "[workspace.package]\nversion = \"4.2.0\"\n\n[package]\nname = \"thing\"\n",
			want:    "",
		},
		{
			name:    "no workspace version to fall back to",
			content: "[workspace]\nmembers = [\"crates/*\"]\n",
			want:    "",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := parseCargoPackageVersion(testCase.content); got != testCase.want {
				t.Fatalf("parseCargoPackageVersion() = %q, want %q", got, testCase.want)
			}
		})
	}
}
