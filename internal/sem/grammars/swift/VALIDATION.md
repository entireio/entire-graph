# Swift 0.7.4 compatibility validation

Compared on macOS arm64 against main at
`28382d8ee2f0cb4ebc962255a3d8cbd364d2f258`, using the ten Swift repositories
and commits in `bench/repos.lock.json` (2026-11-02).

## Per-file parser audit

Parsed all 24,946 tracked, regular `.swift` files within the provider's 4 MiB
limit with `TreeSitterParser.ParseWithStatus`, on both revisions. Counts below
are Swift-only parser results, not whole-repository graph totals. The Swift
compiler corpus includes deliberately malformed inputs.

| Repository | Parse errors before | Parse errors after | Symbols before | Symbols after |
| --- | ---: | ---: | ---: | ---: |
| Alamofire/Alamofire | 10 | 4 | 7,388 | 7,388 |
| ReactiveX/RxSwift | 45 | 45 | 13,734 | 13,734 |
| SnapKit/SnapKit | 0 | 0 | 511 | 511 |
| SwiftyJSON/SwiftyJSON | 0 | 0 | 693 | 693 |
| airbnb/lottie-ios | 2 | 1 | 4,758 | 4,758 |
| apple/swift | 10,581 | 9,736 | 180,725 | 181,979 |
| onevcat/Kingfisher | 4 | 3 | 4,504 | 4,504 |
| pointfreeco/swift-composable-architecture | 201 | 51 | 9,861 | 9,872 |
| realm/realm-swift | 30 | 21 | 11,039 | 11,093 |
| vapor/vapor | 53 | 7 | 4,232 | 4,232 |

Net change: 1,058 fewer parse errors and 1,319 additional symbols. No previously
clean file in the nine application/library repositories gains a parse error.
The 20 newly flagged compiler fixtures contain invalid syntax (including
unterminated comments, malformed conditions and compiler-crasher inputs).
They must remain diagnostics rather than being hidden by source masking.

## Targeted preservation checks

- Hummingbird 2.28.0's `HTTP2ServerConnectionManager+StateMachine.swift` retains
  exactly the baseline's 42 symbol IDs and 113 relation triples, with no parse
  failure. The unpatched PR produced only 23 symbols and 59 relations.
  **Not re-measured on the final parser**, for the same reason as the
  whole-repository totals below: these counts predate the last scanner and
  grammar fixes. The per-file audit above is the current measurement.
- Regression tests cover conditional attributes and extensions, ownership
  switches, conditional switch cases and nested case bodies, multiline
  associated-type constraints, compiler diagnostics, internal/versioned
  compilation conditions, inline `#endif`, comment lookahead (including leading
  operators and closures after comments), string `canImport` versions, flags
  named like predicates, and macros whose names begin with a directive word.
- Graph assertions preserve declaration ranges, attributes, containment,
  calls and field reads/writes. Malformed-source tests require parse failures.
- All 296 upstream grammar corpus tests and the upstream highlighting/outline
  tests pass. Applying `compatibility.patch` to the pinned upstream commit and
  generating with tree-sitter-cli 0.24.7 reproduces `parser.c` and `scanner.c`
  byte for byte.

The full Go suite, targeted race tests, vet and license checks passed on the
final source tree. Reproduce them with:

```sh
go test -timeout 20m ./...
go test -race ./internal/sem -run 'Swift|IdentityRevision' -count=1
go vet ./...
bash scripts/gen-notices.sh --check
```

The default ten-minute run timed out in the CLI and semantic packages while the
corpus benchmark ran concurrently, without an assertion failure. The final
suite uses the larger timeout above and runs without that benchmark load.

## Whole-repository graph comparison

Measured before the final scanner and grammar fixes, which changed three
`apple/swift` parse results and five symbols in the per-file audit above. The
totals below have not been re-measured on the final parser.

Every repository retains or increases its total symbols and relations:

| Metric | Main | Patched grammar | Change |
| --- | ---: | ---: | ---: |
| Symbols | 411,903 | 413,217 | +1,314 |
| Relations | 1,342,900 | 1,343,247 | +347 |
| Parse failures | 12,437 | 11,382 | -1,055 |

Apple gains 89 relations, Composable Architecture 37, Realm 157 and Vapor 64;
the other six repositories retain their relation totals. These totals measure
coverage, not identity equivalence: the targeted Hummingbird comparison above
checks the actual symbol IDs and relation triples.

For this comparison, build `./cmd/graph-bench` at each revision
and run it against the same lock-pinned corpus:

```sh
graph-bench -languages Swift -cache /path/to/corpus -out /path/to/results -skip-clone
```

That benchmark includes other languages found in each repository. Its totals
therefore differ from the Swift-only table above. Concurrent local validation
is not a controlled performance experiment; timing is not a speedup claim.
