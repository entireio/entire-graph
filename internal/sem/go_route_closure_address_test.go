package sem

import "testing"

// A closure that declares its OWN g and takes that local's address has not
// exposed the enclosing g. Resolving the address-of against the enclosing
// scope marked the unrelated outer binding escaped, and an escape is
// permanent: even a later plain reassignment of the outer g stayed unknown.
func TestGoRouteClosureLocalAddressDoesNotEscapeOuter(t *testing.T) {
	assertGoRouteSet(t, goRouteRegistrationsWithin(t, `package routes
func register(e *Echo) {
	g := e.Group("/a")
	helper := func() {
		g := e.Group("/inner")
		set(&g, e)
	}
	helper()
	g = e.Group("/c")
	g.GET("/x", xHandler)
}
`), "/c/x -> xHandler")
}

// The outer g's address taken inside a closure still escapes it.
func TestGoRouteClosureOuterAddressStillEscapes(t *testing.T) {
	assertGoRouteSet(t, goRouteRegistrationsWithin(t, `package routes
func register(e *Echo) {
	g := e.Group("/a")
	helper := func() {
		set(&g, e)
	}
	helper()
	g = e.Group("/c")
	g.GET("/x", xHandler)
}
`))
}

// A closure that both shadows g in an inner block and takes the OUTER g's
// address elsewhere still escapes the outer g, and so does a nested closure
// taking the outer g's address.
func TestGoRouteClosureMixedShadowAndOuterAddressEscapes(t *testing.T) {
	for name, body := range map[string]string{
		"block shadow then outer address": `{
			g := e.Group("/inner")
			set(&g, e)
		}
		set(&g, e)`,
		"nested closure takes outer address": `func() { keep(&g) }()
		{
			g := e.Group("/inner")
			_ = g
		}`,
		"parameter shadow is local": `_ = 1`,
	} {
		t.Run(name, func(t *testing.T) {
			regs := goRouteRegistrationsWithin(t, `package routes
func register(e *Echo) {
	g := e.Group("/a")
	helper := func() {
		`+body+`
	}
	helper()
	g = e.Group("/c")
	g.GET("/x", xHandler)
}
`)
			if name == "parameter shadow is local" {
				assertGoRouteSet(t, regs, "/c/x -> xHandler")
				return
			}
			assertGoRouteSet(t, regs)
		})
	}
	assertGoRouteSet(t, goRouteRegistrationsWithin(t, `package routes
func register(e *Echo) {
	g := e.Group("/a")
	helper := func(g *Group) {
		set(&g, e)
	}
	helper(nil)
	g = e.Group("/c")
	g.GET("/x", xHandler)
}
`), "/c/x -> xHandler")
}
