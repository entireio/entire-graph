package sem

import "testing"

// A field, method or composite-literal key that merely shares a name with a
// root-param function does not use that function as a value: the selector's
// Sel and a struct field name never refer to the package-level function. Only
// a bare reference does, which still taints the parameter.
func TestGoRouteParamValueUseIgnoresSelectorsAndFieldNames(t *testing.T) {
	base := `package routes

import "net/http"

func register(mux *http.ServeMux) {
	mux.HandleFunc("/x", xHandler)
}

func main() {
	mux := http.NewServeMux()
	register(mux)
	http.ListenAndServe(":0", mux)
}
`
	for name, extra := range map[string]string{
		"baseline":           ``,
		"selector field":     "\ntype config struct{ register bool }\n\nfunc use(c config) bool { return c.register }\n",
		"method value":       "\ntype server struct{}\n\nfunc (server) register() {}\n\nfunc use(s server) func() { return s.register }\n",
		"composite key":      "\ntype config struct{ register bool }\n\nvar c = config{register: true}\n",
		"selector of import": "\nfunc use() { _ = other.register }\n",
	} {
		t.Run(name, func(t *testing.T) {
			assertGoRouteSet(t, goRouteRegistrationsWithin(t, base+extra), "/x -> xHandler")
		})
	}
	// A bare function value still hides the callers' arguments.
	assertGoRouteSet(t, goRouteRegistrationsWithin(t, base+"\nvar hook = register\n"))
	// So does a method value of a root-param method, which a selector names.
	method := `package routes

import "net/http"

type server struct{}

func (s server) register(mux *http.ServeMux) {
	mux.HandleFunc("/x", xHandler)
}

func main() {
	mux := http.NewServeMux()
	var s server
	s.register(mux)
	http.ListenAndServe(":0", mux)
}
`
	assertGoRouteSet(t, goRouteRegistrationsWithin(t, method), "/x -> xHandler")
	assertGoRouteSet(t, goRouteRegistrationsWithin(t, method+"\nfunc hook(s server) func(*http.ServeMux) { return s.register }\n"))
}
