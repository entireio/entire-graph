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
