# Result

## Verdict

**NO-GO — larger hardware alone does not bring the measured Windows critical path to 7 minutes or less.** The fastest accepted complete treatment on the 32-vCPU VM was 566.466 seconds (9m26.5s), and the three-run D32 complete-treatment median was 571.822 seconds (9m31.8s). The median is 151.822 seconds, or 36.1%, above the 420-second target. Even the narrower exact test kernel missed: its best and D32 median were 550.180 and 556.693 seconds.

Increasing the comparable VM family from 4 to 32 vCPU produced only a 1.44× complete-treatment speedup (1.39× for the kernel) for 8× the vCPU count. Mean CPU utilization fell from 60.0% to about 15.1%, while the median disk queue remained zero. This is a plateau dominated by the suite's package/test/process/filesystem behavior, not a shortage of aggregate cores.

## Best measured critical path

The best accepted complete measured treatment was **566.466 seconds** on `Standard_D32ads_v7`; the three-run D32 median was **571.822 seconds**. The complete treatment covers pin/VM validation, cache restore, the suite wrapper, post-suite diagnostics, cleanup, and terminal treatment metadata. Within the best treatment, the suite wrapper took 559.193 seconds and the exact `go test -json -timeout 30m ./...` kernel took 550.180 seconds. Artifact packaging and managed-identity upload followed and were measured separately (well under one second combined for every accepted run); they are acceptance evidence but are not included in `treatmentSeconds`.

Those are Azure experiment measurements, not a hosted-runner workflow critical path. The pinned GitHub Actions workflow runs checkout, Go setup, and `go test -timeout 30m ./...` in its `test` job; its Windows `build` job is a separate concurrent matrix job and must not be added serially to the test result. The experiment added `-json` for event-level measurement and reused one prepared VM, so it does not establish byte-for-byte or lifecycle parity with `windows-latest`.

## Coverage equivalence

All six accepted treatments passed. Every treatment used repository SHA `ee6468a6a49d9b2a1a828bd276792f415f392185`, Go 1.26.7, `windows/amd64`, `CGO_ENABLED=1`, harness SHA `b22be6a73adac8d9c582af4bfc681c5d7a517221`, the exact measured command, and the same cache-seed SHA-256.

Each accepted run observed the same 4,207 unique test `run` events and 21 final package events. The normalized test-inventory SHA-256 was `5742e245912be38f1d4fe77b68b23c03e3ff5d07be39ea660c40c034e772fd35`; no package failed. This proves equivalence among the accepted Azure treatments. It does not prove identity with the hosted image because the Azure image, toolchain layout, Run Command service context, workflow command formatting, and VM lifecycle differ.

## Cost

At the official Azure Retail Prices API rates fetched at 2026-09-02T17:11:25Z, the regular Windows VM list rates in East US 2 were $0.412, $0.824, $1.648, and $3.296 per hour for D4, D8, D16, and D32 respectively. The measured-treatment VM-meter estimates were $0.094 for D4, $0.144 for D8, $0.298 for D16, and a median $0.524 for D32.

D32 therefore cost about 5.6× as much per measured treatment as D4 for a 1.44× complete-treatment speedup (1.39× kernel speedup). These are list-price lower bounds over the measured treatment windows, not invoice totals: they exclude boot/resize/deallocation latency, rejected runs, bootstrap, idle time, and shared disk/network/blob charges.

The exact image version does not have its own Retail Prices API meter. The selected rows are the exact regular Windows SKU/region meters, while the Marketplace image was separately pinned to `MicrosoftWindowsServer:WindowsServer:2025-datacenter-azure-edition:26100.33222.260810`. Shared meters are recorded separately in `results/windows-ci/a1/cost.json`: P10 LRS disk $17.92/month, a separately returned P10 mount meter $0.91/month whose applicability was not inferred, NAT Gateway $0.045/hour plus $0.045/GB, and its Standard static IPv4 $0.005/hour.

## Main risk

The dominant validity risk is environment identity. Azure Run Command executes under a Windows service context, not the GitHub-hosted interactive/service layout. That difference caused Git Credential Manager to wait for input during a deliberately invalid remote test and later exposed a missing MinGit `usr\bin` entry needed for `sh`. The accepted environment explicitly sets `GIT_TERMINAL_PROMPT=0`, `GCM_INTERACTIVE=Never`, and the discovered MinGit path. This is a CI-equivalent noninteractive correction, not a claim that the environment is literally identical to the workflow.

A second risk is sampling depth: D32 has three accepted repetitions, while D4/D8/D16 are single randomized screens. The D32 complete-treatment range was 50.318 seconds and the nested kernel range was 33.376 seconds, enough that D8 beat the single D16 screen. That uncertainty does not threaten the decision because even the best observed run missed the target by more than two minutes.

## Recommendation

Do not move the Windows test job to a 32-vCPU runner solely to reach seven minutes. D8's kernel was 594.591 seconds, but its complete treatment was 627.547 seconds (10m27.5s); D32 was the only size with an accepted complete treatment at or below ten minutes. Any larger-runner purchase should therefore be treated as a cost/latency trade rather than a solution.

The next work should reduce the serial/process/filesystem component, especially in `internal/sem`, and then rerun the same controlled curve. Potential directions include splitting the longest package into independent jobs, reducing repeated Git/process launches and deep-path filesystem setup, and measuring package-level concurrency. Keep the current build job concurrent rather than folding it into the test path.

# Experiment record

## Scope and decision rule

The decision question was whether comparable, larger Premium-IO Windows hardware alone could reduce the complete measured treatment to 420 seconds or less without reducing test coverage. The exact instrumented test kernel was retained as a nested decomposition, not substituted for the end-to-end treatment criterion. A treatment was accepted only if all of the following agreed:

- repository, Go, OS/architecture, CGO, harness, VM size, and cache seed matched their pins;
- `go clean -testcache` ran immediately before `go test -json -timeout 30m ./...`;
- suite state and phase were `completed` and `finished`;
- Go test, suite metadata, inner PowerShell process, and outer driver exit codes were all zero;
- managed-identity artifact and checksum upload completed, and the downloaded SHA-256 verified;
- the external watchdog observed the artifact and retained a bounded deallocation path.

The randomized screen order was D32, D4, D8, D16. After the first valid D32 point was credible enough to affect the decision, two additional D32 repetitions were run, for three accepted D32 observations total.

## Environment and controls

The experiment used East US 2 and the unrestricted `StandardDadsv7Family` sizes `Standard_D4ads_v7`, `Standard_D8ads_v7`, `Standard_D16ads_v7`, and `Standard_D32ads_v7`. Validation showed Premium IO support, no regional SKU restriction, 65 regional vCPUs of quota with zero initially used, and 350 family vCPUs of quota with zero initially used. All VMs were regular/non-Spot.

The disposable resource group was `rg-entire-win-ci-a1-scale-260902`. Every resource carried the required purpose, agent, run, expiry, and owner tags. The VM and NIC had no public IP and no custom inbound rule. Secure outbound used a Standard NAT Gateway; the only static public IP was attached to the NAT, not the VM/NIC. Results moved through private Blob Storage with the VM's managed identity and least-scoped blob data access. The OS disk was a 128-GiB Premium LRS disk.

The D32 observation reported Windows Server 2025 Datacenter Azure Edition build 26100.33222, AMD EPYC 9V45, 32 logical processors, 137,433,444,352 bytes of physical memory, and a Premium NVMe virtual OS disk formatted NTFS. Tool versions were Go 1.26.7, Git for Windows 2.55.0, GCC 16.1.0, and PowerShell 7.

The accepted command resolution was:

| Tool | Resolved path |
| --- | --- |
| `sh` | `C:\tools\mingit\usr\bin\sh.exe` |
| `git` | `C:\tools\mingit\cmd\git.exe` |
| `gcc` | `C:\ProgramData\mingw64\mingw64\bin\gcc.exe` |
| `go` | `C:\tools\go\bin\go.exe` |
| `pwsh` | `C:\tools\pwsh\pwsh.exe` |

## Cache and setup

The accepted seed contained 2,333 files and 259,078,925 uncompressed bytes, with SHA-256 `191fe6f421eb53af4f73f90055c1f1eea2a2fa0ea673c2e91f01646bcb467422`. It used a dedicated module cache populated by `go mod download` and a dedicated build cache warmed by `go test -run '^$' -timeout 30m ./...`; the test cache was then cleaned. Seed preparation took 113.167 seconds: 4.901 seconds module download, 106.869 seconds warm no-test build, 0.034 seconds test-cache clean, and 1.261 seconds archive.

Every accepted treatment restored this identical archive and then cleaned only the test cache. Restore time ranged from 1.458 to 7.373 seconds. Packaging ranged from 0.370 to 0.778 seconds; upload ranged from 0.071 to 0.102 seconds; accepted artifacts were 667–683 KiB.

Fresh tool installation was measured once before a later checkout failure: PowerShell 6.925 seconds, Go 39.039 seconds, Git 2.123 seconds, and MinGW 39.310 seconds, totaling 87.397 seconds. Because the subsequent checkout failed under Windows PowerShell 5 native-stderr behavior, this is retained as setup decomposition, not an accepted end-to-end bootstrap. On the persisted toolchain, the successful product and harness checkouts took 4.348 and 3.054 seconds.

## Scaling results

| Treatment | vCPU | Accepted reps | Kernel seconds | Suite wrapper seconds | Measured treatment seconds | Regular Windows rate | Treatment VM estimate |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `Standard_D4ads_v7` | 4 | 1 | 775.422 | 796.783 | 820.736 | $0.412/h | $0.094 |
| `Standard_D8ads_v7` | 8 | 1 | 594.591 | 605.727 | 627.547 | $0.824/h | $0.144 |
| `Standard_D16ads_v7` | 16 | 1 | 611.585 | 629.763 | 651.817 | $1.648/h | $0.298 |
| `Standard_D32ads_v7` r1 | 32 | 1 | 556.693 | 565.051 | 571.822 | $3.296/h | $0.524 |
| `Standard_D32ads_v7` r2 | 32 | 1 | 583.556 | 594.038 | 616.783 | $3.296/h | $0.565 |
| `Standard_D32ads_v7` r3 | 32 | 1 | **550.180** | **559.193** | **566.466** | $3.296/h | $0.519 |
| D32 median | 32 | 3 | 556.693 | — | 571.822 | $3.296/h | $0.524 |

D32 complete-treatment variance was 566.466–616.783 seconds, a 50.318-second range with 27.635 seconds sample standard deviation. The nested kernel was 550.180–583.556 seconds, a 33.376-second range with 17.692 seconds sample standard deviation. The six accepted measured treatments cost an estimated $2.143 in VM meters over their measured treatment windows; this excludes all shared or unmeasured lifecycle costs.

## Resource and package evidence

| Treatment | Mean CPU | CPU p50 | CPU p95 | Disk queue p50 | Disk queue p95 | Slowest package (`internal/sem`) |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| D4 | 60.0% | 65.0% | 100.0% | 0 | 2 | 729.250s |
| D8 | 34.2% | 23.5% | 86.5% | 0 | 2 | 567.225s |
| D16 | 23.4% | 17.0% | 50.0% | 0 | 1 | 586.830s |
| D32 r1 | 15.2% | 10.0% | 42.0% | 0 | 2 | 548.496s |
| D32 r2 | 14.8% | 10.0% | 39.5% | 0 | 2 | 557.051s |
| D32 r3 | 15.2% | 10.0% | 43.2% | 0 | 1 | 541.841s |

The larger sizes have ample CPU headroom and no sustained disk queue. `internal/sem` improved only about 1.35× from D4 to the best D32 repetition, so it sets most of the plateau. `internal/cli` and `internal/gitutil` improved more but remained far shorter than `internal/sem`.

The harness also compiled `internal/sem`, `internal/cli`, and `internal/gitutil` after the suite. At D32 the median probe times were about 4.311, 3.159, and 0.666 seconds. These are **warm-cache post-suite diagnostic probes**, not the compile cost embedded inside the 550–584 second exact kernel, and must not be subtracted from or added to the kernel.

The monitor's `_Total` disk-time counter can exceed 100% when Windows aggregates devices. Disk-time maxima are therefore not used to claim saturation; the queue distribution, bytes/second, and CPU distribution are the more useful evidence.

## Harness validation and rejected evidence

The PowerShell harness was parsed and exercised on Windows before scaling. The accepted smoke artifact proved environment capture, resource monitoring, bounded suite execution, terminal metadata, zero exit propagation, and export. Several real failures improved the harness and are retained rather than hidden:

1. The first D32 attempt used a pre-UTF-8-output harness and lacked noninteractive Git settings. `TestRunReportsCloneFailureInsteadOfMeasuringTheStaleCache` waited in `git ls-remote`, the exact kernel ended at 1,827.622 seconds with the 30-minute timeout, and Run Command stayed open while Azure CPU was idle for more than ten minutes. Terminating and recovering the disk showed a `Start-Process -Wait` descendant/process-tree hang after the kernel. This run is rejected from timing conclusions.
2. The next D32 diagnostic added noninteractive Git and finished the kernel in 563.522 seconds, but three `internal/cli` tests failed because `sh` was absent from PATH. Its wrapper and suite exit codes also disagreed. It is rejected.
3. A bounded graph-bench proof showed the invalid remote failed promptly for the expected noninteractive reason and direct `WaitForExit()` finalized and exported successfully.
4. A bounded CLI proof resolved the exact MinGit `usr\bin` path and produced agreement among suite metadata, inner PowerShell, outer process, driver, checksum, and managed-identity export. Only after that proof did scaling resume.

Earlier bootstrap attempts also caught native-stderr handling, GCC path discovery, optional IMDS fields, Tee output contamination, empty error-list handling, and JSON event encoding. Details and artifact hashes are in `results/windows-ci/a1/rejected-runs.json`.

## Runner access finding

The organization runner API returned HTTP 403 because the credential lacked runner-administration scope. The repository runner API succeeded and returned an empty inventory (`total_count: 0`). This is an access finding, not an experiment blocker; no inference is made about organization-level runner configuration.

## Cleanup

The VM was deallocated after the final accepted repetition. The guarded cleanup validated the exact group and all eight resources before deletion, then deleted `rg-entire-win-ci-a1-scale-260902`. Post-delete checks at 2026-09-02T17:24:41Z returned `group_exists=false`, zero resources with the A1/run tags, and zero resource groups with those tags. A repeated validate-only cleanup reported the exact group absent, confirming idempotent success. Sanitized evidence is in `results/windows-ci/a1/cleanup.txt`.

## Compact evidence and reproduction

Committed evidence is intentionally sanitized and compact:

- `results/windows-ci/a1/summary.json` — accepted timings, exit agreement, coverage hashes, resource distributions, package/test leaders, and artifact hashes;
- `results/windows-ci/a1/environment.json` — sanitized image, hardware, toolchain, path, security, and cache facts;
- `results/windows-ci/a1/cost.json` — timestamped official meters, formulas, estimates, and exclusions;
- `results/windows-ci/a1/rejected-runs.json` — rejected-run diagnoses and acceptance-proof hashes;
- `results/windows-ci/a1/runner-access.json` — the 403/empty-inventory finding;
- `results/windows-ci/a1/manifest.sha256` — checksums for the compact evidence set;
- `results/windows-ci/a1/cleanup.txt` — sanitized Azure cleanup evidence.

Raw ZIPs, binaries, JSONL event streams, performance samples, and logs are retained outside Git on the experiment host under `/Users/thomi/Projects/entire-graph-windows-ci-a1-raw-260902/`. They are not required to reproduce the protocol. The A1 drivers under `tools/ci-bench/a1/` implement bootstrap, identical seed creation/restoration, managed-identity transfer, direct process waiting, fail-closed exit agreement, and a separate Azure watchdog. They take resource/storage names as parameters and contain no credentials.
