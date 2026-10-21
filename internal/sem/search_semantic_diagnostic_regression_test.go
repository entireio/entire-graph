package sem

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type semanticDiagnosticTransport func(*http.Request) (*http.Response, error)

func (transport semanticDiagnosticTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// Break caught: raw transport URLs and service-controlled model names become
// public errors/warnings. All sentinels are invented; the client never dials.
func TestSemanticReviewF5DiagnosticsDoNotEchoSensitiveValues(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, response, sentinel, reason string
	}{
		{
			name: "ordinary_transport_control", endpoint: "http://127.0.0.1",
			sentinel: "SYNTHETIC_QUERY_CREDENTIAL", reason: "endpoint",
		},
		{
			name:     "credential_in_endpoint_query",
			endpoint: "http://127.0.0.1/?token=SYNTHETIC_QUERY_CREDENTIAL",
			sentinel: "SYNTHETIC_QUERY_CREDENTIAL", reason: "endpoint",
		},
		{
			name: "service_controlled_model", endpoint: "http://127.0.0.1",
			response: `{"model":"SYNTHETIC_MODEL_SECRET","embeddings":[[1,0]]}`,
			sentinel: "SYNTHETIC_MODEL_SECRET", reason: "model-mismatch",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := semanticHTTPClient
			calls := 0
			semanticHTTPClient = &http.Client{Transport: semanticDiagnosticTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				if tc.response == "" {
					return nil, errors.New("synthetic connection unavailable")
				}
				return &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(tc.response)), Request: request,
				}, nil
			})}
			t.Cleanup(func() { semanticHTTPClient = previous })
			_, err := semanticEmbed(t.Context(),
				&SemanticConfig{Endpoint: tc.endpoint, Model: "offline-wanted-model"},
				[]string{"search_query: synthetic fixture"}, 1024,
			)
			var failure *semanticUnavailable
			if !errors.As(err, &failure) {
				t.Fatalf("want a classified unavailable error, got %v", err)
			}
			// Rejecting a credential-bearing URL before any request is a valid fix.
			preflightRejection := tc.name == "credential_in_endpoint_query" &&
				failure.reason == "endpoint-invalid" && calls == 0
			if !preflightRejection && (calls != 1 || failure.reason != tc.reason) {
				t.Fatalf("wrong failure path: calls=%d reason=%q, want one injected call and %q", calls, failure.reason, tc.reason)
			}
			var response SearchResponse
			applySemanticOutcome(&response, &semanticOutcome{
				status: semanticUnavailablePrefix + failure.reason, detail: failure.detail,
			})
			if len(response.Warnings) != 1 || response.Warnings[0].Code != "W_SEMANTIC_UNAVAILABLE" {
				t.Fatalf("missing public unavailable warning: %+v", response.Warnings)
			}
			encoded, marshalErr := json.Marshal(response)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(err.Error(), tc.sentinel) || strings.Contains(string(encoded), tc.sentinel) {
				t.Fatalf("synthetic sensitive value escaped into diagnostics: error=%q warning=%q",
					err.Error(), response.Warnings[0].Detail)
			}
		})
	}
}
