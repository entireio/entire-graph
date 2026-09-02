# Result

## Verdict

CONDITIONAL

Linux cross-compilation plus weighted execution on Windows met the acceptable
seven-minute target while preserving every observed Windows test event. The
best parity-gated critical path was 378.748 seconds (6 minutes 18.7 seconds),
versus 1,278.985 seconds for the accepted native Windows `go test` baseline.

The condition is toolchain fidelity: the cross binaries use GCC 13.2 with
statically linked MinGW support and MSVCRT, while the native Windows gates use
GCC 16.2 with UCRT and MinGW runtime DLLs. Keep native Windows production
build, vet, and test-link gates, and retain a periodic native execution lane,
until the host toolchains and C runtimes are aligned.

## Best measured critical path

The recommended candidate runs Linux compile/upload concurrently with the
native Windows fidelity gates, then transfers one artifact and executes the
eight-shard winner:

```text
max(Linux compile + upload, Windows native gates) + transfer + Windows tests
= max(46.996 + 9.000, 101.976) + 5.415 + 271.357
= 378.748 seconds (6m 18.7s)
```

The cross-only path was 332.768 seconds (5 minutes 32.8 seconds). Serializing
the native gates would take 434.744 seconds (7 minutes 14.7 seconds). The
accepted native Windows baseline was 1,278.985 seconds (21 minutes 19.0
seconds), so the concurrent parity-gated path was 3.377 times faster and
reduced the measured critical path by 70.39%.

The time decomposition is:

| Phase | Measured time |
|---|---:|
| Linux compile/link, median of 3 | 46.996 s |
| Linux managed-identity upload phase | 9.000 s |
| Windows download, hash verification, extraction | 5.415 s |
| Windows weighted execution, median of 3 | 271.357 s |
| Native Windows fidelity gates | 101.976 s |
| Accepted native Windows `go test` baseline | 1,278.985 s |

## Coverage equivalence

All accepted screens and repetitions matched the native baseline's fully
qualified `(package, test/subtest, action)` `run`/`pass`/`fail`/`skip`
multiset, including duplicate counts: 8,414 baseline events, 8,414 candidate
events, zero missing, zero unexpected, and zero JSON diagnostics.

The additional fail-closed checks passed:

- The native and cross target selections matched for all 21 repository
  packages, including `GoFiles`, `CgoFiles`, `TestGoFiles`, and `XTestGoFiles`.
- Eight package test binaries exposed the same 2,388 native and cross names:
  2,383 default `Test` roots plus five excluded benchmarks. No default
  `Example` or `Fuzz` roots were present.
- The three heavy packages (`internal/sem`, `internal/cli`, and
  `internal/gitutil`) were partitioned by top-level root, with every subtest
  attached to its parent. Each of the five non-heavy package binaries ran once
  per candidate.
- Every process used an absolute PE path, its package directory as the working
  directory, `go tool test2json -t -p`, `-test.v=test2json`, and
  `-test.timeout=30m`. The real `test2json` exit code was checked.
- Exact Windows CRT command-line serialization, including the quoted Go path,
  backslash expansion, separators, and terminating NUL, stayed below the
  30,000-character guard. The accepted winner's maximum was 11,598.
- Build metadata proved `GOOS=windows`, `GOARCH=amd64`, and `CGO_ENABLED=1`.
  The tree-sitter/CGO assertion and the child re-exec assertion passed in every
  accepted candidate.
- All eight native Windows test binaries linked successfully. Native
  `go build ./...` and target-Windows `go vet ./...` also exited zero. A native
  production build alone was not treated as test-link equivalence.

Sharding repeats package initialization and `TestMain` for heavy packages. The
audit found three `TestMain` definitions: `cmd/entire-graph` remains
unsharded, while `internal/cli` and `internal/sem` are repeated. Their visible
behavior only dispatches opt-in helper modes before `m.Run`, and dynamic output
was identical, but hidden package-global ordering remains a fidelity risk.

## Cost

The official Azure Retail Prices API was queried at
2026-09-02T15:35:00Z for exact, non-Spot `Standard_D8ds_v5` Consumption meters
in `westus3`: $0.452/hour for Linux and $0.82/hour for Windows, effective
2022-08-01.

Measured concurrent compute cost was approximately $0.0933 per parity-gated
candidate:

```text
Linux:  (46.996 + 9.000) * $0.452 / 3600
Windows: (101.976 + 5.415 + 271.357) * $0.82 / 3600
Total: $0.0933009
```

The native baseline's measured compute cost was $0.2913243, a 67.97%
reduction. The cross-only candidate path was $0.0700731. Premium disk, NAT,
Blob capacity/transactions, and any network processing are separate; the
candidate transfers 92,742,606 bytes in-region, and those small charges must be
confirmed from a billing export rather than inferred.

## Main risk

The host compiler and C runtime do not match native Windows:

- Cross: `x86_64-w64-mingw32-gcc/g++` 13.2.0, static MinGW support,
  `KERNEL32.dll` plus `msvcrt.dll` imports for the five CGO packages, and no
  external MinGW runtime DLL dependency.
- Native: MinGW-w64 GCC/G++ 16.2.0 UCRT; the same five CGO packages import
  `libgcc_s_seh-1.dll`, `libstdc++-6.dll`, `KERNEL32.dll`, and UCRT API-set
  DLLs.

The matched already-linked parallel diagnostic was 561.479 seconds cross and
562.989 seconds native, only 1.511 seconds or 0.27% apart. This removes runtime
speed as an explanation for the candidate win, but it does not make the two
runtime implementations equivalent. The remaining risk is a defect that only
native GCC 16/UCRT compilation or execution exposes, plus the repeated
`TestMain`/initialization semantics described above.

## Recommendation

Adopt the eight-shard cross-execution design as a fast Windows signal only if
the native Windows build, vet, and all-package test-link gates remain required
and can run concurrently. Keep native test execution as a periodic or rollout
comparison until the cross compiler uses the same GCC/UCRT runtime construction
as the native lane. Fail closed on any target-file, compiled-list, dynamic
multiset, PE-import, child-reexec, or toolchain assertion.

Do not use the adjacent-DLL construction: child tests copy only their PE into
an isolated directory and replace `PATH`, so adjacent runtime DLLs do not
survive the exact re-exec behavior. Do not use the three-repetition sequential
monolithic driver either; it provides no bounded progress and loses the
critical-path benefit.

## Environment

| Component | Measured configuration |
|---|---|
| Product | `entireio/entire-graph` at `ee6468a6a49d9b2a1a828bd276792f415f392185` |
| Coordinator harness | `b22be6a73adac8d9c582af4bfc681c5d7a517221` |
| Go | 1.26.7 |
| Linux compiler VM | Azure `Standard_D8ds_v5`, Regular, Ubuntu 24.04, Premium SSD |
| Windows executor | Azure `Standard_D8ds_v5`, Regular, Windows Server 2025 Azure Edition, Premium SSD |
| Cross target | `windows/amd64`, CGO enabled, GCC/G++ 13.2.0 |
| Native toolchain | GCC/G++ 16.2.0 UCRT, Git for Windows 2.55.0, PowerShell 7.6.5 |
| Network | No public IPs, no custom inbound rules, tagged Standard NAT gateway |
| Artifact transport | Private Azure Blob, TLS 1.2 minimum, managed identities and RBAC; no SAS or account keys |

The product checkout and Go version were asserted before each driver ran. All
accepted Linux compile repetitions copied the same empty seed `GOCACHE`
(`abcfa6a9d4df344d1781bc2560b5e4cdcae08b39ed303063535e7e1e926a304a`)
and shared the already-downloaded module cache. Windows test caches were cleaned
before every candidate.

## Exact commands and reproduction

Linux compilation used:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=1 \
CC=x86_64-w64-mingw32-gcc \
CXX=x86_64-w64-mingw32-g++ \
CGO_LDFLAGS='-static -static-libgcc -static-libstdc++' \
tools/ci-bench/a3/build-cross-binaries.sh \
  --repo /opt/entire-a3/product \
  --output /opt/entire-a3/cross-results \
  --repetitions 3
```

The accepted native baseline used:

```powershell
go clean -testcache
go test -json -timeout 30m ./...
```

The Windows decision path used:

```powershell
pwsh -NoProfile -File tools/ci-bench/a3/run-cross-weighted-shards.ps1 `
  -RepositoryPath C:\entire-a3\product `
  -CrossArtifactRoot C:\entire-a3\cross-artifacts `
  -OutputDirectory C:\entire-a3\weighted `
  -BaselineJsonPath C:\entire-a3\baseline\go-test.jsonl `
  -RepeatCount 3 `
  -CandidateWatchdogSeconds 1600

pwsh -NoProfile -File tools/ci-bench/a3/run-native-fidelity.ps1 `
  -RepositoryPath C:\entire-a3\product `
  -CrossArtifactRoot C:\entire-a3\cross-artifacts `
  -OutputDirectory C:\entire-a3\native-fidelity `
  -BaselineJsonPath C:\entire-a3\baseline\go-test.jsonl `
  -EvidenceHelperPath tools\ci-bench\a3\compare-evidence.py `
  -ResourceMonitorPath tools\ci-bench\monitor-resources.ps1 `
  -WorkerPath tools\ci-bench\a3\run-cross-shard-worker.ps1
```

The branch-only manual workflow is
`.github/workflows/windows-cross-compile-experiment.yml`. GitHub cannot
dispatch a workflow that exists only off the default branch, so the workflow
is a reviewable prototype; all measurements above ran directly on disposable
Azure infrastructure without pushing the experiment merely to trigger it.

## Individual run timings

The accepted cross artifacts were statically linked and contained exactly the
same eight package binaries:

| Run | Compile/link | Archive bytes | Seed cache |
|---|---:|---:|---|
| cross-01 | 47.203 s | 92,742,606 | identical empty seed |
| cross-02 | 46.996 s | 92,742,422 | identical empty seed |
| cross-03 | 46.049 s | 92,742,413 | identical empty seed |

Compile median was 46.996 seconds, range 46.049–47.203, sample coefficient of
variation 1.316%. CPU averaged 62.526–65.568% across the compile repetitions,
with p95 near 100%; disk utilization averaged 7.588–13.601%.

The randomized shard screen used seed `1206663894`, producing order 6, 4, 8:

| Screen | Duration | Longest shard | Package invocations | Max argv characters |
|---|---:|---:|---:|---:|
| 6 shards | 267.042 s | 265.586 s | 23 | 15,570 |
| 4 shards | 308.416 s | 307.493 s | 17 | 22,343 |
| 8 shards | 254.179 s | 253.119 s | 29 | 11,598 |

The eight-shard winner then ran against three independently built artifacts:

| Accepted repetition | Duration | Longest shard | Dynamic parity |
|---|---:|---:|---|
| repeat-01 | 286.065 s | 284.698 s | exact |
| repeat-02 | 271.357 s | 270.104 s | exact |
| repeat-03 | 267.461 s | 265.573 s | exact |

Execution median was 271.357 seconds, range 267.461–286.065, sample standard
deviation 9.812 seconds, and coefficient of variation 3.568%. Candidate CPU
averaged 95.789–96.606%, p95 was 100%, minimum free RAM exceeded 26 GiB, and
disk utilization averaged 14.908–18.090%.

One parallel-unsharded cross diagnostic ran every package binary once in
561.479 seconds, with exact dynamic parity. It isolates the 2.07-times
additional gain from within-package sharding. The corresponding already-linked
native diagnostic took 562.989 seconds and also had exact parity.

## Native fidelity results

- Native target selection: 21 packages, exact match to cross selection.
- Native/cross test lists: eight packages and 2,388 names each, exact match.
- Eight native test links: 31.085 seconds summed, all exit zero.
- Native `go build ./...`: 5.876 seconds, exit zero.
- Target-Windows `go vet ./...`: 3.071 seconds, exit zero.
- Full native fidelity gates before the matched execution diagnostic: 101.976
  seconds.

The native and cross dependency maps are retained package by package in the
compact summaries. The cross PE screen rejected any `libgcc`, `libstdc++`, or
`libwinpthread` DLL import, which makes a copied child PE self-contained with
respect to MinGW support libraries.

## Failed experiments and unexpected observations

1. The initial private VNet had no default outbound path. A tagged Standard NAT
   gateway restored bootstrap traffic without public IPs or inbound rules.
2. The Azure Ubuntu mirror remained unreachable after general HTTPS worked;
   bootstrap switched to `archive.ubuntu.com`.
3. The first package build failed because go-tree-sitter's YAML binding includes
   C++ and `x86_64-w64-mingw32-g++` was absent. Installing and pinning both GCC
   and G++ fixed it.
4. The first dynamic-runtime artifacts compiled in 50.922, 46.616, and 47.009
   seconds but were rejected. Child re-exec copies the PE alone and replaces
   `PATH`; adjacent GCC 13 runtime DLLs could be absent or silently replaced by
   the host's GCC 16 runtime. Static MinGW support was the accepted construction.
5. Coordinator `c157b32d` first failed three CLI shell-driven tests because
   `git/usr/bin/sh.exe` was missing from `PATH`. After that correction it passed,
   but its raw Go JSON predated explicit UTF-8 handling. The two rejected sem
   observations, 745.220 and 947.169 seconds, demonstrate why a single sem time
   was not accepted as a runtime comparison.
6. A three-repetition sequential cross diagnostic was bounded at 2,178 seconds
   without a final-only sentinel. CPU remained active (39.357% average over the
   last 11 complete minutes), consistent with a normal second repetition rather
   than a hang. Restarting the VM caused Azure Run Command to replay and reset
   the output before a changed helper hash stopped it. The accepted driver writes
   and immediately exports an atomic SHA-256 checkpoint after each candidate and
   enforces a 1,600-second process-tree watchdog.
7. Of 2,383 baseline roots, 1,165 had zero or absent reported duration. The
   partitioner assigned a deterministic 0.001-second fallback weight. Dynamic
   parity, not the timing model, remains the coverage authority.
8. A fresh pinned Linux/amd64 comparator ran after all Windows measurements with
   `CGO_ENABLED=1`, `go clean -testcache`, and exact
   `go test -json -timeout 30m ./...`. Ubuntu's Git 2.43.0 first produced a
   rejected 128.311-second run: seven test packages passed, while 20 top-level
   roots in `cmd/graph-bench` failed because `git checkout` treated
   `--end-of-options` as a path argument. After a noninteractive install of Git
   2.55.0, the fresh comparator passed in 111.518 seconds with 8,492 qualified
   events and 2,373 roots. The failed Git 2.43 run remains rejected evidence;
   the Git 2.55 result is the synthesis baseline and does not alter the A3
   Windows verdict.

## Evidence and prototype paths

Compact, sanitized evidence is under `results/windows-ci/a3/`:

- `cross-compile-summary.json`
- `transfer-summary.json`
- `weighted-execution-summary.json`
- `native-baseline-summary.json`
- `native-fidelity-summary.json`
- `bounded-sequential-diagnostic-summary.json`
- `linux-native-comparator-summary.json`
- `cost-summary.json`
- `infrastructure-summary.json`
- `cleanup-summary.json`

The independently implemented partitioner, worker, evidence comparator, and
regression tests are under `tools/ci-bench/a3/`. Raw ZIPs, PEs, complete logs,
and Azure responses were checksum-verified in the ignored local
`results/windows-ci/a3/260902x1-raw/` directory and were not staged.

After verification, the exact disposable group was deleted. The preflight
resolved 11 resources; a post-delete check found the group absent, zero
matching tagged resources, and zero matching tagged groups at
2026-09-02T17:53:03Z.

## Confidence

High for observed test coverage and the measured Azure VM timing: every
accepted candidate passed exact dynamic-multiset checks, all static inventories
matched, and the winner repeated three times with 3.568% variation. Medium for
production portability: only one Azure VM pair and one Windows image were used,
the experiment does not measure GitHub-hosted queue/cache behavior, the native
and cross C toolchains differ, and sharding repeats package initialization and
`TestMain`.
