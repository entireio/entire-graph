# ADR 0003 — Working-tree search-snapshot cache: clean-tree eligibility

Status: Accepted
Date: 2026-08-16
Partially supersedes: [ADR 0002](0002-committed-tree-cache-key.md) — only its
"working-tree mode stays uncached" consequence. ADR 0002's committed-tree
key-totality decision is unchanged and remains authoritative for cache
identity.

## Context

ADR 0002 recorded, as an explicit non-goal, that working-tree query mode
stayed uncached: "dirty state has no durable key." That held for the provider
records cache and still does. For the interactive query family it produced a
bad default experience: `search`, `neighbors`, and `impact` read the working
tree by default, so the common case — an agent issuing several queries against
an unchanged checkout — paid a full graph rebuild per query.

The observation that unlocked caching is that a *clean* working tree does have
a durable key: it is content-identical to the committed `HEAD` tree, whose
hash is already a keyed term. The problem reduces to deciding when the
working tree is clean enough that a tree-keyed snapshot is valid, and failing
closed everywhere else.

## Decision

The search snapshot cache (`search-snapshot-v7`, family `search`, used by
`search`, `neighbors`, `impact`, and `index`) caches working-tree queries
under these rules, implemented in `worktreeSnapshotCacheable` and
`searchSnapshotKey` (`internal/sem/search_cache.go`):

1. **Separate identity for working-tree entries.** The key covers everything
   ADR 0002 made total — cache version, checkout path, repository identity,
   provider version, `HEAD` tree hash, profile, parse-size limit, resolved
   file cap, file-subset selection, ordered ignore/include paths and
   contents, and `.graphignore` contents — plus a `worktree` marker term.
   The on-disk envelope stores a `worktree` field that is revalidated on
   load, so a working-tree entry and a `--head` entry for the same tree never
   serve each other.
2. **Eligibility is repository-wide and conservative.** A working-tree query
   may load or store a cache entry only when every dirty or untracked path is
   provably irrelevant to the graph:
   - any dirty path with a supported (indexable) extension disables caching
     for the query;
   - any dirty extensionless path disables it (it could be a shebang script
     the graph would index);
   - any dirty root dependency manifest used for import resolution —
     `go.mod`, `package.json`, `tsconfig.json`, `pyproject.toml`,
     `setup.cfg`, `Cargo.toml`, `composer.json`, `pom.xml` — disables it,
     regardless of extension support;
   - dirty paths with known unsupported extensions that are not manifests do
     not disable caching.
3. **Fail closed.** If dirty-path enumeration fails, `HEAD` cannot be
   resolved, or the repository has no commits, the query builds fresh and
   caches nothing. A miss is acceptable; a stale hit is not.
4. **The provider records cache is unchanged.** Bulk streams (`snapshot`,
   `symbols`, `edges`) with `--worktree` still bypass their cache on both
   load and store. This ADR narrows ADR 0002's "never cached" statement to
   that cache only.

One deliberate performance concession: the clean/dirty verdict is memoized
per repository for two seconds, so a rapid query burst does not re-run `git
status` per query. An edit landing inside that window can be served the
pre-edit snapshot; the window is short by design and in-process callers can
invalidate the verdict explicitly.

## Consequences

- Repeated agent queries against a clean checkout hit the cache instead of
  rebuilding, which is the normal steady state of the installed agent guide.
- Any indexable dirty file turns reuse off repository-wide. Notably,
  `init-agents` writes indexable Markdown, so a freshly activated repository
  rebuilds on every query until the activation files are committed.
- `index` still prewarms only the committed-`HEAD` namespace; because of the
  `worktree` key term it cannot warm the default working-tree path.
- ADR 0002's silent-wrong-answer guarantees carry over: every term that
  shapes the graph is either keyed or revalidated, and eligibility only adds
  misses relative to a naive tree-hash reuse.
