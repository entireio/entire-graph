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
  External ignore inputs are bounded to 1 MiB per file and 64 KiB per rule
  line. One listing retains at most 16,384 parsed external rules and observes at
  most 512 nested `.gitignore` files. A limit refusal is reported instead of
  truncating the policy and silently changing the indexed corpus. Nested
  worktree ignore files are confined to the repository and are not followed
  through a symlink that escapes it. When Git cannot enumerate a worktree, the
  bounded filesystem fallback applies the ignore files it can observe and emits
  `W_GIT_WORKTREE_FALLBACK`; Git-only policy such as `core.excludesFile` may be
  unavailable, so the warning explicitly reports that excluded files can be
  present. Its ignored-tree `.git`-pointer sweep stays beneath a held repository
  root and refuses redirects or mount points (including same-filesystem bind
  mounts on Linux); a refused directory is disclosed as
  `W_GITDIR_SWEEP_UNREADABLE_DIRECTORY`.
- For `stats` only: local coding-agent session transcripts
  (`~/.claude/projects/<path-slug>/*.jsonl`, or `--sessions-dir`/
  `--transcript` overrides). This is Claude Code's transcript layout; the
  report is read-only and local. No other command reads transcripts.

### Credential-store path exclusions

A built-in exclude list keeps credential stores out of the provider's
working-tree and committed-tree source corpora. Within those corpora, unless a
caller deliberately re-admits one, a denied path is not parsed as source,
indexed, written to graph/search caches, returned in graph or search results, or
quoted in search context blocks.

This is a corpus and output boundary, not a promise that no local process reads
the underlying file or Git blob. Git-backed search acceleration can scan
tracked or committed blobs before credential-store path rules are applied to
its matches; denied matches are discarded before Entire Graph's content reader,
source parser, caches or responses. The `diff`, `analyze`, `commit` and
`checkpoint` families analyze requested Git ranges rather than the graph/search
corpus, so they can read credential-store paths and report content-derived
metadata about them. Those built-in operations remain local under the provider's
no-egress contract. `verify` runs caller-supplied commands with the caller's
privileges and is outside this exclusion boundary.

The list covers `.env` and its `.env.<environment>` variants, `.envrc`,
`.npmrc`, `.netrc`/`_netrc`, `.pgpass`, `.htpasswd`, `.pypirc`, `.dockercfg`,
`.boto`, `.git-credentials`, Docker's `.docker/config.json`, Kubernetes'
`.kube/config`, Terraform's `credentials.tfrc.json`, Google Cloud's
`application_default_credentials.json`, SSH private keys (`id_rsa`, `id_dsa`,
`id_ecdsa`, `id_ed25519`),
`credentials`/`credentials.{json,yml,yaml,ini,toml}`,
`secrets.{json,yml,yaml,ini,toml}`, key material and encrypted stores by suffix
(`.pem`, `.key`, `.pfx`, `.p12`, `.pkcs12`, `.jks`, `.keystore`, `.truststore`,
`.ppk`, `.kdbx`, `.asc`, `.gpg`), and files ending in `.yaml`, `.yml`, `.json`,
`.ini`, `.toml`, `.cfg`, `.conf`, `.properties`, `.txt` or `.enc` under a
directory segment named `secrets/` or `credentials/` at any depth. Matching is
case-insensitive. The Git, Docker, Kubernetes, Terraform, and Google Cloud
entries match at any depth and are file-only, so a same-named directory and its
descendants remain searchable.

Outside those file-only entries, matching retains Git-style path semantics. A
basename or suffix pattern can therefore match a directory segment; when it
does, that directory's descendants are excluded. For example, `*.key` also
excludes `pkg/client.key/**`. This behavior is intentional.

Classification is based on PATHS, not content: Entire Graph does not inspect a
file's bytes to decide whether an otherwise-unrecognized path contains a
secret. A credential store whose path gives no signal — `deploy/prod-values.yaml`
holding an inline API key — is not covered. Public halves (`.crt`, `.cer`,
`.pub`) are deliberately not excluded, and source code under
`internal/secrets/` or `pkg/credentials/` stays fully searchable.

The list is loaded after the repository's own exclude files, so a negation
inside the repository under analysis cannot switch it off, and before the
caller's `--ignore-file`/`--include-file`, so `--include-file` remains the way
to deliberately re-admit a path (a checked-in `.env.example` used as
configuration documentation, for example).

Because of that position the list only ever adds exclusions: it contains no
negation, so it cannot re-admit anything the repository itself excluded. The
bare `credentials` entry — the AWS CLI shape, `.aws/credentials` — is matched as
a FILENAME only for that reason: a directory named `credentials/` is neither
excluded by it nor re-admitted when the repository's own `.gitignore` or
`.graphignore` excludes it.

Both persistent caches (`index`/`search` snapshots and the streamed record
caches) key on a digest of the effective built-in rules, so a cache entry warmed
by a build with a different policy is not reachable — an entry written before
these rules existed misses instead of re-emitting the paths it named.

## What it writes

- Derivative caches, under `--cache-dir`, else `ENTIRE_PLUGIN_DATA_DIR`, else
  (for most query commands) the per-user cache directory. Cache entries are
  compressed snapshots rebuilt from repository state; deleting them costs a
  rebuild, nothing else. Queries never modify repository source files.
- `init-agents` writes through exactly three repository paths, disclosed in
  [agent activation](agents.md): `.entire/graph-agent.md` and managed blocks
  in `AGENTS.md` and `CLAUDE.md`. A hard-linked pathname elsewhere names the
  same inode and therefore observes the same update; the activation guide
  calls out that filesystem property explicitly.
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

## Command-family tree semantics

Interactive queries read the working tree by default and the committed tree
with `--head`. Bulk streams and ref-based analysis default to committed state
(`--worktree` opts the streams into the working tree). A result is therefore
always attributable to one requested repository view; the cache keying that
preserves this is described in the [operations cache guide](operations.md#cache).
