# Pinned Linux compiler correctness and evaluator build

This package records one completed controller execution for source commit
`effa358f2ceaca2b9accd829984272598fa78078` in session `55365`.

The runner reuses the immutable Linux source already retained by
`check-effa358f-linux-full-01` at
`/opt/graph-validation/check-effa358f-linux-full-01/src`. It requires the
retained source archive SHA-256
`81c0ba6ba6f58c61270a6f70517b6ed23db806f1e64852ab5c69c9649a45fed3`
and the 2,512-file Git-mode manifest SHA-256
`ac4cf87f80b0140d1e9b0ebb21c5a8350a90d7e2e3e22d74ad073b79a33ef267`.
The local controller also requires the retained full-check before and after
manifests to match that hash and requires both the full-check and overall exits
to be zero before any cloud operation. No new source archive is created.

The only executable correctness stage is the accepted 6f23 pinned compiler
selection:

```text
go test -race -v -timeout 30m ./internal/compiler ./internal/sem ./internal/cli -run 'Test(Compiler|LiveCompiler|LiveAdvantage|LiveReview|MapLocation|RPC|Capsule)' -skip 'QualityEvaluation|ExtractionEvaluation|TestExtractionCorpusMeasurement' -count=1
```

`ENTIRE_GRAPH_COMPILER_LIVE=1` enables the positive Linux fixtures. Those
fixtures bind the compiler backend to `/opt/graph-tools/gopls` with its expected
SHA-256 and bind its launcher to `/usr/bin/bwrap`. The runner pins Go 1.26.1,
gopls 0.20.0 and their retained hashes, preserves the inherited `HOME` state,
and sets `GOPROXY=off`, `GOSUMDB=off`, `GOTOOLCHAIN=local`, telemetry off, and
disables Git global/system configuration and terminal prompting.

Correctness exit zero alone is insufficient for admission. Before the build,
the runner requires exactly 28 top-level passes, rejects every top-level or
nested failure or skip, and requires exactly one pass for each of the 10 named
live compiler tests established by the accepted 6f23 run. It uses the current
`ENTIRE_GRAPH_ADVANTAGE_LIVE_OUTPUT` and `ENTIRE_GRAPH_REVIEW_LIVE_OUTPUT`
interfaces and requires both retained outputs to be nonempty.

After correctness passes, the already-proven build step runs `go test -c` for
`./internal/sem`, records the evaluator binary hash and Go build information,
and never runs the compiled evaluator. Binary existence, hashing, and `go
version -m` must all succeed before the run can report overall zero. Before and
after manifests bind both stages to the same remote source. Any first-stage
failure prevents the build.

The controller uses a fresh result namespace, retains the result URL only in
process memory, and deallocates all three resource-group VMs on exit. This
package includes no corpus invocation, product diagnostic, benchmark,
performance or retrieval study, evaluator execution, or full-check rerun.

The local preflight executes the actual source, toolchain, live-coverage, and
binary-metadata gates with temporary shims. Seven negative cases cover a wrong
source hash, wrong Go binary hash, wrong Go version, missing/skipped required
live test, missing live outputs, missing binary, and failed build-info capture.
One positive control reaches accepted build metadata. All eight cases passed;
no VM or network operation was used.

## Result

Controller, transport, correctness, correctness-admission, build, build
metadata, and overall exits are all zero. The correctness run passed exactly
28 top-level tests, including all 10 required live tests, with no failure or
skip. The two live output artifacts are retained as
`raw/compiler-advantage.json` and `raw/compiler-review.json`.

The compiled evaluator SHA-256 is
`f2c2940a565397a010488843af99ff469c725dd54a720c90ed190311249ed1f6`.
It was compiled once and was not executed. The retained results archive SHA-256
is `0fb14ec12f4e82526414e587f94b8685e7a836b08138ab90c1ffb910d07e07d7`
and contains 17 raw files, all matching the extracted copies. The expected,
before, and after source manifests are identical at SHA-256
`ac4cf87f80b0140d1e9b0ebb21c5a8350a90d7e2e3e22d74ad073b79a33ef267`.
`all-vms-terminal.json` records all three VMs deallocated.
