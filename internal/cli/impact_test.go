package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// impactFixtureSnapshot exercises every impact section from one synthetic
// snapshot: depth-2 callers, callees, both type-consumer directions, a data
// flow, a co-change edge on the focus file, and a same-container sibling.
func impactFixtureSnapshot() sem.ProviderSnapshot {
	return sem.ProviderSnapshot{
		Files: []sem.FileRecord{
			{ID: "repo:file:app.go", Path: "app.go"},
			{ID: "repo:file:orders.go", Path: "orders.go"},
		},
		Symbols: []sem.SymbolRecord{
			{ID: "svc", Name: "Service", QualifiedName: "Service", Kind: "struct", FilePath: "app.go", StartLine: 3},
			{ID: "orders", Name: "Orders", QualifiedName: "Service.Orders", Kind: "method", FilePath: "app.go", StartLine: 10, ContainerID: "svc"},
			{ID: "sibling", Name: "List", QualifiedName: "Service.List", Kind: "method", FilePath: "app.go", StartLine: 20, ContainerID: "svc"},
			{ID: "caller1", Name: "Handler", QualifiedName: "Handler", Kind: "function", FilePath: "web.go", StartLine: 5},
			{ID: "caller2", Name: "Route", QualifiedName: "Route", Kind: "function", FilePath: "web.go", StartLine: 15},
			{ID: "callee", Name: "SortOrders", QualifiedName: "SortOrders", Kind: "function", FilePath: "orders.go", StartLine: 8},
			{ID: "limitType", Name: "Limit", QualifiedName: "Limit", Kind: "type", FilePath: "app.go", StartLine: 1},
			{ID: "sink", Name: "Sink", QualifiedName: "Sink", Kind: "function", FilePath: "orders.go", StartLine: 30},
		},
		Relations: []sem.RelationRecord{
			{FromID: "caller1", ToID: "orders", Type: "CALLS"},
			{FromID: "caller2", ToID: "caller1", Type: "CALLS"},
			{FromID: "orders", ToID: "callee", Type: "CALLS"},
			{FromID: "orders", ToID: "limitType", Type: "PARAM_TYPE"},
			{FromID: "sink", ToID: "orders", Type: "USES_TYPE"},
			{FromID: "orders", ToID: "sink", Type: "DATA_FLOWS"},
			{FromID: "repo:file:app.go", ToID: "repo:file:orders.go", Type: "FILE_CHANGES_WITH",
				Reason: "files changed together in 3 recent commits"},
		},
	}
}

func TestImpactBuildsAllSectionsFromSnapshot(t *testing.T) {
	t.Parallel()
	response := buildImpactResponse(impactFixtureSnapshot(), impactFlags{
		Symbol: "Orders", Depth: 2, Limit: defaultImpactSectionLimit,
	})
	if response.DisambiguationRequired || response.FocusMatchesTotal != 1 {
		t.Fatalf("focus resolution = %#v", response)
	}
	if response.Focus == nil || response.Focus.ID != "orders" {
		t.Fatalf("focus = %#v", response.Focus)
	}
	if response.Container == nil || response.Container.ID != "svc" {
		t.Fatalf("container = %#v", response.Container)
	}

	if response.Callers.Total != 2 || response.Callers.Direct != 1 || response.Callers.Transitive != 1 {
		t.Fatalf("callers section = %#v", response.Callers)
	}
	if response.Callers.Entries[0].Endpoint.ID != "caller1" || response.Callers.Entries[0].Depth != 1 {
		t.Fatalf("direct caller = %#v", response.Callers.Entries[0])
	}
	if got := response.Callers.Entries[1]; got.Endpoint.ID != "caller2" || got.Depth != 2 || got.Via != "Handler" {
		t.Fatalf("transitive caller = %#v", got)
	}

	if response.Callees.Total != 1 || response.Callees.Entries[0].Endpoint.ID != "callee" {
		t.Fatalf("callees section = %#v", response.Callees)
	}

	if response.TypeConsumers.Total != 2 || response.TypeConsumers.In != 1 || response.TypeConsumers.Out != 1 {
		t.Fatalf("type consumers section = %#v", response.TypeConsumers)
	}
	if response.DataFlows.Total != 1 || response.DataFlows.Out != 1 ||
		response.DataFlows.Entries[0].Endpoint.ID != "sink" {
		t.Fatalf("data flows section = %#v", response.DataFlows)
	}

	if response.CoChanges.Total != 1 ||
		response.CoChanges.Entries[0].Endpoint.FilePath != "orders.go" ||
		!strings.Contains(response.CoChanges.Entries[0].Detail, "3 recent commits") {
		t.Fatalf("co-change section = %#v", response.CoChanges)
	}

	if response.Siblings.Total != 1 ||
		response.Siblings.Entries[0].Endpoint.ID != "sibling" ||
		response.Siblings.Entries[0].Detail != "sibling" {
		t.Fatalf("siblings section = %#v", response.Siblings)
	}
}

func TestImpactDepthOneSkipsTransitiveCallers(t *testing.T) {
	t.Parallel()
	response := buildImpactResponse(impactFixtureSnapshot(), impactFlags{
		Symbol: "Orders", Depth: 1, Limit: defaultImpactSectionLimit,
	})
	if response.Callers.Total != 1 || response.Callers.Transitive != 0 {
		t.Fatalf("depth-1 callers = %#v", response.Callers)
	}
}

func TestImpactContainerFocusListsMembers(t *testing.T) {
	t.Parallel()
	response := buildImpactResponse(impactFixtureSnapshot(), impactFlags{
		Symbol: "Service", Depth: 2, Limit: defaultImpactSectionLimit,
	})
	if response.Siblings.Total != 2 {
		t.Fatalf("container members = %#v", response.Siblings)
	}
	for _, entry := range response.Siblings.Entries {
		if entry.Detail != "member" {
			t.Fatalf("container focus entry not a member: %#v", entry)
		}
	}
}

func TestImpactAmbiguousSymbolListsDefinitionsAndFileDisambiguates(t *testing.T) {
	t.Parallel()
	snapshot := sem.ProviderSnapshot{
		Symbols: []sem.SymbolRecord{
			{ID: "b-target", Name: "Target", QualifiedName: "Target", FilePath: "b.go", StartLine: 4},
			{ID: "a-target", Name: "Target", QualifiedName: "Target", FilePath: "a.go", StartLine: 9},
			{ID: "caller", Name: "Caller", QualifiedName: "Caller", FilePath: "c.go", StartLine: 1},
		},
		Relations: []sem.RelationRecord{
			{FromID: "caller", ToID: "a-target", Type: "CALLS"},
		},
	}
	ambiguous := buildImpactResponse(snapshot, impactFlags{Symbol: "Target", Depth: 2, Limit: 15})
	if !ambiguous.DisambiguationRequired || ambiguous.FocusMatchesTotal != 2 {
		t.Fatalf("ambiguous response = %#v", ambiguous)
	}
	if len(ambiguous.Definitions) != 2 ||
		ambiguous.Definitions[0].ID != "a-target" || ambiguous.Definitions[1].ID != "b-target" {
		t.Fatalf("definition list = %#v", ambiguous.Definitions)
	}
	if ambiguous.Focus != nil || ambiguous.Callers.Total != 0 {
		t.Fatalf("ambiguous response expanded sections: %#v", ambiguous)
	}
	var text bytes.Buffer
	writeImpactText(&text, ambiguous)
	if !strings.Contains(text.String(), `Ambiguous symbol "Target" matched 2 definitions`) ||
		!strings.Contains(text.String(), "rerun with --file") ||
		!strings.Contains(text.String(), "Target (a.go:9)") {
		t.Fatalf("ambiguous text output:\n%s", text.String())
	}

	resolved := buildImpactResponse(snapshot, impactFlags{Symbol: "Target", File: "a.go", Depth: 2, Limit: 15})
	if resolved.DisambiguationRequired || resolved.Focus == nil || resolved.Focus.ID != "a-target" {
		t.Fatalf("--file did not disambiguate: %#v", resolved)
	}
	if resolved.Callers.Total != 1 || resolved.Callers.Entries[0].Endpoint.ID != "caller" {
		t.Fatalf("resolved callers = %#v", resolved.Callers)
	}
}

func TestImpactNoMatchTextSuggestsFile(t *testing.T) {
	t.Parallel()
	response := buildImpactResponse(sem.ProviderSnapshot{}, impactFlags{Symbol: "Missing", Depth: 2, Limit: 15})
	var text bytes.Buffer
	writeImpactText(&text, response)
	if !strings.Contains(text.String(), `No symbols matched "Missing"`) {
		t.Fatalf("no-match text output:\n%s", text.String())
	}
}

func TestImpactSectionCapEmitsMoreMarker(t *testing.T) {
	t.Parallel()
	snapshot := sem.ProviderSnapshot{
		Symbols: []sem.SymbolRecord{
			{ID: "focus", Name: "Focus", QualifiedName: "Focus", FilePath: "focus.go", StartLine: 1},
			{ID: "c1", Name: "CallerA", QualifiedName: "CallerA", FilePath: "a.go", StartLine: 1},
			{ID: "c2", Name: "CallerB", QualifiedName: "CallerB", FilePath: "b.go", StartLine: 1},
			{ID: "c3", Name: "CallerC", QualifiedName: "CallerC", FilePath: "c.go", StartLine: 1},
		},
		Relations: []sem.RelationRecord{
			{FromID: "c1", ToID: "focus", Type: "CALLS"},
			{FromID: "c2", ToID: "focus", Type: "CALLS"},
			{FromID: "c3", ToID: "focus", Type: "CALLS"},
		},
	}
	response := buildImpactResponse(snapshot, impactFlags{Symbol: "Focus", Depth: 1, Limit: 2})
	if response.Callers.Total != 3 || len(response.Callers.Entries) != 2 {
		t.Fatalf("capped callers = %#v", response.Callers)
	}
	var text bytes.Buffer
	writeImpactText(&text, response)
	if !strings.Contains(text.String(), "- ... +1 more") {
		t.Fatalf("cap marker missing:\n%s", text.String())
	}
}

func TestImpactBoundedOutputStaysUnderBudget(t *testing.T) {
	t.Parallel()
	symbols := []sem.SymbolRecord{
		{ID: "focus", Name: "Focus", QualifiedName: "Focus", FilePath: "focus.go", StartLine: 1},
	}
	var relations []sem.RelationRecord
	for index := 0; index < 40; index++ {
		id := string(rune('a'+index%26)) + strings.Repeat("x", index)
		symbols = append(symbols, sem.SymbolRecord{
			ID: id, Name: "caller_" + id, QualifiedName: "pkg.caller_" + id,
			FilePath: "callers/" + id + ".go", StartLine: index + 1,
		})
		relations = append(relations, sem.RelationRecord{FromID: id, ToID: "focus", Type: "CALLS"})
	}
	response := buildImpactResponse(
		sem.ProviderSnapshot{Symbols: symbols, Relations: relations},
		impactFlags{Symbol: "Focus", Depth: 1, Limit: defaultImpactSectionLimit},
	)
	const budget = 512
	var out bytes.Buffer
	if err := writeImpactBounded(&out, response, budget); err != nil {
		t.Fatal(err)
	}
	if out.Len() > budget {
		t.Fatalf("bounded output %d bytes exceeds %d budget:\n%s", out.Len(), budget, out.String())
	}
	if !strings.Contains(out.String(), "more") && !strings.Contains(out.String(), "!output-truncated") {
		t.Fatalf("bounded output has no truncation signal:\n%s", out.String())
	}
}

func TestImpactEndToEndOnFixtureRepo(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Tests")
	git(t, repo, "config", "user.email", "tests@entire.local")
	write(t, repo, "app.go", `package app

type Config struct{ Limit int }

func Orders(cfg Config) []string {
	return sortOrders(nil, cfg.Limit)
}

func sortOrders(items []string, limit int) []string {
	return items
}

func handler() {
	Orders(Config{})
}

func route() {
	handler()
}
`)
	write(t, repo, "other.go", "package app\n\nfunc other() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "fixture")
	write(t, repo, "app.go", `package app

// touched
type Config struct{ Limit int }

func Orders(cfg Config) []string {
	return sortOrders(nil, cfg.Limit)
}

func sortOrders(items []string, limit int) []string {
	return items
}

func handler() {
	Orders(Config{})
}

func route() {
	handler()
}
`)
	write(t, repo, "other.go", "package app\n\n// touched\nfunc other() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "co-change")

	cacheDir := t.TempDir()
	run := func(extra ...string) string {
		t.Helper()
		var out bytes.Buffer
		args := append([]string{
			"impact", "--repo", repo, "--symbol", "Orders", "--head", "--cache-dir", cacheDir,
		}, extra...)
		if err := Run(t.Context(), Options{
			Version: "0.1.0",
			Env:     EntireEnv{RepoRoot: repo},
			Stdout:  &out,
		}, args); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	text := run("--format", "text")
	for _, want := range []string{
		"Impact: Orders (app.go:",
		"Blast radius:",
		"Callers (",
		"handler (app.go:",
		"route (app.go:",
		"via handler",
		"Callees (",
		"sortOrders (app.go:",
		"Type consumers (",
		"Config (app.go:",
		"Co-change coupling (",
		"other.go [files changed together in 2 recent commits]",
		"Same-container siblings (",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("impact text missing %q:\n%s", want, text)
		}
	}

	var response impactResponse
	if err := json.Unmarshal([]byte(run("--format", "json")), &response); err != nil {
		t.Fatal(err)
	}
	if response.FormatVersion != 1 || response.Focus == nil || response.Focus.Name != "Orders" {
		t.Fatalf("impact JSON focus = %#v", response)
	}
	if response.Callers.Direct < 1 || response.Callers.Transitive < 1 {
		t.Fatalf("impact JSON callers = %#v", response.Callers)
	}
	if response.Callees.Total < 1 || response.TypeConsumers.Total < 1 || response.CoChanges.Total < 1 {
		t.Fatalf("impact JSON sections = callees %#v type %#v cochange %#v",
			response.Callees, response.TypeConsumers, response.CoChanges)
	}
	if !response.IndexCacheHit {
		t.Fatalf("second impact run missed the index cache: %#v", response.Stats)
	}
}
