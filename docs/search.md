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
total (`0` removes the bound). `--top-k` sets the result count.

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

## Profiles

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

## Opt-in semantic channel

With `ENTIRE_GRAPH_SEMANTIC_ENDPOINT` (a localhost/loopback URL) and
`ENTIRE_GRAPH_SEMANTIC_MODEL` both set, a `--head` query built against an
`entire graph index --semantic` index also asks a local embedder for the
nearest functions and interleaves them with the lexical ranking, embedding
first. Unconfigured, the payload is byte-identical to a build without the
channel: none of the fields below appear.

- `results[].semantic_score`: the channel's cosine for a row it matched, on
  its own scale; never copied into `score`. A row the channel synthesized
  (no lexical candidate existed) carries the `semantic:only` signal and a
  `score` of 0, meaning "not measured", not "worthless". A lexical row the
  channel also matched keeps its measured `score` and gains the
  `semantic:embedding` signal.
- `stats.semantic_status`: `used`, `off:flag`, `off:worktree`, or
  `unavailable:<reason>` (which also adds a `W_SEMANTIC_UNAVAILABLE` warning).
- `stats.semantic_results`: delivered primary rows carrying the
  `semantic:embedding` signal, which counts both rows the channel added and
  lexical rows it merely matched. Count `semantic:only` rows for the former.
- `stats.semantic_nominated_files`: files outside the lexical selection
  loaded so the channel's rows can be rendered (at most 10).
- `stats.semantic_evicted_files`: lexical files that gave up their parse slot
  to a nomination. On a preindexed (warm) search nominations are additive and
  this is always 0. On a cold search nominations count against
  `--max-indexed-files`, and an evicted file's lexical rows are not produced.

On a preindexed search the channel does not change the lexical ranking at
all: the lexical candidates, their scores and idf are exactly what an
unconfigured search computes, and the nominated files' symbols join only
after the lexical ranking is final. On a cold search the one difference is
eviction: idf and query-word presence still come from the full lexical
selection, but the evicted files' rows are gone and, with their symbols, so is
their share of BM25's average document length and any call edges they
carried, so surviving scores can shift slightly. The fused order is the
delivered order, so `results` is not necessarily descending by `score`.

`format_version` stays 1: every field above is additive and omitted when the
channel is not configured.
