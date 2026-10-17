# Corpus-IDF retrieval comparison

`corpus-idf-cases.json` fixes twelve task queries and relevant source-file labels
across three local repositories at exact commits, evaluated with both `fast` and
`full` profiles. Labels were selected from source before running the comparison.
The Python 3.12+ POSIX driver verifies every checkout and gold path before
executing either binary. It exports each pinned tree into a temporary filesystem
corpus; it never fetches or changes the original checkout.

Build the baseline without the corpus-IDF change and the candidate with it, then run:

```sh
python3 scripts/compare-search-ranking.py \
  --baseline /path/to/baseline-entire-graph \
  --candidate /path/to/candidate-entire-graph \
  --manifest bench/search/corpus-idf-cases.json \
  --repo graph=/path/to/pinned-graph \
  --repo brain=/path/to/pinned-brain \
  --repo agents=/path/to/pinned-external-agents \
  --output results.json
```

Both binaries search the same exported files with `--worktree --no-cache`, a
fixed 96-file semantic parse limit, `GOMAXPROCS=4`, top-k 10 and a 24576-byte JSON
result budget. This isolates scoring from Git preselection and index reuse. The
report retains returned file paths, first relevant rank, backend,
completeness and partial failures, plus corpus commits and binary/manifest SHA-256
digests. It reports file-level recall at 1/5/10 and reciprocal rank at 10, and lists
every case whose first relevant file moved down or disappeared. Normal result
sections are included; this is a file-location metric, not symbol correctness.

An initial comparison using warmed Git-backed indexes timed out after 180 seconds
on the baseline's Git preselection for the linked-worktree query. That is an
operational limitation, not a ranking miss, and is excluded from the filesystem
metrics. Git-backed cold/warm statistics and unread-file behavior have separate
regression tests in `search_corpus_idf_test.go` and `search_corpus_idf_git_test.go`.

This is a small, developer-authored cohort of Go-heavy projects, not a held-out
general relevance benchmark. Missing targets remain misses rather than being
removed. Passing it does not establish improvements on every repository or
language. Single-query wall times include operational noise and are not a speed
comparison. These metrics do not establish Git-backed end-to-end latency.

The checked-in results are review evidence, not a CI replacement. The normal Go
suite also runs the offline retrieval regression cases in `search_eval_test.go`.

## Recorded comparison (2026-10-15)

`corpus-idf-results.json` records all 24 profile/query cases. The candidate improves
the first relevant file rank in three cases, with no regressions. MRR@10 changes from 0.6875 to 0.7465;
recall@1 from 14/24 to 16/24, recall@5 from 20/24 to 21/24, and recall@10 stays
21/24. The three misses remain in the aggregates. Production-source fingerprints
show that only `internal/sem/search.go` differs between the compared builds.

`git-preselection-probe.json` records the separate baseline Git timeout and its
corpus pin. It is excluded from these scoring aggregates, not counted as a pass.

The baseline production-source fingerprint matches core candidate commit
`5f9f6cfba223ceb64b8bcd404891df7fef818554`, merged into main as
`ba38a3e94e08cc9afaefeed3f7d01c6d0e8a572e`. The result file also records the
candidate search-source Git blob for reproduction independently of merge metadata.
