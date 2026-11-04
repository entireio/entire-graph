# Performance ledger

Keep/revert record for performance changes. Each row states what was measured
before and after, on which repository, and why the change was kept or reverted.
Raw records live in the graph-eval workspace (`results/perf.jsonl`), by label.

| idea | baseline → result | verdict | why |
| --- | --- | --- | --- |
| `entire graph statusline` render: stats collector in-process, figures cached under `<cache>/statusline/`, indexed state from `sem.HeadIndexed` (stats only) (Task 9) | Windows, the largest transcript under `~/.claude/projects/O---web-omnith/` (16.7 MB plus 37 subagent transcripts, copied; 15 lookups), repo entire-graph, 20 runs each, wall time of the whole process: cold cache p50 757 ms / p95 908 ms; miss after an append (figures and the main file's stats memo miss, subagents hit) p50 512 ms / p95 524 ms; warm hit p50 409 ms / p95 420 ms. In-process breakdown of one render: git metadata validation 229 ms, HEAD read 40 ms, `HeadIndexed` 100 ms (HEAD, remote URLs and scope prefix for the entry key, plus the policy capture), transcript listing 80 ms, re-parsing the 16.7 MB main transcript about 100 ms; process start 18 ms | keep, over budget | p50 is above Claude Code's 300 ms debounce in every case, including a hit. Parsing is the smaller share: a hit already costs 409 ms, of which about 370 ms is fixed (the git metadata validation, three git subprocesses and the transcript listing). Follow-ups: incremental (append-offset) transcript parsing as the brief asks, and before that the fixed cost, chiefly the per-process metadata validation and the second HEAD resolution `HeadIndexed` repeats. No background refresher. |
