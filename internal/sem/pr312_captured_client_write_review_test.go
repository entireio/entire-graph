package sem

import "testing"

// The no-reassignment controls for both invocation forms live in
// pr312_client_mask_review_test.go. Reassigning the captured name to another
// HTTP client must not make its one-argument Get call look like a route.
func TestReview312ReassignedCapturedHTTPClientStaysOutOfRouteFallback(t *testing.T) {
	cases := []struct {
		name       string
		invocation string
	}{
		{
			name: "immediate-closure",
			invocation: `func() { client.Get("/up") }()
	client = &http.Client{}`,
		},
		{
			name: "go-closure",
			invocation: `go func() { client.Get("/up") }()
	client = &http.Client{}`,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			review312AssertServedRouterAndNoFallback(t, review312ClosureClientSource(test.invocation))
		})
	}
}
