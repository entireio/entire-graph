# E_PARSE_ERROR source review

This review covers all 186 `E_PARSE_ERROR` records in
`ordered-partials-v1.json`, in their emitted zero-based positions. The complete
record ledger is `parser-review.json`: every entry retains the original
diagnostic, packet index, repository path, full-file SHA-256, frozen source
revision, reported line/column locations, and source-line citations. The
review used the clean local Kubernetes source at
`b2ec8b6fefac451a2dedafc4dd71f2f16c7a6abe`; its `go.mod` declares `go 1.27.0`
at line 9. The evaluator's Go version did not compile Kubernetes and is not
evidence for the source language version.

## Complete classification

| classification | count | causes |
|---|---:|---|
| genuine parser defect | 133 | 132 Go `new(expression)` files; 1 standalone CRD YAML root error |
| unsupported syntax in a supported language | 34 | 26 Bash grammar gaps; 8 protobuf grammar/preparation gaps |
| intentionally non-language fixture | 19 | 17 Go-templated YAML files; 1 substitution-template YAML; 1 deliberately malformed YAML test fixture |

The 132 Go records are one repeated defect, not 132 independent causes. Every
cited failure is at a `new(expression)` form, ranging from `new("rc1")` to
`new(resource.MustParse("100Mi"))` and `new(max(...))`. The frozen source
declares Go 1.27, while Entire routes `.go` directly to the bundled Go grammar
at `internal/sem/parser.go:78` and has no Go preparation step in the parse-source
selection at `internal/sem/parser.go:287-350`. Representative source evidence:

- packet index 25, `pkg/api/job/util.go:121`,
  `cfg.Policy.Gang.MinCount = new(max(...))`;
- packet index 26, `pkg/api/job/util_test.go:119`,
  `ResourceClaimName: new("rc1")`;
- packet index 44, `pkg/kubelet/kubelet_node_status_linux.go:35`,
  `return new(userns.RunningInUserNS())`.

These are supported-language source files and the diagnostic affects semantic
coverage, so the group is a product parser defect. It must not be admitted as
an expected partial merely because one bundled grammar rejects it.

The 26 Bash records are valid repository scripts using constructs that the
bundled Bash grammar or current compatibility mask does not accept. Examples
include environment assignments before namespaced commands
(`build/common.sh:279`), nested/default parameter expansions
(`cluster/gce/config-common.sh:175`), array/arithmetic syntax
(`cluster/gce/util.sh:2347`), parameter substitution
(`cluster/get-kube-binaries.sh:161`), and a composed `until` condition
(`test/cmd/discovery.sh:460`). Entire already applies a Bash compatibility mask
at `internal/sem/parser.go:306-308`; these records show remaining actionable
coverage gaps. They are classified as unsupported syntax rather than expected
exclusions.

The 8 protobuf records split into five `extend` declarations, two leading
underscore field names, and one reserved string name. Examples are
`vendor/github.com/gogo/protobuf/gogoproto/gogo.proto:38`,
`vendor/github.com/google/gnostic-models/openapiv2/OpenAPIv2.proto:262`, and
`vendor/go.etcd.io/etcd/api/v3/etcdserverpb/raft_internal.proto:29`.
`internal/sem/protobuf.go:1-15` already documents that the bundled grammar is
proto3-only and builds a compatibility parse view; these eight valid source
forms remain unsupported and actionable.

Eighteen YAML records contain Go-template or substitution placeholders and are
not standalone YAML before repository tooling renders them. The remaining
testdata record, `cmd/kubeadm/app/util/testdata/baz.yaml:4`, deliberately uses a
tab-indented mapping entry. Those 19 diagnostics are source-grounded fixture
properties. In contrast, packet index 83,
`staging/src/k8s.io/apiextensions-apiserver/test/integration/ratcheting_test_cases/crds/standard-install.yaml:1`,
is a standalone 469,150-byte CRD YAML document. A bounded independent syntax
parse accepts it while Entire reports a root error, so it remains a genuine
parser defect pending a minimal reduction.

## Smallest next defect fix

Address the Go group first because one fix can remove 132 supported-language
partials. Start with an independently authored minimal source such as:

```go
package fixture

func value() int { return 1 }
var result = new(value())
```

Use the pinned local Go compiler only to record whether that exact fixture is
accepted and record the compiler version; do not claim which Go release first
introduced it. Then inspect whether the bundled Go grammar can be updated to
produce the real AST. Prefer grammar support over a mask that would erase the
`value()` call. The focused regression must assert:

- `ParseWithStatus` has no syntax error and preserves source ranges;
- `value` and its call remain available to symbol/relation extraction;
- symbol IDs, body hashes, and extraction-cache round trips match uncached
  extraction; and
- malformed `new()` and multi-argument forms still report parse errors.

Do not change Bash, protobuf, or YAML behavior in that fix. Reduce the valid
CRD YAML separately before proposing its repair. The JSON ledger supplies
minimal source candidates and exact membership for later bounded work.

## Limits

This review does not classify the eight policy-owned non-parse partials or the
warning. It does not relabel historical observations, adopt a partial-admission
policy, or make a coverage claim for unreviewed inputs. No product, corpus, VM,
benchmark, timing, or RSS data was used.
