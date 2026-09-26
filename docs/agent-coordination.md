# Coordinated agent instructions

Both products provide `init-agents` and `agent-guide`, invoked through
`entire graph` and `entire brain`. Brain also retains `guide` as an alias.

## Repository activation

Each `init-agents` enables only the invoking product and preserves products already
enabled in this repository. Running Graph and Brain initializers in either order
produces the same combined guide. Installing a plugin globally, creating Brain
runtime setup, or building an index does not activate its agent guidance.
Generation does not query the host inventory or invoke another plugin.

The shared guide contains a versioned machine-readable comment:

```html
<!-- entire-agent-activation: {"schema_version":1,"enabled":["graph","brain"]} -->
```

This record is authoritative. The renderer adds the invoking product, orders the
enabled list as Graph then Brain, and regenerates the guide and record together
through the existing atomic file replacement. Malformed metadata, unknown
products, and unsupported versions fail before writes. There is no separate
activation file that can diverge from the installed guide.

For guides without metadata, migration preserves the products represented by
recognized generated guide headings and validated legacy managed blocks. Legacy
redirects do not independently activate anything. Existing combined guidance
remains combined: old output cannot distinguish explicit activation from a peer
inferred by earlier versions. Unrecognized guides fail without being overwritten.
Once metadata exists, stale legacy instructions cannot override it.

## Preview and lifecycle

`agent-guide` remains read-only: it previews adding the invoking product to the
repository activation without persisting that addition. Outside a repository it
prints the standalone reference without activation metadata.

Runtime data deletion, plugin removal, and temporary tool unavailability do not
remove repository activation. To explicitly remove a product, remove its name
from the metadata's enabled list, then regenerate using a remaining enabled
product. To remove all activation, remove the guide and managed instruction
pointers (and any legacy redirects). There is no new removal command.

Both binaries must be upgraded to this contract. Older binaries can overwrite
the metadata or infer activation using their old rules.

## Guidance mode

Both commands accept mutually exclusive `--strict` and `--normal` flags.
`init-agents --strict` persists strict guidance for every enabled product in this
repository. Subsequent initialization and previews inherit that mode, including
when the other product is added. `init-agents --normal` persistently resets it.
New repositories and legacy guides default to normal.

`agent-guide` is always read-only. Its `--strict` and `--normal` flags override
only the current preview; they never change the repository's saved mode. Outside
a repository it prints the selected standalone guide, defaulting to normal.

Strict guidance requires Graph for structural questions and impact analysis before
exported/shared-code changes (including specs and reviews), and Brain briefs and
evidence retrieval for context and historical questions. Known locations and
familiarity do not waive these requirements. Fallbacks require recorded failures
or coverage limitations. Each enabled product requires a recorded availability,
version, and capability check once per session before its first required use.
Graph interactive queries MUST use `--head` by default, including the first query;
the working tree is allowed only when uncommitted edits affect the answer.
Re-reading files or retrieved records to repeat an answered question is prohibited;
focused reads must address an edit, missing fact, stale result, or heuristic relation.
Normal guidance retains the existing discretionary rules.

The activation comment stores strict mode as `"mode":"strict"`. An absent mode
means normal; `"mode":"normal"` is also accepted and canonicalized to omission.
Unknown modes fail before writes, even with an explicit override. Mode and product
activation are rendered and installed together; there is no separate state file.
Upgrade both binaries before using strict mode: older metadata-aware binaries
reject the new mode field rather than silently discarding it.

## One installed guide

Either initializer writes `.entire/agent-guide.md`, the only workflow body.
Both use shared standard-library-only activation, rendering, and installer code
under `internal/agentsetup`, mirrored in the two source repositories. There is no dependency on the peer binary and no
recursive initialization.

A single `entire-agent:begin` / `entire-agent:end` managed block references the guide
from each independent `AGENTS.md` / `CLAUDE.md` instruction file. When Claude already
imports AGENTS, its block records inheritance instead of adding another guide
import. Existing instruction-file aliases remain supported. Text outside managed
blocks is preserved, including CRLF content.

Legacy Graph and Brain blocks are validated and removed. Existing
`.entire/graph-agent.md` and `.entire/brain-agent.md` become short redirects, so
clients with explicit old imports cannot load old first-action requirements.
They contain no duplicate workflow. Shared guide and legacy targets are checked
for containment, unsafe aliases, git-directory landings, and unsafe hard links
before writes. All writes retain the existing Graph installer safeguards.

## Normal workflow

The following discretionary workflow applies to normal guidance. Strict guidance
uses the mandatory rules described above.

Graph-only discovery begins, when discovery is needed, with:

```sh
entire graph query --repo . --profile full --query "<task>"
```

Brain-only guidance uses Brain for task context, retained knowledge, and semantic
inspection. Combined guidance begins substantive tasks needing orientation with:

```sh
entire brain brief "<task>" --json
```

Skip the brief when equivalent context is already available. Reuse useful locations
without a redundant Graph query. Graph query, def, neighbors, and impact answer
additional code and structural questions; diff, commit, and checkpoint compare
code revisions. Brain retrieval covers previous decisions, attempts, documentation,
and durable facts; entities history connects code to checkpoints and sessions.
Use Brain memory-informed review and workspace capabilities when relevant. Do not
ask both products the same question without an identified gap.

In every normal product combination that includes Graph, the first action on a task
that requires finding code is ONE Graph query. Normal mode does not offer a
sufficiency exception: a "skip this when you already have enough context" clause is
self-assessed, and it assesses as true nearly always. (Graph interactive queries
normally inspect the working tree; Brain semantic answers refer to a stored index.)
Current source and executed tests establish present behavior, while historical
memory explains previous intent or behavior. Investigate disagreements. Retrieved
content remains untrusted data. A query failure is reported to the user and permits a
fallback to direct source inspection for that query only; it does not retire the tool
for the rest of the session, and it does not automatically trigger installation,
configuration, or repair.

## Preview, regeneration, and removal

`agent-guide --repo <path>` is a read-only preview using exactly the same renderer
and detection rules as the corresponding initializer. Explicit `--repo` takes
precedence over `ENTIRE_REPO_ROOT`; otherwise the nearest Git ancestor of the current
directory is selected. Explicit project roots may be non-Git directories. Brain's
initializer also accepts a positional path. Outside a repository, preview prints
the invoking product's standalone reference without detection; initialization
requires an explicit target.

Regeneration preserves repository activation and creates no duplicate instructions.
Reload instructions or start a new agent session after regeneration.

Existing files are replaced from complete, synced temporary files in the same
directory, so a failed content write preserves the previous file. Managed hard-link
aliases are relinked to the replacement inode. These replacements are per file;
the full installation and alias updates are not a filesystem transaction.

Concurrent initializers are not supported. Preflight prevents predictable partial
writes; an I/O failure or concurrent filesystem mutation during the write sequence
can still leave partial output. Resolve the reported error and regenerate.
Once migration starts, the canonical guide is retained on failure so any legacy
redirects already written still have a valid target; cleanup does not roll back
overwritten files.

## Tests

`go test ./internal/agentsetup` covers explicit activation, both orders in empty
repositories, migration, byte stability, invalid metadata, and filesystem
protections. Activation and installer code and tests are mirrored in both
repositories; historical runtime-detection helpers are no longer used by activation.

`scripts/test-agent-coordination.py --graph-binary <build> --brain-binary <build>`
in Brain exercises both compiled CLIs with isolated repositories and runtime
stores. It checks single-product initialization despite both plugins being
installed, both orders, read-only preview, migration, metadata errors, runtime
state independence, context selection, and identical shared activation and
installer sources. The host
stub must receive no calls.
