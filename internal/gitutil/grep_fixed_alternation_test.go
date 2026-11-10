package gitutil

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Git's multi-pattern -o matcher is pathologically slow on large text blobs,
// so grepFixedStringMatches must hand Git one PCRE2 alternation whenever that
// is provably equivalent to the legacy `-F -i -e p1 -e p2 ...` invocation.
func TestGrepFixedStringAlternationShape(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		want     string
		ok       bool
	}{
		{"one literal", []string{"needle"}, `\Qneedle\E`, true},
		{"longest first so the first branch matching at an offset is the longest", []string{"db", "create", "creates", "table", "tables", "no"}, `\Qcreates\E|\Qcreate\E|\Qtables\E|\Qtable\E|\Qdb\E|\Qno\E`, true},
		{"equal lengths keep caller order", []string{"bb", "aa", "cc"}, `\Qbb\E|\Qaa\E|\Qcc\E`, true},
		{"drops duplicates and empties", []string{"a", "", "b", "a"}, `\Qa\E|\Qb\E`, true},
		{"quotes every metacharacter", []string{"a.b", "f(x)", "[x]", "a|b", "$^*+?{"}, `\Q$^*+?{\E|\Qf(x)\E|\Qa.b\E|\Q[x]\E|\Qa|b\E`, true},
		{"backslash keeps legacy form", []string{"a", `x\Ey`}, "", false},
		{"trailing backslash keeps legacy form", []string{`a\`}, "", false},
		{"newline keeps legacy form", []string{"a\nb"}, "", false},
		{"non-ASCII keeps legacy form", []string{"key", "wéird"}, "", false},
		{"Kelvin sign pattern keeps legacy form", []string{"Key"}, "", false},
		{"no effective pattern keeps legacy form", []string{""}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := grepFixedStringAlternation(tt.patterns)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("grepFixedStringAlternation(%q) = %q, %v; want %q, %v", tt.patterns, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// legacyGrepFixedStringMatches runs the pre-alternation command shape, every
// pattern as its own `-F -i -e`, and parses it with the production parser.
// It is the oracle for equivalence below.
func legacyGrepFixedStringMatches(t *testing.T, repo, treeish string, patterns []string, maxPerFile int) []GrepMatch {
	t.Helper()
	var fixed []string
	for _, pattern := range patterns {
		if pattern != "" {
			fixed = append(fixed, "-e", pattern)
		}
	}
	matches, err := runGrepFixedStringMatches(t.Context(), repo, treeish, maxPerFile, append([]string{"-F"}, fixed...))
	if err != nil {
		t.Fatalf("legacy git grep: %v", err)
	}
	return matches
}

func gitHasPCRE(t *testing.T) bool {
	t.Helper()
	cmd := exec.Command("git", "grep", "--no-index", "-q", "-P", "-e", `\Qx\E`, "--", os.DevNull)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()
	return !strings.Contains(stderr.String(), "USE_LIBPCRE")
}

func TestGrepFixedStringMatchesAlternationMatchesLegacyMultiPattern(t *testing.T) {
	if !gitHasPCRE(t) {
		t.Skip("installed Git has no PCRE2; grepFixedStringMatches always takes the legacy form")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	files := map[string]string{
		// Prefixes (create/creates, table/tables, data/database), overlaps
		// (aa in aaaa, aba in ababa), adjacency (dbdb) and mixed case.
		"a.go":  "package a\nfunc CreatesTables() { createTABLE(); dbdbDB }\nconst Database = \"database-data\"\n",
		"b.txt": "aaaa ababa AbAbA aAaA\nxcreatesx xcreatex tablestable\n",
		// Non-ASCII case equivalents of ASCII letters: KELVIN SIGN (k),
		// LONG S (s), dotted/dotless I, sharp s, and a Greek line.
		"c.txt": "Key key KEY\ndatabaſe ſqlite\nİd ıd id ID\nstraße strasse\nΣσς µμ\n",
		// Metacharacters must stay literal.
		"d.md": "a.b axb f(x) fx [x] a|b $^*+?{ ab\n",
		// More matching lines than -m allows, and many matches per line.
		"e/many.txt": strings.Repeat("db table create db\n", 40),
		"f/none.txt": "nothing relevant here\n",
	}
	for name, content := range files {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "fixture")
	// A dirty worktree change so worktree and HEAD differ.
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n// dirty DB Table creates\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	patternSets := map[string][]string{
		// Shortest first, so an unsorted alternation would pick create over
		// creates and table over tables.
		"prefix pairs":          {"db", "no", "data", "table", "create", "tables", "creates", "database"},
		"overlap and adjacency": {"aa", "aba", "ab", "dbdb", "db"},
		"folding sensitive":     {"key", "database", "sqlite", "id", "strasse", "s"},
		"metacharacters":        {"a.b", "f(x)", "[x]", "a|b", "$^*+?{", "x"},
		"no match":              {"zzqqxx", "qqq"},
	}
	locales := []string{"C"}
	// Add a UTF-8 locale under which legacy -F -i folds U+212A KELVIN SIGN to
	// k; locale names vary across platforms.
	for _, candidate := range []string{"C.UTF-8", "en_US.UTF-8", "en_US.utf8"} {
		cmd := exec.Command("git", "grep", "-h", "-o", "-i", "-F", "-e", "key", "--", "c.txt")
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "LC_ALL="+candidate, "LANG="+candidate)
		if out, err := cmd.Output(); err == nil && bytes.Contains(out, []byte("\u212a")) {
			locales = append(locales, candidate)
			break
		}
	}
	kelvinSeen := false
	for _, locale := range locales {
		t.Setenv("LC_ALL", locale)
		t.Setenv("LANG", locale)
		for name, patterns := range patternSets {
			for _, maxPerFile := range []int{1, 3, 32} {
				for _, treeish := range []string{"", "HEAD"} {
					t.Run(locale+"/"+name+"/m"+strconv.Itoa(maxPerFile)+"/tree="+treeish, func(t *testing.T) {
						if _, ok := grepFixedStringAlternation(patterns); !ok {
							t.Fatalf("expected the alternation form for %q", patterns)
						}
						got, err := grepFixedStringMatches(t.Context(), repo, treeish, patterns, maxPerFile)
						if err != nil {
							t.Fatal(err)
						}
						want := legacyGrepFixedStringMatches(t, repo, treeish, patterns, maxPerFile)
						if !reflect.DeepEqual(got, want) {
							t.Fatalf("alternation diverged from legacy multi-pattern grep\n got: %q\nwant: %q", got, want)
						}
						if name != "no match" && len(want) == 0 {
							t.Fatal("fixture is vacuous: legacy grep matched nothing")
						}
						for _, match := range want {
							if strings.Contains(match.Text, "K") || strings.Contains(match.Text, "ſ") {
								kelvinSeen = true
							}
						}
					})
				}
			}
		}
	}
	if len(locales) > 1 && !kelvinSeen {
		t.Fatal("fixture is vacuous: no UTF-8 run folded a non-ASCII character, so folding equivalence was not exercised")
	}
	if len(locales) == 1 {
		t.Log("no UTF-8 locale available; non-ASCII folding equivalence not exercised")
	}
}

// A Git built without PCRE2 rejects -P; grepFixedStringMatches must detect
// that and run the legacy -F form rather than fail or return nothing.
func TestGrepFixedStringMatchesFallsBackWithoutPCRE(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Git wrapper is a POSIX shell script")
	}
	wrapperDir := t.TempDir()
	script := `#!/bin/sh
case " $* " in
	*" -P "*) echo "fatal: cannot use Perl-compatible regexes when not compiled with USE_LIBPCRE" >&2; exit 128 ;;
	*" -F "*) printf 'auth.py\000Foo\n' ;;
	*) exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(wrapperDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	matches, err := grepFixedStringMatches(t.Context(), t.TempDir(), "", []string{"Foo", "bar"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := []GrepMatch{{Path: "auth.py", Text: "Foo"}}; !reflect.DeepEqual(matches, want) {
		t.Fatalf("fallback matches = %#v, want %#v", matches, want)
	}
}

// The production call must reach Git as one -P alternation, not as one -e per
// word: the multi-pattern form is the slow path this change removes.
func TestGrepFixedStringMatchesSendsOneAlternation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Git wrapper is a POSIX shell script")
	}
	if !gitHasPCRE(t) {
		t.Skip("installed Git has no PCRE2; grepFixedStringMatches always takes the legacy form")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	git(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("Database creates DB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	wrapperDir := t.TempDir()
	argLog := filepath.Join(wrapperDir, "args")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done >> '" + argLog + "'\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrapperDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	matches, err := grepFixedStringMatches(t.Context(), repo, "", []string{"db", "database", "creates"}, 32)
	if err != nil {
		t.Fatal(err)
	}
	if want := []GrepMatch{{Path: "a.txt", Text: "Database"}, {Path: "a.txt", Text: "creates"}, {Path: "a.txt", Text: "DB"}}; !reflect.DeepEqual(matches, want) {
		t.Fatalf("matches = %q, want %q", matches, want)
	}
	logged, err := os.ReadFile(argLog)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(logged), "\n"), "\n")
	count := func(flag string) int {
		n := 0
		for _, arg := range args {
			if arg == flag {
				n++
			}
		}
		return n
	}
	if count("grep") != 1 || count("-P") != 1 || count("-i") != 1 || count("-F") != 0 || count("-e") != 1 {
		t.Fatalf("git invocation is not one -P -i alternation: %q", args)
	}
	for i, arg := range args {
		if arg == "-e" && (i+1 >= len(args) || args[i+1] != `\Qdatabase\E|\Qcreates\E|\Qdb\E`) {
			t.Fatalf("alternation argument = %q", args[i+1:])
		}
	}
}
