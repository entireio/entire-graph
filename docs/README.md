# Entire Graph documentation

These are the current project documents. `entire graph help` lists the public
command surface and documented flags; the command parsers define what the
binary accepts, including deliberately help-hidden compatibility and tuning
flags. Generated capability data is authoritative in
`entire graph capabilities --json`.

| Document | Purpose |
| --- | --- |
| [Root README](../README.md) | Installation, first use, common workflows, and project orientation |
| [README improvement plan](readme-plan.md) | Editable plan for the next root README revision |
| [Language support](language-support.md) | Current semantic and inventory-only language matrix |
| [Semantic provider requirements](semantic-provider-requirements.md) | Provider responsibilities, schema rules, profiles, relations, warnings, and limits |
| [Operations](operations.md) | Local installation and release archive behavior |
| [Benchmarks](benchmarks.md) | Reproduction commands, measurement contracts, results, and caveats |
| [Entire Brain and Entire Graph boundaries](brain-and-graph-boundaries.md) | Ownership decisions and explicit non-goals |
| [ADR 0001](adr/0001-ga-schema-contract.md) | Accepted `1.x` schema compatibility contract |
| [ADR 0002](adr/0002-committed-tree-cache-key.md) | Accepted committed-tree cache-key decision |

Completed plans, superseded references, branch diaries, and point-in-time proof
logs are listed in the [archive](archive/README.md). Archived documents are kept
for provenance and are not normative.

## Sources of truth

| Subject | Source |
| --- | --- |
| Public commands and documented flags | `entire graph help`, `entire graph <command> --help`, and `internal/cli/help.go` |
| Accepted arguments, including help-hidden flags | Command parsers under `internal/cli/` |
| Languages, profiles, and relation types | `entire graph capabilities --json` |
| Provider schema | [ADR 0001](adr/0001-ga-schema-contract.md) and `internal/sem/provider.go` |
| Committed-tree cache identity | [ADR 0002](adr/0002-committed-tree-cache-key.md) and the cache implementations under `internal/sem/` |
| Benchmark behavior | [Benchmarks](benchmarks.md), `bench/README.md`, and `cmd/graph-bench` |
