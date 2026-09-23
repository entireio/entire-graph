# Contributing to Entire Graph

Thanks for taking the time. Issues and pull requests are both welcome, and
external contributions are a meaningful share of what lands here.

## Prerequisites

- **Go 1.27 or later.** `go.mod` declares `go 1.27`; older toolchains refuse
  the module.
- **cgo, with a working C toolchain.** Entire Graph compiles 13 tree-sitter
  grammars from C sources vendored under `internal/sem/`, so a build with
  `CGO_ENABLED=0` fails rather than degrading. Install `gcc` or `clang` on
  Linux, the Xcode command line tools on macOS, and a MinGW-w64 toolchain on
  Windows. If you see undefined references to `tree_sitter_*` symbols, cgo is
  off or no C compiler was found.
- **Git 2.36 or later** on `PATH`, which is also the runtime requirement.

```sh
git clone https://github.com/entireio/entire-graph.git
cd entire-graph
go build ./cmd/entire-graph
./entire-graph version
```

## Before you open a pull request

Run what CI runs:

```sh
gofmt -s -l .          # must print nothing
go vet ./...
go build ./...
go test ./...
sh scripts/gen-notices.sh --check
```

`go test ./...` exercises the full suite and can take several minutes on a cold
cache. CI additionally runs the POSIX shell suite
(`sh scripts/entire-graph-statusline_test.sh`) and a sharded Windows run.

## Changes that need more than code

- **A new vendored grammar or a new Go dependency** changes what is statically
  linked into the released binary, so it changes third-party attribution.
  Regenerate with `sh scripts/gen-notices.sh` and commit the updated `NOTICES`.
  The generator refuses to classify a license it does not recognize; if it
  stops on yours, add the classification deliberately rather than relaxing the
  check. Release archives are immutable once published, which is why this is a
  blocking CI check rather than a release-day step.
- **User-visible behavior** belongs in `CHANGELOG.md` under the unreleased
  section, grouped by Keep a Changelog heading. Tagged releases publish that
  section verbatim as their release notes.
- **Benchmark numbers** carry a documented provenance standard. Read
  `bench/memory/README.md` and `docs/benchmarks.md` before adding, changing, or
  quoting one, and never quote a number that has no registered run behind it.

## Pull request conventions

- Commit subjects follow Conventional Commits with a scope, matching existing
  history: `fix(sem): ...`, `feat(cli): ...`, `docs(readme): ...`,
  `chore(ci): ...`.
- Keep a pull request to one concern. Large mechanical changes are easier to
  review split from behavior changes.
- Describe how you verified the change, including the command you ran. A claim
  with no command behind it will be asked for one.
- Report security-relevant findings through
  [GitHub Issues](https://github.com/entireio/entire-graph/issues) if they are
  already public, and privately to the maintainers if they are not.

## Repository orientation

| Path | What lives there |
| --- | --- |
| `cmd/entire-graph/` | CLI entry point |
| `internal/cli/` | Command parsers, help text, and output formats |
| `internal/sem/` | Semantic analysis, parsers, caches, and vendored grammar sources |
| `internal/gitutil/` | Git object and worktree access |
| `docs/` | Public documentation and accepted ADRs |
| `bench/` | Benchmark harnesses, results, and their provenance records |
| `scripts/` | Release build, notices generation, and shell test suites |
