# Read-only store contract for local consumers

`entire graph store --repo <checkout> --profile full` resolves the search store
for the checkout's committed HEAD and current provider/policy identity. It never
builds an index. `--ignore-file` and `--include-file` select the same policy variant
as `graph index` and `graph context`. Build using `graph index --head` with
`ENTIRE_GRAPH_SEARCH_STORE=1` when no store exists.

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

The search-store cache format is v4 so older stores are rebuilt with the views.
This does not change provider schema 1.x or symbol identity. Release this contract
before the Brain consumer that retires its duplicate code graph.
