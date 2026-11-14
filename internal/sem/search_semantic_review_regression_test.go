package sem

import (
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
)

// These review regressions deliberately remain RED against 3c7d9177. They use
// production fusion, identity lookup, confidence, and normalization functions;
// only the external HTTP transport is replaced. No test binds a socket.

func semanticReviewRow(file, name string, score float64, semanticOnly bool) searchCandidate {
	return searchCandidate{
		result: SearchResult{
			FilePath: file, SymbolID: name, SymbolName: name, Kind: "function",
			StartLine: 1, EndLine: 1, FocusLine: 1,
			SymbolStartLine: 1, SymbolEndLine: 1,
			SnippetStartLine: 1, SnippetEndLine: 1,
			Snippet: "func " + name + "() { return }",
		},
		score: score, semanticOnly: semanticOnly,
	}
}

// This is only the representation boundary to the real confidence consumer:
// expose fusion's actual scores without recalculating any rank or score.
func semanticReviewResponse(candidates []searchCandidate) SearchResponse {
	response := SearchResponse{Results: make([]SearchResult, len(candidates))}
	for i, candidate := range candidates {
		response.Results[i] = candidate.result
		response.Results[i].Score = candidate.score
	}
	return response
}

// Break caught: score copying manufactures a tie that the public confidence
// consumer mistakes for measured ambiguity. This does not prescribe a new
// semantic calibration: it forbids presenting a positional tie as evidence.
func TestSemanticReviewQ1ConfidenceDoesNotInventRelevanceTie(t *testing.T) {
	lexical := []searchCandidate{
		semanticReviewRow("strong.go", "Strong", 40, false),
		semanticReviewRow("next.go", "Next", 20, false),
	}
	keyOf := func(candidate searchCandidate) semanticKey {
		return semanticCandidateKey(candidate, nil)
	}
	t.Run("lexical_control", func(t *testing.T) {
		fused, _ := fuseSemanticCandidates(lexical, nil, 2, keyOf)
		assessment := AssessSearchConfidence(semanticReviewResponse(fused))
		if assessment.Low {
			t.Fatalf("strong separated lexical control is low confidence: %+v", assessment)
		}
	})
	t.Run("synthetic_semantic_head", func(t *testing.T) {
		dense := []searchCandidate{semanticReviewRow("dense.go", "Dense", 0.9, true)}
		fused, _ := fuseSemanticCandidates(lexical, dense, 2, keyOf)
		if len(fused) != 2 || fused[0].result.SymbolName != "Dense" || fused[1].result.SymbolName != "Strong" {
			t.Fatalf("fixture did not seat the distinct semantic and lexical heads: %+v", fused)
		}
		assessment := AssessSearchConfidence(semanticReviewResponse(fused))
		if assessment.Low && strings.Contains(assessment.Reason, "tied") {
			t.Fatalf("fusion invented a relevance tie: scores=[%g %g], confidence=%+v",
				fused[0].score, fused[1].score, assessment)
		}
	})
}

// Break caught: a weak lexical candidate promoted by semantic order clamps the
// independently measured relevance of a strong lexical result from 40 to 2.
func TestSemanticReviewQ1FusionPreservesLexicalRelevance(t *testing.T) {
	strong := semanticReviewRow("strong.go", "Strong", 40, false)
	weak := semanticReviewRow("weak.go", "Weak", 2, false)
	for _, tc := range []struct {
		name     string
		semantic []searchCandidate
	}{
		{name: "semantic_off_control"},
		{name: "weak_semantic_promoted_head", semantic: []searchCandidate{weak}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fused, _ := fuseSemanticCandidates([]searchCandidate{strong, weak}, tc.semantic, 2,
				func(candidate searchCandidate) semanticKey { return semanticCandidateKey(candidate, nil) })
			for _, candidate := range fused {
				if candidate.result.SymbolID == "Strong" {
					if candidate.score != 40 {
						t.Fatalf("Strong lexical relevance = %g, want original measured 40", candidate.score)
					}
					return
				}
			}
			t.Fatal("fusion lost the strong lexical result before its score could be checked")
		})
	}
}

// Break caught: file+line canonicalization gives B's semantic vote to lexical A
// when two distinct, named function declarations occupy the same source line.
func TestSemanticReviewQ3SameLineHitKeepsSymbolIdentity(t *testing.T) {
	symbols := map[string][]SymbolRecord{
		"same.js": {
			{ID: "symbol-a", FilePath: "same.js", StartLine: 1, EndLine: 1,
				Name: "a", QualifiedName: "a", Kind: "function", Language: "javascript"},
			{ID: "symbol-b", FilePath: "same.js", StartLine: 1, EndLine: 1,
				Name: "b", QualifiedName: "b", Kind: "function", Language: "javascript"},
		},
	}
	read := func(path string) (string, bool) {
		if path != "same.js" {
			return "", false
		}
		return "function a() {} function b() {}", true
	}
	lexicalA := semanticReviewRow("same.js", "a", 40, false)
	lexicalA.result.SymbolID = "symbol-a"
	for _, tc := range []struct {
		name string
		pool []searchCandidate
	}{
		{name: "no_lexical_collision_control"},
		{name: "lexical_a_shares_b_line", pool: []searchCandidate{lexicalA}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := semanticCandidates(
				[]semanticHit{{FilePath: "same.js", StartLine: 1, Name: "b", Score: 0.9}},
				tc.pool, symbols, read, map[string]string{"same.js": "javascript"}, SearchOptions{},
			)
			if len(got) != 1 {
				t.Fatalf("renderable hit for b yielded %d candidates, want 1", len(got))
			}
			if got[0].result.SymbolID != "symbol-b" || got[0].result.SymbolName != "b" {
				t.Fatalf("semantic hit b bound to id=%q name=%q, want id=\"symbol-b\" name=\"b\"",
					got[0].result.SymbolID, got[0].result.SymbolName)
			}
		})
	}
}

type semanticReviewRoundTripper func(*http.Request) (*http.Response, error)

func (transport semanticReviewRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// Break caught: finite components overflow the norm accumulator, are accepted,
// and silently become [0,0]. Either safe rejection or [1,0] is acceptable for
// the extreme fixture; this test does not dictate the normalization algorithm.
// Do not parallelize: the existing production seam is a package-level client.
func TestSemanticReviewF4FiniteVectorNormalization(t *testing.T) {
	for _, tc := range []struct {
		name           string
		body           string
		want           [2]float32
		allowRejection bool
	}{
		{
			name: "normal_3_4_control",
			body: `{"model":"review-offline","embeddings":[[3,4]]}`,
			want: [2]float32{0.6, 0.8},
		},
		{
			name: "extreme_finite_magnitude",
			body: `{"model":"review-offline","embeddings":[[1e308,0]]}`,
			want: [2]float32{1, 0}, allowRejection: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := semanticHTTPClient
			semanticHTTPClient = &http.Client{Transport: semanticReviewRoundTripper(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(tc.body)), Request: request,
				}, nil
			})}
			t.Cleanup(func() { semanticHTTPClient = previous })
			vectors, err := semanticEmbed(t.Context(),
				&SemanticConfig{Endpoint: "http://127.0.0.1", Model: "review-offline"},
				[]string{"search_query: offline fixture"}, 1024,
			)
			if err != nil {
				var unavailable *semanticUnavailable
				if tc.allowRejection && errors.As(err, &unavailable) && unavailable.reason == "bad-response" {
					return
				}
				t.Fatalf("unexpected normalization error: %v", err)
			}
			if len(vectors) != 1 || len(vectors[0]) != 2 {
				t.Fatalf("normalization returned shape %v, want one 2-component vector", vectors)
			}
			for i, want := range tc.want {
				got := float64(vectors[0][i])
				if math.IsNaN(got) || math.IsInf(got, 0) || math.Abs(got-float64(want)) > 1e-6 {
					t.Fatalf("accepted normalized vector %v, want %v or a safe bad-response rejection", vectors[0], tc.want)
				}
			}
		})
	}
}
