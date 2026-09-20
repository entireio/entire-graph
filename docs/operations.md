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
- Development uses Go 1.27. Tree-sitter bindings require CGO and a working C
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
under the OS user cache path). The per-user fallback applies to `query`,
`neighbors`, `impact`, `index`, `snapshot`, `symbols`, and `edges`. `def` and
`explain` stop at `ENTIRE_PLUGIN_DATA_DIR`; with neither the flag nor the
variable set they rebuild on every run. One directory is shared by every
repository and worktree on the machine; keys, not directories, separate
entries.

The caller-selected cache root is the filesystem trust boundary and may itself
be a symlink. Beneath it, Entire Graph owns the family and version directories;
each must be an ordinary, non-redirecting directory. A symlink, a Windows
junction or mount-point reparse entry, or a component swapped while it is opened
is refused even when its target would remain inside the cache root. This
intentionally excludes in-root aliases that older versions followed. To
relocate a cache, point `--cache-dir` or `ENTIRE_PLUGIN_DATA_DIR` at the backing
directory, or make the cache root itself a symlink rather than linking a family
or version below it. Best-effort query caching treats a refusal as a cold path;
`index`, whose purpose is durable prewarming, reports the persistence failure.
These checks cover path entries supplied by a repository and substitutions
observed while a component is opened. They do not claim resistance to another
running process with permission to rename an already-opened directory: `os.Root`
continues using that directory object after a move, and portable Go cannot pin
its lexical ancestry. Such a process can already move existing cache artifacts
through the same writable namespace.

### Two cache families

- The **search snapshot cache** backs `query`, `neighbors`, `impact`, and
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
this easy to get wrong: `index` defaults to `--profile full` while `query`
defaults to `--profile fast`, and `index` cannot warm the default
working-tree path at all. To prewarm for the installed agent guide's
committed-tree queries, `index`'s default profile is the right one, but the
guide's queries use the working tree, so they do not reuse that entry.

`index --report <path>` additionally writes a Markdown summary of the built
graph for human review.

### Observing cache state

- `query` (default JSON): `stats.index_cache_hit`.
- `impact --format text` and `neighbors --format text`: leading
  `Index: cache-hit`/`cache-miss` line.
- `query --format agent` and `neighbors --format agent`: normally the same
  `Index:` header. Under a tight byte budget it compacts to `I:hit`/`I:miss`;
  an extremely small budget can omit the telemetry.
- JSON output from `query`, `impact`, and `neighbors` includes cache fields.
- `query --format text` and `explain`: no cache state is reported.
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

`entire graph stats --repo .` prints one line — the estimated tokens the graph
saved — from local session transcripts. `--verbose` restores the full report
(graph vs exploration usage per verb and kind, billed tokens, the measured
per-call costs, and the model's assumption); `--format json` is a machine
contract and is unaffected by `--verbose`.

`--since` prunes transcripts by file mtime before parsing them, and unchanged
transcripts are memoised under the cache directory keyed on file identity plus
the binary's own identity, so repeat runs do not re-parse gigabytes of session
log. `--no-cache` forces a full re-parse; `--cache-dir` relocates the memo.

`scripts/entire-graph-statusline.sh` renders the single-session variant as a
Claude Code status line.

## Release archives

```sh
scripts/release.sh
```

The release script writes `dist/release-<version>/` with one archive per target
and a `checksums.txt` manifest. Non-Windows targets use `.tar.gz`; Windows
targets use `.zip`. `VERSION=<value>` overrides the version; otherwise the
script uses `git describe --tags --always --dirty`.

Each archive carries the binary plus `README.md`, `LICENSE`, `NOTICES`, and
`entire-plugin.yml`. `NOTICES` is the third-party attribution for everything
statically linked into the binary: the tree-sitter parser sources vendored
under `internal/sem/` and the Go modules the six released platforms reach. It
is generated by `scripts/gen-notices.sh` from those license files and the
resolved module graph, committed to the repository, and checked for drift in
CI; `scripts/gen-notices.sh --check` runs the same check locally. The release
script refuses to build an archive when any of the four files is missing,
because a published release asset cannot be corrected in place.

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
Release notes come from the tag's `CHANGELOG.md` section when one exists, and
fall back to auto-generated commit notes otherwise, which is the path nightly
prereleases take.
## Graph health

`entire graph health` always reports indexing health, including `ok`. The default
is text; `--json` (or `--format json`) returns structured data. It uses committed
`HEAD` with the `full` profile. `--profile`, `--cache-dir`, `--ignore-file` and
`--include-file` select the same cache variants as `index`. A missing index is
built locally, with a build notice on stderr. `--refresh` rebuilds and invalidates
derived query entries even when the source tree is unchanged. No source files or
ignore rules are modified. `doctor` keeps its existing capability-check behavior.

Health is shared by indexes, snapshot summaries and query summaries in
`completeness.health` (additive schema 1.3). It includes `source_files`,
`flagged_files`, `flagged_percentage`, `threshold_percentage`,
`intentional_skipped_files`, `status`, and a per-language breakdown. Existing
`stats.completeness_level` (index/health: `counts.completeness_level`) agrees with
that status. Existing fields and full diagnostic arrays remain available.

The percentage is `100 × unique flagged source files / eligible source files`.
The inclusive degradation threshold is 5%; status comparisons use integer counts
without rounding. A file with several diagnostic categories or failures in both
parser phases contributes once. `E_MINIFIED` and `E_FILE_TOO_LARGE` are intentional
skips, excluded from the numerator and reported separately. Eligible skipped
files remain in the denominator. Unsupported recognized source and source read
failures count even when no file record could be emitted.
An optional diagnostic `language` preserves shebang-based classification when
an extensionless source file cannot be read in full.

Eligibility is defined centrally in `internal/sem/health.go`: recognized
programming, template, stylesheet and interface languages count, including
inventory-only languages and scripts identified by shebang. Documentation,
configuration and data formats do not count: for example Markdown, JSON, YAML,
XML, TOML, INI, HCL/Terraform, CUE, Dockerfile, Make and project manifests.
Unknown file types are not assumed to be source. Source files under documentation
directories still count. Files excluded by ignore/build policy are outside the
snapshot scope. Query-selected snapshots calculate the same metric over their
selected scope, not an unexamined whole repository; a no-hit search may reuse a
complete cached report, or omit health when no graph was built.

With zero eligible source files, the percentage is explicitly 0 and the threshold
alone yields `ok`. Stronger guards remain, in their existing order: a majority
unparsed yields `unsafe`; a parsed graph with zero symbols yields `degraded`;
otherwise more than 25% flagged yields `unsafe`, and more than 25% unparsed yields
`degraded`. The existing recorded-file
guards also apply, including to zero-source scopes. A truly empty scope is `ok`.
The report's threshold is therefore not the sole reason a status can be degraded.

Meaningful diagnostics are retained below 5%, and relevant query warnings still
appear. Diagnostics describe parser limitations and incomplete extraction; they
do not establish that source code is invalid. The health command lists affected
files, effects, and locations in diagnostic `detail` when the parser supplies
them (timeouts and read failures may have no line/column). Its JSON additionally
contains `diagnostic_categories` and `intentional_skips`, without removing the
original `partial_failures` or `warnings` arrays. Language percentages cover
eligible source; categories and diagnostic lists also retain non-source failures.

`cache_freshness` is `matching_index`, `built`, or `rebuilt`; `index_cache_hit`,
`commit`, `tree`, `profile` and `index_latency_ms` provide the underlying evidence.
Freshness means a matching committed tree, provider, profile and indexing policy,
not the absence of uncommitted source changes. The schema and both cache-family
versions invalidate earlier status calculations, including local `dev` builds.
Symbol IDs are unchanged. Caches use the platform per-user directory unless
`--cache-dir` or `ENTIRE_PLUGIN_DATA_DIR` overrides it.

### Downstream compatibility check

Run `mise exec -- python3 scripts/test-brain-health.py /path/to/entire-brain`
against the Brain version intended for deployment. It builds this Graph checkout,
generates four synthetic snapshots, and uses a Go test overlay to exercise Brain's
actual stream reader without editing the consumer checkout. The check covers
schema acceptance, summary merging, retained health metadata and diagnostic
code/detail, re-serialized snapshots, and completeness-to-trust mapping.
It is a reader compatibility check, not a full SQLite indexing or deployment test.

Schema 1.3 advertises the optional fields; it does not negotiate completeness
policy. Brain maps provider `ok`/`degraded`/`unsafe` to `trusted`/`partial`/`low`,
so the requested 5% boundary changes those trust labels. This is intentional.
Brain versions that only declare an older supported minor accept 1.3 with a
newer-minor warning; do not suppress that warning without consumer-side evidence.
Brain also has a separately computed completeness/freshness axis: its legacy
10% parse-error rule is not changed by this Graph release. Consumers requiring
the same policy on that axis need a separate coordinated update.
