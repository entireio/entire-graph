# Go `new(expression)` parser correction

This correction is scoped to the 132 Go `E_PARSE_ERROR` records classified as
`go-new-expression` in `parser-review.json`. The implementation and this note
are in the same commit, based on source commit
`8dbf1fce82098eed7db20274697efab3ff25bcb8`.

The regression fixture is independently authored from the reviewed syntax
shape. It was not copied from Kubernetes or another parser implementation:

```go
package fixture

func value() int { return 1 }
var result = new(value())
```

The pinned local toolchain accepted that source before implementation:

- `go version`: `go1.26.1 darwin/arm64`
- `go tool compile new_expression.go`: passed
- source SHA-256: `0b463a02e6f17199e0749c53a889728984ea15f281f9dfb5f5b1bdb9d08f7c8c`

The bundled tree-sitter grammar produced an error tree for the same source. The
fix retains the grammar and adds a bounded retry only after an errored Go parse.
The standard Go parser identifies syntactically valid `new` calls with a
clearly value-shaped argument, and a same-width private parse view changes the
callee token for one retry under the original timeout. The retry is adopted
only when its entire tree is error-free. Entities, hashes, ranges, and relation
scans continue to read the authored source. Ambiguous type/value arguments and
malformed syntax are not rewritten.

`extractionFormatVersion` changes from 3 to 4. Existing private extraction
records therefore miss and rebuild once; rollback likewise causes a cold miss
instead of reusing parser output from the other implementation.

Focused verification on the implementation working tree:

- six Go parser, cache, invalidation, malformed-input, and cancellation tests:
  passed in 0.979s
- the same six tests with `-race`: passed in 2.096s
- graph-selected parse-failure regression
  `TestAnalyzeGitRange_PartialRecoveryKeepsDiffWithWarning`: passed in 0.984s

No corpus request, benchmark, VM call, dependency update, grammar generation,
or runtime fetch was performed.
