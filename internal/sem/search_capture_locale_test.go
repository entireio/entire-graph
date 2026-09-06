package sem

import (
	"context"
	"os"
	"os/exec"
	"reflect"
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
			}
		}
	}
	patterns := []string{"ascii", "É", "é", "K", "K", "s", "ſ", "Σ", "σ", "ς", "I", "i", "İ", "ı"}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "src/cases.go", "package p\n// ASCII ascii\n// É é\n// K K\n// s ſ\n// Σ σ ς\n// I i İ ı\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "captured locale fixture")

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
			captured, _, err := capturedPreselectionMatches(context.Background(), source, source.paths, patterns, 32)
			if err != nil {
				t.Fatal(err)
			}
			gitMatches, err := gitutil.GrepIndexMatches(context.Background(), repo, patterns, 32)
			if err != nil {
				t.Fatal(err)
			}
			got := reduceTermPresence(captured, patterns)
			want := reduceTermPresence(gitMatches, patterns)
			if !reflect.DeepEqual(got, want) {
				t.Logf("locale %s captured=%#v git=%#v rawGit=%#v", locale, got, want, gitMatches)
				t.Fatalf("captured term presence diverges from Git under locale %s", locale)
			}
		})
	}
	_ = os.Getenv("LC_ALL") // keep the environment import explicit for test setup review
}
