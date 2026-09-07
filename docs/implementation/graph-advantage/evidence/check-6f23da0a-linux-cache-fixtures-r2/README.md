# Linux cache-fixture correctness diagnosis, r2

This directory records the one authorized r2 Linux attempt against exact archived source commit `6f23da0ad4fa704da8c7bac53e1065030c89a4f1`. It repaired only the r1 temporary-archive dependency: the checked local archive with SHA-256 `e1a42113a94d043c79833ea9d16ae568965bdb50efdd7a5ccc5f3186c883032b` was uploaded in the fresh r2 namespace and downloaded to durable remote path `/opt/graph-validation/check-6f23da0a-linux-cache-fixtures-r2-source.tar.gz`.

Remote evidence records the existing source root and downloaded archive as present, download exit `0`, archive hash verification success, and no difference between a fresh archive extraction and `/opt/graph-validation/diagnostics-linux-6f23da0a/src`. Independent guards recorded Go `1.26.1` and gopls `v0.20.0`; `mise` was not available on the VM and was not installed. The tracked `mise.toml` SHA was recorded for comparison, while the intentionally bounded source archive did not contain `mise.toml`.

The single authorized command passed with test, toolchain, and overall exits all `0`:

```text
GOMAXPROCS=4 GOFLAGS=-p=1 go test -race -v -count=1 -timeout=5m ./internal/sem -run '^(TestPreindexProviderSnapshotReusesSameTreeAcrossCommits|TestSearchReusesSameTreeCacheAcrossCommitsAndReportsCurrentHEAD|TestWarmSelectiveDerivationFailureFallsBackToFreshBuild)$'
```

All three requested top-level tests passed, with zero failures or skips. This was a bounded correctness diagnosis of toy fixtures, not a benchmark, performance comparison, retrieval study, product invocation, or corpus run. No retry was performed. Controller cleanup and an independent post-run query both found all three validation VMs deallocated.
