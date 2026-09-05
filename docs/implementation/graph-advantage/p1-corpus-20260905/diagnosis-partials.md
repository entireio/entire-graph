# P1 retained partial-failure diagnosis

This is a bounded diagnosis of the retained `baseline-raw` and `paused-raw`
worker archives. It does not establish full-repository coverage or explain
unrun observations. Counts below are occurrences in retained NDJSON records;
the same failure can repeat across workers, profiles, operations and trials.

## Observed status and reason codes

| lane | status counts | partial-failure occurrences |
|---|---|---|
| baseline | 69 `ok`, 33 `partial`, 6 `timeout` | `E_PARSE_ERROR` 243, `E_MINIFIED` 36, `E_FILE_TOO_LARGE` 3 |
| paused campaign | 113 `partial`, 3 `timeout`, 1,434 `unrun` | `E_PARSE_ERROR` 808, `E_MINIFIED` 440, `E_FILE_TOO_LARGE` 3 |

The `unrun` records are a consequence of the paused/circuit-breaker state and
are not failures of source parsing. Kubernetes snapshot failures triggered the
recorded worker block: baseline workers report three consecutive failures or a
baseline hard-failure circuit breaker for the fast/full assignments.

## Classification

`E_FILE_TOO_LARGE` is an expected bounded-input exclusion in the retained
evidence. The representative path is
`kubernetes-kubernetes/api/openapi-spec/swagger.json`; the detail states
4,475,944 bytes versus the 4,194,304-byte parser limit and says the file record
was emitted without holding content in memory. This is an explicit size limit,
not a parser regression.

`E_MINIFIED` is also an expected source-policy exclusion. The four retained
paths are Kubernetes OpenAPI/discovery test data:

- `staging/src/k8s.io/client-go/discovery/testdata/apis/batch/v1.json`
- `staging/src/k8s.io/client-go/discovery/testdata/apis/batch/v1beta1.json`
- `staging/src/k8s.io/kubectl/testdata/openapi/v3/apis/autoscaling/v1.json`
- `staging/src/k8s.io/kubectl/testdata/openapi/v3/apis/autoscaling/v2.json`

The detail identifies them as minified/bundled and says they were not analyzed
as source. Their repetition is therefore expected under the fixed corpus
policy.

`E_PARSE_ERROR` is a genuine partial-coverage class requiring separate review;
it is not safe to relabel it as expected exclusion. Representative retained
paths include shell and templated YAML that contain syntax outside the selected
grammar's accepted form (`hack/lib/golang.sh`,
`hack/update-codegen.sh`, `cluster/gce/config-common.sh`,
`cluster/gce/manifests/konnectivity-server.yaml`), plus Go test files with
syntax-error nodes (`staging/src/k8s.io/dynamic-resource-allocation/cel/compile_test.go`,
`test/integration/apiserver/cel/validatingadmissionpolicy_test.go`). The
details include concrete line/column locations, so these are parse diagnostics
with source paths, not silent drops. The frozen Entire Graph and requests/go-chi
records do not contribute retained partial-failure occurrences in this slice;
zod contributes parse partials in its syntax/full assignments.

## Cache-off/on evidence

The paused archive has 55 paired semantic-digest comparisons for retained
`reuse=false`/`reuse=true` observations. Zero pairs have equal
`semantic_digest`; all 55 differ. This is an observation about the retained
canonical-digest field, not proof of a product regression: the digest contract
includes semantic diagnostics/completeness and the run was stopped, while
paired operation coverage is incomplete.

The partial-failure sets can differ as well. For example, Kubernetes fast
`search`, cold, trial 0 has the same broad classes in both arms, but the
cache-off record includes `.../ratcheting_test_cases/crds/standard-install.yaml`
and `test/cmd/discovery.sh`, while the cache-on record includes
`staging/src/k8s.io/kubectl/pkg/describe/describe_test.go` instead. That is
evidence that the retained off/on outputs are not canonically identical for
this comparison; it is insufficient to identify why without a controlled
reproduction, which is outside this read-only diagnosis.

No timing, benchmark, VM, or product command was run for this diagnosis.
