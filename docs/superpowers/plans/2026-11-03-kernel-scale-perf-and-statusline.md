# Kernel-scale performance pass and status line — Implementation Plan

> **Status (2026-11-04):** Tasks 1–5 done. Tasks 6–7 (W2, W3) are superseded by the [store-native index design](../specs/2026-11-04-store-native-index-design.md). Tasks 8–10 done. Task 9b (added after Task 10 landed) proceeds next, independent of Task 11. Task 11 runs after the redesign's M3.

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
- when the loose ref is absent, the contents of `.git/packed-refs`;
- the contents of `<repo>/.graphignore`, or "absent".

Contents are hashed rather than statted, so a same-size ref rewrite within one mtime tick still changes the key. Every read goes through the repo `os.Root` with `Lstat`, and must find a regular, non-link file under 1 MiB. Git's user and system config are not inputs: `gitutil` runs git with both set to `/dev/null`.

**Fallback:** the render takes today's full path, validation included, when:
- `.git` is not a plain directory (a linked worktree, a submodule, or a link);
- HEAD is neither `ref: refs/...` nor a 40- or 64-hex id;
- the loose ref is itself `ref: ...` (a symbolic-ref chain);
- `.git/config` contains `include`, `includeIf`, `worktreeConfig` or `refStorage`;
- any key input is a link, is not a regular file, or is over 1 MiB.

**Hit and miss:**
- A full-path render writes `<cacheDir>/statusline/git-<sha256(repo)>.json`. It holds the git key, the HEAD tree and the `[]IndexEntry`, and no figures or rendered text.
- On a key hit the render runs no git and skips `WithGitMetadataValidationForSetup`. The validation guards git subprocesses, and a hit spawns none.
- `AnyIndexEntryExists` runs on every render, so a later `index` at the same HEAD shows `✓ indexed` on the next render.
- The figures cache (Task 9) is unchanged, except that its key takes the stored tree.

- [ ] Step 1: Re-measure the Task 9 breakdown (validation, each git call, listing, collector) with `ENTIRE_GRAPH_PPROF` or timers in a scratch build, and correct the statusline row in `docs/perf-ledger.md`.
- [ ] Step 2: Write the tests. Use the Task 9 harness: real `runStatusline`, a real repo and transcript, `NO_COLOR=1`, explicit `COLUMNS` and a temp cache dir. Each test renders twice before it mutates anything, so the render under test is a warm one.
  - `TestStatuslineWarmRenderRunsNoGit`: index, render twice, then render with `PATH` holding no `git`. Expect `✓ indexed`. This catches a hit path that still runs git. It FAILS on Task 9's code.
  - `TestStatuslineNoticesKeyInputChanges`, a table with one subtest per input: a new commit; a change from one `https://github.com/...` origin URL to another (a non-GitHub remote leaves the repo key unchanged); an edited `.graphignore`; a commit through a symbolic-ref chain (HEAD → `alias` → `main`); and a commit inside a linked worktree. Each subtest indexes, renders, mutates and renders again, and expects `not indexed`. This catches a key that misses a ref, config, policy or fallback case. It must PASS on Task 9's code.
  - `TestStatuslineNoticesALaterIndex`: render (`not indexed`), run `index` at the same HEAD, render again. Expect `✓ indexed`. This catches a cached indexed state. It must PASS on Task 9's code.
- [ ] Step 3: Implement `HeadIndexEntries`/`AnyIndexEntryExists`, the git key, the fallback and the hit path. Run the new tests. Then check the table test bites:
  - drop `.git/config` from the key, and expect the remote subtest to fail;
  - drop the loose ref, and expect the commit subtest to fail;
  - restore both.
- [ ] Step 4: Run `go test -short -timeout 40m ./internal/cli`, `go test -short ./internal/sem` and `go vet ./...`.
- [ ] Step 5: Measure on the Task 9 transcript, 20 runs each. Keep the change if and only if:
  - warm-hit p50 ≤ 150 ms;
  - append-miss p50 ≤ 300 ms;
  - git-key-miss p50 is no worse than Task 9's 409 ms hit plus the noise between runs.

  Record p95 and cold for information. Write baseline → result → verdict in `docs/perf-ledger.md`. If the change fails, revert the code and keep the ledger entry.
- [ ] Step 6: Commit: `perf(cli): render a warm status line without running git`.

### Task 10: `setup` installs the status line

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

### Task 11: Re-run the eval and report (W5)

- [ ] Step 1: Build the final binary to `graph-eval/tools/eg-final.exe`. Run `perf_run.py` for `final` on both repos, with the sidecar default (no `store` arg needed after Task 3).
- [ ] Step 2: Re-run the eval scenarios (`tools/run_queries.py`-style) with the final binary for the entire column and score them with `tools/score.py`. Accuracy must equal the baseline's: outputs differ only in telemetry.
- [ ] Step 3: Write `docs/perf-ledger.md`'s summary table (baseline vs final per step: wall, RSS, disk) and update the comparison doc.
- [ ] Step 4: Commit: `docs: perf ledger and kernel-scale results`.
