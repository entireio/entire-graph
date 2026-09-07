# Relations CPU profile analysis at source 450bede9

## Profile result

The input profile is `../diagnostic-dispatch-450bede9/controller/raw/output/relations-cpu.pprof`, SHA-256 `8bec6b6a1d1a4d83ac1a3ba1c9addff49afbfefa840def5a7b240c54e86521c6`, 29,365 bytes. Its sidecar reports a complete 20.022278726-second window triggered at 54.142677379 seconds. The profile records 23.12 CPU-seconds over 20.01 wall-seconds, so percentages describe sampled CPU including concurrent runtime work.

`forEachRelation` owns 19.99s cumulative (86.46%). `goHTTPRouteRelations` owns 17.52s (75.78%), split between `goHTTPRouteRegistrations` at 9.78s (42.30%) and `staticStringConstants` at 7.47s (32.31%). `regexp.FindAllStringSubmatch` owns 17.05s (73.75%). This establishes the dominant stack in this window without treating it as a comparison with another window.

## Why the current guard admits unrelated files

At source 450bede9, `goHTTPRouteCandidate` uses substring checks. They accept longer identifiers and field names that the production registration regexes cannot accept: examples in the frozen Kubernetes source include `.GetPath`, `.GroupVersion`, `.Header`, `.Options`, `.Patches`, and fields containing `Handler`.

An offline scan of the clean frozen Kubernetes commit `b2ec8b6fefac451a2dedafc4dd71f2f16c7a6abe` found 17,838 tracked Go files totaling 186,485,767 bytes. The current substring predicate admits 6,798 files totaling 112,458,928 bytes. A source-only predicate requiring a configured token to be followed by optional whitespace and `(` admits 2,183 files totaling 45,678,653 bytes. These are authored-source classification counts, not evaluator timing, route-match counts, or a performance claim. Ordinary calls such as `.Get(` can remain false positives.

## Minimal next proposal

Replace each raw substring check with a small `strings.Index` loop that finds the token, skips ASCII whitespace accepted by Go regexp `\s`, and requires the next byte to be `(`. Keep the same token set. Every current route registration regex requires `\s*\(` immediately after its Handle, Group, or router-method spelling, so this refinement preserves all currently accepted whitespace/newline forms while rejecting identifier-prefix false positives. Existing parsing remains authoritative and false positives such as unrelated `.Get(...)` calls remain permitted.

The bounded proof should extend the existing prefilter table with suffix identifiers such as `.GetPath`, `.GroupVersion`, `.Header`, and `.OptionsField`; keep every accepted registration form including spaces, tabs, CR/LF, and newlines; assert unchanged complete relation records; and assert non-call prefix files do not populate `fileStringConstants`. No timing threshold is appropriate.

## Limitations

This is one 20-second sampled interval from a run killed at 120 seconds. It cannot establish work outside the window, completion state, final relation count, or whether the proposed refinement resolves the timeout. The frozen-source scan does not execute the production scanner and does not classify its 2,183 call-token files as actual routes. Local analysis used Go 1.26.1 darwin/arm64; the profile came from Linux evaluator build ID `fc844b00d23a00143cb796df8ad8931758754f69`.
