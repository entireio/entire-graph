# Entire Graph README A+ Plan

Status: Draft for editing

## Objective

Turn the root README into a concise, credible landing page that lets a reader:

1. Understand what Entire Graph is and is not.
2. Install the prerequisite Entire CLI and the Entire Graph plugin.
3. Activate Entire Graph for a coding agent with explicit repository side effects.
4. Ask the agent a repository-source-read-only code question and verify that it
   used graph evidence before inspecting focused source.
5. Understand cache reuse and freshness, limits, data flow, and side effects.
6. Reach detailed documentation without searching through the repository.

This is a documentation-only project. It does not change CLI behavior, graph
semantics, benchmarks, releases, the plugin catalog, or integrations.

## Fixed assumptions

- Keep the root filename exactly `README.md`.
- At publication time, the official plugin index contains `graph` and a clean
  `entire plugin install graph` succeeds on every platform the README claims.
- At publication time, a released `init-agents` includes the topology-aware
  behavior merged in [#102](https://github.com/entireio/entire-graph/pull/102):
  when it confidently recognizes a standalone direct import from root `CLAUDE.md`
  to a distinct root `AGENTS.md`, `AGENTS.md` retains the guide pointer and
  `CLAUDE.md`'s managed block contains only the inheritance notice.
- At publication time, a released `init-agents` also includes the preflight behavior
  merged in [#105](https://github.com/entireio/entire-graph/pull/105): malformed,
  reversed, or duplicate managed markers are rejected before any activation file
  is created or changed.
- Entire CLI 0.10.0 or later is already available through its documented
  installation channels.
- Setup uses the console, but day-to-day code discovery and analysis happen
  through the coding agent in response to repository tasks.
- Direct CLI use remains available for debugging, automation, and downstream tools;
  it is not the primary user journey.

## Open decisions

- Which coding-agent clients have a tested instruction-loading and verification
  path that the root README may name?
- Which clean consuming fixture and pinned commit will produce the checked agent
  transcript?
- Which platforms will the installation example explicitly claim to support?
- Is any quantitative result relevant enough for the root README? Default to none
  unless one survives the claim audit.

## Guiding principles

- Make the recommended usage journey agent-first: setup installs and activates
  Entire Graph, then the coding agent performs graph queries during repository work.
- Disclose repository writes before asking the user to run `init-agents`, then
  demonstrate the workflow with a repository-source-read-only task.
- Prefer real commands, output, limits, and failure modes over adjectives.
- Keep one canonical home for each substantial topic.
- Scope every claim precisely; avoid absolutes such as “every,” “always,” and “zero” unless the contract proves them.
- Keep benchmark evidence visible but move methodology and historical corrections out of the landing page.
- Judge README length by usefulness, not an arbitrary line count.
- Preserve existing URLs where practical; use compatibility pointers when documents move.

## Competitor-informed requirements

The comparison with [Graphify](https://github.com/Graphify-Labs/graphify/blob/7fe58b0b0f3873be9a21c30106b8b8527c353aa6/README.md),
[codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp/blob/d150ebe4fc78a9a3f85013d2087a849e5d59eb0f/README.md),
[Mem0](https://github.com/mem0ai/mem0/blob/96d45b78c702b742fc91a2ce9eae91805be9144b/README.md),
[Graphiti](https://github.com/getzep/graphiti/blob/b2ff2eadd9a6b75a261a5cf0b19557883a13f752/README.md), and
[Supermemory](https://github.com/supermemoryai/supermemory/blob/82dae50ef458139823b3bfd3ebaaaac90ffd8a7c/README.md)
produces four requirements:

- Make a natural-language task—not a graph command—the first useful action.
- Show observable agent events, focused source evidence, and the resulting answer;
  never end the quickstart at “installed” or “done.”
- Separate installation, instruction loading, graph adoption, and answer grounding
  so users can verify each layer independently.
- Keep reference material out of the landing page and label direct console use as
  manual, diagnostic, or automation-oriented.

## Target root README structure

### 1. Hero and primary action

- One short category sentence, for example: "Entire Graph is a local
  static-analysis plugin that helps coding agents find definitions,
  relationships, and change impact in Git repositories."
- One supporting line can name tree-sitter and the main evidence types without
  forcing every capability and audience into the opening sentence.
- State the built-in analyzer’s no-egress boundary without implying that
  installation itself is offline.
- Make the primary journey explicit: install → activate for an agent → ask a
  code question → inspect the evidence.
- Link directly to installation, the first agent task, manual CLI use,
  documentation, and limitations.
- Avoid badge walls, slogans, release announcements, and unsupported superlatives.

### 2. Prerequisites and installation

- State that Entire Graph is an external command for the Entire CLI and that
  `entire` 0.10.0 or later must already be installed and on `PATH`. Version
  0.10.0 is the first stable Entire CLI release with remote name/URL plugin
  installation.
- Show the common macOS/Linux Entire CLI installation before the Entire Graph
  installation:

  ```sh
  curl -fsSL https://entire.io/install.sh | bash
  entire version
  ```

- Link other platforms and installation methods to the current
  [Entire CLI installation documentation](https://docs.entire.io/installation)
  instead of copying every platform command into this README.
- Do not require `entire enable`; it configures Entire session capture and is
  separate from dispatching the `entire graph` external command.
- Install the indexed plugin and verify command dispatch:

  ```sh
  entire plugin install graph
  entire graph version
  ```

  The install argument is `graph`, matching `entire-plugin.yml` and the command
  `entire graph`. Do not tell users to run `entire plugin install entire-graph`;
  a bare argument is a plugin-index name, not a repository or executable name.
- Move repository-URL installs, source builds, updates, pinning, migration, and
  platform-specific troubleshooting to operations documentation.

### 3. Activate Entire Graph for the agent

- State that activation is per repository. Before the command, disclose that it:
  - creates `.entire/graph-agent.md`, or replaces it in full on every successful
    rerun, so manual edits there are not preserved;
  - creates `AGENTS.md` and `CLAUDE.md` if absent, appends one managed block when
    an existing file has no Entire Graph markers, or replaces exactly one ordered
    managed block while preserving text outside it;
  - validates both instruction files before writing and refuses malformed,
    reversed, or duplicate Entire Graph markers, as well as non-regular targets,
    without changing any activation file.

  Then show the exact command from the repository root:

  ```sh
  entire graph init-agents --repo .
  ```

- Tell the reader to review the three generated or updated files. Recommend
  committing them as one setup change when the instructions should apply to the
  team. Make the cache consequence explicit: while any of these indexable
  Markdown files remains dirty or untracked, default working-tree queries bypass
  cache reuse for the repository.
- Link the guide emitted by `entire graph agent-guide`, but do not imply that every
  client imports the referenced file the same way.
- Require a fresh client task/session after activation. Use tested client-specific
  language: for example, a new Codex task rooted at the repository rather than a
  vague instruction to “restart.”
- Name only clients whose instruction discovery has been tested. The detailed
  client matrix must cover instruction precedence or shadowing, size limits,
  whether the referenced guide is imported or read on demand, and how a user can
  see that it loaded.
- Test the Claude Code layout where root `CLAUDE.md` has a standalone direct import
  of root `AGENTS.md`. The released topology-aware `init-agents` must keep the
  direct `.entire/graph-agent.md` pointer in `AGENTS.md`, put only its managed
  inheritance notice in `CLAUDE.md`, restore the direct Claude pointer if the
  standalone `CLAUDE.md` import of `AGENTS.md` is removed, and remain
  byte-idempotent on rerun.
- Keep configured-route hygiene distinct from observed client behavior. In the
  Claude Code 2.1.232 fixture, the documented
  [`InstructionsLoaded`](https://code.claude.com/docs/en/hooks#instructionsloaded)
  hook reported one resolved guide load for both the former two-route layout and
  the normalized control. Pin the client version and capture that load evidence;
  do not infer duplicated runtime content merely from configured routes.
- In `docs/agents.md`, document recovery from a marker-validation error: back up
  `AGENTS.md` and `CLAUDE.md`, preserve user-owned text, and reduce each file to
  zero raw Entire Graph marker tokens or exactly one complete begin-before-end
  pair before rerunning `init-agents`. Exact marker strings in examples or
  comments count. Also cover recovery from a non-regular instruction-file target.
  Document ordinary removal or reversal only if a supported procedure exists.

### 4. First agent task and observable verification

- Use a repository task in the user's own words, not an `entire graph` command,
  as the first-use example. The reusable prompt shape is:

  > Without changing repository source files, find where `<known feature>` is
  > implemented, identify what calls it or what changing it would affect, and
  > cite the relevant source.

- Capture the concrete transcript in a clean consuming fixture with no pre-existing
  Entire Graph instructions. Pin the fixture commit and commit the generated
  activation files before recording so the example is reproducible and does not
  confound instruction adoption with dirty-tree cache bypass.
- Show only visible events: the prompt, instruction-file discovery, the graph
  shell command and abbreviated result, the focused source read, any purposeful
  relation or impact query, and the sourced answer. Do not expose or fabricate
  hidden reasoning.
- Require graph search before broad repository scanning, not before instruction
  discovery or other client startup work.
- Separate the verification layers:
  - installation: `entire version` and `entire graph version` succeed;
  - activation: the three expected files exist, the generated guide matches
    `entire graph agent-guide`, their managed markers are intact, and the selected
    client reports or demonstrates that the guide loaded;
  - adoption: the fresh agent session visibly calls the graph before broad source
    exploration;
  - grounding: the answer is checked against focused source and, for a change,
    the narrowest relevant test.
- Do not hand-write plausible-looking output.

### 5. Common agent tasks and cache behavior

Use one small prompt-first table. Commands describe what the agent invokes; they
are not the primary call to action.

| Goal | Ask the agent | Graph operation |
| --- | --- | --- |
| Find code for a task | “Find where … is implemented.” | `search` |
| Understand one definition | “Show and explain the definition of …” | `def` |
| Inspect callers and relations | “What calls …?” | `neighbors` |
| Estimate a symbol’s blast radius | “What would changing … affect?” | `impact` |
| Review a commit or branch | “Summarize the semantic changes in …” | `commit` / `diff` |
| Export the graph | “Export the graph for …” | `snapshot` |

Follow the table with a short cache explanation:

- The interactive query family (`search`, `def`, `explain`, `neighbors`, and
  `impact`) reads the working tree by default, so current edits are visible.
  Whole-graph streams and ref-based analysis have different defaults; link their
  exact semantics instead of generalizing.
- Working-tree snapshot reuse is conservative and repository-wide. A dirty path
  with a supported extension, an extensionless path that could be a shebang
  script, or a root manifest used for import resolution bypasses reuse for that
  query. Other dirty paths with known unsupported extensions do not; `.graphignore`
  content is part of the cache key, and a changed committed tree selects a
  different entry.
- Cache writes are derivative local state, so “repository-source-read-only” does
  not mean “writes nothing.”
- The installed guide's default `search` output is JSON and exposes
  `index_cache_hit`. `--format agent` exposes a compact cache header. Do not claim
  that `search --format text` reports cache state.
- Link optional committed-tree prewarming, cache locations, overrides, and
  cleanup limitations to operations. State there that `index` warms only the
  committed-`HEAD` namespace, not the default working-tree namespace.
- Reconcile the cache-directory fallback differences in `def` and `explain`
  before making a universal cache-location claim.

Do not copy flag catalogs or cache-internals prose into the root README.

### 6. Agent flow, boundaries, and limits

Use one compact flow that makes the agent—not the console—the recommended interface:

```text
user prompt → coding agent → repository instructions → entire graph CLI
            → ranked graph evidence → focused source/test check → sourced answer
```

Add a concise boundary summary:

- Built-in analysis is local and no-egress; installation obtains software.
- Queries may write derivative caches, and `init-agents` writes the three disclosed
  repository instruction files.
- Static relations and dependent counts are not compiler-accurate. Calls, routes,
  tools, renames, dynamic dispatch, reflection, generated code, and runtime wiring
  can be heuristic or incomplete.
- Unsupported languages are inventory-only or produce explicit partial failures.
- Working-tree and committed-tree defaults differ by command family.
- Entire Graph is a one-shot CLI and semantic provider. Entire Brain is the
  separate durable consumer and MCP surface; Entire Graph is not a daemon,
  watcher, hosted memory system, or MCP server.

Link the full language, trust, command, and Brain/Graph boundary documents rather
than embedding a complete trust matrix in the landing page.

### 7. Documentation and manual use

- End with a short documentation map organized by user intent.
- Put direct CLI examples and automation in the command and operations references,
  labeled as manual or programmatic alternatives to the agent flow.
- Keep quantitative methodology, environments, corrections, and reproduction in
  `docs/benchmarks.md`. Include no root benchmark number unless it survives the
  claim audit and materially helps a new reader decide whether to continue.
- Include development, issue/support, and license links. Add a security link only
  if a real reporting policy exists.

## Document migration

Use this table as the canonical destination map; do not create two authorities
for the same subject.

| File | Action | Canonical role |
| --- | --- | --- |
| `README.md` | Rewrite in place; never rename | Agent-first landing page described above |
| `AGENTS.md`, `CLAUDE.md`, `.entire/graph-agent.md` | Regenerate or reconcile, then validate together | Current repository-agent instructions with every managed reference resolving, one intentional configured guide route in each tested client topology, and no stale limits or universal safety claims |
| `entire-plugin.yml` | Track as a separate release prerequisite | Product description consistent with agent-first repository intelligence rather than checkpoint-only positioning |
| `docs/README.md` | Retain and update | User-intent index and explicit archive boundary |
| `docs/commands.md` | Create | Task-oriented manual and automation reference; built-in help remains the exhaustive command surface |
| `docs/agents.md` | Create | Per-repository activation, client matrix, instruction discovery, fresh-session behavior, verification, updates, marker-validation and non-regular-target recovery, and supported removal |
| `docs/search.md` | Create | Ranking, result blocks, prose windows, passages, related sites, verification suggestions, and closed-set warnings |
| `docs/operations.md` | Expand | Install alternatives, upgrades, source builds, cache namespaces and locations, overrides, reports, status line, environment variables, migration, and troubleshooting |
| `docs/trust-and-security.md` | Create | Data flow, no-egress scope, local reads and writes, caller-provided command execution, transcript handling, and downstream boundaries |
| `docs/benchmarks.md` | Consolidate | Quantitative methodology, corrections, limitations, reproduction, and evidence links |
| `docs/snapshot-format.md` | Create | Native and compact NDJSON, compatibility, integrity checks, and `snapshot-query` |
| `docs/language-support.md` | Retain | Semantic and inventory-only language matrix |
| `docs/brain-and-graph-boundaries.md` | Retain and link | Ownership boundary between the repository-local provider and durable Brain/MCP surfaces |
| `docs/semantic-provider-requirements.md` | Rewrite and narrow | Graph owns repository-local parsing, indexing, queries, cache freshness, and instruction distribution; Brain owns durable and cross-repository state and presentation |
| `docs/adr/0001-ga-schema-contract.md` | Retain | `1.x` schema compatibility contract |
| `docs/adr/0002-committed-tree-cache-key.md` | Retain as historical decision; add a superseding ADR | Original committed-tree key and the newer safe clean-worktree reuse decision |
| `docs/archive/` | Retain | Non-normative provenance only |

Historical plans, validation reports, and implemented design records belong in
`docs/archive/` and must be labeled non-normative. Archive this plan after the
rewrite is complete and update the docs index at the same time.

## Command documentation reconciliation

- [ ] Extract the commands and flags used by the README and active references
  from the current CLI help registry.
- [ ] Compare the help registry with accepted parser flags and classify every
  mismatch as public, internal, compatibility-only, or a bug before calling help canonical.
- [ ] Mark `analyze` as an alias rather than a separate concept.
- [ ] Group documented commands by task: setup, inspect, analyze, and export.
- [ ] Keep full descriptions for supported public flags in `entire graph
  <command> --help`; document internal or compatibility-only flags separately if they must remain accepted.
- [ ] Check examples against the current binary.

## Benchmark and claim policy

Create a small claim ledger during implementation:

| Claim | Exact scope | Product version or commit | Corpus/environment | Evidence link | Root README? |
| --- | --- | --- | --- | --- | --- |
| Local/no-egress analysis | Built-in analyzer only | Pinned release | Not applicable | Implementation, contract, and tests | Yes |
| Cache reuse and freshness | Clean working tree, dirty supported-extension or extensionless path, dirty resolver manifest, dirty non-manifest path with a known unsupported extension, changed `.graphignore`, and explicit `--head` are distinct cases | Pinned release | Deterministic repository fixture | Cache tests and ADR | Yes |
| Language counts | Semantic versus inventory-only | TBD | Generated capabilities | Language reference | Maybe |
| Code-query quality | TBD after audit | TBD | TBD | Reproducible result | Maybe |
| Prose-memory comparison | Public-protocol reimplementation | Pinned | LOCOMO/LongMemEval-S | GraphMark bundle | Docs only by default |
| Status-line savings | Local heuristic, not a counterfactual benchmark | Current | Local transcript | Methodology | Docs only |

Rules:

- No number without its scope and evidence.
- No benchmark result from a managed or different product implied to describe this repository.
- No competitor comparison without pinned versions, matching interfaces, common workloads, and visible limitations.
- Preserve historical corrections in the canonical benchmark document, not the landing page.
- Do not introduce a new ranking as part of the README rewrite.

## Editorial anti-slob checklist

- [ ] Remove “This release is for your agents.”
- [ ] Remove unsupported time promises such as “one minute” or “30 seconds.”
- [ ] Replace “every” and “always” with the supported scope.
- [ ] Remove “That’s it” and similar filler.
- [ ] Remove promises about eliminating future turns or grep.
- [ ] Remove generic adjectives such as powerful, seamless, intelligent, robust, cutting-edge, and production-ready unless directly substantiated.
- [ ] Avoid feature-name-plus-colon bullets when a concrete sentence is clearer.
- [ ] Avoid emoji-led headings and decorative badge walls.
- [ ] Avoid rhetorical questions and faux quotations.
- [ ] Use “Entire Graph” for the product and `entire graph` for the command consistently.
- [ ] Distinguish Entire Graph from Entire Brain consistently.
- [ ] Keep paragraphs short and concrete.
- [ ] Ensure each substantial fact appears in one canonical location.
- [ ] Perform a final human copy edit after automated checks.

## Implementation phases

### Phase 1: Establish sources of truth

- [ ] Resolve the four open decisions near the top of this plan.
- [ ] Publish and index an Entire Graph release containing
  [#102](https://github.com/entireio/entire-graph/pull/102) and
  [#105](https://github.com/entireio/entire-graph/pull/105), pin that release for
  activation documentation, and verify the installed binary—not a source build—
  has both behaviors before documenting them.
- [ ] Smoke-test `entire plugin install graph` and both version commands in a
  clean environment on each claimed platform.
- [ ] Inventory the commands and flags used by active documentation and verify
  their defaults against the current binary.
- [ ] Verify the complete per-client activation chain: generated files,
  instruction precedence/import behavior, fresh-task requirement, every managed
  reference resolving, one intentional configured guide route in each tested
  topology, and visible evidence that the client loaded the guide once.
- [ ] Select a clean consuming fixture, pin its commit, install and commit the
  generated instruction files, and record a source-grounded agent task.
- [ ] Verify cache behavior for a clean working tree; a dirty supported-extension,
  extensionless, or resolver-manifest path; a dirty non-manifest path with a known
  unsupported extension; a changed `.graphignore`; explicit `--head`; and
  committed-tree prewarming. Record the exact cache fields or headers exposed by
  the formats used in the README and agent guide.
- [ ] Build the quantitative claim ledger and audit local reads, writes,
  execution, cache, and network boundaries against implementation and tests.
- [ ] Reconcile stale documentation authorities before drafting: repository-agent
  instructions, provider/Brain ownership text, and cache ADRs. Record the plugin
  metadata correction as a separate release prerequisite.

### Phase 2: Build canonical detailed docs

- [ ] Apply the document migration table above and update `docs/README.md` as
  destinations become authoritative.
- [ ] Create the task-oriented command, agent, search, trust, and snapshot-format
  references.
- [ ] Expand operations with install alternatives, source builds, cache details,
  status-line behavior, migration, and troubleshooting.
- [ ] Rewrite `docs/semantic-provider-requirements.md` so its ownership boundary
  matches the repository-local query, cache, and agent-instruction behavior that
  Entire Graph actually implements.
- [ ] Add a superseding cache ADR without rewriting ADR 0002's historical decision.
- [ ] Consolidate benchmark content and preserve archived material as explicitly
  non-normative provenance.
- [ ] Reconcile `AGENTS.md`, `CLAUDE.md`, and `.entire/graph-agent.md` together;
  leave every managed reference resolving, one intentional configured guide route
  in each tested client topology, and no stale limits or unsafe absolutes.

### Phase 3: Rewrite the root README

- [ ] Implement the seven-section target structure above in `README.md` without
  renaming the file.
- [ ] Use the checked installation, activation, agent transcript, cache fields,
  and source citations from Phase 1; do not invent output.
- [ ] Keep direct CLI detail, cache operations, the full trust model, formats,
  and benchmark methodology in their canonical references.

### Phase 4: Editorial and verification pass

- [ ] Run each documented command and safe shell block against the pinned release
  and fixture.
- [ ] Compare documented commands and flags against the help registry and parsers.
- [ ] Validate relative links and heading anchors from each containing file.
- [ ] Check Markdown rendering on GitHub, including narrow/mobile widths.
- [ ] Audit every number against the claim ledger.
- [ ] Search for duplicated authorities, withdrawn claims, stale cache language,
  and the old `entire-sem` name outside historical context.
- [ ] Run spelling, grammar, Markdown, and repository checks.
- [ ] Complete a manual anti-slob copy edit.

## Acceptance criteria

- [ ] A new reader can identify the product category, input, output, audience, and principal boundary from the first screen.
- [ ] The first-run sequence installs and verifies the Entire CLI, installs and
  verifies Entire Graph, discloses and performs agent activation, then
  demonstrates useful output through a repository-source-read-only agent task.
- [ ] In a clean environment, `entire plugin install graph` resolves through the
  default index and makes `entire graph version` succeed.
- [ ] The root README never uses `entire-graph` as a bare plugin-index name and
  never presents `--yes`, `--force`, `@main`, or a local build as the recommended
  first install.
- [ ] Activation is explicitly per repository, shows the exact command, discloses
  all three file effects—including full replacement of `.entire/graph-agent.md`
  on successful reruns—before execution, and tells the user how to review and
  optionally commit the result.
- [ ] The installed, indexed pinned release's `init-agents` fails without
  activation-file writes on malformed, reversed, or duplicate managed markers or
  non-regular instruction-file targets, and `docs/agents.md` gives the supported
  recovery procedure for both.
- [ ] Using that same release, each named client has a tested fresh-task/session
  path and an observable check that the generated guide loaded once without
  shadowing or a dangling reference, and an `init-agents` rerun preserves its
  intended configured route.
- [ ] The pinned fixture transcript shows visible instruction discovery, graph
  use before broad source scanning, focused source inspection, a purposeful
  relation or impact query, and a sourced answer.
- [ ] Example output is captured from the pinned binary and fixture, not invented.
- [ ] The README scopes working-tree defaults to the interactive query family and
  explains clean reuse, repository-wide bypass for dirty supported-extension,
  extensionless, or resolver-manifest paths, continued cache eligibility for
  other dirty paths with known unsupported extensions, `.graphignore` keying,
  derivative cache writes, and the exact observable cache field/header used in
  the example.
- [ ] `index` is not presented as warming the default working-tree agent path.
- [ ] Trust documentation distinguishes built-in analysis, installation network
  activity, derivative cache writes, repository instruction writes, and
  caller-provided command execution.
- [ ] Limitations cover heuristic analysis, inventory-only languages, partial
  failures, dynamic-code gaps, and command-family tree semantics.
- [ ] The root README contains a prompt-first task map; commands are labeled as
  agent internals or manual/debugging interfaces.
- [ ] Benchmark claims retain their scope, versions, corpora, caveats, and evidence links.
- [ ] Withdrawn figures do not appear on the landing page.
- [ ] Each substantial topic has one canonical home.
- [ ] All local links and heading anchors resolve.
- [ ] All safe runnable examples pass against the documented version.
- [ ] No unsupported time promises, absolutes, generic superlatives, repeated slogans, or decorative clutter remain.
- [ ] A human copy edit finds no obvious generated filler, contradictions, or terminology drift.
- [ ] The root file is still named `README.md`.

## Non-goals

- CLI or product behavior changes.
- New benchmark runs.
- New competitor rankings.
- Publishing release assets, changing the external plugin index, or updating
  plugin metadata; those are assumed or separately tracked release work.
- A comprehensive flag reference in the root README.
- An arbitrary README line-count target.
- Adding an MCP server, daemon, watcher, hosted memory product, or visualization UI.
- Rewriting embedded agent doctrine or making further `init-agents` behavior
  changes. Regenerating the released guide and managed blocks is in scope; if the
  documentation audit exposes another format or import mismatch, track that
  product change as a prerequisite rather than documenting behavior that does not exist.
- Rewriting historical ADRs, archived plans, or immutable benchmark evidence. A
  new superseding cache ADR is in scope because current behavior has changed.
