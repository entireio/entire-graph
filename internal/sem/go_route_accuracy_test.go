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

// A receiver is the root router only when this file shows it is: a framework
// constructor, the net/http package, or a parameter/variable typed as a
// framework root. A group or sub-router parameter, a call result, a value of
// several results, and a name this file never declares hold a prefix set
// somewhere else, so their routes are omitted instead of re-rooted.
func TestGoRouteAccuracyUnknownOriginsAreOmitted(t *testing.T) {
	runGoRouteAccuracyCases(t, []goRouteAccuracyCase{
		{
			name: "group and sub-router parameters",
			content: `package p
func a(g *echo.Group) { g.GET("/echo", echoHandler) }
func b(rg *gin.RouterGroup) { rg.GET("/gin", ginHandler) }
func c(ir gin.IRouter) { ir.GET("/irouter", irouterHandler) }
func d(fr fiber.Router) { fr.Get("/fiber", fiberHandler) }
func e(cr chi.Router) { cr.Get("/chi", chiHandler) }
func f(mr *mux.Router) { mr.Methods("GET"); mr.Get("/mux", muxHandler) }
func g(lg *Group) { lg.GET("/local", localHandler) }
func h(pg *server.Router) { pg.GET("/pkg", pkgHandler) }
func i(vs ...*Router) { vs[0].GET("/v", vHandler) }
func j(api *echo.Group) { u := api.Group("/users"); u.GET("/list", listHandler) }
`,
			want: nil,
		},
		{
			name: "framework root parameters stay root",
			content: `package p
func a(e *echo.Echo) { e.GET("/echo", echoHandler) }
func b(r *gin.Engine) { r.GET("/gin", ginHandler); v1 := r.Group("/v1"); v1.GET("/x", xHandler) }
func c(rt *httprouter.Router) { rt.GET("/hr", hrHandler) }
`,
			want: []string{"/echo -> echoHandler", "/gin -> ginHandler", "/v1/x -> xHandler", "/hr -> hrHandler"},
		},
		{
			name: "call results and multi-value results",
			content: `package p
func api(e *echo.Echo) *echo.Group { return e.Group("/api") }
func mk() (*echo.Group, error) { return nil, nil }
func main() {
	e := echo.New()
	g := api(e)
	g.GET("/x", xHandler)
	r := setupRouter()
	r.GET("/y", yHandler)
	h, err := mk()
	_ = err
	h.GET("/z", zHandler)
	var k, err2 = mk()
	_ = err2
	k.GET("/k", kHandler)
	w := e.Wrap()
	w.GET("/w", wHandler)
	e.GET("/root", rootHandler)
}
`,
			want: []string{"/root -> rootHandler"},
		},
		{
			name: "names this file never declares",
			content: `package p
var users = api.Group("/users")
func init() {
	users.GET("/x", xHandler)
	router.GET("/y", yHandler)
	router.Group("/v1").GET("/z", zHandler)
}
`,
			want: nil,
		},
		{
			name: "typed variables follow their type",
			content: `package p
var pg *echo.Group
var pe *echo.Echo
func main() {
	var lg *gin.RouterGroup
	var le *gin.Engine
	pg.GET("/pg", pgHandler)
	pe.GET("/pe", peHandler)
	lg.GET("/lg", lgHandler)
	le.GET("/le", leHandler)
}
`,
			want: []string{"/pe -> peHandler", "/le -> leHandler"},
		},
		{
			name: "constructors are roots, through import aliases, unless shadowed",
			content: `package p
import (
	ec "github.com/labstack/echo/v4"
	"github.com/gin-gonic/gin"
	"example.com/echo"
)
func a() { e := ec.New(); e.GET("/alias", aliasHandler) }
func b() { r := gin.Default(); r.GET("/gin", ginHandler) }
func c() { e := echo.New(); e.GET("/notecho", notEchoHandler) }
func d(gin *Thing) { r := gin.Default(); r.GET("/shadow", shadowHandler) }
func f() { m := &http.ServeMux{}; m.Handle("/lit", http.HandlerFunc(litHandler)); m.GET("/lit2", lit2Handler) }
`,
			want: []string{"/alias -> aliasHandler", "/gin -> ginHandler", "/lit -> litHandler", "/lit2 -> lit2Handler"},
		},
	})
}
