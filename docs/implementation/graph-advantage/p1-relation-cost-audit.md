# P1.5 bounded relation-cost audit

This audit identifies a small eligible raw family; it is not a release benchmark
or an end-to-end latency improvement claim. No parameters were tuned after the
run. The audit produced 450 observations: three languages, five probes, and 30
repetitions per probe. Each language has 40 independently generated files with
10 functions each. Files import one module and contain local and imported calls.
No repository code, generator, dependency fetch or network request executes.

| Language | Full relation median | Raw imports median | Raw imports/full ratio | Paired median change with precomputed imports |
|---|---:|---:|---:|---:|
| Go | 88.882 ms | 0.092 ms | 0.10% | -0.02% |
| TypeScript | 135.561 ms | 3.283 ms | 2.42% | -2.73% |
| Python | 79.266 ms | 3.027 ms | 3.82% | -4.04% |

`importsFor(path, content)` is a file-local string-list scanner. The existing
`forEachRelation` `precomputedImports` map can provide exactly that list while
still resolving local targets against current files. Go imports are inexpensive
in this fixture; TypeScript/Python imports have modest measured cost. This is a
reasonable first presence-bit family, with no justification for treating it as
completion of every raw relation pass.

`callLikeIdentifiers(block, language)` takes 0.8–1.1% of the full relation median
on these fixtures. It excludes receiver, namespace/scope, type, field, data-flow,
route and language-specific scans and is not the entire call-extraction phase.
`resolveCallTargets` is timed separately over precomputed bare-name probes; its
0.1–0.2% ratio is only the basic target-resolution helper, not all resolution.
The audit provides no reason to introduce broader call/scope caching now.

## Measurement boundaries

- Full phase: `forEachRelation`, full profile, one relation worker, already
  materialized file/symbol records and in-memory captured strings. Includes
  global-index construction, raw scanning, scope/receiver/type/etc passes,
  resolution, relation allocation and emission; excludes entity parsing, file IO,
  output serialization and durable cache IO.
- Raw probes: `importsFor` on every file; `callLikeIdentifiers` on the same
  per-symbol blocks used by the provider (exact source spans for TypeScript).
- Resolution probe: `resolveCallTargets` on precomputed generic call names,
  same-file/global symbol indexes, no imported-binding map. It deliberately
  does not purport to measure the full language-specific resolver.
- Before timing, full passes with scanned and precomputed imports compare every
  `RelationRecord` by `reflect.DeepEqual`. Both arms use the same records and
  strings; every fixture's relation output was identical.
- Ordering alternates by trial. All timings are in one warmed test process on
  Darwin arm64 / Apple M4. Page cache is not manipulated. Other local work may
  contribute scheduling noise. Raw medians overlap full-phase work and must not
  be added together or subtracted as exact phase attribution.
- This is not RSS, disk usage, cache-on latency, a real-repository corpus, or a
  held-out quality evaluation. The Go test temporary executable hash was not
  retained; source/corpus hashes and the toolchain/environment are recorded.

## Sources and reproduction

Fixture origin is `TestRelationProfileEvaluation` in
`internal/sem/relation_profile_evaluation_test.go`, authored from the plan's
file-local import/call boundary requirements. Consulted implementation:
`forEachRelation`, `importsFor`, `callLikeIdentifiers`, `resolveCallTargets`,
`symbolBlockFromLines`, and `exactSymbolSource` in Entire's `provider.go`.
The only requirements source is the four-workstream plan; no competitor,
comparison fixture, historical transcript or memory was consulted.

```sh
ENTIRE_GRAPH_RELATION_PROFILE=/absolute/new-output.ndjson go test ./internal/sem -run '^TestRelationProfileEvaluation$|^TestBareImportedCallResolvesUniqueFFITarget$' -count=1
python3 docs/implementation/graph-advantage/probes/summarize_relation_profile.py /absolute/new-output.ndjson
```

The output file uses exclusive creation to prevent silently overwriting results.
Retained evidence is `evidence/relation-profile-v1.ndjson`, its summary JSON and
run log. The first compile attempt encountered a concurrent implementation typo,
not a timing failure; the diagnostic is preserved separately. No caching behavior
is changed by this audit, so rollback is simply not running the opt-in test.

## Implemented family following the audit

Private format 3 now caches only the raw import strings for the measured Go,
TypeScript and Python scanner paths in fast/full profiles. Presence bit 1
separates computed-empty from absent; other raw passes remain uncached.
`providerFileResult.precomputedImports` feeds the existing ordered reducer and
`forEachRelation`, which still resolves against current inputs. New telemetry
counts cache-managed raw-import parses/hits and raw-import extraction time.

`extraction_imports_test.go` covers profile/language payloads, empty and malformed
sources, detached returned lists, presence/version corruption and configured
quotas. Existing concurrent publication and full provider-equivalence fixtures
also run with the new family. The first test expectations omitted Entire's
existing Python module.member import and confused an empty Go slice with nil;
the existing scanner code was inspected and expectations corrected. No scanner
or semantic behavior was changed to satisfy those tests. The initial failure
log is retained as `raw-imports-development-fixture-failure.txt`.

Quota configuration and the complete decision are in ADR 0039. This small raw
family does not establish the larger P1 performance gate; prior failed or
incomplete performance results remain failed/incomplete.
