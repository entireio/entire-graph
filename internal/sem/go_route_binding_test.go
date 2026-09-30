package sem

import "testing"

// Each case pins the exact route set: a correct route must be present, and a
// route whose prefix is not determined at its registration must be omitted,
// never re-rooted to the bare path or given another binding's prefix.
func TestGoRouteBindingResolvesPrefixAtUse(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name: "route before and after a self-update",
			content: `package routes
func register(root *Router) {
	g := root.Group("/base")
	g.GET("/early", earlyHandler)
	g = g.Group("/self")
	g.GET("/late", lateHandler)
}
`,
			want: []string{"/base/early -> earlyHandler", "/base/self/late -> lateHandler"},
		},
		{
			name: "nested shadow with outer restored",
			content: `package routes
func register(root *Router) {
	g := root.Group("/outer")
	g.GET("/before", outerBeforeHandler)
	{
		g := root.Group("/inner")
		g.GET("/inside", innerHandler)
		g = g.Group("/deeper")
		g.GET("/deep", deepHandler)
	}
	g.GET("/after", outerAfterHandler)
}
`,
			want: []string{
				"/outer/before -> outerBeforeHandler",
				"/inner/inside -> innerHandler",
				"/inner/deeper/deep -> deepHandler",
				"/outer/after -> outerAfterHandler",
			},
		},
		{
			name: "descendant and inline chain of an unknown prefix",
			content: `package routes
func register(root *Router, prefix string) {
	g := root.Group(prefix)
	child := g.Group("/child")
	child.GET("/x", childHandler)
	g.Group("/inline").GET("/y", inlineHandler)
	g.GET("/z", directHandler)
	root.GET("/health", healthHandler)
}
`,
			want: []string{"/health -> healthHandler"},
		},
		{
			name: "non-Group assignment invalidates the group and its descendants",
			content: `package routes
func register(root *Router, other *Group) {
	g := root.Group("/base")
	g.GET("/before", beforeHandler)
	g = other
	g.GET("/after", afterHandler)
	child := g.Group("/child")
	child.GET("/x", childHandler)
	g.Group("/inline").GET("/y", inlineHandler)
}
`,
			want: []string{"/base/before -> beforeHandler"},
		},
		{
			name: "write in one branch is unknown to its sibling and afterwards",
			content: `package routes
func register(root *Router, admin bool) {
	g := root.Group("/public")
	if admin {
		g = root.Group("/admin")
		g.GET("/inside", insideHandler)
	} else {
		g.GET("/other", otherHandler)
	}
	g.GET("/after", afterHandler)
}
`,
			want: []string{"/admin/inside -> insideHandler"},
		},
		{
			name: "write in a loop body is unknown throughout the loop",
			content: `package routes
func register(root *Router, names []string) {
	g := root.Group("/v")
	g.GET("/before", beforeHandler)
	for range names {
		g.GET("/each", eachHandler)
		g = g.Group("/n")
	}
	g.GET("/after", afterHandler)
}
`,
			want: []string{"/v/before -> beforeHandler"},
		},
		{
			name: "write in a three-clause for loop is unknown throughout the loop",
			content: `package routes
func register(root *Router, n int) {
	g := root.Group("/v")
	for i := 0; i < n; i++ {
		g.GET("/each", eachHandler)
		g = g.Group("/n")
	}
	g.GET("/after", afterHandler)
}
`,
			want: nil,
		},
		{
			name: "switch clause write reaches a fallthrough clause and afterwards as unknown",
			content: `package routes
func register(root *Router, k int) {
	g := root.Group("/s")
	switch k {
	case 1:
		g = root.Group("/one")
		g.GET("/in", inHandler)
		fallthrough
	case 2:
		g.GET("/two", twoHandler)
	}
	g.GET("/after", afterHandler)
}
`,
			want: []string{"/one/in -> inHandler"},
		},
		{
			name: "function-local shadow does not leak into another function",
			content: `package routes
var g = root.Group("/pkg")
var root *Router
func a(root *Router) {
	g := root.Group("/a")
	g.GET("/x", aHandler)
}
func b() {
	g.GET("/y", bHandler)
}
`,
			want: []string{"/a/x -> aHandler", "/pkg/y -> bHandler"},
		},
		{
			name: "closures see outer writes as unknown and invalidate what they write",
			content: `package routes
func register(root *Router) {
	g := root.Group("/a")
	defer func() { g.GET("/late", lateHandler) }()
	inner := func() { g = root.Group("/b") }
	inner()
	g.GET("/after", afterHandler)
	stable := root.Group("/s")
	func() { stable.GET("/in", inHandler) }()
}
`,
			want: []string{"/s/in -> inHandler"},
		},
		{
			name: "package variable written by a function is unknown elsewhere",
			content: `package routes
var api = root.Group("/api")
var root *Router
func setup() {
	api = root.Group("/v2")
	api.GET("/in", inHandler)
}
func register() {
	api.GET("/users", usersHandler)
}
`,
			want: []string{"/v2/in -> inHandler"},
		},
		{
			name: "package initializer cycle and duplicate are unknown",
			content: `package routes
var a = b.Group("/a")
var b = a.Group("/b")
var d = root.Group("/one")
var d = root.Group("/two")
var ok = root.Group("/ok")
func register() {
	a.GET("/x", cycleHandler)
	d.GET("/z", duplicateHandler)
	ok.GET("/y", okHandler)
}
`,
			want: []string{"/ok/y -> okHandler"},
		},
		{
			name: "goto makes statement order meaningless",
			content: `package routes
func register(root *Router) {
	g := root.Group("/a")
	goto set
use:
	g.GET("/x", xHandler)
	return
set:
	g = root.Group("/b")
	goto use
}
`,
			want: nil,
		},
		{
			name: "field receiver sharing a group name is not that group",
			content: `package routes
func register(root *Router, s *server) {
	g := root.Group("/a")
	g.GET("/x", xHandler)
	s.g.GET("/y", yHandler)
	v1 := s.api.Group("/v1")
	v1.GET("/z", zHandler)
}
`,
			want: []string{"/a/x -> xHandler"},
		},
		{
			name: "unparseable file omits group receivers and keeps plain routers",
			content: `package routes
func register(root *Router) {
	g := root.Group("/a")
	g.GET("/x", xHandler)
	root.GET("/health", healthHandler)
	func(
}
`,
			want: []string{"/health -> healthHandler"},
		},
		{
			name: "known empty prefix is the parent, not unknown",
			content: `package routes
func register(root *Router) {
	g := root.Group("")
	g.GET("/x", xHandler)
	child := g.Group("/c")
	child.GET("/y", yHandler)
}
`,
			want: []string{"/x -> xHandler", "/c/y -> yHandler"},
		},
		{
			name: "parallel assignment evaluates every right side first",
			content: `package routes
func register(root *Router) {
	a := root.Group("/a")
	b := root.Group("/b")
	a, b = b, a
	a.GET("/x", aHandler)
	b.GET("/y", bHandler)
}
`,
			want: []string{"/b/x -> aHandler", "/a/y -> bHandler"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertGoRouteSet(t, goRouteRegistrationsWithin(t, tc.content), tc.want...)
		})
	}
}

// A name b never declares may be a package variable from a sibling file: its
// prefix is not in this file. a's local g must not be what b sees. (Treating
// an unresolved receiver as the root router is the long-standing heuristic for
// e.GET-style receivers; omission would also be acceptable here.)
func TestGoRouteBindingLocalDoesNotLeakAcrossFunctions(t *testing.T) {
	regs := goRouteRegistrationsWithin(t, `package routes
func a(root *Router) {
	g := root.Group("/a")
	g.GET("/x", aHandler)
}
func b() {
	g.GET("/y", bHandler)
}
`)
	routes := goRouteSet(regs)
	if !routes["/a/x -> aHandler"] {
		t.Fatalf("missing a's own route: %v", regs)
	}
	for route := range routes {
		if route != "/a/x -> aHandler" && route != "/y -> bHandler" {
			t.Fatalf("a's local binding leaked into b: %v", regs)
		}
	}
}
