# Relations CPU profile analysis at source 90ac3e16

## Proven profile facts

The input is `../diagnostic-dispatch-90ac3e16/controller/raw/output/relations-cpu.pprof`, SHA-256 `3f9b2fbe80daff71816e2d6cf2e8d66db0203cda5b08aca758aaa88a47cab88b`, 29,973 bytes. Its status sidecar says the profile is complete, triggered at 53.435075004 seconds, requested a 20-second window, and recorded 20.028516076 seconds. The profile contains 23.27 CPU-seconds over 20.02 wall-seconds; percentages therefore describe sampled CPU, including concurrent runtime work.

`forEachRelation` owns 19.98s cumulative (85.86% of sampled CPU). `goHTTPRouteRelations` owns 17.80s (76.49%). Its call at provider.go:23693 owns 17.71s, divided into:

- `goHTTPRouteRegistrations`: 10.12s (43.49%). The group scan costs 2.83s and the four registration scans cost 1.77s, 1.67s, 1.81s, and 1.93s. Compiling those four regexes totals only 0.11s, so precompilation alone does not address the dominant cost.
- `staticStringConstants`: 7.59s (32.62%), of which `staticStringAssignRe.FindAllStringSubmatch` owns 7.49s.
- Runtime GC accounts for 3.13s cumulative (13.45%), consistent with the regex matching and submatch allocation visible in the dominant stacks, though the profile does not attribute all GC work to one caller.

## Likely algorithmic cause and smallest change

At exact source 90ac3e16, `goHTTPRouteRelations` calls `constantsByFile.forFile(file.Path, content)` and `goHTTPRouteRegistrations(...)` for every Go file. This performs one to five whole-file static-assignment scans and five whole-file route regex scans even when a file contains no supported route-registration spelling. The profile proves these scans dominate this sample. The source proves they are unconditional per Go file. The profile does not contain per-file counters, so the proportion of non-route Go files remains unmeasured.

The smallest safe candidate is a conservative, allocation-free content predicate in `goHTTPRouteRelations`, before handler-map construction and before `constantsByFile.forFile`. It should return true whenever the content contains any literal token required by the supported regex forms: `HandleFunc`, `Handle`, `.Group`, and each supported upper/title-case router method name. False positives are harmless because existing parsing still decides matches; the predicate must be tested exhaustively against every currently accepted registration form so it introduces no false negatives. Files failing that predicate skip constants extraction and route regex scans. Existing registration semantics and relation output remain unchanged.

A bounded proof should use the existing Go HTTP route registration tests plus a focused test with many large non-route Go files and one valid file, instrumenting the content/constant-scan boundary with a test-only observer or separable predicate assertion. It should establish output equivalence and that non-candidates never enter `staticStringConstants`/registration parsing. It should not use wall-clock thresholds. A fresh profile would be needed later to measure effect; this analysis alone makes no performance or timeout-resolution claim.

## Limitations

This is one 20-second interval from 53.435s to 73.464s in a run killed at the 120-second product timeout. It can miss work before and after the window, cannot establish the final relation count or completion point, and cannot prove this hotspot alone caused the timeout. Source mapping used the exact 90ac3e16 blobs. The local analysis tool is Go 1.26.1 darwin/arm64, while the recorded profile came from Linux evaluator build ID `6973dbcc31ce12953f9b0454a42c5f5f1c440925`.
