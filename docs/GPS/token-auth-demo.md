# Token/Auth MVP Demo

The checked-in fixture at `internal/cli/testdata/gps/token-auth/` is a small,
local-only authentication service. It contains two authored requirements, two
declared test mappings, token creation, and Go tests. The CLI integration test
copies it into a temporary Git repository, so it also covers committed-view and
base-revision behavior without depending on the current checkout.

```sh
entire graph spec validate --repo . --format json
entire graph context --repo . --query token --max-context-bytes 1600 --format json
entire graph check --repo . --head --base <base-commit> --format json
```

The fixture intentionally starts without reviewed anchor bindings. Bind an
anchor after inspection with `entire graph anchor bind`; a declared test mapping
is an obligation to inspect, not evidence that a test ran. The integration
fixture changes `auth.go` in a later commit and expects the static
`GPS-DELTA-CODE-ONLY` finding.

`internal/cli/testdata/gps/golden/` pins the public JSON contract fields for
invalid validation, Git-backed code-only checks, removed mappings and anchors,
working-tree versus `--head` selection, ignored paths, partial graphs, and
ambiguous anchor resolution. Dynamic values such as Git object IDs and intent
digests are deliberately excluded from those compact goldens. The fixture
README also documents the malformed-binding and unborn-`HEAD` negative cases.

## Invalid Authoring

`entire graph spec validate` reports independent readable-document failures in
one deterministic JSON response:

```json
{
  "schema_version": "1.0",
  "valid": false,
  "diagnostics": [
    {"path": ".entire/graph/specs/auth.yaml", "code": "E_SPEC_INVALID", "message": "..."}
  ]
}
```

Normal GPS consumers remain strict: `context`, `check`, `anchor`, and `why`
stop when intent is invalid rather than analyzing a partial declaration set.

## Context Budget

The JSON `budget` object includes deterministic section percentages:
requirements 25%, approved symbols 10%, ranked source snippets 35%,
dependencies 5%, declared tests 10%, and inferred tests 15%. Unused capacity
carries only to later sections. `rendered_bytes` counts the final UTF-8 JSON
serialization, including metadata and omission diagnostics; output that cannot
fit returns `BUDGET_TOO_SMALL` with a minimal manifest. The minimal manifest
omits the quota declaration itself if that is necessary to meet the budget.
