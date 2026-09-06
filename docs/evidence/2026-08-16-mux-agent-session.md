# Captured agent session — gorilla/mux, Entire Graph v0.3.0

This is the evidence record behind the transcript excerpted in the
[root README](../../README.md). It contains the capture conditions, every
observable event in order, the graph command outputs verbatim, and the agent's
final answer verbatim. It is a point-in-time record: agent behavior is not
deterministic, so a re-run reproduces the setup and the graph outputs exactly,
and the agent's tool sequence approximately.

## Capture conditions

- Date: 2026-08-16.
- Entire Graph: the v0.3.0 release binary from
  `entire-graph_0.3.0_darwin_arm64.tar.gz` (checksum verified against the
  release's `checksums.txt`), dispatched as `entire graph`.
  `entire graph version` printed `v0.3.0`.
- Fixture: a fresh clone of `github.com/gorilla/mux` at upstream commit
  `db9d1d0073d27a0a2d9a8c1bc52aa0af4374d265`, containing no agent instruction
  files.
- Activation: `entire graph init-agents --repo .` (v0.3.0), then the three
  generated files were committed. The activation commit's hash depends on
  committer metadata, but its Git *tree* hash is reproducible from identical
  content: `1d03684d8a9a174794c55f51f23a340d863a401e`, as recorded in the
  session's search output below.
- Client: Claude Code 2.1.233, model Claude Fable 5, run headless
  (`claude -p … --output-format stream-json`) from the fixture root with tools
  restricted to Bash, Read, Grep, and Glob.
- Cache state: warm (the activation flow had already run queries against the
  committed tree), which is why the outputs below report `cache-hit`.

## Prompt

> Without changing any files, find where route matching is implemented in this
> repository, what calls it, and what is most likely to be affected if its
> behavior changes. Cite the relevant source.

## Observable events, in order

Agent text and tool commands are verbatim from the session stream.

1. Agent: "I'll use the code graph to locate route matching, then trace its
   callers and impact."
2. Tool call:
   `entire graph search --repo . --profile full --query "route matching implementation for incoming request paths"`
3. Tool call: `entire graph impact --repo . --symbol Route.Match`
4. Tool call: `entire graph impact --repo . --symbol Router.Match`
5. Agent: "Now reading the focused source around the three layers:
   `Router.Match`/`ServeHTTP`, `Route.Match`, and the `routeRegexp` matcher."
6. Tool call: `sed -n 145,230p mux.go`
7. Tool call: `sed -n 40,120p route.go`
8. Tool call: `grep -n "func.*Match\|matcher\b" route.go regexp.go | head -40`
9. Agent: "One more focused read: the lowest-level matcher
   `routeRegexp.Match`, plus a check for other callers of `Match` (e.g. the
   CORS middleware)."
10. Tool call:
    `sed -n 185,225p regexp.go; grep -n "\.Match(" mux.go route.go middleware.go route_test.go mux_test.go 2>/dev/null | grep -v "_test"`
11. Agent: final answer (verbatim below).

## Graph outputs the agent received

### Search (event 2) — leading excerpt

The full response was 12,572 bytes of JSON, reproduced in its entirety in
[Full search response](#full-search-response-event-2) below. Its opening,
truncated at the first result's signature field and marked here as an
excerpt:

```text
{"query":"route matching implementation for incoming request paths",
 "commit":"d0a7bb58f336e47bb9f6d24021edf634a998e4fd",
 "tree":"1d03684d8a9a174794c55f51f23a340d863a401e","profile":"full",
 "results":[{"rank":1,"score":32.7491,"file_path":"route.go",
 "start_line":237,"end_line":292, ... "kind":"method",
 "symbol_id":"gh/gorilla/mux:Go:route.go:method:Route.addMatcher",
 "qualified_name":"Route.addMatcher",
 "signature":"func (r *Route) addMatcher(m matcher) *Route", ...
```

(One long line wrapped for width; the `repo_root` field, a local absolute
path, is elided.) Rank 2 was `newRouteRegexp` (`regexp.go:41`). The response
ended with `"index_cache_hit":true` in its `stats`, a `verify_command` of
`go test . -run '^Test_newRouteRegexp_Errors$'`, and a completeness breakdown.

### Impact on `Route.Match` (event 3) — verbatim

```text
Index: cache-hit (60ms) | Query: 0ms | Total: 61ms
Impact: Route.Match (route.go:47) def=47 span=47-114 [method in Route]
Blast radius: 0 callers (0 direct, 0 transitive), 2 callees, 3 type consumers, 0 data flows, 9 co-change files, 46 siblings.
Callers (0 direct, 0 transitive; who breaks if behavior changes):
- none
Callees (2; what it depends on):
- routeRegexpGroup.setMatch (regexp.go:339)
- Route.GetHandlerWithMiddlewares (route.go:196)
Type consumers (0 in, 3 out; USES_TYPE/PARAM_TYPE/RETURNS_TYPE):
- -> RouteMatch (mux.go:446) [PARAM_TYPE]
- -> RouteMatch (mux.go:446) [USES_TYPE]
- -> Route (route.go:17) [USES_TYPE]
Data flows (0 in, 0 out):
- none
Co-change coupling (9; files that historically change with route.go):
- README.md [files changed together in 4 recent commits]
- doc.go [files changed together in 2 recent commits]
- middleware.go [files changed together in 4 recent commits]
- middleware_test.go [files changed together in 3 recent commits]
- mux.go [files changed together in 12 recent commits]
- mux_httpserver_test.go [files changed together in 2 recent commits]
- mux_test.go [files changed together in 23 recent commits]
- old_test.go [files changed together in 6 recent commits]
- regexp.go [files changed together in 8 recent commits]
Same-container siblings (46):
- Route.handler (route.go:19)
- Route.buildOnly (route.go:21)
- Route.name (route.go:23)
- Route.err (route.go:25)
- Route.metadata (route.go:28)
- Route.namedRoutes (route.go:31)
- Route.middlewares (route.go:34)
- Route.SkipClean (route.go:42)
- Route.GetError (route.go:121)
- Route.BuildOnly (route.go:126)
- Route.Metadata (route.go:134)
- Route.GetMetadata (route.go:144)
- Route.MetadataContains (route.go:149)
- Route.GetMetadataValue (route.go:155)
- Route.GetMetadataValueOr (route.go:165)
- ... +31 more (use --format json for the full list)
```

The `0 callers` line here is the graph limitation discussed in the README and
final answer: both real call sites (`mux.go:153`, `middleware.go:79`) invoke
`Route.Match` through a loop over an interface value, which static call
resolution does not attribute to this method.

### Impact on `Router.Match` (event 4) — verbatim

```text
Index: cache-hit (49ms) | Query: 0ms | Total: 50ms
Impact: Router.Match (mux.go:151) def=151 span=151-182 [method in Router]
Blast radius: 1 caller (1 direct, 0 transitive), 0 callees, 3 type consumers, 1 data flow, 7 co-change files, 29 siblings.
Callers (1 direct, 0 transitive; who breaks if behavior changes):
- Router.ServeHTTP (mux.go:203, def :188)
Callees (0; what it depends on):
- none
Type consumers (0 in, 3 out; USES_TYPE/PARAM_TYPE/RETURNS_TYPE):
- -> Router (mux.go:54) [USES_TYPE]
- -> RouteMatch (mux.go:446) [PARAM_TYPE]
- -> RouteMatch (mux.go:446) [USES_TYPE]
Data flows (1 in, 0 out):
- <- Router.ServeHTTP (mux.go:188)
Co-change coupling (7; files that historically change with mux.go):
- doc.go [files changed together in 3 recent commits]
- middleware.go [files changed together in 3 recent commits]
- mux_test.go [files changed together in 16 recent commits]
- old_test.go [files changed together in 3 recent commits]
- regexp.go [files changed together in 7 recent commits]
- route.go [files changed together in 12 recent commits]
- route_test.go [files changed together in 2 recent commits]
Same-container siblings (29):
- Router.NotFoundHandler (mux.go:57)
- Router.MethodNotAllowedHandler (mux.go:61)
- Router.routes (mux.go:64)
- Router.namedRoutes (mux.go:67)
- Router.KeepContext (mux.go:72)
- Router.middlewares (mux.go:75)
- Router.ServeHTTP (mux.go:188)
- Router.Get (mux.go:232)
- Router.GetRoute (mux.go:238)
- Router.StrictSlash (mux.go:261)
- Router.SkipClean (mux.go:274)
- Router.OmitRouteFromContext (mux.go:284)
- Router.OmitRouterFromContext (mux.go:293)
- Router.UseEncodedPath (mux.go:304)
- Router.NewRoute (mux.go:314)
- ... +14 more (use --format json for the full list)
```

The source reads (events 6-10) returned line ranges of `mux.go`, `route.go`,
and `regexp.go` at the pinned commit; they are reproducible from the fixture
and are not duplicated here.

## Full search response (event 2)

The complete JSON the agent received, 12,572 bytes as one line on the
wire, reproduced here pretty-printed (`python3 -m json.tool` whitespace only;
every key and value is unmodified). The `repo_root` value is the local
scratch path the capture ran in.

```json
{
  "query": "route matching implementation for incoming request paths",
  "repo_root": "/private/tmp/claude-501/-Users-thomi-Projects-entire-graph/6c774419-2fd5-436e-95ca-e30ee7ad67e1/scratchpad/fixture-mux",
  "commit": "d0a7bb58f336e47bb9f6d24021edf634a998e4fd",
  "tree": "1d03684d8a9a174794c55f51f23a340d863a401e",
  "profile": "full",
  "results": [
    {
      "rank": 1,
      "score": 32.7491,
      "file_path": "route.go",
      "start_line": 237,
      "end_line": 292,
      "focus_line": 239,
      "snippet_start_line": 239,
      "snippet_end_line": 292,
      "symbol_start_line": 239,
      "symbol_end_line": 244,
      "language": "Go",
      "kind": "method",
      "symbol_id": "gh/gorilla/mux:Go:route.go:method:Route.addMatcher",
      "symbol_name": "addMatcher",
      "qualified_name": "Route.addMatcher",
      "signature": "func (r *Route) addMatcher(m matcher) *Route",
      "signals": [
        "path",
        "body",
        "symbol-name",
        "graph:callers",
        "complete-symbol",
        "contiguous-span"
      ],
      "snippet": "func (r *Route) addMatcher(m matcher) *Route {\n\tif r.err == nil {\n\t\tr.matchers = append(r.matchers, m)\n\t}\n\treturn r\n}\n\n// addRegexpMatcher adds a host or path matcher and builder to a route.\nfunc (r *Route) addRegexpMatcher(tpl string, typ regexpType) error {\n\tif r.err != nil {\n\t\treturn r.err\n\t}\n\tif typ == regexpTypePath || typ == regexpTypePrefix {\n\t\tif len(tpl) > 0 && tpl[0] != '/' {\n\t\t\treturn fmt.Errorf(\"mux: path must start with a slash, got %q\", tpl)\n\t\t}\n\t\tif r.regexp.path != nil {\n\t\t\ttpl = strings.TrimRight(r.regexp.path.template, \"/\") + tpl\n\t\t}\n\t}\n\trr, err := newRouteRegexp(tpl, typ, routeRegexpOptions{\n\t\tstrictSlash:    r.strictSlash,\n\t\tuseEncodedPath: r.useEncodedPath,\n\t})\n\tif err != nil {\n\t\treturn err\n\t}\n\tfor _, q := range r.regexp.queries {\n\t\tif err = uniqueVars(rr.varsN, q.varsN); err != nil {\n\t\t\treturn err\n\t\t}\n\t}\n\tif typ == regexpTypeHost {\n\t\tif r.regexp.path != nil {\n\t\t\tif err = uniqueVars(rr.varsN, r.regexp.path.varsN); err != nil {\n\t\t\t\treturn err\n\t\t\t}\n\t\t}\n\t\tr.regexp.host = rr\n\t} else {\n\t\tif r.regexp.host != nil {\n\t\t\tif err = uniqueVars(rr.varsN, r.regexp.host.varsN); err != nil {\n\t\t\t\treturn err\n\t\t\t}\n\t\t}\n\t\tif typ == regexpTypeQuery {\n\t\t\tr.regexp.queries = append(r.regexp.queries, rr)\n\t\t} else {\n\t\t\tr.regexp.path = rr\n\t\t}\n\t}\n\tr.addMatcher(rr)\n\treturn nil\n}",
      "merged_ranks": [
        1,
        3
      ]
    },
    {
      "rank": 2,
      "score": 31.7137,
      "file_path": "regexp.go",
      "start_line": 41,
      "end_line": 165,
      "focus_line": 41,
      "snippet_start_line": 41,
      "snippet_end_line": 165,
      "symbol_start_line": 41,
      "symbol_end_line": 165,
      "language": "Go",
      "kind": "function",
      "symbol_id": "gh/gorilla/mux:Go:regexp.go:function:newRouteRegexp",
      "symbol_name": "newRouteRegexp",
      "qualified_name": "newRouteRegexp",
      "signature": "func newRouteRegexp(tpl string, typ regexpType, options routeRegexpOptions) (*routeRegexp, error)",
      "signals": [
        "graph:calls",
        "graph:outgoing",
        "graph:callers",
        "complete-symbol"
      ],
      "snippet": "func newRouteRegexp(tpl string, typ regexpType, options routeRegexpOptions) (*routeRegexp, error) {\n\t// Check if it is well-formed.\n\tidxs, errBraces := braceIndices(tpl)\n\tif errBraces != nil {\n\t\treturn nil, errBraces\n\t}\n\t// Backup the original.\n\ttemplate := tpl\n\t// Now let's parse it.\n\tdefaultPattern := \"[^/]+\"\n\tif typ == regexpTypeQuery {\n\t\tdefaultPattern = \".*\"\n\t} else if typ == regexpTypeHost {\n\t\tdefaultPattern = \"[^.]+\"\n\t}\n\t// Only match strict slash if not matching\n\tif typ != regexpTypePath {\n\t\toptions.strictSlash = false\n\t}\n\t// Set a flag for strictSlash.\n\tendSlash := false\n\tif options.strictSlash && strings.HasSuffix(tpl, \"/\") {\n\t\ttpl = tpl[:len(tpl)-1]\n\t\tendSlash = true\n\t}\n\tvarsN := make([]string, len(idxs)/2)\n\tvarsR := make([]*regexp.Regexp, len(idxs)/2)\n\n\tvar pattern, reverse strings.Builder\n\tpattern.WriteByte('^')\n\n\tvar end, colonIdx, groupIdx int\n\tvar err error\n\tvar patt, param, name string\n\tfor i := 0; i < len(idxs); i += 2 {\n\t\t// Set all values we are interested in.\n\t\tgroupIdx = i / 2\n\n\t\traw := tpl[end:idxs[i]]\n\t\tend = idxs[i+1]\n\t\ttag := tpl[idxs[i]:end]\n\n\t\t// trim braces from tag\n\t\tparam = tag[1 : len(tag)-1]\n\n\t\tcolonIdx = strings.Index(param, \":\")\n\t\tif colonIdx == -1 {\n\t\t\tname = param\n\t\t\tpatt = defaultPattern\n\t\t} else {\n\t\t\tname = param[0:colonIdx]\n\t\t\tpatt = param[colonIdx+1:]\n\t\t}\n\n\t\t// Name or pattern can't be empty.\n\t\tif name == \"\" || patt == \"\" {\n\t\t\treturn nil, fmt.Errorf(\"mux: missing name or pattern in %q\", tag)\n\t\t}\n\t\t// Build the regexp pattern.\n\t\tgroupName := varGroupName(groupIdx)\n\n\t\tpattern.WriteString(regexp.QuoteMeta(raw) + \"(?P<\" + groupName + \">\" + patt + \")\")\n\n\t\t// Build the reverse template.\n\t\treverse.WriteString(raw + \"%s\")\n\n\t\t// Append variable name and compiled pattern.\n\t\tvarsN[groupIdx] = name\n\t\tvarsR[groupIdx], err = RegexpCompileFunc(\"^\" + patt + \"$\")\n\t\tif err != nil {\n\t\t\treturn nil, fmt.Errorf(\"mux: error compiling regex for %q: %w\", tag, err)\n\t\t}\n\t}\n\t// Add the remaining.\n\traw := tpl[end:]\n\tpattern.WriteString(regexp.QuoteMeta(raw))\n\tif options.strictSlash {\n\t\tpattern.WriteString(\"[/]?\")\n\t}\n\tif typ == regexpTypeQuery {\n\t\t// Add the default pattern if the query value is empty\n\t\tif queryVal := strings.SplitN(template, \"=\", 2)[1]; queryVal == \"\" {\n\t\t\tpattern.WriteString(defaultPattern)\n\t\t}\n\t}\n\tif typ != regexpTypePrefix {\n\t\tpattern.WriteByte('$')\n\t}\n\n\t// Compile full regexp.\n\tpatternStr := pattern.String()\n\treg, errCompile := RegexpCompileFunc(patternStr)\n\tif errCompile != nil {\n\t\treturn nil, errCompile\n\t}\n\n\t// Check for capturing groups which used to work in older versions\n\tif reg.NumSubexp() != len(idxs)/2 {\n\t\tpanic(fmt.Sprintf(\"route %s contains capture groups in its regexp. \", template) +\n\t\t\t\"Only non-capturing groups are accepted: e.g. (?:pattern) instead of (pattern)\")\n\t}\n\n\tvar wildcardHostPort bool\n\tif typ == regexpTypeHost {\n\t\tif !strings.Contains(patternStr, \":\") {\n\t\t\twildcardHostPort = true\n\t\t}\n\t}\n\treverse.WriteString(raw)\n\tif endSlash {\n\t\treverse.WriteByte('/')\n\t}\n\n\t// Done!\n\treturn &routeRegexp{\n\t\ttemplate:         template,\n\t\tregexpType:       typ,\n\t\toptions:          options,\n\t\tregexp:           reg,\n\t\treverse:          reverse.String(),\n\t\tvarsN:            varsN,\n\t\tvarsR:            varsR,\n\t\twildcardHostPort: wildcardHostPort,\n\t}, nil\n}"
    },
    {
      "rank": 3,
      "score": 26.6965,
      "file_path": "doc.go",
      "start_line": 89,
      "end_line": 93,
      "focus_line": 91,
      "snippet_start_line": 89,
      "snippet_end_line": 93,
      "language": "Go",
      "signals": [
        "body",
        "symbol-usage"
      ],
      "snippet": "...or header values:\n\n\tr.Headers(\"X-Requested-With\", \"XMLHttpRequest\")\n\n...or query values:"
    },
    {
      "rank": 4,
      "score": 26.6965,
      "file_path": "middleware.go",
      "start_line": 55,
      "end_line": 70,
      "focus_line": 55,
      "snippet_start_line": 55,
      "snippet_end_line": 70,
      "symbol_start_line": 55,
      "symbol_end_line": 70,
      "language": "Go",
      "kind": "function",
      "symbol_id": "gh/gorilla/mux:Go:middleware.go:function:CORSMethodMiddleware",
      "symbol_name": "CORSMethodMiddleware",
      "qualified_name": "CORSMethodMiddleware",
      "signature": "func CORSMethodMiddleware(r *Router) MiddlewareFunc",
      "signals": [
        "body",
        "symbol-usage",
        "complete-symbol"
      ],
      "snippet": "func CORSMethodMiddleware(r *Router) MiddlewareFunc {\n\treturn func(next http.Handler) http.Handler {\n\t\treturn http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {\n\t\t\tallMethods, err := getAllMethodsForRoute(r, req)\n\t\t\tif err == nil {\n\t\t\t\tfor _, v := range allMethods {\n\t\t\t\t\tif v == http.MethodOptions {\n\t\t\t\t\t\tw.Header().Set(\"Access-Control-Allow-Methods\", strings.Join(allMethods, \",\"))\n\t\t\t\t\t}\n\t\t\t\t}\n\t\t\t}\n\n\t\t\tnext.ServeHTTP(w, req)\n\t\t})\n\t}\n}",
      "comment_focus_line": 51,
      "body_from_reanchor": true
    },
    {
      "rank": 5,
      "score": 26.6965,
      "file_path": "mux.go",
      "start_line": 338,
      "end_line": 342,
      "focus_line": 342,
      "snippet_start_line": 341,
      "snippet_end_line": 342,
      "language": "Go",
      "signals": [
        "body",
        "symbol-usage"
      ],
      "snippet": "// See Route.Headers().\nfunc (r *Router) Headers(pairs ...string) *Route {",
      "comment_focus_line": 340
    },
    {
      "rank": 6,
      "score": 28.5423,
      "file_path": "regexp.go",
      "start_line": 41,
      "end_line": 45,
      "focus_line": 41,
      "snippet_start_line": 41,
      "snippet_end_line": 42,
      "language": "Go",
      "signals": [
        "body",
        "symbol-usage"
      ],
      "snippet": "func newRouteRegexp(tpl string, typ regexpType, options routeRegexpOptions) (*routeRegexp, error) {\n\t// Check if it is well-formed.",
      "comment_focus_line": 31
    },
    {
      "rank": 7,
      "score": 0,
      "file_path": "regexp.go",
      "start_line": 299,
      "end_line": 320,
      "focus_line": 299,
      "snippet_start_line": 299,
      "snippet_end_line": 299,
      "kind": "function",
      "qualified_name": "braceIndices",
      "signals": [
        "related:sibling"
      ],
      "section": "related",
      "snippet": "func braceIndices(s string) ([]int, error) {"
    },
    {
      "rank": 8,
      "score": 0,
      "file_path": "route.go",
      "start_line": 338,
      "end_line": 345,
      "focus_line": 338,
      "snippet_start_line": 338,
      "snippet_end_line": 338,
      "kind": "method",
      "qualified_name": "Route.HeadersRegexp",
      "signals": [
        "related:caller"
      ],
      "section": "related",
      "snippet": "func (r *Route) HeadersRegexp(pairs ...string) *Route {"
    },
    {
      "rank": 9,
      "score": 0,
      "file_path": "regexp.go",
      "start_line": 257,
      "end_line": 291,
      "focus_line": 257,
      "snippet_start_line": 257,
      "snippet_end_line": 257,
      "kind": "function",
      "qualified_name": "findFirstQueryKey",
      "signals": [
        "related:sibling"
      ],
      "section": "related",
      "snippet": "func findFirstQueryKey(rawQuery, key string) (value string, ok bool) {"
    },
    {
      "rank": 10,
      "score": 0,
      "file_path": "route.go",
      "start_line": 382,
      "end_line": 384,
      "focus_line": 382,
      "snippet_start_line": 382,
      "snippet_end_line": 382,
      "kind": "method",
      "qualified_name": "Route.MatcherFunc",
      "signals": [
        "related:caller"
      ],
      "section": "related",
      "snippet": "func (r *Route) MatcherFunc(f MatcherFunc) *Route {"
    },
    {
      "rank": 11,
      "score": 0,
      "file_path": "regexp_test.go",
      "start_line": 11,
      "end_line": 34,
      "focus_line": 30,
      "snippet_start_line": 29,
      "snippet_end_line": 32,
      "kind": "function",
      "qualified_name": "Test_newRouteRegexp_Errors",
      "signals": [
        "related:covering-test"
      ],
      "section": "covering-test",
      "snippet": "\t\t\t} else {\n\t\t\t\tt.Error(\"Expected error, got nil\")\n\t\t\t}\n\t\t})"
    }
  ],
  "literal_cluster": {
    "literal": "routeRegexpOptions",
    "hits": [
      {
        "file_path": "mux_test.go",
        "line": 2786,
        "symbol": "Test_copyRouteConf",
        "role": "CONSUMER"
      },
      {
        "file_path": "old_test.go",
        "line": 698,
        "symbol": "TestNewRegexp",
        "role": "CONSUMER"
      },
      {
        "file_path": "regexp.go",
        "line": 17,
        "symbol": "routeRegexpOptions",
        "role": "EDIT"
      },
      {
        "file_path": "regexp_test.go",
        "line": 23,
        "symbol": "Test_newRouteRegexp_Errors",
        "role": "CONSUMER"
      }
    ],
    "hits_total": 7,
    "files_total": 5
  },
  "verify_command": {
    "command": "go test . -run '^Test_newRouteRegexp_Errors$'",
    "targets": "regexp_test.go",
    "derived_from": "go.mod module root + covering test name",
    "tier": "narrow"
  },
  "coverage_note": {
    "symbol": "newRouteRegexp",
    "total": 3,
    "peers": [
      "Test_copyRouteConf",
      "TestNewRegexp"
    ]
  },
  "stats": {
    "files_scanned": 30,
    "preselection_backend": "inventory",
    "preselection_passes": 1,
    "preselection_files_examined": 30,
    "files_content_read_during_preselection": 0,
    "files_content_read_during_query": 30,
    "bytes_content_read_during_query": 258580,
    "files_indexed": 30,
    "symbols_considered": 484,
    "lexical_candidates": 411,
    "graph_candidates": 20,
    "caller_boosted_candidates": 39,
    "identifier_usage_candidates": 32,
    "same_container_neighbor_candidates": 9,
    "same_file_bridge_candidates": 3,
    "sparse_candidates": 0,
    "sparse_files_content_read": 0,
    "candidates_selected": 11,
    "result_bytes": 9821,
    "context_budget_bytes": 24576,
    "snippets_truncated_by_budget": 6,
    "complete_symbol_snippets": 3,
    "locator_snippets": 5,
    "doc_reanchored_hits": 3,
    "merged_spans": 1,
    "related_sites": 4,
    "covering_tests": 1,
    "literal_cluster_bytes": 419,
    "literal_cluster_hits": 4,
    "verify_command_bytes": 342,
    "verify_tier": "narrow",
    "context_block_bytes": 761,
    "index_cache_hit": true,
    "index_latency_ms": 54,
    "query_latency_ms": 70,
    "total_latency_ms": 170,
    "search_latency_ms": 170,
    "preselect_latency_ms": 46
  },
  "warnings": [
    {
      "code": "W_WORKTREE_SNAPSHOT",
      "severity": "warning",
      "effect_on_semantic_completeness": "snapshot records are read from the working tree because --worktree was requested"
    }
  ],
  "partial_failures": [],
  "completeness": {
    "languages": {
      "EditorConfig": {
        "files": 1,
        "symbols": 1
      },
      "Git Ignore": {
        "files": 1,
        "symbols": 1
      },
      "Go": {
        "files": 17,
        "symbols": 356
      },
      "Make": {
        "files": 1,
        "symbols": 10
      },
      "Markdown": {
        "files": 4,
        "symbols": 101
      },
      "YAML": {
        "files": 4,
        "symbols": 15
      }
    },
    "relations": {
      "ACCESSES": 1,
      "CALLS": 495,
      "CONFIGURES": 25,
      "CONSTRUCTS": 7,
      "CONTAINS": 195,
      "DATA_FLOWS": 93,
      "DEFINES": 484,
      "FILE_CHANGES_WITH": 32,
      "HANDLES_ROUTE": 43,
      "IMPORTS": 75,
      "PARAM_TYPE": 41,
      "READS_FIELD": 85,
      "RETURNS_TYPE": 46,
      "SIMILAR_TO": 12,
      "TESTS": 7,
      "USES_TYPE": 151,
      "WRITES_FIELD": 28
    }
  }
}
```

## Final answer — verbatim

> Route matching in this repo (a checkout of `gorilla/mux`) is implemented in
> three layers, dispatched from `Router.ServeHTTP`. Here's the map, verified
> against the graph output and the source itself.
>
> **Where matching is implemented**
>
> **1. `Router.Match` — `mux.go:151-182`.** The top-level entry: it iterates
> the registered routes in order, returns on the first `route.Match` success,
> wraps the matched handler in the router's middleware chain (only when
> `match.MatchErr == nil`), and otherwise falls back to
> `MethodNotAllowedHandler` (on `ErrMethodMismatch`) or
> `NotFoundHandler`/`ErrNotFound`.
>
> **2. `Route.Match` — `route.go:47-114`.** The per-route logic: it runs every
> registered `matcher` against the request (`route.go:56`), with special-case
> error handling — a failed `methodMatcher` records `ErrMethodMismatch` but
> keeps going, a failed query-type `routeRegexp` produces `ErrNotFound`, and
> subrouter `ErrNotFound`s are cleared so later siblings still run middleware.
> On success it fills `match.Route`/`match.Handler` and calls
> `r.regexp.setMatch` (`regexp.go:339`) to extract path/host/query variables
> into `match.Vars`.
>
> **3. The `matcher` implementations** behind the interface at `route.go:234`:
>
> - `routeRegexp.Match` — `regexp.go:189-209`, the core host/path/query regexp
>   test (path cleaning vs. `useEncodedPath` handled here). The regexps
>   themselves are built by `newRouteRegexp` (`regexp.go:41-165`), which the
>   graph search surfaced first, and attached via
>   `Route.addRegexpMatcher`/`addMatcher` (`route.go:239-292`).
> - `headerMatcher` (`route.go:299`), `headerRegexMatcher` (`route.go:324`),
>   `MatcherFunc` (`route.go:377`), `methodMatcher` (`route.go:391`),
>   `schemeMatcher` (`route.go:487`). A subrouter's `Router` is itself
>   installed as a matcher, which makes `Router.Match` recursive.
>
> **What calls it**
>
> - `Router.ServeHTTP` (`mux.go:203`) — the only direct caller of
>   `Router.Match` per `entire graph impact`, and the path every HTTP request
>   takes. After matching it injects the route, vars, and router into the
>   request context (`mux.go:206-215`).
> - `getAllMethodsForRoute` (`middleware.go:79`) calls `route.Match` directly
>   to compute allowed methods; it backs `CORSMethodMiddleware`
>   (`middleware.go:55`), which builds the `Access-Control-Allow-Methods`
>   header from those results.
> - `Router.Match` is exported public API, so downstream applications can call
>   it directly too. (One graph caveat I verified by hand:
>   `entire graph impact --symbol Route.Match` reports 0 callers because both
>   call sites go through a loop/interface — the real callers are `mux.go:153`
>   and `middleware.go:79`.)
>
> **Blast radius if matching behavior changes**
>
> - **Every request dispatch**: handler selection, 404 vs. 405 responses, and
>   whether middleware runs at all (`Router.Match` only wraps middleware when
>   `MatchErr` is nil — the subtle error-clearing logic in `Route.Match`
>   exists precisely to keep sibling subrouters' middleware working).
> - **Route variables**: `mux.Vars()` values come from `setMatch` at the end
>   of `Route.Match`; changes there ripple into any handler reading vars from
>   context.
> - **URL reversing**: `newRouteRegexp` builds both the match pattern and the
>   `reverse` template consumed by `routeRegexp.url` (`regexp.go:211`), so
>   `Route.URL`/`URLPath`/`URLHost` are coupled to the same code — a matching
>   change can silently break URL building.
> - **CORS middleware** output, via `getAllMethodsForRoute` as above.
> - **Historically coupled files** (graph co-change data): `mux.go`,
>   `route.go`, and `regexp.go` change together frequently, with `mux_test.go`
>   (23 co-commits), `old_test.go`, and `regexp_test.go` as the test surfaces
>   most likely to need updating — `go test .` over those is the right
>   verification for any change here. Per your request I changed nothing and
>   ran nothing mutating.

## Reproducing

1. Install Entire CLI 0.10.0+ and the v0.3.0 plugin release
   (`entire plugin install graph`; confirm with `entire graph version`).
2. `git clone https://github.com/gorilla/mux && cd mux && git checkout db9d1d0073d27a0a2d9a8c1bc52aa0af4374d265`
3. `entire graph init-agents --repo .`, review, commit the three files. The
   resulting tree hash should be `1d03684…` if the guide content matches
   v0.3.0's `entire graph agent-guide`.
4. Give a Claude Code session in that directory the prompt above. The graph
   command outputs will match this record byte for byte apart from latency
   numbers; the agent's exact tool sequence and answer wording will vary.
