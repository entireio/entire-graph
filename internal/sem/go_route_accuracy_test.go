package sem

import "testing"

type goRouteAccuracyCase struct {
	name    string
	content string
	want    []string
}

func runGoRouteAccuracyCases(t *testing.T, cases []goRouteAccuracyCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertGoRouteSet(t, goRouteRegistrationsWithin(t, tc.content), tc.want...)
		})
	}
}

// Route-like text that is not a call expression must never become a route,
// in a file that parses and in one that does not.
func TestGoRouteAccuracyOnlyRealCallsRegister(t *testing.T) {
	runGoRouteAccuracyCases(t, []goRouteAccuracyCase{
		{
			name: "line and block comments",
			content: `package p
// Example: e.GET("/doc", docHandler)
/* http.HandleFunc("/block", blockHandler) */
func main() {
	e := echo.New()
	e.GET("/real", realHandler) // e.GET("/trailing", trailingHandler)
}
`,
			want: []string{"/real -> realHandler"},
		},
		{
			name:    "raw and interpreted string literals",
			content: "package p\nvar doc = `e.GET(\"/raw\", rawHandler)`\nvar doc2 = \"http.HandleFunc(\\\"/str\\\", strHandler)\"\nfunc main() { http.HandleFunc(\"/real\", realHandler) }\n",
			want:    []string{"/real -> realHandler"},
		},
		{
			name: "unparseable file: comments and strings still never match",
			content: `package p
// e.GET("/doc", docHandler)
var s = "e.GET(\"/str\", strHandler)"
func register(e *Echo) {
	e.GET("/health", healthHandler)
`,
			want: []string{"/health -> healthHandler"},
		},
		{
			name: "unparseable file: assigned and group-typed receivers are not trusted",
			content: `package p
func register(e *Echo, g *echo.Group, r chi.Router) {
	x := e.Group("/a")
	x.GET("/x", xHandler)
	g.GET("/g", gHandler)
	r.Get("/r", rHandler)
	y := build()
	y.GET("/y", yHandler)
	undeclared.GET("/u", uHandler)
	e.GET("/health", healthHandler)
`,
			want: []string{"/health -> healthHandler"},
		},
		{
			name: "unparseable file with Route, Mount, Subrouter or StripPrefix is left unresolved",
			content: `package p
func register(r Router) {
	r.Route("/a", func(r Router) { r.Get("/x", xHandler) })
	r.Get("/health", healthHandler)
`,
			want: nil,
		},
	})
}
