# Result

## Verdict

**NO-GO.** A file-preserving overlay prototype can compile independent native
Windows `internal/sem` test binaries with an exact test inventory and the real
Windows `TestMain` in every process. However, the first real four-binary screen
exceeded the native 30-minute test boundary and did not publish a terminal
checkpoint. It therefore has neither an accepted critical path nor dynamic
run/pass/fail/skip parity with the accepted monolithic baseline.

## Best measured critical path

No sharded candidate was accepted. The b22 monolithic full-suite baseline
completed in **556.841 s** inside a **561.988 s** wrapper. The two-binary
prototype gate took **11.909 s**, but that number covers generation, three
compiles (two shards plus the monolithic comparison binary), list comparison,
and `TestMain` probes; it is not an end-to-end test result. The four-binary
screen ran beyond **30 minutes** before the external watchdog stopped the VM.

## Coverage equivalence

The compile/list gate is exact: the target-Windows inventory contained 154
`TestGoFiles`, zero `XTestGoFiles`, and 1,743 top-level runnable declarations.
The two shard lists and the monolithic list had the same 1,743-entry multiset
and SHA-256 signature, with no duplicates or omissions. Both shard probes
executed the package's byte-identical `TestMain`. Dynamic event equivalence is
**not proved** because the four-shard screen never exported its run/pass/fail/
skip streams or terminal process metadata.

## Cost

The official Azure Retail Prices response captured at
2026-09-02T17:44:51Z lists the regular, non-spot Windows
`Standard_D32ads_v7` rate in East US 2 as **$3.296/hour** effective
2025-11-01. At that rate, the accepted baseline wrapper cost **$0.515** in VM
time. The rejected four-shard execution consumed at least 30 minutes, or
**$1.648** in VM time; the interval from the baseline checkpoint through the
watchdog/deallocation grace was about 35 minutes, or about **$1.95**. The
maximum VM-only estimate for the complete resource-group window is **$5.87**.
These are retail calculations, not an invoice, and exclude the P10 disk, NAT,
IPv4, storage transaction, and egress meters.

## Main risk

Each binary creates a new package process. That repeats production `init`, the
real Windows `TestMain`, package globals, and fixture initialization. It can
also hide cross-test pollution that exists in the monolithic process. The
prototype deliberately accepts this process-lifetime change, and the failed
screen shows an additional operational risk: Azure Run Command did not return
the native timeout stack or a candidate checkpoint before control-plane
preemption.

## Recommendation

Do not adopt independent, file-preserving `internal/sem` test binaries for the
Windows CI path. Keep the generator as a reproducible negative prototype. If
the idea is revisited, first add a host-side watchdog and streaming checkpoint
per shard, then investigate a compile-once/run-many strategy or explicit
test-level grouping. Neither alternative should be accepted without the same
source, `TestMain`, exact-event, and process-exit gates.

## Experiment identity

| Field | Value |
| --- | --- |
| Product commit | `ee6468a6a49d9b2a1a828bd276792f415f392185` |
| Harness commit | `b22be6a73adac8d9c582af4bfc681c5d7a517221` |
| Target | `windows/amd64`, `CGO_ENABLED=1` |
| Go | `go1.26.7 windows/amd64` |
| C compiler | MinGW GCC 16.1.0, x86_64 posix/seh |
| VM | regular `Standard_D32ads_v7`, 32 logical CPUs, 128 GiB |
| CPU | AMD EPYC 9V45 |
| Image | Windows Server 2025 Datacenter Azure Edition, build 26100.33222 |
| Region | East US 2 |

The VM was private, had no public IP or inbound NSG rule, and used NAT-only
outbound access. A system-assigned managed identity read the private script
container and wrote the private results container. No SAS, storage key,
credential, or token is retained. All resources had mandatory purpose, agent,
run, expiry, and owner tags.

## Prototype design

The generator obtains the package selection from target-environment
`go list -json`; it never globs all `_test.go` files. A Go AST helper inventories
declarations and byte ranges. Test-bearing files are assigned by weighted LPT.
For each shard:

1. Owned test files are compiled from their original paths without an overlay
   replacement, so their test, benchmark, fuzz, and example bodies are
   byte-for-byte unchanged.
2. Every unowned selected file is replaced by a generated support surrogate.
   The surrogate retains exact non-runnable declarations required by the
   package and removes every top-level runnable declaration.
3. The Windows `TestMain` declaration is retained verbatim in a surrogate when
   its source file is not owned by that shard. The owner shard compiles the
   original file. Consequently every executable runs the real `TestMain` and
   no test in its source file is duplicated.
4. The manifest proves that every selected file and every runnable declaration
   has exactly one owner. It records the original file hashes, exact runnable
   declaration byte ranges and hashes, retained declaration hashes, generated
   surrogate hashes, and overlay hashes.

The source proofs at the pinned commit are:

| Proof | Value |
| --- | --- |
| 90 production Go/Cgo files | `1e9851b4824608c134050e82d76efd0ac123fd036e9dd43763c9d20be2158bc3` |
| 154 target-selected test files | `4733ad5ad784fce6e116d9881f54df07d579cd32096842a58bb826c0fae9d3f7` |
| 1,743 runnable declaration proof aggregate | `a86a9ab9cf255e8257cbf90dd4bfa0af8c271ebb86dcfd2b692cc164315af64b` |
| `TestMain` source file | `5862f6b72093440aeed863073de5528c6cf7bc4956b98a8232ec55924a87859d` |
| Exact `TestMain` declaration | `e62d6a507d71cf6ec1b253bf9cc6e5fb0e5e54177ef91262f23cb72a2a5287ed` |

The runnable aggregate is SHA-256 over sorted, newline-separated records of
name, source file, kind, byte start/end, and exact declaration SHA-256. The
complete per-declaration proof remains in the ignored raw manifest.

## Dependency and lifecycle audit

A naive full-file dependency closure is a negative result. Cross-file helper
references collapse the 154 selected files into 42 components; the largest
component contains 105 files and 1,480 runnable declarations. Including that
component in multiple binaries would duplicate tests, while assigning it once
would preserve nearly all of the monolithic critical path. The prototype uses
exact-declaration support surrogates instead.

In the two-shard gate, shard 1 owned 75 files/872 runnables and generated 79
surrogates retaining 194 declarations. Shard 2 owned 79 files/871 runnables and
generated 75 surrogates retaining 202 declarations. The `TestMain` source file
has 40 runnable declarations. Shard 2 owned it; shard 1's surrogate retained
`TestMain` and its helpers while omitting all 40 runnables.

The package has one production `init`, in `similarity.go`; it builds fixed
MinHash coefficients and repeats once per process. The Windows `TestMain`
performs a marker branch used by four fake-Git subprocess tests and otherwise
calls `m.Run`; it repeats once per shard. Static review found package fixture
maps/slices and the `-update` flag, but their uses in the selected tests are
read-only. Environment mutations use `t.Setenv`, including the one test that
later calls `os.Unsetenv`, so the testing package registers restoration. No
actual `os.Chdir` call was found. `parser_depth_test.go` re-executes tests from
its own file and remains self-contained. These checks reduce helper-omission
risk but do not make repeated process lifetime equivalent to the baseline.

## Commands and gates

The baseline command was:

```text
go test -json -timeout 30m ./...
```

Each native test binary was compiled from the package source tree with an
absolute overlay and output path:

```text
go test -vet=off -overlay <absolute-overlay.json> -c ./internal/sem -o <absolute-shard.test.exe>
```

Each candidate process was launched from the `internal/sem` source directory
with a conservative argv-length check and this exact command shape:

```text
go tool test2json -t -p github.com/entireio/entire-graph/internal/sem <absolute-shard.test.exe> -test.v=test2json -test.timeout=30m
```

Before execution, `<absolute-shard.test.exe> -test.list .` was compared with
the monolithic list. A separate marker probe ran each shard with
`ENTIRE_GRAPH_TEST_FAKE_GIT_MARKER` and `-test.list ^$`; both wrote `started`
and exited 23, exactly exercising the source `TestMain` marker branch.

The acceptance gate required every wrapper, compile, test, and metadata process
to exit zero; every shard to complete; every target-selected file and runnable
to have one owner; source hashes to remain stable; and the fully-qualified
dynamic run/pass/fail/skip event multiset to equal the baseline. The four-shard
screen did not reach that gate.

## Measurements

| Treatment | State | End-to-end | Compile/link | Execution / longest shard | Parity |
| --- | --- | ---: | ---: | ---: | --- |
| Accepted monolithic b22 baseline | completed | 561.988 s wrapper | included | 556.841 s suite | all recorded exits zero |
| 2-shard prototype gate | compile/list only | 11.909 s gate | combined in gate | not run | exact 1,743 list + `TestMain` probes |
| 4-shard screen | watchdog-terminated | unavailable | unavailable | >1,800 s lower bound | dynamic parity unavailable |
| 8-shard screen | not run | — | — | — | blocked by 4-shard failure |
| 12-shard screen | not run | — | — | — | blocked by 4-shard failure |

The external Azure CPU metric peaked near 36% during the initial parallel phase
and then remained near 3.16% for roughly 25 minutes. That low sustained CPU is
consistent with one stuck or externally waiting shard, but it is not sufficient
to identify the test. The terminal artifact, timeout stack, per-shard completion,
compile/link decomposition, and execution disk series were not exported. Disk
topology was captured (P10 Premium OS disk plus local NVMe), but candidate disk
utilization is therefore unavailable. No variance can be reported: the screen
never produced one credible candidate, so the planned three repetitions were
not run.

## Failed experiments and recovery

Bootstrap was fail-closed and corrected between attempts:

- Attempt 1 ran `go mod download` from the Run Command service working
  directory; explicit repository working directories fixed it.
- Attempt 2 exposed misleading PowerShell 5 `Start-Process` exit capture. A
  direct probe with MinGW on `PATH` proved the intended exit-23 marker behavior.
- Attempt 3 found a stale owned gate directory; bootstrap now removes only its
  exact gate result before regenerating.
- Attempt 4 passed compile/list/TestMain equivalence but hit PowerShell 5 scalar
  `.Count` behavior for an empty `git status`; array wrapping fixed it.
- Attempt 5 completed the full Windows gate. List capture was also made explicit
  UTF-8 so non-ASCII test names cannot be corrupted.

The accepted baseline checkpoint was published before the four-shard screen.
The screen crossed its native 30-minute boundary without a terminal checkpoint.
At the bounded recovery deadline the external watchdog deallocated the exact VM;
the original Run Command ended with `OperationPreempted`. One restart and one
reapply were attempted, but provisioning remained `Updating`. Recovery was then
stopped rather than expanding into disk forensics. The timeout stacks and raw
candidate result tree are unavailable.

## Operations, maintenance, and cleanup

Generating one binary per shard compiles the production package and its Cgo
dependencies repeatedly and maintains a declaration-aware source transformer.
New declaration forms, build tags, `TestMain` behavior, or cross-file helpers
would require ongoing generator and proof updates. Repeating process lifetime is
also a permanent semantic difference that reviewers must understand.

Raw logs, ZIPs, manifests, and binaries are permission-restricted below the
ignored `results/windows-ci/a4/live/` directory and are not allowlisted for Git.
The cloud artifacts were private and were destroyed with the resource group.
At 2026-09-02T18:51:56Z, Azure reported that the exact A4 resource group did not
exist and that both tagged group and tagged resource searches for `agent=a4`
were empty.

## Reproduction and confidence

The durable entry points are:

- `tools/ci-bench/generate-test-overlays.py`
- `tools/ci-bench/go-test-file-inventory.go`
- `tools/ci-bench/a4/bootstrap.ps1`
- `tools/ci-bench/a4/run-experiment.ps1`
- `tools/ci-bench/a4/verify-test-events.py`

Confidence is **high** that this exact file-preserving independent-binary design
should not ship: it failed its first bounded real screen and has no accepted
speedup or dynamic parity result. Confidence is **medium** about the mechanism
of the hang because the candidate's terminal stacks and process metadata were
not recoverable.
