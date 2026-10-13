package sem

import "testing"

func TestGoRouteClosureAddressEscapeUsesLexicalBinding(t *testing.T) {
	for _, closure := range []string{
		`func() { g := 1; _ = &g }`,
		`func(g int) { _ = &g }`,
		`func() { var g int; child := func() { _ = &g }; _ = child }`,
	} {
		source := `package routes
func register(root *Router) {
 g := root.Group("/outer")
 g.GET("/before", beforeHandler)
 f := ` + closure + `
 _ = f
 g.GET("/after", afterHandler)
}`
		assertGoRouteSet(t, goRouteRegistrationsWithin(t, source),
			"/outer/before -> beforeHandler", "/outer/after -> afterHandler")
	}
	// An actual outer-binding escape still invalidates later direct reassignments.
	source := `package routes
func register(root *Router) {
 g := root.Group("/outer")
 g.GET("/before", beforeHandler)
 f := func() { _ = &g }
 _ = f
 g = root.Group("/rebound")
 g.GET("/after", afterHandler)
}`
	assertGoRouteSet(t, goRouteRegistrationsWithin(t, source), "/outer/before -> beforeHandler")
}
