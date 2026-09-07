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

## Bash parameter-replacement and comment-backtick partials

The source-bound parameter-replacement reproductions used Kubernetes revision
`b2ec8b6fefac451a2dedafc4dd71f2f16c7a6abe`:

- `cluster/gce/config-common.sh:175`, SHA-256
  `c6a03d78e6021c2e0a8325becc6047a88be3c12e98d0283840d1bb3053a07800`
- `cluster/gce/util.sh:3107`, SHA-256
  `0e6090bccf0b29ca88ab893de8163df403d46cfa3cfb80510e24ae0618bf8c35`
- `test/e2e/testing-manifests/storage-csi/external-snapshot-metadata/run_snapshot_metadata_e2e.sh:90-92`,
  SHA-256
  `b88b7a9ad23e5f02fb36109dc740501b7bd2b43d4ef1a76e840149bbd89fc7f2`

The bundled grammar rejected literal punctuation after a nested expansion in
complete, double-quoted `${NAME:-word}` and `${NAME:+word}` forms, and the
escaped-brace fallback `${NAME:-{\}}`. Independently authored tests retain the
same syntax shapes with different identifiers and surrounding commands. The
parse view changes only the rejected backslash, semicolon, or three-byte
escaped-brace fallback, preserving byte and newline positions. Nested
expansions, command substitutions, and backticks remain in the parse tree;
incomplete or malformed expansions remain `E_PARSE_ERROR`.

The standalone comment-backtick reproduction is bound to
`hack/update-codegen.sh:101` at the same Kubernetes revision, SHA-256
`a9ff06eba58b6f439bb7e558ccb7452d9c2d6b91c69e583cf2f9ad2efec127ef`.
Its independently authored test masks only complete standalone backtick words
whose contents are a single-line comment. Real commands, quoted or attached
forms, multiline content, and escaped forms remain unchanged.

Public parsing, authored ranges and body hashes, supported call relations, and
cached versus uncached extraction are covered by the focused tests. The
current shell relation scanner does not emit a command substitution nested in
a quoted local-assignment value, even for the independently authored
grammar-clean control. The fix preserves that command-substitution AST and
the exact public `CALLS` set produced by the clean control; it does not extend
the resolver. The private extraction format advances from 5 to 6 so cached v5
parse results cannot survive either new compatibility correction.

Focused verification on the implementation working tree:

- assignment-prefix, parameter-replacement, comment-backtick, cache-version,
  and graph-selected shell-call tests: passed in 1.438s
- the same focused tests with `-race`: passed in 2.629s

No corpus product request, comparative run, benchmark, VM call, grammar update,
dependency update, policy change, or broad shell relation change was performed.
