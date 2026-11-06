# Kernel-scale performance pass and status line — Implementation Plan

> **Status (2026-11-06):** Tasks 1–5 done. Tasks 6–7 (W2, W3) are superseded by the [store-native index design](../specs/2026-11-04-store-native-index-design.md). Tasks 8–10 and 9b–9g done (9g's #4 reverted as neutral). Task 9h (from the third `entire review`) proceeds next, independent of Task 11. PR split: perf (Tasks 1–5, 8b fixes, 9d, 9g, 9h #2), status line (8, 9, 9b, 9c, 9e, 10, 9h #4 and #6), and Windows subprocess consoles (9f, its own PR). Task 11 runs after the redesign's M3.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `entire graph` lookups and indexing affordable on kernel-scale repos without changing a byte of verb output, and add a graft-style Claude Code status line that `setup` installs when the user opts in (Task 9c).

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

**Interfaces:** Produces: when built with `-tags pprof` and `ENTIRE_GRAPH_PPROF=<dir>` is set, the process writes `<dir>/cpu-<pid>.pprof` for its lifetime, `<dir>/heap-peak-<pid>.pprof` near peak live heap, and `<dir>/heap-exit-<pid>.pprof` at exit. Release builds are unaffected.

- [ ] Step 1: Implement it.
  - An `init()` starts `pprof.StartCPUProfile`.
  - `main.go:51` is the only `os.Exit`, and signals return through it as `SignalError`. Add a tag-gated `var atExit func()` in `pprof.go` and a no-op stub in `pprof_off.go` (`//go:build !pprof`). `main` calls `atExit()` before `os.Exit`.
  - **Peak heap, not exit heap.** A heap profile taken at exit shows memory after the work is done, so `inuse_space` would mislead. Instead, a goroutine samples `runtime/metrics` `/memory/classes/heap/objects:bytes` every 250 ms. Whenever that exceeds the previous maximum by more than 10%, it rewrites `<dir>/heap-peak-<pid>.pprof`. `atExit` also writes `heap-exit-<pid>.pprof`, which is still useful for `alloc_space`.
  - Panics and OOM kills lose the CPU profile. That's acceptable for a dev tool; note it in the file comment.
- [ ] Step 2: Check with `go build -tags pprof ./cmd/entire-graph`. Running `version` with `ENTIRE_GRAPH_PPROF` set writes the files, and `go tool pprof -top` reads them.
- [ ] Step 3: Commit: `build: add pprof-tagged profiling hook for local profiling`.

No test: it is tag-gated developer tooling with no production behavior.

### Task 2: Differential equivalence guard (lands before any lookup change)

**Files:**
- Create:
  - `internal/cli/focused_equivalence_test.go`
  - `internal/sem/testdata/fixtures/focus-edge-cases/` (guard-only fixture, described below)
  - `internal/sem/testdata/fixtures/c-kernelish/` (C: a struct of function pointers assigned in a designated initializer, a static function called through a macro, a header and a `.c` file)

**Interfaces:** Consumes the existing `Run`/`Options` CLI entry. Produces `TestLookupsAnswerIdenticallyFromSidecarAndFullLoad`.

**What it pins:** for any way of naming a symbol, every lookup verb prints identical output whether it reads the sidecar or the gz entry.
- **Break it catches:** any focused subgraph missing a record, or a header count, that the verb reads.
- **No expected literals:** it is written once and never edited to make a change pass, so it cannot drift from the code.

**The guard-only `focus-edge-cases` fixture** is built by the test as a temp git repo. It includes:
- (a) A Go type `Config` and a function `config` in different files. These are case-colliding names.
- (b) A Swift or Kotlin type plus an extension adding a member in another file. This gives `ContainerID` without a local CONTAINS edge (see `def.go:787-790`).
- (c) A type with a supertype whose members def lists as inherited.
- (d) A method that OVERRIDES another.
- (e) **Co-change history.**
  - Three commits that change `a.go` and `b.go` together, so `FileCochanges`' ≥ 2 threshold is met.
  - After indexing, the test makes a commit that edits `a.go` and `c.go`, then a commit reverting it. The tree is unchanged, so the index is a cache hit, but history now differs. Lookups run after that.

**Name forms per symbol (sampled):**
- the plain name;
- the name upper-cased (case-fold path);
- the qualified name;
- the stable ID;
- `--file <its file>` and `--file wrong.go`;
- `--kind <its kind>` and `--kind class` when the symbol is not a class;
- `--line <its line>` and `--line 1`;
- the positional `path:line` form;
- for def, `A::b`/`A->b`/`A#b` rewrites of a qualified name, and a three-name `def A B C` call;
- `zzNoSuchSymbol`;
- a misspelling (the last rune dropped).

**Verbs × formats:**
- `neighbors --direction in|out --depth 1|2`, plus `--relation USES_TYPE` once;
- `def`;
- `impact --depth 1|2`.

Each runs in `--format json` AND `--format agent` (and `text` where the verb has it), with `--head --cache-dir <same>`, once under `ENTIRE_GRAPH_SEARCH_STORE=1` and once under `=0`.
- **Explain** reads compiler output on stdin, not a symbol flag. Add it only if the implementer can synthesize stdin that explain resolves (for example a gcc-style `file:line: error: … 'Name'` line built from each sampled symbol). Otherwise explain stays on the full load in Task 5, and the plan's ledger says so.

**Normalization:**
- **JSON:** decode it, then delete, recursively at any depth, keys ending `_latency_ms` or starting `index_cache_`. Compare with `reflect.DeepEqual`.
- **Text/agent:** compare bytes after removing only the telemetry lines the verb emits. Find the exact patterns by reading the renderer, and list them in the test's doc comment.
- **On mismatch:** report fixture, verb, args, and a unified diff.

**Runtime:** runs go in-process through `Run`, not subprocesses.
- **Default:** every fixture, at most 6 sampled symbols per fixture (fixed seed), all name forms. Target under 60 s, so `mise run test` stays green without `-short`.
- **`ENTIRE_GRAPH_EQUIV_FULL=1`:** every symbol. Run this at every Task 5 step.
- `-short` skips the guard.

- [ ] Step 1: Write the test and both fixtures.
- [ ] Step 2: Run `go test ./internal/cli -run TestLookupsAnswerIdenticallyFromSidecarAndFullLoad -v`, then again with `ENTIRE_GRAPH_EQUIV_FULL=1`. Expected: PASS, because both paths fully materialize today. Record both runtimes in the ledger.
- [ ] Step 3: Mutation check.
  - Temporarily make `loadSearchStore` skip one CALLS row into a sampled symbol while leaving the header untouched.
  - Confirm the test FAILS, and that the diff shows a missing caller record (not only a count).
  - Revert.
- [ ] Step 4: Commit: `test(cli): pin lookup output equality between sidecar and full load`.

### Task 3: Sidecar on by default

**Files:**
- Modify: `internal/sem/search_store.go` (`searchStoreEnabled` at :47, `repairSearchStoreSidecarForEntry` at :~715)
- Modify: `internal/sem/search_cache.go` (how a repair error on a cache hit is surfaced, :~692)
- Modify: docs that say "opt-in" (`AGENTS.md` store section, the `store_unavailable` hint, `docs/store-contract.md`, `README.md` if present)
- Test: `internal/cli/store_test.go` (add one test)

**Interfaces:** Produces:
- An *unset* `ENTIRE_GRAPH_SEARCH_STORE` means enabled. A set value keeps today's parse exactly: `""`, `0`, `false`, `off` and `no` disable it, and anything else enables it.
- A sidecar repair failure on a cache hit no longer fails `index`. It becomes a header warning (a new warning code, additive), and lookups fall back to the full load.
  - Users who never opted in must not see `index` start failing, for example on a full disk.
  - The failure is still surfaced, never silent.

**Fix the doubled no-op cost first.** On a cache hit, preindex already loads the store fully (`search_cache.go:661 → 1426 → 1679`). Then `repairSearchStoreSidecarForEntry` calls `loadSearchStoreForEntry`, a second full decode, just to learn whether the store exists.
- Replace that call with the meta-only exactness check `openExactSearchStore` uses: version, entry digest and tree (`search_store_context.go:615-656`).
- The digest it needs is already computed once per run; reuse it rather than hashing again.

- [ ] Step 1: Write the failing test `TestIndexWritesTheStoreWithoutOptIn`.
  - Case 1: with the variable unset (`os.Unsetenv` under `t.Setenv` restore), index a one-file repo. `store` must then report `store_unavailable` false and a `path` that exists on disk.
  - Case 2: with `"0"` and a **separate** cache dir, it must report `store_unavailable: true`.
  - Break caught: the default flipped back, or the opt-out ignored.
- [ ] Step 2: Run it. Expected: FAIL on the first case.
- [ ] Step 3: Implement the unset-means-on parse, the meta-only repair check, and the warning on repair failure.
- [ ] Step 4: Run the full `./internal/...` suite.
  - Tests that set `"1"` explicitly stay valid.
  - Fix any test that assumed default-off by setting `"0"` explicitly where the test is about the gz path. Record each such edit in the commit body.
- [ ] Step 5: **Keep/revert gate.** Build `eg-t3.exe` and run `perf_run.py` for `index-cold` and `index-noop` on both repos with no `store` argument (the default now applies).
  - Record the results against `base` and `base-store`.
  - Kernel no-op index must come in within noise of `base` (~51 s), not `base-store` (104 s). If it doesn't, find out why before continuing.
  - Cold index pays the sidecar build (~+26% on the kernel). Record that as the accepted cost of the lookup win, which is measured in Task 5.
  - Also record orbweaver cold `def` latency: a cold lookup now writes a sidecar too.
- [ ] Step 6: Close two gaps.
  - **Lookup cache misses.** Today a miss on the lookup path writes the gz entry with no sidecar (`search_cache.go:~538`), unlike the query path (`:~478`). Make it write the sidecar the same way, so the first `--head` lookup on a fresh repo leaves the next one fast.
    - Extend `TestIndexWritesTheStoreWithoutOptIn` with a case that runs `def --head` on a fresh cache, then `store`, and expects the store to be available.
  - **Document** that after a sidecar schema bump, lookups fall back to the full load (correct, slower) until `index` or the next cache-miss write rebuilds the sidecar.
- [ ] Step 7: Commit: `feat(store): write and read the sqlite sidecar by default`, with the measurements in the body.

### Task 4: Focused subgraph loader in `sem`

**Files:**
- Create: `internal/sem/search_store_focus.go`, `docs/perf/focus-read-sets.md`
- Modify: `internal/sem/search_store.go`, `internal/sem/search_cache.go` (extract the post-load chain, see below)
- Test: covered by Task 2's guard, plus one sem-level test below

**Step 0 (before any code): write the read-set table.** For each of neighbors, def, impact (and explain if it's guarded), trace the verb from its `LoadOrBuildProviderSnapshot` call to output. Record every snapshot field and every whole-slice scan it does, in `docs/perf/focus-read-sets.md`. The reviewer found these, so the table must cover at least:
- **Name resolution:**
  - `resolveFocusSymbols` (`symbolref.go:197`) matches an exact ID, then `EqualFold` on Name OR QualifiedName, then filters by `--file`/`--kind`/`--line`;
  - `def` rewrites forms (`def.go:684-692`);
  - `def A B C` takes several names;
  - positional `path:line`;
  - the fuzzy ladder ranks over *every* symbol (`symbolref.go:606-676`).
- **Containment by `ContainerID` scan, not CONTAINS:** impact siblings/members (`impact.go:609-620`), impact's container (457-461), def members (`def.go:827-831`), def/explain owner (`def.go:1038`, `explain.go:528,575`), and extension members (`def.go:787-790,1050-1052`).
- **def's second hop:** inherited members via supertypes' members (`def.go:1078-1086`), and `impl:` via OVERRIDES, then the target's `ContainerID` (815-822).
- **Relation fields read:** `Evidence` (call-site spans), `Resolution` (doc-mention label, `impact.go:357-365`), `Reason` (co-change detail, 538).
- **Header:** `Stats` printed by neighbors (`:405`) and impact (`:432`), and completeness.
- **Per-hop walk:** which relation types and directions each verb walks at hop 1 and hop 2. Impact's hop 2 is incoming call-family only.

Task 4's reviewer checks the closure below against this table.

**Interfaces:** Produces:
```go
// SnapshotFocus describes what one lookup verb will read. Built by the verb.
type SnapshotFocus struct {
	Names []string    // every candidate spelling the verb may resolve: as typed, plus def's rewritten forms
	Hops  []FocusHop  // hop i expands the frontier through these relation types and directions
	Kinship bool      // also load containers (by container_id), their container_id children,
	                  // and the members of supertypes reached through EXTENDS/IMPLEMENTS/OVERRIDES
}
type FocusHop struct {
	Types   []string // nil means every type
	In, Out bool
}

// LoadFocusedProviderSnapshot returns (snapshot, focused, cacheHit, err).
// focused=false means the full snapshot was returned (no exact sidecar, a
// positional path:line ref, or any error reading the sidecar), with the same
// contract as LoadOrBuildProviderSnapshot.
func LoadFocusedProviderSnapshot(ctx context.Context, repo, providerVersion string,
	options ProviderSnapshotOptions, cacheDir string, disableCache bool,
	focus SnapshotFocus) (ProviderSnapshot, bool, bool, error)
```

**Fallback contract.** The verb resolves exactly as it does today, against the snapshot it gets back.
- If the snapshot was focused and the verb's *exact* resolution (ID / EqualFold name / qualified name, then filters) comes back empty, the verb discards it and calls `LoadOrBuildProviderSnapshot` before running the fuzzy ladder. The ladder ranks over every symbol, so it always sees the full set.
- This is sound because the starting set below is a superset of every symbol exact resolution could return. Filters only ever narrow.

**Sidecar schema additions.** Bump the sidecar version so old sidecars are rebuilt.
- New symbol columns, each with an index:
  - `name_fold` and `qname_fold`, each folded in Go. Map every rune to the smallest rune in its `unicode.SimpleFold` orbit, so equality matches `strings.EqualFold` exactly. Don't use sqlite `NOCASE`, which is ASCII-only.
  - `container_id`.
- An index on `relations(type)` only if the Step 6 measurement needs it.

**Starting set:** symbols where `id IN names` OR `name_fold IN fold(names)` OR `qname_fold IN fold(names)`.

**Closure:**
- **Header:** the full header from sidecar meta (warnings, partial failures, completeness).
- **Files:** all file rows. They feed `parseSymbolRef` and path readers; 65k small rows on the kernel.
- **Relations:**
  - For each `FocusHop`, take every relation with the frontier on the given side and of the given types. This is uncapped and fetches full rows (Evidence, Resolution, Reason, Confidence, …) through the same row decoder `loadSearchStore` uses: refactor the decoder so both paths call it.
  - Do NOT use `searchStoreRelationsForIDs`. It caps at `limit`, orders by confidence, and returns only 4 fields.
  - Endpoints of each retained relation join the next frontier only if they are symbols. Files and externals are loaded, but never expanded.
- **Kinship:** as described in `SnapshotFocus`, through `container_id`.
- **Endpoints:** every symbol and external referenced by a retained relation.
- **Order:** every slice is the full snapshot's order restricted to the retained rows: read `ORDER BY rowid` (the sidecar is written in snapshot order). Do not re-sort. Step 1's test verifies the rowid assumption.
- **Side maps:** the sidecar's internal side maps (byte ranges, parameter names, signature types, bodyless, name lines, local IDs) are restored for retained symbols only.

**Post-load chain.** Reproduce it once, not twice. Extract the steps `loadCachedSearchSnapshot` applies after decode (`search_cache.go:~1419-1480` and `:1597`) into one function that both the full and the focused paths call:
- validity check (`validCachedSearchSnapshot`);
- scope check;
- commit restamp;
- `local/` repo-key rebind (`rebindCachedSearchSnapshot`);
- the FILE_CHANGES_WITH refresh (drop stored FILE_CHANGES_WITH, splice fresh history);
- warning retirement;
- a history read failure treated as a miss.

That function takes the *total* relation count and per-type completeness counts as inputs, rather than `len(relations)`.
- Full path: `len(all)`.
- Focused path: `stored_total − stored_FILE_CHANGES_WITH + len(history)`, counted in SQL from the sidecar.

This keeps `Stats.Relations` and `Completeness.Relations` identical.

- [ ] Step 1: Write the failing sem tests.
  - `TestFocusedSnapshotNeverLoadsUnrelatedSymbols`:
    - Index the `multilang-relations` fixture with the sidecar.
    - Call `LoadFocusedProviderSnapshot` for one leaf function with a one-hop CALLS focus.
    - Expect `focused == true` and the focus present.
    - Expect a hand-picked symbol from an unrelated file absent. Name it in the test after reading the fixture.
    - Break caught: the loader silently returns everything.
  - `TestFocusedSnapshotIsAnOrderedSubsetOfTheFull`:
    - For the same focus, compare the focused `Symbols`, `Relations` and `Files` against `LoadOrBuildProviderSnapshot`'s.
    - Each focused slice must be a subsequence of the full slice.
    - Header `Stats` and `Completeness` must be equal.
    - Break caught: a re-sort, a wrong rowid assumption, or header counts taken from the subset. This is a property, not a literal.
- [ ] Step 2: Run them. Expected: FAIL (undefined function).
- [ ] Step 3: Implement it.
- [ ] Step 4: Run the sem tests plus `go test ./internal/sem/...`. Expected: PASS.
- [ ] Step 5: Commit: `feat(sem): load a focused subgraph from the sidecar`.

### Task 5: Lookup verbs use the focused loader

**Files:**
- Modify:
  - `internal/cli/neighbors.go:155`, `internal/cli/def.go:172`, `internal/cli/impact.go:183`;
  - `internal/cli/explain.go:281`, only if Task 2 guards explain.
  - Each `LoadOrBuildProviderSnapshot` call becomes `LoadFocusedProviderSnapshot`, with the `SnapshotFocus` the read-set table prescribes for that verb.
  - Add the fallback: exact resolution empty on a focused snapshot → full load, then today's code path, including fuzzy.

- [ ] Step 1: Switch neighbors only and run Task 2's guard (`ENTIRE_GRAPH_EQUIV_FULL=1`).
  - Every mismatch is a missing record in the closure. Fix it in the loader or in the verb's `SnapshotFocus` (and update the read-set table), never in the test. Repeat until green.
- [ ] Step 2: Do the same for def, then impact (then explain if guarded), one at a time.
- [ ] Step 2b: **Closure size check on kernel hubs.** With the Task 1 build, run `impact --symbol` and `neighbors --depth 2` on `kmalloc`, `file_operations` and `spin_lock`. Record wall time, RSS and retained relation count in the ledger.
  - If any exceeds the target (single-digit seconds, < 1 GiB), narrow that verb's `Hops` to what the read-set table says it walks, rather than raising the target.
- [ ] Step 3: Run the full suite, `go vet ./...` and `gofmt -l .`. Expected: clean.
- [ ] Step 4: Measure: build to `graph-eval/tools/eg-w1.exe`, then `PERF_STEPS=index-cold,neighbors,def,impact python -I tools/perf_run.py <exe> w1 orbweaver` and the same for `linux`. Compare to `base-store` and `base`. Diff `raw/perf-w1-*-{neighbors,def,impact}.txt` against `raw/perf-base-*` (ignore latency lines). Ledger entry.
- [ ] Step 5: Commit: `perf(cli): answer lookups from a focused sidecar subgraph`, with before/after numbers in the body.

### Task 6: Profile-driven load and index memory (W2)

**Files:** determined by the profile. Ledger at `docs/perf-ledger.md`.

- [ ] Step 1: Profile with the Task 1 build on the kernel: `index --force` (cold) and `query`. Save `go tool pprof -top -sample_index=alloc_space` (from `heap-exit`) and `inuse_space` (from `heap-peak`) summaries to the ledger.
- [ ] Step 2: For each candidate, in order of profile weight, make the one change, rebuild, re-measure the same step twice on the kernel, and keep it only if it beats the variance of the two baseline runs. Candidates:
  - (a) allocation-free relation comparator: compare `Type`, then `FromID`, then `ToID` piecewise with prefix semantics equal to the concatenation, as the existing `spliceFileChangesWithRelations` comment requires;
  - (b) string interning on gz decode and during index build (`unique.Make` or a local map interner) for IDs, paths, reasons and types;
  - (c) hold symbols by index or pointer instead of by-value copies in `StreamSnapshot`/`forEachRelation` maps.
  - (a) is only correct if it compares `Type`, `FromID`, `ToID` as one virtual concatenation: walk the bytes across the boundaries, not field-by-field. Add a property test: for random triples (including prefix and empty cases), `sign(new(a,b)) == sign(strings.Compare(a.Type+a.FromID+a.ToID, ...))`.
  - No default memory limit: sizing from physical RAM ignores cgroup limits and can turn an OOM into GC thrash. Document `GOMEMLIMIT` for large repos in operations docs instead, and record that in the ledger as a considered-and-rejected candidate.
- [ ] Step 3: After each kept change, run the full test suite and Task 2's guard, and confirm `raw/perf-*` lookup outputs are unchanged. A kept change that needs a test edited is a revert.
- [ ] Step 4: One commit per kept change: `perf(sem): <change> (<metric> before → after)`. Reverted attempts get a ledger row only.

### Task 7: `query --head` from the sidecar (W3)

**Files:** `internal/sem/search.go:1226` area, `internal/sem/search_cache.go:956` (`selectiveSearchSnapshotFromFull`)

- [ ] Step 1: Profile `query --head --profile full` on the kernel with the Task 1 build and record the ledger.
- [ ] Step 2: Add a guard test first, `TestQueryAnswersIdenticallyFromSidecarAndFullLoad`.
  - Same shape as Task 2, over 5 fixed natural-language queries per fixture, in `--format json` and `--format agent`.
  - JSON normalization must be the *recursive* key strip. Query's latency and cache fields are nested under `stats` (`search.go:505-513`), so a top-level strip fails on timing alone.
  - Include one query that matches nothing, and one on a fixture small enough that preselection selects every file.
  - It must PASS before the change.
- [ ] Step 3: Today `loadCachedCompleteSearchSnapshotBinding` loads the full snapshot before preselection (`search.go:1226`), and three things depend on it. Replace each with a sidecar read:
  - (a) The no-hit `SymbolsConsidered = len(Symbols)` (`:1335`) becomes a sidecar `count(*)`.
  - (b) The diagnostics header (`:2132,2179`) comes from the Task 4 post-load chain, applied to the sidecar header.
  - (c) A selection covering every file (`:1379-1395`) keeps today's full load.
- [ ] Step 4: For a partial selection, build the selective view from sidecar rows *fed through the same pipeline `selectiveSearchSnapshotFromFull` uses*: `emitRelation`, `externalRecordFor`/`mergeExternalRecord`, and the dedupe and profile filters (`search_cache.go:~1041-1062`), in rowid order. Concretely:
  - Refactor that function so its per-row body takes an iterator.
  - The full path iterates the in-memory snapshot. The sidecar path iterates `symbols WHERE file_path IN selected` (add `CREATE INDEX symbols_file ON symbols(file_path)` with this reader and bump the sidecar version) and `relations WHERE from_id IN (selected symbol IDs ∪ selected file IDs)`. Selected *file* IDs are relation sources too (`selectiveRelationRetained`, `:1199-1203`).
  - Externals are re-derived by the pipeline, never copied from the externals table.
  - Fall back to today's path when there is no exact sidecar.
- [ ] Step 5: Guard green, full suite green, kernel `query` re-measured, ledger entry. Keep or revert.
- [ ] Step 6: Commit: `perf(search): derive the selective view from the sidecar`.

### Task 8: `stats` gross estimate

**Files:**
- Modify: `internal/cli/stats.go` (`summaryCall`, `statsResponse`, `finish`), `internal/cli/stats_cache.go` (`statsCacheSchema` `v4` → `v5`)
- Test: `internal/cli/stats_gross_test.go`

**Where the data lives:**
- Named paths exist only in `fileScanner`'s timeline (`entry.paths = graphOutputPaths(text)`, `stats.go:~687`).
- The per-transcript memo (`stats_cache.go`) is keyed by transcript identity plus binary, not by repo, so file sizes must NEVER be computed inside the scanner or stored in the memo.
- Instead, the scanner records each successful (non-`is_error`) locate result's normalized named paths, plus the root that the call resolved against, on `summaryCall`:
  - new `json:"p,omitempty"` and `json:"o,omitempty"` fields;
  - bump `statsCacheSchema` to `v5`.
- `finish` stats each distinct (root, path) once and sums per call.
- Replay dedup by call ID (`merge`) then covers sidechain duplicates for free.

**Root resolution per call:**
1. the call's own `--repo` argument, made absolute against the record's `cwd`;
2. else the record's `cwd`;
3. else the report's `--repo`.

Stat through `os.OpenRoot(root)`, so absolute, `..` and UNC paths are refused rather than followed. That refusal is the no-egress guard on Windows. A refused or missing path contributes 0.

**Interfaces:** Produces two additive JSON fields on `statsResponse`:
```go
// Sum over successful graph locate results of max(0, size of the distinct existing
// files the result names - the result's own bytes). Never negative per result.
GrossCoveredBytes     int64 `json:"gross_covered_bytes"`
GrossCoveredEstTokens int64 `json:"gross_covered_est_tokens"` // bytes / 4
```

- [ ] Step 1: Write the failing tests.
  - **Fixture shape.** Result texts must use the renderer's row shape, which is the only shape that names a file (`graphLocatedRowPattern`). Use rows such as `1. a.go:10 func A()`, one per line. Prose like "found in a.go:10" names nothing.
  - **Transcript shape.** Write it in the test, mirroring the real Claude Code JSONL shape that `stats_test.go` fixtures use. The tool_use is a Bash `entire graph query --repo <tmp> ...`.
  - **Command.** Run `stats --repo <tmp> --transcript <file> --since all --format json --cache-dir <t.TempDir()>`.
  - `TestGrossCountsNamedFilesMinusResultBytes`:
    - Setup: a temp repo with `a.go` of exactly 4,000 bytes and `b.go` of exactly 2,000 bytes. The query's tool_result is exactly 600 bytes, with rows naming `a.go:10` and `b.go:3`, padded with a trailing comment line.
    - Expect `gross_covered_bytes == 5400` and `gross_covered_est_tokens == 1350`.
  - `TestGrossIgnoresResultsNamingNoFile`: the same with a result naming no file. Expect `0`.
  - `TestGrossNeverGoesNegative`: a result naming one 100-byte file, with the result itself 500 bytes. Expect `0`.
  - `TestGrossCountsEachFileOncePerResult`: a result naming `a.go:1` and `a.go:50`. Expect `4000 − result bytes`.
  - `TestGrossFollowsTheCallsOwnRepo`:
    - Setup: the report runs with `--repo <tmpA>`. The query call passes `--repo <tmpB>`, where `a.go` is 4,000 bytes; `<tmpA>` has no `a.go`.
    - Expect the 4,000-byte figure.
    - Then re-run with `a.go` in `<tmpB>` grown to 8,000 bytes and the same cache dir. Expect the new figure.

  Break caught by each: wrong sum, double-counting, a negative leak, counting unnamed results, the wrong root, and sizes frozen into the memo.
- [ ] Step 2: Run. Expected: FAIL (unknown field, zero).
- [ ] Step 3: Implement as described under "Where the data lives". Exclude `is_error` results.
- [ ] Step 4: Run the stats tests and the full `./internal/cli`. Expected: PASS.
- [ ] Step 5: Commit: `feat(stats): report a gross covered-files estimate beside the modeled balance`.

### Task 9: `entire graph statusline` subcommand

> Amended by Task 9c: the rendered segments (no `✓ indexed`, lookup count or `net n/a`; a stale state), and nothing outside a checkout.

**Files:**
- Create: `internal/cli/statusline.go`, `internal/cli/statusline_test.go`, `internal/sem/head_indexed.go`
- Modify:
  - `internal/cli/root.go`: dispatch `statusline`.
  - `internal/cli/stats.go` `graphVerbs`: add `statusline`, or `TestGraphVerbsCoverEveryCommand` fails.
  - `internal/cli/help.go` `commandDocs`: add an entry.
  - `internal/cli/help_test.go` `dispatchCommands`: add it, or `TestRegistryMatchesDispatch` fails.
  - `savingsModelText` (`stats.go:~45`) and AGENTS.md's stats section: both say the status line reads `estimated_savings_est_tokens_unfloored`. Update them to name `modeled_balance_est_tokens` and `gross_covered_est_tokens`.

**Interfaces:**
- **Consumes** the Task 8 fields.
- **Produces** `runStatusline(ctx, opts Options, args []string) error`.
  - It reads the Claude Code status JSON from `opts.Stdin`. Fields: `transcript_path`, `workspace.project_dir`, `workspace.current_dir`, `cwd`, `context_window.used_percentage`.
  - The repo anchor is `workspace.project_dir`, falling back to `current_dir`, then `cwd`, resolved to the git top level.
  - It writes ≤ 2 lines.
- **Produces** in `sem`: `func HeadIndexed(ctx context.Context, repo, providerVersion, cacheDir string) (bool, error)`.
  - True when a committed-tree cache entry exists for HEAD under any profile (full or fast, with the repo's default policy inputs).
  - It only stats entry paths. It never hashes or opens the entry or the sidecar.
  - `DescribeSearchStore` is NOT usable here: it SHA-256s the whole gz entry (404 MiB on the kernel; `cache_entry.go:358`), runs `count(*)` over four tables, and only sees the full-profile sidecar.
  - Cost: one `git` HEAD/tree resolution plus a few `stat`s.

Render, with ANSI colours unless `NO_COLOR` is set (indigo `38;2;84;111;255` for the brand mark and figures, muted `38;5;244` for separators):
```
◤ entire graph · ✓ indexed · 23 lookups · ~184K tok gross · ~12.4K net
▸ ctx 42%
```
- `lookups`: graph locate calls in this transcript.
- `gross`: `gross_covered_est_tokens`.
- `net`: `modeled_balance_est_tokens`, which may be negative (`−7.3K net`). It reads `net n/a` when `sessions_with_savings_comparison == 0`.
- With no lookups, line 1 drops the three figures.
- `not indexed · run entire graph index` replaces `✓ indexed` when `HeadIndexed` is false, or when HEAD is unborn or unresolvable. That case is a state, not an error.
- Line 2 is omitted when `context_window` or `used_percentage` is absent or `null`. It can be `null` early in a session, per the Claude Code status line docs.
- Width: segments are dropped from the right until the visible width is ≤ `COLUMNS`, which Claude Code sets. The default is 120.
- Any other error prints nothing and returns nil.
- **Caching:** cache the computed figures, never the rendered line, so width and `NO_COLOR` always apply live.
  - Location: `<resolveCacheDir>/statusline/<sha256(transcript_path)>.json`, so `--cache-dir` and the plugin data dir are honoured.
  - Key: size and mtime of every transcript file in scope (main plus `<session>/subagents/*.jsonl`), plus the HEAD tree and the binary identity.
  - A miss recomputes synchronously.
  - Claude Code debounces at 300 ms and cancels an in-flight run when a new update arrives. Step 5 therefore measures the render on a large real transcript. If p50 > 300 ms, record it in the ledger and file incremental (append-offset) parsing as a follow-up. Do not add a background refresher: no daemons.

- [ ] Step 1: Write the failing tests.
  - **Harness.** Each test runs the real `runStatusline` with real stdin bytes, a transcript and repo built in the test, `NO_COLOR=1`, an explicit `COLUMNS` (so an inherited CI value can't change the result) and a `t.TempDir()` cache dir.
  - `TestStatuslineShowsLookupsGrossNetAndContext`:
    - Transcript: 1 graph query result whose rows name the 4,000-byte `a.go`. The result is 400 bytes, so gross is 900 tokens.
    - Stdin: `{"transcript_path":…,"workspace":{"project_dir":…},"context_window":{"used_percentage":41.6}}`.
    - Expect line 2 `▸ ctx 42%`. Expect line 1 to contain, in order, `1 lookups`, `~900 tok gross`, and a net segment equal to `stats --transcript <same> --format json`'s `modeled_balance_est_tokens` passed through the formatter (or `net n/a` when `sessions_with_savings_comparison == 0`).
    - The net is a differential, not a hand literal: its formula (`modeledBalanceBytes` with its 5-call follow-up window) belongs to `stats`, and this test only pins that the status line shows what `stats` computes.
    - Assert segment presence and order, not the full line.
  - `TestStatuslineShowsIndexedAfterIndex`: index the temp repo (fast profile) into the test cache dir. Line 1 contains `✓ indexed`.
  - `TestStatuslineReportsMissingIndex`: a fresh repo with no index gives line 1 containing `not indexed`.
  - `TestStatuslinePrintsNothingOnBadInput`: empty stdin, `{`, and a transcript path that does not exist each give empty stdout and a nil error.
  - `TestStatuslineOmitsContextWhenNull`: `"used_percentage":null` gives exactly one line.
  - `TestStatuslineDropsWholeSegmentsToFit`: `COLUMNS=40`. The output's visible width is ≤ 40, and every segment present is one of the segments from the 120-column render, unchanged.
- [ ] Step 2: Run. Expected: FAIL (unknown verb).
- [ ] Step 3: Implement it.
  - Reuse the stats collector in-process (no subprocess), and use `HeadIndexed` for the indexed state.
  - Format numbers as `~N` below 1,000, then `~N.NK` and `~N.NM`. Negatives use U+2212.
- [ ] Step 4: Run it plus the full `./internal/cli`. Expected: PASS.
- [ ] Step 5: Measure render latency (cold cache and miss-after-append) on the largest real transcript under `~/.claude/projects/O---web-omnith/`. Record it in the ledger.
- [ ] Step 6: Commit: `feat(cli): add a Claude Code status line subcommand`.

### Task 9b: Status line warm render without git

> Amended by Task 9c: tests that expect `✓ indexed` or `not indexed` now classify the state as current, behind or not indexed; the git record also holds ancestor entries.

Task 9 measured a cache-hit render at 409 ms p50, above Claude Code's 300 ms debounce (see Task 9, Caching). Most of a hit is spent on git work whose answer has not changed: `WithGitMetadataValidationForSetup`, plus the git calls for HEAD and, inside `HeadIndexed`, for HEAD again, the remote URLs and the scope prefix. The transcript listing is also on the hit path, and it stays there because the figures key needs it. Step 1 re-measures this breakdown, since Task 9's per-item numbers overlap and sum to more than 409 ms.

**Files:**
- Modify: `internal/cli/statusline.go`, `internal/sem/head_indexed.go`, `docs/perf-ledger.md`
- Test: `internal/cli/statusline_test.go`

**Interface:** replace `HeadIndexed` with
```go
// IndexEntry is an opaque, JSON-serialisable handle on one file whose presence means
// "HEAD is indexed". Only HeadIndexEntries builds it; M3 changes only that function's body.
type IndexEntry struct{ Root, Relative string }
func HeadIndexEntries(ctx context.Context, repo, providerVersion, cacheDir string) ([]IndexEntry, error)
// AnyIndexEntryExists stats each entry through os.OpenRoot(Root), the way HeadIndexed's
// exists() does today. It never opens or hashes an entry.
func AnyIndexEntryExists(entries []IndexEntry) bool
```
Delete `HeadIndexed`; the status line is its only caller.

**Git key:** a key built without running git, from:
- the repo path, the cache dir path (the path, not its stat), `statsCacheSalt(version)`, and the resolved max-files value (`resolveMaxSourceFiles`, which reads `ENTIRE_GRAPH_MAX_FILES`);
- the contents of `.git/HEAD`, of the loose ref HEAD names, and of `.git/config` (all small);
- when the loose ref is absent, the `packed-refs` lines that can name its value: the header, the named ref's line and its `^` peeled line. They come from a streaming scan of `.git/packed-refs` (up to 256 MiB); a repo that has run `git gc` keeps hitting;
- the contents of `<repo>/.graphignore`, or "absent".

Contents are hashed rather than statted, so a same-size ref rewrite within one mtime tick still changes the key. Every read goes through the repo `os.Root` with `Lstat`, and must find a regular, non-link file. Each file is read up to 1 MiB, except `packed-refs` (above). Git's user and system config are not inputs: `gitutil` runs git with both set to `/dev/null`.

**Fallback:** the render takes today's full path, validation included, when:
- `.git` is not a plain directory (a linked worktree, a submodule, or a link), or `.git/commondir` exists;
- HEAD is neither `ref: refs/...` nor a 40- or 64-hex id;
- the loose ref is itself `ref: ...` (a symbolic-ref chain);
- `.git/config` contains `include`, `includeIf`, `worktreeConfig` or `refStorage`, matched case-insensitively;
- any key input is a link, is not a regular file, or exceeds its read bound.

**Hit and miss:**
- A full-path render writes `<cacheDir>/statusline/git-<sha256(repo)>.json`. It holds the git key, the HEAD tree and the `[]IndexEntry`, and no figures or rendered text.
  - It writes the record only when validation passed, HEAD resolved (or is unborn), and the context was not cancelled.
  - A cancelled, timed-out or refused render writes nothing. That keeps a transient failure from pinning "not indexed" under an unchanged key.
- On a key hit the render runs no git and skips `WithGitMetadataValidationForSetup`. The validation guards git subprocesses, and a hit spawns none.
- `AnyIndexEntryExists` runs on every render, so a later `index` at the same HEAD shows `✓ indexed` on the next render.
- The figures cache (Task 9) is unchanged, except that its key takes the stored tree.

- [ ] Step 1: Re-measure the Task 9 breakdown (validation, each git call, listing, collector) with `ENTIRE_GRAPH_PPROF` or timers in a scratch build, and correct the statusline row in `docs/perf-ledger.md`.
- [ ] Step 2: Write the tests. Use the Task 9 harness: real `runStatusline`, a real repo and transcript, `NO_COLOR=1`, explicit `COLUMNS` and a temp cache dir. Each test renders twice before it mutates anything, so the render under test is a warm one.
  - `TestStatuslineWarmRenderRunsNoGit`:
    - Index, render twice, then render with `PATH` set to an empty temp dir. Git for Windows ships `git.exe` in several directories, so filtering git out of PATH is not enough.
    - Expect `✓ indexed`.
    - This catches a hit path that needs git. It FAILS on Task 9's code.
    - A hit path that spawns git and ignores the failure would still pass this test. Step 5's latency gate catches that case instead.
  - `TestStatuslineNoticesKeyInputChanges` is a table test with one subtest per input:
    - a new commit;
    - a change from one `https://github.com/...` origin URL to another (a non-GitHub remote leaves the repo key unchanged);
    - an edited `.graphignore`;
    - a commit through a symbolic-ref chain (HEAD → `alias` → `main`);
    - a commit inside a linked worktree, with the stdin anchor set to the linked worktree.

    Each subtest indexes, renders, mutates and renders again, and expects `not indexed`. This catches a key that misses a ref, config, policy or fallback case. The test must PASS on Task 9's code.
  - `TestStatuslineNoticesALaterIndex`: render (`not indexed`), run `index` at the same HEAD, render again. Expect `✓ indexed`. This catches a cached indexed state. It must PASS on Task 9's code.
- [ ] Step 3: Implement `HeadIndexEntries`/`AnyIndexEntryExists`, the git key, the fallback and the hit path. Run the new tests. Then check the table test bites:
  - drop `.git/config` from the key, and expect the remote subtest to fail;
  - drop the loose ref, and expect the commit subtest to fail;
  - drop the "`.git` is not a plain directory" fallback, and expect the worktree subtest to fail. An unreadable input must hash as a stable "absent" for this check to bite;
  - restore all three.
- [ ] Step 4: Run `go test -short -timeout 40m ./internal/cli`, `go test -short ./internal/sem` and `go vet ./...`.
- [ ] Step 5: Measure on the Task 9 transcript, 20 runs each. Keep the change if and only if:
  - warm-hit p50 ≤ 150 ms;
  - append-miss p50 ≤ 300 ms;
  - git-key-miss p50 is no worse than Task 9's 409 ms hit plus the noise between runs.

  Record p95 and cold for information. Write baseline → result → verdict in `docs/perf-ledger.md`. If the change fails, revert the code and keep the ledger entry.
- [ ] Step 6: Commit: `perf(cli): render a warm status line without running git`.

### Task 10: `setup` installs the status line

> Amended by Task 9c: setup installs it only with `--statusline` or a "yes" at an interactive prompt, never by default; the plugin manifest no longer sets it; the preflight hint names only the applicable opt-out.

**Files:**
- Modify:
  - `internal/agentsetup/claude_hook.go`: reuse its settings read/merge/refuse-malformed/atomic-write.
  - `internal/cli/agents.go` `graphAgentCommand` install branch: add flags and preflight, and move the status line step so `--no-claude-hook`'s early return (`agents.go:~87`) no longer skips it.
  - `internal/cli/help.go`: document the flags.
- Test: `internal/agentsetup/claude_statusline_test.go`, plus one `internal/cli` setup test.

**Interfaces:** Produces:
```go
// ForeignStatusLine reports which settings scope already defines a statusLine that
// is not ours, in Claude Code precedence order: project local, project shared
// (<root>/.claude/settings.json), user (<home>/.claude/settings.json). "" when none.
// home is a parameter so tests never read the real one.
func ForeignStatusLine(root, home string) (path string, err error)
func CheckClaudeStatusLine(root string) error // preflight: settings.local.json readable and an object
// InstallClaudeStatusLineChanged merges statusLine into <root>/.claude/settings.local.json.
// It never touches a foreign statusLine in that file.
func InstallClaudeStatusLineChanged(root string) (path string, created, changed, foreign bool, err error)
```

**Command:**
- The command written is the bare `entire graph statusline` on every OS.
- `statusLine` has no `shell` key. On Windows Claude Code runs it through Git Bash when that is installed, else PowerShell. The hook's PowerShell `try{…}` wrapper would break under bash, and the POSIX `|| true` wrapper breaks under PowerShell 5.1.
- The subcommand fails open itself. An older binary's "unknown command" goes to stderr, which Claude Code never renders.
- "Ours" means the command string contains `entire graph statusline`.

**Precedence:**
- A project-local `statusLine` overrides the shared-project and user ones. Installing one silently hides a user's existing status line (for example a graft line in `~/.claude/settings.json`).
- Default behavior:
  - If `ForeignStatusLine` finds one in any scope, setup writes nothing and reports `kept your status line (<path>) — rerun with --statusline to use entire graph's in this repo`.
  - `--statusline` installs into settings.local.json even when a shared/user status line exists, and says that it now overrides `<path>` in this repo.
  - A foreign line *inside settings.local.json* is never replaced, even with `--statusline`.
- `--no-statusline` skips the step entirely, including its preflight.
- The preflight error hint changes from "(fix it, or pass --no-claude-hook)" to name both opt-outs.

- [ ] Step 1: Write the failing tests, all against real files in a temp dir, with `home` set to another temp dir.
  - `TestStatusLineInstallIsIdempotent`: install twice. First call `changed=true`; second `changed=false`; file bytes are equal after both.
  - `TestStatusLineInstallKeepsForeignLocalStatusLine`: pre-write settings.local.json with `{"statusLine":{"type":"command","command":"node graft-statusline.cjs"}}`. Install returns `foreign=true, changed=false`, and the file is byte-identical.
  - `TestForeignStatusLineFindsUserScope`: a user-scope settings.json with a foreign `statusLine` and an empty project. Expect its path. Ours in user scope → "".
  - `TestStatusLineInstallPreservesOtherSettings`: pre-existing `hooks.SubagentStart` and an unrelated key survive the install (decode and compare those subtrees).
  - `TestSetupInstallsStatusLineByDefault` (cli): with an empty home, `setup --repo <tmp>` writes a `statusLine` whose command is `entire graph statusline`. With `--no-claude-hook` it still writes it.
  - `TestSetupNoStatuslineFlagWritesNone` (cli): `setup --repo <tmp> --no-statusline` leaves no `statusLine` key.
  - `TestSetupKeepsUserStatusLineUnlessAsked` (cli): with a home that has a foreign user status line, setup writes none. With `--statusline` it writes ours.
  - Setup tests in `internal/cli` need a way to point `home` at a temp dir: use the existing `Options` env/home seam if there is one, else `t.Setenv("HOME")` and `USERPROFILE`.
- [ ] Step 2: Run. Expected: FAIL.
- [ ] Step 3: Implement the install and a UI row with these outcomes:
  - `wrote`
  - `unchanged`
  - `kept your status line (<path>) — rerun with --statusline …`
  - `overrides <path> in this repo`
  - `skipped (--no-statusline)`
- [ ] Step 3b: Manually check on this Windows machine. With Git Bash present, Claude Code shows the line. Confirm piped stdin reaches the binary when the command runs under PowerShell 5.1 (run `'{}' | powershell -NoProfile -Command "entire graph statusline"`). Record the result in the commit body.
- [ ] Step 4: Run the full suite and `go vet`. Update the README/AGENTS.md `setup` descriptions and the statusline-script header to point at the subcommand.
- [ ] Step 5: Commit: `feat(setup): install the Claude Code status line`.

### Task 9c: The status line is opt-in, lean and stale-aware

> **Amended by Task 9h:** steps 3, 4.1 and 4.2 name `OurStatusLineInstalled` and a skip that reads no other settings. Task 9h replaces both with `ActiveStatusLine`, which finds our line in any scope and reports it unchanged.

Added after the branch's first `entire review` (2026-11-04) and the first live render in Claude Code. It amends Tasks 9, 9b and 10. Three product decisions drive it:
- **Opt-in.** A status line takes a screen line the user owns, so installing it is the user's choice, made at setup, in every harness `setup` configures that runs a command status line. Today that is Claude Code only. Installing the Claude Code plugin no longer turns it on.
- **Lean.** The line shows only what the user acts on. `✓ indexed`, the lookup count and `net n/a` go.
- **Stale-aware.** When HEAD has no index but a recent first-parent ancestor does, the line says how far behind the index is instead of `not indexed`. Automatic refresh is an M4 requirement (see the ledger), not this task.

It also closes `entire review` findings #2 (`setup --no-claude-hook` aborts on a malformed `settings.local.json`), #7 (a non-git directory shows `not indexed`) and #8 (the script's fallback drops `"$@"`).

**Files:**
- Modify:
  - `internal/cli/agents.go`: the status line step of the setup install branch (decision below). It runs after `ui.Intro` and before `InstallChanged`, so nothing is written before the user answers.
  - `internal/cli/root.go`: an `Options.interactive func() bool` seam beside `homeDir`. Its default is "stdin and stdout are both terminals". `isTerminal` (`progressbar.go:16`) takes an `io.Writer`; add the same character-device check for stdin.
  - `internal/agentsetup/claude_statusline.go`: add `OurStatusLineInstalled(root string) bool`. It is true only when `settings.local.json` is a readable, regular, non-link JSON object whose `statusLine.command` contains `entire graph statusline`. Every read or parse failure is false, never an error.
  - `internal/agentsetup/claude_hook.go`: `claudeSettingsHint` names only the opt-out that applies. The hook step's refusal names `--no-claude-hook`; the status line step's refusal names `--no-statusline`.
  - `internal/sem/head_indexed.go`: `HeadIndexEntries` also returns the ancestors' entries (below), and `HeadKeyWithoutGit` hashes `.git/shallow`.
  - `internal/cli/root.go` and `internal/cli/agents.go`: thread `ctx` into `graphAgentCommand` and `runSetup` (`agents.go:179`, `root.go:371`), which the prompt read needs.
  - `internal/cli/statusline.go`: the segments below, the stale state, and no anchor outside a checkout. The git record gains the ancestor entries; bump `statuslineGitCacheSchema`.
  - `.claude-plugin/plugin.json`: remove the whole `settings` object, not just its `statusLine` key.
  - `scripts/entire-graph-statusline.sh`: the fallback becomes `exec entire graph statusline "$@"`. Its header (line 5, "the plugin manifest runs it directly") stops being true; reword it.
  - `internal/cli/help.go`: the setup and statusline entries.
  - Docs that state default install, the plugin's status line, `lookups` or `net n/a`. Amend these passages in place; do not add a contradicting entry:
    - `README.md:95-99`;
    - `AGENTS.md:207-210` (stats section);
    - `docs/agents.md:43-56`, `:70` and `:182`;
    - `docs/operations.md:217-221`;
    - `docs/trust-and-security.md:215-218`;
    - `skills/stats/SKILL.md:98-121`;
    - `CHANGELOG.md:17-30` and `:44`.
- Test: `internal/cli/statusline_test.go`, `internal/cli/setup_statusline_test.go`, `scripts/entire-graph-statusline_test.sh`.

**Setup decision** for the status line step, first match wins:
1. `--no-statusline`: skip without reading any settings. Row: `skipped (--no-statusline)`.
2. `--statusline`: install exactly as Task 10 does. That means the preflight, overriding a shared or user line in this repo, and never replacing a foreign line inside `settings.local.json`.
3. No flag, not interactive:
   - `OurStatusLineInstalled` (it never errors) gives row `unchanged`.
   - Otherwise skip without reading any other settings. Row: `skipped (pass --statusline to install it)`.
   - A skip never writes, so a line installed earlier always stays.
4. No flag, interactive:
   1. `OurStatusLineInstalled`: row `unchanged`, no prompt.
   2. `ForeignStatusLine` fails: Task 10's `skipped (could not read <path>)` row, no prompt. It finds a line in any scope: Task 10's `kept your status line (<path>) — rerun with --statusline …` row, no prompt.
   3. Otherwise print `Install the Claude Code status line (estimated graph tokens, graph size)? [y/N] ` to stdout.
      - Read the answer in a goroutine and select on `ctx.Done()`. A cancelled context returns its error before anything is written.
      - The answer is the text up to the first newline or EOF, trimmed. `y` or `yes`, in any case, means yes.
      - Anything else means no, including EOF, a read error and a nil `Stdin`. Row: `skipped (not chosen)`.
   4. On yes: run `CheckClaudeStatusLine`, then install as in 2. A preflight error fails setup before any write, with a hint naming `--no-statusline`.

So an unchosen line never fails setup on `settings.local.json`. That covers steps 1 and 3, step 4.2, and a "no" at 4.3. A malformed file fails only where the Claude hook step already refuses it, as on `main` (finding #2).

**Render**, amending Task 9's spec:
```
◤ entire graph · ~184K tok gross · ~12.4K net
▸ ctx 42%
```
Line 1 holds these segments in order. Each appears only when its condition holds:
1. `◤ entire graph`: always.
2. The index state, only when HEAD has no index entry:
   - `index N commits behind` when the nearest first-parent ancestor with an entry, within `sem.MaxStoreContextAncestorDistance` (5), is N commits back. Use `1 commit behind` for N = 1.
   - `not indexed` otherwise. That covers no such ancestor, and HEAD unborn or unresolvable inside a checkout.
3. `run entire graph index`: whenever segment 2 is present.
4. `~X tok gross`: when lookups > 0.
5. `~Y net`, signed with U+2212: when lookups > 0 and `sessions_with_savings_comparison > 0`.

The line never shows `✓ indexed`, `N lookups` or `net n/a`. With no checkout at the anchor it prints nothing (finding #7): `statuslineAnchorRepo` returns "" when `discoverImplicitCheckoutRoot` fails, instead of the absolute path. Width fitting, colours, the figures cache and line 2 are unchanged.

**Stale state:** `HeadIndexEntries` returns the ancestors' entries too, so HEAD, the strict repo key, the scope and the policy inputs are each derived once per render:
```go
// HeadIndexEntries returns the entries whose presence means HEAD is indexed, and, for each
// of HEAD's first-parent ancestors up to maxBehind commits back, nearest first, the same
// for that ancestor's tree (same repo key, scope and policy inputs). The ancestors come
// from one gitutil.FirstParentCommitTrees(ctx, repo, head, maxBehind+1) walk, whose first
// element must be HEAD itself (as search_store_ancestor.go:179 checks). A shorter or
// shallow history returns fewer. walkErr reports a failed walk separately from err.
func HeadIndexEntries(ctx context.Context, repo, providerVersion, cacheDir string, maxBehind int) (head []IndexEntry, behind [][]IndexEntry, walkErr, err error)
```
- **When it runs:** only on the full (git-key miss) path. The walk runs only when HEAD resolved to a tree.
- **Errors:**
  - `err` is handled as today: the render prints nothing and saves no record.
  - A `walkErr` is not fatal, because the walk is an extra. The render shows HEAD's own state (current or `not indexed`) and saves no record.
- **Record:** the git record stores the ancestor entries beside HEAD's.
- **Key input:** the git key also hashes `.git/shallow`, or "absent" when it is missing. A `git fetch --deepen` or `--unshallow` at the same HEAD then refreshes the ancestor list.
- **Every render**, warm hits included, stats HEAD's entries and then each ancestor's in order. It runs no git. A later `index` at HEAD therefore shows as current on the next render.

- [ ] Step 1: Write or amend the tests. Render tests use the Task 9 harness. Setup tests use real temp repos and a temp home.
  - **Test helper:** `statuslineIndexedState` (`statusline_test.go:211-222`) classifies line 1 into one of three states:
    - current: a non-empty line with no state segment;
    - `behind N`;
    - `not indexed`.
    Empty output is a failure, never "current".
  - `TestStatuslineShowsGrossNetAndContext`, renamed from `TestStatuslineShowsLookupsGrossNetAndContext`. It indexes the repo first (`statuslineIndex`).
    - Line 1 is exactly `◤ entire graph · ~900 tok gross · <net>`, with no net segment when `stats` reports no comparison.
    - Line 2 is `▸ ctx 42%`.
    - This catches a filler segment returning.
  - `TestStatuslineIsBareWhenIndexedWithNoLookups` replaces `TestStatuslineShowsIndexedAfterIndex`. Index, then render with a transcript that has no lookups. Stdout is exactly `◤ entire graph\n`.
  - `TestStatuslineShowsHowFarBehindTheIndexIs`:
    - Index, make two commits, render. Line 1 contains `index 2 commits behind · run entire graph index`.
    - Index again and render: the state is current.
    - This catches stale falling back to `not indexed`, and a cached stale state.
  - `TestStatuslineSaysNotIndexedBeyondTheAncestorBound`, a table test. Index, make N commits, render.
    - N = 5 gives `index 5 commits behind`.
    - N = 6 gives `not indexed`.
    - This catches a walk that passes `maxBehind` instead of `maxBehind+1`.
  - `TestSetupHintNamesOnlyTheApplicableOptOut`: `settings.local.json` holds `[]`.
    - A plain `setup` fails at the hook step, and its error names `--no-claude-hook` and not `--no-statusline`.
    - `setup --statusline --no-claude-hook` fails at the status line step, and its error names `--no-statusline` and not `--no-claude-hook`.
  - `TestStatuslinePrintsNothingOutsideARepo`: anchor a plain temp dir, with `GIT_CEILING_DIRECTORIES` set to its parent so a git-tracked home cannot interfere. Stdout is empty.
  - Amend Task 9b's `TestStatuslineNoticesKeyInputChanges`: each subtest expects the state not to be current. A commit now reads `behind 1`, and an origin or `.graphignore` change reads `not indexed`.
  - `TestStatuslineWarmRenderRunsNoGit` and `TestStatuslineNoticesALaterIndex` expect the state current where they expected `✓ indexed`.
  - Replace `TestSetupInstallsStatusLineByDefault` with `TestSetupLeavesTheStatusLineOffUnlessChosen`. A non-interactive `setup --repo <tmp>` writes no `statusLine`; with `--statusline` it writes ours.
  - `TestSetupAsksBeforeInstallingTheStatusLine` (interactive seam on):
    - Stdin `y\n` writes ours.
    - Stdin `\n` writes none, and empty stdin writes none.
    - Stdout contains `[y/N]`; do not assert the prompt's wording.
    - This catches a prompt that defaults to yes.
  - `TestSetupDoesNotAskWhenAStatusLineExists` (interactive seam on, stdin `y\n`):
    - With ours already in `settings.local.json`, it is still ours afterwards and no `[y/N]` is printed.
    - With a foreign line in the user scope, no `statusLine` is written, no `[y/N]` is printed, and the row says `kept your status line`.
  - `TestSetupNoClaudeHookIgnoresStatusLineSettingsUnlessChosen`:
    - `settings.local.json` holds `[]`, then a BOM-prefixed object.
    - Run it non-interactively, and interactively with stdin `\n`.
    - `setup --no-claude-hook` succeeds and writes the guides (finding #2).
  - Amend `TestSetupKeepsUserStatusLineUnlessAsked` and `TestSetupSkipsStatusLineWhenAScopeIsUnreadable`. Their default cases run with the interactive seam on and expect the same rows as today. `TestSetupNoStatuslineFlagWritesNone` is unchanged.
  - `scripts/entire-graph-statusline_test.sh`: with `ENTIRE_GRAPH_BIN` unset and the test's stub `entire` first on PATH, an argument reaches the stub's recorded argv (finding #8).
- [ ] Step 2: Run them. New and amended tests FAIL; unchanged ones pass.
- [ ] Step 3: Implement it. Remove `settings.statusLine` from the plugin manifest and amend the help text and docs listed above.
- [ ] Step 4: Run `go test -short -timeout 40m ./internal/cli`, `go test ./internal/agentsetup`, `go test -short ./internal/sem -run 'Index|Ancestor'`, the script test and `go vet ./...`.
- [ ] Step 5: Measure as in Task 9b Step 5, 20 runs each.
  - The warm-hit p50 stays within Task 9b's result plus noise, since a hit adds only stats.
  - The git-key-miss p50, which now includes one first-parent walk, must be no more than 10% above Task 9b's.
  - Record both in `docs/perf-ledger.md`.
  - If the miss gate fails, revert only the stale state: the walk, the ancestor entries and their tests. Keep the rest of the task, and record the attempt in the ledger.
- [ ] Step 6: Commit: `feat(setup): ask before installing a lean, stale-aware status line`.

### Task 9d: `entire review` cleanups

Closes the remaining in-scope `entire review` findings. #3 and #6 (repeated hashing and git work) are parked for M1, which replaces those code paths.

**Files:**
- Modify: `internal/cli/def.go` (the text and agent renderers), `internal/sem/search_store_focus.go` (`distinctIDs`).
- Test: `internal/cli/store_test.go` (`TestColdIndexSurfacesStoreWriteFailureAsWarning`, `:185`), `internal/sem/search_store_stamp_test.go`, `internal/cli/lookup_snapshot_test.go`.

- **#1:** `def` in text and agent format prints `W_SEARCH_STORE_UNAVAILABLE` the way `explain` does since cb21b953.
  - Reuse `explainWarnings` and explain's line format and placement.
  - Print it once per invocation, not once per name, outside the `--max-context-bytes` budget.
  - Output without the warning is byte-identical.
  - The warning line counts as cache telemetry under the Global Constraints' byte-identity rule, as it already does for `explain`.
- **#4:** delete the assertion in `search_store_stamp_test.go` that a corrupted entry behind an unchanged stamp is served undetected. Delete the test too if nothing else remains in it. Add no replacement: an output-equality check cannot see a skipped hash, because the sidecar and the entry mirror the same tree. The Task 2 equivalence guard covers the focused load's correctness.
- **#5:** delete `TestLookupVerbsReadTheWholeGraphOnlyForAnExactMiss`. The Task 2 equivalence guard covers correctness and the perf ledger covers speed; say so in the commit body.
- **#9:** replace `distinctIDs` with `dedupeStrings` plus an empty-string filter at the call site. `dedupeStrings` compacts in place, which is safe because every caller (`search_store_focus.go:393-590`) owns its slice.

- **From the Task 9c and 9e reviews:**
  - `internal/cli/hook_test.go:217`: the comment "leaving .claude alone takes both opt-outs" is stale. Non-interactively, `--no-claude-hook` alone is enough.
  - `statusline_test.go`: drop the `statuslineThousands` helper, which copies the production formatter line for line. `TestStatuslineShowsTheGraphSize` asserts the hand-derived literal for its deterministic fixture instead (`1,001 nodes`, plus its edge count, derived by hand).
  - `sem.IndexEntryCounts`: `Lstat` through the root and require a regular file before opening, as `exists()` does. A FIFO at the counts path must never block a render.
  - `writeSearchSnapshotCounts` calls `refuseTruncatedSnapshot` itself and writes nothing on a refusal, so every caller is guarded. That includes `main`'s `writeSearchSnapshot` site when the status line PR is cut.

- [ ] Step 1: Add a subtest to `TestColdIndexSurfacesStoreWriteFailureAsWarning` for `def --head` in text and in agent format: the warning line appears. It FAILS today.
- [ ] Step 2: Implement #1, #4, #5 and #9, and the review items above.
- [ ] Step 3: Run `go test -short -timeout 40m ./internal/cli`, `go test -short ./internal/sem -run 'Store|Focus|Stamp|Lookup'`, the Task 2 equivalence guard and `go vet ./...`.
- [ ] Step 4: Commit: `fix(cli): show the store warning in def, and drop internals-coupled tests`.

### Task 9e: The status line shows the graph's size

Added at the user's request after Task 9c: the indexed graph's size is a relevant stat, shown as node and edge counts, numbers written in full. It amends Task 9c's segment list and two of its tests.

**Render:**
```
◤ entire graph · 17,271 nodes | 86,038 edges · ~184K tok gross · ~12.4K net
```
- **Segment:** `<symbols> nodes | <relations> edges`.
  - The numbers are what `entire graph index` reports for that entry as `counts.symbols` and `counts.relations`. Nodes are symbol nodes only, not file or external-endpoint nodes; the docs say so once.
  - Numbers are written in full, with comma thousands separators (`17,271`, never `17.3K`).
  - The numbers are coloured as figures. ` | ` is coloured as a separator, like ` · `.
- **Position:** this becomes item 3 of Task 9c's line 1 list. It goes after `◤ entire graph` and before the token segments. It appears only when HEAD's own index exists, so it never sits beside a state segment. A size beside `index N commits behind` would read as the current code's size.
- **Which entry:** the first existing HEAD entry in `headIndexedProfiles` order (full before fast). Its counts are the ones reported.
- **Absent:** the segment is absent when HEAD is behind or not indexed, or when that entry has no readable counts file.

**Source:**
- **The counts file:** `<key>.counts.json`, holding `{"symbols":N,"relations":M}` as raw JSON (not gzip). It sits beside the entry `<key>.json.gz`, the way `.sqlite` and `.generation.json.gz` do. Its name does not end in `.json.gz`, so the tests that count `*.json.gz` artifacts are unaffected.
- **Who writes it:** a helper `writeSearchSnapshotCounts(entry, cache)`, fed from `cache.Snapshot.Header.Stats`.
  - It writes through the confined entry directory (`openCacheEntryDirectory`, `createRootTemp`, then `Rename`), per ADR 0004.
  - **Callers:** every complete entry writer calls it, after `refuseTruncatedSnapshot` and before the entry's own rename.
    - On this branch: `writeSearchSnapshotAndStore` (cold complete builds, `:486`, `:555`) and `persistVerifiedSearchSnapshot` (`index`, `:755`). All of them are complete-only.
    - On upstream `main`, which the status line PR is cut from: the same writers (`:478`, `:703`), plus `writeSearchSnapshot` at `:538`. That call writes both complete and selective entries, so it gets the helper guarded by `len(options.OnlyFiles) == 0`.
    - The guard sits at the caller. `cachedSearchSnapshot` cannot tell a selective view from a complete one: its stats count a subset, and `DerivedFrom` is often "".
  - **Best effort:** a failed counts write never fails `index` or a lookup, and never blocks the entry rename. The segment is just absent. This covers Windows, where a render holding the file open can make the replacing rename fail.
  - Writing it first means a render rarely sees an entry without its counts.
- **Rewrite on an `index` hit:** write the counts file when it is missing **or its numbers differ from the hit's stats**, beside `repairSearchStoreSidecarForEntry`.
  - The key covers the tree, but a full-profile hit recomputes `Stats.Relations` from the current commit's history (`bindCachedSearchSnapshot`). The same tree at another commit can therefore report a different edge count.
  - So the file holds the counts of the latest build or `index` for that entry, which is also what `index` printed last. Entries from earlier builds gain a size the next time `index` runs.
- **Who reads it:** `sem.IndexEntryCounts(entry IndexEntry) (symbols, relations int, ok bool)`. It reads the entry's counts file with a 4 KiB bound, and any failure is `ok=false`.
  - The status line calls it on **every** render, warm hits included, for the reported entry. That is one small file read beside the entry stats that a hit already does, and still no git.
  - Nothing goes into the git record, so the status line never holds its own copy: a rewrite, a `--force` rebuild or a fast-to-full change shows on the next render.
  - The path comes from `IndexEntry.Relative`: trim `.json.gz` and add `.counts.json`, as `searchStoreEntryFor` derives `.sqlite` (`search_store.go:724`).
- **Cleanup:** none is needed. Complete entries, and their counts files, live directly in `search/<ver>/`, and nothing evicts top-level entries. Retention lists only `*.sqlite` (`search_store_retention.go:262`), and no digest covers the file. Add no per-file cleanup.

**Files:**
- Modify:
  - `internal/sem/search_cache.go`: the helper, its two callers and the hit-path backfill.
  - `internal/sem/head_indexed.go`: `IndexEntryCounts`.
  - `internal/cli/statusline.go`: the segment, and the header comment at `:24`.
  - `internal/cli/help.go`: the statusline entry.
  - Docs that show or describe the line: `skills/stats/SKILL.md:103`, `README.md:96`, `AGENTS.md` (stats section), `docs/agents.md:43-56` and `:70`, `docs/operations.md:217-221`. Also `docs/trust-and-security.md`, which gains the new cache file written and read, and `CHANGELOG.md`.
- Test: `internal/cli/statusline_test.go`.

- [ ] Step 1: Write the failing tests. Use the Task 9 harness, and pin `--profile full` in every `index` call these tests make.
  - **Fixture:** the temp repo holds one generated Go file with 1,000 one-line functions, so the node count crosses 1,000 and exercises the separator.
  - `TestStatuslineShowsTheGraphSize`:
    - Index the fixture, then read `counts.symbols` and `counts.relations` from `entire graph index --profile full --format json` on it.
    - Line 1 contains `<symbols> nodes | <relations> edges`, with comma separators.
    - This catches a missing or abbreviated size, and counts that disagree with `index`.
  - Amend Task 9c's tests:
    - `TestStatuslineIsBareWhenIndexedWithNoLookups` becomes `TestStatuslineShowsOnlyTheSizeWithNoLookups`: stdout is exactly `◤ entire graph · <symbols> nodes | <relations> edges\n`.
    - `TestStatuslineShowsGrossNetAndContext`'s exact line 1 gains the size segment after the brand. Its numbers come from the `--format json` output of the test's own `statuslineIndex` call.
  - `TestStatuslinePrefersTheFullIndexSize`: index the fixture `--profile fast`, render, then index `--profile full` and render. Each render shows that profile's `index --format json` counts. The fixture must have calls, so the relation counts differ between the profiles. This catches a fast entry winning over full.
  - `TestHeadQueryWritesTheSize`: run `query --head --profile fast` on an un-indexed fixture, then render. The size segment shows the fast entry's counts. This catches a complete-entry writer without the helper.
  - `TestStatuslineShowsTheSizeOnlyForTheCurrentIndex`:
    - Index, commit, render: the state is `behind 1` and no `nodes` segment appears.
    - Index again and render: the size is back.
  - `TestIndexBackfillsTheSizeOnAHit`:
    - Index, delete every `*.counts.json` under the cache dir, then render: no `nodes` segment.
    - Run `index` again (a hit) and render: the size appears.
    - This catches both a hit that never writes counts and a render that keeps a stale "no size".
- [ ] Step 2: Run them. Expected: FAIL.
- [ ] Step 3: Implement it.
- [ ] Step 4: Run `go test -short -timeout 40m ./internal/cli`, `go test -short ./internal/sem -run 'Cache|Index|Store|Truncat'` (including the `*.json.gz` artifact-count tests, and `TestEverySearchSnapshotWriterRefusesATruncatedSnapshot`, which must also find no counts file after a refusal) and `go vet ./...`.
- [ ] Step 5: Measure as in Task 9b Step 5, 20 runs each.
  - The warm-hit p50 must stay within Task 9c's result plus noise; a hit adds one small read.
  - Record it in `docs/perf-ledger.md`.
  - If the gate fails, revert the task and keep the ledger entry.
- [ ] Step 6: Commit: `feat(cli): show the indexed graph's size in the status line`.

### Task 9f: Subprocesses never open a console window on Windows

Added after the user saw 3–4 console windows flash and steal focus. It fixes code already on upstream `main` and ships as its own PR (`fix/windows-hidden-subprocess`), independent of the perf and status line PRs.

**Cause** (confirmed by the first implementation attempt's probe). When entire-graph itself runs **without a console**, every console subprocess it starts gets a new, visible console window. A status line render that Claude Code starts that way is one example.
- The probe showed this for path-output commands: `hwnd` non-zero, visible, and the child the only console process.
- With a windowless console, as in an agent shell, children got no window.
- The surrogate parent alone is not the cause.

**Fix:** `HideConsoleWindow(cmd *exec.Cmd)` in `gitutil`. It is the only name; there is no lowercase twin.
- **Files:**
  - `internal/gitutil/console_windows.go`: when the current process has no console window (`GetConsoleWindow() == 0`), it ORs `CREATE_NO_WINDOW` into `cmd.SysProcAttr.CreationFlags` and sets `HideWindow`. It creates `SysProcAttr` if nil and keeps every other field. When the process has a console window, it does nothing.
  - `internal/gitutil/console_other.go` (`//go:build !windows`): a no-op.
- **Why conditional:** a terminal user keeps a console shared with git and with VERIFY's `sh`. Ctrl-C still reaches the whole tree, and no extra `conhost.exe` starts per call.
- **Called from:** `newCmd` and `newGitCmdWithCallerLocale` in `git.go` (every production git command), and `runVerifyShell` in `internal/cli/verify.go` (VERIFY's `sh`).
- **Path-output children:** they always get `CREATE_NO_WINDOW` and `HideWindow`, whatever the current process has. Their surrogate parent never has a console, so they cannot share the caller's console anyway. Set this in `pathOutputChildSysProcAttr` (`process_job_windows.go`). Amend `TestPathOutputChildSysProcAttrPreservesEveryCallerField` so it expects the caller's fields plus these two flags.
- **Out of scope:** `internal/bench` is dev tooling.
- **Docs:** add one `### Fixed` line under `[Unreleased]` in `CHANGELOG.md`, creating the heading if it is absent. No other doc describes the Windows launcher.

**Files:**
- Create: `internal/gitutil/console_windows.go`, `internal/gitutil/console_other.go`, `internal/gitutil/console_windows_test.go`.
- Modify: `internal/gitutil/git.go` (the two constructors), `internal/gitutil/process_job_windows.go` (`pathOutputChildSysProcAttr`), `internal/gitutil/process_job_windows_test.go` (the amended test), `internal/cli/verify.go`, `CHANGELOG.md`.

- [ ] Step 1: Write the failing test, `internal/gitutil/console_windows_test.go` (`//go:build windows`).
  - **Probe:** add a package `TestMain`; gitutil has none today. It checks `ENTIRE_GRAPH_TEST_CONSOLE_PROBE` **before** `m.Run()`, because git-style argv would fail the test flag parser.
    - When the variable is set, it writes `hidden` or `visible` to the file the variable names, then exits 0. `hidden` means `GetConsoleWindow()` returns 0, or a window that `IsWindowVisible` reports hidden.
    - Otherwise it ends with `os.Exit(m.Run())`.
    - The verdict goes to a file, not stdout, so the path-output NUL parser never sees it.
  - **Shim:** copy the test binary into a temp dir as `git.exe`, put that dir first on `PATH`, and set the variable. `gitSubprocessEnvironment` strips only `GIT_*`, so the variable reaches the child.
  - `TestSubprocessesStartedWithoutAConsoleHaveNoVisibleWindow`:
    - **Setup:** re-run the test binary with `-test.run=^<helper>$` and `DETACHED_PROCESS`, so the helper process has no console. Use the existing env-var helper pattern for the helper process.
    - **Inside the helper:** it builds `newCmd(ctx, dir, "git", "ls-files", "-z")` twice, sending one through plain `run` and one through the path-output launcher (`visitBoundedNULPaths`).
    - **Assertion:** each shim must write `hidden` to its own verdict file.
    - It catches any subprocess that opens its own console window when entire-graph has none, which is the user's flash.
- [ ] Step 2: Run it on this Windows 10 machine. Expected: FAIL, with `visible` from both cases.
  - The RED run briefly opens console windows on the desktop. That is the bug being reproduced; say so in the report.
  - On Windows 11 with Windows Terminal, `GetConsoleWindow` can return a hidden pseudo-window, so the RED proof holds on Windows 10. The post-fix assertion holds everywhere.
- [ ] Step 3: Implement the helper, the three call sites and the path-output flags. Update the amended test.
- [ ] Step 4: Run these:
  - `go test ./internal/gitutil` and `go vet ./...` on Windows.
  - `GOOS=linux GOARCH=amd64 go vet ./internal/gitutil` and `GOOS=darwin GOARCH=arm64 go vet ./internal/gitutil`, so the `!windows` file builds. A cross-compile cannot vet `internal/cli`: it needs CGO for the tree-sitter grammars. The Linux and macOS CI jobs (`test.yml`) cover it.
  - `go test -short -timeout 40m ./internal/cli`, because VERIFY changed.
  - Manual check: with this build installed as the plugin, make a commit so the next status line render re-checks git, and confirm no window flashes. Record the result in the commit body.
- [ ] Step 5: Commit: `fix(gitutil): never give a subprocess a visible console window on Windows`.

**PR note:** `main`'s constructors sit about 22 lines earlier in `git.go` than on this branch, which edits `RemoteURLs`. Cherry-picking onto `main` may need a trivial context resolution.

### Task 9g: Second `entire review` fixes

**Scope.** The branch's second `entire review` (2026-11-05, `entire-review-68798e85.txt` in the SDD workspace) found no correctness bug. Of its nine findings, this task fixes #2, #4, #6, #7 and #8, all in the perf PR.
- #1 is an uncommitted file the branch excludes.
- #3 and #5 stay parked for M1 and M3.
- #9 is parked as a recorded Minor. A redirected stdin fails safe to "no", and a public-boundary test of a real terminal check is not practical on Windows.
- **Interim.** #4 patches stamp trust that M3 retires along with the gz entry. #2 and #6 touch the focused loader that M3 reworks, so M3's plan must carry the behaviour forward.

**#2: narrow the focused starting set to the selected symbol.**
- **Where:** after the focused load reads the name and qualified-name matches (`search_store_focus.go`, around `:382-393`), filter them **in Go**. Two new fields carry the filters: `SnapshotFocus.File string` and `SnapshotFocus.Kind string`.
  - A starting row stays when its `file_path` equals the normalised `--file` under `strings.EqualFold`. It is normalised the way `normalizeSymbolRefFile` normalises it (`symbolref.go:91`).
  - It stays when its `kind` equals `strings.TrimSpace(--kind)` under `strings.EqualFold`.
  - An empty field means no filter.
  - **Normalisation root:** normalise `--file` against `filepath.Abs` of the same `repo` passed to `loadLookupSnapshot`, which is what `snapshot.Header.RepoRoot` holds. Not `flags.Repo` as typed.
- **Remove dropped rows.** Every row the filter drops is also deleted from the focused read's symbols (`read.symbols`, filled by `symbolsWhere` at `search_store_focus.go:567-581`). A later hop may add it back through `symbolsByID`, and that is fine. So if the filter ever disagrees with exact resolution, resolution finds nothing and the full load answers; the focus is never printed without its hops.
- **ID matches are never filtered.** An ID wins ahead of every filter in `resolveFocusSymbols` (`symbolref.go:199`). The sem filter skips rows matched by ID, so callers need no "plain name" gate.
- **When the filter applies:** `impactLookupFocus` and `neighborLookupFocus` set `File` and `Kind` whenever `--file` or `--kind` is given. The `path:line` form already goes to the full load (`focusLookupNames`).
- **Why this is safe:** the filter repeats exact resolution's own comparison, so the focused starting set holds exactly the symbols exact resolution can pick. Hops and kinship start from that set. The SQL and the sidecar schema are unchanged.
- `def` and `explain` are unchanged. `--line` is not a filter.

**#4: trust the stamp on the full-sidecar read.** *Reverted in fix round 1 (381c36a6).* It measured neutral: orbweaver 2.858 s → 2.853 s, and on the kernel it removes about 0.3 s of a 54–57 s read. The full-sidecar read hashes the entry every time again. The steps below are kept as a record and are not the shipped behaviour.
- Extract `entryDigestForStore(ctx context.Context, db *sql.DB, entry cacheEntry) (cacheDigest, error)` from `openExactSearchStoreForEntry`. It returns the recorded digest when `searchEntryStampMatches` holds, and otherwise `memoizedEntryDigest`.
- Use it in both `openExactSearchStoreForEntry` and `loadSearchStoreForEntry` (`search_store.go:999`).
  - `loadSearchStore` (`:409`) opens its own connection today. Restructure so that `loadSearchStoreForEntry` opens the sidecar once and hands that `db` to both `entryDigestForStore` and the load, rather than opening it twice.
- Update the doc comments that say the full read always hashes (`search_store_context.go:628-631`, `search_store.go:815-821`).
- Note in the ledger: `index --head` on an unchanged tree still hashes once in the repair step, so it does not speed up.

**#6: tests can see which load served a lookup.**
- When `ENTIRE_GRAPH_TRACE_LOAD=1`, `loadLookupSnapshot` writes one line to the command's stderr, `entire-graph: index load focused` or `entire-graph: index load full`. Without the variable it writes nothing.
  - It is read through an `EntireEnv` field, like the other knobs (`env.go:75-107`), and the guard sets that field.
  - `loadLookupSnapshot` gains a stderr `io.Writer` parameter. The four call sites pass `opts.Stderr`: `impact.go:183`, `neighbors.go:161`, `def.go:178`, `explain.go:307`.
- This is a test hook at the process boundary. It is not verb output, not a JSON field and not documented for users. It cannot change any verb's bytes or interact with `--max-context-bytes`.
- **The equivalence guard** (`focused_equivalence_test.go`) sets the variable.
  - Store on: every lookup whose `--symbol` is a plain name that the full load resolves exactly must print `index load focused`. Store off, every lookup prints `index load full`.
  - The `path:line` cases are excluded, because they are always full.
  - The guard's `cli()` helper captures each lookup's stderr separately, since lookups run concurrently, and searches it for the exact trace line. The guard does not byte-compare stderr.
  - It catches a focused read that fails quietly and takes the full load.

**#7:** delete `TestFoldSymbolNameAgreesWithEqualFold`. Add to the guard's fixture a Go function named with the Kelvin sign (`KelvinTemp`, starting with U+212A). Look it up through `def --head --symbol kelvintemp`. The output must be identical with the store on and off, and the trace must read `focused`.

**#8:** `search_store_stamp_test.go` finds the entry the way `search_store_default_test.go:34` does: a `WalkDir` for the single `*.json.gz` under the cache dir. It does not use `CaptureProviderCachePolicy`, `searchSnapshotKey`, `repoKey` or `newCacheEntry`.

**Files:**
- Modify: `internal/sem/search_store_focus.go`, `internal/sem/search_store.go`, `internal/sem/search_store_context.go`, `internal/cli/lookup_snapshot.go`, `internal/cli/impact.go` and `internal/cli/neighbors.go` (where they build the focus), `docs/perf-ledger.md`.
- Test: `internal/cli/focused_equivalence_test.go`, `internal/sem/search_store_focus_test.go`, `internal/sem/search_store_stamp_test.go`.

- [ ] Step 1: Write the failing tests.
  - **#6:** the guard's trace assertion. It FAILS today, because nothing prints the trace.
  - **#2:** a guard fixture where one name is defined in two files, with `impact --head --symbol <name> --file <one of them>`. The output is identical with the store on and off, and the trace reads `focused`.
    - A case variant names the file with different letter case. It must match the store-off output, which `EqualFold` resolves.
    - A guard case passes an absolute `--file`. It must match the store-off output, with the trace reading `focused`.
    - A sem-level test: `SnapshotFocus{Names: [<the symbol's ID>], File: <the other file>}` still loads that symbol. It catches the ID exemption being dropped.
  - **#4, a regression guard:** mirror `TestFocusedLookupHashesAnEntryWhoseStampChanged` through `LoadOrBuildProviderSnapshot`, with the store on. It passes today, because the full read always hashes; it keeps the stamp from being trusted after a change. Rewrite the entry's bytes so its stamp changes; the warm load must report `cacheHit=false`.
  - **#7:** the Kelvin case.
- [ ] Step 2: Implement #2, #4, #6, #7 and #8.
- [ ] Step 3: Run `go test -short -timeout 40m ./internal/cli`, `go test -short ./internal/sem -run 'Store|Focus|Stamp|Lookup'` and `go vet ./...`. Do not run `./internal/gitutil`'s console test.
- [ ] Step 4: Measure #4 with `perf_run.py <binary> <tag> orbweaver store`. The `store` mode matters: the default mode is `nostore`. Run `index-cold` once per tag, then `PERF_STEPS=query`, for a before and an after build. Record the result in `docs/perf-ledger.md`.
- [ ] Step 5: Commit: `perf(store): narrow focused lookups, trust the stamp for warm queries, and trace the load`.

### Task 9h: Third `entire review` fixes

**Scope.** The branch's third `entire review` (2026-11-06, `entire-review-011b61b0.txt` in the SDD workspace) found three new defects, checked against the source. #2 goes to the perf PR; #4 and #6 go to the status line PR. Its other four findings repeat items already excluded or parked.

**#2: the store warning is not a completeness diagnostic.**
- **Defect:** `W_SEARCH_STORE_UNAVAILABLE` has no `FilePath`, so `diagnosticInScope` puts it in scope (`completeness.go:114`).
  - A cold `neighbors --head` whose sidecar write fails prints a `Completeness:` banner in text, and `C:ok ... W1` on the compact agent path.
  - `impact` text lists it on its `Diagnostics:` footer, and counts it in the banner's total when files also failed.
  - Agents read these lines as possibly missing facts, although the warning itself says the graph is complete.
- **Rule:** the code leaves the completeness view entirely.
  - `buildCompletenessScope` and `completenessScopeOrAll` skip `sem.SearchStoreUnavailableCode`, as they skip `W_WORKTREE_SNAPSHOT`. Both, or the fallback brings it back.
  - The banner's total excludes it. Filter it where `writeScopedCompletenessBlock`'s `warnings` are built (`snapshotWarnings` at `value_refs.go:101`, or the writer itself), not from `response.Warnings`.
- **How it is shown:** `impact` and `neighbors` print `- warning W_SEARCH_STORE_UNAVAILABLE` after the answer, in every text and agent output, the way `def` and `explain` do (`explainWarnings`, `appendWarningLines`, `explain.go:207,656`).
  - It is appended after the byte-budgeted output, outside `--max-context-bytes`, as `RenderExplainWithProvenance` does (`explain.go:647-651`). So `writeImpactBounded`'s truncation and `compactAgentNeighbors` cannot drop it.
  - It prints on every path that prints output, including no match, a fuzzy match and the ambiguous definition list.
- **JSON:** the top-level `warnings` array is unchanged. `completeness_scope.in_scope_warnings` no longer lists the code; that is the intended change.
- **`query` is unchanged.** Its `Coverage:` line counts the warning at level `notice`. The agent guide already says a Coverage line does not mean partial results.

**#4: setup does not install a second copy of our status line.**
- **Defect:** `ForeignStatusLine` (`agentsetup/claude_statusline.go:149`) returns `""` when the first scope that defines a status line holds ours, whether in `.claude/settings.json` or `~/.claude/settings.json`.
  - Interactive `setup` then still asks, and on `y` it writes a duplicate into `settings.local.json`.
  - A non-interactive run says "skipped (pass --statusline to install it)" although the line already shows.
  - `AGENTS.md` says setup never asks "when another settings scope already defines one".
- **Change:** rename it `ActiveStatusLine(root, home string) (path string, ours bool, err error)`.
  - `path` is the first scope, in precedence order, that defines a status line, or `""` when none does. `ours` says whether that line is ours.
  - The scan order, the read safety and the error rules stay as they are. Update its doc comment.
- **`planStatusLine`** (`internal/cli/agents.go:161`), without `--statusline` or `--no-statusline`:
  - If the active line is ours, report `unchanged <path> (Claude Code status line already present)`, marked done.
  - Otherwise unchanged. A non-interactive run skips with the hint, including when a scope cannot be read.
  - Delete `OurStatusLineInstalled`; its only caller is this branch.
  - Rewrite the numbered steps in `planStatusLine`'s doc comment and the `scopeErr` comment on `installStatusLine` (`agents.go:235`). Steps 3 and 4 (`agents.go:154-155`) no longer hold.
- **`--statusline` is unchanged.** It writes the local line even when ours is active in another scope, because the user asked for it. `foreign` is set only when the active line is not ours.
- **Plan note:** this amends Task 9c steps 3, 4.1 and 4.2, which name `OurStatusLineInstalled` and the read-nothing-else skip. Add an "Amended by Task 9h" line under 9c's heading.

**#6: stats sizes a graph call against the directory a leading `cd` moved to.**
- **Defect:** `graphCallRoot` (`stats.go:1363`, called only at `:677`) resolves `--repo` against the record's `cwd` and ignores a `cd` earlier in the command.
  - In `cd pkg && entire graph query --repo . ...`, the graph prints paths relative to `<cwd>/pkg`, but the gross estimate sizes `<cwd>/<path>`.
  - The effect is that it prices nothing, or prices a different file.
- **Rule:** before resolving, walk the top-level segments that come before the graph call's segment.
  - The walk must know each separator. `splitShellSegments` drops them, so add a walk that keeps them; do not change `splitShellSegments`.
  - A segment whose words (`shellArgWords`) are exactly `cd DIR` moves the working directory, if the separator after it is `&&`, `;` or a newline.
    - An absolute `DIR` replaces the working directory. On Windows this includes the Git Bash form `/x/rest` with a single drive letter, which becomes `X:\rest`.
    - A relative `DIR` joins the working directory.
  - **The root is unknown when:**
    - a `cd` is followed by any other separator;
    - `DIR` contains `$` or a backtick, starts with `~`, is `-`, or (on Windows) starts with `/` or `\` without a drive letter;
    - the segment is any other `cd` form: no operand, a flag, or more than one operand;
    - an earlier segment runs `pushd` or starts with `(`;
    - the graph call is found only inside a command substitution and the command contains a `cd`.
  - Then resolve `--repo` as `graphCallRoot` does today. An absolute `--repo` ignores every `cd`. With no `--repo`, `top` stays true and the root is the moved directory. A command without `cd` resolves exactly as before.
- **An unknown root** leaves `summaryCall.Paths` nil for that call. Do not set `Root` to `""`, which means "the report's --repo".
  - Only `countCovered` and the sizer read `Paths`; follow-up classification uses the timeline entry's own paths. So the call drops out of the gross estimate and nothing else changes.
  - The stats cache is keyed on the binary's identity, so no cache version changes.

**Files:**
- Modify:
  - #2: `internal/cli/completeness.go`, `internal/cli/value_refs.go` (or the block writer), `internal/cli/impact.go`, `internal/cli/impact_text.go`, `internal/cli/neighbors.go`.
  - #4: `internal/agentsetup/claude_statusline.go`, `internal/cli/agents.go`, and the plan's Task 9c note.
  - #6: `internal/cli/stats.go`.
- Test:
  - #2: `internal/cli/store_test.go`.
  - #4: `internal/agentsetup/claude_statusline_test.go` (`TestForeignStatusLineFindsUserScope`, `TestForeignStatusLineDoesNotFollowASharedSettingsSymlink`, renamed to the new function and returns), `internal/cli/setup_statusline_test.go`.
  - #6: the stats gross-estimate tests.

- [ ] Step 1: Write the failing tests.
  - **#2:** extend `TestColdIndexSurfacesStoreWriteFailureAsWarning` on its blocked-sidecar cache, in a loop over three cases: `neighbors --head` text, `neighbors --head` agent with a `--max-context-bytes` above 27 (the `!output-truncated/coverage` marker plus newline) and below the fixed full agent output, so the compact path runs both before and after the fix, and `impact --head` text. `impact` has no agent format.
    - Each output contains `\n- warning W_SEARCH_STORE_UNAVAILABLE\n`.
    - The warning is the fixture's only diagnostic. So no line starts with `Completeness:`, `C:` or `Diagnostics:`, and the warning line comes after the answer, at the end of the output.
  - **#4:** run setup through its command entry point with the temp-home seam. A table covers our line in the temp home's `settings.json` and in the project's `.claude/settings.json`.
    - At a simulated terminal with stdin `y\n`: no prompt, no status line written to `settings.local.json`, and the `unchanged` row naming the file that holds it.
    - Non-interactive: the same `unchanged` row.
  - **#4, existing tests:** every test that runs setup without `--no-statusline` or `--statusline` must use the home seam, now that the non-interactive path reads the user scope. Audit those runs.
  - **#6:** a transcript whose record `cwd` is a temp dir.
    - `cd sub && entire graph query --repo . ...`, with a result that names a file existing only under `sub/`: the gross estimate prices it.
    - On Windows, the same with `cd /<drive>/<rest of the temp path>/sub`.
    - `cd "$D" && entire graph query --repo . ...`, with the named file present at `<cwd>/<path>` with a known size: the gross estimate does not include it.
- [ ] Step 2: Run them and see each fail for the stated reason.
- [ ] Step 3: Implement.
- [ ] Step 4: Keep all temp and cache work off `C:`: set TMP, TEMP, GOCACHE and the zig cache dirs to `O:` or `E:`. Then run:
  - `go test -short -timeout 40m ./internal/cli`
  - `go test ./internal/agentsetup`
  - `go vet ./...`
  - Do not run `./internal/gitutil`'s console test.
- [ ] Step 5: Commit each fix on its own, so each can go to its PR:
  - `fix(cli): keep the store warning out of the completeness banner`
  - `fix(setup): do not install a second copy of the status line`
  - `fix(stats): size a graph call against the directory a leading cd moved to`

### Task 11: Re-run the eval and report (W5)

- [ ] Step 1: Build the final binary to `graph-eval/tools/eg-final.exe`. Run `perf_run.py` for `final` on both repos, with the sidecar default (no `store` arg needed after Task 3).
- [ ] Step 2: Re-run the eval scenarios (`tools/run_queries.py`-style) with the final binary for the entire column and score them with `tools/score.py`. Accuracy must equal the baseline's: outputs differ only in telemetry.
- [ ] Step 3: Write `docs/perf-ledger.md`'s summary table (baseline vs final per step: wall, RSS, disk) and update the comparison doc.
- [ ] Step 4: Commit: `docs: perf ledger and kernel-scale results`.
