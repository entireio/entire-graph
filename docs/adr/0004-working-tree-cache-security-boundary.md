# ADR 0004 — Working-tree cache security boundary

Status: Accepted
Date: 2026-08-21
Supersedes: [ADR 0003](0003-working-tree-search-snapshot-cache.md). This
restores ADR 0002's original working-tree bypass for the search snapshot cache;
ADR 0002's committed-tree key-totality decision remains unchanged.

## Context

ADR 0003 allowed a working-tree query to reuse a `HEAD`-tree-keyed search
snapshot after `git status` reported that every relevant path matched `HEAD`.
That probe is not safe at this trust boundary. Git status applies clean and
long-running process filters selected by repository attributes and config, so
checking a repository the user has not audited can execute a command chosen by
that repository.

The comparison is also semantically mismatched: the provider parses raw
worktree bytes, while Git status compares the index with content after Git's
configured conversion filters. A status-clean tree therefore does not prove
that the bytes the provider reads are identical to the committed blobs naming
the cache entry.

## Decision

Working-tree search snapshots are not cacheable:

1. A query with working-tree options never loads or stores a search-snapshot
   cache entry. It builds through the existing fresh-worktree path.
2. Committed-tree (`--head`) cache behavior and key identity are unchanged.
3. The status-based cleanliness probe and its memoized verdict are removed.
4. The cache envelope retains its `worktree` discriminator so old entries, and
   any future safe implementation, cannot collide with committed-tree entries.

Working-tree caching may be reconsidered only with a bounded comparison of raw
worktree bytes against HEAD/index blobs that contains filesystem traversal and
does not invoke Git conversion filters or other repository-selected programs.

## Consequences

- A working-tree query always reports a cache miss and repeated interactive
  queries rebuild the snapshot, including on a clean checkout.
- `index` prewarms only the committed-tree namespace and therefore benefits
  later queries only when they use `--head` with the same cache variant.
- The previous two-second cleanliness-verdict window and explicit invalidation
  API no longer exist.
- This accepts a performance regression to keep indexing an unaudited
  repository from executing its clean/process filter commands.
