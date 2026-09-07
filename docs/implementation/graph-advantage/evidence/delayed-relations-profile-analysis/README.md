# Delayed relations profile observer

This change adds a diagnostic-only scheduling option to the extraction-corpus
CPU profile harness. It does not alter provider behavior, the frozen corpus,
the 20-second profile window, the 8 MiB profile cap, or any request deadline.
No diagnostic was dispatched while preparing it.

`ENTIRE_GRAPH_EXTRACTION_CORPUS_RELATIONS_CPU_PROFILE_START_AFTER_NS` is an
optional decimal count of nanoseconds since the request started. It is read only
when relations CPU profiling is enabled. An absent value preserves the existing
immediate start on the first relations progress callback. A supplied value must
be non-negative and no greater than the existing 90-second latest-start bound.
A future diagnostic intended to observe the later part of a 120-second request
can pin the value to `88000000000`, retaining about 12 seconds for the fixed
20-second profile to stop, sync, and publish before the unchanged deadline.

The first relations callback claims one profiler lifecycle. For a delayed
start, the harness durably records `scheduled`, waits asynchronously, and
rechecks cancellation and the latest-start bound before calling the
process-global CPU profiler. The same goroutine owns start, the sampling wait,
stop, file sync, status publication, and completion. `Close` cancels and joins
that owner. A request ending before start records `cancelled`; a first callback,
timer wake, setup, or completed backend start beyond the safe bound records
`missed_safe_window` with a stable reason. If the backend crossed the bound
while starting, it is stopped before the lifecycle completes. Status-write
failure before launch prevents the backend from starting.

Status records retain the requested start and record the actual sampling start
only after the backend returns successfully. `actual_ns` remains distinct from
the fixed `window_ns`, so an early `Close` produces a complete, flushed artifact
without claiming that it sampled for the full window. Error text written to the
status file is bounded, and invalid option errors never echo the supplied value.

The existing relation counter is sampled in memory on existing progress
callbacks and copied into existing lifecycle status writes as
`relations_first`, `relations_latest`, and `relation_progress_events`. This adds
no per-callback disk write or timer. An increasing counter is evidence that
retained relation emission advanced during the observation. An unchanged
counter is inconclusive because work can continue between emission callbacks or
produce filtered or duplicate relations. Similar function stacks in two CPU
profiles are also inconclusive because different inputs can legitimately follow
the same code. Without advancing counters or more specific input identity, the
classification remains unknown rather than pathological repetition.

## Sources, fixtures, and validation

The change was derived from the approved implementation plan at
`entire-plan/entire-graph-advantage-implementation-plan.md`,
the repository `AGENTS.md` and generated graph-agent guidance, the current
Entire profiler/evaluation harness source, and the retained invocation-6 phase
and profile artifacts. No repository history, session transcript, competitor
implementation, or fetched repository was used.

The fake clock, controllable wait, and blocking backend regressions in
`internal/sem/extraction_corpus_relations_cpu_profile_test.go` were independently
authored for this change. They exercise bounded scheduling without a 90-second
sleep or a corpus request.

Validation used `go version go1.26.1 darwin/arm64`:

- `go test ./internal/sem -run '^TestExtractionCorpusRelationsCPUProfile' -count=1`
  passed in 1.709 seconds.
- `go test -race ./internal/sem -run '^TestExtractionCorpusRelationsCPUProfile' -count=1`
  passed in 2.882 seconds.
- The exact graph-provided verification command,
  `go test ./internal/gitutil -run '^TestListBoundedWorktreePathsTruncatesOneFieldPastRawBound$'`,
  passed in 0.272 seconds.
- `git diff --check` passed.
