# GPS for Entire Graph

Status: Incremental implementation; command details below describe the current local-only GPS behavior.

GPS (Graph Parsing System) is the design initiative for extending **Entire Graph**
from code intelligence into repository-local intent, context, and verification.
It is not a new product executable, parser, graph service, or replacement for Entire.
The public interface remains `entire graph`; the plugin remains `entire-graph`.

## Product Definition

Entire Graph connects explicit developer intent to the code that implements it,
the dependencies a change affects, and the evidence available to verify that
change. Agents receive a bounded explanation of **where to change code, why it
exists, what may be affected, and what still needs verification**.

```text
Developer-authored specifications and decisions
                      |
               Semantic anchors
                      |
         Existing Entire Graph code graph
                      |
       Context + intent-aware impact + test mapping
                      |
                 Agent changes
                      |
       Structural checks + explicit test evidence
                      |
             Developer review and acceptance
```

Structural consistency is not proof of behavioral correctness. A mapped test is
not measured coverage, a passing test is not proof of every requirement, and a
deterministic inference is still an inference.

## Document Map

| Document | Purpose |
| --- | --- |
| [MVP](MVP.md) | First end-to-end delivery, exclusions, milestones, and acceptance gates. |
| [Intent and Specifications](intent-and-specifications.md) | Authored YAML, requirements, acceptance criteria, relationships, and decisions. |
| [Anchors and Evidence](anchors-and-evidence.md) | Stable intent bindings, resolution, drift, provenance, and authority. |
| [Context and Impact](context-and-impact.md) | Request-to-context assembly, intent explanations, test selection, and blast radius. |
| [Verification](verification.md) | Static checks, test execution boundary, result contracts, and review. |
| [Feature Roadmap](feature-roadmap.md) | Comments, history, agent integration, MCP, storage, and external evaluation. |
| [Token/Auth MVP Demo](token-auth-demo.md) | Runnable fixture, Git-backed check contract, invalid-input diagnostics, and budget behavior. |
| [Feature Comparison](feature-comparison.md) | What Entire Graph provides with and without GPS, plus implemented GPS value. |

The documents deliberately separate a buildable first slice from later features.
Examples of new commands and schemas are **proposed contracts**, not current CLI
documentation. Existing commands retain their current behavior unless a future
implementation explicitly adds an opt-in mode.

## Committed Views and Provenance

GPS committed operations capture `HEAD` once and use that commit/tree for intent,
graph, and diff inputs. A subsequent `HEAD` movement produces an additive
`GPS-INPUT-CHANGED` incomplete finding rather than a clean result. Base/head GPS
checks compare symbol fingerprints joined to anchors and declared test mappings;
ordinary changed-file membership alone does not imply an implementation finding.
Deleted bound symbols remain reportable as deltas.

`entire graph why --history` is opt-in and reads at most 32 local Git commits for
the resolved symbol path. It exposes commit subjects and `Entire-Checkpoint`
trailers only. It never reads session transcripts, contacts a remote, or changes
code/spec conclusions. If that local projection cannot be read, its additive
history section reports `HISTORY_UNAVAILABLE`.

## What We Reuse

| Existing capability | GPS contribution |
| --- | --- |
| Local tree-sitter parsing, files, symbols, and typed relations | Join authored intent to existing records; do not create another parser. |
| `search`, `def`, `neighbors`, and ranked source context | Use them as retrieval primitives for intent-scoped context. |
| `impact`, semantic `diff`, `commit`, and `checkpoint` | Add explicit specification and verification consequences. |
| Test associations and derived verification commands | Distinguish declared mappings, inferred candidates, and executed results. |
| `verify` command execution and baseline comparison | Reuse the runner after adding scope and evidence integrity checks. |
| `snapshot`, `symbols`, `edges`, `index`, and derivative caches | Preserve code interchange and reuse graph construction. |
| `agent-guide` and `init-agents` | Extend the existing instructions when intent-aware commands ship. |

Today, `explain` resolves declarations mentioned in compiler or test errors; it
does not explain product intent. A future `why` command has that distinct purpose.
Existing search context assembly is useful infrastructure, not an existing
`context` command or a complete intent verification system.

## Ownership Boundaries

| System | Owns | Does not own |
| --- | --- | --- |
| Entire Graph | Facts from selected repository inputs; specs; anchors; deterministic joins; context; impact; checks; explicit verification artifacts. | Hidden conversational memory, a background graph service, hosted inference, or outbound telemetry. |
| Entire | Sessions, prompts, transcripts, tools, subagents, checkpoints, attribution, and rewind/resume. | The authoritative meaning of repository specifications. |
| Entire Brain | Durable memory, cross-repository reconciliation, learned feedback, and the MCP client-facing surface. | Replacing the provider's reproducible code facts or authored intent. |
| External evaluation tooling, including Databricks | Opt-in export ingestion, evaluation, analytics, MLflow traces, and governance. | Any dependency in the local parsing, context, or checking path. |

Repository-authored decisions belong in Graph because they are selected source
inputs. Decisions inferred from private conversations remain external proposals
until explicitly authored or supplied with provenance.

This follows the existing [Brain/Graph boundary](../brain-and-graph-boundaries.md).
In particular, GPS does **not** introduce `gps mcp`, a second provider MCP server,
a watcher, or a built-in Databricks client.

## Architectural Rules

1. Preserve the `entire graph` namespace and existing provider identity.
2. Reuse the code graph; model intent as separate records joined to code IDs.
3. Keep authored intent in Git-friendly files, never solely in a cache or database.
4. Keep analysis local and no-egress; no model, API key, or remote grammar is required.
5. Keep `compound-v1` code symbol identities unchanged.
6. Preserve the additive-only provider schema `1.x` contract and downstream consumers.
7. Report unresolved, ambiguous, unsupported, truncated, and partial results explicitly.
8. Never turn inferred relationships into approved intent without an explicit change.
9. Never run repository-supplied commands during search, context, impact, or static checking.
10. Bind every conclusion to selected code, specification, policy, and evidence inputs.

The same explicit inputs must produce the same semantic result. Timings and test
execution timestamps are operational metadata, not inputs to ranking or authority.

## Repository Layout

These documents live in `docs/GPS/`. The proposed runtime layout in a repository
using the feature is separate:

```text
.entire/
  graph-agent.md              # Existing generated activation guide
  graph/
    intent.yaml              # Versioned selection and check policy
    specs/                   # Human-authored YAML specifications
    anchors/                 # Reviewed anchor bindings and baseline fingerprints
    decisions/               # Authored decisions, introduced after MVP
```

No `.gps/` directory is introduced. Initialization is explicit and must preserve
Entire-owned files and `.entire/graph-agent.md`. It must not enable Entire session
recording, install hooks, or modify agent configuration as a side effect.

Indexes remain disposable derivatives under the existing cache policy. Context
plans and verification evidence are written only to paths explicitly requested
by the caller; they do not become hidden memory that changes future answers.

SQLite is not an MVP requirement. An optional derivative index can be considered
after measurement; it must not replace authored YAML or the existing snapshot
contract. See [the roadmap](feature-roadmap.md#storage-and-scale).

## Implementation Direction

Keep `cmd/entire-graph/main.go` thin and use the existing CLI dispatcher. Begin
with a small `internal/intent/` package for schema validation, bindings, and joins.
Reuse `internal/sem/` for parsing and retrieval and `internal/gitutil/` for selected
repository views. Extract shared CLI-private logic only when a second caller
actually requires it; do not duplicate impact traversal or test execution.

New intent/context/check payloads receive their own versioned contracts. The MVP
does not add intent records to the default code snapshot. Later interchange must
be opt-in, additive, and tested against the
[GA schema contract](../adr/0001-ga-schema-contract.md); experimental code-stream
relations must use `X-provider:RELATION` namespacing rather than redefine core types.

## Success Criteria

- An agent can connect a request to a declared requirement, a real symbol, and a test.
- A changed or deleted implementation produces an explainable check finding.
- Missing information stays visible instead of being reported as success.
- Existing code-only workflows work unchanged when no specifications exist.
- Everything needed for the MVP works without sessions, Brain, or cloud services.
- Evaluation measures correct changes and missed regressions, not just smaller context.

## Existing Contracts

- [Command reference](../commands.md)
- [Cache and operations](../operations.md)
- [Trust and security](../trust-and-security.md)
- [Agent activation](../agents.md)
- [Provider schema](../adr/0001-ga-schema-contract.md)
- [Brain and Graph boundaries](../brain-and-graph-boundaries.md)
