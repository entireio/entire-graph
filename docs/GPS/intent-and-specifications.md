# Intent and Specifications

Status: Proposed design. See the [GPS overview](README.md).

## Purpose

The intent layer is an explicit, versioned repository input. It captures what
the software is expected to do and connects that expectation to implementation
and verification evidence. It is not a generated description of the code.

Code can help discover candidate specifications, but generated candidates remain
proposals until a developer authors or accepts them in a specification document.

## Authoritative Documents

The first format uses strict YAML under `.entire/graph/specs/` and a selection
policy at `.entire/graph/intent.yaml`.

```text
.entire/graph/
  intent.yaml
  specs/
    authentication.yaml
  anchors/
    authentication.yaml
```

Document identity is the `id`, not its filename. The file path is a locator for
humans and Git history. A document is selected only when it is under the
configured root and passes validation.

`intent.yaml` initially declares only schema version, document roots, and safe
size limits. It must not contain commands, URLs to fetch, executable hooks, or
personal state.

## Specification Schema

This is the MVP shape. Unknown keys are errors so a typo cannot silently drop a
requirement. Schema versions are explicit; adding a field requires a documented
migration rule.

```yaml
version: 1
id: SPEC-AUTH-001
title: User authentication
intent: Users can authenticate securely and receive an access token.

requirements:
  - id: REQ-AUTH-INVALID
    description: Invalid credentials are rejected.
  - id: REQ-AUTH-TOKEN
    description: Valid credentials produce an access token.

acceptance:
  - id: ACC-AUTH-INVALID
    requirement: REQ-AUTH-INVALID
    description: Invalid credentials return an authentication error.
  - id: ACC-AUTH-TOKEN
    requirement: REQ-AUTH-TOKEN
    description: Valid credentials produce a token accepted by protected routes.

anchors:
  - id: ANCHOR-AUTHENTICATE
    requirement: REQ-AUTH-TOKEN

tests:
  - id: TEST-AUTH-INVALID
    acceptance: ACC-AUTH-INVALID
    selector:
      name: TestAuthenticateRejectsInvalidCredentials
  - id: TEST-AUTH-TOKEN
    acceptance: ACC-AUTH-TOKEN
    selector:
      name: TestAuthenticateReturnsAccessToken
```

The requirement identifiers are unique within a specification. Acceptance,
anchor, and test IDs are globally unique in the selected intent set. A test
selector identifies an expected test symbol or runner test ID; it is not test
source code and does not grant execution permission.

Every requirement needs a description. An empty `requirements` list is invalid
for an implemented specification. An initial design-only specification can set
`status: proposed`; checks then report the lack of anchors rather than treating
it as a failure of shipped behavior.

## Relationships and Decisions

After the MVP, specifications can add explicit relationship declarations:

```yaml
relationships:
  - type: depends_on
    target: SPEC-SESSION-002
  - type: supersedes
    target: SPEC-AUTH-000
```

Allowed types are `parent_of`, `depends_on`, `related_to`, `supersedes`, and
`conflicts_with`. Relationship edges are authored evidence with `confidence: 1`.
Cycles are permitted only for `related_to`; directional relationship cycles are
validation errors unless a future type explicitly permits them.

Decisions are separate documents under `.entire/graph/decisions/`. They may
reference specs and anchors, but must not silently rewrite requirements.

```yaml
version: 1
id: ADR-017
title: Rotate refresh tokens
decision: Refresh tokens are replaced on every successful refresh.
reason:
  - Limits replay value.
affects:
  - SPEC-AUTH-001
  - SPEC-SESSION-002
anchors:
  - ANCHOR-TOKEN-REFRESH
```

`entire graph why` can later cite a decision only when the reference is explicit.
Git co-change and Entire checkpoints are historical evidence, not an implicit
decision record.

## Selection and Validation

Validation occurs before graph joining:

1. Resolve the repository root and select a single code/intent view.
2. Enumerate regular YAML files beneath configured roots without following unsafe paths.
3. Parse with aliases, duplicate keys, depth, document count, and byte limits controlled by policy.
4. Validate schema version, required fields, ID uniqueness, local references, and cross-document references.
5. Canonicalize records by ID and compute an intent-set digest.
6. Return documents plus all diagnostics; a bad document never disappears silently.

Duplicate mappings may be permitted when several tests verify one acceptance
criterion. An acceptance criterion may not name a missing requirement. A test,
anchor, decision, or relationship reference to an unknown ID is invalid.

The JSON result includes the code view object, intent-file blob identities,
selection policy digest, canonical intent-set digest, and diagnostics. Context,
impact, and verification payloads include these values so their conclusions are
reproducible.

## Authority and Evidence

| Fact | Source | Authority |
| --- | --- | --- |
| Requirement text | Authored spec YAML | Developer-confirmed intent |
| Specification relationship | Authored spec YAML | Developer-confirmed intent |
| Decision text | Authored decision YAML | Developer-confirmed rationale |
| Requirement-to-anchor link | Authored spec YAML | Declared traceability |
| Anchor-to-symbol binding | Reviewed anchor document | Approved implementation link |
| Call/import/test association | Semantic provider | Observed or inferred code evidence |
| Candidate spec or link | Tool or model proposal | Non-authoritative suggestion |
| Test execution result | Explicit command execution | Runtime evidence for a declared scope |

GPS must preserve these distinctions in text and JSON. A graph edge with high
static confidence does not become a specification relationship. A requirement
with no approved binding remains an explicit gap.

## Future Authoring Assistance

`entire graph spec discover` may later emit candidate specs or links from source
comments, tests, names, and historical changes. Its output must include evidence,
confidence, and a `proposed` state. It never writes YAML, creates anchors, or
changes check outcomes without an explicit developer command.

Source comment directives are also a later feature. The proposed grammar is:

```text
@entire.graph.spec SPEC-AUTH-001
@entire.graph.implements REQ-AUTH-TOKEN
@entire.graph.decision ADR-017
@entire.graph.verify TEST-AUTH-TOKEN
```

Comments are references that reinforce or propose a YAML relationship. YAML and
reviewed bindings remain authoritative because comments can move, be copied, or
occur outside a parsed symbol. Parsers must capture directive locations as
evidence and reject malformed directives rather than guessing.
