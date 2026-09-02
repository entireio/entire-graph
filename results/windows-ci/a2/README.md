# A2 compile-once shard evidence

This directory contains only sanitized, reviewable evidence for the Windows
compile-once experiment. Raw ZIPs, executables, command output, and cloud
identifiers remain in a permission-restricted operator archive outside the
repository.

- `experiment-summary.json` is the compact baseline, screening, repetition,
  resource, parity, failure, and limitation record.
- `artifact-checksums.json` maps the raw archive's relative paths to byte sizes
  and SHA-256 checksums without identifying the storage account.
- `top-level-timings.jsonl` is the sanitized baseline-04 timing input consumed
  by the workflow prototype. Each line contains only `Action`, `Package`,
  `Test`, and `Elapsed`.
- `cost-rate.json` records timestamped Azure Retail Prices API meters and the
  cost calculation.
- `randomization.json` records the screen-order draw made before accepted
  screening.
- `testmain-package-global-audit.md` records the process-semantics audit.
- `cleanup.json` records post-experiment resource absence after cleanup.

The fail-closed `.gitignore` ignores every other file in this directory. The
full report and reproduction commands are in
`docs/ci/windows-investigations/02-compile-once-shards.md`.
