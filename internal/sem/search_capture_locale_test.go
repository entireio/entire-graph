package sem

import (
	"context"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/gitutil"
)

func TestCapturedPreselectionLocaleTermPresenceMatchesGit(t *testing.T) {
	locales := []string{"C", "POSIX"}
	if output, err := exec.Command("locale", "-a").Output(); err == nil {
		for _, candidate := range strings.Fields(string(output)) {
			if strings.Contains(strings.ToUpper(candidate), "UTF-8") || strings.Contains(strings.ToUpper(candidate), "UTF8") {
				locales = append(locales, candidate)
				break
			}
		}
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	fixtures := map[string]string{
		"ascii.go":           "ascii",
		"accent-upper.go":    "É",
		"accent-lower.go":    "é",
		"kelvin-ascii.go":    "K",
		"kelvin-sign.go":     "K",
		"long-s.go":          "s",
		"long-s-sign.go":     "ſ",
		"sigma-upper.go":     "Σ",
		"sigma-lower.go":     "σ",
		"sigma-final.go":     "ς",
		"turkish-upper.go":   "I",
		"turkish-lower.go":   "i",
		"turkish-dotted.go":  "İ",
		"turkish-dotless.go": "ı",
	}
	for path, content := range fixtures {
		write(t, repo, "src/"+path, "package p\n// "+content+"\n")
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "captured locale fixture")
	patterns := make([]string, 0, len(fixtures))
	for _, pattern := range fixtures {
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)

	for _, locale := range locales {
		locale := locale
		t.Run(locale, func(t *testing.T) {
			// Git's grep deliberately preserves caller locale. Cases are sequential
			// so each subprocess sees one stable locale without changing Git config.
			t.Setenv("LC_ALL", locale)
			t.Setenv("LANG", locale)
			source, err := prepareSource(context.Background(), repo, ProviderSnapshotOptions{
				NoNetwork: true, Worktree: true, ExtractionReuse: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer source.close()
			for _, pattern := range patterns {
				captured, _, err := capturedPreselectionMatches(context.Background(), source, source.paths, []string{pattern}, 32)
				if err != nil {
					t.Fatal(err)
				}
				gitMatches, err := gitutil.GrepIndexMatches(context.Background(), repo, []string{pattern}, 32)
				if err != nil {
					t.Fatal(err)
				}
				got := reduceSingleTermPresence(captured, pattern)
				want := reduceSingleTermPresence(gitMatches, pattern)
				if !reflect.DeepEqual(got, want) {
					t.Logf("locale %s pattern %q captured=%#v git=%#v rawGit=%#v", locale, pattern, got, want, gitMatches)
					t.Fatalf("captured term presence diverges from Git under locale %s pattern %q", locale, pattern)
				}
			}
		})
	}
}

func reduceSingleTermPresence(matches []gitutil.GrepMatch, pattern string) map[string]map[string]bool {
	presence := map[string]map[string]bool{}
	for _, match := range matches {
		if presence[match.Path] == nil {
			presence[match.Path] = map[string]bool{}
		}
		presence[match.Path][strings.ToLower(pattern)] = true
	}
	return presence
}
