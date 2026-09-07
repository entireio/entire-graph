package sem

import (
	"reflect"
	"strings"
	"testing"
)

func TestGoHTTPRouteCandidateCoversRegistrationForms(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"unqualified HandleFunc", `HandleFunc("/x", handler)`},
		{"qualified HandleFunc with newline", "http.HandleFunc \n (\n\"/x\", handler\n)"},
		{"unqualified Handle wrapper", `Handle("/x", HandlerFunc(handler))`},
		{"qualified Handle wrapper with newline", "mux.Handle \n (\n\"/x\", http.HandlerFunc \n (handler)\n)"},
		{"HandleFunc with tab", "http.HandleFunc\t(\"/x\", handler)"},
		{"Handle with CRLF", "mux.Handle\r\n(\"/x\", http.HandlerFunc(handler))"},
		{"HandleFunc with form feed", "http.HandleFunc\f(\"/x\", handler)"},
	}
	for _, method := range []string{
		"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS",
		"Get", "Post", "Put", "Patch", "Delete", "Head", "Options",
	} {
		tests = append(tests,
			struct {
				name    string
				content string
			}{method + " method", "router." + method + " \n (\n\"/x\", handler\n)"},
			struct {
				name    string
				content string
			}{method + " chained group", "router.Group \n (\n\"/api\"\n)." + method + " \n (\n\"/x\", handler\n)"},
		)
	}
	tests = append(tests, struct {
		name    string
		content string
	}{"assigned group", "api := router.Group \n (\n\"/api\"\n)\napi.GET(\"/x\", handler)"})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if registrations := goHTTPRouteRegistrations(test.content, staticStringConstants(test.content)); len(registrations) == 0 {
				t.Fatalf("fixture is not accepted by the route scanner: %q", test.content)
			}
			if !goHTTPRouteCandidate(test.content) {
				t.Fatalf("prefilter rejected accepted registration: %q", test.content)
			}
		})
	}
}

func TestGoHTTPRouteCandidateRejectsIdentifierPrefixes(t *testing.T) {
	for _, content := range []string{
		"package quiet\n\nconst message = \"nothing to register\"\n",
		"value.GetPath()",
		"value.GroupVersion()",
		"response.Header()",
		"value.Patches = nil",
		"config.OptionsField = value",
		"record.Handler = handler",
	} {
		if goHTTPRouteCandidate(content) {
			t.Fatalf("identifier prefix was classified as a route call: %q", content)
		}
	}
}

func TestGoHTTPRouteCandidateFindsLaterValidOccurrence(t *testing.T) {
	content := "value.GetPath()\nrouter.Get\t(\"/x\", handler)"
	if !goHTTPRouteCandidate(content) {
		t.Fatal("rejected prefix hid a later valid route token")
	}
}

func TestGoHTTPRouteRelationsPreservesCompleteRecordsAndSkipsNonCandidates(t *testing.T) {
	const routePath = "server.go"
	const quietPath = "generated.go"
	routeContent := `package server

const prefix = "/api"
const route = prefix + "/health"

func register() {
	http.HandleFunc(route, health)
}

func health() {}
`
	quietContent := "package generated\n\nconst payload = \"" + strings.Repeat("x", 1<<20) + "\"\n"
	handler := SymbolRecord{
		RecordType: "symbol", ID: "handler-id", StableIDVersion: "compound-v1",
		Kind: "function", Name: "health", QualifiedName: "health", FilePath: routePath,
		StartLine: 9, EndLine: 9, Language: "Go",
	}
	contents := map[string]string{routePath: routeContent, quietPath: quietContent}
	constants := newFileStringConstants()
	got := goHTTPRouteRelations(
		[]FileRecord{{Path: quietPath}, {Path: routePath}},
		map[string][]SymbolRecord{routePath: {handler}},
		func(path string) (string, bool) { content, ok := contents[path]; return content, ok },
		constants,
	)
	want := []expressRouteRelation{{
		Route:   "/api/health",
		Handler: handler,
		Relation: RelationRecord{
			RecordType: "relation", FromID: handler.ID, ToID: externalID("route", "/api/health"),
			Type: "HANDLES_ROUTE", Confidence: 0.86,
			Reason:        "Go net/http route registration resolved to local handler",
			RelationScope: "external", Resolution: "exact", TargetKind: "route",
			Evidence: []Evidence{{
				Kind: "go_http_handle_func", FilePath: routePath, StartLine: 9, EndLine: 9,
				Detail: "/api/health -> health",
			}},
			WarningCodes: []string{},
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("route relations changed\ngot:  %#v\nwant: %#v", got, want)
	}
	constants.mu.Lock()
	defer constants.mu.Unlock()
	if _, ok := constants.by[quietPath]; ok {
		t.Fatalf("non-candidate %q triggered static constant extraction", quietPath)
	}
	if _, ok := constants.by[routePath]; !ok {
		t.Fatalf("candidate %q did not trigger static constant extraction", routePath)
	}
}
