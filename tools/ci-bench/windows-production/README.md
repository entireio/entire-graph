# Production Windows test sharding

The `test` workflow compiles the configured CGO test binaries once on a
standard ephemeral Windows runner, inventories every default-runnable root
(`Test`, `Example`, and `Fuzz`, but never `Benchmark`), and assigns those roots
to eight deterministic weighted shards. The compiled binaries and immutable
plan are downloaded by eight independent standard Windows runners.

`settings.json` is the only tuning surface. Its defaults preserve `go test`
semantics: shuffle is off, `-parallel` and `GOMAXPROCS` are unset, and every
shard has a 30-minute package timeout. Historical timings only seed known
roots. The inventory produced from the current compiled binary is
authoritative, so a newly added root is always included with the configured
default weight.

Direct test-binary launches preserve the environment and lifecycle details
that `cmd/go` supplies: `PWD` is the absolute package directory, the active
`GOTOOLDIR` is first on `PATH`, and `-test.paniconexit0` is unconditional.
Every shard clears only the Go test-result cache once before execution while
retaining setup-go's module and build caches. Before and after prepare, shard,
and non-heavy execution, the harness requires a clean tracked worktree and an
unchanged commit.

The target identity is fixed to native `windows/amd64` with `CGO_ENABLED=1`
through every inventory, plan, runner record, and verification boundary.
`inventory_testmain.go` parses only the target-selected `TestGoFiles` and
`XTestGoFiles`; settings pin each top-level `TestMain` declaration by filename
and the SHA-256 of its exact AST source range after LF normalization. That
makes lifecycle-hook drift fail closed while remaining stable across LF and
CRLF checkouts.

The verifier has no monolithic reference run. It instead fails closed unless
the plan is a disjoint, complete partition of the compiled inventories; every
planned root has exactly one top-level `run` and one successful terminal
event; each package process starts and terminates with the planned
multiplicity; every non-heavy target-Windows package runs exactly once; all
binary hashes, repository/toolchain identities, metadata exits, and serialized
Windows command-line budgets agree. Exit fields are schema-mandated: prepare's
top-level result and every expected operation, each shard's cache clean and
planned invocation, and the non-heavy clean/test sequence must all be present
and zero.

The workflow keeps native Windows `go build ./...` coverage and adds a separate
Windows `go vet ./...` job. It uses only standard ephemeral hosted runners.
