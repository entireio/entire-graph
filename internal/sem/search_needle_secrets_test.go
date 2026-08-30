package sem

import (
	"slices"
	"testing"
)

// TestSearchNeedleGrepDropsCredentialStores covers the second application point.
//
// Excluding a path from the corpus LISTING is not sufficient on its own, because
// the needle index has a route that does not go through the listing: Git answers
// `git grep` over the whole tree. Git has already scanned that broader tree; this
// filter prevents a denied path from being handed to the provider's content reader
// and surfaced in a search block.
//
// The unfiltered call below is the control, and it is what makes this test
// non-vacuous: it proves Git really does name `.env` and
// `deploy/secrets/prod-secrets.yaml` for this needle, so the filtered call's
// absence is the filter's doing and not the fixture's.
func TestSearchNeedleGrepDropsCredentialStores(t *testing.T) {
	t.Parallel()

	repo := credentialStoreRepo(t)

	// The control: no ignore rules at all, which is what this route did before.
	unfiltered, ok := newSearchNeedleGrep(t.Context(), repo, "", ignoreMatcher{})("STRIPE_SECRET_KEY")
	if !ok {
		t.Fatal("git grep could not answer; the assertions below would be vacuous")
	}
	for _, victim := range credentialStoreNeedleVictimPaths {
		if !slices.Contains(unfiltered, victim) {
			t.Fatalf("git grep did not name %s for this needle, so the filtered call proves "+
				"nothing; got %v", victim, unfiltered)
		}
	}

	ignores, err := loadWorktreeIgnoreMatcher(repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	filtered, ok := newSearchNeedleGrep(t.Context(), repo, "", ignores)("STRIPE_SECRET_KEY")
	if !ok {
		t.Fatal("git grep could not answer through the filtered route")
	}
	if len(filtered) == 0 {
		t.Fatal("the filtered route returned nothing at all; it must drop the credential stores, " +
			"not the answer")
	}
	for _, victim := range credentialStoreVictimPaths {
		if slices.Contains(filtered, victim) {
			t.Errorf("the git-grep route still names credential store %s; it is read by "+
				"readLines, so an excluded path would be reopened here: %v", victim, filtered)
		}
	}
	for _, control := range []string{"internal/config/keys.go", "internal/http/client.go"} {
		if !slices.Contains(filtered, control) {
			t.Errorf("the git-grep route lost first-party file %s: %v", control, filtered)
		}
	}
}

// TestSearchNeedleGrepHonoursIncludeFile pins that the override reaches this route
// too. Applying the corpus's own matcher rather than a separate secret predicate is
// what buys that: one verdict, one place to change it.
func TestSearchNeedleGrepHonoursIncludeFile(t *testing.T) {
	t.Parallel()

	repo := credentialStoreRepo(t)
	includeFile := writeCredentialIncludeFile(t, ".env\n")

	ignores, err := loadWorktreeIgnoreMatcher(repo, nil, []string{includeFile})
	if err != nil {
		t.Fatal(err)
	}
	paths, ok := newSearchNeedleGrep(t.Context(), repo, "", ignores)("STRIPE_SECRET_KEY")
	if !ok {
		t.Fatal("git grep could not answer")
	}
	if !slices.Contains(paths, ".env") {
		t.Errorf("--include-file did not re-admit .env on the git-grep route: %v", paths)
	}
	if slices.Contains(paths, "deploy/secrets/prod-secrets.yaml") {
		t.Errorf("--include-file naming .env also re-admitted an unrelated credential store: %v", paths)
	}
}
