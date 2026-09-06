# GPS Feature Roadmap

Status: Proposed sequencing. The [MVP](MVP.md) is the only committed first slice.

## Principle

Build the intent-to-code loop before adding integrations. Each phase must retain
no-egress deterministic behavior for its local functionality and must prove its
own reliability with fixtures, integration tests, and schema contracts.

| Phase | Outcome | Depends on |
| --- | --- | --- |
| 0 | Intent-aware context and static checks | Existing code graph and MVP documents. |
| 1 | Rich traceability and specification navigation | Phase 0 stable schemas and bindings. |
| 2 | Entire-aware provenance | Phase 1 and an explicit Entire adapter contract. |
| 3 | Agent delivery and CI workflows | Stable context/check payloads. |
| 4 | External evaluation and learning | Opt-in exported events and measurable outcomes. |

## Phase 0: Deterministic Core

Deliver the MVP:

- Strict YAML specifications, requirements, acceptance criteria, and declared tests.
- Reviewed code-symbol bindings and drift detection.
- Evidence and authority model.
- `spec`, `anchor`, `context`, and `check` command slice.
- Intent-aware impact as an opt-in extension after static correctness is proven.
- Versioned JSON, source citations, deterministic context budgets, and fixtures.

Exit gate: an end-to-end demo exposes modified implementation, broken anchors,
missing mappings, and graph incompleteness without network or test execution.

## Phase 1: Intent Navigation

Add these only after authoring quality is good enough to make them useful:

- `entire graph why <symbol>` for explicit spec, requirement, decision, and test links.
- `entire graph spec relationships` and `spec search` over authored content.
- Explicit spec graph edges: parent, dependency, related, superseding, and conflicts.
- Multi-anchor and endpoint/configuration anchor schema, with reviewed migration.
- Source comment directives for `spec`, `implements`, `depends`, `decision`, and `verify`.
- `spec discover` that emits evidence-backed proposals but never authoritative links.
- `review` as a structured diff-to-intent projection.

Exit gate: comment parsing is language-aware and citation-backed; rejected or
malformed directives cannot alter an approved relation. All new joins declare
whether they are authored, observed, inferred, or proposed.

## Phase 2: Entire Provenance Mesh

Entire remains the authority for session facts. Add a narrow adapter rather than
copying transcripts, checkpoints, or mutable agent state into the provider.

Candidate capabilities:

- `history` and `blame` projections that correlate symbols with commits and supplied checkpoint IDs.
- Explicit links between an authored decision and an Entire checkpoint.
- Historical context in `context` and `impact`, marked as historical evidence.
- Agent attribution when the caller supplies an Entire-verified record.
- `checkpoint` display that joins a known code revision to intent and anchor state.

Constraints:

- No requirement is derived automatically from an agent prompt.
- No agent identity is inferred from Git author data alone.
- Local code/spec results remain valid without Entire being installed or logged in.
- Entire session data and Brain memory are not caches inside Entire Graph.

Exit gate: disconnecting Entire removes only history sections and produces an
explicit `HISTORY_UNAVAILABLE` notice; it cannot change code/spec conclusions.

## Phase 3: Agent and CI Delivery

Use existing `agent-guide` and `init-agents` to deliver workflow guidance after
the commands exist:

```text
Before changing unfamiliar code:
1. Run `entire graph context --query "..."`.
2. Inspect cited specs, requirements, code, and gaps.
3. Run `impact --intent` for a behavior change.
4. Modify code and relevant specifications deliberately.
5. Run `check` and explicitly authorized verification.
6. Review all incomplete and review-required findings.
```

Possible distribution work:

- Host-specific activation templates for OpenCode, Claude Code, Codex, Cursor, Copilot, and Gemini CLI.
- CI invocation that writes JSON/artifacts to caller-selected paths and uses a pinned base revision.
- Pull-request annotation in an external integration that consumes the JSON; no provider network client.
- Read-only `agent-help` additions for automated discovery of the new commands.

MCP remains an **Entire Brain** concern. Brain may expose `entire graph` context,
why, impact, and check results as MCP tools by shelling out or consuming stable
JSON. Entire Graph does not add `gps mcp`, own MCP lifecycle, or run a server.

Exit gate: an agent can use the guidance with no direct MCP support, and CI can
distinguish `FAIL`, `REVIEW_REQUIRED`, and `INCOMPLETE` without parsing text.

## Phase 4: External Evaluation

Databricks or equivalent systems are opt-in consumers outside the critical path.
The provider can export caller-requested, redacted event files; a separate
integration is responsible for transport, MLflow traces, governance, and
analytics.

Potential event fields:

```json
{
  "event": "entire_graph_context_generated",
  "event_version": 1,
  "repository_pseudonym": "caller-defined",
  "request_digest": "sha256:...",
  "intent_digest": "sha256:...",
  "selected_spec_ids": ["SPEC-AUTH-001"],
  "selected_symbol_ids": ["compound-v1:..."],
  "risk": "high",
  "context_bytes": 8120,
  "result_status": "complete_with_gaps"
}
```

Do not export source text, access tokens, prompts, private repository identity,
or Entire session content by default. Event collection needs documented consent,
redaction, retention, and deletion policy. Offline failures must never block
local `search`, `context`, `impact`, `check`, or `verify`.

Evaluation questions:

- Did intent-aware context improve merged-change correctness and review findings?
- Which gaps and drift states precede avoidable regressions?
- Are inferred test or dependency links accurate enough to show as candidates?
- Does context reduce irrelevant exploration without reducing task resolution?
- Which recommendations are ignored, accepted, or overturned by review?

Any learned ranking or risk suggestion returns to Entire Graph only as an explicit,
versioned query input. It is never hidden user history and never changes the
authority of a spec or anchor.

## Storage and Scale

Initial source of truth:

| Data | Storage | Properties |
| --- | --- | --- |
| Specifications, decisions, anchors, policy | Versioned YAML in repository | Human-readable, reviewable, survives cache deletion. |
| Code graph | Existing snapshot/record streams and caches | Rebuildable derivative facts. |
| Context/check result | JSON emitted to stdout or an explicit output path | Scoped to named code and intent inputs. |
| Verification result | Explicit caller-selected evidence file | Provenance-bound execution evidence. |

SQLite can be introduced only as a rebuildable local acceleration index after
profiling shows the YAML join is a bottleneck. It must contain input digests and
be invalidated on schema, policy, code-view, or intent-view changes. It cannot
be the sole copy of developer intent, silently influence ranking across runs, or
create a daemon requirement.

The default provider snapshot remains code-focused. A future opt-in intent export
must use additive records or a separate versioned stream and must preserve the
`1.x` compatibility contract for existing consumers.

## Deferred Ideas

These ideas are worthwhile only after evidence supports their cost:

- LLM-assisted requirement extraction and semantic relationship proposals.
- Natural-language spec similarity beyond deterministic lexical search.
- Manual verification checklists and external test-case management links.
- API contract, database migration, infrastructure, and runtime configuration semantics.
- Organization-wide policy packs and multi-repository intent views.
- Visual graph experiences and long-running watch services.
- Automatic anchor repair, automatic test selection, or automatic approvals.

Every deferred feature must answer: does it preserve local determinism, retain
provenance, keep intent reviewable in Git, and leave the no-egress core usable
when integrations are unavailable?
