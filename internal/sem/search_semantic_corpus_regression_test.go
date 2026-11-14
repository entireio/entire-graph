package sem

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

type semanticCorpusRegressionTransport func(*http.Request) (*http.Response, error)

func (transport semanticCorpusRegressionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// TestSemanticIndexCorpusRegression catches reuse of an artifact from a different eligible
// symbol corpus just because the committed tree and model match. Both snapshots point at real
// committed fixture files; only their eligible symbol lists differ. No provider parser is run.
//
// Keep this test and its subtests serial: semanticHTTPClient is temporarily replaced with a
// wholly in-process transport. No server, listener, DNS lookup or network dial is involved.
func TestSemanticIndexCorpusRegression(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "alpha.go", "package fixture\n\n// Alpha is the first fixture function.\nfunc Alpha() {}\n")
	writeFile(t, repo, "beta.go", "package fixture\n\n// Beta is the second fixture function.\nfunc Beta() {}\n")
	git(t, repo, "add", "alpha.go", "beta.go")
	git(t, repo, "-c", "commit.gpgsign=false", "commit", "-m", "semantic corpus fixture")
	commit := gitInput(t, repo, "", "rev-parse", "HEAD")
	tree := gitInput(t, repo, "", "rev-parse", "HEAD^{tree}")
	wide := ProviderSnapshot{
		Header: SnapshotHeader{RepoRoot: repo, Commit: commit, Tree: tree, Profile: "full"},
		Symbols: []SymbolRecord{
			{Kind: "function", Name: "Alpha", FilePath: "alpha.go", StartLine: 4, EndLine: 4, Signature: "func Alpha() {}", Language: "go"},
			{Kind: "function", Name: "Beta", FilePath: "beta.go", StartLine: 4, EndLine: 4, Signature: "func Beta() {}", Language: "go"},
		},
	}
	narrow := wide
	narrow.Symbols = append([]SymbolRecord(nil), wide.Symbols[:1]...)

	const model = "semantic-corpus-regression"
	config := SemanticConfig{Endpoint: "http://127.0.0.1:1", Model: model}
	var embedded []string
	requests := 0
	originalClient := semanticHTTPClient
	client := *originalClient
	client.Transport = semanticCorpusRegressionTransport(func(request *http.Request) (*http.Response, error) {
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
		if body.Model != model || len(body.Input) == 0 {
			return nil, fmt.Errorf("unexpected semantic request model/count: %q/%d", body.Model, len(body.Input))
		}
		vectors := make([][]float64, len(body.Input))
		for i, input := range body.Input {
			switch {
			case strings.Contains(input, "func Alpha() {}"):
				embedded = append(embedded, "alpha.go:Alpha")
				vectors[i] = []float64{1, 0}
			case strings.Contains(input, "func Beta() {}"):
				embedded = append(embedded, "beta.go:Beta")
				vectors[i] = []float64{0, 1}
			default:
				return nil, fmt.Errorf("unexpected semantic document: %q", input)
			}
		}
		requests++
		payload, err := json.Marshal(struct {
			Model      string      `json:"model"`
			Embeddings [][]float64 `json:"embeddings"`
		}{Model: model, Embeddings: vectors})
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(payload)),
			Request:    request,
		}, nil
	})
	semanticHTTPClient = &client
	t.Cleanup(func() { semanticHTTPClient = originalClient })

	assertStoredCorpus := func(t *testing.T, cacheDir string, want []string) {
		t.Helper()
		index, err := loadSemanticIndex(cacheDir, tree, model)
		if err != nil {
			t.Fatalf("load stored corpus: %v", err)
		}
		var got []string
		for _, symbol := range index.Symbols {
			got = append(got, symbol.FilePath+":"+symbol.Name)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("stored corpus = %v, want %v", got, want)
		}
	}

	for _, tc := range []struct {
		name                  string
		first, second         ProviderSnapshot
		firstCount, wantCount int
		firstKeys, wantKeys   []string
		wantStatus            string
	}{
		{
			name: "same_corpus_reuses", first: narrow, second: narrow,
			firstCount: 1, wantCount: 1,
			firstKeys: []string{"alpha.go:Alpha"}, wantKeys: []string{"alpha.go:Alpha"},
			wantStatus: "reused",
		},
		{
			name: "narrow_to_wide", first: narrow, second: wide,
			firstCount: 1, wantCount: 2,
			firstKeys: []string{"alpha.go:Alpha"}, wantKeys: []string{"alpha.go:Alpha", "beta.go:Beta"},
			wantStatus: "built",
		},
		{
			name: "wide_to_narrow", first: wide, second: narrow,
			firstCount: 2, wantCount: 1,
			firstKeys: []string{"alpha.go:Alpha", "beta.go:Beta"}, wantKeys: []string{"alpha.go:Alpha"},
			wantStatus: "built",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cacheDir := t.TempDir()
			beforeInputs, beforeRequests := len(embedded), requests
			first, err := BuildSemanticIndex(t.Context(), repo, tc.first, cacheDir, config, false)
			if err != nil {
				t.Fatalf("initial build: %v", err)
			}
			if first.Status != "built" || first.Symbols != tc.firstCount || first.Dimension != 2 {
				t.Fatalf("initial build = %+v; want built, %d symbols, 2 dimensions", first, tc.firstCount)
			}
			if requests == beforeRequests || !slices.Equal(embedded[beforeInputs:], tc.firstKeys) {
				t.Fatalf("initial build sent %d requests for %v; want embeddings for %v",
					requests-beforeRequests, embedded[beforeInputs:], tc.firstKeys)
			}
			assertStoredCorpus(t, cacheDir, tc.firstKeys)

			beforeRequests = requests
			second, err := BuildSemanticIndex(t.Context(), repo, tc.second, cacheDir, config, false)
			if err != nil {
				t.Fatalf("second build: %v", err)
			}
			if second.Status != tc.wantStatus || second.Symbols != tc.wantCount || second.Dimension != 2 {
				t.Errorf("second build = %+v; want %s, %d symbols, 2 dimensions", second, tc.wantStatus, tc.wantCount)
			}
			if tc.wantStatus == "reused" && requests != beforeRequests {
				t.Errorf("same-corpus reuse sent %d additional requests, want 0", requests-beforeRequests)
			}
			assertStoredCorpus(t, cacheDir, tc.wantKeys)
		})
	}
}
