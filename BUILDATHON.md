# BUILDATHON.md — DevCheck

## Project Name

**DevCheck**

## One-Sentence Summary

DevCheck is a local MCP tool that uses Entire Graph to analyze code change impact and produce evidence-based change-safety reports.

## Problem and Intended User

**Problem:** When a developer is about to change a function or file, they need to know what else might be affected, why, and which tests to run. Manually tracing call chains, type dependencies, and data flows across a codebase is error-prone and time-consuming.

**Intended User:** Any developer using an AI coding assistant (via Antigravity, Claude Code, etc.) who wants to make safer code changes with evidence-backed impact analysis.

## Selected Entire Track

**Track 2: Build with Graph Intelligence**

## Why Entire Graph Is Essential

Without Entire Graph, DevCheck cannot reliably discover code relationships. Static text search (grep) misses:
- Transitive callers and callees
- Type consumers and data flows
- Co-change file patterns
- Symbol-level definition details

Entire Graph provides **structural understanding** — callers, callees, type consumers, data flows, co-change files, and symbol definitions — that DevCheck turns into a readable, evidence-based change-safety report.

## Architecture and Main Workflow

```
User prompt → Antigravity → DevCheck MCP Server
                                │
                    ┌───────────┼───────────┐
                    │           │           │
              scan_project  check_change_impact
                    │           │
                    ▼           ▼
               graph.ts     graph.ts
              (version,    (search, impact,
               doctor)      def, diff)
                    │           │
                    ▼           ▼
              report.ts     report.ts
             (scan fmt)   (impact fmt)
                    │           │
                    └───────────┘
                        │
                   Formatted Report
```

### Key Design Decisions

1. **Separation of concerns:** `graph.ts` wraps CLI commands with no interpretation. `report.ts` formats and interprets. `server.ts` orchestrates.
2. **Evidence, not truth:** All output uses hedging language ("may be affected") and includes raw Graph evidence so developers can verify.
3. **Read-only:** DevCheck inspects code but never modifies it.
4. **Graceful degradation:** When Graph is unavailable or returns incomplete data, the report says so clearly and recommends manual review.

## Entire Graph Findings and Verification

### Current evidence status (truthful as of Codex review)

The DevCheck code is ready to execute Graph queries, but this workspace cannot yet produce submission evidence: the `entire` CLI was not available on PATH during review, and `C:\Users\Anupam\Desktop\antigravity projects\Scalar` was not a Git repository. Therefore, no Graph search, definition lookup, impact analysis, semantic diff, or Entire checkpoint is claimed in this document.

### Evidence to collect before submission

Once Entire is installed and the workspace is initialized as a Git repository, use the installed CLI's `--help` output to confirm the current Graph command syntax, then record:

1. A Graph search or definition lookup for a real DevCheck symbol, with the raw output and the matching source location.
2. A Graph impact analysis before a meaningful change, with the raw output and source/test verification of the reported relationship.
3. A final Graph semantic-diff analysis between the pre-change and final commits, with the raw output and a comparison against Git's file diff.

Do not replace these placeholders with inferred or sample results.

## Noon Curveball

_To be filled in during the buildathon when the curveball is announced._

**What changed:** Pending the buildathon announcement.

**How we adapted:** Record only the actual response after the announcement, including the relevant commit/checkpoint reference.

## Checkpoint Links

| Checkpoint | What It Proves |
|---|---|
| Baseline review | Pending — document existing purpose, architecture, working behavior, risks, and that Codex reviewed earlier Antigravity work; do not describe it as an Antigravity transcript |
| Pre-Curveball stable state | Pending — commit after Entire is enabled and Graph evidence is captured |
| Curveball response | Pending — commit only after the actual announcement and adaptation |
| Final implementation | Pending — commit after final verification and semantic-diff evidence |

## Setup, Run, and Test Instructions

### Prerequisites

- Node.js v18+
- Git repository
- Entire CLI installed and verified with `entire version`
- Entire enabled for Codex (`entire enable -y --agent codex`)
- Entire Graph installed and initialized using the commands shown by the installed CLI's help

### Build and Test

```bash
cd devcheck
npm install
npm run build
npm test
```

### Run the MCP Server

The server is configured in `.agents/mcp_config.json`. Restart Antigravity to load it.

Then use it in chat:

```
Use devcheck scan_project to check this project
Use devcheck check_change_impact to analyze formatImpactReport
```

### Verify Graph Findings (required before submission)

1. Open the source file referenced in the report
2. Confirm the symbol exists at the reported location
3. Confirm the reported callers/dependents actually reference the target
4. Run the recommended tests

## Known Limitations

- **Language coverage:** Entire Graph's language support determines which symbols are indexed. Unsupported languages produce empty results.
- **Dynamic dispatch:** Graph uses static analysis. Runtime-only connections (reflection, eval, dynamic imports) are not captured.
- **First-run latency:** The first query against a repository may be slow due to cache building.
- **Evidence quality:** Graph evidence is structural, not semantic. It shows what _could_ be affected, not what _will_ break.
- **Current environment:** Entire CLI and Git repository setup must be completed before Track 2 evidence and checkpoints can be collected.

## Next Steps

- Add `entire graph diff` integration for post-change semantic diff reports
- Add `entire graph verify` integration to run tests and track state changes
- Support for file-path targets (not just symbol names)
- Richer output formatting (collapsible sections, severity levels)
- Integration tests against a known reference repository
