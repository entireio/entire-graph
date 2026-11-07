# Rust 0.24.2 grammar validation

Compared on macOS arm64. The baseline is the bundled go-tree-sitter Rust
grammar, which this change replaces; the candidate adds only the vendored
tree-sitter-rust v0.24.2. Both
used the ten Rust repositories and commits in `bench/repos.lock.json`
(2026-11-07).

## Per-file parser audit

Parsed all 40,518 regular `.rs` files within the provider's 4 MiB limit with
`TreeSitterParser.ParseWithStatus`, on both revisions. A symbol is keyed by
kind, name, start line and signature. Counts are Rust-only parser results,
not whole-repository graph totals. The `rust-lang/rust` test suite includes
deliberately malformed and unstable-syntax inputs.

| Repository | Files | Parse errors before | Parse errors after | Symbols before | Symbols after |
| --- | ---: | ---: | ---: | ---: | ---: |
| BurntSushi/ripgrep | 100 | 0 | 0 | 3,980 | 3,980 |
| alacritty/alacritty | 88 | 3 | 2 | 2,841 | 2,841 |
| clap-rs/clap | 329 | 13 | 13 | 5,088 | 5,088 |
| denoland/deno | 1,012 | 52 | 6 | 37,584 | 37,592 |
| rust-lang/rust | 37,362 | 4,248 | 3,525 | 311,418 | 311,310 |
| serde-rs/serde | 208 | 0 | 0 | 3,177 | 3,177 |
| sharkdp/bat | 67 | 2 | 2 | 1,157 | 1,157 |
| starship/starship | 245 | 0 | 0 | 3,795 | 3,795 |
| tauri-apps/tauri | 323 | 12 | 12 | 7,187 | 7,187 |
| tokio-rs/tokio | 784 | 2 | 2 | 10,057 | 10,057 |

Net change: 770 fewer parse errors (772 files fixed, 2 newly failing) and 100
fewer symbols.

No file that parsed cleanly before loses a symbol. Every file whose symbols
changed already had a parse error on the baseline:

- 94 such files now parse cleanly. Their symbols change shape (for example
  functions inside `impl` blocks are now methods), and only one loses a
  symbol on net.
- 67 still fail on both grammars, and error recovery differs. 36 of them,
  all in `rust-lang/rust`, lose 156 symbols on net. 17 of those use unstable
  const-trait syntax (`[const] Trait`, `const trait`) that neither grammar
  supports. The largest losses are `library/core/src/cmp.rs` (39),
  `compiler/rustc_hir/src/attrs/diagnostic.rs` (19),
  `tests/ui/unpretty/exhaustive.rs` (16) and `library/core/src/slice/cmp.rs`
  (8). The cause in `diagnostic.rs` is not yet identified; a trailing `\` in a
  doc comment and its fenced example were ruled out.

The two newly failing files keep their one symbol each:

- `tests/crashes/132126.rs` is a known-bug crash fixture
  (`trait UnsafeCopy where Self: use<Self> {}`).
- `tests/ui/parser/shebang/multiline-attrib.rs` is valid Rust: a `#!` line
  followed by an attribute on the next line. This is a small grammar
  regression.

## Whole-repository graph comparison

Built `./cmd/graph-bench` at each revision and ran it against the same
lock-pinned corpus. These totals include the other languages in each
repository.

| Repository | Symbols before | Symbols after | Relations before | Relations after | Parse failures before | Parse failures after |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| BurntSushi/ripgrep | 4,733 | 4,733 | 21,525 | 21,525 | 2 | 2 |
| alacritty/alacritty | 3,393 | 3,393 | 12,809 | 12,809 | 47 | 46 |
| clap-rs/clap | 7,515 | 7,515 | 23,080 | 23,080 | 13 | 13 |
| denoland/deno | 99,214 | 99,222 | 305,222 | 305,972 | 121 | 75 |
| rust-lang/rust | 343,253 | 343,145 | 1,236,685 | 1,236,199 | 4,284 | 3,563 |
| serde-rs/serde | 3,377 | 3,377 | 15,267 | 15,267 | 0 | 0 |
| sharkdp/bat | 4,168 | 4,168 | 11,110 | 11,110 | 47 | 47 |
| starship/starship | 32,070 | 32,070 | 46,109 | 46,109 | 0 | 0 |
| tauri-apps/tauri | 16,262 | 16,262 | 42,203 | 42,203 | 30 | 30 |
| tokio-rs/tokio | 11,442 | 11,442 | 46,233 | 46,233 | 2 | 2 |
| **Total** | 525,427 | 525,327 | 1,760,243 | 1,760,507 | 4,546 | 3,778 |

Nine repositories keep or increase their symbol and relation totals.
`rust-lang/rust` loses 108 symbols and 486 relations, consistent with the
already-failing files above; Deno gains 8 symbols and 750 relations.

```sh
graph-bench -languages Rust -cache /path/to/corpus -out /path/to/results -skip-clone
```

Concurrent local runs are not a controlled performance experiment; timing is
not a speedup claim.
