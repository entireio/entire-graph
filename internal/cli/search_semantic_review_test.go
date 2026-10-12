package cli

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSemanticNoNetworkMakesNoRequests(t *testing.T) {
	repo := semanticCLIRepo(t)
	fake := &cliFakeEmbedder{}
	server := httptest.NewServer(fake)
	defer server.Close()
	env := EntireEnv{RepoRoot: repo, SemanticEndpoint: server.URL, SemanticModel: "fake-embed"}
	cache := t.TempDir()
	if _, err := runGraph(t, env, "index", "--repo", repo, "--cache-dir", cache, "--semantic", "--no-network"); err == nil || !strings.Contains(err.Error(), "--no-network") {
		t.Fatalf("index: %v", err)
	}
	for _, format := range []string{"json", "ndjson"} {
		out, err := runGraph(t, env, "query", "--repo", repo, "--head", "--cache-dir", cache, "--no-network", "--query", "validate token", "--format", format)
		if err != nil {
			t.Fatal(err)
		}
		if format == "ndjson" && !strings.Contains(out, `"record_type":"search_result"`) {
			t.Fatalf("missing result type: %s", out)
		}
		if !strings.Contains(out, `"semantic_status":"off:flag"`) {
			t.Fatalf("channel enabled: %s", out)
		}
	}
	if fake.requests.Load() != 0 {
		t.Fatalf("sent %d requests", fake.requests.Load())
	}
}
