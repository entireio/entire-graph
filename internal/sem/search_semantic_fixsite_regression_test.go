package sem

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type semanticFixsiteRoundTripper func(*http.Request) (*http.Response, error)

func (transport semanticFixsiteRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// Break caught: semantic fusion must not undo the existing production-fix-site
// protection for a query that did not ask for tests. Observe the final delivered
// SearchResponse after all ranking, sectioning, and byte-fitting passes.
// Nonparallel because semanticHTTPClient is the existing package-global seam.
func TestSemanticReviewQ2FinalFixSiteOrder(t *testing.T) {
	repo, cacheDir := t.TempDir(), t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "src/connection.py", "def repair_connection():\n    return \"production repair\"\n")
	writeFile(t, repo, "tests/test_connection.py", "def test_repair_connection():\n    return \"test expectation\"\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "isolated semantic fix-site fixture")

	previous := semanticHTTPClient
	semanticHTTPClient = &http.Client{Transport: semanticFixsiteRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/embed" {
			return nil, fmt.Errorf("unexpected fixture request: %s %s", request.Method, request.URL.Path)
		}
		var body semanticEmbedRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		if body.Model != "q2-offline" {
			return nil, fmt.Errorf("unexpected fixture model %q", body.Model)
		}
		vectors := make([][]float64, len(body.Input))
		for i, input := range body.Input {
			switch {
			case strings.HasPrefix(input, "search_query: "):
				vectors[i] = []float64{1, 0}
			case strings.HasPrefix(input, "search_document: test repair connection."):
				vectors[i] = []float64{1, 0}
			case strings.HasPrefix(input, "search_document: repair connection."):
				vectors[i] = []float64{0, 1}
			default:
				return nil, fmt.Errorf("unexpected fixture embedding input %q", input)
			}
		}
		encoded, err := json.Marshal(semanticEmbedResponse{Model: "q2-offline", Embeddings: vectors})
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(bytes.NewReader(encoded)), Request: request,
		}, nil
	})}
	t.Cleanup(func() { semanticHTTPClient = previous })
	config := &SemanticConfig{Endpoint: "http://127.0.0.1", Model: "q2-offline"}

	snapshot, _, err := PreindexProviderSnapshot(t.Context(), repo, "review-q2",
		ProviderSnapshotOptions{NoNetwork: true, Profile: ProfileFast}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	indexed, err := BuildSemanticIndex(t.Context(), repo, snapshot, cacheDir, *config, false)
	if err != nil {
		t.Fatal(err)
	}
	if indexed.Status != "built" || indexed.Symbols != 2 || indexed.Dimension != 2 {
		t.Fatalf("fixture did not build both callable vectors: %+v", indexed)
	}

	for _, topK := range []int{1, 2} {
		t.Run(fmt.Sprintf("k%d", topK), func(t *testing.T) {
			for _, tc := range []struct {
				name, query, wantPath, wantSymbol string
				semantic                          *SemanticConfig
			}{
				{
					name: "lexical_off_control", query: "repair_connection",
					wantPath: "src/connection.py", wantSymbol: "repair_connection",
				},
				{
					name: "semantic_no_test_intent", query: "repair_connection", semantic: config,
					wantPath: "src/connection.py", wantSymbol: "repair_connection",
				},
				{
					name: "explicit_test_intent_control", query: "tests for repair_connection", semantic: config,
					wantPath: "tests/test_connection.py", wantSymbol: "test_repair_connection",
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					response, err := SearchRepository(t.Context(), repo, "review-q2", tc.query, SearchOptions{
						CacheDir: cacheDir, Profile: ProfileFast, TopK: topK,
						IndexAllFiles: true, MaxIndexedFiles: 2, MaxContextBytes: 8192,
						SingleResolution: true, DocumentResolution: true, Semantic: tc.semantic,
					})
					if err != nil {
						t.Fatal(err)
					}
					if err := response.Validate(); err != nil {
						t.Fatal(err)
					}
					if response.Commit == "" || response.Tree == "" {
						t.Fatal("fixture search did not read a committed snapshot")
					}
					if tc.semantic != nil && (response.Stats.SemanticStatus != "used" || response.Stats.SemanticResults == 0) {
						t.Fatalf("fixture did not exercise semantic fusion: %+v", response.Stats)
					}
					var primary []SearchResult
					for _, result := range response.Results {
						if result.Section == "" {
							primary = append(primary, result)
						}
					}
					if len(primary) == 0 || strings.TrimSpace(primary[0].Snippet) == "" {
						t.Fatalf("fixture delivered no primary source: %+v", response.Results)
					}
					if topK == 2 && tc.name == "semantic_no_test_intent" {
						productionPresent := false
						for _, result := range primary {
							productionPresent = productionPresent || result.FilePath == "src/connection.py"
						}
						if !productionPresent {
							t.Fatalf("fixture lost the available production primary: %+v", primary)
						}
					}
					if primary[0].FilePath != tc.wantPath || primary[0].SymbolName != tc.wantSymbol {
						t.Fatalf("final primary rank 1 = %s:%s, want %s:%s (topK=%d, query=%q)",
							primary[0].FilePath, primary[0].SymbolName, tc.wantPath, tc.wantSymbol, topK, tc.query)
					}
				})
			}
		})
	}
}
