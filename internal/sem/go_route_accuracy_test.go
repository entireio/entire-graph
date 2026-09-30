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

// chi: Route closures carry the prefix, Group closures and With keep it, and
// a router's routes are placed under a Mount only when the router is created
// in this file, mounted once, unconditionally, with a static prefix, and never
// handed to anything else that could mount it.
func TestGoRouteAccuracyChi(t *testing.T) {
	runGoRouteAccuracyCases(t, []goRouteAccuracyCase{
		{
			name: "Route, nested Route, Group and With",
			content: `package p
func main() {
	r := chi.NewRouter()
	r.Get("/", indexHandler)
	r.Route("/a", func(r chi.Router) {
		r.Get("/x", xHandler)
		r.Route("/b", func(sub chi.Router) {
			sub.Get("/y", yHandler)
			sub.With(paginate).Get("/p", pHandler)
		})
	})
	r.Group(func(r chi.Router) {
		r.Use(auth)
		r.Get("/g", gHandler)
	})
	r.With(mw).Get("/w", wHandler)
	http.ListenAndServe(":3000", r)
}
`,
			want: []string{"/ -> indexHandler", "/a/x -> xHandler", "/a/b/y -> yHandler", "/a/b/p -> pHandler", "/g -> gHandler", "/w -> wHandler"},
		},
		{
			name: "Route closure of an unclassified parameter type still gets the prefix",
			content: `package p
func routes(r Router) {
	r.Route("/a", func(r Router) { r.Get("/x", xHandler) })
}
`,
			want: []string{"/a/x -> xHandler"},
		},
		{
			name: "Route with a dynamic prefix or a named function",
			content: `package p
func main(prefix string) {
	r := chi.NewRouter()
	r.Route(prefix, func(r chi.Router) { r.Get("/x", xHandler) })
	r.Route("/n", named)
	http.ListenAndServe(":3000", r)
}
func named(r chi.Router) { r.Get("/y", yHandler) }
`,
			want: nil,
		},
		{
			name: "single composable Mount, nested",
			content: `package p
func main() {
	r := chi.NewRouter()
	api := chi.NewRouter()
	admin := chi.NewRouter()
	api.Get("/users", usersHandler)
	admin.Get("/audit", auditHandler)
	api.Route("/v1", func(v chi.Router) { v.Get("/x", xHandler) })
	r.Mount("/api", api)
	api.Mount("/admin", admin)
	srv := &http.Server{Addr: ":80", Handler: r}
	log.Fatal(srv.ListenAndServe())
}
`,
			want: []string{"/api/users -> usersHandler", "/api/admin/audit -> auditHandler", "/api/v1/x -> xHandler"},
		},
		{
			name: "Mount under a group prefix",
			content: `package p
func main() {
	r := chi.NewRouter()
	sub := chi.NewRouter()
	sub.Get("/x", xHandler)
	r.Route("/v1", func(r chi.Router) { r.Mount("/sub", sub) })
	http.ListenAndServe(":3000", r)
}
`,
			want: nil,
		},
		{
			name: "Mount twice, conditionally, or with a dynamic prefix",
			content: `package p
func main(on bool, p string) {
	r := chi.NewRouter()
	twice := chi.NewRouter()
	twice.Get("/t", tHandler)
	r.Mount("/a", twice)
	r.Mount("/b", twice)
	cond := chi.NewRouter()
	cond.Get("/c", cHandler)
	if on {
		r.Mount("/c", cond)
	}
	dyn := chi.NewRouter()
	dyn.Get("/d", dHandler)
	r.Mount(p, dyn)
	http.ListenAndServe(":3000", r)
}
`,
			want: nil,
		},
		{
			name: "returned router (todos-resource shape)",
			content: `package p
type todosResource struct{}
func (rs todosResource) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", rs.List)
	r.Route("/{id}", func(r chi.Router) {
		r.Get("/", rs.Get)
	})
	return r
}
func main() {
	r := chi.NewRouter()
	r.Mount("/todos", todosResource{}.Routes())
	r.Get("/", rootHandler)
	http.ListenAndServe(":3333", r)
}
`,
			want: []string{"/ -> rootHandler"},
		},
		{
			name: "router passed, stored, addressed or package-level",
			content: `package p
var pkg = chi.NewRouter()
var stored chi.Router
type S struct{ r chi.Router }
func a() { r := chi.NewRouter(); r.Get("/a", aHandler); register(r) }
func b(s *S) { r := chi.NewRouter(); r.Get("/b", bHandler); s.r = r }
func c() { r := chi.NewRouter(); r.Get("/c", cHandler); stored = r }
func d() { r := chi.NewRouter(); r.Get("/d", dHandler); keep(&r) }
func e() { pkg.Get("/e", eHandler) }
func f() { r := chi.NewRouter(); r.Get("/f", fHandler); h := r.ServeHTTP; use(h) }
func g() { r := chi.NewRouter(); alias := r; alias.Get("/g", gHandler); defer register(alias) }
func h() { r := chi.NewRouter(); r.Get("/h", hHandler); _ = S{r: r} }
`,
			want: nil,
		},
		{
			name: "serving and Handle are not escapes",
			content: `package p
func main() {
	r := chi.NewRouter()
	r.Get("/a", aHandler)
	top := http.NewServeMux()
	top.Handle("/", r)
	http.ListenAndServe(":80", top)
	go http.ListenAndServeTLS(":443", "c", "k", r)
}
`,
			want: []string{"/a -> aHandler"},
		},
	})
}

// fiber: Route closures carry the prefix (with or without a route name),
// Group accepts middleware, a v2 Mount of an app created here is composed, and
// an app handed to Use (v3 mounting) or returned is unknown.
func TestGoRouteAccuracyFiber(t *testing.T) {
	runGoRouteAccuracyCases(t, []goRouteAccuracyCase{
		{
			name: "Route, named Route, Group with middleware",
			content: `package p
func main() {
	app := fiber.New()
	app.Get("/", indexHandler)
	app.Route("/a", func(api fiber.Router) {
		api.Get("/x", xHandler)
	})
	v1 := app.Group("/api", logger)
	v1.Route("/users", func(r fiber.Router) {
		r.Get("/list", listHandler)
	}, "users.")
	log.Fatal(app.Listen(":3000"))
}
`,
			want: []string{"/ -> indexHandler", "/a/x -> xHandler", "/api/users/list -> listHandler"},
		},
		{
			name: "v2 Mount is composed once",
			content: `package p
func main() {
	app := fiber.New()
	micro := fiber.New()
	micro.Get("/doe", doeHandler)
	app.Mount("/john", micro)
	app.Listen(":3000")
}
`,
			want: []string{"/john/doe -> doeHandler"},
		},
		{
			name: "app handed to Use, returned, or mounted twice",
			content: `package p
func a() {
	app := fiber.New()
	sub := fiber.New()
	sub.Get("/u", uHandler)
	app.Use("/v3", sub)
	app.Listen(":3000")
}
func setup() *fiber.App {
	app := fiber.New()
	app.Get("/s", sHandler)
	return app
}
func b() {
	app := fiber.New()
	m := fiber.New()
	m.Get("/m", mHandler)
	app.Mount("/one", m)
	app.Mount("/two", m)
	app.Listen(":3000")
}
`,
			want: nil,
		},
	})
}

// gorilla: PathPrefix(..).Subrouter() is a router under that prefix, and
// HandleFunc/Handle place their route by their receiver exactly like router
// methods: an unknown or field receiver is omitted.
func TestGoRouteAccuracyGorillaAndHandleFuncReceivers(t *testing.T) {
	runGoRouteAccuracyCases(t, []goRouteAccuracyCase{
		{
			name: "Subrouter, nested and inline",
			content: `package p
func main() {
	r := mux.NewRouter()
	r.HandleFunc("/root", rootHandler)
	s := r.PathPrefix("/a").Subrouter()
	s.HandleFunc("/x", xHandler)
	s.HandleFunc("/y", yHandler).Methods("GET")
	s.Handle("/w", http.HandlerFunc(wHandler))
	b := s.PathPrefix("/b").Subrouter()
	b.HandleFunc("/z", zHandler)
	r.PathPrefix("/i").Subrouter().HandleFunc("/j", jHandler)
	http.ListenAndServe(":8080", r)
}
`,
			want: []string{"/root -> rootHandler", "/a/x -> xHandler", "/a/y -> yHandler", "/a/w -> wHandler", "/a/b/z -> zHandler", "/i/j -> jHandler"},
		},
		{
			name: "Subrouter chains this pass cannot place",
			content: `package p
func main(p string) {
	r := mux.NewRouter()
	h := r.Host("api.example.com").Subrouter()
	h.HandleFunc("/h", hHandler)
	d := r.PathPrefix(p).Subrouter()
	d.HandleFunc("/d", dHandler)
	m := r.PathPrefix("/m").Methods("GET").Subrouter()
	m.HandleFunc("/m", mHandler)
	t := r.PathPrefix("/t/").Subrouter()
	t.HandleFunc("/t", tHandler)
}
`,
			want: nil,
		},
		{
			name: "HandleFunc receivers: unknown, field, bare, gorilla package",
			content: `package p
import "github.com/gorilla/mux"
type S struct{ mux *http.ServeMux }
func a(r *mux.Router) { r.HandleFunc("/param", paramHandler) }
func (s *S) b() { s.mux.HandleFunc("/field", fieldHandler) }
func c() { HandleFunc("/bare", bareHandler) }
func d() { router.HandleFunc("/undeclared", undeclaredHandler) }
func e() { mux.HandleFunc("/pkg", pkgHandler) }
func f() { r := newRouter(); r.Handle("/call", http.HandlerFunc(callHandler)) }
`,
			want: nil,
		},
		{
			name: "HandleFunc receivers that are roots",
			content: `package p
import "net/http"
func a() { http.HandleFunc("/default", defaultHandler) }
func b() { m := http.NewServeMux(); m.HandleFunc("/local", localHandler); http.ListenAndServe(":80", m) }
func c(m *http.ServeMux) { m.Handle("/param", http.HandlerFunc(paramHandler)) }
`,
			want: []string{"/default -> defaultHandler", "/local -> localHandler", "/param -> paramHandler"},
		},
	})
}

// http.StripPrefix serves a handler under a prefix its registrations do not
// show. It is never composed: routers inside it are omitted, and a ServeMux
// that escapes (where a StripPrefix usually happens) is omitted too.
func TestGoRouteAccuracyStripPrefixIsNotGuessed(t *testing.T) {
	runGoRouteAccuracyCases(t, []goRouteAccuracyCase{
		{
			name: "stripped ServeMux, wrapped engine, method value",
			content: `package p
func main() {
	top := http.NewServeMux()
	api := http.NewServeMux()
	top.Handle("/api/", http.StripPrefix("/api", api))
	api.HandleFunc("/x", xHandler)
	top.HandleFunc("/health", healthHandler)
	g := gin.New()
	g.GET("/g", gHandler)
	top.Handle("/g/", http.StripPrefix("/g", logging(g)))
	r := mux.NewRouter()
	r.HandleFunc("/r", rHandler)
	top.Handle("/r/", http.StripPrefix("/r", http.HandlerFunc(r.ServeHTTP)))
	lit := &http.ServeMux{}
	lit.HandleFunc("/l", lHandler)
	top.Handle("/l/", http.StripPrefix("/l", lit))
	e := echo.New()
	e.GET("/e", eHandler)
	top.Handle("/e/", http.StripPrefix("/e", e))
	http.ListenAndServe(":80", top)
}
`,
			want: []string{"/health -> healthHandler"},
		},
		{
			name: "escaped ServeMux",
			content: `package p
func apiRoutes() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("/users", usersHandler)
	return m
}
func other() {
	m := http.NewServeMux()
	m.HandleFunc("/o", oHandler)
	wrap(m)
}
func main() {
	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", apiRoutes()))
	mux.HandleFunc("/", rootHandler)
	http.ListenAndServe(":80", mux)
}
`,
			want: []string{"/ -> rootHandler"},
		},
	})
}

// Recall without guessing: a router this file serves itself is the top-level
// router, so handing it to read-only helpers does not make it unknown; a
// framework's own package constructs routers unqualified; new(T) of a router
// whose zero value works is a root.
func TestGoRouteAccuracyServedAndInPackageRoots(t *testing.T) {
	runGoRouteAccuracyCases(t, []goRouteAccuracyCase{
		{
			name: "served routers survive read-only escapes",
			content: `package p
func a() {
	r := chi.NewRouter()
	r.Get("/a", aHandler)
	fmt.Println(docgen.MarkdownRoutesDoc(r))
	http.ListenAndServe(":80", r)
}
func b(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/b", bHandler)
	ts := httptest.NewServer(r)
	defer ts.Close()
	check(t, r)
}
func c() {
	app := fiber.New()
	app.Get("/c", cHandler)
	helper(app)
	app.Test(req)
}
func d() {
	r := chi.NewRouter()
	r.Put("/d", dHandler)
	chi.Walk(r, walkFn)
}
`,
			want: []string{"/a -> aHandler", "/b -> bHandler", "/c -> cHandler", "/d -> dHandler"},
		},
		{
			name: "unserved escapes and served-and-mounted stay unknown",
			content: `package p
func a() {
	r := chi.NewRouter()
	r.Get("/a", aHandler)
	helper(r)
}
func b() {
	top := chi.NewRouter()
	sub := chi.NewRouter()
	sub.Get("/b", bHandler)
	top.Mount("/sub", sub)
	http.ListenAndServe(":80", sub)
	http.ListenAndServe(":81", top)
}
func c() {
	app := fiber.New()
	app.Get("/c", cHandler)
	app2 := buildApp()
	app2.Test(req)
	helper(app)
}
`,
			want: nil,
		},
		{
			name: "in-package constructors",
			content: `package chi
func TestX(t *testing.T) {
	r := NewRouter()
	r.Get("/x", xHandler)
	ts := httptest.NewServer(r)
	defer ts.Close()
}
`,
			want: []string{"/x -> xHandler"},
		},
		{
			name: "in-package constructor names outside the framework package are unknown",
			content: `package server
func main() {
	r := NewRouter()
	r.Get("/x", xHandler)
	http.ListenAndServe(":80", r)
}
`,
			want: nil,
		},
		{
			name: "new of a router type",
			content: `package p
func a() { r := new(mux.Router); r.HandleFunc("/a", aHandler) }
func b() { r := new(Thing); r.HandleFunc("/b", bHandler) }
func c() { e := echo.NewWithConfig(cfg); e.GET("/c", cHandler) }
`,
			want: []string{"/a -> aHandler", "/c -> cHandler"},
		},
	})
}
