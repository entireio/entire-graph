# Bounded retry plan (not executed)

The checked `mise.toml` defines `check` with four parallel dependencies:
`fmt`, `vet`, `test:ci`, `test:statusline` (which depends on `build`), and
`build`; `test:ci` itself runs `go test -race -timeout 30m ./...`.

Smallest resource-safe retry: use a fresh clean detached source worktree and
run the exact `mise run check` with `MISE_JOBS=1` to serialize the task graph.
If package compiler pressure remains, add `GOFLAGS=-p=1` and
`GOMAXPROCS=2` while retaining the same task command. Record these environment
values as part of the verification identity. Do not change source, skip a task,
or interpret a resource-limited run as a semantic result.
