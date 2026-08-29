# Agent activation

This page is the reference for `entire graph init-agents`: what it writes, how
reruns behave, how Claude inheritance is handled, how to verify that a coding
agent loaded the guide, and how to recover from invalid instruction files. The
behavior described here matches the 0.4.0 release target. Run
`entire graph init-agents --help` for the flags in your installed version.

## What `init-agents` writes

Activation is per repository. From the repository root:

```sh
entire graph init-agents --repo .
```

The command manages three repository paths:

- `.entire/graph-agent.md`: the complete operating guide for coding agents.
  It is regenerated on every successful run, so manual edits do not survive.
  Its content is identical to `entire graph agent-guide` output.
- `AGENTS.md`: the canonical cross-agent entry point. The command creates the
  file if absent, appends one managed block if no block exists, or replaces the
  existing managed block while preserving other content.
- `CLAUDE.md`: a Claude Code entry point whose managed block is selected from
  the repository's existing instruction-file topology.

The direct managed block contains a pointer to the generated guide. Its
identifying lines are:

```markdown
<!-- entire-graph:begin -->
...
@.entire/graph-agent.md
<!-- entire-graph:end -->
```

A client that resolves `@` imports loads the guide into context. A client that
treats the block as plain text still receives an instruction to read
`.entire/graph-agent.md` before exploring code.

## Claude inheritance

`AGENTS.md` always receives the direct guide pointer. The `CLAUDE.md` block
depends on how that file already reaches `AGENTS.md`:

- If a distinct `CLAUDE.md` contains a live standalone import whose path
  resolves to the root `AGENTS.md`, its managed block contains only this notice:

  ```markdown
  <!-- entire-graph:begin -->
  <!-- Entire Graph instructions are inherited through AGENTS.md. -->
  <!-- entire-graph:end -->
  ```

  The user's `AGENTS.md` import remains in place, and the guide is not imported
  directly a second time.
- If that import is absent or cannot be identified safely, `CLAUDE.md` receives
  the direct guide pointer. Removing or adding a live `AGENTS.md` import and
  rerunning `init-agents` switches the managed block accordingly.
- If `AGENTS.md` and `CLAUDE.md` resolve to the same regular file through a
  symlink or hard link, the shared file receives the direct block once. The
  link topology is preserved.

Import detection recognizes standalone relative or absolute paths that resolve
to the root `AGENTS.md`. Mentions inside inline code, fenced or indented code,
HTML comments, or the managed block do not count. Ambiguous Markdown keeps the
direct pointer rather than assuming inheritance. `init-agents` does not create
or remove the user-owned `AGENTS.md` import itself.

## Preflight and rerun behavior

Before its first write, `init-agents` inspects both instruction paths, reads
each distinct file, validates the marker layout, and renders all managed
content from that validated snapshot.

Each path must be missing or resolve to a regular file. Symlinks to regular
files are supported, with the target written either relatively or as an
absolute path, as long as it stays inside the project root; a symlink that
resolves outside is refused and nothing is installed. Staying inside the project
root is necessary but not sufficient: a symlink that lands inside a git
directory — `.git` at any depth, including a nested checkout's and a linked
worktree's `.git` pointer — is refused as well. `.git` is inside the project root
but is not project content, and no instruction file belongs there; writing a
managed block into `config` or a hook would corrupt the repository rather than
configure an agent. The git directory is recognised by its structure rather than
by its name, so a repository whose administrative directory is not called `.git`
— `git init --separate-git-dir=admin`, or a checkout driven by `GIT_DIR` — is
covered by the same refusal.

The landing must also be an agent-instruction file. An alias exists so that
`AGENTS.md` and `CLAUDE.md` can share one instruction file, so a target that is
markdown by extension, or a rules file such as `.cursorrules`, is written; a
target that is some other existing file — a `Makefile`, `.envrc`, or
`.github/workflows/ci.yml` — is refused rather than having a managed block
appended to it. A target that does not exist yet is still created, which is what
the dangling-alias case below relies on, and a target this command wrote on an
earlier run stays writable whatever it is named. Hard links are also
supported, but every hard-linked pathname names the same inode: an update
through `AGENTS.md` or `CLAUDE.md` is therefore visible through any other hard
link to that file, including one outside the project. A directory, named pipe,
socket, device, or other non-regular target is rejected with its type named in
the error. A dangling alias between `AGENTS.md` and `CLAUDE.md` is supported;
the shared target is created and updated once.

A relative symlink target may not step above the project root and later
re-enter it through another alias. On Windows, a UNC target whose share cannot
be matched to the repository's own UNC spelling is refused before it is probed;
this means a mapped-drive checkout must use that mapped-drive spelling rather
than an UNC spelling of the same directory. A Windows reparse target is also
refused when its authoritative NT spelling cannot be represented without
changing meaning in the confined relative operation. This includes raw
alternate separators, absolute NT dot components, trailing dots or spaces, and
non-DOS device namespaces; an informational extended-path display name alone
does not change the target Windows resolves.

Each distinct file must contain either no Entire Graph marker tokens or exactly
one begin marker followed by one end marker. Marker tokens in examples, code
fences, or comments still count because they make the replacement range
ambiguous.

If either path or marker layout fails preflight, the command stops before
creating or changing `.entire/graph-agent.md`, `AGENTS.md`, or `CLAUDE.md`.
After preflight succeeds, a rerun:

- regenerates `.entire/graph-agent.md`;
- appends a block to an unmanaged instruction file;
- replaces one valid managed block in place;
- preserves all content outside the managed block; and
- produces byte-identical files when no inputs have changed.

### Recovering from invalid instruction files

The error names the file and the condition that blocked the run. To recover:

1. Back up `AGENTS.md` and `CLAUDE.md`.
2. Replace any directory or other non-regular target with a regular file, or
   move it aside if the instruction file should be created.
3. In each regular file, keep either zero Entire Graph marker tokens or exactly
   one complete begin/end pair in that order. Reword marker strings shown in
   examples or comments.
4. Preserve user-owned text outside the intended managed block.
5. Rerun `entire graph init-agents --repo .` and confirm that each independent
   instruction file contains one managed block.

## Removal

There is no removal command. To deactivate, delete
`.entire/graph-agent.md` and remove the managed block, including both marker
lines, from `AGENTS.md` and `CLAUDE.md`. Delete `.entire/` or either instruction
file only if it is otherwise empty.

## Working-tree queries bypass the cache

The interactive query family reads the working tree by default and rebuilds its
snapshot on every query, whether or not the activation files are committed. Use
`--head` when committed-tree semantics are acceptable and cache reuse matters.
See the [operations cache guide](operations.md#working-tree-queries).

## Client notes

### Claude Code

Claude Code loads the repository's `CLAUDE.md` at session start and resolves
live `@` imports. The managed direct pointer loads `.entire/graph-agent.md`.
When `CLAUDE.md` already imports `AGENTS.md`, the inheritance layout avoids a
second direct guide pointer.

A session opened before activation does not see the new files. Start a fresh
session or task in the repository after running `init-agents`.

### Other clients

Clients that read a root `AGENTS.md` encounter the direct pointer text. Clients
that do not resolve `@` imports must follow the written instruction and read
`.entire/graph-agent.md` themselves. Whether they do so depends on the client
and model, so verify the behavior rather than assuming it.

## Verifying the activation chain

Each layer has an observable check:

1. **Installation.** `entire version` and `entire graph version` both succeed;
   the second prints the installed release, such as `v0.4.0`.
2. **Files.** The three managed paths exist. Each independent instruction file
   has exactly one ordered marker pair, and
   `diff <(entire graph agent-guide) .entire/graph-agent.md` is empty. If
   `CLAUDE.md` imports `AGENTS.md`, confirm its managed block contains the
   inheritance notice rather than another direct guide import.
3. **Instruction load.** Start a fresh agent session and give it a
   code-location task. The client-side signal depends on the client; behavior
   is checked in the next step.
4. **Adoption.** The session's first code-locating tool call is
   `entire graph search ...`, before broad grep, find, or whole-file reading. If
   the agent starts elsewhere, the guide may not have loaded or may not have
   been followed. Check the activation files and the client's instruction view.
5. **Grounding.** The answer cites files and lines opened after the graph query,
   and any proposed change is checked against focused source or a narrow test.
