package gitutil

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Git's multi-pattern line matcher is pathologically slow on large text blobs
// (minutes for twelve case-folded literals on a 17 MB JSON file, versus ~2s as
// one alternation), so grepPatternLines must hand Git exactly one pattern.
func TestGrepPatternLinesInputIsOneAlternation(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		want     string
	}{
		{"joins every pattern as a group", []string{"[dD][bB]", "(^|[^[:alnum:]])[nN][oO]", "[fF]([iI]|İ)[lL][eE]"}, "([dD][bB])|((^|[^[:alnum:]])[nN][oO])|([fF]([iI]|İ)[lL][eE])\n"},
		{"single pattern", []string{"needle"}, "(needle)\n"},
		{"drops duplicates", []string{"a", "b", "a"}, "(a)|(b)\n"},
		{"mirrors -f line splitting, CR stripping and empty-line skipping", []string{"a\r\nb", "", "c"}, "(a)|(b)|(c)\n"},
		{"bracket members are not grouping", []string{"[)(]", "[]a]", "[^]a]", "[[:alpha:]]x", "[[.-.]]", `[\]`}, "([)(])|([]a])|([^]a])|([[:alpha:]]x)|([[.-.]])|([\\])\n"},
		{"escaped parentheses are literals", []string{`\(x\)`}, "(\\(x\\))\n"},
		{"back-reference keeps legacy form", []string{"(a)\\1", "b"}, "(a)\\1\nb\n"},
		{"unbalanced close keeps legacy form", []string{")(", "b"}, ")(\nb\n"},
		{"unbalanced open keeps legacy form", []string{"(a", "b"}, "(a\nb\n"},
		{"unterminated bracket keeps legacy form", []string{"[a", "b"}, "[a\nb\n"},
		{"trailing backslash keeps legacy form", []string{`a\`}, "a\\\n"},
		{"no effective pattern keeps legacy form", []string{""}, "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := grepPatternLinesInput(tt.patterns); got != tt.want {
				t.Fatalf("grepPatternLinesInput(%q) = %q, want %q", tt.patterns, got, tt.want)
			}
		})
	}
}

// legacyGrepPatternLines runs the pre-alternation command shape: every
// pattern on its own `-f -` line. It is the oracle for equivalence below.
func legacyGrepPatternLines(t *testing.T, repo, treeish string, patterns []string, maxPerFile int) []GrepMatch {
	t.Helper()
	args := []string{"grep", "--no-recurse-submodules", "--no-line-number", "--no-column", "--no-color", "--no-full-name", "-z", "-I", "-E", "-m", strconv.Itoa(maxPerFile), "-f", "-"}
	if treeish != "" {
		args = append(args, treeish)
	}
	args = append(args, "--")
	cmd := newGitCmdWithCallerLocale(t.Context(), repo, args...)
	cmd.Stdin = strings.NewReader(strings.Join(patterns, "\n") + "\n")
	out, err := cmd.Output()
	var exitError *exec.ExitError
	if err != nil && !(errors.As(err, &exitError) && exitError.ExitCode() == 1) {
		t.Fatalf("legacy git grep: %v", err)
	}
	var matches []GrepMatch
	if err := readGrepPatternLines(bufio.NewReader(bytes.NewReader(out)), func(match GrepMatch) error {
		match.Path = strings.TrimPrefix(match.Path, treeish+":")
		matches = append(matches, match)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestGrepPatternLinesAlternationMatchesLegacyMultiPattern(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	files := map[string]string{
		// Earliest matching line comes from different patterns per file, so a
		// matcher that honoured only one pattern, or preferred one pattern's
		// hit over an earlier line, would pick a different line.
		"a.go":        "package a\nfunc openDB() {}\n// no tables here\nconst Database = 1\n",
		"b.go":        "package b\n// helper: creates FIXTURE\nvar dbs = 2\nfunc NoOp() {}\n",
		"c.txt":       strings.Repeat("filler line\n", 50) + "the sqlite fİle\n" + strings.Repeat("helper again\n", 40),
		"d.md":        "nothing relevant\n",
		"f.txt":       "path a\\b here\nq]r\\]s\n",
		"e/nested.py": "x = 'userDBName'\nNO = True\ndef table(): pass\n",
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

	patternSets := map[string][]string{
		"case-folded literals":   {"[dD][aA][tT][aA][bB][aA][sS][eE]", "[hH][eE][lL][pP][eE][rR]", "[fF]([iI]|İ)[lL][eE]", "[nN][oO]", "[tT][aA][bB][lL][eE]"},
		"anchors and boundaries": {"^package", "[{}]$", "(^|[^[:alnum:]])[dD][bB]([sS])?($|[^[:alnum:]])"},
		"alias alternations":     {"((^|[^[:alnum:]])[dD][bB]([sS])?($|[^[:alnum:]])|[[:lower:][:digit:]]D([bB]s|b)[[:upper:]])", "[cC][rR][eE][aA][tT][eE][sS]"},
		"no match":               {"zzqqxx", "[qQ]{9}"},
		// POSIX ERE: a backslash inside a bracket expression is a literal member, so
		// "[\\]" is a complete bracket and "[\\]r]" is that bracket followed by "r]".
		"bracket backslash": {`[\]`, `[\]r]`, "relevant"},
	}
	for name, patterns := range patternSets {
		for _, maxPerFile := range []int{1, 32} {
			for _, treeish := range []string{"", "HEAD"} {
				t.Run(name+"/m"+strconv.Itoa(maxPerFile)+"/tree="+treeish, func(t *testing.T) {
					if got := grepPatternLinesInput(patterns); strings.Count(got, "\n") != 1 {
						t.Fatalf("expected a single alternation line, got %q", got)
					}
					var got []GrepMatch
					if err := grepPatternLines(t.Context(), repo, treeish, patterns, maxPerFile, func(match GrepMatch) error {
						got = append(got, match)
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					want := legacyGrepPatternLines(t, repo, treeish, patterns, maxPerFile)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("alternation diverged from legacy multi-pattern grep\n got: %q\nwant: %q", got, want)
					}
					if name != "no match" && len(want) == 0 {
						t.Fatal("fixture is vacuous: legacy grep matched nothing")
					}
				})
			}
		}
	}
}
