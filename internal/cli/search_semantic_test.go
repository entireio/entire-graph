package cli

import (
	"bytes"
	"encoding/json"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"unicode"
)

// cliFakeEmbedder is a deterministic Ollama-shaped /api/embed on loopback: a bag of hashed words.
// These tests are about PLUMBING (env, flags, the index verb), so the vectors only need to be
// valid; the ranking itself is pinned in internal/sem.
type cliFakeEmbedder struct{ requests atomic.Int32 }

func (fake *cliFakeEmbedder) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	fake.requests.Add(1)
	var body struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil || request.URL.Path != "/api/embed" {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	vectors := make([][]float64, 0, len(body.Input))
	for _, input := range body.Input {
		vector := make([]float64, 8)
		for _, word := range strings.FieldsFunc(strings.ToLower(input), func(r rune) bool { return !unicode.IsLetter(r) }) {
			hash := fnv.New32a()
			_, _ = hash.Write([]byte(word))
			vector[hash.Sum32()%8]++
		}
		vectors = append(vectors, vector)
	}
	_ = json.NewEncoder(writer).Encode(map[string]any{"model": body.Model, "embeddings": vectors})
}

var volatileSearchStats = regexp.MustCompile(`"(index_latency_ms|query_latency_ms|total_latency_ms|search_latency_ms|preselect_latency_ms|index_cache_hit)":(\d+|true|false)`)

func normalisedSearchOutput(output string) string {
	return volatileSearchStats.ReplaceAllString(output, `"$1":_`)
}

func semanticCLIRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.py", "# Validate a bearer token.\ndef validate_token(token):\n    return bool(token)\n")
	write(t, repo, "pool.py", "# Drop a stalled backend.\ndef abandon_backend(conn):\n    conn.close()\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

func runGraph(t *testing.T, env EntireEnv, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := Run(t.Context(), Options{Version: "test-version", Env: env, Stdout: &out, Stderr: &errOut}, args)
	return out.String(), err
}

// TestSemanticChannelIsOffUnlessBothEnvVarsAreSet is the default-off contract at the command
// surface: with a VALID semantic index in the cache and a LIVE endpoint, a search whose environment
// names neither or only one of the two settings produces the baseline JSON byte for byte (wall-clock
// and cache-hit fields normalised) and sends the endpoint nothing.
func TestSemanticChannelIsOffUnlessBothEnvVarsAreSet(t *testing.T) {
	repo := semanticCLIRepo(t)
	fake := &cliFakeEmbedder{}
	server := httptest.NewServer(fake)
	defer server.Close()
	cacheDir := t.TempDir()
	configured := EntireEnv{RepoRoot: repo, SemanticEndpoint: server.URL, SemanticModel: "fake-embed"}

	// index --semantic without the configuration is a usage error, raised before any build.
	if _, err := runGraph(t, EntireEnv{RepoRoot: repo}, "index", "--repo", repo, "--cache-dir", cacheDir, "--semantic", "--format", "json"); err == nil ||
		!strings.Contains(err.Error(), envSemanticEndpoint) {
		t.Fatalf("index --semantic without env: err = %v", err)
	}
	indexOut, err := runGraph(t, configured, "index", "--repo", repo, "--cache-dir", cacheDir, "--semantic", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var indexed indexResponse
	if err := json.Unmarshal([]byte(indexOut), &indexed); err != nil {
		t.Fatal(err)
	}
	if indexed.Semantic == nil || indexed.Semantic.Status != "built" || indexed.Semantic.Symbols != 2 || indexed.Semantic.Dimension != 8 {
		t.Fatalf("index --semantic report = %+v", indexed.Semantic)
	}
	// A plain index run carries no semantic field at all.
	plainIndex, err := runGraph(t, configured, "index", "--repo", repo, "--cache-dir", cacheDir, "--format", "json")
	if err != nil || strings.Contains(plainIndex, `"semantic"`) {
		t.Fatalf("plain index mentions semantic (err %v): %s", err, plainIndex)
	}
	built := fake.requests.Load()

	search := []string{"query", "--repo", repo, "--head", "--cache-dir", cacheDir, "--format", "json", "--query", "stalled backend"}
	// The first --head search on a cache writes the selective entry the later ones hit, which
	// legitimately changes preselection stats; warm it so every compared run sees one cache state.
	if _, err := runGraph(t, EntireEnv{RepoRoot: repo}, search...); err != nil {
		t.Fatal(err)
	}
	baseline, err := runGraph(t, EntireEnv{RepoRoot: repo}, search...)
	if err != nil {
		t.Fatal(err)
	}
	baseline = normalisedSearchOutput(baseline)
	if strings.Contains(baseline, "semantic") {
		t.Fatalf("an unconfigured payload mentions the channel: %s", baseline)
	}
	for name, env := range map[string]EntireEnv{
		"endpoint-only": {RepoRoot: repo, SemanticEndpoint: server.URL},
		"model-only":    {RepoRoot: repo, SemanticModel: "fake-embed"},
	} {
		got, err := runGraph(t, env, search...)
		if err != nil {
			t.Fatal(err)
		}
		if normalisedSearchOutput(got) != baseline {
			t.Fatalf("%s: payload differs from baseline:\n got %s\nwant %s", name, got, baseline)
		}
	}
	if got := fake.requests.Load(); got != built {
		t.Fatalf("an unconfigured search made %d embedding requests", got-built)
	}

	// NON-VACUITY: the same cache and endpoint DO drive the channel once both settings are present,
	// so the equalities above are not explained by a broken index.
	used, err := runGraph(t, configured, search...)
	if err != nil || !strings.Contains(used, `"semantic_status":"used"`) {
		t.Fatalf("configured --head search did not use the channel (err %v): %s", err, used)
	}
	off, err := runGraph(t, configured, append(search, "--no-semantic")...)
	if err != nil || !strings.Contains(off, `"semantic_status":"off:flag"`) {
		t.Fatalf("--no-semantic did not turn the channel off (err %v): %s", err, off)
	}
	worktree, err := runGraph(t, configured, "query", "--repo", repo, "--cache-dir", cacheDir, "--format", "json", "--query", "stalled backend")
	if err != nil || !strings.Contains(worktree, `"semantic_status":"off:worktree"`) {
		t.Fatalf("a default (worktree) search consulted the channel (err %v): %s", err, worktree)
	}
}

// TestEntireEnvSemanticConfigNeedsBothSettings pins the environment half of the default-off rule on
// its own: the sem layer enforces it too, so the end-to-end test above cannot tell which layer held.
func TestEntireEnvSemanticConfigNeedsBothSettings(t *testing.T) {
	for _, env := range []EntireEnv{
		{},
		{SemanticEndpoint: "http://localhost:11434"},
		{SemanticModel: "nomic-embed-text"},
		{SemanticEndpoint: "  ", SemanticModel: "nomic-embed-text"},
	} {
		if config := env.semanticConfig(); config != nil {
			t.Fatalf("%+v configured the channel: %+v", env, config)
		}
	}
	config := searchSemanticConfig(EntireEnv{SemanticEndpoint: "http://localhost:11434", SemanticModel: "m"}, true)
	if config == nil || !config.Disabled || config.Model != "m" {
		t.Fatalf("both settings + --no-semantic = %+v", config)
	}
}
