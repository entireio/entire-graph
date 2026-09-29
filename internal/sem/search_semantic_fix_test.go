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
