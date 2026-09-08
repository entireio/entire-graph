# Immutable Linux full check for `effa358f`

The complete 2,512-file source archive for
`effa358f2ceaca2b9accd829984272598fa78078` passed content and normalized
Git executable-mode identity before and after execution. The runner left
`HOME` unset and reused checksum-verified mise 2026.4.11 plus the existing
Go 1.26.1 installation through an offline mise link.

The unchanged tracked `mise run check` passed in 767.184 seconds: formatting,
vet, the complete race suite, build, and the status-line task completed. Nine
packages passed under the race detector, including `internal/sem` in 459.084
seconds; 13 grammar or support packages reported no test files. The Go run
reported 52 skipped tests, including ten opt-in live-compiler tests; the other
skips cover platform, permission, and explicit evaluation gates. The
status-line shell driver separately reported 145 passed, zero failed, and
three platform or ownership skips. Counts and the three status-line skip names
are retained in `result.json`; the raw log retains every Go skip name.

This archive establishes correctness for the exact source commit above. It is
not product, corpus, stability, performance, or release-gate evidence. No
remote retry, evaluator compile, or product/corpus invocation occurred. The
cleanup capture confirms all three validation VMs were deallocated.
Because the ten live-compiler tests were not enabled, this check does not
establish positive pinned-compiler behavior for this source.

One local controller-host launch failed before any Azure operation because a
non-login shell did not expose the already-installed Azure CLI. Its inert
cleanup loop was interrupted with exit 130. A login-shell preflight resolved
the existing `az`, `sha256sum`, and `bash` executables before the sole remote
full-check attempt. This preparation failure did not start a VM, upload an
archive, invoke RunCommand, or run the full check.

The source and input archives remain local and untracked. The downloaded
`results.tar.gz` is retained as the immutable raw-result archive; its hash,
the raw log hash, and the controller and remote producer hashes are recorded in
`result.json`. Its 14 raw files match the extracted copies by hash.
`source-provenance.json` records the source recipe, exact
source/input/manifest hashes, file count, and unsigned Azure input-blob
locator.
