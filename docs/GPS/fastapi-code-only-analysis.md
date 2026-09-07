# FastAPI Code-Only Analysis

Status: first, code-only stage of the FastAPI GPS comparison. This document
records observed graph output only; it does not infer product requirements.

## Scope

- Repository: FastAPI
- Commit: `50113da16fec53b66b80d75e80a89296de4fa5a5`
- Tree: `13b7e52ad340d6524b0caf60feec7a5b2e55b58d`
- Question: how FastAPI constructs an OpenAPI schema for registered path
  operations.
- Commands: `search` and `impact` only. No GPS command or test execution was
  used in this stage.

## Confirmed Structural Evidence

- `FastAPI.openapi` in `fastapi/applications.py:1070-1103` is the primary
  application-level schema generation method.
- It caches the result in `app.openapi_schema` and invalidates that cache when
  the router route version changes.
- `FastAPI.openapi` has one resolved direct callee:
  `get_openapi` in `fastapi/openapi/utils.py:585-679`.
- `get_openapi` collects fields from routes and webhooks, produces model
  definitions, iterates route contexts, calls `get_openapi_path`, and emits
  OpenAPI paths, components, webhooks, tags, and external documentation.
- Search returned `tests/test_additional_properties_bool.py:test_openapi_schema`
  as a covering test candidate.

## Heuristic Or Incomplete Evidence

- The resolved `FastAPI.openapi` method had zero static callers. This is not a
  claim that the method is unused: FastAPI may invoke it through framework
  setup, route handlers, or dynamic dispatch that the static graph does not
  resolve.
- `FastAPI` also contains a second method named `openapi` at
  `fastapi/applications.py:1108-1118`; impact required a line selector to
  disambiguate the schema-generation method.
- The selected worktree had no partial parse failures and reported Python
  completeness `ok`, but it carries `W_WORKTREE_SNAPSHOT`. The result is a
  complete static view of this checkout, not a runtime trace.

## Claims Requiring Source Or Test Verification

- Which registered route combinations and Pydantic models produce correct
  OpenAPI output.
- Whether `test_openapi_schema` exercises the behavior relevant to a proposed
  change rather than merely a related schema case.
- Runtime route registration and endpoint invocation paths not represented by
  a direct static caller relation.

## Value And Limitation

Code-only Entire Graph quickly identified the implementation boundary,
schema-building callee, supporting helpers, and a covering test candidate. It
does not establish the product requirement for schema generation, declare a
test as an approval obligation, or prove runtime behavior. The next GPS stage
will add only explicit, reviewable requirement-to-symbol and
acceptance-to-test mappings.
