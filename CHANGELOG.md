# Changelog

All notable changes to Entire Graph are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Entries describe user-visible behavior. Nightly prereleases
(`vX.Y.Z-nightly.*`) are snapshots of the development branch and are not listed
here. Releases before 0.5.0 have no hand-written entry: their contents are the
auto-generated notes on
[the releases page](https://github.com/entireio/entire-graph/releases).

## [Unreleased]

### Fixed

- Partial clones (`--filter=blob:none`, or any repository with a promisor
  remote) are no longer refused outright when the Git that runs in them is
  2.45.1 or newer (or, on an older release line, 2.39.4+, 2.40.2+, 2.41.1+,
  2.42.2+, 2.43.4+ or 2.44.1+).
  `index --head` used to fail on them and every query rebuilt an uncached
  working-tree snapshot; now `index --head`, `query --head`, `impact`,
  `neighbors`, `def`, `context`, `store`, `health` and `snapshot` read the
  committed tree and reuse its cache. Every Git subprocess sets
  `GIT_NO_LAZY_FETCH=1`, which those releases honour for every lazy fetch, so
  the promisor remote is never contacted. What still needs a blob the clone
  lacks fails or degrades instead of fetching it: a missing HEAD file is
  reported as an unreadable file (`E_FILE_READ`); `diff` and `commit` fail
  when rename detection needs a missing blob (for example
  `commit HEAD` on a commit that renames and edits a file); and co-change
  (`FILE_CHANGES_WITH`) relations are omitted when history needs one. On any
  other Git, including 2.45.0, the refusal stays, and its message now says
  lazy fetching cannot be proven disabled for that Git instead of promising a
  fallback that `index --head` does not have.
- Committed-tree commands no longer fail with "malformed HEAD commit/tree
  metadata" in a repository that sets `i18n.logOutputEncoding` to UTF-16.

## [0.5.0] - 2026-10-23

### Removed

- The `init-agents` command, renamed to `setup` to match `entire brain setup`.
  The behavior is unchanged: `setup` installs the agent guide and managed
  `AGENTS.md`/`CLAUDE.md` blocks, `--strict` / `--normal` save the guidance
  mode and a bare re-run inherits the saved one, and `--no-claude-hook` skips
  the Claude Code hook. It also now warns up front, before any write, when the
  target repository is nested inside another one and no explicit `--repo` was
  given; a git-tracked home directory does not count as an outer repository.
  Running `init-agents` fails with a pointer to `setup`. `agent-guide` remains
  the read-only preview.

### Added

- `graph store` resolves the current committed-tree search store without building
  it, exposing a versioned read-only SQL projection for Brain code inspection.
  Brain can reuse Graph's database instead of maintaining a second copy.

- Added an experimental SCIP snapshot export, and versioned persisted diff and analyze output with schema-major enforcement, cache-validity binding, and a wire-shape regression freeze.
- Added a `graph health` command with per-source-file health accounting, and made `graph query` the canonical search command with support for trailing free-text queries.
- Added a persistent strict agent guidance mode, with Graph and Brain agent instructions coordinated to stay consistent.
- Added nightly LoCoMo benchmark CI for the entire-graph arm, authenticated via OIDC with no stored credentials.
- `setup` (then `init-agents`) now registers a Claude Code `SubagentStart` hook (`entire graph hook claude-subagent-start`) in the per-user `.claude/settings.local.json`, merged into existing settings and added once, so Explore and Plan subagents, which do not load `CLAUDE.md`, are told to use the graph. `--no-claude-hook` skips it.
- Added a `NOTICES` file to every release archive, covering the third-party parser sources and Go modules statically linked into the binary.

### Changed

- `setup` now renders one settled row per phase (agent instructions, Claude hook) in the Entire CLI style — an outcome glyph, the phase, and what happened — colored at a terminal and plain under a pipe, matching `entire brain setup`.
- `graph diff`, `commit`, and `checkpoint` now reject a subdirectory passed to `--repo` instead of returning root-wide changes that disagree with subdirectory-scoped snapshots.
- `graph query --format agent` with no `--max-context-bytes` now uses the smallest of 4 KiB, 8 KiB or 16 KiB that still shows both the first printed block and the best-ranked result exactly as an unbounded render does, and 24 KiB only when none does. An explicit budget is honoured exactly. Every candidate render keeps the exact-name answer, VERIFY block, declaration rows and session-replay rules of an explicit budget; an exact-identifier query is still rendered at the 24 KiB ceiling.
- Under a binding budget, agent-format search now keeps every ranked location with its rank, name and score, and shares the remaining bytes towards the head of the ranking, passing any share a result cannot use on to the others. The top hit no longer degrades to a bare `path:line *` to fit one more body line.
- Upgraded the Go toolchain to 1.27.
- Improved snapshot, search, and analyze performance and substantially reduced memory allocation, and made `stats` return a fast, single-line answer instead of a full rescan.
- Stats leads with observed follow-up rates and a signed modeled balance; the statusline retains its explicitly unvalidated 1:1 context model. Both retain losses and distinguish unavailable comparisons from zero; legacy JSON fields remain compatible.
- Aligned Graph's trail runners with Brain's and hardened CI with Windows test sharding and safer nightly prerelease publishing.

### Fixed

- Stats attributes located paths only to result rows, excludes diagnostic and quoted-source paths, and no longer credits a shared follow-up read as an avoided read.
- Preserved editing-tool read prerequisites and unknown-freshness fallback in every generated guide, with a guard keeping the checked-in activation artifact synchronized with the installer.
- The normal agent guide no longer caps graph use at ONE query: it asks for a Graph call on each locate or relationship question, names `def`, `neighbors` and `impact` for follow-ups, and gives every command with `--head` and a 4 KiB budget. Agents following the old wording ran one uncached query, then switched to grep.
- Corrected search term-rarity weighting to use repository-wide content frequencies when exact counts are available, with inspected-sample fallback when they are not. This can change result ordering without changing symbols or the output schema.
- Corrected transcript exploration classification for quoted commands, pipeline tails and writes; added signed stats model totals while retaining legacy JSON fields. Search `stats.context_block_bytes` now includes file outlines and validates their measured cost and independent cap.
- Scoped generated agent guidance to the observed source view: a complete body does not certify later freshness or dependencies, and unmarked source no longer mandates a duplicate read. Follow-up reads target missing or possibly changed source.
- Certified already-whole search bodies against their exact source without changing the selected JSON spans, and preserved unmarked source excerpts when an agent response cannot fit the complete body and its marker.
- Preserved validated nested repository boundaries in working-tree fallback scans, avoiding duplicate source and search results from untracked nested clones, linked worktrees, and submodules while retaining outer-repository tracked source.
- Classified ambiguous same-file nearest-declaration call guesses as `name_only` with reduced confidence, preserving the ambiguity through C++ out-of-line mapping. This corrects false certainty, not semantic overload selection; fast-profile and argument-forwarding filters now omit these uncertain edges.
- Fixed seven wrong-answer defects in the provider and type scanners and stopped bare type names resolving across language boundaries.
- Fixed language-scanner defect classes across C, C++, F#, and Julia, including declarator-based function naming, data-member and in-class method extraction, module-path and qualifier resolution, a forward-pipe precision bug, and bare-call scoping, and declared the type and data-flow relations all ten supported languages actually emit.
- Fixed six defects in default-export extraction, GraphQL fragment spreads, compact snapshots, and command-table search, a nested JS/TS function-expression scoping bug, and a prose-query ranking miss in search.
- Fixed nine CLI and gitutil navigation defects that previously answered wrongly instead of not answering at all, and stopped `explain` buffering its input.
- Fixed parameter-clause parsing to read every clause, let `--force` reach derived snapshots, and fixed the compact-tree walker to list only regular files.
- Fixed verification and search to stop advertising commands that cannot run, and stopped reporting a failed verification as a pass.
- Fixed opt-in agent session replay to respect the current output byte budget, including attribution and terminal escaping; oversized stored responses now yield a fresh bounded search instead of an over-budget echo.
- Fixed pipeline workers to observe shared context correctly, closed a map-race condition, and fixed the semantic diff to report pure file renames instead of hiding them.
- Fixed the doctor handshake, git-metadata error reporting, and repository agent activation so failures explain themselves and activation survives initializers.
- Fixed statusline to prefer the managed install over a stray developer build, and fixed the LoCoMo benchmark reproduction kit to run off the author's machine with stronger scoring and redaction guards.
- Fixed the worktree-snapshot warning to stop blaming a `--worktree` flag nobody passed — worktree mode is the default for the interactive query family — and named `--head` as the cacheable, committed-tree alternative.

### Security

- Confined worktree reads, cache writes, `init-agents` writes, and report and baseline writes to the repository or cache root, and closed an outside-path TOCTOU along with platform-specific alias-resolver rules.
- Refused to write through hard-linked targets and stopped indexing a git directory whatever its name or depth.
- Classified symlinked tree entries instead of parsing their contents.
- Bounded resource use against untrusted repository input by capping AST walk recursion, root ignore-file size and rule count, git blob read sizes, and instruction-file reads, and guarded cache-key ignore-file reads against non-regular files.
- Rejected option-shaped git revisions and refs before they reached git's argv or fetch, and escaped terminal control bytes in fatal error reports.
- Excluded credential stores such as `.env` and `.npmrc` from every graph corpus.
- Surfaced repository-controlled ignore exclusions in search instead of silently hiding matches, stopped a repository's own exclusions from vanishing silently, and honored `.graphignore` and secret-redaction rules in semantic diff output.
- Quarantined record-shaped lines found inside quoted source to block a payload and record-forgery vector.

### Documentation

- Rewrote README launch copy with a new Setup section and a LoCoMo benchmark summary, and qualified the supermemory and cmm LoCoMo comparison rows wherever they appear.
- Documented the direct release-archive download path, the Go and cgo prerequisites for building from source, and added contributor guidance.
- Updated the graphify link and version references in the README.
- Documented adopting the session before committing from a scratch worktree.

[0.5.0]: https://github.com/entireio/entire-graph/compare/v0.4.0...HEAD
