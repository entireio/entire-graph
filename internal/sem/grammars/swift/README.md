# Swift grammar

Based on tree-sitter-swift 0.7.4, commit
`82bb3a533e0801fd2bbaa11dc49676e10bf41948`.
The license and headers are copied from upstream. The parser is
generated at ABI 14 with tree-sitter-cli **0.24.7** after applying
[compatibility.patch](compatibility.patch) to upstream's `grammar.js` and `src/scanner.c`.

The patch preserves syntax that regressed in the grammar upgrade:

- Ownership operators immediately after `switch` cannot capture the switch
  body as a trailing closure.
- Conditional directives can follow attributes before their declaration.
  Both branches remain in the syntax tree; no build configuration is assumed.
- Conditional compilation recognizes versioned `canImport`, feature and internal
  platform predicates, switch-case directives and inline closing directives.
- A comma in a generic constraint list continues that list across a newline.
- Compiler diagnostics are valid within class, enum and protocol bodies.
- The scanner looks through block comments when deciding whether a newline
  separates statements, while retaining operator continuations.

To regenerate, in a separate checkout of the upstream commit:

```sh
git apply /path/to/entire-graph/internal/sem/grammars/swift/compatibility.patch
tree-sitter generate --abi 14
cp src/parser.c src/scanner.c /path/to/entire-graph/internal/sem/grammars/swift/
```

Use the exact generator version above. No generation or download occurs when
building or running Graph. `grammar-refresh` copies upstream verbatim, so after
using it, reapply or reconcile this patch before committing. Update the manifest
note and identity revision whenever the resulting parser changes.

Regression coverage lives in `internal/sem/swift_grammar_regression_test.go`;
`swift_test.go` also protects the newer Swift Testing macro support.
See [VALIDATION.md](VALIDATION.md) for the baseline comparison and reproduction
commands.
