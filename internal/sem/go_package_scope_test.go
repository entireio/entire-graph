package sem

import (
	"testing"
)

// writeGoSiblingDiscoveryRepo lays out the shape that made receiver-method
// resolution pick the wrong package in prometheus/prometheus: two sibling
// packages under one module, each declaring `Discovery` with a `refresh`
// method and a `NewDiscovery` constructor, plus a test in one of them that
// calls `d.refresh(ctx)` on the constructor's result.
func writeGoSiblingDiscoveryRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module github.com/example/mod\n\ngo 1.21\n")
	pkg := func(name string) string {
		return `package ` + name + `

type SDConfig struct {
	Port int
}

type Discovery struct {
	port int
}

func NewDiscovery(conf *SDConfig) (*Discovery, error) {
	port := conf.Port
	return &Discovery{port: port}, nil
}

func (d *Discovery) refresh(ctx string) (int, error) {
	return d.port, nil
}
`
	}
	// azure sorts before puppetdb, so a first-match resolver lands here.
	writeFile(t, repo, "discovery/azure/azure.go", pkg("azure"))
	writeFile(t, repo, "discovery/puppetdb/puppetdb.go", pkg("puppetdb"))
	writeFile(t, repo, "discovery/puppetdb/puppetdb_test.go", `package puppetdb

import "testing"

func TestPuppetDBRefresh(t *testing.T) {
	cfg := SDConfig{Port: 80}
	d, err := NewDiscovery(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := "ctx"
	tgs, err := d.refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = tgs
}
`)
	return repo
}

// relationTargetFiles returns the target file paths of every relation of the
// given type whose from-symbol short name matches.
func relationTargetFiles(snapshot ProviderSnapshot, relType, from, targetName string) []string {
	byID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		byID[s.ID] = s
	}
	var out []string
	for _, r := range snapshot.Relations {
		if r.Type != relType || lastSegment(r.FromID) != from {
			continue
		}
		target, ok := byID[r.ToID]
		if !ok || (targetName != "" && target.Name != targetName) {
			continue
		}
		out = append(out, target.FilePath)
	}
	return out
}

// A Go receiver-method call must bind inside the call site's own package. Before
// package scoping, `d.refresh(ctx)` in discovery/puppetdb/puppetdb_test.go bound
// to discovery/azure's identically named Discovery.refresh, so no hop from the
// test reached the code under test.
func TestGoReceiverMethodBindsToSamePackageNotSibling(t *testing.T) {
	t.Parallel()
	repo := writeGoSiblingDiscoveryRepo(t)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	targets := relationTargetFiles(snapshot, "CALLS", "TestPuppetDBRefresh", "refresh")
	if len(targets) == 0 {
		t.Fatalf("no CALLS edge from TestPuppetDBRefresh to a refresh method")
	}
	for _, file := range targets {
		if file != "discovery/puppetdb/puppetdb.go" {
			t.Fatalf("CALLS TestPuppetDBRefresh -> refresh resolved to %s, want discovery/puppetdb/puppetdb.go", file)
		}
	}
}

// The same scoping governs field access: `conf.Port` inside puppetdb's
// NewDiscovery must read puppetdb's own SDConfig.Port, not the azure sibling's.
func TestGoFieldAccessBindsToSamePackageNotSibling(t *testing.T) {
	t.Parallel()
	repo := writeGoSiblingDiscoveryRepo(t)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	targets := relationTargetFiles(snapshot, "READS_FIELD", "NewDiscovery", "Port")
	if len(targets) == 0 {
		t.Fatalf("no READS_FIELD edge from NewDiscovery to SDConfig.Port")
	}
	for _, file := range targets {
		if file != "discovery/puppetdb/puppetdb.go" && file != "discovery/azure/azure.go" {
			t.Fatalf("unexpected READS_FIELD target file %s", file)
		}
	}
	// Both packages declare NewDiscovery, so each edge must stay inside its own
	// package rather than collapsing onto whichever file sorted first.
	if got := relationTargetFiles(snapshot, "READS_FIELD", "NewDiscovery", "Port"); len(got) != 2 {
		t.Fatalf("READS_FIELD NewDiscovery -> SDConfig.Port = %v, want one edge per package", got)
	}
	seen := map[string]bool{}
	for _, file := range targets {
		if seen[file] {
			t.Fatalf("two NewDiscovery declarations both read %s: %v", file, targets)
		}
		seen[file] = true
	}
}

// pathProximityRank orders candidates same-file < same-directory < nearer
// shared-prefix < farther, and never reorders equal-proximity candidates.
func TestPathProximityRankOrdersByPackageDistance(t *testing.T) {
	t.Parallel()
	site := "discovery/puppetdb/puppetdb_test.go"
	sameFile := pathProximityRank(site, site)
	samePkg := pathProximityRank("discovery/puppetdb/puppetdb.go", site)
	sibling := pathProximityRank("discovery/azure/azure.go", site)
	distant := pathProximityRank("web/api/v1/api.go", site)
	if !(sameFile < samePkg && samePkg < sibling && sibling < distant) {
		t.Fatalf("ranks not ordered: sameFile=%d samePkg=%d sibling=%d distant=%d", sameFile, samePkg, sibling, distant)
	}
}

// nearestToSite is stable: two candidates at the same proximity keep input order,
// so ambiguity falls back to the previous first-match behaviour rather than to an
// arbitrary map-iteration winner.
func TestNearestToSiteIsStableOnTies(t *testing.T) {
	t.Parallel()
	candidates := []SymbolRecord{
		{ID: "a", FilePath: "pkg/one/a.go"},
		{ID: "b", FilePath: "pkg/two/b.go"},
	}
	got, ok := nearestToSite(candidates, "pkg/three/c.go")
	if !ok || got.ID != "a" {
		t.Fatalf("nearestToSite tie = %q (ok=%v), want first input candidate %q", got.ID, ok, "a")
	}
	if _, ok := nearestToSite(nil, "pkg/three/c.go"); ok {
		t.Fatalf("nearestToSite on empty candidates reported a match")
	}
}
