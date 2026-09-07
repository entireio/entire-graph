# Policy record review

This is a source-grounded classification of the retained diagnostic policy records. It is not an admission decision.

The review is bound to `ordered-partials-v1.json` (SHA256 `610e298c5c882356b363974c2fdb75392de7659b0cc99b75ef16e7bd82474f3e`), `source-inventory.json` (SHA256 `da9b7d4ae88f049d3f9353d5216957d3085a034c3c85c3fd95ab551a584fe8f7`), and the diagnostic artifact (SHA256 `79be6d6d2c2fafa495e9306fe93e4f67b8486d4b5af37f2aea2ceefe69ae038c`). The approved local corpus is at `<P1_CORPUS_ROOT>/kubernetes-kubernetes`, checked out at `b2ec8b6fefac451a2dedafc4dd71f2f16c7a6abe`.

The observation remains `status: partial` and `admission_eligible: false`. Eight intentional policy exclusions are classified below; all other partial records, including 186 `E_PARSE_ERROR` records, remain outside this review. No timing, RSS, process, or comparative fields were used.

## Classifications

| Index | Code | Path | Source SHA256 | Classification | Basis |
|---:|---|---|---|---|---|
| 0 | `E_FILE_TOO_LARGE` | `api/openapi-spec/swagger.json` | `08a88d5d5fd9dcd1f93a77286f1d3ac8e471caca158284f511e75500a97a4de7` | **valid_policy_exclusion** | The packet detail reports the same byte count as the approved local source. |
| 90 | `E_FILE_TOO_LARGE` | `staging/src/k8s.io/cli-runtime/artifacts/openapi/swagger.json` | `dee95c3360db5f065718077cf7371dda307c2e0416769828252506b700d211fc` | **valid_policy_exclusion** | The packet detail reports the same byte count as the approved local source. |
| 91 | `E_MINIFIED` | `staging/src/k8s.io/client-go/discovery/testdata/apis/batch/v1.json` | `7b9f001f0d1a935e5fd91ffe3c192a4396e1857613f7894177a37d140f6c4bfe` | **valid_policy_exclusion** | The approved local source has a maximum line of 298681 bytes. |
| 92 | `E_MINIFIED` | `staging/src/k8s.io/client-go/discovery/testdata/apis/batch/v1beta1.json` | `a28dbf04ac31f6e007ff8f4fc8e85f1edcd557d5ba92636c3e4359ad4bba0b95` | **valid_policy_exclusion** | The approved local source has a maximum line of 241441 bytes. |
| 119 | `E_FILE_TOO_LARGE` | `staging/src/k8s.io/kubectl/testdata/openapi/swagger.json` | `cd312f10dd741cde6ad4ef67eaa7d678dc5ef6041457e3c07211885de10fd594` | **valid_policy_exclusion** | The packet detail reports the same byte count as the approved local source. |
| 120 | `E_MINIFIED` | `staging/src/k8s.io/kubectl/testdata/openapi/v3/apis/autoscaling/v1.json` | `c5912f36985f3e4b099a1c84ed37f7e54e3367a1af82d4ebb85b6a8d878545dd` | **valid_policy_exclusion** | The approved local source has a maximum line of 106466 bytes. |
| 121 | `E_MINIFIED` | `staging/src/k8s.io/kubectl/testdata/openapi/v3/apis/autoscaling/v2.json` | `2c13a19fbb5ca2e37e8ad50d203455930322feee431e6f58c0185e283907f646` | **valid_policy_exclusion** | The approved local source has a maximum line of 132605 bytes. |
| 193 | `E_MINIFIED` | `vendor/sigs.k8s.io/kustomize/kyaml/openapi/kubernetesapi/v1_21_2/swagger.go` | `cbc9d20b525374bf16cc68dd797cf40b8f742b911394fc8d89881d7c6c255365` | **valid_policy_exclusion** | The approved local source has a maximum line of 1284159 bytes. |

All eight policy records report the same effect: the file record is emitted and symbol parsing is skipped. For `E_FILE_TOO_LARGE`, the observed byte counts are 4,475,944, 4,707,204, and 5,393,964, each above the 4,194,304-byte parser input limit. For `E_MINIFIED`, each source exceeds the 5,000-byte line threshold and has at least 0.70 of its bytes on overlong lines; the measured fractions are recorded in the JSON artifact.

The implementation treats `E_FILE_TOO_LARGE` and `E_MINIFIED` as intentional skips (`internal/sem/provider.go:27452-27460`) and excludes them from the completeness failure count (`internal/sem/completeness_test.go:9-25`). That policy classification does not make the source semantically complete and does not authorize `reviewed_partial` adoption.

## Warning

Warning index 0, `W_WORKTREE_SNAPSHOT`, is classified as **valid_policy_warning**. The provider emits it when the worktree option is used (`internal/sem/provider.go:1845-1850`), and the implementation describes it as provenance. The packet does not independently retain the invocation flag, so the warning is preserved and excluded from any completeness or admission claim.

## Boundary

This review does not classify parse errors, alter the observation status, change thresholds, or adopt a new evaluation stratum. A future admission review would still require the complete source-bound review manifest and separate acceptance of the partial coverage policy.
