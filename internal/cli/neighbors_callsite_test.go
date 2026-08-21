package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A section that was never queried must never render as an empty result: an
// agent reading "Callees: none" from a --direction in query concluded the graph
// had no outgoing edge, while the same edge was queryable the other way.
func TestNeighborsDoesNotReportUnqueriedDirectionAsEmpty(t *testing.T) {
	t.Parallel()
	cases := []struct {
		direction     string
		wantCallers   string
		wantCallees   string
		forbidCallers string
		forbidCallees string
	}{
		{
			direction:     "in",
			wantCallers:   "Callers:\n- Alpha",
			wantCallees:   "Callees: not queried (--direction in)",
			forbidCallees: "Callees:\n- none",
		},
		{
			direction:     "out",
			wantCallers:   "Callers: not queried (--direction out)",
			wantCallees:   "Callees:\n- Gamma",
			forbidCallers: "Callers:\n- none",
		},
		{
			direction:   "both",
			wantCallers: "Callers:\n- Alpha",
			wantCallees: "Callees:\n- Gamma",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.direction, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			write(t, repo, "calls.go", "package calls\n\nfunc Alpha() { Beta() }\nfunc Beta() { Gamma() }\nfunc Gamma() {}\n")
			var out bytes.Buffer
			err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
				"neighbors", "--repo", repo, "--symbol", "Beta",
				"--direction", testCase.direction, "--format", "text",
			})
			if err != nil {
				t.Fatal(err)
			}
			text := out.String()
			for _, want := range []string{testCase.wantCallers, testCase.wantCallees} {
				if !strings.Contains(text, want) {
					t.Fatalf("direction %s omitted %q:\n%s", testCase.direction, want, text)
				}
			}
			for _, forbidden := range []string{testCase.forbidCallers, testCase.forbidCallees} {
				if forbidden != "" && strings.Contains(text, forbidden) {
					t.Fatalf("direction %s claimed %q for a section it never queried:\n%s",
						testCase.direction, forbidden, text)
				}
			}
		})
	}
}

// The same CALLS edge must be reachable from both ends. Nothing may be visible
// inbound and absent outbound.
func TestNeighborsCallEdgeIsTraversableInBothDirections(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "calls.go", "package calls\n\nfunc Alpha() { Beta() }\nfunc Beta() { Gamma() }\nfunc Gamma() {}\n")

	outgoing := neighborsJSON(t, repo, "Beta", "out")
	incoming := neighborsJSON(t, repo, "Gamma", "in")
	if len(outgoing.Matches) != 1 || len(incoming.Matches) != 1 {
		t.Fatalf("focus resolution: out=%d in=%d", len(outgoing.Matches), len(incoming.Matches))
	}
	if !hasEndpointNamed(outgoing.Matches[0].Outgoing, "Gamma") {
		t.Fatalf("Beta -> Gamma missing outbound: %#v", outgoing.Matches[0].Outgoing)
	}
	if !hasEndpointNamed(incoming.Matches[0].Incoming, "Beta") {
		t.Fatalf("Beta -> Gamma missing inbound: %#v", incoming.Matches[0].Incoming)
	}
	if outgoing.Direction != "out" || incoming.Direction != "in" {
		t.Fatalf("direction not echoed: out=%q in=%q", outgoing.Direction, incoming.Direction)
	}
}

func hasEndpointNamed(edges []neighborEdge, name string) bool {
	for _, edge := range edges {
		if edge.Endpoint.Name == name {
			return true
		}
	}
	return false
}

func neighborsJSON(t *testing.T, repo, symbol, direction string) neighborResponse {
	t.Helper()
	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
		"neighbors", "--repo", repo, "--symbol", symbol, "--direction", direction, "--format", "json",
	})
	if err != nil {
		t.Fatal(err)
	}
	var response neighborResponse
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatalf("decode neighbors json: %v\n%s", err, out.String())
	}
	return response
}

// End to end: a caller must be located at the line it calls on, with the
// enclosing conditions quoted and its body NOT inlined.
func TestNeighborsReportsCallSiteWithGuardsAndWithoutTheCallerBody(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	var body strings.Builder
	body.WriteString("package dispatch\n\nfunc Dispatch(kind string, ok bool) {\n")
	for index := 0; index < 400; index++ {
		body.WriteString(fmt.Sprintf("\tfiller%d := %d\n\t_ = filler%d\n", index, index, index))
	}
	body.WriteString("\tif kind == \"format\" {\n\t\tif ok {\n\t\t\tTarget(kind)\n\t\t}\n\t}\n}\n\nfunc Target(kind string) string { return kind }\n")
	write(t, repo, "dispatch.go", body.String())

	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
		"neighbors", "--repo", repo, "--symbol", "Target", "--direction", "in", "--format", "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()

	// The call is ~800 lines below the definition of Dispatch.
	callLine := 3 + 800 + 3
	if !strings.Contains(text, fmt.Sprintf("dispatch.go:%d, def :3", callLine)) {
		t.Fatalf("caller not reported at its call site (want dispatch.go:%d, def :3):\n%s", callLine, text)
	}
	for _, want := range []string{
		"conditions in force at this call",
		"if kind == \"format\" {",
		"if ok {",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("call context omitted %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "filler200") > 0 {
		t.Fatalf("caller body was inlined:\n%s", text[:400])
	}
	if len(text) > 4096 {
		t.Fatalf("answer grew to %d bytes for one caller of a 2800-line function", len(text))
	}
}

// The focus reports both numbers a reader may see for one symbol, each labelled,
// so `def=` here and a `start-end` range from search are one fact, not two.
func TestNeighborsFocusLabelsDefinitionLineAndSpan(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "calls.go", "package calls\n\n// Doc for Beta.\nfunc Beta() {\n\tGamma()\n}\n\nfunc Gamma() {}\n")
	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
		"neighbors", "--repo", repo, "--symbol", "Beta", "--direction", "out", "--format", "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Focus: Beta (calls.go:4) def=4 span=4-6") {
		t.Fatalf("focus did not label def and span:\n%s", out.String())
	}
}

// `impact` upgrades the caller LOCATION but stays a bounded overview: it must not
// start quoting source, which would push real sections out of its byte budget.
func TestImpactReportsCallSiteLineWithoutQuotingSource(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	var body strings.Builder
	body.WriteString("package dispatch\n\nfunc Dispatch(kind string) {\n")
	for index := 0; index < 200; index++ {
		body.WriteString(fmt.Sprintf("\tfiller%d := %d\n\t_ = filler%d\n", index, index, index))
	}
	body.WriteString("\tTarget(kind)\n}\n\nfunc Target(kind string) string { return kind }\n")
	write(t, repo, "dispatch.go", body.String())

	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
		"impact", "--repo", repo, "--symbol", "Target", "--format", "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, fmt.Sprintf("dispatch.go:%d, def :3", 3+400+1)) {
		t.Fatalf("impact caller not reported at its call site:\n%s", text)
	}
	if strings.Contains(text, "conditions in force at this call") {
		t.Fatalf("impact quoted source it does not budget for:\n%s", text)
	}
}

// The compact (--format agent) form is byte-capped, so both fixes must survive
// there too: the call-site line, and the refusal to imply an unqueried section
// is empty.
func TestCompactNeighborsCarriesCallSiteAndDirectionScope(t *testing.T) {
	t.Parallel()
	response := neighborResponse{
		Query:     "Target",
		Direction: "in",
		Matches: []neighborFocus{{
			Symbol: neighborEndpoint{ID: "target", Name: "Target", FilePath: "dispatch.go", StartLine: 900, EndLine: 910},
			Incoming: []neighborEdge{{
				Direction: "in", Relation: "CALLS", Resolution: "exact",
				Endpoint: neighborEndpoint{ID: "caller", Name: "Dispatch", FilePath: "dispatch.go", StartLine: 3},
				CallSite: &callSite{FilePath: "dispatch.go", Line: 806},
			}},
		}},
	}
	var out bytes.Buffer
	if err := writeAgentNeighborsBounded(&out, response, 200); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if out.Len() > 200 {
		t.Fatalf("compact output used %d bytes, cap 200", out.Len())
	}
	if !strings.Contains(text, "dispatch.go:806, def :3") {
		t.Fatalf("compact form dropped the call site:\n%s", text)
	}
	if !strings.Contains(text, ">?not-queried") {
		t.Fatalf("compact form did not mark the unqueried direction:\n%s", text)
	}
}

// A call site on the caller's own definition line reports one number, not two.
func TestCallSiteLocationOmitsRedundantDefinitionLine(t *testing.T) {
	t.Parallel()
	endpoint := neighborEndpoint{Name: "Alpha", FilePath: "calls.go", StartLine: 3}
	same := formatCallSiteLocation(endpoint, &callSite{FilePath: "calls.go", Line: 3})
	if same != "Alpha (calls.go:3)" {
		t.Fatalf("same-line rendering = %q", same)
	}
	other := formatCallSiteLocation(endpoint, &callSite{FilePath: "calls.go", Line: 42})
	if other != "Alpha (calls.go:42, def :3)" {
		t.Fatalf("distinct-line rendering = %q", other)
	}
	if none := formatCallSiteLocation(endpoint, nil); none != "Alpha (calls.go:3)" {
		t.Fatalf("unresolved rendering = %q", none)
	}
}

// Relation queries over a worktree always rebuild until raw worktree equality
// can be established without invoking repository-selected Git filters. Both a
// clean repeat and a dirty edit therefore bypass the committed-tree cache.
func TestRelationQueryRebuildsWorktreeIndex(t *testing.T) {
	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = repo
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write(t, repo, "calls.go", "package calls\n\nfunc Alpha() { Beta() }\nfunc Beta() {}\n")
	runGit("init")
	runGit("config", "user.name", "Entire Graph Test")
	runGit("config", "user.email", "graph@example.com")
	runGit("add", ".")
	runGit("commit", "-m", "initial")
	cacheDir := t.TempDir()

	query := func() neighborResponse {
		t.Helper()
		var out bytes.Buffer
		err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
			"neighbors", "--repo", repo, "--symbol", "Beta", "--direction", "in",
			"--format", "json", "--cache-dir", cacheDir,
		})
		if err != nil {
			t.Fatal(err)
		}
		var response neighborResponse
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return response
	}

	if first := query(); first.IndexCacheHit {
		t.Fatal("first query on a cold cache reported a hit")
	}
	if second := query(); second.IndexCacheHit {
		t.Fatal("second query on a clean worktree reused the index")
	}

	// A dirty working tree must never be served committed-tree content, and the
	// edit must be visible.
	write(t, repo, "calls.go", "package calls\n\nfunc Alpha() { Beta() }\nfunc Beta() {}\nfunc Delta() { Beta() }\n")
	dirty := query()
	if dirty.IndexCacheHit {
		t.Fatal("dirty worktree was served a cached index")
	}
	if len(dirty.Matches) != 1 || len(dirty.Matches[0].Incoming) != 2 {
		t.Fatalf("dirty worktree edit not visible: %#v", dirty.Matches)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "search")); err == nil {
		t.Fatal("worktree query wrote a cache entry")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
