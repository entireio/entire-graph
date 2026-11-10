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
| Shards | 8 | 4 |
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

macOS uses four shards rather than eight because GitHub-hosted macOS
concurrency is capped per account (as low as five concurrent jobs) and the
workflow runs other macOS jobs. Four shards plus the non-heavy job is five.

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
separate native `go vet ./...` job for each, because the harness compiles and
runs with `-vet=off` and the lint job vets only the files Go selects on Linux.
It uses only standard ephemeral hosted runners.

## Historical weights

Weights are per target and only balance shards; they never decide what runs.
The Windows file was seeded from a Windows monolithic baseline. The macOS file
was seeded from one sequential local run of the sharded flow on an Apple M1
(darwin/arm64); roots below two seconds use the configured default, as on
Windows. Hosted runners are slower, which scales every shard alike.

## Running a target locally

On a machine of the target platform, at a clean committed checkout:

```sh
H=tools/ci-bench/windows-production; O=$(mktemp -d); SHA=$(git rev-parse HEAD)
python3 -B $H/prepare.py --target darwin/arm64 --repository "$PWD" --output "$O/bundle" \
  --settings $H/settings.darwin-arm64.json --weights $H/historical-weights.darwin-arm64.json \
  --expected-repository-sha "$SHA"
for i in 0 1 2 3; do
  python3 -B $H/run_shard.py --target darwin/arm64 --repository "$PWD" --bundle "$O/bundle" \
    --output "$O/shards/macos-test-shard-$i" --shard-index $i --expected-repository-sha "$SHA"
done
python3 -B $H/run_other.py --target darwin/arm64 --repository "$PWD" --bundle "$O/bundle" \
  --output "$O/other" --expected-repository-sha "$SHA"
python3 -B $H/verify_results.py --target darwin/arm64 --bundle "$O/bundle" \
  --shard-results "$O/shards" --other-results "$O/other" --output "$O/verification.json"
```
