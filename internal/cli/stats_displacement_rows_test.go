package cli

import (
	"slices"
	"testing"
)

// Fixtures are rendered by this repository's own binary against a synthetic two-file Go repo
// (internal/a/a.go defines Foo, internal/a/b.go's Bar calls it) plus one minified JS file that fails
// to parse, so every degraded-coverage banner below names web/app.min.js. No session content.

// zeroResultAgentQuery is the exact shape of a query that found nothing under degraded coverage.
const zeroResultAgentQuery = "Index: cache-hit (578ms) | Query: 0ms | Preselect: 216ms | Total: 794ms\n" +
	"Coverage: degraded (10 languages/1427 files; 0 warnings; 1 partial failure)\n" +
	"- partial E_MINIFIED: .entire/runners/trail-security.json\n" +
	"No search results.\n"

func TestGraphOutputPathsReadsOnlyResultRows(t *testing.T) {
	t.Parallel()
	const (
		a = "internal/a/a.go"
		b = "internal/a/b.go"
	)
	for _, tc := range []struct {
		name   string
		output string
		want   []string
	}{
		{"query-agent-zero-results-degraded", zeroResultAgentQuery, nil},
		{"query-agent-zero-results-with-verify", "Index: cache-miss (154ms) | Query: 73ms | Total: 318ms\n" +
			"Coverage: degraded (2 languages/3 files; 1 warning; 2 partial failures)\n" +
			"- warning W_WORKTREE_SNAPSHOT\n- partial E_MINIFIED: web/app.min.js\n" +
			"- partial E_PARSE_DEPTH_EXCEEDED: web/app.min.js\n" +
			"VERIFY: none derivable (no build manifest found) - syntax-check the file you edited and stop.\n" +
			"  targets the file you edited (from no manifest, no test file, no single-file checker for this language) tier=none\n" +
			"No search results.\n", nil},
		// A located token in a diagnostic or VERIFY line is still not a result row.
		{"query-agent-located-tokens-in-diagnostics-and-verify", "Coverage: degraded (1 language/2 files; 1 warning; 0 partial failures)\n" +
			"- warning W_LARGE: internal/z/z.go:3\n" +
			"VERIFY: go test ./internal/a\n  targets internal/a/a_test.go:12 tier=narrow\n" +
			"LOW CONFIDENCE: top score 3.1 (weak, below 12) and only internal/z/y.go:9 matched.\n" +
			"1. internal/a/a.go:6 Foo\n", []string{a}},
		// Empty-response trailing text is not a new result row.
		{"no-search-results-sentinel", "No search results.\nTests:\n  internal/a/a_test.go:5\n", nil},
		{"chained-hit-then-empty", "1. internal/a/a.go:6 Foo\n" + zeroResultAgentQuery, []string{a}},
		{"chained-empty-then-hit", zeroResultAgentQuery + "1. internal/a/a.go:6 Foo\n", []string{a}},
		{"chained-empty-queries", zeroResultAgentQuery + zeroResultAgentQuery, nil},
		{"clipped-match-bodies", `{"match_bodies":[{"file_path":"internal/a/a.go","source":"`, []string{a}},
		{"clipped-definitions", `{"definitions":[{"file_path":"internal/a/a.go","name":"`, []string{a}},
		// A path in prose with no location is mentioned, not located.
		{"prose-path-without-location", "Callees: not queried; see docs/graph.md for details\n", nil},
		{"query-text-zero-results-is-verify-only", "VERIFY: none derivable (no build manifest found) - syntax-check the file you edited and stop.\n" +
			"  targets the file you edited (from no manifest, no test file, no single-file checker for this language) tier=none\n", nil},
		{"query-source-quoted-location-is-not-a-row", "1. internal/a/a.go:6 Foo\nfunc Foo() {\nlog.Printf(\"see docs/readme.md:12\")\n}\n", []string{a}},
		{"source-location-without-row-is-ineligible", "log.Printf(\"see docs/readme.md:12\")\n", nil},
		{"source-label-is-not-focus-record", "1. internal/a/a.go:6 Foo\nFocus: log.Printf(\"docs/readme.md:12\")\n", []string{a}},
		{"query-source-json-is-not-another-payload", "1. internal/a/a.go:6 Foo\n{\"results\":[{\"file_path\":\"docs/readme.md\"}]}\n", []string{a}},
		{"query-agent-hits", "Index: cache-miss (143ms) | Query: 69ms | Preselect: 81ms | Total: 295ms\n" +
			"Coverage: degraded (2 languages/3 files; 1 warning; 2 partial failures)\n" +
			"- warning W_WORKTREE_SNAPSHOT\n- partial E_MINIFIED: web/app.min.js\n" +
			"VERIFY: go test ./internal/a\n  targets package ./internal/a (from go.mod module root) tier=narrow\n" +
			"1. internal/a/a.go:6-11 Foo [complete] s=11.5 [focus:6]\nfunc Foo(x int) error {\n\treturn nil\n}\n\n" +
			"2. internal/a/b.go:4-6 Bar [complete] s=5.0 [focus:4]\nfunc Bar() error {\n\treturn Foo(1)\n}\n", []string{a, b}},
		{"query-agent-compact-forms", "!D W1 F2 L2/3\n!LOW s=3.1\nC:degraded Go 1/3 W0 F2-other\n" +
			"1. internal/a/a.go:6 Foo\n", []string{a}},
		{"query-text-hits", "1. internal/a/a.go:6-11 score=11.4555 symbol=Foo kind=function focus=6 signals=body\n" +
			"func Foo(x int) error {\n}\n\n2. internal/a/b.go:4-6 score=4.9675 symbol=Bar kind=function focus=4\n", []string{a, b}},
		{"neighbors-text-no-match-degraded", "Index: cache-miss (160ms) | Query: 0ms | Total: 161ms\n" +
			"Completeness: unsafe for this query (2 this query files failed to parse; 3 total diagnostics in this snapshot)\n" +
			"- partial E_MINIFIED: web/app.min.js\n- partial E_PARSE_DEPTH_EXCEEDED: web/app.min.js\n" +
			"No symbols matched \"Nope\". Run `entire graph query --query \"Nope\"` to find the name, or `entire graph symbols --repo .` for the full definition inventory.\n", nil},
		{"neighbors-text-hits", "Index: cache-miss (165ms) | Query: 0ms | Total: 165ms\n" +
			"Completeness: unsafe for this query (1 this query file failed to parse; 2 total diagnostics in this snapshot)\n" +
			"- partial E_MINIFIED: web/app.min.js\n" +
			"Focus: Foo (internal/a/a.go:6) def=6 span=6-11\nCallers:\n- Bar (internal/a/b.go:5, def :4) [package]\n" +
			"  internal/a/b.go:4-6\n    4 | func Bar() error {\n    5 | \treturn Foo(\"web/app.min.js:1\")\n    6 | }\n" +
			"Callees: not queried (--direction in); rerun with --direction both to include them\n", []string{a, b}},
		{"impact-text-hits-with-co-change", "Index: cache-miss (158ms) | Query: 0ms | Total: 158ms\n" +
			"Completeness: no parse failures in Go (2 files parsed); 2 elsewhere (JavaScript 2) cannot affect this answer — see --format json.\n" +
			"Impact: Foo (internal/a/a.go:6) def=6 span=6-11 [function]\n" +
			"Blast radius: 1 caller (1 direct, 0 transitive), 1 callee, 0 type consumers, 1 data flow, 1 co-change file, 0 siblings.\n" +
			"Callers (1 direct, 0 transitive; who breaks if behavior changes):\n- Bar (internal/a/b.go:5, def :4)\n" +
			"Callees (1; what it depends on):\n- fmt.Errorf\n" +
			"Co-change coupling (1; files that historically change with internal/a/a.go):\n- internal/c/c.go [3 shared commits]\n" +
			"Same-container siblings (0):\n- none\n", []string{a, b, "internal/c/c.go"}},
		{"impact-text-no-match-degraded", "Completeness: unsafe for this query (2 this query files failed to parse; 3 total diagnostics in this snapshot)\n" +
			"- partial E_MINIFIED: web/app.min.js\nNo symbols matched \"Nope\".\n", nil},
		{"def-text-hit", "internal/a/a.go:6  function Foo\n  signature: func Foo(x int) error\n\n" +
			"internal/a/a.go:6-11 Foo [function]\n   6→ func Foo(x int) error {\n   7→ \treturn x.go:1\n  11→ }\n", []string{a}},
		{"def-text-no-match", "No symbols matched \"Nope\". Run `entire graph query --query \"Nope\"` to find the name.\n", nil},
		{"json-query-empty-results-with-partial-failure", `{"format_version":1,"query":"zz","results":[],"partial_failures":[{"code":"E_MINIFIED","file_path":"web/app.min.js"}],"warnings":[]}`, nil},
		{"json-empty-results-names-nothing", `{"results":[],"related":[{"file_path":"internal/a/a.go"}]}`, nil},
		{"json-query-hits-skip-diagnostics", `{"results":[{"rank":1,"file_path":"internal/a/a.go","symbol_id":"x:Go:internal/a/b.go:function:F"}],"partial_failures":[{"file_path":"web/app.min.js"}],"verify_command":{"command":"go test ./internal/a"}}`, []string{a}},
		{"json-neighbors-no-match", `{"matches":[],"completeness_scope":{"in_scope_failures":[{"file_path":"web/app.min.js"}]},"partial_failures":[{"file_path":"web/app.min.js"}]}`, nil},
		// Keys are walked in sorted order: callers before focus.
		{"json-impact-hit", `{"focus":{"file_path":"internal/a/a.go"},"callers":{"entries":[{"endpoint":{"file_path":"internal/a/b.go"}}]},"completeness":{"health":{"file_path":"web/app.min.js"}}}`, []string{b, a}},
		{"json-def-hit", `{"declarations":[{"file_path":"internal/a/a.go"}],"partial_failures":[{"file_path":"web/app.min.js"}]}`, []string{a}},
		// Clipped JSON (`| head -c`) does not parse: answer sections still count, diagnostics do not.
		{"json-clipped", `{"partial_failures":[{"file_path":"web/app.min.js"}],"results":[{"file_path":"internal/a/a.go","symbol":{"file_path":"internal/a/b.go"}}],"warnings":[{"file_path":"web/x.js"}],"results2`, []string{a, b}},
		// JSON after other text is still JSON: an echoed header, or a persisted-output preview.
		{"json-line-after-header", "=== search ===\n" + `{"results":[{"file_path":"internal/a/a.go"}],"partial_failures":[{"file_path":"web/app.min.js"}]}` + "\n", []string{a}},
		{"json-line-in-persisted-preview", "<persisted-output>\nOutput too large (31.1KB). Full output saved to: /tmp/x/tool-results/b.txt\n\nPreview (first 2KB):\n" +
			`{"format_version":1,"partial_failures":[{"file_path":"web/app.min.js"}],"results":[{"file_path":"internal/a/b.go","snippet":"x`, []string{b}},
		// Escapes in a clipped value are decoded, not cut at the first backslash.
		// (No backslash in the decoded value: normaliseOverlapPath turns one into "/" on Windows.)
		{"json-clipped-escaped-path", `{"results":[{"file_path":"internal\u002fa\"y.go"},{"file_path":"internal/a/a.go"}],"resu`, []string{"internal/a\"y.go", a}},
		{"json-clipped-before-any-answer", `{"partial_failures":[{"file_path":"web/app.min.js"}],"profile":"fast","resu`, nil},
		// Prose and lowercase selectors are not files, located or not.
		{"prose-eg", "see e.g. the docs", nil},
		{"prose-eg-located", "see e.g:1 and store.list:3 and os.path:2", nil},
		{"slashless-known-extension", "1. main.go:3 main\n", []string{"main.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := graphOutputPaths(tc.output); !slices.Equal(got, tc.want) {
				t.Fatalf("graphOutputPaths = %q, want %q", got, tc.want)
			}
		})
	}
}

// B1 end to end: a zero-result locate followed by a full window of unrelated calls named nothing,
// so it is INELIGIBLE, never NO FOLLOW-UP — and it is not credited in the modeled balance.
func TestFollowUpZeroResultLocateIsNeverCredited(t *testing.T) {
	t.Parallel()
	agentQuery := `entire graph query --repo . --profile full --head --format agent --max-context-bytes 4096 --query "nonexistent thing"`
	calls := append([]dispCall{grepCall(), bashCall(agentQuery, zeroResultAgentQuery)}, fillers(5)...)
	raw := sessionOf(t, calls...)
	assertFollowUp(t, raw, map[string]int64{"ineligible": 1})
	if got := rawInt(t, raw, "modeled_balance_credited_results"); got != 0 {
		t.Fatalf("modeled_balance_credited_results = %d, want 0", got)
	}
	// Prose that merely looks like a dotted token is not a named file either.
	assertFollowUp(t, sessionOf(t, append([]dispCall{graphSearch("see e.g. the docs")}, fillers(5)...)...),
		map[string]int64{"ineligible": 1})
}

// B2 end to end: a query and a neighbors call in ONE turn both name internal/a.go, then the agent
// reads it. Both results were followed up; neither may finish its window as NO FOLLOW-UP.
func TestFollowUpParallelResultsSharingOneReadAreBothFollowedUp(t *testing.T) {
	t.Parallel()
	ts := statsTime(0)
	dir := t.TempDir()
	lines := []string{
		batchLine(t, ts,
			toolUseBlock("Bash", "q1", map[string]any{"command": "entire graph query --repo . --format agent --query x"}),
			toolUseBlock("Bash", "q2", map[string]any{"command": "entire graph neighbors --repo . --format agent --symbol Foo --direction in"})),
		resultLine(t, ts, "q1", "1. internal/a.go:10-20 Foo\n", false),
		resultLine(t, ts, "q2", "< Bar (internal/a.go:40)\n", false),
	}
	lines = append(lines, dispLines(t, "r", []dispCall{
		readFile("/repo/internal/a.go", "body"),
		bashCall("go test ./...", "ok"), bashCall("go test ./...", "ok"),
		bashCall("go test ./...", "ok"), bashCall("go test ./...", "ok"),
	})...)
	writeTranscript(t, dir, "s.jsonl", lines...)
	raw := dispReport(t, dir)
	assertFollowUp(t, raw, map[string]int64{"unknown": 1, "shared": 1})
	if got := rawInt(t, raw, "modeled_balance_credited_results"); got != 0 {
		t.Fatalf("modeled_balance_credited_results = %d, want 0", got)
	}
}

// A read claimed by a sibling only marks results whose NAMED file it opened: an unrelated parallel
// result still runs out its window.
func TestFollowUpSharedReadNeedsANamedFile(t *testing.T) {
	t.Parallel()
	ts := statsTime(0)
	dir := t.TempDir()
	lines := []string{
		batchLine(t, ts,
			toolUseBlock("Bash", "q1", map[string]any{"command": graphCommand}),
			toolUseBlock("Bash", "q2", map[string]any{"command": graphCommand})),
		resultLine(t, ts, "q1", "1. internal/other.go:1 X\n", false),
		resultLine(t, ts, "q2", "1. internal/a.go:1 Y\n", false),
	}
	lines = append(lines, dispLines(t, "r", append([]dispCall{readFile("/repo/internal/a.go", "b")}, fillers(4)...))...)
	writeTranscript(t, dir, "s.jsonl", lines...)
	assertFollowUp(t, dispReport(t, dir), map[string]int64{"unknown": 1, "none": 1})
}

func TestGraphVerbFromCommandSeesThroughWrappers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		command string
		verb    string
	}{
		{"nice -n 15 entire graph query --query x", "query"},
		{"nice -n15 entire graph query --query x", "query"},
		{"nice -10 entire graph neighbors --symbol Foo", "neighbors"},
		{"nice --adjustment 5 entire-graph impact --symbol Foo", "impact"},
		{"timeout 60 entire graph query --query x", "query"},
		{"timeout -s KILL 30s entire graph search --query x", "search"},
		{"timeout --preserve-status 5m entire graph def --symbol Foo", "def"},
		{"env -i HOME=/tmp GOMAXPROCS=2 entire graph query --query x", "query"},
		{"env -u FOO entire graph query --query x", "query"},
		{"time -p entire graph query --query x", "query"},
		{"command entire graph query --query x", "query"},
		{"nice -n 15 env FOO=1 timeout 60 entire graph query --query x", "query"},
		{"cd /repo && GOMAXPROCS=2 nice -n 15 entire graph impact --symbol Foo", "impact"},
		{"command -v entire graph query", ""},
		{"timeout 60 go test ./...", ""},
		{"nice -n 15 go test ./...", ""},
	} {
		verb, ok := graphVerbFromCommand(tc.command)
		if tc.verb == "" {
			if ok {
				t.Errorf("%q: counted as graph verb %q", tc.command, verb)
			}
			continue
		}
		if !ok || verb != tc.verb {
			t.Errorf("%q: verb = %q (%v), want %q", tc.command, verb, ok, tc.verb)
		}
	}
}

func TestFollowUpReadOfQuotedSourcePathIsNotAReadOfLocatedFile(t *testing.T) {
	t.Parallel()
	body := "1. internal/a.go:10-20 Foo\nlog.Printf(\"see docs/readme.md:12\")\n"
	calls := []dispCall{graphSearch(body), readFile("/r/docs/readme.md", "x")}
	calls = append(calls, fillers(5)...)
	assertFollowUp(t, sessionOf(t, calls...), map[string]int64{"none": 1})
}
