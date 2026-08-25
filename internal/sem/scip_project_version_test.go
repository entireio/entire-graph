package sem

import (
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
