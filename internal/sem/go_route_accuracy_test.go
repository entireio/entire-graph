package sem

import (
	"sort"
	"strings"
	"testing"
)

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
func register(e *Echo, g *echo.Group, r chi.Router, s *server) {
	s.e.GET("/sel", selHandler)
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
			// A ServeMux has no verb methods: m.GET is not a registration.
			want: []string{"/alias -> aliasHandler", "/gin -> ginHandler", "/lit -> litHandler"},
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
			name: "an alias of a mounted router is the same instance",
			content: `package p
func main() {
	r := chi.NewRouter()
	api := chi.NewRouter()
	alias := api
	alias.Get("/al", alHandler)
	r.Mount("/api", api)
	http.ListenAndServe(":80", r)
}
`,
			want: []string{"/api/al -> alHandler"},
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

// A withheld or composed Go registration must not reappear as a bare path
// through the pattern-level route literal fallback, and a composed route is
// emitted exactly. (todos-resource: Routes() returns a router main mounts
// under /todos, which is not composable, so nothing under it is emitted.)
func TestGoRouteAccuracySnapshotDoesNotResurrectBarePaths(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "todos.go", `package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

type todosResource struct{}

func (rs todosResource) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/list", rs.List)
	r.Route("/{id}", func(r chi.Router) {
		r.Get("/sync", rs.Sync)
	})
	return r
}

func (rs todosResource) List(w http.ResponseWriter, r *http.Request) {}

func (rs todosResource) Sync(w http.ResponseWriter, r *http.Request) {}
`)
	writeFile(t, repo, "main.go", `package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func main() {
	r := chi.NewRouter()
	r.Mount("/todos", todosResource{}.Routes())
	r.Route("/api", func(r chi.Router) {
		r.Get("/health", health)
	})
	http.ListenAndServe(":3333", r)
}

func health(w http.ResponseWriter, r *http.Request) {}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type != "HANDLES_ROUTE" {
			continue
		}
		switch relation.ToID {
		// (A Route/Group prefix literal such as "/{id}" is a separate,
		// pre-existing pattern-level match and is not asserted here.)
		case externalID("route", "/list"), externalID("route", "/sync"),
			externalID("route", "/{id}/sync"), externalID("route", "/health"):
			t.Fatalf("bare or unmounted path emitted: %#v", relation)
		}
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "health", "/api/health") {
		t.Fatalf("missing composed /api/health: %#v", snapshot.Relations)
	}
}

// Each framework joins a prefix and a path its own way: gin path.Join with ""
// and "/" distinct, echo verbatim concatenation, fiber TrimRight+path with a
// slash added, gorilla TrimRight(parent, "/")+template.
func TestGoRouteAccuracyPerFrameworkJoin(t *testing.T) {
	runGoRouteAccuracyCases(t, []goRouteAccuracyCase{
		{
			name: "gin",
			content: `package p
func main() {
	r := gin.New()
	g := r.Group("/g/")
	g.GET("/x", xHandler)
	h := r.Group("/h")
	h.GET("x", relHandler)
	h.GET("", emptyHandler)
	h.GET("/", slashHandler)
	v1 := r.Group("v1")
	v1.GET("users/add", addHandler)
	r.GET("admin", adminHandler)
	r.Run(":1")
}
`,
			want: []string{"/g/x -> xHandler", "/h/x -> relHandler", "/h -> emptyHandler", "/h/ -> slashHandler", "/v1/users/add -> addHandler", "/admin -> adminHandler"},
		},
		{
			name: "echo",
			content: `package p
func main() {
	e := echo.New()
	g := e.Group("/g/")
	g.GET("/x", xHandler)
	h := e.Group("/h")
	h.GET("x", relHandler)
	h.GET("", emptyHandler)
	h.GET("/", slashHandler)
	e.GET("unrelated", unrelatedHandler)
	e.Start(":1")
}
`,
			want: []string{"/g//x -> xHandler", "/hx -> relHandler", "/h -> emptyHandler", "/h/ -> slashHandler", "/unrelated -> unrelatedHandler"},
		},
		{
			name: "fiber",
			content: `package p
func main() {
	app := fiber.New()
	g := app.Group("/g/")
	g.Get("/x", xHandler)
	h := app.Group("/h")
	h.Get("x", relHandler)
	h.Get("", emptyHandler)
	h.Get("/", slashHandler)
	app.Listen(":1")
}
`,
			want: []string{"/g/x -> xHandler", "/h/x -> relHandler", "/h -> emptyHandler", "/h/ -> slashHandler"},
		},
		{
			name: "gorilla slash-terminated prefix is composable",
			content: `package p
func main() {
	r := mux.NewRouter()
	s := r.PathPrefix("/sub/").Subrouter()
	s.HandleFunc("/", slashHandler)
	s.HandleFunc("/x", xHandler)
	a := r.PathPrefix("/a").Subrouter()
	a.HandleFunc("/", aSlashHandler)
	http.ListenAndServe(":1", r)
}
`,
			want: []string{"/sub/ -> slashHandler", "/sub/x -> xHandler", "/a/ -> aSlashHandler"},
		},
		{
			name: "unclassified routers still need an absolute path",
			content: `package p
func register(root *Router) {
	g := root.Group("v1")
	g.GET("/x", xHandler)
	root.GET("rel", relHandler)
}
`,
			want: nil,
		},
	})
}

// A framework-root-typed parameter is root only if every same-file caller
// passes a router served at the root.
func TestGoRouteAccuracyRootParamsFollowSameFileCallers(t *testing.T) {
	runGoRouteAccuracyCases(t, []goRouteAccuracyCase{
		{
			name: "caller StripPrefix-mounts the argument",
			content: `package p
func ginRoutes(e *gin.Engine) { e.GET("/g", gHandler) }
func echoRoutes(e *echo.Echo) { e.GET("/e", eHandler) }
func muxRoutes(mux *http.ServeMux) { mux.HandleFunc("/m", mHandler) }
func forward(e *gin.Engine) { ginRoutes2(e) }
func ginRoutes2(e *gin.Engine) { e.GET("/f", fHandler) }
func main() {
	root := gin.New()
	sub := gin.New()
	ginRoutes(sub)
	forward(sub)
	root.Any("/api/*p", gin.WrapH(http.StripPrefix("/api", sub)))
	eroot := echo.New()
	esub := echo.New()
	echoRoutes(esub)
	eroot.Any("/api/*", echo.WrapHandler(http.StripPrefix("/api", esub)))
	api := http.NewServeMux()
	muxRoutes(api)
	http.Handle("/api/", http.StripPrefix("/api", api))
	root.Run(":1")
}
`,
			want: nil,
		},
		{
			name: "caller passes the served root",
			content: `package p
func ginRoutes(e *gin.Engine) { e.GET("/g", gHandler) }
func muxRoutes(mux *http.ServeMux) { mux.HandleFunc("/m", mHandler) }
func main() {
	r := gin.Default()
	ginRoutes(r)
	r.Run(":1")
	m := http.NewServeMux()
	muxRoutes(m)
	http.ListenAndServe(":2", m)
}
`,
			want: []string{"/g -> gHandler", "/m -> mHandler"},
		},
		{
			name: "caller passes a group, or the function is used as a value",
			content: `package p
func ginRoutes(e *gin.Engine) { e.GET("/g", gHandler) }
func valueRoutes(e *gin.Engine) { e.GET("/v", vHandler) }
func main() {
	r := gin.Default()
	ginRoutes(pick(r))
	register(valueRoutes)
	r.Run(":1")
}
`,
			want: nil,
		},
	})
}

// Cheap, sound recall: a same-file helper that only calls methods on the
// router does not make it escape; echo Host keeps the path; echo virtual-host
// map values are ordinary roots.
func TestGoRouteAccuracyReadOnlyHelpersAndHosts(t *testing.T) {
	runGoRouteAccuracyCases(t, []goRouteAccuracyCase{
		{
			name: "fiber app handed to a read-only helper",
			content: `package p
func check(t *testing.T, app *fiber.App, path string) { resp, _ := app.Test(req(path)); _ = resp }
func TestX(t *testing.T) {
	app := fiber.New()
	app.Get("/x", xHandler)
	check(t, app, "/x")
}
`,
			want: []string{"/x -> xHandler"},
		},
		{
			name: "helpers that return, pass on or mount the app are escapes",
			content: `package p
func keep(app *fiber.App) *fiber.App { return app }
func pass(app *fiber.App) { other(app) }
func mountIt(app *fiber.App) { parent.Mount("/p", app) }
func a() { app := fiber.New(); app.Get("/a", aHandler); keep(app) }
func b() { app := fiber.New(); app.Get("/b", bHandler); pass(app) }
func c() { app := fiber.New(); app.Get("/c", cHandler); mountIt(app) }
`,
			want: nil,
		},
		{
			name: "echo Host and virtual hosts",
			content: `package p
func main() {
	e := echo.New()
	teapot := e.Host("teapot.example")
	teapot.GET("/brew", brewHandler)
	ok := echo.New()
	ok.GET("/ok", okHandler)
	vh := echo.NewVirtualHostHandler(map[string]*echo.Echo{"ok.com": ok})
	vh.GET("/vh", vhHandler)
	e.Start(":1")
}
`,
			want: []string{"/brew -> brewHandler", "/ok -> okHandler"},
		},
		{
			name: "gorilla Host is not path-preserving here",
			content: `package p
func main() {
	r := mux.NewRouter()
	h := r.Host("x.example")
	h.HandleFunc("/h", hHandler)
}
`,
			want: nil,
		},
	})
}

// At the edges level an inline or local-variable handler gets the composed
// route from its enclosing function, and the bare literal of any
// registration the Go pass decided is never re-emitted by the route literal
// fallback. The claim is by position: an identical literal registered
// correctly elsewhere is still emitted.
func TestGoRouteAccuracySnapshotInlineHandlersAndPositions(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "main.go", `package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func healthHandler(w http.ResponseWriter, r *http.Request) {}

func adminRoutes() chi.Router {
	sub := chi.NewRouter()
	sub.Get("/health", healthHandler)
	sub.Get("/stats", func(w http.ResponseWriter, r *http.Request) {})
	return sub
}

func main() {
	r := chi.NewRouter()
	local := func(w http.ResponseWriter, r *http.Request) {}
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {})
	r.Route("/api", func(r chi.Router) {
		r.Get("/users", local)
		r.Get("/items", func(w http.ResponseWriter, r *http.Request) {})
	})
	r.Mount("/admin", adminRoutes())
	m := http.NewServeMux()
	m.HandleFunc("/x", healthHandler)
	http.Handle("/a/", http.StripPrefix("/a", m))
	// r.Get("/commented", healthHandler)
	http.ListenAndServe(":1", r)
}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, relation := range snapshot.Relations {
		if relation.Type == "HANDLES_ROUTE" {
			got[lastSegment(relation.FromID)+" "+relation.ToID] = true
		}
	}
	for _, want := range []string{"main /health", "main /api/users", "main /api/items", "main /a/"} {
		if !got[strings.Replace(want, " ", " "+externalID("route", ""), 1)] {
			t.Fatalf("missing %s: %v", want, got)
		}
	}
	for key := range got {
		for _, bare := range []string{"/users", "/items", "/stats", "/x", "/a", "/api", "/admin", "/commented"} {
			if strings.HasSuffix(key, " "+externalID("route", bare)) {
				t.Fatalf("bare or undetermined path emitted: %s (all: %v)", key, got)
			}
		}
		if strings.HasPrefix(key, "adminRoutes ") || strings.HasPrefix(key, "healthHandler ") {
			t.Fatalf("route of a returned or StripPrefix'd router emitted: %s (all: %v)", key, got)
		}
	}
}

// Inline registrations (closures, calls, middleware arguments, Method and
// gin Handle forms) get exactly their composed route, and call shapes that
// only look like registrations get none.
func TestGoRouteAccuracyInlineRegistrations(t *testing.T) {
	for _, tc := range []goRouteAccuracyCase{
		{
			name: "closures, middleware arguments and Method forms",
			content: `package p
func main() {
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		r.Get("/items", func(w http.ResponseWriter, req *http.Request) {})
		r.Method("POST", "/m", handler)
		r.MethodFunc("PUT", "/mf", hf)
	})
	e := echo.New()
	g := e.Group("/g")
	g.GET("/mw", h, m2)
	http.ListenAndServe(":1", r)
}
`,
			want: []string{"/api/items", "/api/m", "/api/mf", "/g/mw"},
		},
		{
			name: "gin Handle(method, path, handlers...) and handler-less calls",
			content: `package p
func main() {
	router := gin.New()
	router.Handle("GET", "/x", h)
	router.Handle("PO ST", "/")
	router.Run(":1")
}
`,
			want: []string{"/x"},
		},
		{
			name: "net/http client calls are not registrations",
			content: `package p
func main() {
	http.Post("/api/x", "application/json", body)
	http.HandleFunc("/y", func(w http.ResponseWriter, r *http.Request) {})
}
`,
			want: []string{"/y"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, inline := range goRouteRegistrationsDetailed(tc.content, nil).inline {
				got = append(got, inline.Route)
			}
			sort.Strings(got)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("inline routes = %v, want %v", got, tc.want)
			}
		})
	}
}

// Inside a framework's own package, its unqualified types are classified like
// the qualified ones: a group-typed parameter there is unknown, a root-typed
// one is root.
func TestGoRouteAccuracyInPackageTypes(t *testing.T) {
	runGoRouteAccuracyCases(t, []goRouteAccuracyCase{
		{
			name: "gin IRoutes parameter and *Engine parameter",
			content: `package gin
func testRoutes(t *testing.T, r IRoutes) { r.GET("/any", anyHandler) }
func setup(e *Engine) { e.GET("/root", rootHandler) }
func TestX(t *testing.T) {
	router := New()
	testRoutes(t, router)
	testRoutes(t, router.Group("/v1"))
	setup(router)
	router.Run()
}
`,
			want: []string{"/root -> rootHandler"},
		},
	})
}

// A file that only builds groups (and hands them to helpers elsewhere)
// registers nothing, and its prefix literals are not routes either.
func TestGoRouteAccuracySnapshotPrefixOnlyFile(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "main.go", `package main

import "github.com/gin-gonic/gin"

var router = gin.Default()

func getRoutes() {
	v1 := router.Group("/v1")
	addUserRoutes(v1)
}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "HANDLES_ROUTE" {
			t.Fatalf("a group prefix was emitted as a route: %#v", relation)
		}
	}
}

// A function literal's parameter shadows the outer router name whatever its
// type, and whoever calls the literal (go, defer, an immediate call, a
// callback argument) decides its value.
func TestGoRouteAccuracyFuncLitParamsShadow(t *testing.T) {
	content := `package p
type client struct{}
func (client) Get(p string, h any) {}
func main() {
	r := chi.NewRouter()
	func(r client) { r.Get("/param-shadow", hA) }(client{})
	go func(r client) { r.Get("/go-shadow", nil) }(client{})
	defer func(r client) { r.Get("/defer-shadow", hA) }(client{})
	register(func(r client) { r.Get("/callback-shadow", hA) })
	r.Get("/outer", hA)
	http.ListenAndServe(":1", r)
}
`
	assertGoRouteSet(t, goRouteRegistrationsWithin(t, content), "/outer -> hA")
	if inline := goRouteRegistrationsDetailed(content, nil).inline; len(inline) != 0 {
		t.Fatalf("shadowed func-literal parameter produced inline routes: %v", inline)
	}
}

// The F1 taint is keyed by receiver type and name: a StripPrefix'd argument
// to (A).register does not make (B).register unknown. A call whose receiver
// type is not visible still reaches every method of that name.
func TestGoRouteAccuracyRootParamTaintByReceiverType(t *testing.T) {
	runGoRouteAccuracyCases(t, []goRouteAccuracyCase{
		{
			name: "composite receivers",
			content: `package p
type A struct{}
type B struct{}
func (A) register(e *gin.Engine) { e.GET("/t3a", hA) }
func (B) register(e *gin.Engine) { e.GET("/t3b", hB) }
func register(e *gin.Engine) { e.GET("/pkg", hP) }
func main() {
	root := gin.New()
	sub := gin.New()
	A{}.register(sub)
	(&B{}).register(root)
	register(root)
	root.Any("/api/*p", gin.WrapH(http.StripPrefix("/api", sub)))
	root.Run(":1")
}
`,
			want: []string{"/t3b -> hB", "/pkg -> hP"},
		},
		{
			name: "receiver type not visible reaches every method of the name",
			content: `package p
type A struct{}
type B struct{}
func (A) register(e *gin.Engine) { e.GET("/t3a", hA) }
func (B) register(e *gin.Engine) { e.GET("/t3b", hB) }
func main() {
	root := gin.New()
	sub := gin.New()
	var a A
	a.register(sub)
	B{}.register(root)
	root.Any("/api/*p", gin.WrapH(http.StripPrefix("/api", sub)))
	root.Run(":1")
}
`,
			want: nil,
		},
		{
			name: "chi Use(middleware.StripPrefix) rewrites every route",
			content: `package p
func main() {
	r := chi.NewRouter()
	r.Use(middleware.StripPrefix("/api"))
	r.Get("/accounts", hA)
	http.ListenAndServe(":1", r)
}
`,
			want: nil,
		},
	})
}

// String arguments of calls whose receiver is known not to be a router
// (request contexts, HTTP clients and request constructors, request helpers)
// never reach the route literal fallback; a literal on a receiver of unknown
// type is left to it.
func TestGoRouteAccuracySnapshotNonRouterLiterals(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "main.go", `package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gofiber/fiber/v2"
)

var client = &http.Client{}

func testRequest(t *testing.T, method, path string) {}

func handler(c *gin.Context) {
	c.Redirect(http.StatusFound, "/redirect-target")
	c.Set("/context-key", 1)
}

func fiberHandler(c *fiber.Ctx) error {
	fiber.Post("/fiber-client-post")
	return c.Redirect("/fiber-redirect")
}

func TestX(t *testing.T) {
	http.Get("/client-get")
	req := httptest.NewRequest("GET", "/test-request", nil)
	_ = req
	client.Get("/client-var")
	testRequest(t, "GET", "/helper-request")
	cache.Get("/unknown-receiver")
}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, relation := range snapshot.Relations {
		if relation.Type == "HANDLES_ROUTE" {
			got[relation.ToID] = true
		}
	}
	for _, path := range []string{"/redirect-target", "/context-key", "/fiber-client-post", "/fiber-redirect", "/client-get", "/test-request", "/client-var", "/helper-request"} {
		if got[externalID("route", path)] {
			t.Fatalf("non-router literal %s emitted as a route: %v", path, got)
		}
	}
	if !got[externalID("route", "/unknown-receiver")] {
		t.Fatalf("a literal on an unknown receiver must be left to the route literal fallback: %v", got)
	}
}
