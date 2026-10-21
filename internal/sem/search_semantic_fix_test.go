package sem

// Regressions added with the fixes for the peer review of the semantic channel (F1-F5, Q1-Q5).
// Every test here is offline: the HTTP transport is an in-process fake or a loopback httptest
// server, and nothing reaches a real model.

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// semanticFixRoundTripper is an in-process transport: it never dials.
type semanticFixRoundTripper func(*http.Request) (*http.Response, error)

func (transport semanticFixRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func semanticFixJSONClient(body string) *http.Client {
	return &http.Client{Transport: semanticFixRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(body)), Request: request,
		}, nil
	})}
}

// F4: literal vectors through the real client. Degenerate or overflowing answers are rejected as
// bad-response; extreme but meaningful magnitudes normalise to the true direction.
func TestSemanticFixF4NormalisationTable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		vector string
		want   []float32 // nil = must be rejected as bad-response
	}{
		{name: "ordinary", vector: `[3,4]`, want: []float32{0.6, 0.8}},
		{name: "zero", vector: `[0,0]`},
		{name: "overflowing_square", vector: `[1e308,1e308]`, want: []float32{float32(math.Sqrt2 / 2), float32(math.Sqrt2 / 2)}},
		{name: "overflowing_single", vector: `[-1e308,0]`, want: []float32{-1, 0}},
		{name: "subnormal", vector: `[5e-324,0]`, want: []float32{1, 0}},
		{name: "underflowing_square", vector: `[1e-200,1e-200]`, want: []float32{float32(math.Sqrt2 / 2), float32(math.Sqrt2 / 2)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := semanticHTTPClient
			semanticHTTPClient = semanticFixJSONClient(`{"model":"f4","embeddings":[` + tc.vector + `]}`)
			t.Cleanup(func() { semanticHTTPClient = previous })
			vectors, err := semanticEmbed(t.Context(), &SemanticConfig{Endpoint: "http://127.0.0.1", Model: "f4"},
				[]string{"search_query: x"}, 1024)
			if tc.want == nil {
				var failure *semanticUnavailable
				if !errors.As(err, &failure) || failure.reason != "bad-response" {
					t.Fatalf("degenerate vector %s: vectors=%v err=%v, want bad-response", tc.vector, vectors, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("vector %s rejected: %v", tc.vector, err)
			}
			for i, want := range tc.want {
				if got := vectors[0][i]; math.Abs(float64(got-want)) > 1e-6 {
					t.Fatalf("vector %s normalised to %v, want %v", tc.vector, vectors[0], tc.want)
				}
			}
		})
	}
}

// F4: the loader rejects a persisted row that is finite but not unit length (all-zero, huge,
// short-of-unit), because nearest reads its dot product as a cosine.
func TestSemanticFixF4PersistedRowNorms(t *testing.T) {
	for _, tc := range []struct {
		name  string
		row   []float32
		valid bool
	}{
		{name: "unit", row: []float32{0.6, 0.8}, valid: true},
		{name: "all_zero", row: []float32{0, 0}},
		{name: "huge_finite", row: []float32{3e38, 0}},
		{name: "half_length", row: []float32{0.5, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blob := make([]byte, 0, 4*len(tc.row))
			for _, value := range tc.row {
				blob = binary.LittleEndian.AppendUint32(blob, math.Float32bits(value))
			}
			index := semanticIndexFile{
				Recipe: semanticRecipeVersion, Model: "m", Tree: "t", Dimension: len(tc.row), Count: 1,
				Symbols: []semanticIndexSymbol{{FilePath: "a.go", StartLine: 1, Name: "A"}}, Vectors: blob,
			}
			err := index.validate("t", "m")
			if tc.valid && err != nil {
				t.Fatalf("unit row rejected: %v", err)
			}
			if !tc.valid && err == nil {
				t.Fatalf("non-unit persisted row %v accepted", tc.row)
			}
		})
	}
}

// F5: userinfo and fragment URL state are refused before any request (the peer test covers the
// query string), and nothing a cache file holds is quoted into the warning.
func TestSemanticFixF5NoSensitiveEcho(t *testing.T) {
	for _, endpoint := range []string{
		"http://user:SYNTHETIC_USERINFO_SECRET@127.0.0.1",
		"http://127.0.0.1/#SYNTHETIC_FRAGMENT_SECRET",
		"http://127.0.0.1/?",
	} {
		previous := semanticHTTPClient
		calls := 0
		semanticHTTPClient = &http.Client{Transport: semanticFixRoundTripper(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("SYNTHETIC_TRANSPORT_SECRET")
		})}
		_, err := semanticEmbed(t.Context(), &SemanticConfig{Endpoint: endpoint, Model: "m"}, []string{"q"}, 1024)
		semanticHTTPClient = previous
		var failure *semanticUnavailable
		if !errors.As(err, &failure) || failure.reason != "endpoint-invalid" || calls != 0 {
			t.Fatalf("%s: err=%v calls=%d, want endpoint-invalid before any request", endpoint, err, calls)
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("%s: error echoes URL state: %v", endpoint, err)
		}
	}

	// A cache entry naming a foreign model: its string must not reach the warning.
	cacheDir := t.TempDir()
	entry, err := semanticIndexEntry(cacheDir, "tree", "wanted-model")
	if err != nil {
		t.Fatal(err)
	}
	if err := entry.write("semantic", semanticIndexFile{
		Recipe: semanticRecipeVersion, Model: "SYNTHETIC_CACHE_MODEL_SECRET", Tree: "tree", Dimension: 1,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = loadSemanticIndex(cacheDir, "tree", "wanted-model")
	var failure *semanticUnavailable
	if !errors.As(err, &failure) || failure.reason != "invalid-index" {
		t.Fatalf("foreign-model entry: %v, want invalid-index", err)
	}
	var response SearchResponse
	applySemanticOutcome(&response, &semanticOutcome{status: semanticUnavailablePrefix + failure.reason, detail: failure.detail})
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(response.Warnings[0].Detail, "SECRET") {
		t.Fatalf("cache-held string echoed: err=%v warning=%q", err, response.Warnings[0].Detail)
	}
}

// F1: only localhost or a literal loopback address is accepted, before any request is built, and
// the production transport neither consults a proxy nor resolves names. No test here dials a
// non-loopback address: the dialer case runs on an already-canceled context, so a dialer that
// stopped refusing would fail with context.Canceled instead of reaching the network.
func TestSemanticFixF1LoopbackOnly(t *testing.T) {
	for _, tc := range []struct {
		endpoint string
		allowed  bool
	}{
		{"http://127.0.0.1:11434", true},
		{"http://127.9.9.9", true},
		{"http://localhost:11434", true},
		{"http://LOCALHOST", true},
		{"http://[::1]:11434", true},
		{"http://10.0.0.1:11434", false},
		{"http://192.0.2.1", false},
		{"https://embed.example.com", false},
		{"http://127.0.0.1.nip.io", false},
		{"http://localhost.localdomain", false},
		{"http://[::ffff:192.0.2.1]", false},
		{"http://0.0.0.0:11434", false},
	} {
		previous := semanticHTTPClient
		calls := 0
		semanticHTTPClient = &http.Client{Transport: semanticFixRoundTripper(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("offline")
		})}
		_, err := semanticEmbed(t.Context(), &SemanticConfig{Endpoint: tc.endpoint, Model: "m"}, []string{"q"}, 1024)
		semanticHTTPClient = previous
		var failure *semanticUnavailable
		if !errors.As(err, &failure) {
			t.Fatalf("%s: unclassified error %v", tc.endpoint, err)
		}
		if tc.allowed && (failure.reason != "endpoint" || calls != 1) {
			t.Fatalf("%s: loopback endpoint refused: reason=%q calls=%d", tc.endpoint, failure.reason, calls)
		}
		if !tc.allowed && (failure.reason != "endpoint-not-loopback" || calls != 0) {
			t.Fatalf("%s: non-loopback endpoint admitted: reason=%q calls=%d", tc.endpoint, failure.reason, calls)
		}
	}

	transport, ok := semanticHTTPClient.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || transport.DialContext == nil {
		t.Fatalf("production transport must be proxy-free with the loopback dialer: %#v", semanticHTTPClient.Transport)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, address := range []string{"192.0.2.1:80", "embed.example.com:443", "127.0.0.1.nip.io:80"} {
		if _, err := transport.DialContext(canceled, "tcp", address); !errors.Is(err, errSemanticNotLoopback) {
			t.Fatalf("dial %s: err=%v, want the loopback refusal", address, err)
		}
	}

	// "localhost" reaches a loopback daemon through the production client without a resolver.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"model":"m","embeddings":[[1,0]]}`))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := semanticEmbed(t.Context(), &SemanticConfig{Endpoint: "http://localhost:" + parsed.Port(), Model: "m"}, []string{"q"}, 1024)
	if err != nil || len(vectors) != 1 {
		t.Fatalf("localhost endpoint through the production client: %v", err)
	}
}

// F3: the CALLER's cancellation during the semantic request is an error, not a successful
// lexical answer — here with a query that selects no lexical file at all, the case the review
// showed returning an empty success. A channel deadline (the caller still waiting) stays a
// disclosed fallback; TestSemanticChannelFailsOpen/hang covers that half.
func TestSemanticFixF3QueryCancellationPropagates(t *testing.T) {
	repo := semanticFixtureRepo(t)
	server := httptest.NewServer(&fakeEmbedder{})
	defer server.Close()
	cacheDir := t.TempDir()
	buildSemanticFixtureIndex(t, repo, cacheDir, server.URL)
	config := &SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel}
	const noLexicalHit = "zzqxv wqpzk"

	control, err := SearchRepository(t.Context(), repo, "test-version", noLexicalHit,
		SearchOptions{CacheDir: cacheDir, MaxIndexedFiles: 2, Semantic: config})
	if err != nil || control.Stats.SemanticStatus != SemanticStatusUsed {
		t.Fatalf("NON-VACUITY: uncanceled control err=%v status=%q", err, control.Stats.SemanticStatus)
	}
	lexical, err := SearchRepository(t.Context(), repo, "test-version", noLexicalHit,
		SearchOptions{CacheDir: cacheDir, MaxIndexedFiles: 2})
	if err != nil || len(lexical.Results) != 0 {
		t.Fatalf("NON-VACUITY: the query must select no lexical result: err=%v results=%d", err, len(lexical.Results))
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	previous := semanticHTTPClient
	semanticHTTPClient = &http.Client{Transport: semanticFixRoundTripper(func(request *http.Request) (*http.Response, error) {
		cancel()
		return nil, request.Context().Err()
	})}
	t.Cleanup(func() { semanticHTTPClient = previous })
	response, err := SearchRepository(ctx, repo, "test-version", noLexicalHit,
		SearchOptions{CacheDir: cacheDir, MaxIndexedFiles: 2, Semantic: config})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled search returned err=%v status=%q results=%d, want context.Canceled",
			err, response.Stats.SemanticStatus, len(response.Results))
	}
}

// F3: load and scan stop on a done context instead of running to completion.
func TestSemanticFixF3LoadAndScanHonourContext(t *testing.T) {
	cacheDir := t.TempDir()
	entry, err := semanticIndexEntry(cacheDir, "tree", "m")
	if err != nil {
		t.Fatal(err)
	}
	blob := binary.LittleEndian.AppendUint32(nil, math.Float32bits(1))
	index := semanticIndexFile{Recipe: semanticRecipeVersion, Model: "m", Tree: "tree", Dimension: 1, Count: 1,
		Symbols: []semanticIndexSymbol{{FilePath: "a.go", StartLine: 1, Name: "A"}}, Vectors: blob}
	if err := entry.write("semantic", index); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSemanticIndexContext(t.Context(), cacheDir, "tree", "m"); err != nil {
		t.Fatalf("NON-VACUITY: live load failed: %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := loadSemanticIndexContext(canceled, cacheDir, "tree", "m"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled load: %v, want context.Canceled", err)
	}
	if err := index.validate("tree", "m"); err != nil {
		t.Fatal(err)
	}
	if hits, err := index.nearest(t.Context(), []float32{1}, 1); err != nil || len(hits) != 1 {
		t.Fatalf("NON-VACUITY: live scan %v %v", hits, err)
	}
	if _, err := index.nearest(canceled, []float32{1}, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scan: %v, want context.Canceled", err)
	}
}

// F3: a build whose context is already done never publishes, and reports the cancellation.
func TestSemanticFixF3CanceledBuildNeverPublishes(t *testing.T) {
	repo := semanticFixtureRepo(t)
	server := httptest.NewServer(&fakeEmbedder{})
	defer server.Close()
	cacheDir := t.TempDir()
	first := buildSemanticFixtureIndex(t, repo, cacheDir, server.URL)
	before := semanticCacheFiles(t, cacheDir)
	snapshot, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{NoNetwork: true}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	report, err := BuildSemanticIndex(canceled, repo, snapshot, cacheDir,
		SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel}, true)
	if !errors.Is(err, context.Canceled) || report.Status == "built" {
		t.Fatalf("canceled forced rebuild: report=%+v err=%v, want context.Canceled and no build", report, err)
	}
	if after := semanticCacheFiles(t, cacheDir); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("canceled build changed the cache: %v -> %v", before, after)
	}
	if reloaded, err := loadSemanticIndex(cacheDir, snapshot.Header.Tree, semanticFixtureModel); err != nil || reloaded.Count != first.Symbols {
		t.Fatalf("previous generation not intact: %v", err)
	}
}

// F2 at query time: an index prepared under one ignore policy is used only by a search under the
// same policy; a search whose corpus is wider treats it as missing rather than trusting it.
func TestSemanticFixF2QueryRequiresTheSameCorpus(t *testing.T) {
	repo := semanticFixtureRepo(t)
	server := httptest.NewServer(&fakeEmbedder{})
	defer server.Close()
	cacheDir := t.TempDir()
	ignore := t.TempDir() + "/ignore"
	writeFile(t, ignore[:strings.LastIndex(ignore, "/")], "ignore", "util/\n")
	narrow := ProviderSnapshotOptions{NoNetwork: true, IgnoreFiles: []string{ignore}}
	snapshot, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", narrow, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, symbol := range snapshot.Symbols {
		if strings.HasPrefix(symbol.FilePath, "util/") {
			t.Fatalf("NON-VACUITY: the narrow snapshot still holds %s", symbol.FilePath)
		}
	}
	config := SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel}
	if report, err := BuildSemanticIndex(t.Context(), repo, snapshot, cacheDir, config, false); err != nil || report.Status != "built" {
		t.Fatalf("narrow build: %+v %v", report, err)
	}
	same := semanticSearch(t, repo, cacheDir, &config, func(options *SearchOptions) { options.IgnoreFiles = []string{ignore} })
	if same.Stats.SemanticStatus != SemanticStatusUsed {
		t.Fatalf("same-policy search: status %q, want used", same.Stats.SemanticStatus)
	}
	// The wider search prepares its own complete snapshot first, so a corpus exists to compare.
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{NoNetwork: true}, cacheDir); err != nil {
		t.Fatal(err)
	}
	wider := semanticSearch(t, repo, cacheDir, &config, nil)
	if wider.Stats.SemanticStatus != "unavailable:no-index" || wider.Stats.SemanticNominatedFiles != 0 {
		t.Fatalf("wider-policy search: status %q nominated %d, want unavailable:no-index and no nomination",
			wider.Stats.SemanticStatus, wider.Stats.SemanticNominatedFiles)
	}
	// No prepared snapshot for the search's policy at all: nothing proves the corpus either.
	other := t.TempDir() + "/other"
	writeFile(t, other[:strings.LastIndex(other, "/")], "other", "api/\n")
	unprepared := semanticSearch(t, repo, cacheDir, &config, func(options *SearchOptions) { options.IgnoreFiles = []string{other} })
	if unprepared.Stats.SemanticStatus != "unavailable:no-index" {
		t.Fatalf("unprepared-policy search: status %q, want unavailable:no-index", unprepared.Stats.SemanticStatus)
	}
}

// Q3: fusion dedupe is by symbol identity. Two distinct symbols that start on the same line both
// survive; two rows of ONE symbol collapse to one.
func TestSemanticFixQ3FusionDedupesBySymbolID(t *testing.T) {
	row := func(id string, score float64, semanticOnly bool) searchCandidate {
		return searchCandidate{result: SearchResult{FilePath: "same.js", SymbolID: id, SymbolName: id,
			StartLine: 1, EndLine: 1, FocusLine: 1, SymbolStartLine: 1}, score: score, semanticOnly: semanticOnly}
	}
	keyOf := func(candidate searchCandidate) semanticKey { return semanticCandidateKey(candidate, nil) }
	fused, _ := fuseSemanticCandidates([]searchCandidate{row("symbol-a", 40, false)}, []searchCandidate{row("symbol-b", 0.9, true)}, 5, keyOf)
	if len(fused) != 2 {
		t.Fatalf("distinct same-line symbols collapsed: %d rows", len(fused))
	}
	fused, _ = fuseSemanticCandidates([]searchCandidate{row("symbol-a", 40, false)}, []searchCandidate{row("symbol-a", 0.9, false)}, 5, keyOf)
	if len(fused) != 1 {
		t.Fatalf("one symbol seated twice: %d rows", len(fused))
	}
}

// Q1: confidence reads the lexically scored rows. A synthesized head neither ties with, nor
// weakens, a strong lexical answer; a weak lexical answer is still marked weak; an all-semantic
// payload is marked because nothing lexical vouches for it.
func TestSemanticFixQ1ConfidenceReadsLexicalRows(t *testing.T) {
	semantic := SearchResult{FilePath: "dense.go", Score: 0, SemanticScore: 0.9, Signals: []string{semanticSignal, semanticOnlySignal}}
	strong := SearchResult{FilePath: "strong.go", Score: 40}
	next := SearchResult{FilePath: "next.go", Score: 20}
	weak := SearchResult{FilePath: "weak.go", Score: 2}
	for _, tc := range []struct {
		name    string
		results []SearchResult
		low     bool
	}{
		{name: "semantic_head_strong_lexical", results: []SearchResult{semantic, strong, next}},
		{name: "semantic_head_weak_lexical", results: []SearchResult{semantic, weak}, low: true},
		{name: "all_semantic", results: []SearchResult{semantic, semantic}, low: true},
	} {
		assessment := AssessSearchConfidence(SearchResponse{Results: tc.results})
		if assessment.Low != tc.low {
			t.Fatalf("%s: %+v, want low=%v", tc.name, assessment, tc.low)
		}
		if strings.Contains(assessment.Reason, "tied") {
			t.Fatalf("%s: manufactured tie: %+v", tc.name, assessment)
		}
	}
}

// Q5 unit (COLD path; the warm path is additive — see TestSemanticPlanIsAdditiveWhenWarm): nominations
// are spent inside the cap. Free slots first, then the lexical tail yields;
// the lexical head and a file a hit points into never yield; an over-cap selection never grows.
func TestSemanticFixQ5NominationStaysInsideTheCap(t *testing.T) {
	corpus := []string{"l1.go", "l2.go", "l3.go", "n1.go", "n2.go", "n3.go"}
	hits := func(files ...string) []semanticHit {
		out := make([]semanticHit, 0, len(files))
		for _, file := range files {
			out = append(out, semanticHit{FilePath: file})
		}
		return out
	}
	for _, tc := range []struct {
		name      string
		selected  []string
		hits      []semanticHit
		maxFiles  int
		want      string
		nominated int
	}{
		{name: "free_slots_first", selected: []string{"l1.go"}, hits: hits("n1.go", "n2.go"), maxFiles: 3, want: "l1.go,n1.go,n2.go", nominated: 2},
		{name: "tail_yields", selected: []string{"l1.go", "l2.go", "l3.go"}, hits: hits("n1.go", "n2.go"), maxFiles: 3, want: "l1.go,n1.go,n2.go", nominated: 2},
		{name: "head_never_yields", selected: []string{"l1.go"}, hits: hits("n1.go"), maxFiles: 1, want: "l1.go", nominated: 0},
		{name: "hit_file_never_yields", selected: []string{"l1.go", "l2.go"}, hits: hits("l2.go", "n1.go"), maxFiles: 2, want: "l1.go,l2.go", nominated: 0},
		{name: "over_cap_selection_swaps", selected: []string{"l1.go", "l2.go", "l3.go"}, hits: hits("n1.go"), maxFiles: 1, want: "l1.go,l2.go,n1.go", nominated: 1},
		{name: "empty_lexical", selected: nil, hits: hits("n1.go", "n2.go", "n3.go"), maxFiles: 2, want: "n1.go,n2.go", nominated: 2},
	} {
		out, nominated := nominateSemanticFiles(tc.selected, corpus, tc.hits, semanticTopK, tc.maxFiles)
		if strings.Join(out, ",") != tc.want || nominated != tc.nominated {
			t.Fatalf("%s: %v (%d nominated), want %s (%d)", tc.name, out, nominated, tc.want, tc.nominated)
		}
		if limit := max(tc.maxFiles, len(tc.selected)); len(out) > limit {
			t.Fatalf("%s: %d files exceed the cap %d", tc.name, len(out), limit)
		}
	}
}

// Q5 end to end, COLD path (index prepared at full, search at fast): with --max-indexed-files 1
// the channel never indexes a second file; the lexical
// answer keeps its only slot.
func TestSemanticFixQ5EndToEndCapIsHard(t *testing.T) {
	repo := semanticFixtureRepo(t)
	server := httptest.NewServer(&fakeEmbedder{})
	defer server.Close()
	cacheDir := t.TempDir()
	buildSemanticFixtureIndex(t, repo, cacheDir, server.URL)
	config := &SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel}
	for _, maxFiles := range []int{1, 2} {
		response := semanticSearch(t, repo, cacheDir, config, func(options *SearchOptions) { options.MaxIndexedFiles = maxFiles })
		if response.Stats.SemanticStatus != SemanticStatusUsed {
			t.Fatalf("max %d: status %q", maxFiles, response.Stats.SemanticStatus)
		}
		if response.Stats.FilesIndexed > maxFiles {
			t.Fatalf("max %d: indexed %d files (nominated %d)", maxFiles, response.Stats.FilesIndexed, response.Stats.SemanticNominatedFiles)
		}
	}
}

// nominateSemanticFiles is the file set a COLD plan parses — the lexical selection minus
// evictions, then the nominations — in the shape the Q5 tables above were written against.
func nominateSemanticFiles(selected, corpus []string, hits []semanticHit, budget, maxFiles int) ([]string, int) {
	plan := planSemanticNominations(selected, corpus, hits, budget, maxFiles, false)
	if len(plan.nominated) == 0 {
		return selected, 0
	}
	out := append(append([]string(nil), plan.withoutEvicted(selected)...), plan.nominated...)
	return out, len(plan.nominated)
}
