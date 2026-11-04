package sem

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// vendoredGrammarsDigest pins the bytes of every vendored grammar under
// internal/sem/grammars. Changing a grammar changes parse results, and the
// snapshot caches are keyed on the git tree of the *indexed repository*, which
// a parser change does not touch — so IdentityRevision is the only thing that
// stops records built by the old grammar being served for unchanged source.
//
// Nothing else in the suite can notice a grammar change: there is no Swift
// fixture, and the one golden holding Swift symbols is byte-identical across
// the 0.5.0 -> 0.7.4 swap apart from its identity_revision field. Two earlier
// tests tried to cover this by comparing IdentityRevision against the previous
// value, which cannot fail while TestParserIdentityRevisionsSnapshotAndCaches
// pins it exactly; this compares the grammar bytes instead.
//
// When this fails because a grammar was deliberately refreshed: decide whether
// parse results change (they almost always do), bump IdentityRevision in
// provider.go if so, and then update the constant below to the printed digest.
const vendoredGrammarsDigest = "769dfdcf34ccef15c993e9989535761389ff967e7708e2f0e72cf19a4f932bf4"

func TestVendoredGrammarsMatchTheManifest(t *testing.T) {
	grammars := loadGrammarManifest(t)
	if len(grammars) == 0 {
		t.Fatal("grammars.json lists no grammars")
	}
	languageVersion := regexp.MustCompile(`(?m)^#define LANGUAGE_VERSION (\d+)\s*$`)
	seen := map[string]bool{}
	for _, g := range grammars {
		if seen[g.Language] {
			t.Errorf("%s is listed twice in grammars.json", g.Language)
		}
		seen[g.Language] = true
		dir := filepath.Join("grammars", g.Language)
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("grammars.json lists %s, but %s does not exist: %v", g.Language, dir, err)
			continue
		}
		// The manifest's files list is what a refresh treats as the managed set:
		// a file on disk it omits would be deleted, and one it names that is
		// absent would never be replaced.
		onDisk := managedGrammarFiles(t, dir)
		recorded := append([]string(nil), g.Files...)
		sort.Strings(recorded)
		if strings.Join(onDisk, ",") != strings.Join(recorded, ",") {
			t.Errorf("%s: grammars.json files = %v, on disk = %v", g.Language, recorded, onDisk)
		}
		parser, err := os.ReadFile(filepath.Join(dir, "parser.c"))
		if err != nil {
			t.Errorf("%s: %v", g.Language, err)
			continue
		}
		m := languageVersion.FindSubmatch(parser)
		if m == nil {
			t.Errorf("%s: parser.c has no #define LANGUAGE_VERSION", g.Language)
			continue
		}
		if got := string(m[1]); got != strconv.Itoa(g.ABI) {
			t.Errorf("%s: parser.c is ABI %s, grammars.json records %d", g.Language, got, g.ABI)
		}
	}
	// Every grammar directory must be described, or a refresh of it has no
	// origin to fetch and nothing records that it carries local changes.
	entries, err := os.ReadDir("grammars")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() && !seen[e.Name()] {
			t.Errorf("grammars/%s is vendored but absent from grammars.json", e.Name())
		}
	}
}

func TestVendoredGrammarBytesAreTiedToTheIdentityRevision(t *testing.T) {
	sum := sha256.New()
	var files []string
	for _, g := range loadGrammarManifest(t) {
		dir := filepath.Join("grammars", g.Language)
		for _, rel := range managedGrammarFiles(t, dir) {
			files = append(files, filepath.ToSlash(filepath.Join(dir, rel)))
		}
	}
	sort.Strings(files)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		// Hash the path too, so moving a grammar is a change.
		sum.Write([]byte(f))
		sum.Write([]byte{0})
		sum.Write(data)
	}
	got := hex.EncodeToString(sum.Sum(nil))
	if got != vendoredGrammarsDigest {
		t.Fatalf("vendored grammar bytes changed (%d files).\n"+
			"  digest now: %s\n"+
			"  pinned:     %s\n"+
			"If a grammar was refreshed, decide whether parse results change; if they do, "+
			"bump IdentityRevision in internal/sem/provider.go (currently %q) so cached "+
			"records built by the old grammar are not served, then set "+
			"vendoredGrammarsDigest to the digest above.",
			len(files), got, vendoredGrammarsDigest, IdentityRevision)
	}
}

type grammarManifestEntry struct {
	Language string   `json:"language"`
	Files    []string `json:"files"`
	ABI      int      `json:"abi"`
}

func loadGrammarManifest(t *testing.T) []grammarManifestEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("grammars", "grammars.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []grammarManifestEntry
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// managedGrammarFiles lists the files in a vendored grammar directory that a
// refresh owns, using the same rule as cmd/grammar-refresh: parser.c,
// scanner.c, headers, tree_sitter/*.h and the licence. binding.go and the
// Swift notes are deliberately excluded.
func managedGrammarFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		upper := strings.ToUpper(name)
		switch {
		case e.IsDir() && name == "tree_sitter":
			headers, err := filepath.Glob(filepath.Join(dir, "tree_sitter", "*.h"))
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range headers {
				out = append(out, "tree_sitter/"+filepath.Base(h))
			}
		case e.IsDir():
		case name == "parser.c" || name == "scanner.c",
			strings.HasSuffix(name, ".h"),
			strings.HasPrefix(upper, "LICENSE"),
			strings.HasPrefix(upper, "COPYING"):
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
