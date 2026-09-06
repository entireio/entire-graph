# Entire Graph With and Without GPS

GPS adds repository-local intent and verification evidence to Entire Graph. It
does not replace the code graph, change its stable symbol IDs, or add a hosted
service. The command remains `entire graph` and analysis remains local.

## Without GPS

Entire Graph provides code facts. It parses the selected repository and exposes
symbols, typed relations, source retrieval, diffs, impact analysis, snapshots,
and explicit test-command verification.

| Capability | Value |
| --- | --- |
| `search`, `def`, `neighbors`, `impact` | Locate code and understand callers, callees, types, and dependencies. |
| `snapshot`, `symbols`, `edges`, `index` | Export or reuse a deterministic semantic code graph. |
| `diff`, `commit`, `checkpoint` | Describe code changes and their graph-level blast radius. |
| `verify` | Run a caller-authorized test command and compare its observed result with a baseline. |

This answers questions such as "where is this implemented?" and "what code may
be affected?" It cannot, by itself, answer which product requirement a symbol
serves, whether a test is an approved requirement obligation, or whether a
change left authored intent stale.

## With GPS

GPS adds explicitly authored records and joins them to existing code-graph
facts. It answers "why does this code exist?", "which declared requirement and
test obligation are affected?", and "what evidence is missing?"

```text
Authored specification -> reviewed anchor -> code symbol -> graph relations
        |                       |                  |
 acceptance criteria       drift baseline      declared tests
        |                       |                  |
                 static check, context, review, and why
```

## Implemented GPS Features

| Area | Implemented feature | Added value |
| --- | --- | --- |
| Intent | Strict local YAML policy, specifications, requirements, acceptance criteria, relationships, decisions, anchors, and test mappings. | Makes intent reviewable and versioned alongside code. |
| Validation | Bounded safe file loading, unknown-field and alias rejection, reference checks, deterministic digests, and aggregate `spec validate` diagnostics. | Finds authoring errors without executing code or hiding invalid inputs. |
| Specification CLI | `spec init`, `list`, `show`, `validate`, and `relationships`. | Gives teams a small, explicit authoring workflow instead of generated or inferred specs. |
| Anchors | `anchor bind`, `list`, and `resolve`; stable symbol IDs plus versioned signature, container, body, and file fingerprints. | Creates reviewed links from requirements to implementations. |
| Drift | `VALID`, `CONTENT_DRIFT`, `STRUCTURAL_DRIFT`, `MISSING`, `AMBIGUOUS`, `CANDIDATE_REBIND`, and `UNVERIFIABLE` states. | A move, parser gap, or body edit is visible and never silently rebinding. |
| Context | Requirement matching, ranked code snippets, approved anchors, direct dependencies, declared tests, inferred test candidates, decisions, citations, quotas, and budget accounting. | Gives an agent a bounded explanation of what to inspect and why. |
| No-spec fallback | Code context remains available with `NO_SPECS`. | Existing code-only repositories retain useful behavior without adoption pressure. |
| Static checks | Anchor drift, missing/unresolved mappings, partial-analysis findings, and explicit dispositions. | Separates structural traceability from unproven runtime correctness. |
| Deltas | Pinned base/head checks, symbol-level implementation and test changes, deleted anchors/tests, removed mappings, spec-only, and code-only findings. | Identifies review obligations from semantic changes rather than file membership alone. |
| Capture integrity | Committed views are pinned to one commit/tree; changed inputs produce `GPS-INPUT-CHANGED` and an incomplete result. | Prevents clean conclusions assembled from mixed repository views. |
| Explanation | `why` joins symbols to requirements, mappings, and decisions; `review` summarizes changed files, obligations, and symbol deltas. | Makes code review evidence traceable to authored intent. |
| Intent-aware impact | `impact --intent` includes explicit bound anchor IDs. | Extends existing blast-radius analysis with requirement ownership. |
| Verification evidence | `verify` records repository, commit/tree, scope, parser, platform, timeout, intent digest, and policy digest. `check` and `review` project evidence as current, stale, failed, or unavailable without executing commands. | Preserves a strict execution boundary while making observed test evidence reviewable. |
| Verification policy | Optional strict `verification.yaml` defines approved scope and caller-supplied command metadata. | Prevents evidence from an unrelated command or policy from being treated as applicable. |
| Local provenance | Opt-in `why --history` returns bounded Git history and checkpoint-trailer evidence, or `HISTORY_UNAVAILABLE`. | Adds local context without treating history as proof or reading private sessions. |
| Fixtures and contracts | Token/auth demo, Git-backed integration scenarios, negative fixtures, and JSON golden contracts. | Keeps behavior reproducible as GPS evolves. |

## What GPS Does Not Claim

- A structurally valid mapping does not prove a requirement is implemented.
- An inferred test candidate does not fulfill a declared verification obligation.
- A passing runner result does not prove complete behavior or coverage.
- `context`, `check`, `review`, `why`, and `impact` do not execute repository
  commands.
- GPS does not send repository content, telemetry, or requests to a remote
  service.

## Typical Workflow

```sh
entire graph spec init --repo .
entire graph anchor bind --repo . --id ANCHOR-AUTH --symbol Authenticate --file auth.go
entire graph context --repo . --query "change token lifetime" --format json
entire graph check --repo . --head --base HEAD~1 --format json
entire graph why --repo . --symbol Authenticate --file auth.go --history --format json
```

Run tests only through explicit authorization, then project the resulting
baseline into review:

```sh
entire graph verify --repo . --scope auth --test "go test ./internal/auth" --record-baseline /tmp/auth.json
entire graph check --repo . --evidence /tmp/auth.json --format json
```

GPS therefore adds a reproducible intent-to-code-to-evidence path while Entire
Graph continues to provide the underlying code facts and graph traversal.
