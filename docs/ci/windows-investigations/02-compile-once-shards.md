# Result

## Verdict

CONDITIONAL

## Best measured critical path

Eight execution shards were the winner. Across three accepted runs, the
instrumented worker-path critical path had a **294.231 s median** and a
292.095–296.012 s range. The best individual run was 292.095 s.

The end-to-end treatment wall clock, measured from concurrent job launch
through fail-closed equivalence verification, had a **303.221 s median** and a
300.074–304.879 s range. The modeled path is below five minutes; the full
orchestration median is 3.221 s above five minutes. Compared with the accepted
monolithic baseline, the medians reduce the 766.832 s `go test` step by 61.63%
and the 780.313 s wrapper wall clock by 61.14%, respectively.

## Coverage equivalence

All five accepted v2 runs (the 4/6/8 screens and two additional 8-shard
repetitions) passed the same fail-closed guard:

- exact fully qualified `run`/`pass`/`fail`/`skip` dynamic-event multiset,
  8,414 baseline events and 8,414 candidate events, without Unicode
  normalization;
- exact heavy-package top-level inventory, 2,307 expected and 2,307 assigned,
  with no missing, extra, duplicate, or regex-selected tests;
- all 21 target-Windows packages present;
- every baseline package terminal exactly once, every candidate non-heavy
  package terminal exactly once, and every heavy package terminal exactly once
  per nonempty planned shard assignment;
- no package failure, no multiplicity mismatch, and every recursively found
  exit-code field equal to integer zero (33 records for 4 shards, 45 for 6,
  and 57 for 8).

## Cost

The timestamped Azure Retail Prices API meter for a regular Windows
`Standard_D16ads_v5` VM in `westus3` was **$1.56/hour USD**. Multiplying that
meter by the three-run median modeled critical path gives **$0.12750/run**
(range $0.12657–$0.12827). Using full orchestration wall time instead gives a
$0.13140 median (range $0.13003–$0.13211).

These figures exclude fixed/shared infrastructure. The separately recorded
meters were $0.045/hour for Standard NAT Gateway, $0.005/hour for its Standard
public IP, $17.92/month for the P10 LRS OS disk, and normal Hot LRS blob
capacity/transaction charges. The median 8-shard run moved 358,414,396 payload
bytes through the artifact path (one compiled upload, eight downloads, and one
result upload); multiplying that whole payload by the NAT $0.045/GB data meter
is a conservative **$0.01613 upper bound**, not a claim about the invoice's
byte accounting. Blob capacity and operations are below one tenth of a cent at
this scale. The prototype's GitHub-hosted-runner cost will differ and was not
measured.

## Main risk

The result was measured on one 16-vCPU Azure VM emulating concurrent workers,
not on independent GitHub-hosted runners. Runner allocation, GitHub artifact
service latency, cache behavior, quotas, and billing remain unmeasured. The
branch-only `workflow_dispatch` prototype also cannot run until it exists on
the default branch.

There is a semantic risk as well: dynamic listing starts every heavy binary,
and execution repeats package initialization—and `TestMain` where present—once
for each shard containing that package. The exact event guard proves observed
test outcomes, but sharding can hide an accidental dependency on mutable state
left by a different top-level test.

## Recommendation

Place the prototype on the default branch as a **manual, non-required** pilot
and run the 8-shard configuration at least five times on the actual target
runners. Keep the event/inventory/multiplicity/exit-code verifier and the
30,000-character argv guard as required gates. Do not replace the existing
required Windows job until those production runs reproduce coverage and the
end-to-end gate reliably meets the target.

Six shards are the fallback if fan-out or artifact charges dominate: its one
accepted screen was only 1.07% slower than the 8-shard screen while using two
fewer execution workers.

## Environment

The product checkout was detached at
`ee6468a6a49d9b2a1a828bd276792f415f392185`. The staged coordinator harness
included the Windows-validated fixes through `b22be6a7`. The target was:

- Windows Server 2025 Datacenter Azure Edition,
  `MicrosoftWindowsServer:WindowsServer:2025-datacenter-azure-edition:26100.33222.260810`;
- `westus3`, regular `Standard_D16ads_v5`, Premium LRS P10 OS disk;
- private VM with no public IP, no custom inbound rule, and no Spot/Low
  Priority scheduling;
- outbound-only Standard NAT for bootstrap after the private subnet was found
  to have no default outbound access;
- private artifact containers, public blob access disabled, TLS 1.2, VM
  managed-identity transfers, and controller-side Azure CLI authentication;
- PowerShell 7.6.4, Go 1.26.7, Git 2.55, Python 3.13.14, and MSYS2 GCC 16.2;
- `GOOS=windows`, `GOARCH=amd64`, and `CGO_ENABLED=1`.

The VM, network, disk, storage, and identity resources were isolated in the
required `rg-entire-win-ci-a2-<run-id>` naming scheme and tagged with purpose,
agent, run, expiry, and owner metadata. All 22 raw result blobs were exported
to a permission-restricted local archive and checksummed before the exact
tagged group was deleted.

## Method and timing boundaries

Baseline-04 created the fixed module/build-cache seed, generated the complete
package inventory on the Windows target, and ran `go test -json -timeout 30m
./...` with default vet. Every treatment restored independent compile,
non-heavy, standalone-vet, and per-shard caches from that identical snapshot.
The accepted screen order was drawn before execution with
`random.Random(7).shuffle([4, 6, 8])`, producing 8, 4, 6.

For each treatment, the non-heavy package job and strengthened standalone vet
started concurrently with the compile path. The compile path built the three
heavy package binaries sequentially with default `go test` vet, dynamically
listed tests from each absolute binary while its working directory was the
package source directory, partitioned weighted top-level tests, compressed and
uploaded the binaries once, downloaded the artifact independently for every
shard, then ran all shards concurrently.

The reported modeled path is:

```text
max(
  heavy compile + dynamic inventory + artifact upload + longest download + longest shard,
  all non-heavy packages once,
  strengthened standalone go vet
)
```

`heavy compile` was stopped before partition-plan generation and ZIP
compression; the upload measurement covers the blob transfer itself. The
end-to-end orchestration clock includes those omitted steps, process startup,
all concurrent jobs, and equivalence verification, but stops before final
result ZIP compression/upload. Both values are reported so the approximately
8–9 s of real orchestration overhead is not hidden.

The baseline's post-suite `go test -vet=off -c` calls were timing decomposition
only. Every fidelity candidate retained default vet, and the concurrent
standalone `go vet ./...` is explicitly strengthened coverage.

## Exact commands

Provisioning used the checked-in private-VM helper with a fresh run ID and UTC
expiry:

```sh
export A2_RUN_ID='<fresh-lowercase-run-id>'
tools/ci-bench/create-azure-vm.sh \
  --agent a2 \
  --run-id "$A2_RUN_ID" \
  --location westus3 \
  --vm-size Standard_D16ads_v5 \
  --image-urn MicrosoftWindowsServer:WindowsServer:2025-datacenter-azure-edition:26100.33222.260810 \
  --expires '<UTC-expiry>'
```

The product and harness were independently pinned before the Windows gate:

```sh
git -C '<product>' checkout --detach ee6468a6a49d9b2a1a828bd276792f415f392185
git -C '<harness>' checkout --detach b22be6a7
```

The important commands inside the Windows drivers were:

```powershell
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '1'

# Accepted monolithic reference (default vet).
pwsh -NoProfile -File tools/ci-bench/run-go-suite.ps1 `
  -RepositoryPath '<absolute-product>' `
  -OutputDirectory '<absolute-baseline-output>' `
  -RunId 'a2-baseline-04' `
  -RunLabel 'identical-seed-default-vet-monolithic-baseline' `
  -ExpectedRepositorySha 'ee6468a6a49d9b2a1a828bd276792f415f392185' `
  -ExpectedGoVersion 'go1.26.7'

# Fidelity compilation: default go test vet is deliberately retained.
go test -c ./internal/sem -o '<absolute-binaries>/sem.test.exe'
go test -c ./internal/cli -o '<absolute-binaries>/cli.test.exe'
go test -c ./internal/gitutil -o '<absolute-binaries>/gitutil.test.exe'

pwsh -NoProfile -File tools/ci-bench/list-tests.ps1 `
  -RepositoryPath '<absolute-product>' `
  -BinaryPath '<absolute-binary>' `
  -PackageArgument './internal/sem' `
  -OutputPath '<absolute-inventory.json>'

python tools/ci-bench/partition-tests.py `
  --inventory '<sem-inventory.json>' `
  --inventory '<cli-inventory.json>' `
  --inventory '<gitutil-inventory.json>' `
  --timings results/windows-ci/a2/top-level-timings.jsonl `
  --shards 8 `
  --output '<plan.json>'

pwsh -NoProfile -File tools/ci-bench/run-test-shard.ps1 `
  -PlanPath '<absolute-plan.json>' `
  -ShardIndex 0 `
  -RepositoryPath '<absolute-product>' `
  -BinaryDirectory '<absolute-downloaded-binaries>' `
  -OutputDirectory '<absolute-shard-output>' `
  -ExpectedRepositorySha 'ee6468a6a49d9b2a1a828bd276792f415f392185' `
  -Timeout 30m

go test -json -timeout 30m '<every target-Windows non-heavy package exactly once>'
go vet ./...

python tools/ci-bench/verify-shard-inventory.py `
  --baseline-events '<baseline-events.jsonl>' `
  --candidate-events '<other-events.jsonl>' `
  --candidate-events '<each-shard-events.jsonl>' `
  --plan '<plan.json>' `
  --inventory '<each-heavy-inventory.json>' `
  --package-inventory '<windows-package-inventory.json>' `
  --metadata '<every-phase-metadata.json>' `
  --output '<equivalence.json>'
```

Each shard invocation resolves to this exact shape, with an absolute Go
executable and test binary and the package source as the working directory:

```text
<absolute-go.exe> tool test2json -t -p <import-path> <absolute-test.exe> -test.timeout=30m -test.run=<compressed-regex> -test.v=test2json
```

`test2json`'s real child exit status is captured immediately. The runner
serializes the executable, every argument, quoting/backslash expansion,
separators, and terminating NUL. It fails closed above 30,000 UTF-16
characters, below the Win32 absolute 32,767-character limit. It never batches
implicitly because batching would repeat package initialization and
`TestMain` without making that change explicit.

Cleanup used the exact derived target and then checked for absence:

```sh
tools/ci-bench/delete-azure-resources.sh \
  --agent a2 --run-id "$A2_RUN_ID" --yes
```

## Individual measurements

The accepted randomized screens were:

| Shards | Modeled path | Orchestration | Compile | Upload | Longest download | Longest shard | Other packages | Vet | Tests/shard | Worst argv |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 8 | 296.012 s | 304.879 s | 104.143 s | 0.998 s | 1.608 s | 189.264 s | 139.070 s | 84.133 s | 288–289 | 9,093 |
| 4 | 322.246 s | 329.173 s | 104.280 s | 1.117 s | 1.284 s | 215.565 s | 130.559 s | 80.665 s | 576–577 | 16,820 |
| 6 | 299.189 s | 306.237 s | 101.860 s | 1.179 s | 1.413 s | 194.737 s | 136.813 s | 83.909 s | 384–385 | 12,047 |

The 8-shard winner repetitions were:

| Run | Modeled path | Orchestration | Compile | Longest shard | Other packages | Vet |
|---|---:|---:|---:|---:|---:|---:|
| screen-v2-8-01 | 296.012 s | 304.879 s | 104.143 s | 189.264 s | 139.070 s | 84.133 s |
| repeat-v2-8-02 | 292.095 s | 300.074 s | 102.256 s | 187.167 s | 142.362 s | 81.003 s |
| repeat-v2-8-03 | 294.231 s | 303.221 s | 102.866 s | 188.345 s | 144.474 s | 80.589 s |
| **Median** | **294.231 s** | **303.221 s** | **102.866 s** | **188.345 s** | **142.362 s** | **81.003 s** |
| **Range** | **292.095–296.012 s** | **300.074–304.879 s** | **102.256–104.143 s** | **187.167–189.264 s** | **139.070–144.474 s** | **80.589–84.133 s** |

The population standard deviations were 1.602 s for modeled path and 1.993 s
for orchestration wall time.

## Resource measurements

Two-second whole-VM samples show that sharding spends the saved wall time by
using the machine more fully:

| Run | Mean CPU | CPU p95/max | Disk B/s p95 | Disk B/s max |
|---|---:|---:|---:|---:|
| baseline-04 | 27.62% | 90% / 100% | 7,893,792 | 234,860,886 |
| screen-v2-4-01 | 64.74% | 100% / 100% | 222,056,405 | 277,624,994 |
| screen-v2-6-01 | 72.11% | 100% / 100% | 139,627,108 | 268,992,723 |
| screen-v2-8-01 | 75.98% | 100% / 100% | 125,876,123 | 344,689,735 |
| repeat-v2-8-02 | 78.46% | 100% / 100% | 169,016,739 | 337,381,511 |
| repeat-v2-8-03 | 75.01% | 100% / 100% | 181,576,713 | 293,176,132 |

For the three winner runs, mean CPU was 75.01–78.46%, disk p95 was
125.9–181.6 MB/s, and disk maximum was 293.2–344.7 MB/s. The minimum available
memory remained above 57.2 GB and maximum committed memory remained below
11.8 GB, so neither memory capacity nor paging was the limiting resource.

## Process-semantics audit

`internal/sem` and `internal/cli` each have a Windows `TestMain` whose normal
path simply calls `m.Run`; special environment markers deliberately re-execute
the absolute test binary for fake-Git child behavior. `internal/gitutil` has no
`TestMain`, but tests deliberately execute `os.Args[0]`. Absolute binaries and
package-source working directories preserve all three behaviors.

Dynamic listing starts each heavy binary once. The 8-shard plan then starts
each of the three heavy packages eight times, so package init is repeated eight
times per heavy package and the two `TestMain` functions are repeated eight
times each during execution. The baseline starts each package once. This is an
intentional semantic difference, disclosed by metadata and package terminal
multiplicity rather than hidden by command batching.

Package-scope declarations are static fixtures, compiled regexes, reflection
values, and one disabled golden-update flag. The audit found no fixture
map/slice mutation after initialization. CLI tests that call `os.Chdir` are
not parallel and register cleanup; `t.Setenv` restores environment changes.
The remaining risk is an undiscovered dependency on state left by another
top-level test. See
`results/windows-ci/a2/testmain-package-global-audit.md` for the focused audit.

## Failed and rejected experiments

| Run | Observed result | Disposition |
|---|---|---|
| baseline-01 | 772.972 s, three CLI failures | Rejected: explicit PATH omitted Git's `bin`, so `sh` was unavailable. |
| baseline-02 | Passed, 774.476 s wrapper / 719.947 s event wall | Rejected: `Tee-Object` mojibaked two em-dash test names. |
| baseline-03 | Passed, 772.481 s wrapper / 721.531 s event wall | Diagnostic only: started before exact `b22be6a7` UTF-8 wrapper staging. |
| screen-8-01 | 285.768 s modeled, equal event counts | Rejected: correct candidate Unicode compared with the rejected mojibake baseline, producing four exact-key differences. |
| screen-8-02 | 292.847 s modeled and exact parity | Rejected after guard tightening: old partitioning produced a 31,580-character argv, above the conservative 30,000 threshold. |
| screen-4-01 | 307.916 s partial modeled path | Failed closed: old partitioning attempted a 35,438-character `sem` argv. |

All failures and diagnostic runs were retained in the secure raw archive; none
was used in the accepted median.

## Unexpected observations

1. The newly created private subnet had no default outbound access. A Standard
   NAT Gateway was required for bootstrap while keeping the VM without a public
   IP or inbound path.
2. PowerShell's inherited service-context encoding can corrupt otherwise valid
   Go UTF-8 JSON. Setting both console and pipeline output encodings before the
   native command fixed the exact multiset without normalizing names.
3. Many top-level terminal durations round to zero. Pure weight-based LPT made
   those placements objective-neutral but concentrated names into one regex.
   The v2 deterministic count-first tie-break for zero-weight tests reduced the
   worst 8-shard argv from 31,580 to 9,093 characters without changing weight
   totals.
4. The 8-shard layout saturated CPU at p95 while staying well within memory.
   Four shards had larger disk p95 bursts but a longer critical test shard;
   storage throughput alone did not determine the winner.

## Validation

The Windows gate parsed every staged PowerShell script in PowerShell 7, checked
the pinned environment, exercised resource monitoring and the UTF-8 suite
wrapper, performed compile decomposition, and ran A2-specific inventory/shard
smokes before the baseline. Local focused verification ran 13 Python unit tests
covering timing ingestion, deterministic LPT, trie regex exactness, zero-weight
balance, exact dynamic multisets, no-test package skips, package terminal
multiplicity, and recursive exit-code failure. All passed. The prototype YAML
also parsed successfully.

The workflow was not pushed or executed: GitHub does not expose a
branch-only `workflow_dispatch` until the workflow exists on the default
branch. It remains a reviewable prototype, not production evidence.

## Artifacts and reproduction

Raw ZIPs, executables, full JSONL streams, stderr/stdout logs, Run Command
responses, and environment captures are intentionally not versioned. They are
held in an operator-local directory with mode 0700 and files with mode 0600.
`results/windows-ci/a2/artifact-checksums.json` lists all 22 raw archive paths,
byte sizes, and SHA-256 values without cloud account or resource identifiers.

Reviewable evidence is limited to:

- `results/windows-ci/a2/experiment-summary.json`;
- `results/windows-ci/a2/artifact-checksums.json`;
- `results/windows-ci/a2/cost-rate.json`;
- `results/windows-ci/a2/randomization.json`;
- `results/windows-ci/a2/top-level-timings.jsonl`;
- `results/windows-ci/a2/testmain-package-global-audit.md`;
- `results/windows-ci/a2/cleanup.json`.

To reproduce, provision a new tagged private VM with the command above, attach
an outbound-only path, install the pinned toolchain, stage the exact harness
and product commits, create one cache seed, run baseline-04, draw and execute
the 4/6/8 screens, repeat the winner, export artifacts before cleanup, and run
the verifier for every accepted comparison. The manual prototype is
`.github/workflows/windows-compile-once-experiment.yml`.
