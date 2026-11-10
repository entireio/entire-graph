package sem

import (
	"fmt"
	"testing"
)

type idfObservation struct {
	df    map[string]int
	n     int
	exact bool
}

// coldAndWarmIDF runs one query cold (content pass, cache disabled) and warm (Git tree grep over a
// full preindex of the committed tree) and returns the idf statistics each search scored with.
func coldAndWarmIDF(t *testing.T, repo, query string) (cold, warm idfObservation, coldBackend, warmBackend string) {
	t.Helper()
	var observed []idfObservation
	observe := func(df map[string]int, n int, exact bool) {
		copied := make(map[string]int, len(df))
		for term, count := range df {
			copied[term] = count
		}
		observed = append(observed, idfObservation{copied, n, exact})
	}
	base := SearchOptions{Profile: ProfileFull, TopK: 10, MaxIndexedFiles: 1, idfObserver: observe}
	coldOptions := base
	coldOptions.DisableCache = true
	coldResponse, err := SearchRepository(t.Context(), repo, "test-version", query, coldOptions)
	if err != nil {
		t.Fatal(err)
	}
	warmOptions := base
	warmOptions.CacheDir = t.TempDir()
	warmOptions.idfObserver = nil
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFull}, warmOptions.CacheDir); err != nil {
		t.Fatal(err)
	}
	warmOptions.idfObserver = observe
	warmResponse, err := SearchRepository(t.Context(), repo, "test-version", query, warmOptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed) != 2 {
		t.Fatalf("observer saw %d scored searches, want 2", len(observed))
	}
	return observed[0], observed[1], coldResponse.Stats.PreselectionBackend, warmResponse.Stats.PreselectionBackend
}

func gitIDFFixture(t *testing.T, files map[string]string, configure ...[]string) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	for _, args := range configure {
		git(t, repo, args...)
	}
	for path, content := range files {
		write(t, repo, path, content)
	}
	for i := 0; i < 4; i++ {
		write(t, repo, fmt.Sprintf("noise/n%d.go", i), fmt.Sprintf("package noise\n\nfunc N%d() int { return %d }\n", i, i))
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

func requireSameExactIDF(t *testing.T, cold, warm idfObservation, coldBackend, warmBackend string) {
	t.Helper()
	if coldBackend == warmBackend || warmBackend != "git-tree-grep" {
		t.Fatalf("fixture drift: backends cold=%q warm=%q, want go-content vs git-tree-grep", coldBackend, warmBackend)
	}
	if !cold.exact || !warm.exact || cold.n != warm.n || fmt.Sprint(cold.df) != fmt.Sprint(warm.df) {
		t.Fatalf("warm Git statistics differ from cold:\ncold (%s) DF=%v N=%d exact=%v\nwarm (%s) DF=%v N=%d exact=%v",
			coldBackend, cold.df, cold.n, cold.exact, warmBackend, warm.df, warm.n, warm.exact)
	}
}

// A NUL-free text file marked -diff contains the query terms only in its CONTENT. `git grep -I`
// treats it as binary and omits it, so a warm Git preselection that trusted -I counted DF without
// it while the cold content pass counted it.
func TestWarmGitIDFCountsTextMarkedDiffUnset(t *testing.T) {
	t.Parallel()
	repo := gitIDFFixture(t, map[string]string{
		".gitattributes": "marked.go -diff\n",
		"marked.go":      "package fixture\n\n// quorum ledger reconciliation\nfunc Marked() {}\n",
		"plain.go":       "package fixture\n\n// quorum ledger reconciliation\nfunc Plain() {}\n",
	})
	cold, warm, cb, wb := coldAndWarmIDF(t, repo, "quorum ledger reconciliation")
	requireSameExactIDF(t, cold, warm, cb, wb)
	if warm.df["quorum"] != 2 {
		t.Fatalf("warm DF[quorum]=%d, want 2 (the -diff file must be counted)", warm.df["quorum"])
	}
}

// With no attributes at all, diff.default.binary=true makes `git grep -I` treat every file as
// binary, so the same omission happens through repository config.
func TestWarmGitIDFUnaffectedByDiffDefaultBinary(t *testing.T) {
	t.Parallel()
	repo := gitIDFFixture(t, map[string]string{
		"target.go": "package fixture\n\n// quorum ledger reconciliation\nfunc Target() {}\n",
	}, []string{"config", "diff.default.binary", "true"})
	cold, warm, cb, wb := coldAndWarmIDF(t, repo, "quorum ledger reconciliation")
	requireSameExactIDF(t, cold, warm, cb, wb)
	if warm.df["quorum"] != 1 {
		t.Fatalf("warm DF[quorum]=%d, want 1", warm.df["quorum"])
	}
}

// A file with a NUL byte that contains the term is inspected: it contributes 0 to DF and counts in
// N, on both backends.
func TestWarmGitIDFCountsANULFileInNButNotDF(t *testing.T) {
	t.Parallel()
	repo := gitIDFFixture(t, map[string]string{
		"target.go": "package fixture\n\n// quorum ledger reconciliation\nfunc Target() {}\n",
		"blob.bin":  "quorum ledger\x00reconciliation\n",
	})
	cold, warm, cb, wb := coldAndWarmIDF(t, repo, "quorum ledger reconciliation")
	requireSameExactIDF(t, cold, warm, cb, wb)
	if warm.df["quorum"] != 1 || warm.n != 6 {
		t.Fatalf("warm DF[quorum]=%d N=%d, want DF 1 (NUL file excluded) N 6 (NUL file counted)", warm.df["quorum"], warm.n)
	}
}

// The alias route (a query with inferred abbreviations) selects by validated alias hits, a
// different term set from q.terms; it must not claim exact corpus statistics from that subset.
func TestWarmGitIDFAliasRouteIsNotExact(t *testing.T) {
	t.Parallel()
	repo := gitIDFFixture(t, map[string]string{
		"target.go": "package fixture\n\n// integer overflow guard\nfunc Target() int { return 0 }\n",
	})
	if q := buildSearchQuery("integer overflow guard"); len(q.inferredAbbreviations) == 0 {
		t.Fatal("fixture drift: query no longer infers an abbreviation, so the alias route is not exercised")
	}
	_, warm, _, wb := coldAndWarmIDF(t, repo, "integer overflow guard")
	if wb != "git-tree-grep" {
		t.Fatalf("fixture drift: warm backend %q", wb)
	}
	if warm.exact {
		t.Fatalf("alias route claimed exact corpus statistics: DF=%v N=%d", warm.df, warm.n)
	}
}
