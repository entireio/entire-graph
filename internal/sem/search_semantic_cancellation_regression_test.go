package sem

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

type semanticCancellationRegressionTransport func(*http.Request) (*http.Response, error)

func (transport semanticCancellationRegressionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// TestSemanticIndexCancellationRegression catches publication after parent cancellation at the
// successful final-response boundary. The control proves that the replacement vector really is
// different and that an uncanceled forced rebuild actually publishes it.
//
// No parallel subtests: each temporarily replaces the package-global HTTP client. The transport
// is wholly in-process and deliberately returns its successful response even after cancellation;
// this tests the builder's publication boundary, not the ordinary HTTP transport's cancellation.
func TestSemanticIndexCancellationRegression(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "alpha.go", "package fixture\n\n// Alpha is a cancellation fixture.\nfunc Alpha() {}\n")
	git(t, repo, "add", "alpha.go")
	git(t, repo, "-c", "commit.gpgsign=false", "commit", "-m", "semantic cancellation fixture")
	commit := gitInput(t, repo, "", "rev-parse", "HEAD")
	tree := gitInput(t, repo, "", "rev-parse", "HEAD^{tree}")
	snapshot := ProviderSnapshot{
		Header: SnapshotHeader{RepoRoot: repo, Commit: commit, Tree: tree, Profile: "full"},
		Symbols: []SymbolRecord{
			{Kind: "function", Name: "Alpha", FilePath: "alpha.go", StartLine: 4, EndLine: 4, Signature: "func Alpha() {}", Language: "go"},
		},
	}
	const model = "semantic-cancellation-regression"
	config := SemanticConfig{Endpoint: "http://127.0.0.1:1", Model: model}

	for _, tc := range []struct {
		name           string
		cancelResponse bool
		wantVector     []float32
	}{
		{name: "uncanceled_forced_rebuild_publishes", wantVector: []float32{0, 1}},
		{name: "canceled_before_final_response_preserves_generation", cancelResponse: true, wantVector: []float32{1, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cacheDir := t.TempDir()
			rebuildCtx, cancelRebuild := context.WithCancel(t.Context())
			t.Cleanup(cancelRebuild)
			requests := 0
			canceledBeforeResponse := false
			originalClient := semanticHTTPClient
			client := *originalClient
			client.Transport = semanticCancellationRegressionTransport(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodPost || request.URL.Host != "127.0.0.1:1" || request.URL.Path != "/api/embed" {
					return nil, fmt.Errorf("unexpected semantic request: %s %s", request.Method, request.URL)
				}
				defer request.Body.Close()
				var body struct {
					Model string   `json:"model"`
					Input []string `json:"input"`
				}
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					return nil, err
				}
				if body.Model != model || len(body.Input) != 1 || !strings.Contains(body.Input[0], "func Alpha() {}") {
					return nil, fmt.Errorf("unexpected semantic document request: %+v", body)
				}
				requests++
				if requests > 2 {
					return nil, fmt.Errorf("unexpected extra embedding request %d", requests)
				}
				vector := []float64{1, 0}
				if requests == 2 {
					vector = []float64{0, 1}
				}
				payload, err := json.Marshal(struct {
					Model      string      `json:"model"`
					Embeddings [][]float64 `json:"embeddings"`
				}{Model: model, Embeddings: [][]float64{vector}})
				if err != nil {
					return nil, err
				}
				response := &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(bytes.NewReader(payload)),
					Request:    request,
				}
				if requests == 2 && tc.cancelResponse {
					cancelRebuild()
					canceledBeforeResponse = true
					if !errors.Is(request.Context().Err(), context.Canceled) {
						return nil, errors.New("parent cancellation did not reach the embedding request")
					}
				}
				return response, nil
			})
			semanticHTTPClient = &client
			t.Cleanup(func() { semanticHTTPClient = originalClient })

			assertStoredVector := func(want []float32) {
				t.Helper()
				index, err := loadSemanticIndex(cacheDir, tree, model)
				if err != nil {
					t.Fatalf("load stored generation: %v", err)
				}
				if index.Count != 1 || index.Dimension != 2 || len(index.Symbols) != 1 ||
					index.Symbols[0] != (semanticIndexSymbol{FilePath: "alpha.go", StartLine: 4, Name: "Alpha"}) {
					t.Fatalf("stored generation has unexpected shape/symbols: count=%d dimension=%d symbols=%v",
						index.Count, index.Dimension, index.Symbols)
				}
				if !slices.Equal(index.decoded, want) {
					t.Errorf("stored vector = %v, want %v", index.decoded, want)
				}
			}

			first, err := BuildSemanticIndex(t.Context(), repo, snapshot, cacheDir, config, false)
			if err != nil {
				t.Fatalf("initial build: %v", err)
			}
			if first.Status != "built" || first.Symbols != 1 || first.Dimension != 2 || requests != 1 {
				t.Fatalf("initial build = %+v with %d requests; want built, 1 symbol, 2 dimensions, 1 request", first, requests)
			}
			assertStoredVector([]float32{1, 0})
			if t.Failed() {
				t.Fatal("initial artifact is not the required valid generation")
			}

			second, err := BuildSemanticIndex(rebuildCtx, repo, snapshot, cacheDir, config, true)
			if requests != 2 {
				t.Fatalf("forced rebuild made %d total requests, want 2", requests)
			}
			if tc.cancelResponse {
				if !canceledBeforeResponse || !errors.Is(rebuildCtx.Err(), context.Canceled) {
					t.Fatal("fixture did not cancel the parent before returning the final successful response")
				}
				if !errors.Is(err, context.Canceled) {
					t.Errorf("canceled forced rebuild error = %v, want context.Canceled", err)
				}
				if second.Status == "built" {
					t.Errorf("canceled forced rebuild reported built: %+v", second)
				}
			} else if err != nil || second.Status != "built" || second.Symbols != 1 || second.Dimension != 2 {
				t.Errorf("uncanceled forced rebuild = %+v, error = %v; want built, 1 symbol, 2 dimensions", second, err)
			}
			assertStoredVector(tc.wantVector)
		})
	}
}
