# Protobuf compatibility fix evidence

This note binds the protobuf compatibility change to the retained source
review. It is source-bound evidence only; it does not claim a post-fix corpus
rerun, semantic coverage result, or benchmark result.

## Inputs

- Source origin: `https://github.com/kubernetes/kubernetes.git`
- Frozen source revision: `b2ec8b6fefac451a2dedafc4dd71f2f16c7a6abe`
- Local source root: `<P1_CORPUS_ROOT>/kubernetes-kubernetes`
- Review ledger: `review-input/parser-review.json`
- Review ledger SHA-256: `610e298c5c882356b363974c2fdb75392de7659b0cc99b75ef16e7bd824f3e`
- Approved source archive SHA-256: `a8767fd7f6df1f811e89b7a09257faa723ae53dd606557fb30fc4e90ce9312bd`

The review ledger classifies `partial_failure_index` values 174 through 181 as five protobuf
extension declarations, records 176 and 177 as leading-underscore fields,
and record 180 as a quoted reserved name. The record positions refer to the
ledger's zero-based packet ordering and are retained unchanged.

## Form coverage

All eight reviewed protobuf source forms fit the implemented compatibility
surface:

| Reviewed form | Source record and shape | Compatibility result |
| --- | --- | --- |
| Extension | `containerd/.../fieldpath.proto:36`, qualified `google.protobuf.FileOptions`, plain `optional bool` field | Accepted by the narrow extension validator |
| Extension | `gogo.proto:38`, qualified `google.protobuf.EnumOptions`, repeated plain `optional bool/string` fields | Accepted; the related FileOptions, MessageOptions, and FieldOptions blocks have the same shape |
| Extension | `gnostic-models/openapiv3/annotations.proto:42`, `Document document = 1143` | Accepted as an unqualified field type plus identifier and valid number |
| Extension | `grpc-gateway/.../annotations.proto:10`, comments followed by `Swagger openapiv2_swagger = 1042` | Accepted; comments are skipped lexically |
| Extension | `etcd/.../version.proto:9`, `optional string etcd_version_msg = 50000` | Accepted; its related Field/Enum/EnumValue blocks have the same shape |
| Field | `gnostic-models/openapiv2/OpenAPIv2.proto:262,447,579`, `string _ref = 1` | Accepted by leading-underscore parse-view masking |
| Field | `gnostic-models/openapiv3/OpenAPIv3.proto:459,487`, `string _ref = 1` | Accepted by leading-underscore parse-view masking |
| Reserved name | `etcd/.../raft_internal.proto:29`, `reserved "v2";` | Accepted by quoted-reserved-name masking |

The five extension records contain no field options, groups, nested extension
constructs, or invalid/reserved field numbers. The validator intentionally
leaves those broader or malformed forms visible as parse failures. It masks
extensions only at file scope or directly inside a message; extensions inside
services or enums remain visible and are covered by focused regressions.

## Reproduction and limits

The source mapping was checked with the retained ledger and local source only:

```sh
python3 - <<'PY'
import json
from pathlib import Path
d = json.loads(Path("docs/implementation/graph-advantage/evidence/diagnostic-dispatch-1f20f694/review-input/parser-review.json").read_text())
for record in d["records"]:
    if record["cause_id"].startswith("protobuf"):
        print(record["partial_failure_index"], record["cause_id"], record["file_path"])
PY
```

The cited source blocks were then inspected under the frozen local source root
with `sed` and a balanced-brace source-only Python listing. Validation of the
implementation used:

```sh
go test ./internal/sem -run '^TestProtocolBuffers' -count=1
go test ./internal/sem -run '^TestProtocolBuffers' -count=1 -race
git diff --check
```

No product request, corpus measurement, VM, or post-fix evaluator run was made
for this mapping. “Accepted” means the reviewed syntax matches the bounded
parse-view validator; it does not establish complete extraction or eliminate
the historical partial rows without a separately authorized rerun.
