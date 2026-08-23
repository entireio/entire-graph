# Operations

Entire Graph is a local Entire CLI plugin. This page covers installation
channels, the cache, and the release tooling used to build archives.

## Installing

The supported installation is from the plugin index:

```sh
entire plugin install graph
entire graph version
```

Update an indexed installation with:

```sh
entire plugin upgrade graph
entire graph version
```

To install a local development build instead, use:

```sh
scripts/install-local.sh
```

The script builds `./entire-graph`, installs it with `entire plugin install
./entire-graph --force`, and prints `entire graph version`. It fails before
writing anything if the parent `entire` CLI is not on `PATH`. The helper
injects `git describe --tags --always --dirty` as the version. A raw `go build`
without version linker flags reports `dev`.

## Requirements

- The Entire CLI (0.10.0 or later) must be on `PATH`.
- Git 2.36 or later must be on `PATH`; committed-tree readers use its
  single-session `cat-file --batch-command` protocol.
- Development uses Go 1.26. Tree-sitter bindings require CGO and a working C
  compiler for the target platform.
- Local release builds require `tar` for non-Windows archives, `zip` or `7z`
  for Windows archives, and either `sha256sum` or `shasum`. Signing additionally
  requires `cosign` or `gpg` and a configured local key.

## Cache

Queries build a snapshot of the repository graph and cache it as compressed
derivative state. Nothing in the cache modifies repository files, and deleting
the cache directory costs a rebuild, nothing else.

### Location

The cache root is resolved in order: `--cache-dir`, then
`ENTIRE_PLUGIN_DATA_DIR`, then the per-user cache directory (`entire-graph`
under the OS user cache path). The per-user fallback applies to `search`,
`neighbors`, `impact`, `index`, `snapshot`, `symbols`, and `edges`. `def` and
`explain` stop at `ENTIRE_PLUGIN_DATA_DIR`; with neither the flag nor the
variable set they rebuild on every run. One directory is shared by every
repository and worktree on the machine; keys, not directories, separate
entries.

### Two cache families

- The **search snapshot cache** backs `search`, `neighbors`, `impact`, and
  `index`. It caches committed-tree (`--head`) queries; working-tree queries
  always bypass it.
- The **provider records cache** backs the bulk streams (`snapshot`,
  `symbols`, `edges`). It is committed-tree only; `--worktree` streams always
  bypass it.

### What a cache entry is keyed on

A search-snapshot entry is a function of: the cache format version, the
checkout path, the repository identity (derived from the Git remote), the
provider version, the committed `HEAD` tree hash, the profile, the parse-size
and file-count limits, any file-subset selection, the paths **and contents** of
`--ignore-file`/`--include-file` inputs in caller order, and the contents of
`.graphignore`. Change any of these and the next query builds a new entry;
matching all of them is what "cache hit" means. A legacy worktree discriminator
remains in the envelope only to reject retired entries; it is not a current key
term because working-tree snapshots cannot receive persistent keys.
[ADR 0002](adr/0002-committed-tree-cache-key.md) records why the key is total
over graph-shaping inputs.

A provider-record entry binds the same graph-shaping inputs plus the exact
commit and output mode. Exact commit identity is required because the cached
opaque NDJSON header records it; unlike a structured search snapshot, that
stream cannot be safely restamped on a same-tree cache hit.

### Working-tree queries

Interactive queries default to the working tree and always build a fresh
snapshot. They neither load nor store search-snapshot cache entries, even when
the checkout is clean. Git's status/diff machinery cannot establish raw
worktree equality safely here because it may run repository-selected clean or
process filters.

Use `--head` when committed-tree semantics are acceptable and repeated queries
should reuse a cache entry. [ADR 0004](adr/0004-working-tree-cache-security-boundary.md)
records the security decision; [ADR 0003](adr/0003-working-tree-search-snapshot-cache.md)
is the superseded clean-tree reuse design.

### Prewarming with `index`

`entire graph index` builds one committed-`HEAD` snapshot entry and verifies
it was written. It warms exactly one cache variant: later queries reuse it
only when they run with `--head` and matching profile, cache directory,
ordered ignore/include inputs, and unchanged `.graphignore`. Two defaults make
this easy to get wrong: `index` defaults to `--profile full` while `search`
defaults to `--profile fast`, and `index` cannot warm the default
working-tree path at all. To prewarm for the installed agent guide's
committed-tree queries, `index`'s default profile is the right one, but the
guide's queries use the working tree, so they do not reuse that entry.

`index --report <path>` additionally writes a Markdown summary of the built
graph for human review.

### Observing cache state

- `search` (default JSON): `stats.index_cache_hit`.
- `impact --format text` and `neighbors --format text`: leading
  `Index: cache-hit`/`cache-miss` line.
- `search --format agent` and `neighbors --format agent`: normally the same
  `Index:` header. Under a tight byte budget it compacts to `I:hit`/`I:miss`;
  an extremely small budget can omit the telemetry.
- JSON output from `search`, `impact`, and `neighbors` includes cache fields.
- `search --format text` and `explain`: no cache state is reported.
- `def`: JSON field only.

## Updating and troubleshooting

`entire plugin upgrade graph` installs the current indexed release;
`entire graph version` confirms what is active. After an upgrade, the first
query per repository is cold because cache entries embed the provider version.
Old entries simply stop matching and can be deleted at leisure.

Working-tree queries always rebuild. If a `--head` query rebuilds when you
expect a hit, check in order: a changed committed tree or `.graphignore`, a
profile mismatch between runs, and, for `def`/`explain`, a missing
`--cache-dir`/`ENTIRE_PLUGIN_DATA_DIR`, since those two commands do not fall
back to the per-user cache directory.

## Reports and the status line

`entire graph stats --repo .` reports graph vs exploration tool usage from
local session transcripts, read-only. `scripts/entire-graph-statusline.sh`
renders the single-session variant as a Claude Code status line.

## Release archives

```sh
scripts/release.sh
```

The release script writes `dist/release-<version>/` with one archive per target
and a `checksums.txt` manifest. Non-Windows targets use `.tar.gz`; Windows
targets use `.zip`. `VERSION=<value>` overrides the version; otherwise the
script uses `git describe --tags --always --dirty`.

By default the script builds the current host target. Set
`ENTIRE_RELEASE_TARGETS` to a space-separated list of `GOOS/GOARCH` targets to
request more builds:

```sh
ENTIRE_RELEASE_TARGETS="darwin/arm64 linux/amd64" scripts/release.sh
```

`entire-graph` includes native tree-sitter parser bindings, so cross-platform
artifacts require the matching cgo-capable compiler/toolchain for each
requested target. The script records checksums for artifacts it successfully
builds; it also signs archives when a local signing key is explicitly
configured:

- `COSIGN_KEY=<key-ref>` with `cosign` on `PATH` writes `<archive>.sig`.
- `GPG_SIGNING_KEY=<key-id>` with `gpg` on `PATH` writes `<archive>.asc`.

If both signing variables are set and both tools are available, cosign takes
precedence and the script writes only the `.sig` file.

The script does not publish artifacts. The GitHub `release` workflow builds and
verifies six platform archives. A manual workflow run on a branch only validates
them; pushing a `v*` tag also publishes the GitHub release and `checksums.txt`.
