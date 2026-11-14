package sem

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

// A nomination snapshot that cannot be loaded is a CHANNEL failure: the search falls open to the
// lexical answer — exactly the unconfigured one, cold-path evictions undone — with the stable
// unavailable:nomination-snapshot status and one W_SEMANTIC_UNAVAILABLE warning. It used to fail
// the whole search.
func TestSemanticNominationSnapshotFailureFallsOpen(t *testing.T) {
	t.Parallel()
	repo := semanticFixtureRepo(t)
	server := httptest.NewServer(&fakeEmbedder{})
	defer server.Close()
	cacheDir := t.TempDir()
	buildSemanticFixtureIndex(t, repo, cacheDir, server.URL)
	config := &SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel}
	for _, maxFiles := range []int{2, 0} {
		limit := func(options *SearchOptions) { options.MaxIndexedFiles = maxFiles }
		lexical := semanticSearch(t, repo, cacheDir, nil, limit)
		used := semanticSearch(t, repo, cacheDir, config, limit)
		// NON-VACUITY: the fault only matters where the channel actually nominated something.
		if used.Stats.SemanticStatus != SemanticStatusUsed || used.Stats.SemanticNominatedFiles == 0 {
			t.Fatalf("max=%d fixture drift: channel status %q nominated %d", maxFiles,
				used.Stats.SemanticStatus, used.Stats.SemanticNominatedFiles)
		}
		faults := 0
		failed := semanticSearch(t, repo, cacheDir, config, func(options *SearchOptions) {
			limit(options)
			options.nominationSnapshotFault = func() error { faults++; return errors.New("injected: /secret/path unreadable") }
		})
		if faults != 1 {
			t.Fatalf("max=%d: fault seam ran %d times", maxFiles, faults)
		}
		if want := semanticUnavailablePrefix + semanticNominationSnapshotReason; failed.Stats.SemanticStatus != want {
			t.Fatalf("max=%d: status %q, want %q", maxFiles, failed.Stats.SemanticStatus, want)
		}
		if n := countWarnings(failed.Warnings, "W_SEMANTIC_UNAVAILABLE"); n != 1 {
			t.Fatalf("max=%d: %d W_SEMANTIC_UNAVAILABLE warnings, want 1", maxFiles, n)
		}
		for _, warning := range failed.Warnings {
			if warning.Code == "W_SEMANTIC_UNAVAILABLE" && strings.Contains(warning.Detail, "/secret/path") {
				t.Fatalf("max=%d: warning leaks the underlying error: %q", maxFiles, warning.Detail)
			}
		}
		if failed.Stats.SemanticNominatedFiles != 0 || failed.Stats.SemanticEvictedFiles != 0 || failed.Stats.SemanticResults != 0 {
			t.Fatalf("max=%d: failed channel still reports budget: %+v", maxFiles, failed.Stats)
		}
		if got, want := resultsJSON(t, failed.Results), resultsJSON(t, lexical.Results); got != want {
			t.Fatalf("max=%d: fall-open results differ from the lexical answer:\n got %s\nwant %s", maxFiles, got, want)
		}
		// The first search on a cache writes entries later ones hit, which legitimately changes
		// preselection stats; compare against a lexical run on the same (now warm) cache state.
		again := semanticSearch(t, repo, cacheDir, nil, limit)
		if got, want := resultsJSON(t, failed.Results), resultsJSON(t, again.Results); got != want {
			t.Fatalf("max=%d: fall-open results differ from a same-state lexical run", maxFiles)
		}
		if failed.Stats.FilesIndexed != again.Stats.FilesIndexed || failed.Stats.SymbolsConsidered != again.Stats.SymbolsConsidered ||
			failed.Stats.CandidatesSelected != again.Stats.CandidatesSelected {
			t.Fatalf("max=%d: fall-open indexed %d files/%d symbols/%d selected, lexical %d/%d/%d", maxFiles,
				failed.Stats.FilesIndexed, failed.Stats.SymbolsConsidered, failed.Stats.CandidatesSelected,
				again.Stats.FilesIndexed, again.Stats.SymbolsConsidered, again.Stats.CandidatesSelected)
		}
	}
}

// The caller's own cancellation during the nomination load is still an error, never a lexical
// answer dressed as success.
func TestSemanticNominationSnapshotCancellationIsAnError(t *testing.T) {
	t.Parallel()
	repo := semanticFixtureRepo(t)
	server := httptest.NewServer(&fakeEmbedder{})
	defer server.Close()
	cacheDir := t.TempDir()
	buildSemanticFixtureIndex(t, repo, cacheDir, server.URL)
	ctx, cancel := context.WithCancel(t.Context())
	ranked := 0
	defer cancel()
	_, err := SearchRepository(ctx, repo, "test-version", semanticFixtureQuery, SearchOptions{
		CacheDir: cacheDir, MaxIndexedFiles: 2,
		Semantic:                &SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel},
		nominationSnapshotFault: func() error { cancel(); return context.Canceled },
		// Stops at the nomination load: the lexical snapshot and ranking are never built for a
		// caller who has stopped asking.
		preFusionObserver: func([]searchCandidate) { ranked++ },
	})
	if ranked != 0 {
		t.Fatalf("search kept ranking after the caller canceled (observer ran %d times)", ranked)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled nomination load returned %v, want context.Canceled", err)
	}
}

// On the COLD path the plan also evicted lexical files to fund the nominations. A failed nomination
// load must undo the eviction too, or the fall-open answer is missing rows the unconfigured search
// returns (srv/other.go here).
func TestSemanticNominationSnapshotFailureUndoesColdEviction(t *testing.T) {
	repo := lexicalInvarianceRepo(t)
	writeFile(t, repo, "srv/zebra.go", "package srv\n\n// ZebraMoose answers.\nfunc ZebraMoose() string {\n\treturn \"quit zebra server moose\"\n}\n")
	writeFile(t, repo, "srv/other.go", "package srv\n\n// Other answers.\nfunc Other() string {\n\treturn \"quit server yak\"\n}\n")
	writeFile(t, repo, "srv/pool.go", "package srv\n\n// AbandonStalledBackend drops a stalled server backend: abandon it.\nfunc AbandonStalledBackend() {\n\tZz1()\n}\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "presence")
	server := httptest.NewServer(&fakeEmbedder{})
	defer server.Close()
	cacheDir := t.TempDir()
	buildSemanticFixtureIndex(t, repo, cacheDir, server.URL)
	const query = "quit sluggish server zebra yak moose"
	run := func(semantic *SemanticConfig, fault func() error) SearchResponse {
		t.Helper()
		response, err := SearchRepository(t.Context(), repo, "test-version", query, SearchOptions{
			CacheDir: cacheDir, MaxIndexedFiles: 3, TopK: 10, Profile: ProfileFast, Semantic: semantic,
			nominationSnapshotFault: fault,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Validate(); err != nil {
			t.Fatal(err)
		}
		return response
	}
	config := &SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel}
	run(nil, nil) // warm the selective cache entries both arms below hit
	used := run(config, nil)
	if used.Stats.SemanticEvictedFiles == 0 {
		t.Fatalf("fixture drift: nothing evicted: %+v", used.Stats)
	}
	failed := run(config, func() error { return errors.New("injected") })
	off := run(nil, nil)
	if failed.Stats.SemanticEvictedFiles != 0 || failed.Stats.SemanticNominatedFiles != 0 {
		t.Fatalf("fall-open still reports the plan: %+v", failed.Stats)
	}
	if got, want := resultsJSON(t, failed.Results), resultsJSON(t, off.Results); got != want {
		t.Fatalf("fall-open is not the unconfigured answer:\n got %v\nwant %v", resultFiles(failed.Results), resultFiles(off.Results))
	}
	if failed.Stats.FilesIndexed != off.Stats.FilesIndexed {
		t.Fatalf("fall-open indexed %d files, unconfigured %d", failed.Stats.FilesIndexed, off.Stats.FilesIndexed)
	}
}
