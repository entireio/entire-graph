package sem

import (
	"encoding/json"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"
)

// The fake embedder is the ONLY model these tests talk to: no network beyond loopback, no paid
// call. It is deterministic by construction — a text's vector is a bag of its words hashed into
// fakeSemanticDims dimensions — with a small synonym table that folds words an agent would type
// and words the code uses onto shared "concept" dimensions. That table is the whole mechanism
// under test: it makes a symbol the NEAREST neighbour of a query that shares no word with it, which
// is exactly the case lexical ranking cannot reach.
const fakeSemanticDims = 16

var fakeSemanticConcepts = map[string]int{
	// quit ~ abandon, sluggish ~ stalled. Dimensions 0-1 are reserved for concepts; every other
	// word hashes into 2..fakeSemanticDims-1.
	"quit": 0, "abandon": 0,
	"sluggish": 1, "stalled": 1,
}

func fakeSemanticVector(text string, dims int) []float64 {
	text = strings.TrimPrefix(strings.TrimPrefix(text, semanticQueryPrefix), semanticDocumentPrefix)
	vector := make([]float64, dims)
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) }) {
		if concept, ok := fakeSemanticConcepts[word]; ok {
			// Concepts weigh more than incidental words so the fixture's nearest neighbour does not
			// hinge on which hashed words happen to collide.
			vector[concept] += 4
			continue
		}
		hash := fnv.New32a()
		_, _ = hash.Write([]byte(word))
		vector[2+int(hash.Sum32())%(dims-2)]++
	}
	return vector
}

type fakeEmbedder struct {
	mode     string // "", "500", "hang", "wrong-dim", "wrong-model", "garbage"
	model    string
	requests atomic.Int32
	queries  atomic.Int32
}

func (fake *fakeEmbedder) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	fake.requests.Add(1)
	if request.Method != http.MethodPost || request.URL.Path != "/api/embed" {
		http.Error(writer, "not found", http.StatusNotFound)
		return
	}
	var body semanticEmbedRequest
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	isQuery := len(body.Input) == 1 && strings.HasPrefix(body.Input[0], semanticQueryPrefix)
	if isQuery {
		fake.queries.Add(1)
	}
	// Failure modes apply to QUERIES only, so an index can be built against the same server that
	// then fails at search time.
	if isQuery {
		switch fake.mode {
		case "500":
			http.Error(writer, "boom", http.StatusInternalServerError)
			return
		case "hang":
			select {
			case <-request.Context().Done():
			case <-time.After(5 * time.Second):
			}
			return
		case "garbage":
			_, _ = writer.Write([]byte(`{"embeddings": "not a list"`))
			return
		}
	}
	model := body.Model
	if isQuery && fake.mode == "wrong-model" {
		model = "some-other-model"
	}
	dims := fakeSemanticDims
	if isQuery && fake.mode == "wrong-dim" {
		dims = fakeSemanticDims + 3
	}
	vectors := make([][]float64, 0, len(body.Input))
	for _, input := range body.Input {
		vectors = append(vectors, fakeSemanticVector(input, dims))
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(semanticEmbedResponse{Model: model, Embeddings: vectors})
}

const semanticFixtureModel = "fake-embed"

// semanticFixtureQuery shares NO word with the target symbol's file: lexical ranking can only find
// the decoy, which says "server".
const semanticFixtureQuery = "quit sluggish server"

func semanticFixtureRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "upstream/pool.go", `package upstream

// Conn is one pooled backend connection.
type Conn struct{ open bool }

// AbandonStalledBackend drops a stalled backend connection: abandon it rather than wait.
func AbandonStalledBackend(conn *Conn) error {
	conn.open = false
	return nil
}
`)
	writeFile(t, repo, "api/handler.go", `package api

// HandleServer answers one server request.
func HandleServer(request string) string {
	return "server " + request
}

// ServerName names the server.
func ServerName() string {
	return "server"
}
`)
	writeFile(t, repo, "util/strings.go", `package util

// Reverse reverses a string.
func Reverse(value string) string {
	runes := []rune(value)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}
`)
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "fixture")
	return repo
}

// buildSemanticFixtureIndex builds the committed-tree snapshot and the semantic index for repo's
// HEAD into cacheDir, exactly as `index --semantic` does.
func buildSemanticFixtureIndex(t *testing.T, repo, cacheDir, endpoint string) SemanticIndexReport {
	t.Helper()
	snapshot, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version",
		ProviderSnapshotOptions{NoNetwork: true}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	report, err := BuildSemanticIndex(t.Context(), repo, snapshot, cacheDir,
		SemanticConfig{Endpoint: endpoint, Model: semanticFixtureModel}, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "built" || report.Symbols < 4 || report.Dimension != fakeSemanticDims {
		t.Fatalf("fixture index is not what the tests assume: %+v", report)
	}
	return report
}

func semanticSearch(t *testing.T, repo, cacheDir string, semantic *SemanticConfig, mutate func(*SearchOptions)) SearchResponse {
	t.Helper()
	options := SearchOptions{CacheDir: cacheDir, MaxIndexedFiles: 1, Semantic: semantic}
	if mutate != nil {
		mutate(&options)
	}
	response, err := SearchRepository(t.Context(), repo, "test-version", semanticFixtureQuery, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
	return response
}

func resultsJSON(t *testing.T, results []SearchResult) string {
	t.Helper()
	encoded, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func resultFiles(results []SearchResult) []string {
	files := make([]string, 0, len(results))
	for _, result := range results {
		files = append(files, result.FilePath+":"+result.SymbolName)
	}
	return files
}

func countWarnings(warnings []ProviderWarning, code string) int {
	count := 0
	for _, warning := range warnings {
		if warning.Code == code {
			count++
		}
	}
	return count
}

// TestSemanticChannelNominatesAndRanksTargetFirst is the channel's reason to exist. The lexical
// ranking — with MaxIndexedFiles 1, so the target's file is not even indexed — cannot return the
// target; the embedding makes it the nearest neighbour; with the channel on it is rank 1, because
// the fusion is embedding-first, and the file it lives in was indexed through the separate
// nomination budget that stats.semantic_nominated_files discloses.
func TestSemanticChannelNominatesAndRanksTargetFirst(t *testing.T) {
	t.Parallel()
	repo := semanticFixtureRepo(t)
	fake := &fakeEmbedder{}
	server := httptest.NewServer(fake)
	defer server.Close()
	cacheDir := t.TempDir()
	buildSemanticFixtureIndex(t, repo, cacheDir, server.URL)

	lexical := semanticSearch(t, repo, cacheDir, nil, nil)
	// NON-VACUITY: the fixture only proves nomination while lexical search genuinely misses the
	// target and genuinely indexes fewer files than the channel does.
	for _, result := range lexical.Results {
		if result.FilePath == "upstream/pool.go" {
			t.Fatalf("fixture drift: lexical search already returns the target: %v", resultFiles(lexical.Results))
		}
	}
	if len(lexical.Results) == 0 || lexical.Stats.FilesIndexed != 1 {
		t.Fatalf("fixture drift: want a lexical answer from exactly one indexed file, got %d results over %d files",
			len(lexical.Results), lexical.Stats.FilesIndexed)
	}

	semantic := semanticSearch(t, repo, cacheDir, &SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel}, nil)
	if semantic.Stats.SemanticStatus != SemanticStatusUsed {
		t.Fatalf("semantic status = %q, want used (warnings %+v)", semantic.Stats.SemanticStatus, semantic.Warnings)
	}
	if len(semantic.Results) == 0 || semantic.Results[0].FilePath != "upstream/pool.go" ||
		semantic.Results[0].SymbolName != "AbandonStalledBackend" {
		t.Fatalf("rank 1 = %v, want upstream/pool.go:AbandonStalledBackend", resultFiles(semantic.Results))
	}
	if semantic.Stats.SemanticNominatedFiles < 1 || semantic.Stats.FilesIndexed != lexical.Stats.FilesIndexed+semantic.Stats.SemanticNominatedFiles {
		t.Fatalf("nomination not disclosed: nominated=%d indexed=%d lexical indexed=%d",
			semantic.Stats.SemanticNominatedFiles, semantic.Stats.FilesIndexed, lexical.Stats.FilesIndexed)
	}
	if semantic.Stats.SemanticResults < 1 || countWarnings(semantic.Warnings, "W_SEMANTIC_UNAVAILABLE") != 0 {
		t.Fatalf("stats/warnings wrong for a used channel: %+v %+v", semantic.Stats, semantic.Warnings)
	}
	for index, result := range semantic.Results {
		if index > 0 && result.Score > semantic.Results[index-1].Score {
			t.Fatalf("fused scores increase at rank %d: %v", result.Rank, semantic.Results)
		}
	}
}

// TestSemanticChannelKeepsLexicalRankOneInTopTwo is the control the fusion rule was frozen with:
// the lexical rank-1 hit may be displaced by at most the one embedding row seated ahead of it.
func TestSemanticChannelKeepsLexicalRankOneInTopTwo(t *testing.T) {
	t.Parallel()
	repo := semanticFixtureRepo(t)
	server := httptest.NewServer(&fakeEmbedder{})
	defer server.Close()
	cacheDir := t.TempDir()
	buildSemanticFixtureIndex(t, repo, cacheDir, server.URL)
	// Unbounded selection, so lexical ranks every file it can.
	wide := func(options *SearchOptions) { options.MaxIndexedFiles = 0 }
	lexical := semanticSearch(t, repo, cacheDir, nil, wide)
	if len(lexical.Results) == 0 {
		t.Fatal("fixture drift: lexical search returned nothing")
	}
	top := lexical.Results[0]
	semantic := semanticSearch(t, repo, cacheDir, &SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel}, wide)
	if semantic.Stats.SemanticStatus != SemanticStatusUsed {
		t.Fatalf("semantic status = %q", semantic.Stats.SemanticStatus)
	}
	position := 0
	for index, result := range semantic.Results {
		if result.FilePath == top.FilePath && result.SymbolName == top.SymbolName {
			position = index + 1
			break
		}
	}
	if position == 0 || position > 2 {
		t.Fatalf("lexical rank 1 %s:%s is at position %d under the channel, want <= 2: %v",
			top.FilePath, top.SymbolName, position, resultFiles(semantic.Results))
	}
	// NON-VACUITY: position 1 would also pass if the channel did nothing; it must actually have
	// seated an embedding row ahead of the lexical winner here.
	if semantic.Results[0].FilePath == top.FilePath && semantic.Results[0].SymbolName == top.SymbolName {
		t.Fatalf("channel seated nothing ahead of the lexical winner: %v", resultFiles(semantic.Results))
	}
}

// TestSemanticChannelFailsOpen: every way the channel can fail returns the LEXICAL answer, byte for
// byte in its results, reports unavailable:<reason>, carries one warning, and does not panic.
func TestSemanticChannelFailsOpen(t *testing.T) {
	t.Parallel()
	repo := semanticFixtureRepo(t)
	good := httptest.NewServer(&fakeEmbedder{})
	defer good.Close()
	cacheDir := t.TempDir()
	buildSemanticFixtureIndex(t, repo, cacheDir, good.URL)
	lexical := semanticSearch(t, repo, cacheDir, nil, nil)
	want := resultsJSON(t, lexical.Results)

	for _, tc := range []struct {
		name, mode, model, wantStatus string
		closed                        bool
	}{
		{name: "http-500", mode: "500", wantStatus: "unavailable:http-500"},
		{name: "hang", mode: "hang", wantStatus: "unavailable:timeout"},
		{name: "wrong-dimension", mode: "wrong-dim", wantStatus: "unavailable:dimension-mismatch"},
		{name: "wrong-model-answer", mode: "wrong-model", wantStatus: "unavailable:model-mismatch"},
		{name: "garbage", mode: "garbage", wantStatus: "unavailable:bad-response"},
		{name: "no-index-for-model", model: "other-model", wantStatus: "unavailable:no-index"},
		{name: "endpoint-down", closed: true, wantStatus: "unavailable:endpoint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeEmbedder{mode: tc.mode}
			server := httptest.NewServer(fake)
			endpoint := server.URL
			if tc.closed {
				server.Close()
			} else {
				defer server.Close()
			}
			model := semanticFixtureModel
			if tc.model != "" {
				model = tc.model
			}
			started := time.Now()
			response := semanticSearch(t, repo, cacheDir,
				&SemanticConfig{Endpoint: endpoint, Model: model, QueryTimeout: 300 * time.Millisecond}, nil)
			if response.Stats.SemanticStatus != tc.wantStatus {
				t.Fatalf("status = %q, want %q", response.Stats.SemanticStatus, tc.wantStatus)
			}
			if got := resultsJSON(t, response.Results); got != want {
				t.Fatalf("fail-open results differ from lexical:\n got %s\nwant %s", got, want)
			}
			if response.Stats.SemanticNominatedFiles != 0 || response.Stats.SemanticResults != 0 {
				t.Fatalf("a failed channel still spent budget: %+v", response.Stats)
			}
			if countWarnings(response.Warnings, "W_SEMANTIC_UNAVAILABLE") != 1 {
				t.Fatalf("want exactly one W_SEMANTIC_UNAVAILABLE warning, got %+v", response.Warnings)
			}
			if tc.mode == "hang" && time.Since(started) > 4*time.Second {
				t.Fatalf("hang was not bounded by the query timeout: %s", time.Since(started))
			}
			if tc.mode != "" && fake.queries.Load() == 0 {
				t.Fatalf("NON-VACUITY: the failure mode %q was never exercised", tc.mode)
			}
		})
	}
}

// TestSemanticIndexIsKeyedByTree: an index describes one committed tree. After a commit that
// changes the tree, the old index must not be found — the channel reports no-index and the answer
// is lexical, even though the endpoint and model are unchanged and would happily answer.
func TestSemanticIndexIsKeyedByTree(t *testing.T) {
	t.Parallel()
	repo := semanticFixtureRepo(t)
	fake := &fakeEmbedder{}
	server := httptest.NewServer(fake)
	defer server.Close()
	cacheDir := t.TempDir()
	buildSemanticFixtureIndex(t, repo, cacheDir, server.URL)
	config := &SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel}
	if before := semanticSearch(t, repo, cacheDir, config, nil); before.Stats.SemanticStatus != SemanticStatusUsed {
		t.Fatalf("NON-VACUITY: the index was not usable on its own tree: %q", before.Stats.SemanticStatus)
	}
	// Shift every symbol in the target file down so an index read against the new tree would point
	// at the wrong lines, then commit.
	writeFile(t, repo, "upstream/pool.go", "// Package upstream pools backends.\n\n"+readFileString(t, filepath.Join(repo, "upstream/pool.go")))
	git(t, repo, "commit", "-am", "move symbols")
	after := semanticSearch(t, repo, cacheDir, config, nil)
	if after.Stats.SemanticStatus != "unavailable:no-index" {
		t.Fatalf("status after a tree change = %q, want unavailable:no-index", after.Stats.SemanticStatus)
	}
	for _, result := range after.Results {
		if result.FilePath == "upstream/pool.go" {
			t.Fatalf("a stale index still shaped the ranking: %v", resultFiles(after.Results))
		}
	}
}

// TestSemanticChannelNeverRunsInWorktreeMode: a worktree has no durable identity, so the channel is
// not consulted at all — no request reaches the endpoint — and the payload is the worktree lexical
// payload.
func TestSemanticChannelNeverRunsInWorktreeMode(t *testing.T) {
	t.Parallel()
	repo := semanticFixtureRepo(t)
	fake := &fakeEmbedder{}
	server := httptest.NewServer(fake)
	defer server.Close()
	cacheDir := t.TempDir()
	buildSemanticFixtureIndex(t, repo, cacheDir, server.URL)
	built := fake.requests.Load()
	worktree := func(options *SearchOptions) { options.Worktree = true }
	lexical := semanticSearch(t, repo, cacheDir, nil, worktree)
	response := semanticSearch(t, repo, cacheDir, &SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel}, worktree)
	if response.Stats.SemanticStatus != SemanticStatusOffWorktree {
		t.Fatalf("status = %q, want %q", response.Stats.SemanticStatus, SemanticStatusOffWorktree)
	}
	if got := fake.requests.Load(); got != built {
		t.Fatalf("worktree search made %d embedding requests", got-built)
	}
	if resultsJSON(t, response.Results) != resultsJSON(t, lexical.Results) || len(response.Warnings) != len(lexical.Warnings) {
		t.Fatalf("worktree payload changed under a configured channel")
	}
}

// TestSemanticChannelUnconfiguredIsByteIdentical: with no configuration — nil, or only one of the
// two settings — the whole response is the baseline's, byte for byte (latencies zeroed), even with
// a valid index sitting in the cache and a live endpoint. The per-call off switch is the one
// configured case that differs, and only by its disclosed status.
func TestSemanticChannelUnconfiguredIsByteIdentical(t *testing.T) {
	t.Parallel()
	repo := semanticFixtureRepo(t)
	fake := &fakeEmbedder{}
	server := httptest.NewServer(fake)
	defer server.Close()
	indexed := t.TempDir()
	buildSemanticFixtureIndex(t, repo, indexed, server.URL)
	built := fake.requests.Load()
	baseline := normalisedSearchJSON(t, semanticSearch(t, repo, t.TempDir(), nil, nil))
	for name, config := range map[string]*SemanticConfig{
		"nil":           nil,
		"endpoint-only": {Endpoint: server.URL},
		"model-only":    {Model: semanticFixtureModel},
	} {
		if got := normalisedSearchJSON(t, semanticSearch(t, repo, indexed, config, nil)); got != baseline {
			t.Fatalf("%s: payload differs from baseline:\n got %s\nwant %s", name, got, baseline)
		}
	}
	if strings.Contains(baseline, "semantic") {
		t.Fatalf("an unconfigured payload mentions the channel: %s", baseline)
	}
	if got := fake.requests.Load(); got != built {
		t.Fatalf("an unconfigured search made %d embedding requests", got-built)
	}
	off := semanticSearch(t, repo, indexed, &SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel, Disabled: true}, nil)
	if off.Stats.SemanticStatus != SemanticStatusOffFlag || fake.requests.Load() != built {
		t.Fatalf("--no-semantic: status %q, requests %d", off.Stats.SemanticStatus, fake.requests.Load()-built)
	}
}

// normalisedSearchJSON is the response's JSON with every wall-clock field zeroed, and the index
// cache-hit bit cleared: those are the only fields two identical searches may legitimately differ
// in (the baseline runs on a cold cache directory).
func normalisedSearchJSON(t *testing.T, response SearchResponse) string {
	t.Helper()
	response.Stats.IndexLatencyMS, response.Stats.QueryLatencyMS = 0, 0
	response.Stats.TotalLatencyMS, response.Stats.SearchLatencyMS, response.Stats.PreselectLatencyMS = 0, 0, 0
	response.Stats.IndexCacheHit = false
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// TestSemanticIndexBuildIsAtomic: a build leaves exactly one artifact and no temporary residue, and
// a build that fails partway leaves nothing at all.
func TestSemanticIndexBuildIsAtomic(t *testing.T) {
	t.Parallel()
	repo := semanticFixtureRepo(t)
	server := httptest.NewServer(&fakeEmbedder{})
	defer server.Close()
	cacheDir := t.TempDir()
	buildSemanticFixtureIndex(t, repo, cacheDir, server.URL)
	entries := semanticCacheFiles(t, cacheDir)
	if len(entries) != 1 || strings.HasPrefix(filepath.Base(entries[0]), ".") || !strings.HasSuffix(entries[0], ".json.gz") {
		t.Fatalf("want exactly one index artifact and no temp files, got %v", entries)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "down", http.StatusServiceUnavailable)
	}))
	defer failing.Close()
	failedCache := t.TempDir()
	snapshot, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{NoNetwork: true}, failedCache)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildSemanticIndex(t.Context(), repo, snapshot, failedCache,
		SemanticConfig{Endpoint: failing.URL, Model: semanticFixtureModel}, false); err == nil {
		t.Fatal("a build against a failing endpoint succeeded")
	}
	if residue := semanticCacheFiles(t, failedCache); len(residue) != 0 {
		t.Fatalf("a failed build left %v", residue)
	}
}

func semanticCacheFiles(t *testing.T, cacheDir string) []string {
	t.Helper()
	var files []string
	root := filepath.Join(cacheDir, semanticCacheFamily)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return err
		}
		if !entry.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// TestFuseSemanticCandidatesInterleavesEmbeddingFirst pins the frozen rule on its own: e1, g1, e2,
// g2, ...; a row already seated is skipped (canonical dedupe); the list is cut to top-k; scores never
// increase down the list.
func TestFuseSemanticCandidatesInterleavesEmbeddingFirst(t *testing.T) {
	t.Parallel()
	row := func(file string, line int, score float64, semanticOnly bool) searchCandidate {
		return searchCandidate{
			result:       SearchResult{FilePath: file, StartLine: line, FocusLine: line, SymbolStartLine: line},
			score:        score,
			semanticOnly: semanticOnly,
		}
	}
	lexical := []searchCandidate{row("a.go", 1, 9, false), row("b.go", 1, 7, false), row("c.go", 1, 5, false)}
	semantic := []searchCandidate{row("x.go", 1, 0.9, true), row("b.go", 1, 7, false), row("y.go", 1, 0.8, true)}
	keyOf := func(candidate searchCandidate) semanticKey {
		return semanticKey{file: candidate.result.FilePath, line: candidate.result.SymbolStartLine}
	}
	fused, seated := fuseSemanticCandidates(lexical, semantic, 4, keyOf)
	var order []string
	for _, candidate := range fused {
		order = append(order, candidate.result.FilePath)
	}
	if strings.Join(order, ",") != "x.go,a.go,b.go,y.go" {
		t.Fatalf("fused order = %v, want x.go,a.go,b.go,y.go", order)
	}
	if seated != 3 {
		t.Fatalf("seated = %d, want 3", seated)
	}
	for index := 1; index < len(fused); index++ {
		if fused[index].score > fused[index-1].score {
			t.Fatalf("scores increase at %d: %v", index, fused)
		}
	}
	if fused[0].score != 9 {
		t.Fatalf("a semantic-only rank 1 should take the score of the lexical row it displaced, got %v", fused[0].score)
	}
}

// TestSplitSemanticNameWords pins recipe 1's identifier split, which is part of the index key's
// meaning: changing it without bumping semanticRecipeVersion would silently mis-embed.
func TestSplitSemanticNameWords(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"parseHTTPResponse_v2":  "parse http response v2",
		"AbandonStalledBackend": "abandon stalled backend",
		"snake_case_name":       "snake case name",
		"x":                     "x",
	} {
		if got := strings.Join(splitSemanticNameWords(name), " "); got != want {
			t.Errorf("%s -> %q, want %q", name, got, want)
		}
	}
	lines := strings.Split("package p\n\n// Frob twiddles.\n// Second line.\n@Deco\nfunc Frob() {}\n", "\n")
	got := semanticDocumentText(SymbolRecord{Name: "Frob", StartLine: 6}, lines)
	if got != "search_document: frob. Frob twiddles. Second line. func Frob() {}" {
		t.Fatalf("recipe text = %q", got)
	}
	long := semanticDocumentText(SymbolRecord{Name: "Frob", StartLine: 1, Signature: strings.Repeat("é", 2000)}, []string{"x"})
	if len(long) > semanticMaxDocumentBytes || !strings.HasPrefix(long, semanticDocumentPrefix) {
		t.Fatalf("truncation broken: %d bytes", len(long))
	}
}
