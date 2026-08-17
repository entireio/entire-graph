# Agent activation

This page is the reference for `entire graph init-agents`: what it writes, how
reruns behave, how to verify that a coding agent actually loaded the guide, and
how to recover when instruction files get into a bad state. Behavior described
here was verified against the installed v0.3.0 release; run
`entire graph init-agents --help` for the flags of the version you have.

## What `init-agents` writes

Activation is per repository. From the repository root:

```sh
entire graph init-agents --repo .
```

The command touches exactly three paths:

- `.entire/graph-agent.md` — the operating guide for coding agents. It is
  generated in full and regenerated in full on every successful rerun, so
  manual edits to this file do not survive. Its content is identical to the
  output of `entire graph agent-guide`.
- `AGENTS.md` — created if absent; otherwise the command appends or replaces
  one managed block (see below) and leaves the rest of the file alone.
- `CLAUDE.md` — same treatment as `AGENTS.md`.

The managed block is delimited by HTML-comment markers and contains a short
pointer plus an import line:

```markdown
<!-- entire-graph:begin -->
This repo has the entire-graph code graph installed. Before exploring code with
grep/find/whole-file reads, read .entire/graph-agent.md — resolution-first guidance
for using graph retrieval, focused source inspection, and verification.
@.entire/graph-agent.md
<!-- entire-graph:end -->
```

The block is written to work two ways. A client that resolves `@`-imports
(Claude Code does) pulls the guide into context automatically. A client that
treats the block as plain text still gets an explicit instruction to read
`.entire/graph-agent.md` before exploring.

## Rerun behavior

Verified on v0.3.0:

- When both files contain one intact begin/end pair, a rerun replaces the block
  content and regenerates `.entire/graph-agent.md`. With no other changes the
  rerun is byte-idempotent: all three files hash identically before and after.
- When a file exists but has no markers, a rerun appends one managed block at
  the end and preserves the existing text.
- v0.3.0 does not validate markers before writing. If a file contains a stray
  or incomplete marker — an orphan `<!-- entire-graph:begin -->` with no end,
  markers in reversed order, or marker text quoted inside an example — a rerun
  appends a second block instead of replacing the first, leaving duplicate
  markers in the file. Worse, once an orphan `begin` sits above an appended
  block, the next rerun treats the orphan as the block start and replaces
  everything from it to the appended block's end marker, deleting any of your
  text in between. Keep the marker lines exactly as written and do not quote
  them elsewhere in the same file.

### Recovering from broken markers

If `AGENTS.md` or `CLAUDE.md` ends up with duplicate, orphaned, or reordered
markers:

1. Back up both files.
2. Edit each file so it contains either zero Entire Graph marker lines or
   exactly one `begin` marker followed by one `end` marker. Marker strings
   inside code fences or comments count — remove or reword those too.
3. Keep all of your own text; only the marker lines and the generated block
   between them belong to `init-agents`.
4. Rerun `entire graph init-agents --repo .` and confirm each file now has one
   managed block.

## Removal

There is no removal command. To deactivate, delete `.entire/graph-agent.md`
(and the `.entire/` directory if it is now empty) and remove the managed block,
including both marker lines, from `AGENTS.md` and `CLAUDE.md`. If either file
contains only the managed block, delete the file.

## Commit before you rely on the cache

The three activation files are indexable Markdown. While any of them is
untracked or modified, the working tree is dirty in a way the query cache
respects: default (working-tree) queries rebuild the graph instead of reusing
the cached snapshot. Committing the activation files restores clean-tree cache
reuse. This is also why activation and a query benchmark should not share one
uncommitted checkout. Details are in
[operations — cache](operations.md#cache).

## Client notes

### Claude Code (tested)

Tested with Claude Code 2.1.233 against a fixture repository activated by
v0.3.0. Claude Code loads the repository's `CLAUDE.md` at session start and
resolves `@`-imports, so the managed block's `@.entire/graph-agent.md` line
places the guide in context without an explicit read. In the captured session
the agent's first tool call was an `entire graph search`, made without first
opening the guide — the import route, not an on-demand read, delivered the
instructions.

Two practical consequences:

- A session that was already open during activation does not see the new
  files. Start a fresh session (or task) in the repository after running
  `init-agents`.
- `AGENTS.md` and `CLAUDE.md` both carry the block after a v0.3.0 activation.
  In the tested layout this did not produce doubled guide content in the
  session; if you maintain these files by hand, keeping `CLAUDE.md` as a thin
  pointer to `AGENTS.md` is a reasonable convention, but it is not something
  v0.3.0 sets up for you.

### Other clients

Any agent client that reads a root `AGENTS.md` or `CLAUDE.md` will encounter
the pointer text. Clients that do not resolve `@`-imports must follow the
written instruction and read `.entire/graph-agent.md` themselves; whether they
do depends on the client and model. No client other than Claude Code has been
verified end to end, so treat integration with other clients as plausible but
untested, and use the verification steps below.

## Verifying the activation chain

Each layer has an observable check:

1. **Installation.** `entire version` and `entire graph version` both succeed;
   the second prints the installed release (for example `v0.3.0`).
2. **Files.** The three paths above exist, `AGENTS.md` and `CLAUDE.md` each
   contain exactly one begin/end pair, and
   `diff <(entire graph agent-guide) .entire/graph-agent.md` is empty.
3. **Instruction load.** Start a fresh agent session and give it a code-location
   task. The client-side signal depends on the client; behaviorally, a loaded
   guide shows up as the next check.
4. **Adoption.** The session's first code-locating tool call is an
   `entire graph search …`, before any broad grep/find or whole-file reading.
   If the agent starts with grep, the instructions did not reach it — check
   steps 2 and 3.
5. **Grounding.** The agent's answer cites files and lines it actually opened
   after the graph query, and any proposed change is checked against focused
   source or a narrow test.
