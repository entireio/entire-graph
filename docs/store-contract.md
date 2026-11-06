# Read-only store contract for local consumers

`entire graph store --repo <checkout> --profile full` resolves the search store
for the checkout's committed HEAD and current provider/policy identity. It never
builds an index. `--ignore-file` and `--include-file` select the same policy variant
as `graph index` and `graph context`. Build using `graph index --head` when no
store exists.

The store is written by default: `graph index --head`, and a cold `--head`
lookup (`def`, `neighbors`, `impact`, `explain`) that persists a complete entry,
write it beside the gz entry. `ENTIRE_GRAPH_SEARCH_STORE` set to `0`, `false`,
`off`, `no` or the empty string turns both the write and the read off; unset or
any other value leaves them on. A no-op `graph index --head` whose store is
missing or stale rebuilds it from the entry. Whenever a store write fails (a
full disk, for example), on that rebuild or on a cold build by `index`, a
`--head` lookup or `query`, the command still succeeds and its `warnings` carry
`W_SEARCH_STORE_UNAVAILABLE` with the reason (`explain`'s `warnings` is present
only when it carries one); lookups then take the full snapshot load. After a
search-store version bump, an existing store is treated
as missing: lookups fall back to the full snapshot load (correct, slower) until
`graph index --head` or the next cache-miss write rebuilds the store.

The JSON descriptor carries `contract_version: 1`, an absolute `path`,
`entry_digest`, indexed `commit` and `tree`, a provider `header`, and file/symbol/
relation counts. A missing or incompatible entry yields `store_unavailable:true`.
Consumers must resolve per operation, open SQLite with `mode=ro&immutable=1`, and
compare the JSON-encoded `meta.entry_digest` on that connection to the descriptor
before reading. Publication replaces the file atomically; a digest mismatch is a
retryable resolution race. Never persist the path as a portable artifact.

Graph keeps the newest three stores per repository and checkout scope, plus
any store built for the publishing checkout's current HEAD tree, and removes
older ones when it publishes a new one. `ENTIRE_GRAPH_SEARCH_STORE_KEEP` changes
the count; 0 or a negative value keeps every store. The gz snapshot entries are
never removed, and `graph index --head` rebuilds an evicted store from its entry
without reparsing. A store removed after it was resolved reads as
`store_unavailable` on the next resolution. On POSIX a connection that already
has the store open keeps reading it; on Windows an open store is not removed.

The stable SQL surface consists of `brain_symbols_v1`, `brain_relations_v1`,
`brain_files_v1`, `brain_reverse_relations_v1` and `brain_symbol_fts_v1`. These are
views over Graph's tables, including typed JSON projections of provider records.
They preserve Brain's existing inspection shapes without another database.
Consumers may create connection-local TEMP aliases and join their own runtime
observations, but must not modify Graph tables. Contract changes require a new
version; internal table changes remain Graph's responsibility.

The file view exposes content_hash as git-blob:<blob> from the indexed file record,
or an empty string when no blob is available. It never hashes the live working
tree, which may differ from the committed source.

The search-store cache format is v5 so older stores are rebuilt. v4 added the views; v5
adds the indexed symbol columns name_fold, qname_fold and container_id and the
relation_type_counts meta key, for focused `--head` lookups.
This does not change provider schema 1.x or symbol identity. Release this contract
before the Brain consumer that retires its duplicate code graph.

## Store key

A store is a sidecar of one search snapshot entry and shares its key. The key is
`searchSnapshotKeyForSchema` (`internal/sem/search_cache.go`) and hashes exactly:

- the search cache version and the provider schema version;
- the repository key class (`gh/<owner>/<name>`, or one term for every `local/` key)
  and the checkout scope (Git's `--show-prefix`);
- the **provider version**;
- the **tree** (`HEAD^{tree}` of the committed HEAD; the commit is not in the key);
- the profile, max parse bytes, the resolved file cap and any only-files list;
- the built-in secret rules digest;
- the **ignore policy**: each `--ignore-file` and `--include-file` path with its
  content, and the repository-root `.graphignore` content or its absence.

A trigger that wants to know when a new store is needed (Brain's watch step) can
therefore watch three inputs: the HEAD tree, the provider version from
`entire graph version --json`, and a hash of the ignore files it passes
(`--ignore-file`) plus `.graphignore`. The other terms are fixed per caller.

## Ancestor fallback for `graph context`

Feature-detect it first: `entire graph capabilities --json` reports
`"context_allow_ancestor": true` in `optional_local_only_features`. The key is a
flat boolean; a provider without it does not support the flag.

`graph context --allow-ancestor N` (N from 1 to 5, default 0 = off) serves an
exact miss from an ancestor commit's store instead of returning
`store_unavailable`. Without the flag, and whenever the exact store exists, the
output is unchanged. `graph store` never serves an ancestor, and its descriptor
refuses (exits non-zero) a store whose recorded tree differs from the tree it was
keyed by.

On an exact miss Graph walks the first-parent history of the committed HEAD and
takes the newest of the N nearest ancestors that has a store under the request's
key with only the tree replaced. An ancestor built by another provider version, or
under another ignore policy, is therefore never eligible. That ancestor serves
only when:

- the paths that differ between its tree and the requested tree are listed
  completely (`git diff-tree -r --no-renames` between the two trees, in the store's
  scope coordinates, with lazy fetching disabled), and
- they number at most 5% of the files the ancestor's store indexed.

Otherwise, including a failed diff, a missing tree object in a partial clone, a
path that is not valid UTF-8 or an unreadable store, the response is the exact
miss byte for byte: `store_unavailable: true` and the same `detail`, with no
`stale` block.

An ancestor answer withholds every symbol and test whose file changed, and every
relation of a returned symbol whose other endpoint is a symbol or file node in a
changed file. It carries this block:

```json
"stale": {
  "ancestor_commit": "<first-parent ancestor whose store answered>",
  "ancestor_tree": "<its tree>",
  "commits_behind": 1,
  "changed_files": ["path/renamed_from.go", "path/renamed_to.go", "path/deleted.go"],
  "excluded_records": 4
}
```

| Field | Meaning |
|---|---|
| `ancestor_commit`, `ancestor_tree` | The ancestor whose store answered. Top-level `commit` and `tree` keep their meaning: what the served store records. |
| `commits_behind` | First-parent distance from the requested commit, 1 to 5. |
| `changed_files` | Every path changed between `ancestor_tree` and the requested tree, sorted, never truncated. A rename appears as both of its paths; a deletion as its old path; an added path the ancestor never indexed appears too. File paths only (gitlinks included), relative to the store's scope. Empty only when nothing in scope changed. |
| `excluded_records` | Records withheld from this answer: task-matched symbols and tests in changed files, plus relations of the returned symbols that touch a changed file. |

The block's absence means the answer is exact. Records outside `changed_files` are
as of `ancestor_tree`: a relation from an unchanged file can still resolve
differently in a fresh build when a changed file added or removed its target, so an
absent result is not proof of absence in the requested tree.
