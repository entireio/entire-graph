# Parser review fix status appendix

This appendix maps a reviewed diagnostic to a later correctness fix. It does
not alter `ordered-partials-v1.json`, `parser-review.json`, their classification,
or the historical diagnostic. It also does not establish that a new corpus
snapshot is clean.

## YAML partial-failure index 83

The source-bound reproduction used:

- source commit: `b2ec8b6fefac451a2dedafc4dd71f2f16c7a6abe`
- path: `staging/src/k8s.io/apiextensions-apiserver/test/integration/ratcheting_test_cases/crds/standard-install.yaml`
- source SHA-256: `558218a3e7f5a6c9f0d1d8c26e3519c5fdc3955e4a398b2c61cc2066a2034e78`
- source size: 469,150 bytes, below the 4 MiB parser input bound

The bundled YAML grammar parsed the authored file as a complete `stream` with
no error nodes. The existing preprocessing reduced the parse view to 469,094
bytes and produced a root `ERROR`. It mistook four multiline single-quoted CEL
scalar continuations shaped like `''ReplaceFullPath'' : true'` for mapping keys,
replacing each with the shorter `'key' : true'` and breaking the surrounding
scalar.

The independently authored regression uses a different API, field, value, and
expression while retaining the YAML construct:

```yaml
apiVersion: example.test/v1
kind: Example
metadata:
  name: sample
spec:
  rule: 'enabled(self.mode) ? self.mode ==
    ''Active'' : false'
```

The fix is in the same commit as this appendix. Quoted-key preprocessing now
recognizes only a complete line-leading quoted token followed by whitespace and
`:`; it handles doubled single quotes and escaped double quotes. The private
replacement preserves the exact byte and newline counts. Malformed quoted
input remains an `E_PARSE_ERROR`, and entities, ranges, hashes, relations, and
cached extraction continue to use the authored source. The private extraction
format advances from 4 to 5 so a published v4 YAML error record cannot survive
the parser correction.

Focused verification on the implementation working tree:

- four YAML correctness/cache tests plus the v4 invalidation test: passed in
  0.837s
- the same five tests with `-race`: passed in 1.915s
- graph-selected parse-error-detail regression: passed in 0.647s

No corpus product request, comparative run, benchmark, VM call, grammar update,
dependency update, or policy change was performed.
