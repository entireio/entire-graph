package sem

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// RR-WARM: on the warm exact-preindex git-tree-grep path, a nomination must not change the
// lexical corpus (selected files / idf) relative to OFF.
func TestSemanticRereviewWarmPathLexicalCorpusUnchanged(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "upstream/pool.go", "package upstream\n\n// AbandonStalledBackend drops a stalled backend: abandon it.\nfunc AbandonStalledBackend() {}\n")
	var many strings.Builder
	many.WriteString("package util\n\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&many, "// Stalled%c is stalled.\nfunc Stalled%c() {}\n\n", 'A'+i, 'A'+i)
	}
	writeFile(t, repo, "util/many.go", many.String())
	for i := 1; i <= 4; i++ {
		writeFile(t, repo, fmt.Sprintf("srv/s%d.go", i), fmt.Sprintf("package srv\n\nfunc Zz%d() string {\n\treturn \"quit server\"\n}\n", i))
	}
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "fixture")
	server := httptest.NewServer(&fakeEmbedder{})
	defer server.Close()
	cacheDir := t.TempDir()
	snapshot, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{NoNetwork: true, Profile: ProfileFull}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildSemanticIndex(t.Context(), repo, snapshot, cacheDir, SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel}, false); err != nil {
		t.Fatal(err)
	}
	run := func(semantic *SemanticConfig) (SearchResponse, map[string]int, int) {
		var df map[string]int
		var files int
		options := SearchOptions{CacheDir: cacheDir, MaxIndexedFiles: 2, Semantic: semantic, TopK: 10, Profile: ProfileFull}
		options.idfObserver = func(d map[string]int, f int, _ bool) {
			df = map[string]int{}
			for k, v := range d {
				df[k] = v
			}
			files = f
		}
		response, err := SearchRepository(t.Context(), repo, "test-version", "quit sluggish server", options)
		if err != nil {
			t.Fatal(err)
		}
		return response, df, files
	}
	offResp, offDF, offN := run(nil)
	onResp, onDF, onN := run(&SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel})
	t.Logf("OFF backend=%s indexed=%d lexcand=%d df=%v N=%d", offResp.Stats.PreselectionBackend, offResp.Stats.FilesIndexed, offResp.Stats.LexicalCandidates, offDF, offN)
	t.Logf("ON  backend=%s indexed=%d lexcand=%d df=%v N=%d status=%s nominated=%d", onResp.Stats.PreselectionBackend, onResp.Stats.FilesIndexed, onResp.Stats.LexicalCandidates, onDF, onN, onResp.Stats.SemanticStatus, onResp.Stats.SemanticNominatedFiles)
	lexFiles := func(r SearchResponse) []string {
		var out []string
		for _, res := range r.Results {
			if strings.HasPrefix(res.FilePath, "srv/") {
				out = append(out, res.FilePath)
			}
		}
		sort.Strings(out)
		return out
	}
	t.Logf("OFF srv rows=%v ON srv rows=%v", lexFiles(offResp), lexFiles(onResp))
	if fmt.Sprint(offDF) != fmt.Sprint(onDF) || offN != onN {
		t.Errorf("ON arm lexical IDF differs from OFF: off=%v/%d on=%v/%d", offDF, offN, onDF, onN)
	}
	if offResp.Stats.LexicalCandidates != onResp.Stats.LexicalCandidates {
		t.Errorf("ON arm lexical candidate pool differs: off=%d on=%d", offResp.Stats.LexicalCandidates, onResp.Stats.LexicalCandidates)
	}
}

// RR-PROSE: expandProseResolution re-sorts by Score; a semantic-only rank-1 row (Score 0) sinks.
func TestSemanticRereviewProseExpansionSinksSemanticOnlyRow(t *testing.T) {
	results := []SearchResult{
		{Rank: 1, Score: 0, SemanticScore: 0.9, Signals: []string{semanticSignal, semanticOnlySignal}, FilePath: "a.go", StartLine: 1, EndLine: 2, SnippetStartLine: 1, SnippetEndLine: 2},
		{Rank: 2, Score: 5, FilePath: "doc.md", StartLine: 1, EndLine: 2, SnippetStartLine: 1, SnippetEndLine: 2,
			Passages: []SearchPassage{{StartLine: 10, EndLine: 12, FocusLine: 10, Snippet: "x"}}},
	}
	out := expandProseResolution(results, 5, 0, 0)
	var order []string
	for _, r := range out {
		order = append(order, fmt.Sprintf("%d:%s:%v", r.Rank, r.FilePath, r.Score))
	}
	t.Logf("order after prose expansion: %v", order)
	if out[0].FilePath != "a.go" {
		t.Errorf("embedding-first rank 1 undone by prose expansion: %v", order)
	}
}

// RR-FULLUNIT: a semantic-only rank 1 (Score 0) makes every rank "within gap".
func TestSemanticRereviewFullUnitGapWithSemanticOnlyHead(t *testing.T) {
	results := []SearchResult{{Score: 0, SemanticScore: 0.8}, {Score: 40}, {Score: 1}}
	got := searchFullUnitForceRanks(results, 3)
	t.Logf("forced ranks with semantic-only head = %d; callee-hop ranks = %d", got, searchCalleeHopRanks(results))
	if got != 1 {
		t.Errorf("semantic-only head forces %d full units (lexical-head case would gate on score gap)", got)
	}
}

// RR-Q1NEG: a semantic-only row with a non-positive cosine is not recognised as semantic-only.
func TestSemanticRereviewSemanticOnlyNonPositiveCosine(t *testing.T) {
	for _, cos := range []float64{0.2, 0, -0.15} {
		r := SearchResult{Score: 0, SemanticScore: cos, Signals: []string{semanticSignal, semanticOnlySignal}, FilePath: "a.go"}
		resp := SearchResponse{Results: []SearchResult{r, {Score: 40, FilePath: "b.go"}, {Score: 12, FilePath: "c.go"}}}
		a := AssessSearchConfidence(resp)
		t.Logf("cos=%v SemanticOnly=%v confidence=%+v", cos, r.SemanticOnly(), a)
		if !r.SemanticOnly() {
			t.Errorf("cos=%v: synthesized row not recognised as semantic-only (low=%v reason=%q)", cos, a.Low, a.Reason)
		}
	}
}

// RR-F1: loopback-host bypass table.
func TestSemanticRereviewF1Bypasses(t *testing.T) {
	cases := map[string]bool{
		"localhost": true, "LOCALHOST": true, "LocalHost": true, "127.0.0.1": true, "127.9.9.9": true, "::1": true,
		"::ffff:127.0.0.1": true,
		"localhost.":       false, "::ffff:8.8.8.8": false, "2130706433": false, "0177.0.0.1": false, "0x7f.0.0.1": false,
		"127.1": false, "0.0.0.0": false, "::": false, "::1%lo0": false, "foo.localhost": false, "127.0.0.1.nip.io": false,
		"[::1]": false, "8.8.8.8": false,
	}
	for host, want := range cases {
		if got := semanticLoopbackHost(host); got != want {
			t.Errorf("semanticLoopbackHost(%q)=%v want %v", host, got, want)
		}
	}
	for _, endpoint := range []string{
		"http://2130706433:11434", "http://0177.0.0.1:11434", "http://localhost.:11434", "http://[::ffff:8.8.8.8]:80",
		"http://127.0.0.1:11434@8.8.8.8", "http://8.8.8.8#@127.0.0.1", "http://[::1%25en0]:11434", "http://evil.com\\@127.0.0.1",
	} {
		_, err := semanticEmbed(context.Background(), &SemanticConfig{Endpoint: endpoint, Model: "m"}, []string{"x"}, 1024)
		t.Logf("%s -> %v", endpoint, err)
		if err == nil || !(strings.Contains(err.Error(), "not-loopback") || strings.Contains(err.Error(), "endpoint-invalid")) {
			t.Errorf("%s not refused before dial: %v", endpoint, err)
		}
	}
	for _, address := range []string{"[::ffff:8.8.8.8]:80", "8.8.8.8:80", "example.com:80", "localhost.:80", "2130706433:80"} {
		if _, err := semanticLoopbackDial(context.Background(), "tcp", address); err != errSemanticNotLoopback {
			t.Errorf("dial %s: %v", address, err)
		}
	}
}

// RR-F4: denormal and extreme inputs.
func TestSemanticRereviewF4Extremes(t *testing.T) {
	for _, vector := range [][]float64{
		{5e-324, 0}, {5e-324, 5e-324}, {1e308, -1e308}, {1e-310, 1, 1e308}, {math.SmallestNonzeroFloat64, math.MaxFloat64},
	} {
		out, err := normaliseSemanticVector(vector)
		var sum float64
		for _, v := range out {
			sum += float64(v) * float64(v)
		}
		t.Logf("%v -> %v err=%v sum=%v", vector, out, err, sum)
		if err == nil && math.Abs(sum-1) > 1e-3 {
			t.Errorf("%v accepted with non-unit norm %v", vector, sum)
		}
	}
	// persisted row: float32 denormals only -> must be refused
	index := semanticIndexFile{Recipe: semanticRecipeVersion, Model: "m", Tree: "t", Dimension: 2, Count: 1,
		Symbols: []semanticIndexSymbol{{FilePath: "a", StartLine: 1, Name: "a"}}}
	blob := make([]byte, 8)
	for i := 0; i < 2; i++ {
		bits := math.Float32bits(math.SmallestNonzeroFloat32)
		blob[i*4], blob[i*4+1], blob[i*4+2], blob[i*4+3] = byte(bits), byte(bits>>8), byte(bits>>16), byte(bits>>24)
	}
	index.Vectors = blob
	if err := index.validate("t", "m"); err == nil {
		t.Errorf("denormal persisted row accepted")
	}
	nan := math.Float32bits(float32(math.NaN()))
	blob2 := []byte{byte(nan), byte(nan >> 8), byte(nan >> 16), byte(nan >> 24), 0, 0, 0x80, 0x3f}
	index.Vectors = blob2
	if err := index.validate("t", "m"); err == nil {
		t.Errorf("NaN persisted row accepted")
	}
}

// RR-F3mid: cancel the parent while the embed response is in flight but still return a valid body.
func TestSemanticRereviewF3MidEmbedCancel(t *testing.T) {
	repo := semanticFixtureRepo(t)
	server := httptest.NewServer(&fakeEmbedder{})
	defer server.Close()
	cacheDir := t.TempDir()
	buildSemanticFixtureIndex(t, repo, cacheDir, server.URL)
	before := semanticCacheFiles(t, cacheDir)
	snapshot, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{NoNetwork: true}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	previous := semanticHTTPClient
	semanticHTTPClient = &http.Client{Transport: semanticFixRoundTripper(func(request *http.Request) (*http.Response, error) {
		var body semanticEmbedRequest
		_ = json.NewDecoder(request.Body).Decode(&body)
		vectors := make([][]float64, len(body.Input))
		for i, in := range body.Input {
			vectors[i] = fakeSemanticVector(in, fakeSemanticDims)
		}
		encoded, _ := json.Marshal(semanticEmbedResponse{Model: body.Model, Embeddings: vectors})
		cancel()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(encoded)), Request: request}, nil
	})}
	t.Cleanup(func() { semanticHTTPClient = previous })
	report, err := BuildSemanticIndex(ctx, repo, snapshot, cacheDir, SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel}, true)
	t.Logf("build: %+v err=%v", report, err)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-embed cancel not reported: %v", err)
	}
	if after := semanticCacheFiles(t, cacheDir); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("cache changed: %v -> %v", before, after)
	}
	if strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error leaks URL: %v", err)
	}
	// Query side: valid response but parent canceled during the call.
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	semanticHTTPClient = &http.Client{Transport: semanticFixRoundTripper(func(request *http.Request) (*http.Response, error) {
		encoded, _ := json.Marshal(semanticEmbedResponse{Model: semanticFixtureModel, Embeddings: [][]float64{fakeSemanticVector("quit", fakeSemanticDims)}})
		cancel2()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(encoded)), Request: request}, nil
	})}
	_, err = SearchRepository(ctx2, repo, "test-version", "zzqxv wqpzk", SearchOptions{CacheDir: cacheDir, MaxIndexedFiles: 2,
		Semantic: &SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel}})
	t.Logf("query: err=%v", err)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("query mid-embed cancel swallowed: %v", err)
	}
}
