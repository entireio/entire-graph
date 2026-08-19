# Trust and security

This page states what Entire Graph reads, writes, executes, and sends over the
network, scoped precisely enough to be checked. Statements match the 0.4.0
release target and the implementation under `internal/`.

## Network

The built-in analyzer is local-only. During analysis it does not fetch remote
code, download grammars or models, call hosted APIs, resolve embeddings, or
send telemetry. Tree-sitter grammars are compiled into the binary. The
benchmark harness's clone phase and `entire plugin install graph` are the
networked operations, and both are explicit: one is a development tool, the
other is installation.

## What it reads

- Repository content: the working tree (default for interactive queries) or
  committed objects via `git` (for `--head`, bulk streams, and `diff`/
  `commit`).
- Bounded local Git history, for co-change relations (`FILE_CHANGES_WITH`) and
  change analysis. History never leaves the machine.
- The full Git ignore stack, including nested `.gitignore` files,
  `.git/info/exclude`, per-worktree excludes, and `core.excludesFile`, plus
  `.graphignore` and any
  `--ignore-file`/`--include-file` the caller passes.
- For `stats` only: local coding-agent session transcripts
  (`~/.claude/projects/<path-slug>/*.jsonl`, or `--sessions-dir`/
  `--transcript` overrides). This is Claude Code's transcript layout; the
  report is read-only and local. No other command reads transcripts.

## What it writes

- Derivative caches, under `--cache-dir`, else `ENTIRE_PLUGIN_DATA_DIR`, else
  (for most query commands) the per-user cache directory. Cache entries are
  compressed snapshots rebuilt from repository state; deleting them costs a
  rebuild, nothing else. Queries never modify repository source files.
- `init-agents` writes exactly three repository files, disclosed in
  [agent activation](agents.md): `.entire/graph-agent.md` and managed blocks
  in `AGENTS.md` and `CLAUDE.md`.
- `index --report <path>` writes a Markdown graph report to the path you give
  it.
- `verify --record-baseline <path>` creates parent directories as needed and
  writes a JSON baseline to the path you give it.

No other command family writes into the repository unless a caller-provided
command does so.

## What it executes

- Graph queries (`search`, `def`, `explain`, `neighbors`, `impact`) and
  streams run `git` subprocesses and parse files. They do not execute
  repository code.
- `search` **suggests** a `VERIFY:` command derived from repository contents
  (test names, build files). It does not run it. Anything that later runs
  that command is executing text influenced by repository contents. Read the
  command first in repositories you do not trust.
- `entire graph verify` executes the test command the caller provides and
  adjudicates the result. With `--setup <command>`, it executes that setup
  command first. Both run with your privileges; pass only commands you would
  run yourself.

## Determinism and heuristics

The same repository view and options produce the same graph. There is no
hidden per-user state influencing results; learned or durable memory belongs
to [Entire Brain](brain-and-graph-boundaries.md), not this provider.

Within that determinism, static relations are heuristic. Call, route, event,
test, and data-flow edges carry `resolution` and `confidence` fields instead
of a completeness promise; dynamic dispatch, reflection, and generated code
can produce missing or unresolved edges. Inventory-only languages get file and
symbol structure with no semantic relations. `capabilities --json` reports
which tier a language is in. Files the parser cannot process emit
machine-readable partial failures rather than disappearing silently.

## Repository-controlled exclusions are disclosed

`.graphignore`, `.gitignore` and `.git/info/exclude` live in the repository, so
whoever can commit to it decides part of what the graph sees. One committed line
naming a tracked source file removes that file from every answer.

`search` therefore reports what those rules removed rather than presenting the
surviving corpus as the whole of it. When repository-controlled rules exclude
files Git itself lists, the response carries `repo_ignored` (the count, the ignore
files responsible, and up to ten of the excluded paths),
`stats.files_excluded_by_repo_ignore_rules`, and a `W_REPO_IGNORED_SOURCE`
warning; the text and agent payloads print the count and name the paths. A
repository that excludes nothing adds nothing.

Exclusions **you** asked for with `--ignore-file` are not reported: they are your
own instruction, and reporting them back would bury the case that is not.

The count is exact except in two cases, and both say so. When enumerating an
excluded directory tree hits something it cannot read, `repo_ignored` carries
`count_incomplete` with the paths responsible and the response carries an
`E_REPO_IGNORE_UNREADABLE` partial failure. When the excluded tree is larger than
the accounting enumerates — the walk is bounded so that a committed rule over a
huge tree cannot hand back the cost the prune saved, on every search — the report
carries `count_incomplete` and an `E_REPO_IGNORE_COUNT_INCOMPLETE` partial
failure. Either way the number is known to be a lower bound rather than quietly
understated.

This is disclosure, not prevention. It tells you that files were removed and
which ones; it does not tell you whether one of them was the answer to your
query, and deciding that still means reading the file. Commands other than
`search` do not yet carry the disclosure.

## Command-family tree semantics

Interactive queries read the working tree by default and the committed tree
with `--head`. Bulk streams and ref-based analysis default to committed state
(`--worktree` opts the streams into the working tree). A result is therefore
always attributable to one requested repository view; the cache keying that
preserves this is described in the [operations cache guide](operations.md#cache).
