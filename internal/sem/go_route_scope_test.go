package sem

import "testing"

// runGoRouteCasesWithFileConstants runs cases with the constant table the
// provider derives from the file (staticStringConstants), not an empty one:
// scoping is only observable when the file-wide table holds the shadowed name.
func runGoRouteCasesWithFileConstants(t *testing.T, cases []goRouteAccuracyCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertGoRouteSet(t, goRouteRegistrations(tc.content, staticStringConstants(tc.content)), tc.want...)
		})
	}
}

// A route or prefix argument resolves an identifier through the lexical scope
// at its call: a parameter or local variable is not a static string (whatever
// a package-level constant of the same name holds), a local string constant
// is, and only a name no function scope declares reaches the package-level
// constants and never-reassigned variables.
func TestGoRouteAccuracyConstantsRespectLexicalScope(t *testing.T) {
	runGoRouteCasesWithFileConstants(t, []goRouteAccuracyCase{
		{
			name: "a parameter shadows a package constant used as a Group prefix",
			content: `package main
import "github.com/gin-gonic/gin"
const prefix = "/v1"
func reg(r *gin.Engine, prefix string) {
	g := r.Group(prefix)
	g.GET("/x", xH)
}
`,
			want: nil,
		},
		{
			name: "a parameter shadows a package constant used as the path",
			content: `package main
import "github.com/gin-gonic/gin"
const path = "/v1"
func reg(r *gin.Engine, path string) { r.GET(path, xH) }
`,
			want: nil,
		},
		{
			name: "a range variable shadows an earlier assignment of the same name",
			content: `package main
import "github.com/gin-gonic/gin"
func reg(r *gin.Engine) {
	for _, route := range dynamicRoutes() {
		r.GET(route, loopH)
	}
}
func other() {
	route := "/first"
	_ = route
}
`,
			want: nil,
		},
		{
			name:    "the reviewer's loop-variable shadow",
			content: "package main\nimport \"github.com/gin-gonic/gin\"\nfunc reg(r *gin.Engine) {\n\troute := \"\"\n\troute = \"/first\"\n\tr.GET(route, firstH)\n\tfor _, route := range dynamicRoutes() {\n\t\tr.GET(route, loopH)\n\t}\n}\n",
			want:    nil,
		},
		{
			name: "a local variable shadows a package constant",
			content: `package main
import "github.com/gin-gonic/gin"
const p = "/a"
func reg(r *gin.Engine) {
	p := dynamic()
	r.GET(p, h)
}
`,
			want: nil,
		},
		{
			name: "a closure parameter shadows a package constant",
			content: `package main
import "github.com/go-chi/chi/v5"
const sub = "/a"
func main() {
	r := chi.NewRouter()
	each(func(sub string) { r.Get(sub, h) })
	http.ListenAndServe(":1", r)
}
`,
			want: nil,
		},
		{
			name: "a package variable reassigned in a function is not static",
			content: `package main
import "github.com/gin-gonic/gin"
var p = "/a"
func init() { p = "/b" }
func reg(r *gin.Engine) { r.GET(p, h) }
`,
			want: nil,
		},
		{
			name: "package constants, a const block, concatenation and a never-written var resolve",
			content: `package main
import "github.com/gin-gonic/gin"
const PingPath = "/ping"
const (
	base  = "/api"
	users = base + "/users"
)
var health = "/health"
func reg(r *gin.Engine) {
	r.GET(PingPath, ping)
	r.GET(users, listUsers)
	r.GET(health, healthH)
	v := r.Group(base)
	v.GET(PingPath, apiPing)
}
`,
			want: []string{"/ping -> ping", "/api/users -> listUsers", "/health -> healthH", "/api/ping -> apiPing"},
		},
		{
			name: "a local string constant resolves within its scope only",
			content: `package main
import "github.com/gin-gonic/gin"
const p = "/pkg"
func reg(r *gin.Engine) {
	const p = "/local"
	r.GET(p, localH)
	{
		const p = 5
		r.GET(p, numberH)
	}
}
func other(r *gin.Engine) { r.GET(p, pkgH) }
`,
			want: []string{"/local -> localH", "/pkg -> pkgH"},
		},
	})
}

func TestGoRoutePackageStringWritesCannotInventRoutes(t *testing.T) {
	for _, write := range []string{
		`p = "/b"`, `p += "/b"`, `sink(&p)`,
		`for p = range paths {}`, `f := func() { p = "/b" }; f()`,
	} {
		t.Run(write, func(t *testing.T) {
			runGoRouteCasesWithFileConstants(t, []goRouteAccuracyCase{{
				name: "package write",
				content: `package main
import "github.com/gin-gonic/gin"
var p = "/a"
func init() { ` + write + ` }
func reg(r *gin.Engine) { r.GET(p, h); r.GET("/health", health) }
`, want: []string{"/health -> health"},
			}})
		})
	}
}
