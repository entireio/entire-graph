package sem

import "testing"

// This is a pure parsed-source consumer fixture. It exercises the route
// resolver's masks and the generic literal fallback without a provider,
// repository, index, framework runtime, network, or model.
func TestReview312ConcreteHTTPClientTypeDoesNotMaskUnknownInterfaceFallback(t *testing.T) {
	source := `package routes
import "net/http"

type opaque interface { Get(string) (*http.Response, error) }
func lookupOpaque() opaque { return nil }

var packageDynamic opaque = http.DefaultClient
func init() { packageDynamic = lookupOpaque() }
func packageFetch() { packageDynamic.Get("/package-interface-unknown") }

type store struct{}
func (store) Get(string) any { return nil }
func lookupStore() store { return store{} }

func fetch(client *http.Client) {
	typedAlias := client
	typedAlias.Get("/typed-client-alias")

	var dynamic opaque = client
	dynamic.Get("/known-client-in-interface")
	dynamic = lookupOpaque()
	dynamic.Get("/unknown-interface")

	interfaceAlias := dynamic
	interfaceAlias.Get("/unknown-interface-alias")
	{
		dynamic := client
		dynamic.Get("/concrete-client-shadow")
	}
	dynamic.Get("/unknown-interface-after-shadow")
}

func other() {
	kv := store{}
	kv = lookupStore()
	kv.Get("/kv-unknown")
}

func makeClient() *http.Client { return nil }
func concreteClientAfterCapturedWrite() {
	var concrete *http.Client = makeClient()
	rewrite := func() { concrete = makeClient() }
	go rewrite()
	concrete.Get("/explicit-concrete-after-captured-write")
}
`
	detail := goRouteRegistrationsDetailed(source, nil)
	if len(detail.regs) != 0 {
		t.Fatalf("one-argument calls became registrations: %#v", detail.regs)
	}
	fallback := routeLiterals(goRouteMaskContent(source, detail.masks))
	want := map[string]bool{
		"/unknown-interface":              true,
		"/unknown-interface-alias":        true,
		"/unknown-interface-after-shadow": true,
		"/package-interface-unknown":      true,
		"/kv-unknown":                     true,
	}
	if len(fallback) != len(want) {
		t.Fatalf("generic route fallback = %#v, want only unknown interface/local controls", fallback)
	}
	for _, route := range fallback {
		if !want[route] {
			t.Fatalf("generic route fallback = %#v, unexpected %q", fallback, route)
		}
		delete(want, route)
	}
	if len(want) != 0 {
		t.Fatalf("generic route fallback = %#v, missing %#v", fallback, want)
	}
}
