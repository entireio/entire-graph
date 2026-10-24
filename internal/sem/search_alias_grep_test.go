package sem

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// aliasPreselect runs the committed-tree preselection against a complete
// preindex of repo, which is the only state that takes the alias grep route.
func aliasPreselect(t *testing.T, repo, query string) searchFileSelection {
	t.Helper()
	cacheDir := t.TempDir()
	snapshot, _, err := PreindexProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{Profile: ProfileSyntaxOnly}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	q := buildSearchQuery(query)
	if len(q.inferredAbbreviations) == 0 {
		t.Fatalf("query %q infers no abbreviation, so it cannot exercise the alias route", query)
	}
	selection, err := preselectSearchFiles(
		t.Context(), repo, q, searchQuery{},
		SearchOptions{Profile: ProfileSyntaxOnly, TopK: 10, MaxIndexedFiles: 1},
		ProviderSnapshotOptions{NoNetwork: true, Profile: ProfileSyntaxOnly},
		snapshot, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if selection.preselectionBackend != "git-tree-grep" {
		t.Fatalf("preselection took the %q route, want the alias git-tree-grep route", selection.preselectionBackend)
	}
	return selection
}

func aliasGrepFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	for path, content := range files {
		write(t, repo, path, content)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

func sortedPaths(paths []string) []string {
	out := append([]string(nil), paths...)
	sort.Strings(out)
	return out
}

// The alias grep runs over the whole commit and drops hits outside the corpus
// afterwards, so a blob the corpus refused (here a .graphignored generated
// parser) must never be scanned at all. A git shim records what each alias grep
// reported: the refused blob matches the expression, so it would appear there
// if Git read it. The selection itself must be what filtering always produced.
func TestCommittedAliasPreselectionNeverScansNonCorpusPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the git shim is a POSIX shell script")
	}
	repo := aliasGrepFixture(t, map[string]string{
		".graphignore":            "parser.c\n",
		"value.go":                "package app\nfunc readInt() {}\n",
		"output.go":               "package app\nfunc Print() {}\n",
		"vendor/grammar/parser.c": "int readInt(void);\n" + strings.Repeat("static int readInt_state;\n", 64),
	})
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	logPath := filepath.Join(shimDir, "alias-grep.log")
	shim := "#!/bin/sh\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$arg\" = \"-f\" ]; then\n" +
		"    out=\"" + shimDir + "/out.$$\"\n" +
		"    \"" + realGit + "\" \"$@\" > \"$out\"\n" +
		"    status=$?\n" +
		"    printf 'INVOCATION\\n' >> \"" + logPath + "\"\n" +
		"    cat \"$out\" >> \"" + logPath + "\"\n" +
		"    cat \"$out\"\n" +
		"    rm -f \"$out\"\n" +
		"    exit $status\n" +
		"  fi\n" +
		"done\n" +
		"exec \"" + realGit + "\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	selection := aliasPreselect(t, repo, "integer")

	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("the alias grep never ran through the shim: %v", err)
	}
	if !bytes.Contains(logged, []byte("INVOCATION\n")) || !bytes.Contains(logged, []byte(":value.go\x00")) {
		t.Fatalf("shim log lacks the corpus hit, so it cannot show what was scanned: %q", logged)
	}
	if bytes.Contains(logged, []byte("parser.c")) {
		t.Fatalf("the alias grep scanned a blob outside the corpus: %q", logged)
	}
	if got, want := sortedPaths(selection.files), []string{"value.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("selected %q, want %q", got, want)
	}
}

func TestSearchGitGrepExcludedPathsListsNonCorpusBlobsLargestFirst(t *testing.T) {
	repo := aliasGrepFixture(t, map[string]string{
		"keep.go":       "package app\n",
		"gen/small.c":   "x\n",
		"gen/parser.c":  strings.Repeat("y", 4096),
		"gen/scanner.c": strings.Repeat("z", 512),
	})
	got := searchGitGrepExcludedPaths(t.Context(), repo, gitHead(t, repo), map[string]bool{"keep.go": true})
	if want := []string{"gen/parser.c", "gen/scanner.c", "gen/small.c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("excluded %q, want every non-corpus blob largest first %q", got, want)
	}
}

func gitHead(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// A hit too large for the bounded reader cannot be validated on its content.
// It is validated line by line instead: admitted when any line passes the
// matcher, rejected when none does. late.json's first expression hit fails the
// matcher (under the C locale the expression takes é's bytes as a word
// boundary, the matcher does not) and a later line passes, the one case where
// this differs from the old first-matching-line rule, which rejected it.
func TestCommittedAliasPreselectionValidatesOversizeHitsByLine(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	filler := strings.Repeat("abcdefghij\n", defaultMaxParseBytes/len("abcdefghij\n")+1)
	repo := aliasGrepFixture(t, map[string]string{
		"value.go":   "package app\nfunc readInt() {}\n",
		"output.go":  "package app\nfunc Print() {}\n",
		"late.json":  "éint\n" + filler + "func readInt() {}\n",
		"noise.json": "éint\n" + filler + "éint\n",
	})
	q := buildSearchQuery("integer")
	if textMatchesSearchQuery(q, "éint") || !textMatchesSearchQuery(q, "func readInt() {}") {
		t.Fatal("fixture drift: the matcher must reject the é line and accept readInt")
	}
	cmd := exec.Command("git", "grep", "-l", "-I", "-E", "-f", "-", "HEAD", "--", "noise.json")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(strings.Join(searchGitAliasPatternsForTerms(q, q.terms), "\n") + "\n")
	noiseIsHit := cmd.Run() == nil
	if !noiseIsHit && runtime.GOOS != "windows" {
		t.Fatal("fixture drift: under the C locale the expression must match the é line")
	}

	selection := aliasPreselect(t, repo, "integer")
	if got, want := sortedPaths(selection.files), []string{"late.json", "value.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("selected %q, want %q: an oversize hit is admitted iff some line validates", got, want)
	}
	wantReads := 2 // value.go read whole, late.json streamed
	if noiseIsHit {
		wantReads++ // noise.json streamed and rejected
	}
	if selection.filesContentRead != wantReads {
		t.Fatalf("files content read = %d, want %d", selection.filesContentRead, wantReads)
	}
}
