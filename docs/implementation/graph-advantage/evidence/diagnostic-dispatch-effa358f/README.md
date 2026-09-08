# Eighth delayed Kubernetes snapshot diagnostic

Dispatch `p1-diag-effa358f-k8s-late-relations-off-01` ran exactly one
cache-OFF, full-profile snapshot against frozen Kubernetes commit
`b2ec8b6fefac451a2dedafc4dd71f2f16c7a6abe`. The one worker arm was reserved,
started, and failed at the 120-second deadline. There was no ON arm, warm-up,
retry, comparison, admission, or clean batch. This was the eighth historical
product invocation.

The run used evaluator source
`effa358f2ceaca2b9accd829984272598fa78078`, compiled binary SHA-256
`f2c2940a565397a010488843af99ff469c725dd54a720c90ed190311249ed1f6`,
retained build-manifest SHA-256
`fcb17b1b279acbfd80bde9f753b1fcad0e6cae2aef55db88e55bb319a0cecb4c`,
and frozen input-manifest SHA-256
`d2fdce2a59befb3a0a02bcc7fc5a531eb8571a1788b0070b6fd2147e92e273e0`.
The before and after repository identities match: effective input SHA-256
`d7a25ec35c9720efead0ac3f3dccc493385f6f4bc8c42d2f0313e2afbc9e4db4`
and tracked-manifest SHA-256
`7112c36429cb59223135260d8119a88158edd65786633a0efca30b32a0a719d1`.
The post-run control identity check reported no mismatch.

The inventory phase ran from 11.655464286 through 11.954399241 elapsed
seconds. Parsing ended at 53.803566914 seconds after processing 30,866 files
and 418,711 symbols. Relations started at that same elapsed time and did not
emit a phase-end event before the process was killed at the deadline. GNU time
did not retain a usable peak-RSS record after the kill, so peak RSS is unknown.

The relations CPU profile is complete. It started at 88.012898811 elapsed
seconds for 20.031817991 seconds, within the requested 88-to-90-second start
bound. The 44,928-byte profile has SHA-256
`9a453aee29907d31995e3009fe779e06eaaa06d2c95dc4648fc04407c0229b4c`.
It completed at approximately 108.044716802 elapsed seconds. Its first/latest
relation values are 512/672,256 with 1,313 events. Those counters are cumulative
from the first relations callback at 53.803566914 seconds through profile
completion; they do not isolate progress inside the profile window.

Offline profile-only symbolization records 31.83 CPU-seconds sampled over the
20.02-second wall window. The cumulative view attributes 25.10 seconds to the
relation worker path, 22.69 seconds through regexp execution, 9.63 seconds to
`returnFlowCalls`, 6.97 seconds to `receiverCallRelations`, and 6.28 seconds to
the runtime GC drain. These are overlapping cumulative samples, not elapsed
time or a benchmark comparison. The exact evaluator binary was not supplied
to local `go tool pprof`; the profile's embedded function symbols were used.
No optimization or timeout-cure conclusion is made here.

The controller and transport exited zero after retaining the failed collector
result; the collector exited one and the product process was killed with exit
-9. `controller/results.tar.gz` has SHA-256
`a874da2a08ca701e3dc6a7e67be04fcf80725d1bf0b4d442d5adb70aabf8e40e`
and contains 29 regular raw files. Their hashes matched the initial extraction;
one generated Python cache file from that extraction is preserved only inside
the immutable result archive and is excluded from the retained raw tree. All
three VMs are `VM deallocated`.

## Preparation and correctness boundary

The package was committed at `81f68e49d04a67e3eb1b0f0aa301878f033f691d`
after the inherited controller suite passed all eight tests in 0.987 seconds
and the no-execute preflight passed without creating a claim. The immutable
Linux full check at `a54a0a6440e705690bc3efdf21f67b236c56a9a3`
passed but disclosed 52 skipped Go tests and three statusline skips. The pinned
compiler run at `0007d4b50af1757b8502d621b7b0abc6f6424e8c`
passed its selected 28-test set including ten live tests, then compiled this
evaluator without running it. These checks and this diagnostic do not establish
full correctness coverage, performance, stability, admission, release, or a
timeout cure.

## Consulted sources

- Current source at commit `effa358f2ceaca2b9accd829984272598fa78078`.
- `docs/implementation/graph-advantage/evidence/diagnostic-dispatch-950567a3/`
  as the reviewed controller, collector, budget, gate, and test template.
- `docs/implementation/graph-advantage/evidence/check-effa358f-linux-full-01/`
  and `docs/implementation/graph-advantage/evidence/diagnostics-linux-effa358f-compiler-01/`.
- This directory's controller, transport, claim, raw result archive, profile,
  process, identity, and VM terminal-state records.
- `entire-plan/entire-graph-advantage-implementation-plan.md`
  and the authorized `graph-advantage-progress-review-2026-09-05.md` review.
