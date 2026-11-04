# Kernel-scale performance pass and status line — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `entire graph` lookups and indexing affordable on kernel-scale repos without changing a byte of verb output, and add a graft-style Claude Code status line installed by `setup`.

**Architecture:**
- Lookups (`neighbors`, `def`, `impact`, `explain`) read a focused subgraph out of the existing sqlite sidecar instead of decoding the whole gz-JSON entry. The sidecar becomes default-on.
- Memory work on load, index and query is profile-driven and kept only when measured.
- The status line is a Go subcommand fed by `stats`, which gains a "gross" estimate next to its existing "net" modeled balance.

**Tech Stack:** Go 1.27, CGO tree-sitter (on Windows build with `CC="zig cc -target x86_64-windows-gnu"`), modernc sqlite, encoding/json/v2.

**Spec:** `docs/superpowers/specs/2026-11-03-kernel-scale-perf-and-statusline-design.md`

## Global Constraints

- Verb output (`neighbors`, `def`, `impact`, `explain`, `query`, in every format) is byte-identical before and after, except for latency and cache-telemetry fields.
- Schema `1.x` is frozen and additive-only. No egress. No daemon or watch mode.
- Any change to the gz entry format bumps `searchSnapshotCacheVersion`. Any change to the sidecar schema bumps the sidecar's schema version so old sidecars are rebuilt.
- Unparseable or unsupported files still surface as partial failures.
- Tests:
  - assert behavior at public boundaries (CLI output, files written, exit status), with hand-derived literal expectations;
  - never grep source text, assert private structure, or assert on mocks;
  - each test names the break it catches in its doc comment.
- Test env: `GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null` (as `mise.toml`). Git ≥ 2.36 must be first on PATH. On this machine that is VS2022's git: `export PATH="/c/Program Files/Microsoft Visual Studio/2022/Community/Common7/IDE/CommonExtensions/Microsoft/TeamFoundation/Team Explorer/Git/cmd:$PATH"`.
- Every perf change records baseline → result → verdict in `docs/perf-ledger.md`, and so does every reverted attempt.
- The status line never shows dollars.
- Inline `python -c` is mangled on this machine. Write `.py` files.

## Measurement harness (outside the repo)

`O:/_web/omnith/graph-eval/tools/perf_run.py <binary> <tag> <repo> [store]` runs the cold index, the no-op index, `neighbors`, `def`, `impact` and `query` on `runs/entire/{orbweaver,linux}`:
- records go to `results/perf.jsonl` (wall time, peak tree RSS);
- outputs go to `results/raw/perf-<tag>-<repo>-<step>.txt` for byte comparison;
- `PERF_STEPS=neighbors,def` limits the steps.

Baseline tags are `base` and `base-store`. Run kernel jobs one at a time, since each can take ~45 GiB.

---

### Task 1: Dev-only profiling hook

**Files:**
- Create: `cmd/entire-graph/pprof.go` (`//go:build pprof`)

**Interfaces:** Produces: when built with `-tags pprof` and `ENTIRE_GRAPH_PPROF=<dir>` is set, the process writes `<dir>/cpu-<pid>.pprof` for its lifetime and `<dir>/heap-<pid>.pprof` at exit. Release builds are unaffected.

- [ ] Step 1: Implement it with an `init()` that starts `pprof.StartCPUProfile`, plus an exit hook. `main.go` calls `os.Exit`, so add a tag-gated `var atExit func()` in `pprof.go` and a no-op stub in `pprof_off.go` (`//go:build !pprof`). `main` calls `atExit()` before `os.Exit`.
- [ ] Step 2: Check with `go build -tags pprof ./cmd/entire-graph`. Running `version` with `ENTIRE_GRAPH_PPROF` set writes both files, and `go tool pprof -top` reads them.
- [ ] Step 3: Commit: `build: add pprof-tagged profiling hook for local profiling`.

No test: it is tag-gated developer tooling with no production behavior.

### Task 2: Differential equivalence guard (lands before any lookup change)

**Files:**
- Create: `internal/cli/focused_equivalence_test.go`

**Interfaces:** Consumes the existing `runIndex`, `runNeighbors`, `runDef`, `runImpact`, `runExplain` and `Options`. Produces `TestLookupsAnswerIdenticallyFromSidecarAndFullLoad`.

The test pins one property: for any symbol name in a real repository, every lookup verb prints identical output whether it reads the sidecar or the gz entry. The break it catches is any focused-load subgraph that is missing a record the verb reads. The test is written once, never edited to make a change pass, and contains no expected literals, so it cannot drift from the code.

- [ ] Step 1: Write the test.
  - For each directory under `internal/sem/testdata/fixtures` with a semantic language (skip `formats-config`, `services-config`, `terraform-basic`), copy the tree into `t.TempDir()`, `git init`, and commit (use the `store_test.go` git helper pattern).
  - Index once with `ENTIRE_GRAPH_SEARCH_STORE=1` into a temp cache dir.
  - Collect symbol names by running `runIndex`'s sibling `symbols --format ndjson`. Decode with `encoding/json` and take the distinct `name` values, plus one name that does not exist (`zzNoSuchSymbol`) and one misspelled real name (first name with the last rune dropped), to cover the fuzzy path.
  - For each name and each variant, run once with `ENTIRE_GRAPH_SEARCH_STORE=1` and once with `=0`, both `--head --cache-dir <same>`. Variants:
    - `neighbors --direction in|out --depth 1|2 --format json`
    - `def --format json`
    - `impact --format json`
    - `explain --format json` (if explain takes a symbol)
  - Normalize both outputs by decoding the JSON into `map[string]any` and deleting top-level keys whose name ends in `_latency_ms` or starts with `index_cache_`, then compare with `reflect.DeepEqual`. On mismatch, report fixture, verb, args and a unified diff of the re-encoded JSON.
  - Gate it with `testing.Short()` so `go test -short` skips it.
- [ ] Step 2: Run `go test ./internal/cli -run TestLookupsAnswerIdenticallyFromSidecarAndFullLoad -v`. Expected: PASS today, because both paths currently fully materialize. That is the point: it is the regression guard for Tasks 3–5. Record the runtime. If it exceeds 60 s, sample at most 40 names per fixture with a fixed seed.
- [ ] Step 3: Mutation check. Temporarily make `loadSearchStore` drop one relation (`continue` on the first row) and confirm the test FAILS. Revert.
- [ ] Step 4: Commit: `test(cli): pin lookup output equality between sidecar and full load`.

### Task 3: Sidecar on by default

**Files:**
- Modify: `internal/sem/search_store.go:47` (`searchStoreEnabled`)
- Modify: docs that say "opt-in" (`AGENTS.md` store section, `docs/store-contract.md`, `README.md` if present)
- Test: `internal/cli/store_test.go` (add one test)

**Interfaces:** Produces: an unset `ENTIRE_GRAPH_SEARCH_STORE` means enabled. `0`, `false` and `off` (case-insensitive) disable it. Other values enable it.

- [ ] Step 1: Write the failing test `TestIndexWritesTheStoreWithoutOptIn`. With `t.Setenv("ENTIRE_GRAPH_SEARCH_STORE", "")` (unset semantics), index a one-file repo, then `runStore` must report `store_unavailable` false and a `path` that exists on disk. A second case with `"0"` must report `store_unavailable: true`. Break caught: default flipped back, or opt-out ignored.
- [ ] Step 2: Run it. Expected: FAIL on the first case.
- [ ] Step 3: Implement the parse in `searchStoreEnabled`.
- [ ] Step 4: Run the full `./internal/...` suite. Tests that set `"1"` explicitly stay valid. Fix any test that assumed default-off by setting `"0"` explicitly where the test is about the gz path; record each such edit in the commit body.
- [ ] Step 5: Commit: `feat(store): write and read the sqlite sidecar by default`.

### Task 4: Focused subgraph loader in `sem`

**Files:**
- Create: `internal/sem/search_store_focus.go`
- Modify: `internal/sem/search_store.go` (schema: `CREATE INDEX symbols_name ON symbols(name)`; bump the sidecar schema version constant or meta key that `loadSearchStoreForEntry` checks)
- Test: covered by Task 2's guard, plus one sem-level test below

**Interfaces:** Produces:
```go
// SnapshotFocus names the one symbol a lookup verb answers about.
type SnapshotFocus struct {
	Name  string // bare or qualified name as typed by the user (--symbol / positional)
	File  string // optional --file disambiguator, repo-relative or absolute as typed
	Depth int    // relation hops the verb walks from the focus (1 or 2)
}

// LoadFocusedProviderSnapshot returns a ProviderSnapshot containing everything a
// lookup verb reads when answering about focus, read from the sidecar. When the
// sidecar is absent or stale, or the name has no exact match (fuzzy path), it
// returns LoadOrBuildProviderSnapshot's full snapshot instead. Same return
// contract as LoadOrBuildProviderSnapshot.
func LoadFocusedProviderSnapshot(ctx context.Context, repo, providerVersion string,
	options ProviderSnapshotOptions, cacheDir string, disableCache bool,
	focus SnapshotFocus) (ProviderSnapshot, bool, error)
```

Focused content is a superset closure:
- **Header:** full header from sidecar meta (stats, completeness, warnings, all partial failures).
- **Files:** all file rows. Path lists feed `parseSymbolRef` and line readers, and on the kernel that is 65k small rows, which is cheap.
- **Symbols, starting set:** symbols whose `name` equals the focus's last segment.
- **Relations:** every relation with `from_id` or `to_id` in the current set, iterated `Depth` times, adding the other endpoint each time.
- **Containment:** symbols that are containers of the set (CONTAINS edges into it) and their CONTAINS children (siblings).
- **Endpoints:** every symbol and external referenced by a retained relation.
- **History:** the FILE_CHANGES_WITH refresh for the focus symbols' files, applied exactly as `loadCachedSearchSnapshot` applies it.

The sidecar's internal side maps (byte ranges, parameter names, signature types, bodyless, name lines, local IDs) are restored for the retained symbols only. Ordering of every slice matches the full snapshot's order (relations by the `Type+FromID+ToID` concatenation, symbols and files as the full snapshot sorts them), so order-sensitive verb logic sees the same sequence.

- [ ] Step 1: Write the failing sem test `TestFocusedSnapshotNeverLoadsUnrelatedSymbols`. Index the `multilang-relations` fixture with the sidecar, then call `LoadFocusedProviderSnapshot` for one leaf function. The returned `Symbols` must not contain a hand-picked symbol from an unrelated file that shares no relation path with it, which you name in the test after reading the fixture. It must contain the focus. Break caught: the loader silently returns the full snapshot, which is the whole point of the task.
- [ ] Step 2: Run it. Expected: FAIL (undefined function).
- [ ] Step 3: Implement it, reusing `loadSearchStore`'s row decoders: refactor the per-row decode into functions both paths call, so there is no second decoder. Use `searchStoreRelationsForIDs` (`search_store_context.go`) for the fan-out.
- [ ] Step 4: Run the sem test plus `go test ./internal/sem/...`. Expected: PASS.
- [ ] Step 5: Commit: `feat(sem): load a focused subgraph from the sidecar`.

### Task 5: Lookup verbs use the focused loader

**Files:**
- Modify: `internal/cli/neighbors.go:155`, `internal/cli/def.go:172`, `internal/cli/impact.go:183`, `internal/cli/explain.go:281` (each `LoadOrBuildProviderSnapshot` call becomes `LoadFocusedProviderSnapshot` with the verb's focus: neighbors uses `--symbol/--file/--depth`; def uses its positional name with depth 1; impact uses `--symbol/--file/--depth` (default 2); explain uses its symbol with depth 1)

- [ ] Step 1: Switch neighbors only and run Task 2's guard. Every mismatch is a missing record in the Task 4 closure: fix it in the loader, never in the verb or the test. Repeat until green.
- [ ] Step 2: Do the same for def, then impact, then explain, one at a time.
- [ ] Step 3: Run the full suite, `go vet ./...` and `gofmt -l .`. Expected: clean.
- [ ] Step 4: Measure: build to `graph-eval/tools/eg-w1.exe`, then `PERF_STEPS=index-cold,neighbors,def,impact python -I tools/perf_run.py <exe> w1 orbweaver` and the same for `linux`. Compare to `base-store` and `base`. Diff `raw/perf-w1-*-{neighbors,def,impact}.txt` against `raw/perf-base-*` (ignore latency lines). Ledger entry.
- [ ] Step 5: Commit: `perf(cli): answer lookups from a focused sidecar subgraph`, with before/after numbers in the body.

### Task 6: Profile-driven load and index memory (W2)

**Files:** determined by the profile. Ledger at `docs/perf-ledger.md`.

- [ ] Step 1: Profile with the Task 1 build on the kernel: `index --force` (cold) and `query`. Save `go tool pprof -top -sample_index=alloc_space` and `inuse_space` summaries to the ledger.
- [ ] Step 2: For each candidate, in order of profile weight, make the one change, rebuild, re-measure the same step twice on the kernel, and keep it only if it beats the variance of the two baseline runs. Candidates:
  - (a) allocation-free relation comparator: compare `Type`, then `FromID`, then `ToID` piecewise with prefix semantics equal to the concatenation, as the existing `spliceFileChangesWithRelations` comment requires;
  - (b) string interning on gz decode and during index build (`unique.Make` or a local map interner) for IDs, paths, reasons and types;
  - (c) hold symbols by index or pointer instead of by-value copies in `StreamSnapshot`/`forEachRelation` maps;
  - (d) a default `debug.SetMemoryLimit` sized from physical RAM unless `GOMEMLIMIT` is set.
- [ ] Step 3: After each kept change, run the full test suite and Task 2's guard, and confirm `raw/perf-*` lookup outputs are unchanged. A kept change that needs a test edited is a revert.
- [ ] Step 4: One commit per kept change: `perf(sem): <change> (<metric> before → after)`. Reverted attempts get a ledger row only.

### Task 7: `query --head` from the sidecar (W3)

**Files:** `internal/sem/search.go:1226` area, `internal/sem/search_cache.go:956` (`selectiveSearchSnapshotFromFull`)

- [ ] Step 1: Profile `query --head --profile full` on the kernel with the Task 1 build and record the ledger.
- [ ] Step 2: Add a guard test first, `TestQueryAnswersIdenticallyFromSidecarAndFullLoad`. Same shape as Task 2, over 5 fixed natural-language queries per fixture, `--format json`, normalized the same way. It must PASS before the change.
- [ ] Step 3: When the sidecar is present, build the selective view by reading only the preselected files' rows (symbols by `file_path`, which needs `CREATE INDEX symbols_file ON symbols(file_path)` landing with this reader, with the sidecar version bumped, plus relations from those symbols, plus the endpoints they name) instead of `selectiveSearchSnapshotFromFull`. Fall back to today's path otherwise.
- [ ] Step 4: Guard green, full suite green, kernel `query` re-measured, ledger entry. Keep or revert.
- [ ] Step 5: Commit: `perf(search): derive the selective view from the sidecar`.

### Task 8: `stats` gross estimate

**Files:**
- Modify: `internal/cli/stats.go` (`statsResponse`, `sessionAcc`, `finish`), `internal/cli/stats_displacement.go` (reuse the named-path extraction already used for follow-up classification)
- Test: `internal/cli/stats_gross_test.go`

**Interfaces:** Produces two additive JSON fields on `statsResponse`:
```go
// Sum over graph locate results of max(0, size of the distinct existing files the
// result names - the result's own bytes). Resolved against the repo root.
GrossCoveredBytes     int64 `json:"gross_covered_bytes"`
GrossCoveredEstTokens int64 `json:"gross_covered_est_tokens"` // bytes / 4
```

- [ ] Step 1: Write the failing tests.
  - `TestGrossCountsNamedFilesMinusResultBytes`: a temp repo with `a.go` of exactly 4,000 bytes and `b.go` of exactly 2,000 bytes. A transcript fixture (written in the test, mirroring the real Claude Code JSONL shape that `stats_test.go` fixtures use) has one Bash `entire graph query ...` tool_use. Its tool_result is a 600-byte text naming `a.go:10` and `b.go:3` (pad with spaces to exactly 600 bytes). Run `stats --repo <tmp> --transcript <file> --since all --format json`. Expect `gross_covered_bytes == 5400` and `gross_covered_est_tokens == 1350`.
  - `TestGrossIgnoresResultsNamingNoFile`: the same with a result naming no file. Expect `0`.
  - `TestGrossNeverGoesNegative`: a result naming one 100-byte file, with the result itself 500 bytes. Expect `0`.
  - `TestGrossCountsEachFileOncePerResult`: a result naming `a.go:1` and `a.go:50`. Expect 4000 − result bytes.

  Break caught by each: wrong sum, double-counting, a negative leak, counting unnamed results.
- [ ] Step 2: Run. Expected: FAIL (unknown field, zero).
- [ ] Step 3: Implement it in the accumulator. Stat each named path once per result relative to the repo root. Missing files contribute 0.
- [ ] Step 4: Run the stats tests and the full `./internal/cli`. Expected: PASS.
- [ ] Step 5: Commit: `feat(stats): report a gross covered-files estimate beside the modeled balance`.

### Task 9: `entire graph statusline` subcommand

**Files:**
- Create: `internal/cli/statusline.go`, `internal/cli/statusline_test.go`
- Modify: `internal/cli/root.go` (dispatch `statusline`; add it to meta verbs that `stats` excludes from its verb split)

**Interfaces:** Consumes the Task 8 fields and `sem.DescribeSearchStore`. Produces `runStatusline(ctx, opts Options, args []string) error`. It reads the Claude Code status JSON from `opts.Stdin` (fields `transcript_path`, `workspace.current_dir`, `cwd`, `session_id`, `context_window.used_percentage`) and writes ≤ 2 lines.

Render, with ANSI colours unless `NO_COLOR` is set (indigo `38;2;84;111;255` for the brand mark and figures, muted `38;5;244` for separators):
```
◤ entire graph · ✓ indexed · 23 lookups · ~184K tok gross · ~12.4K net
▸ ctx 42%
```
- `lookups`: graph locate calls in this transcript.
- `gross`: `gross_covered_est_tokens`.
- `net`: `modeled_balance_est_tokens`, which may be negative (`−7.3K net`). It reads `net n/a` when `sessions_with_savings_comparison == 0`.
- With no lookups, line 1 drops the three figures.
- `not indexed · run entire graph index` replaces `✓ indexed` when the store descriptor is unavailable.
- Line 2 is omitted when `context_window` is absent.
- Width: segments are dropped from the right until the visible width is ≤ `COLUMNS` (default 120).
- Any error prints nothing and returns nil.
- Results are cached in `os.UserCacheDir()/entire-graph/statusline/<sha256(transcript_path)>.json`, keyed by transcript size + mtime.

- [ ] Step 1: Write the failing tests. They run real `runStatusline` with real stdin bytes and a transcript and repo built in the test, plus `NO_COLOR=1`, and use literal expected lines.
  - `TestStatuslineShowsLookupsGrossNetAndContext`: a transcript with 1 graph query result naming the 4,000-byte `a.go` (result 400 bytes → gross 900 tok) and the exploration calls needed for a known net value, hand-computed from `savingsBytes`'s documented formula in a comment. Stdin is `{"transcript_path":…,"cwd":…,"context_window":{"used_percentage":41.6}}`. Expect line 2 `▸ ctx 42%` and line 1 containing the exact segments `1 lookups`, `~900 tok gross` and the hand-computed net. Assert segment presence and order, not the full line, so colours and separators can change.
  - `TestStatuslinePrintsNothingOnBadInput`: empty stdin, `{`, and a transcript path that does not exist each give empty stdout and a nil error.
  - `TestStatuslineDropsWholeSegmentsToFit`: `COLUMNS=40`. The output's visible width is ≤ 40 and every segment present is complete (no cut words).
  - `TestStatuslineReportsMissingIndex`: a fresh repo with no index gives line 1 containing `not indexed`.
- [ ] Step 2: Run. Expected: FAIL (unknown verb).
- [ ] Step 3: Implement it, reusing the stats collector in-process (no subprocess) and `DescribeSearchStore` for the indexed state. Format numbers as `~N` below 1,000, then `~N.NK` and `~N.NM`. Negatives use U+2212.
- [ ] Step 4: Run it plus the full `./internal/cli`. Expected: PASS.
- [ ] Step 5: Commit: `feat(cli): add a Claude Code status line subcommand`.

### Task 10: `setup` installs the status line

**Files:**
- Modify: `internal/agentsetup/claude_hook.go` (reuse its settings read/merge/write), `internal/cli/agents.go:60-110` (flag `--no-statusline`, preflight, UI row)
- Test: `internal/agentsetup/claude_statusline_test.go`

**Interfaces:** Produces:
```go
func CheckClaudeStatusLine(root string) error
// InstallClaudeStatusLineChanged merges statusLine into <root>/.claude/settings.local.json.
// foreign=true (and nothing written) when a statusLine it did not write is present.
func InstallClaudeStatusLineChanged(root string) (path string, created, changed, foreign bool, err error)
```
The command written is `entire graph statusline`, wrapped the same way the SubagentStart hook command is on Windows and POSIX (fail-open). "Ours" means the command string contains `entire graph statusline`.

- [ ] Step 1: Write the failing tests, all against real files in a temp dir.
  - `TestStatusLineInstallIsIdempotent`: install twice. First call `changed=true`; second `changed=false`; file bytes are equal after both.
  - `TestStatusLineInstallKeepsForeignStatusLine`: pre-write settings.local.json with `{"statusLine":{"type":"command","command":"node graft-statusline.cjs"}}`. Install returns `foreign=true, changed=false`, and the file is byte-identical.
  - `TestStatusLineInstallPreservesOtherSettings`: pre-existing `hooks.SubagentStart` and an unrelated key survive the install (decode and compare those subtrees).
  - `TestSetupNoStatuslineFlagWritesNone`: `runSetup --repo <tmp> --no-statusline` leaves no `statusLine` key.
- [ ] Step 2: Run. Expected: FAIL.
- [ ] Step 3: Implement the install and a UI row with the outcomes: `wrote`, `unchanged`, `kept your existing status line (… ) — pass --no-statusline to silence`, or `skipped (--no-statusline)`.
- [ ] Step 4: Run the full suite and `go vet`. Update the README/AGENTS.md `setup` descriptions and the statusline-script header to point at the subcommand.
- [ ] Step 5: Commit: `feat(setup): install the Claude Code status line`.

### Task 11: Re-run the eval and report (W5)

- [ ] Step 1: Build the final binary to `graph-eval/tools/eg-final.exe`. Run `perf_run.py` for `final` on both repos, with the sidecar default (no `store` arg needed after Task 3).
- [ ] Step 2: Re-run the eval scenarios (`tools/run_queries.py`-style) with the final binary for the entire column and score them with `tools/score.py`. Accuracy must equal the baseline's: outputs differ only in telemetry.
- [ ] Step 3: Write `docs/perf-ledger.md`'s summary table (baseline vs final per step: wall, RSS, disk) and update the comparison doc.
- [ ] Step 4: Commit: `docs: perf ledger and kernel-scale results`.
