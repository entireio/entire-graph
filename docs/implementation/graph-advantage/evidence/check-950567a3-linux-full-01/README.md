# Immutable Linux full check for `950567a3`

The complete 2,398-file source archive for
`950567a3c2796c99c9f46af84f7c0a8afac3351d` passed content and normalized
Git executable-mode identity before and after execution. The runner left
`HOME` unset and reused checksum-verified mise 2026.4.11 plus the existing
Go 1.26.1 installation through an offline mise link.

The unchanged tracked `mise run check` passed in 799.164 seconds: formatting,
vet, the complete race suite, build, and the status-line task completed.
`internal/sem` passed under the race detector in 476.837 seconds. The
status-line shell driver reported 145 passed, zero failed, and three explicit
platform or ownership skips retained in `result.json`.

This archive establishes correctness for the exact source commit above. It is
not product, corpus, stability, performance, or release-gate evidence. No
retry, evaluator compile, or product/corpus invocation occurred in this run.
The cleanup capture confirms all three validation VMs were deallocated.

The source and input archives remain local and untracked. The downloaded
`results.tar.gz` is retained as the immutable raw-result archive; its hash,
the raw log hash, and the controller and remote producer hashes are recorded in
`result.json`. `source-provenance.json` records the source recipe, exact
source/input/manifest hashes, file count, and unsigned Azure input-blob
locator.
