# FastAPI GPS Analysis

Status: second stage of the FastAPI comparison. This analysis used a disposable
worktree at FastAPI commit `50113da16fec53b66b80d75e80a89296de4fa5a5` and did
not modify FastAPI application code or execute tests.

## Authored Intent

The analysis added a minimal local specification with two requirements:

- `REQ-OPENAPI-ROUTES`: registered path operations contribute to the OpenAPI
  schema.
- `REQ-OPENAPI-CACHE`: generated schema output is cached until routes change.

It also declared acceptance criteria and one test mapping. The intent document
validated successfully.

## Confirmed Structural Evidence

- `ANCHOR-OPENAPI-BUILDER` was explicitly bound to
  `get_openapi` at `fastapi/openapi/utils.py:585-679`.
- The bound builder has a resolved caller relation from the primary
  `FastAPI.openapi` method at `fastapi/applications.py:1070`.
- Its resolved direct callees include `get_fields_from_routes`,
  `_get_api_route_for_openapi`, `get_openapi_path`, `jsonable_encoder`, and the
  `OpenAPI` model construction.
- Two focused test functions call the builder directly:
  `test_get_openapi_accepts_filtered_route_contexts_with_effective_paths` and
  `test_get_openapi_accepts_webhook_route_contexts`.
- Context attached the approved builder anchor to
  `REQ-OPENAPI-ROUTES`, including source citation and graph dependencies.

## Heuristic Or Incomplete Evidence

- The query produced inferred `Test*` candidates from documentation headings.
  GPS marked them `fulfills_mapping: false`; they are not verification
  obligations.
- `FastAPI.openapi` is ambiguous because two methods share its qualified name
  in `fastapi/applications.py`. `anchor bind` currently accepts a file but not a
  line selector, so GPS correctly refused to create an application anchor.
- The graph was complete for this working-tree capture, but static caller and
  callee relations remain structural evidence, not a runtime execution trace.

## Claims Requiring Source Or Test Verification

- `TEST-OPENAPI-SCHEMA` did not resolve from the authored selector, producing
  `DECLARED_TEST_UNRESOLVED:TEST-OPENAPI-SCHEMA`. It must be replaced by a
  resolvable test symbol or intentionally left as a visible gap.
- `ANCHOR-OPENAPI-APPLICATION` remained unbound due to the duplicate method
  name. A line-aware anchor selector or an unambiguous symbol identity is
  required before treating the cache requirement as structurally linked.
- Correct OpenAPI output, cache invalidation behavior, and coverage of the
  acceptance criteria require source review and explicitly authorized FastAPI
  tests.

## GPS Value In This Stage

The code graph already found the schema generator. GPS added reviewed ownership:
the `REQ-OPENAPI-ROUTES` requirement is explicitly linked to the builder, and
the intended acceptance/test obligations are visible. It also made two missing
pieces of evidence explicit instead of treating related tests or an ambiguous
method name as confirmation.
