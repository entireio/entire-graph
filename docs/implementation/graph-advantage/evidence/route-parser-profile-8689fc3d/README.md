# Go HTTP route parser offline evidence

Source baseline: `3fe4e8d`

The bounded parser change was motivated by the complete diagnostic-only CPU
profile at
`diagnostic-dispatch-8689fc3d/controller/raw/output/relations-cpu.pprof`
(SHA-256 `2c72e17b6089a06fb0fb991a68c30498d407c88ad5baf3777121d0f78b870776`).
The profile, its runtime manifest, the implementation plan, the authorized
2026-09-05 progress review, the existing Go HTTP route scanner and focused
route fixtures were the only inputs consulted. No product or evaluator run,
cloud invocation, measurement campaign, competitor material, memory, or
transcript was used.

The change keeps the former regex matchers as the compatibility oracle and
applies them to lexically bounded candidate regions. It preserves accepted
registration forms, matcher-category ordering, group-prefix resolution, and
relation records. Static constants are initialized on the first expression
that needs them. Deterministic differential fixtures cover multiline
whitespace and assignments, brace-prefixed declarations, multiple matcher
categories, semicolons, groups, literal and constant routes, comments, raw
strings, false identifier prefixes, later valid matches, a large unrelated
payload, and 4,096 blank continuation lines. These fixtures establish the
covered scanner compatibility boundary; they do not prove equivalence for all
possible source text.

Focused verification:

```text
go test ./internal/sem -run '^TestGoHTTPRoute'
ok  github.com/entireio/entire-graph/internal/sem  1.907s

go test -race ./internal/sem -run '^TestGoHTTPRoute'
ok  github.com/entireio/entire-graph/internal/sem  3.384s
```

This evidence supports source attribution and focused correctness only. It
makes no latency, throughput, memory, benchmark, or product-performance claim.
The default behavior and public schema are unchanged. Rollback is to revert the
route-parser commit; no cache, migration, persisted format, or default setting
requires cleanup.
