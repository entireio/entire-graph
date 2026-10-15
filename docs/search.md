# Search results and ranking

`entire graph query` turns a plain-language task description into ranked
source regions. This page describes what comes back and how to read it,
verified against current output. `entire graph query --help` documents the
flags. Supply the query with `--query`, or as one quoted argument after all flags:

```sh
entire graph query --repo . --format text "where is token refresh handled?"
```

Use one form at a time. Queries beginning with `-` require `--query`.

## Ranking

Ranking is hybrid: lexical scoring over bodies, identifiers (camelCase and
snake_case aware), signatures, and paths, expanded through the code graph to
callers, callees, usage sites, and same-container neighbors of strong lexical
candidates. Each result carries a `signals` array naming why it ranked (for
example `path`, `body`, `symbol-name`, `graph:callers`, `complete-symbol`), so
a surprising hit can be audited instead of trusted.

Results are byte-budgeted to drop into an agent's context: top hits carry full
snippets, later hits shrink to locators, and `--max-context-bytes` bounds the
total (`0` removes the bound). `--top-k` caps normal results, including automatic
related sites and covering tests. Explicit `--callee-hop` is an exception: it may
add called-helper results beyond that cap, under its existing byte-budget and
ranked-source protections.

## What a response contains

The default format is JSON, one object per query:

- `results`: ranked hits. Each has `file_path`, `start_line`/`end_line`, a
  `focus_line`, the symbol's `kind`, `qualified_name`, and `signature` when the
  hit is a known symbol, the `signals` that ranked it, and a source `snippet`.
  Trailing entries in `section: "related"` are zero-scored context, such as
  callers, siblings, or a covering test adjacent to the real hits.
- `literal_cluster`: occurrences of a distinctive literal from the top hits
  across the repository, tagged by role (edit site vs consumer).
- `verify_command`: a suggested check, with the evidence it was derived from
  and a `tier`: `narrow` (a focused test), a suite fallback, a build check, or
  a residual floor when nothing narrower can be derived. Text and agent
  formats render it as a `VERIFY:` line. It is a suggestion constructed from
  repository contents; read it before running it (see
  [trust and security](trust-and-security.md)).
  Language-specific suite fallbacks require a matching source or covering test;
  a neighboring manifest alone does not assign an unrelated language's runner.
  Native Go inputs require bounded, same-package evidence (and a real `import
  "C"` for cgo inputs). This does not evaluate platform/build constraints or
  infer SWIG-generated sources. The `targets` description distinguishes a whole
  suite from an identified narrow command.
- `stats`: counts, byte accounting, latency, and `index_cache_hit`.
- `warnings`, `partial_failures`, `completeness`: machine-readable coverage:
  which languages and how many files/symbols/relations backed this answer, and
  what was skipped or failed.

## Formats

- `json` (default): everything above; what the installed agent guide's
  command produces.
- `ndjson`: the same data as a record stream with a trailing
  `search_summary`.
- `text`: human-readable tiers with full snippets for top hits, terse locators
  after, then the `VERIFY:` line. Does not report cache state.
- `agent`: compact ranked output opening with a latency/cache header
  (`Index: cache-hit (…ms) | Query: …ms | …`), degrading gracefully under
  tight byte budgets.

## Complete bodies in agent output

An agent result header may carry `[complete]` when the producer certified the
whole primary symbol body and display preserves its source bytes. A clipped
unit, a head window, or a body rewritten by terminal escaping or quarantine is
not certified. File names and other metadata escape the reserved token so they
cannot impersonate this header annotation. JSON/NDJSON retain the original data.
Text output also omits the `complete-symbol` signal when display changes the body.

For certified bodies, the agent renderer emits the whole body with the marker
or falls back to an unmarked locator if the budget cannot hold it. Ordinary
uncertified results retain excerpt fitting and trailing-line behavior. A genuine
final source blank line is preserved only on the certified path; an ordinary
line terminator does not become an extra source line.

Marker absence is conservative: it does not prove that a body is incomplete.
After allocation, selected primary results can also receive certification when
identity, symbol bounds and exact source bytes agree and the signal fits the
remaining serialized budget. Other already-visible callables may stay uncertified. `--full-unit-top` requests explicit unit expansion;
a safety-clipped forced unit still cannot claim completeness. Renderer tests
cover certification through both opportunistic enclosure and forced-unit paths
without adding signals after the producer's byte budget has been finalized.

## Profiles

Term-rarity weights use repository-wide content frequencies when the search can
establish exact counts. Bounded or unread-file cases that cannot establish them
fall back to statistics from the inspected sample. This corrects a bias from
counting only files preselected for matching the query, and can change result
order. Other ranking inputs, such as average candidate length, can still vary
between backends. The [pinned scoring comparison](../bench/search/README.md)
records a limited before/after cohort and its coverage boundaries.

`query` defaults to `--profile fast` (shallow, high-precision call
resolution). The installed agent guide asks for `--profile full`, which
enables the complete relation set and deeper graph expansion. Profile is part
of the cache key, so mixing profiles across runs builds separate cache
entries. See the [operations cache guide](operations.md#cache).

The guide also asks for `--format agent`, which is a cost choice rather than a
depth one. Measured on one query against this repository, `--profile full` with
the default json rendering came to 17595 bytes and the same query with
`--format agent` to 6011 -- 2.93x cheaper for the same results. json carries a
fixed diagnostic floor (`repo_ignored`, `warnings`, `completeness`,
`partial_failures`, `stats`) that `--max-context-bytes` does not bound, and the
agent renderer is the only one whose ceiling binds the whole payload rather than
`results` alone. Profile is not where the cost is: `full` versus `fast` differed
by 42 bytes on the same query.

Session replay requires a clean VCS revision stamped into the producing binary.
Unstamped or modified builds always search again; a changed producer invalidates
the stored answer. This identifies a source revision, not the toolchain or build
flags. Binaries installed without VCS metadata trade replay reuse for freshness.

Declaration rendering preserves the parser’s name line when an uncertified source
window would otherwise omit it. Exact-identifier answers retain every matching
definition or fall back to the ordinary ranking. Certified source keeps its whole
body and completeness marker, including in exact-name answers; when that body
cannot fit, the ordinary renderer uses an unmarked locator. Additional passages
cannot spend the primary location’s reserved space. Declaration floors are used
only when they collectively fit, preserving the bounded rank-weighted allocator.
