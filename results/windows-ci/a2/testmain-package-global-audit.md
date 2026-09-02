# Windows TestMain and package-global audit

Scope: the three compiled heavy packages at
`ee6468a6a49d9b2a1a828bd276792f415f392185`.

## TestMain

- `internal/sem/provider_gitdir_unc_windows_test.go` defines the Windows-only
  `TestMain`. In a normal process it only calls `m.Run`. When
  `ENTIRE_GRAPH_TEST_FAKE_GIT_MARKER` is present, a deliberately re-executed
  copy writes a marker and exits 23. Absolute test-binary invocation preserves
  that child-process behavior.
- `internal/cli/git_metadata_guard_windows_test.go` has the equivalent
  `ENTIRE_GRAPH_CLI_TEST_FAKE_GIT_MARKER` path and otherwise only calls
  `m.Run`.
- `internal/gitutil` has no `TestMain`, but several tests deliberately execute
  `os.Args[0]`; an absolute binary path and package-source working directory
  preserve those re-executions.

Dynamic listing itself starts each heavy binary once, so it runs package init
and `TestMain` once per inventory. Execution then starts one process for each
package represented in each shard. There is no implicit command-line batching:
the runner fails closed above a conservative 30,000-character serialized
command-line threshold, below Win32's 32,767-character absolute limit. Therefore init
and `TestMain` repetition is deliberate and visible, not hidden by batching.

## Package globals and process state

The package-scope test declarations are static fixtures, compiled regular
expressions, reflection values, and one `flag.Bool("update", false, ...)` for
golden-file maintenance. The audit found no assignments that mutate the named
fixture maps/slices after initialization. The `-update` flag remains false in
every shard because only prefixed `-test.*` arguments are passed.

Several CLI tests temporarily call `os.Chdir`, and many tests use `t.Setenv`.
The `os.Chdir` tests do not call `t.Parallel` and register cleanup; `t.Setenv`
also restores state. Sharding isolates top-level tests into additional
processes, which cannot create new cross-test contamination but can hide an
accidental dependency on state left by a different top-level test. The
baseline-vs-shard dynamic event multiset, three-repeat stability check, and
retained failure artifacts are the experiment's guards for that residual risk.

No test body, assertion, fixture, build tag, timeout, or Windows filesystem,
Git, process, path, or API behavior is changed by the prototype.
