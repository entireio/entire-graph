# Production native test sharding (Windows and macOS)

This directory keeps its original name, but the harness in it now serves two
native targets with one code path: `windows/amd64` and `darwin/arm64`. Linux
still runs `go test ./...` as a single job.

For each target the `test` workflow compiles the configured CGO test binaries
once on a standard ephemeral runner, inventories every default-runnable root
(`Test`, `Example`, and `Fuzz`, but never `Benchmark`), and assigns those roots
to deterministic weighted shards. The compiled binaries and immutable plan are
downloaded by independent standard runners of the same target, one per shard.

## Targets

`targets.py` is the single definition of what differs between targets. Every
script takes `--target` (default `windows/amd64`, so the Windows invocations
are unchanged); the workflow pins it, and it must agree with the settings file,
the host platform, `go env`, and every artifact.

| | `windows/amd64` | `darwin/arm64` |
| --- | --- | --- |
| Settings | `settings.json` | `settings.darwin-arm64.json` |
| Weights | `historical-weights.json` | `historical-weights.darwin-arm64.json` |
| Shards | 16 | 12 |
| Test binaries | `*.test.exe` | `*.test` |
| Command budget | 30000 UTF-16 units | 262144 argv bytes |
| Budget record | `commandLineUtf16Units` | `commandLineArgvBytes` |
| Workflow jobs, artifacts | `windows-test-*` | `macos-test-*` |
| Aggregate required check | `test (windows-latest)` | `test (macos-latest)` |

A settings file names its target with a `target` key; `settings.json` predates
the key and its absence means `windows/amd64`. Inside a bundle the settings and
weights are always stored as `settings.json` and `historical-weights.json`.
The JSON schema identifiers keep their historical `entire-graph.windows-ci.*`
prefix for both targets; the target is carried by `targetEnvironment`.

GitHub-hosted macOS concurrency is capped per account, so on macOS the shards
are the only jobs that compete for runners once the plan exists: the native
vet and the harness unit tests are steps of the one short non-heavy job. When
changing the macOS shard count, compare each shard job's queued and started
times afterwards; a shard that queues costs more wall time than it saves. With
twelve shards, all thirteen macOS jobs started within two seconds of the plan,
so the account's cap is above thirteen; it has not been probed further.

## What bounds the wall time

Each sharded target ends at compile-and-plan, plus its slowest shard, plus the
aggregate job. Measured with twelve shards per target:

- Windows: each Depot Windows runner takes 80 to 175 s to start, and the
  critical path pays that twice (compile-and-plan, then the shards), which is
  more than a third of the target's time. Test steps take 100 to 140 s.
- macOS: runners start at once; a shard spends 50 to 60 s on checkout, the Go
  toolchain and the bundle before 70 to 130 s of tests. Hosted macOS runners
  differ in speed by up to forty percent from one job to the next, which is
  what is left of the spread between shards.
- compile-and-plan is fast only while its Go build cache holds compiled test
  packages. On macOS that job has a cache key of its own for that reason
  (22 s to compile instead of 117 s).

## Semantics

A target's settings file is its only tuning surface. The defaults preserve
`go test` semantics: shuffle is off, `-parallel` and `GOMAXPROCS` are unset,
and every shard has a 30-minute package timeout. Historical timings only seed
known roots. The inventory produced from the current compiled binary is
authoritative, so a newly added root is always included with the configured
default weight.

Direct test-binary launches preserve the environment and lifecycle details
that `cmd/go` supplies: `PWD` is the absolute package directory, the active
`GOTOOLDIR` is first on `PATH`, and `-test.paniconexit0` is unconditional.
Every shard clears only the Go test-result cache once before execution while
retaining setup-go's module and build caches. Before and after prepare, shard,
and non-heavy execution, the harness requires a clean tracked worktree and an
unchanged commit.

The target identity is fixed to the native target with `CGO_ENABLED=1` through
every inventory, plan, runner record, and verification boundary.
`inventory_testmain.go` parses only the target-selected `TestGoFiles` and
`XTestGoFiles`; settings pin each top-level `TestMain` declaration by filename
and the SHA-256 of its exact AST source range after LF normalization. That
makes lifecycle-hook drift fail closed while remaining stable across LF and
CRLF checkouts. On Windows the pinned declarations live in `*_windows_test.go`
files; Go selects none of those on darwin and no other test file of the heavy
packages declares `TestMain`, so the macOS pins are empty lists and a
`TestMain` that appears later fails the prepare job until it is pinned.

## Command-line budgets

A shard selects its roots with one `-test.run` expression and the harness
refuses to batch implicitly, so each launch is measured against the limit of
its target and fails closed above it.

On Windows the limit is `CreateProcess`'s 32767 UTF-16 units for the serialized
command line; the setting is 30000.

On macOS the constraint is `execve`'s argument area, `ARG_MAX` (1048576 bytes;
`getconf ARG_MAX`, `NCARGS` in `<sys/syslimits.h>`). That area holds the
NUL-terminated argv strings, the environment strings, and one pointer per
string, and Darwin has no separate per-argument limit. `commandLineLimit` is
therefore an argv budget in bytes: 262144, a quarter of `ARG_MAX`, which leaves
three quarters for the environment and pointers (a settings file may claim at
most half). Each launch additionally records `execArgumentAreaBytes` (argv plus
the runner's actual environment plus pointers) against
`execArgumentAreaLimit` (`sysconf(SC_ARG_MAX)` on that runner) and is refused
if it would not fit; the verifier requires both records.

## Artifact transport on macOS

`actions/upload-artifact` stores files without POSIX modes, so downloaded test
binaries are not executable. `run_shard.py` first matches each binary's SHA-256
to the plan and only then restores the owner execute bit, recording
`executableBitRestored`. The mode is not part of the verified identity; the
bytes are.

## Verification

The verifier runs inside each target's aggregate job (the required check
`test (<os>)`), whenever the plan exists, and the job's last step then requires
every component job to have succeeded. Running it there rather than in a job
of its own saves one runner start at the end of the critical path.

The verifier has no monolithic reference run. It instead fails closed unless
the plan is a disjoint, complete partition of the compiled inventories; every
planned root has exactly one top-level `run` and one successful terminal
event; each package process starts and terminates with the planned
multiplicity; every non-heavy target-selected package runs exactly once; all
binary hashes, repository/toolchain identities, metadata exits, and command
budgets agree. Exit fields are schema-mandated: prepare's top-level result and
every expected operation, each shard's cache clean and planned invocation, and
the non-heavy clean/test sequence must all be present and zero.

The workflow keeps native `go build ./...` coverage on both targets and adds a
native `go vet ./...` for each (a job on Windows, a step of the non-heavy job
on macOS), because the harness compiles and runs with `-vet=off` and the lint
job vets only the files Go selects on Linux. The harness unit tests run
natively in each target's non-heavy job, which the aggregate check requires,
rather than in compile-and-plan, which every shard waits for. It uses only
standard ephemeral hosted runners.

## Historical weights

Weights are per target and only balance shards; they never decide what runs.
A weight is a root's top-level elapsed time on the real CI runner, and the
planner balances estimated wall time rather than summed weights. A shard runs
one test process per heavy package, and `go test` runs a binary's serial roots
one at a time and then its `t.Parallel()` roots together, so the planner
estimates each process as

    serial elapsed + max(longest parallel root, parallel elapsed / parallelism)

and gives each root, slowest first, to the shard that would finish soonest
with it. The weights file marks the roots that call `t.Parallel()` and records
the `parallelism` that best explains the measured process times (about 2.5 on
the three-core hosted macOS runner and 2.75 on Windows). Planning by summed
elapsed instead made a shard of serial `internal/sem` roots look equal to one
of overlapping `internal/cli` sweeps, and it took half as long again. A weights
file without those fields plans every root as serial, which is plain
longest-processing-time-first.

Roots below half a second are not recorded and use `defaultWeightSeconds`,
which is set to their measured mean (0.05 s) so that the thousands of quick
roots do not outweigh the slow ones.

Refresh the weights from a green run whenever the shards drift apart, or after
adding or splitting a slow test:

```sh
H=tools/ci-bench/windows-production; E=$(mktemp -d); RUN=<run id>
gh run download "$RUN" -D "$E/macos" -p 'macos-test-shard-*'
gh run download "$RUN" -D "$E/windows" -p 'windows-test-shard-*'
python3 -B $H/refresh_weights.py --target darwin/arm64 --run "$RUN" \
  --evidence "$E/macos" --output $H/historical-weights.darwin-arm64.json
python3 -B $H/refresh_weights.py --target windows/amd64 --run "$RUN" \
  --evidence "$E/windows" --output $H/historical-weights.json
```

`refresh_weights.py` refuses evidence that is not one complete, successful run
of the named target (a missing shard, a nonzero exit, two commits, or a root
with more than one terminal event), so a partial download cannot skew a plan.

A shard cannot be shorter than its slowest root. When one root approaches half
of a shard's time, split it into several top-level tests that together make
the same assertions; the planner can then spread them. One slow runner in the
evidence run inflates the weights of the roots it ran, so prefer a run whose
shards finished close together.

## Running a target locally

On a machine of the target platform, at a clean committed checkout:

```sh
H=tools/ci-bench/windows-production; O=$(mktemp -d); SHA=$(git rev-parse HEAD)
python3 -B $H/prepare.py --target darwin/arm64 --repository "$PWD" --output "$O/bundle" \
  --settings $H/settings.darwin-arm64.json --weights $H/historical-weights.darwin-arm64.json \
  --expected-repository-sha "$SHA"
for i in $(seq 0 11); do
  python3 -B $H/run_shard.py --target darwin/arm64 --repository "$PWD" --bundle "$O/bundle" \
    --output "$O/shards/macos-test-shard-$i" --shard-index $i --expected-repository-sha "$SHA"
done
python3 -B $H/run_other.py --target darwin/arm64 --repository "$PWD" --bundle "$O/bundle" \
  --output "$O/other" --expected-repository-sha "$SHA"
python3 -B $H/verify_results.py --target darwin/arm64 --bundle "$O/bundle" \
  --shard-results "$O/shards" --other-results "$O/other" --output "$O/verification.json"
```
