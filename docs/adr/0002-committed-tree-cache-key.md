# ADR 0002: Committed-Tree Cache Key (Total over Graph-Shaping Inputs)

Status: Accepted; partially superseded by
[ADR 0003](0003-working-tree-search-snapshot-cache.md) (2026-08-16), which
replaces only this document's "no change to working-tree mode / stays
uncached" consequence for the search snapshot cache. The key-totality
decision below is unchanged, and the working-tree bypass still holds for the
provider records cache.
Date: 2026-07-28

## Context

`entire-graph` has two committed-tree caches of the same shape:

| cache | key | consumers | on by default? |
|---|---|---|---|
| search snapshot (`internal/sem/search_cache.go`, `searchSnapshotKey`) | `<cacheDir>/search/…/<sha256>.json.gz` | `search`, `neighbors`, `impact`, `index` | no, `searchFlags{Worktree: true}`, so only `--head` |
| provider records (`internal/sem/records_cache.go`, `providerRecordsKey`) | `<cacheDir>/records/…/<sha256>.json.gz` | whole-graph `snapshot`, `symbols`, `edges` | yes, those commands default to committed-tree |

`cacheDir` is `--cache-dir` else `ENTIRE_PLUGIN_DATA_DIR`: one flat global directory, shared by
every repository and every worktree on the machine.

The question this ADR answers: with N worktrees off one repository: branch A refactoring a
package, branch B renaming symbols, `main` moving on: can a cache entry written on one branch
be served on another, and what does the current keying cost?

Both keys hash the cache version, the **checkout path** (`filepath.Abs(repo)`), the provider
version, the **HEAD tree hash**, the profile, `MaxParseBytes`, `OnlyFiles`, and the **contents**
of any `--ignore-file` / `--include-file`. In `--head` mode the file set is otherwise a pure
function of the tree: `openSource` lists via `gitutil.ListFiles(ctx, repo, committedRevision)`
and filters with `loadExplicitIgnoreMatcher` (content-hashed) plus a tree-derived
`headIgnoreMatcher`. `.git/info/exclude` and `core.excludesFile` reach only the working-tree
path, which is never cached.

Measured on a binary built from `cdcb8db`, against an isolated clone with three linked
worktrees: `wtA`/`wtC` at `cdcb8db`, `wtB` at `6e4bfde`, 3 of 317 tracked files differing:

**Branch drift is already safe.** The tree hash is in both keys, so switching branches at one
checkout path misses:

```
symbols at cdcb8db = 5740 ; at 6e4bfde = 5736
byte-identical?    = no          cache entries = 2
```

Two worktrees cannot collide either, because the checkout path is in the key.

**But two inputs that do shape the graph are not in either key, and both produce a silent wrong
answer.** First, the file-listing cap: `capSourceFiles` truncates to
`resolveMaxSourceFiles(options.MaxFiles)`, which folds in `ENTIRE_GRAPH_MAX_FILES`. Neither the
option nor the effective cap is keyed or re-validated:

```
1. cap=5, cold  -> symbols = 28     entries=1
2. NO cap, warm -> symbols = 28     entries=1   <== WRONG
3. NO cap, cold -> symbols = 5740               <== correct
```

99.5% of the graph absent, carrying a `W_FILE_LIMIT` warning into a run that asked for no cap.
One `entire-brain` ingest with a bound poisons every later unbounded query on that tree.

Second, repository identity: `repoKey` resolves the GitHub remote, and **every** symbol and file
ID is `repoKey + ":" + …`. It is not keyed and not validated on a direct hit:

```
1. remote=entireio/entire-graph, cold -> repo_key=gh/entireio/entire-graph   id=gh/entireio/entire-graph:JSON:…
2. remote=someoneelse/a-fork,    warm -> repo_key=gh/entireio/entire-graph   id=gh/entireio/entire-graph:JSON:…  <== WRONG
3. remote=someoneelse/a-fork,    cold -> repo_key=gh/someoneelse/a-fork      id=gh/someoneelse/a-fork:JSON:…     <== correct
```

`selectiveSearchSnapshotFromFull` already rejects a `RepoKey` mismatch, which shows the
invariant is known; the two direct-hit paths and all of `records_cache.go` skip the check.

**And keying on the checkout path costs everything the tree hash was supposed to buy.** One
shared cache dir, three worktrees, two of them at a byte-identical tree:

```
### A. DEFAULT MODE (working tree; no --head)
wtA default run1     0.06s  entries=0   cache=0K
wtA default run2     0.04s  entries=0   cache=0K

### B. --head MODE, ONE shared cache dir
wtA --head cold      9.02s  entries=1   cache=428K
wtA --head warm      0.87s  entries=1   cache=428K
wtB --head cold      9.37s  entries=2   cache=856K
wtB --head warm      1.07s  entries=2   cache=856K

### C. wtC = THIRD worktree at the SAME COMMIT/TREE as wtA
wtC --head           9.04s  entries=3   cache=1284K
```

The cache is worth having (9.02s → 0.87s, **10.4×**). But `wtB`, differing in 3 of 317 files,
pays a full rebuild and a second full 428 KB entry; and `wtC`, whose graph is **byte-identical**
to `wtA`'s, still pays a full 9.04s build and a third 435 KB entry. N worktrees at one commit
cost N × 9s and N × 435 KB where 1 × would do.

## Decision

**The cache key becomes a total function of every input that shapes the graph: repository
identity + tree + all snapshot-shaping options. Nothing that can change the output is left
implicit, and every discriminating field is re-validated on load.**

Concretely, in both `searchSnapshotKey` and `providerRecordsKey`, and in both on-disk
envelopes and their `valid…` predicates:

1. **Repository identity (`repoKey`) joins the key** and is compared on load. Identity is what
   every symbol ID embeds, so an entry built under a different identity must miss, not be
   re-served. This replaces the checkout path as the *identity* term: the path is a proxy for
   identity that is simultaneously too strict (three worktrees, one graph, three entries) and
   too loose (identity can change under a fixed path).
2. **The effective file-listing cap joins the key**: `resolveMaxSourceFiles(options.MaxFiles)`,
   the resolved value, so `ENTIRE_GRAPH_MAX_FILES` is covered by the same term as the option.
3. **Cache version strings bump** (`search-snapshot-v5`, `provider-records-v2`) so entries
   written by the old keying are unreachable by name rather than merely unfindable by hash.

**The trade-off accepted: this only adds misses, never hits.** Every change above narrows what
counts as a valid entry. Runs that previously reused an entry across a remote-URL change or a
cap change now rebuild, correctly. We accept the extra cold builds and one extra `git remote`
subprocess per cache lookup, because a wrong cache hit is worse than a slow miss. We do not
take the sharing win in the same change, so no new reuse surface is opened alongside a
correctness fix.

## Consequences

**What this does NOT do, explicitly:**

- **No cross-worktree sharing.** The checkout path stays in the key, so `wtC` still rebuilds
  `wtA`'s identical graph and the third 435 KB entry is still written. Keying on identity alone
  is now *possible* (identity is in the key and validated), but not *safe* yet: on a cache hit
  `search.go:356` overwrites the response's `repoRoot` from the cached header, so a shared entry
  would report the wrong checkout path, and `records_cache.go` stores opaque NDJSON bytes, so
  restamping its header means rewriting the serialized first line. Both are separate changes
  with their own tests.
- **No per-file content-hash caching / incrementality.** The prize measured above: `wtB`
  differing in 3 of 317 files should reuse ~99% of the parse, not 0%: needs a per-blob parse
  cache keyed by blob hash plus relation re-resolution over the union, which is a new index
  layer, not a key change. This ADR is its prerequisite: a per-blob cache is only sharable
  across worktrees if repository identity is a keyed, validated term, which it now is.
- **No change to working-tree mode.** It stays uncached; dirty state has no durable key. The
  0-entry rows in the measurement are that policy, not a write failure.
- **No change to tree-only keying of the parsed graph.** Two commits sharing a tree still share
  an entry, and `FILE_CHANGES_WITH` co-change edges are still allowed to come from the other
  commit's history: already documented on `searchSnapshotKey` and unchanged here.
- **No fix for the linked-worktree `.git/info/exclude` ENOTDIR failure** seen while measuring
  (`read ignore file ".../wtA/.git/info/exclude": not a directory`, exit 1). That is
  working-tree-only, therefore cache-irrelevant, and is owned by a separate change.

**What it costs:** every existing cache entry is orphaned on disk under the old version
directory; the first run after upgrading is cold. One additional `git remote` invocation per
committed-tree cache lookup, alongside the two `rev-parse` calls already made.

**What it buys:** the two measured silent-wrong-answer paths become misses, and the key is now
defensible as complete: the audit above enumerates the committed-tree inputs, and after this
change each one is either hashed or provably tree-derived.

**Verified on the same three-worktree lab, with a binary built from this change.** Both holes
close and nothing else moves:

```
HOLE 1  NO cap, warm -> symbols = 5740  entries=2   (was 28, entries=1)
HOLE 2  remote re-pointed, warm -> repo_key=gh/someoneelse/a-fork   (was gh/entireio/entire-graph)
branch switch at one path -> 2 entries, 5740 vs 5736 symbols   (unchanged)
wtA --head cold 9.75s -> warm 0.86s                            (warm win unchanged, 11.3x)
wtC --head at wtA's identical tree -> still a full build, third entry   (unchanged, by design)
```

## Addendum: what upstream had already fixed

This ADR was written against an older base. Rebasing onto current `main` showed upstream had
independently keyed `searchSnapshotKey` on repository identity and bumped that cache to
`search-snapshot-v6`, so the identity half of the problem was already solved **for the search
cache**. Two gaps remained and are what this change actually closes:

- `searchSnapshotKey` did not fold in the resolved file cap. Bumped to `search-snapshot-v7`.
- `providerRecordsKey` folded in NEITHER identity NOR the cap, and it is the cache that is on by
  default. Bumped to `provider-records-v2`; `LoadProviderRecords`/`StoreProviderRecords` now take a
  `context.Context` so the key can include `repoKey(ctx, repo)` the way the search cache does.

Still explicitly NOT done, unchanged from the original decision: cross-worktree sharing (blocked by
`repoRoot` being overwritten from the cached header) and per-file content-hash incrementality, for
which a total key is the prerequisite.
