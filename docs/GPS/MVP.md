# MVP: Intent-Aware Context and Checks

Status: Proposed first delivery. Read the [overview](README.md) for boundaries.

## Outcome

Deliver one complete local workflow inside Entire Graph:

```text
Authored specification
        |
Bind a requirement to a code symbol and a test
        |
entire graph context --query "change the token lifetime"
        |
Agent inspects source and modifies code
        |
entire graph check --base HEAD
        |
Drift, affected requirements, and verification gaps
        |
Explicitly authorized tests and developer review
```

The first release establishes structural traceability and evidence integrity.
It does not claim to decide whether arbitrary natural-language requirements have
been implemented correctly.

## In Scope

| Feature | Minimum behavior |
| --- | --- |
| Specifications | Strict YAML parsing; unique IDs; requirements; identified acceptance criteria; declared anchors and test mappings. |
| Anchor binding | Explicit, reviewable binding to an existing symbol ID, with source locator and versioned fingerprints. |
| Resolution | Exact resolution; no-match and ambiguous states; move/rename candidates without automatic rebinding. |
| Drift | Detect body, structural, missing, and unverifiable states without changing the stored baseline. |
| Intent mesh | Join spec, requirement, acceptance criterion, anchor, symbol, and test records with provenance. |
| Context | Deterministic lexical retrieval plus existing graph expansion, source citations, bounded output, and missing-evidence notices. |
| Static checks | Validate authoring, references, binding resolution, changes, mappings, and analysis completeness. |
| Output | Human text and versioned JSON with machine-readable findings and repository-view identity. |
| Compatibility | Existing code-only commands and snapshots continue to behave as before. |

Use the existing semantic-language support. The demo may use a small Go fixture;
the specification format must not assume Go. Unsupported or inventory-only files
must be reported as unverifiable, not treated as deleted implementations.

## Out of Scope

- Rebuilding tree-sitter support, replacing the code graph, or introducing SQLite.
- Automatic spec generation, embeddings, LLM calls, or learned ranking.
- Source comment directives, decision extraction, and full specification traversal.
- Entire transcript ingestion, agent attribution, and historical rationale.
- A new MCP server, web dashboard, daemon, or cross-repository graph.
- Databricks, telemetry uploads, hosted analytics, or API keys.
- General behavioral proof, test coverage measurement, or automatic approval.
- Automated test execution by `context` or `check`.

## Proposed CLI Slice

All commands below are new proposals, not current commands. `--repo .` is explicit
so the workflow does not depend on an active Entire session.

```sh
entire graph spec init --repo .
entire graph spec list --repo . --format json
entire graph spec show --repo . --id SPEC-AUTH-001 --format json
entire graph spec validate --repo . --format json
entire graph anchor bind --repo . --id ANCHOR-AUTH-001 --symbol AuthService.authenticate --file src/auth/service.ts
entire graph anchor list --repo . --format json
entire graph anchor resolve --repo . --id ANCHOR-AUTH-001 --format json
entire graph context --repo . --query "Change token lifetime" --max-context-bytes 12000 --format json
entire graph check --repo . --base HEAD --format json
```

`spec init` creates only the documented intent layout and policy template. It does
not invent requirements. Specifications are initially authored with an editor;
`anchor bind` is an explicit write to a named binding document. It must refuse an
existing binding unless the caller explicitly requests an update.

New machine-oriented commands support `--format json` and `--json` as aliases.
Existing command formats are not silently changed. `check` is analysis-only;
`verify` remains the distinct, explicitly authorized execution surface.

## View and Baseline Rules

- New interactive commands read code and intent from the working tree by default.
- `--head` selects committed code and committed intent together, not a mixed view.
- `check --base <ref>` compares that resolved Git revision with the selected current view.
- Resolve refs once at command start and report their object IDs.
- Reuse repository selection and ignore rules; do not silently index ignored files.
- Report tracked/untracked inclusion policy and files excluded by limits.
- Anchor drift compares against the reviewed binding fingerprint; change impact compares against `--base`. These are distinct baselines.
- Working-tree queries rebuild and bypass persistent snapshot caches as today.
- Detect relevant file changes during capture and return `INPUT_CHANGED`, not a clean result.

If no base is supplied, `check` validates current structure and anchor drift but
reports change-delta analysis as not requested. If no specs exist, `context` falls
back to code context with `NO_SPECS`; `check` reports `NOT_CONFIGURED`, not passed.

## Delivery Steps

### M1: Authoring and Validation

Implement safe, bounded YAML reading, schema validation, local reference checking,
and deterministic digests. Add initialization, list, show, and validate commands.

Acceptance: valid documents round-trip semantically; duplicate keys and IDs,
unknown fields, unsupported versions, escaping paths, and dangling references
produce precise diagnostics. Read operations do not modify repository files.

### M2: Bindings and Drift

Resolve authored selectors through the existing symbol graph. Persist bindings
only through an explicit command. Compare signature/container/path and body
fingerprints separately and preserve unresolved candidates.

Acceptance: body edits report content drift; line-only movement does not break an
anchor; a moved symbol yields structural change or a proposed rebind; duplicate
candidates remain ambiguous; parse failure never masquerades as deletion.

### M3: Context

Join matched requirements and anchors to existing search hits, direct dependencies,
and declared or inferred tests. Render one versioned context package with citations,
reasons for inclusion, uncertainty, and a complete budget accounting.

Acceptance: a query finds the fixture's intended spec and code; the package does
not exceed its declared UTF-8 byte budget; omissions and absent mappings are
visible; repeated identical inputs produce identical semantic payloads.

### M4: Checks and Demo

Implement static findings and an aggregate disposition. Compare a selected base
when requested. Add a demo fixture, negative fixtures, and end-to-end CLI tests.

Acceptance: the demo detects a body edit, a removed anchored symbol, a missing
mapped test, a spec-only change, and an incomplete graph without executing code.
It never reports tests as passed merely because mappings exist.

## Demo Script

1. Use a fixture with token creation, authentication middleware, and two tests.
2. Author a specification with separate valid-token and expiry requirements.
3. Bind the implementing symbols and map each acceptance criterion to a test.
4. Run `context` for a token-lifetime change and inspect its cited source.
5. Modify the token lifetime without updating the expiry test or approved binding.
6. Run `check --base HEAD` and show content drift plus affected verification mappings.
7. Run the selected tests explicitly; show actual output separately from static findings.
8. Review the implementation, test results, and whether intent must change.
9. Update the binding only after review; rerun checks without hiding test gaps.

The demo succeeds when it exposes what needs review. An intentional change is
not automatically a bug, and updating a fingerprint is not evidence that a test passed.

## Release Gates

- Unit tests cover YAML validation, stable identifiers, fingerprint versions, and deterministic ordering.
- Integration tests exercise new commands in a temporary Git repository, including an unborn HEAD.
- Negative cases cover ambiguous symbols, unsupported files, ignored paths, malformed input, and partial graphs.
- View tests distinguish working tree, HEAD, base revision, spec-only edits, and dirty-tree capture.
- Security tests cover symlinks, Git administrative paths, input limits, and untrusted command text.
- Golden JSON tests pin each new schema and preserve existing snapshot compatibility.
- Rebuilding or deleting caches cannot lose authored intent or approved bindings.
- Analysis does not access the network, execute repository code, or rewrite authored inputs.
- Run focused tests during implementation and the repository's `mise run test` and `mise run check` before release.

## MVP Completion

MVP is complete when the workflow is usable end-to-end with truthful limitations,
not when every future CLI verb exists. Intent-aware execution evidence, comments,
history, and integrations have separate gates in the
[feature roadmap](feature-roadmap.md).
