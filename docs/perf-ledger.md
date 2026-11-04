# Performance ledger

Keep/revert record for performance changes. Each row states what was measured
before and after, on which repository, and why the change was kept or reverted.
Raw records live in the graph-eval workspace (`results/perf.jsonl`), by label.

| idea | baseline → result | verdict | why |
| --- | --- | --- | --- |
| SQLite sidecar on by default, no-op repair checks store meta instead of loading the store twice (Task 3) | kernel `index` no-op: base (store off) 50.7 s, re-run same day 56.3 / 56.4 s; base-store 104.0 s → 57.7 / 57.5 / 58.0 s, peak RSS 42.0 GB → 15.9 GB. Kernel cold `index`: base 854 s, base-store 1073 s → 1150 s (+35% over base, 44.2 GB peak). orbweaver cold `index` 2.43 s → 3.12 s, no-op 1.02 s → 1.19 s, cold `def --head` 2.39 s (store off) → 2.80 s | keep | No-op is within noise of base and no longer pays the second full decode base-store paid. The cold-index increase is the sidecar build, accepted as the cost of the focused lookup reads (measured in Task 5). |
