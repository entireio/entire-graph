package sem

import (
	"reflect"
	"strconv"
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
			if registrations := goHTTPRouteRegistrationsForTest(test.content); len(registrations) == 0 {
				t.Fatalf("fixture is not accepted by the route scanner: %q", test.content)
			}
			if !goHTTPRouteCandidate(test.content) {
				t.Fatalf("prefilter rejected accepted registration: %q", test.content)
			}
		})
	}
}

func goHTTPRouteRegistrationsForTest(content string) []goHTTPRouteRegistration {
	return goHTTPRouteRegistrations(content, func() map[string]string {
		return staticStringConstants(content)
	})
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

func TestGoHTTPRouteRegistrationsPreserveFormsAndOrder(t *testing.T) {
	content := `package server

const api = "/api"
const users = "/users"

func register() {
	http.HandleFunc("/health", health)
	http.Handle("/wrapped", http.HandlerFunc(wrapped))
	v1 := router.Group(api)
	v1.GET(users, listUsers)
	router.Group("/admin").POST("/jobs", admin.createJob)
	http.HandleFunc("/later", later)
}
`
	got := goHTTPRouteRegistrationsForTest(content)
	want := []goHTTPRouteRegistration{
		{Route: "/health", Handler: "health", EvidenceKind: "go_http_handle_func", Detail: "/health -> health"},
		{Route: "/later", Handler: "later", EvidenceKind: "go_http_handle_func", Detail: "/later -> later"},
		{Route: "/wrapped", Handler: "wrapped", EvidenceKind: "go_http_handler_func", Detail: "/wrapped -> wrapped"},
		{Route: "/api/users", Handler: "listUsers", EvidenceKind: "go_router_method", Detail: "/api/users -> listUsers"},
		{Route: "/admin/jobs", Handler: "admin.createJob", EvidenceKind: "go_router_group_method", Detail: "/admin/jobs -> admin.createJob"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registrations changed\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestGoHTTPRouteRegistrationsPreserveWhitespaceAndLaterMatches(t *testing.T) {
	content := "value.GetPath()\n" +
		"http.HandleFunc \n (\n\"/one\", first\n)\n" +
		"api := router.Group \n (\n\"/api\"\n)\n" +
		"api.Get\t(\"/two\", second)\n"
	got := goHTTPRouteRegistrationsForTest(content)
	want := []goHTTPRouteRegistration{
		{Route: "/one", Handler: "first", EvidenceKind: "go_http_handle_func", Detail: "/one -> first"},
		{Route: "/api/two", Handler: "second", EvidenceKind: "go_router_method", Detail: "/api/two -> second"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registrations changed\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestGoHTTPRouteRegistrationsResolveConstantsLazily(t *testing.T) {
	calls := 0
	literal := goHTTPRouteRegistrations(`http.HandleFunc("/literal", handler)`, func() map[string]string {
		calls++
		return nil
	})
	if len(literal) != 1 || calls != 0 {
		t.Fatalf("literal registrations=%#v constant lookups=%d, want one registration and no lookup", literal, calls)
	}

	content := "const route = \"/constant\"\nhttp.HandleFunc(route, handler)"
	constant := goHTTPRouteRegistrations(content, func() map[string]string {
		calls++
		return staticStringConstants(content)
	})
	if len(constant) != 1 || constant[0].Route != "/constant" || calls != 1 {
		t.Fatalf("constant registrations=%#v total lookups=%d, want one resolved registration and one lookup", constant, calls)
	}
}

func TestGoHTTPRouteCandidateRegionsBoundLargeFilesAndRejectFalsePrefixes(t *testing.T) {
	content := "package generated\nconst payload = \"" + strings.Repeat("x", 1<<20) + "\"\n" +
		"value.GetPath()\nvalue.GroupVersion()\n" +
		"http.HandleFunc(\"/later\", later)\n"
	regions := goHTTPRouteCandidateRegions(content)
	if len(regions) != 1 {
		t.Fatalf("candidate regions=%d, want 1", len(regions))
	}
	if len(regions[0]) > 128 {
		t.Fatalf("candidate region retained %d bytes from large unrelated source", len(regions[0]))
	}
	got := goHTTPRouteRegistrationsForTest(content)
	if len(got) != 1 || got[0].Route != "/later" {
		t.Fatalf("registrations=%#v, want only later valid match", got)
	}
}

func TestGoHTTPRouteRegistrationsMatchLegacyWholeFileScanner(t *testing.T) {
	fixtures := []string{
		"http.HandleFunc\n(\n\"/a\"\n,\nhandler\n)",
		"http.Handle\n(\n\"/a\",\nhttp.HandlerFunc\n(\nhandler\n)\n)",
		"api\n:=\nrouter.Group\n(\n\"/api\"\n)\napi.GET\n(\n\"/a\", handler\n)",
		"api =\nrouter.Group(\"/api\"); api.POST(\"/a\", handler); api.GET(\"/b\", other)",
		"router.Group(\"/api\").PATCH\n(\n\"/a\", handlers.patch\n)",
		"value.GetPath(); value.GroupVersion(); router.Get(\"/later\", handler)",
		"// http.HandleFunc(\"/comment\", handler)\nhttp.HandleFunc(\"/real\", handler)",
		"const raw = `router.GET(\"/raw\", handler)`\nhttp.HandleFunc(\"/real\", handler)",
		"const route = \"/constant\"\nconst prefix = \"/api\"\napi := router.Group(prefix)\napi.Options(route, handler)",
		"func register() { api :=\nrouter.Group(\"/api\")\napi.GET(\"/a\", handler)\n}",
		"if enabled { api\n:=\nrouter.Group(\"/api\")\napi.GET(\"/a\", handler)\n}",
	}
	for i, content := range fixtures {
		constants := staticStringConstants(content)
		got := goHTTPRouteRegistrations(content, func() map[string]string { return constants })
		want := legacyGoHTTPRouteRegistrations(content, constants)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("fixture %d differs from legacy scanner\ngot:  %#v\nwant: %#v\nsource:\n%s", i, got, want, content)
		}
	}
}

func TestGoHTTPRouteRegistrationsGrammarBoundaryDifferential(t *testing.T) {
	prefixes := []string{"", "func register() { ", "if enabled { "}
	spaces := []string{"", " ", "\t", "\n", "\n\n"}
	for _, prefix := range prefixes {
		for _, beforeAssign := range spaces {
			for _, afterAssign := range spaces {
				content := prefix + "api" + beforeAssign + ":=" + afterAssign + "router.Group \n (\"/api\")\n" +
					"http.HandleFunc(\"/direct\", direct); api.GET(\"/grouped\", grouped)\n" +
					"const quoted = `value.GetPath(); router.GET(\"/quoted\", ignored)`\n" +
					"// router.POST(\"/comment\", commented)\n"
				constants := staticStringConstants(content)
				got := goHTTPRouteRegistrations(content, func() map[string]string { return constants })
				want := legacyGoHTTPRouteRegistrations(content, constants)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("prefix=%q beforeAssign=%q afterAssign=%q differs\ngot:  %#v\nwant: %#v", prefix, beforeAssign, afterAssign, got, want)
				}
			}
		}
	}
}

func TestGoHTTPRouteCandidateRegionsCarryContinuationAcrossBlankLines(t *testing.T) {
	content := "http.HandleFunc" + strings.Repeat("\n", 4096) + "(\"/late\", handler)\n"
	regions := goHTTPRouteCandidateRegions(content)
	if len(regions) != 1 || regions[0] != content {
		t.Fatalf("blank-line continuation produced %d regions", len(regions))
	}
	constants := staticStringConstants(content)
	got := goHTTPRouteRegistrations(content, func() map[string]string { return constants })
	want := legacyGoHTTPRouteRegistrations(content, constants)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("long blank-line continuation differs\ngot:  %#v\nwant: %#v", got, want)
	}
}

// legacyGoHTTPRouteRegistrations is the former whole-file matcher retained as
// a differential oracle for the bounded scanner.
func legacyGoHTTPRouteRegistrations(content string, constants map[string]string) []goHTTPRouteRegistration {
	groupPrefixes := map[string]string{}
	groupMatches := goHTTPRouteRegistrationsGroupRe.FindAllStringSubmatch(content, -1)
	for changed := true; changed; {
		changed = false
		for _, match := range groupMatches {
			prefix, ok := staticRouteExpressionValue(match[3], constants)
			if !ok {
				continue
			}
			if parent := groupPrefixes[match[2]]; parent != "" {
				prefix = joinRoutePaths(parent, prefix)
			}
			if groupPrefixes[match[1]] != prefix {
				groupPrefixes[match[1]] = prefix
				changed = true
			}
		}
	}
	var registrations []goHTTPRouteRegistration
	add := func(routeExpr, handler, evidence string) {
		route, ok := staticRouteExpressionValue(routeExpr, constants)
		if ok && handler != "" {
			registrations = append(registrations, goHTTPRouteRegistration{Route: route, Handler: handler, EvidenceKind: evidence, Detail: route + " -> " + handler})
		}
	}
	for _, match := range goHTTPHandleFuncRe.FindAllStringSubmatch(content, -1) {
		add(match[1], match[2], "go_http_handle_func")
	}
	for _, match := range goHTTPHandleFuncWrapperRe.FindAllStringSubmatch(content, -1) {
		add(match[1], match[2], "go_http_handler_func")
	}
	for _, match := range goHTTPRouterMethodRe.FindAllStringSubmatch(content, -1) {
		routeExpr := match[2]
		if prefix := groupPrefixes[match[1]]; prefix != "" {
			if route, ok := staticRouteExpressionValue(routeExpr, constants); ok {
				routeExpr = strconv.Quote(joinRoutePaths(prefix, route))
			}
		}
		add(routeExpr, match[3], "go_router_method")
	}
	for _, match := range goHTTPChainedGroupMethodRe.FindAllStringSubmatch(content, -1) {
		prefix, ok := staticRouteExpressionValue(match[2], constants)
		if !ok {
			continue
		}
		if parent := groupPrefixes[match[1]]; parent != "" {
			prefix = joinRoutePaths(parent, prefix)
		}
		route, ok := staticRouteExpressionValue(match[3], constants)
		if ok {
			add(strconv.Quote(joinRoutePaths(prefix, route)), match[4], "go_router_group_method")
		}
	}
	return registrations
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
