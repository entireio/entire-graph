# A4 test-binary split evidence

This directory contains the compact, sanitized evidence for the Windows
`internal/sem` independent-test-binary experiment. The result is NO-GO: the
exact compile/list/TestMain gate passed, but the first four-shard execution
crossed 30 minutes and was terminated without dynamic event parity.

The ignore-all/allowlist policy in `.gitignore` prevents raw logs, generated Go
files, ZIPs, event streams, and Windows executables from entering Git. Local raw
material is under `live/` with owner-only permissions. The private Azure copies
were destroyed with the disposable resource group.

Files:

- `metrics.json`: accepted baseline, prototype gate, and rejected screens.
- `coverage-equivalence.json`: static/list/TestMain and dynamic parity states.
- `source-integrity.json`: target inventory, hashes, and dependency closure.
- `environment-summary.json`: sanitized machine, toolchain, and security facts.
- `azure-cost.json`: timestamped official retail rate and calculated VM costs.
- `failures.json`: bootstrap, screen, recovery, and cleanup outcomes.
- `checksums.txt`: SHA-256 checksums for the allowlisted evidence.

See `docs/ci/windows-investigations/04-test-binary-split.md` for the conclusion,
method, limitations, and reproduction commands.
