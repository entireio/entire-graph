# Store-native index — design

Date: 2026-11-04 · Status: approved direction; spec reviewed (correctness,
consistency, simplicity), findings folded
Decision record: [ADR 0005](../../adr/0005-store-native-index.md)
Replaces: Tasks 6–7 (W2, W3) of
[the kernel-scale plan](../plans/2026-11-03-kernel-scale-perf-and-statusline.md).
Tasks 1–5 of that plan are done. Tasks 8–10 (status line) proceed, with one
dependency noted under [Plan of record](#plan-of-record).

## Why

The kernel-scale pass moved `neighbors`/`def`/`impact` onto the sqlite store,
which was a sidecar of the gz entry at the time.

| Linux kernel, 16-core Windows host | entire graph | build |
|---|---|---|
| `neighbors` / `def` / `impact` `--head` | 53–55 s at 42–45 GiB (store off), 54–58 s at 16–18 GiB (store on) → 1.02–1.15 s, ~160 MiB | base, base-store → w1 (Task 5) |
| cold `index --head --profile full` | 1147 s, 43.0 GiB peak RSS | w1 |
| no-op `index` | 57.7 s, 15.5 GiB | t3 (Task 3) |
| `query --head --profile full` | 223 s | base |

Run labels are `perf-<build>-linux-<step>` in the eval workspace's `perf.jsonl`.
The perf ledger names that workspace: [`docs/perf-ledger.md`](../../perf-ledger.md).
It lives outside this repository. The kernel snapshot holds 82,762 files,
5.41 M symbols and 10.93 M relations.

For scale, graft (a tree-sitter graph tool, run on the same host and clone):
- **Cold build:** 264 s at 4.8 GiB peak, with `--workers auto` and a 16 GiB
  Node heap. At the default heap it aborts.
- **No-op build:** 220 s.
- **Ranked "ask" query:** 72–102 s at 7–9 GiB, which needs the 16 GiB heap.

Graft indexes 65,797 of the files.

### Where the cold build's time goes

A CPU and heap profile of one cold build (`prof-t5-linux-index-cold`, the same
commit built with `-tags pprof`, 1090 s, 43.4 GiB) shows four costs.

**Garbage collection is ~46 % of real CPU.** The build holds the whole graph
as pointer-rich Go values: 16.9 GiB live at the peak sample, about 1 KB per
record. The largest holders:
- the `forEachRelation` indexes, 7.7 GiB, which copy `SymbolRecord` by value;
- the snapshot slices, 3.9 GiB;
- `entitySymbols`, 2.5 GiB;
- `recordIndexes`, 2.1 GiB.

**Regex backtracking is ~19 % of real CPU.**
- The `mask*` C/C++ preprocessing family (`maskCUnsupportedSyntax` and its
  callees, run before every C/C++ parse) takes ~420 s.
- Return-flow and assignment-flow detection (`returnFlowCalls`,
  `assignmentFlowEvents`) takes ~325 s.
- `relationMatchIndices` takes 148 s.

**About 3.7 of 16 cores are busy.**
- Parse and per-file resolution workers are capped at 8
  (`provider_parallel.go:29-37`).
- Every committed-mode blob read goes through one `git cat-file --batch`
  process behind a mutex (`gitutil/git.go:1714`).
- Resolution runs long serial passes (`provider.go:4996-5210`, `6714-6829`).

**Writing is serial and done twice.**
1. The gz snapshot is encoded and hashed.
2. The store is rebuilt from memory: 214 s in total, 177 s of it in sqlite
   `stmt.exec`.
3. The gz is hashed again.

Tree-sitter itself is ~5 % (about 215 s of C time). The raw profile shows
`runtime.cgocall` at 21 %, but most of that is `ReadFile` blocked on subprocess
pipes. The Windows profiler counts blocked syscalls as CPU.

## Targets

Kernel, same host. **Goal** is what the work aims for. **Floor** is what the
finished work must reach to ship. Individual milestones are kept or reverted
by the rule in [Milestones](#milestones), not by these floors.

| | goal | floor |
|---|---|---|
| cold `index --head --profile full` | ≤ 120 s, ≤ 4 GiB peak RSS | ≤ 240 s, ≤ 6 GiB |
| no-op `index` | ≤ 5 s | ≤ 5 s |
| `index --head` after a one-function body edit in one `.c` file, committed | ≤ 10 s | ≤ 60 s |
| `def` without `--head` (working tree), nothing edited since the last build | ≤ 10 s | ≤ 60 s |
| `neighbors` / `def` / `impact` `--head` | ≤ 1 s | no regression beyond noise (today 1.02–1.15 s) |
| warm `query --head` | ≤ 2 s | ≤ 5 s |

Orbweaver is measured at every milestone to catch small-repo regressions. The
eval harness gains steps for the edit row and the working-tree row, and a mode
that leaves `ENTIRE_GRAPH_SEARCH_STORE` unset by default.

## Invariants

These hold at every milestone. The [Testing](#testing) section maps each one to
its check.

**Output.** The output of `index`, `neighbors`, `def`, `impact`, `explain`,
`query`, `context`, `store`, `snapshot`, `symbols` and `edges` is byte-identical,
in every format, before and after this work. It includes record order, the
relation sort order (`provider.go:1124`), header `Stats` and `Completeness`, and
partial-failure lists. Three things are exempt:
- latency and cache-telemetry fields;
- the descriptor's `entry_digest` value, which is redefined;
- the warning and documentation text this design makes false, listed under
  [Contract changes](#contract-changes).

The relation sort order is the string concatenation `Type+FromID+ToID`, not
tuple order.

**Emission order.** Duplicate relations resolve first-wins, the `DATA_FLOWS`
unmerged-evidence count is unchanged (`provider.go:1584-1604`), and a
source-located external record wins over a bare one
(`mergeExternalRecord`, `provider.go:13629`). All three depend on emission
order:
- pass order first: Go routes, then DEFINES/CONTAINS, then the per-file scan,
  then OVERRIDES and the later passes through TESTS and SIMILAR_TO
  (`provider.go:5285-6829`);
- then file order;
- then order within a file.

Any parallel stage reproduces that order.

**Contracts and safety.**
- Schema `1.x` is frozen and additive-only. No egress.
- Symbol IDs do not change. `IdentityRevision` bumps only if a parser change
  re-keys symbols.
- Indexing an unaudited repository runs no Git conversion filters or
  repository-selected programs
  ([ADR 0004](../../adr/0004-working-tree-cache-security-boundary.md)).
  Working-tree reads stay confined to the checkout (`os.OpenRoot` with symlink
  refusal, `provider.go:13934-14000`).
- Partial failures are never dropped. A failed, stopped or truncated result is
  never persisted as complete (`refuseTruncatedSnapshot`,
  `E_ANALYSIS_BUDGET_EXCEEDED`).
- The store contract stays at `contract_version: 1`. That covers the
  descriptor shape, the `brain_*_v1` views, atomic publication and the
  `meta.entry_digest` check.

## Architecture

```
 files ──► [1 extract] ──► FileFacts ──► [2 resolve] ──► relations ──► [3 store]
            per file,       cached by     global, over     compact,      sqlite,
            parallel        blob id       a compact        sorted        streamed,
                                          symbol table                   only on-disk form
```

### Stage 1 — extract: per-file facts

`FileFacts` holds everything later stages need from one file.

**Contents:**
- the `FileRecord`, including the fields extraction computes from content:
  `Blob` (the sha256 of the bytes, which is not the git blob id), `Bytes` and
  `Lines`. A committed-mode hit does not read the file, so these must come from
  the fact;
- the symbols from `entitySymbols` and `syntheticBoundarySymbols`, with the
  parser-private fields the cache keeps today in side maps: byte ranges,
  parameter names, signature type text, `bodyless`, name lines, local IDs,
  C++ owners and C linkage;
- every content-derived input that resolution reads today.

**Rule.** Stage 2 never reads file content; whatever it would derive from
content is a fact. The first step of M4 enumerates every content read in
resolution and moves each derivation into `FileFacts`. The known readers are:
- `resolveFileRelations` (call sites, imports, type and field references,
  data-flow events);
- `phpDocblockReturnTypes`;
- the scope parses in the relation phase (`js_scopes.go:150`, Python bare
  scopes), with their own partial failures;
- the similarity signatures (`similarityRelations`, `similarity.go:71`);
- RESOURCE_DEPENDS_ON and CONFIGURES (`provider.go:6800-6815`);
- manifest contents (`go.mod`, `package.json`);
- JS/TS re-export tables and namespaces;
- Go route constants;
- F# module paths.

A derivation that cannot be expressed per file keeps its content read. It is
listed in the plan and measured.

**Purity.** `FileFacts` is a pure function of its key. Anything that needs more
than one file is computed in stage 2:
- the registration census;
- `Aliases`;
- cross-file container parenting;
- partial-type canonical IDs;
- route pairing;
- re-export chain resolution.

**Key.** The key covers every input that shapes a fact:
- **File identity:** the path and the file's **git blob id**.
  - Committed mode takes the blob id from `ls-tree`, so a cache hit reads no
    file.
  - Working-tree mode computes it in Go over the raw bytes read (the
    `blob <len>\0` header, in the repository's object format). No Git program
    runs. Detecting the object format is new code, and the format is part of
    the key.
  - A file whose working-tree bytes differ from its blob (a filter, a line
    ending conversion) gets a different id and misses, which is correct.
  - Go's sha1 has no collision detection. So a working-tree hit is also checked
    against the stored `FileRecord.Blob`, the sha256 of the bytes just read. A
    crafted sha1 collision therefore misses instead of serving another
    content's facts.
- **Parse options:** the profile, max-parse-bytes, and the Go build context
  (`build.Default` GOOS, GOARCH, CGO; `gobuild.go:43-110`).
- **Parser identity:** `IdentityRevision`, the provider version, and a build
  fingerprint (the sha256 of the running executable, computed lazily the first
  time a process opens the fact cache). The fingerprint means a changed binary
  never reads facts from an older one, so the format needs no hand-bumped
  constant. Lookups served from the store never compute it.
- **Repository key:** symbol IDs are stored with it factored out and
  re-prefixed at read time. Unlike `rebindCachedSearchSnapshot`, which rewrites
  only `local/` keys, this covers `gh/` keys too.

**Not cached.** These results are recomputed on every run:
- parse timeouts (`E_PARSE_TIMEOUT` comes from a 5 s wall-clock budget,
  `parser.go:140`);
- results cut short by a stop or a budget (`provider_parallel.go:107,120,253,301`);
- oversize files and read failures.

**Reading.**
- Committed mode uses a pool of `git cat-file --batch` processes, one per
  worker, replacing the single mutex-guarded process. It reads by object name,
  so no conversion filters run.
- Working-tree mode reads raw bytes through the confined root, as today.
- Workers default to `GOMAXPROCS`, without today's cap of 8. Results still
  reduce in file order.

**Regex hot paths.** The `mask*` C/C++ family and the data-flow detectors become
single-pass byte scanners. A scanner replaces a regex only where a differential
check shows identical masked bytes or identical events, on all fixtures (every
language the detector runs on), orbweaver, and a fixed kernel sample. Otherwise
the regex stays.

### Stage 2 — resolve: compact global graph

Resolution keeps its logic, moved from by-value records onto a compact table.

**Symbols and strings:**
- symbols have dense `int32` indexes;
- names, qualified names, paths and kinds are interned once;
- each index in `forEachRelation` (`provider.go:4996-5210`) becomes an
  `[]int32` keyed by interned string.

**Container IDs.** A symbol keeps two container IDs: the one parsed from its
file, and the one after cross-file and partial-type resolution. That is because
`typeLikeByShortName` is built from values copied before the in-place rewrite
(`provider.go:5030-5070`), and emitted records keep the earlier value. Each
index reads the same state it reads today.

**Relations.** Relations are compact tuples (type, from, to, payload). Payload
fields (evidence, reason, resolution, warning codes) move to a spill file only
if the M2 heap profile still shows them as a top holder after interning.

**Parallelism.** Per-file resolution already runs in parallel, but capped at 8.
It runs on all cores against the read-only table. Each serial pass is either:
- partitioned by file, when the 1-vs-N property shows the output is unchanged;
  or
- kept serial and measured.

Only passes that matter to the targets are restructured.

**Order.** The final sort runs over the compact tuples. Its comparator
reproduces `Type+FromID+ToID` concatenation order exactly, including the
repository-key prefix, without building strings. Deduplication keeps its
64-bit hash set and first-wins rule.

### Stage 3 — store: sqlite as the only on-disk form

Stage 3 writes the store from stage 2's sorted output. It keeps today's
publication method (`search_store.go:177-803`):
- prepared statements in large transactions;
- `journal_mode=OFF` and `synchronous=OFF`;
- write to a temp file, then rename.

What changes:
- **The gz snapshot is retired.** A full snapshot load reads the store.
- **Row order.** Relations go in in final order, so `rowid` order stays
  snapshot order (`search_store_focus.go:325,338`).
- **Insert cost.** The record-JSON columns are reduced to what the
  `brain_*_v1` views project. Secondary indexes are built after load.
- **Selective entries.** Selective (`OnlyFiles`) entries, written as gz today
  (`search_cache.go:547`), are written by the same store writer.

**Key and digest.** The cache key is unchanged (ADR 0002), and the store file
is named by it. `meta.entry_digest` becomes a SHA-256 over the record stream as
written. It is deterministic, so two builds of the same inputs publish the same
digest; the 1-vs-N check relies on this. Its contract role is unchanged:
consumers compare the descriptor's value with the one on their connection.
This removes:
- the entry stamp;
- the digest memo;
- the stamp-trust and repair-from-gz paths (`search_store.go:804-955`).

**No-op `index`.** A no-op `index` (57.7 s today) opens the store for the key
and reads the header from `meta`. It decodes only the `files` table, to refresh
and recount co-change relations in memory, as the load path does today
(`search_cache.go:1511-1546`). The store is not rewritten. `--report` still
needs a full load.

**Failure and opt-out.**
- **Write failure.** A store that cannot be written (a full disk, for example)
  leaves `index` successful. The result carries `W_SEARCH_STORE_UNAVAILABLE`,
  the answer is served from memory, and nothing is cached.
- **Opt-out variable.** With no second on-disk form, `ENTIRE_GRAPH_SEARCH_STORE`
  can no longer choose a read path. It is retired: documented, and ignored.

`FILE_CHANGES_WITH` comes from history, not from the tree. It is stored with
the build and refreshed from `git log` on every full-profile load.

### Incremental re-index

**M4.** M4 makes the floor reachable without incremental resolution. An edit
re-extracts only files whose fact key changed. Stage 2 and stage 3 then run in
full over cached facts. This meets the 60 s floor if M2 and M3 bring stage 2
plus the store write under about 50 s. The same mechanism serves working-tree
mode: a working-tree build reuses facts for every file whose raw bytes hash to
a cached blob id. Working-tree runs therefore write to disk where today they
write nothing. Facts derived from uncommitted bytes are stored in the fact
cache and kept under its size-bounded retention. The whole-snapshot cache
stays bypassed, as ADR 0004 requires.

**M5.** The 10 s goal needs incremental resolution. That gets its own design,
written after M4 is measured. The reviews of this spec set its constraints:
- **No in-place edits.** A published store is immutable (`immutable=1`,
  atomic rename), and row order must stay snapshot order. Incremental output
  is a new store. It is produced by an ordered merge of the previous store with
  the re-emitted rows, or by a layered design. The merge cost on a 16 M-row
  store is the first thing to measure.
- **Emission-order outcomes.** First-wins dedupe, the `DATA_FLOWS` unmerged
  count, external-record selection and `Aliases` all depend on corpus-wide
  emission order. Today only the winner is kept. Either every emission is
  persisted with its (pass, file, sequence) rank, or these are recomputed
  globally on each run. Symbol rows are in scope too, not only relations.
- **Dependencies.** Dependencies are tracked on lookups (hits and misses), on
  manifest content, and on signatures and linkage, not only on defined names.
  SIMILAR_TO (which has global caps), TESTS, the registration census and route
  pairing are recomputed in full until shown local.

### Query

`query` loads the whole snapshot even when warm (`search.go:1226`). It
preselects files with `git grep` or an inventory scan (`search.go:2556`,
`3462`), derives a selective view in memory (`search_cache.go:982`), and scores
candidates with BM25. Document frequencies are computed per query over the
preselected files (`search.go:1590-1620`).

M6 starts with a measured breakdown of a warm kernel query. Its first change
reads the selective view from the store for the preselected files only.
Persisted per-file token postings are added only if the breakdown shows scoring
still keeps query above the floor. In that case, per-query statistics are
still summed over the preselected files, so scores do not change.

## Contract changes

**`docs/store-contract.md` is rewritten where it depends on the gz entry (M3):**
- "write it beside the gz entry" (:10-11);
- "rebuilds it from the entry" (:13-14);
- the opt-out variable's two meanings (:9-13, now retired);
- "lookups fall back to the full snapshot load" (:16-19);
- "The gz snapshot entries are never removed … without reparsing" (:32-34),
  which becomes "an evicted store is rebuilt from cached facts, parsing only
  files whose facts were evicted too" (M4);
- "A store is a sidecar of one search snapshot entry" (:56);
- the stale "format is v4" (:50).

`entry_digest` keeps its name, type and check, so `contract_version` stays 1.

**ADRs.**
- ADR 0005 amends ADR 0002. It lifts two of ADR 0002's non-goals: "no per-file
  content-hash caching", and "no change to working-tree mode" (working-tree
  facts are reused). ADR 0002's key-totality decision is unchanged.
- ADR 0005 also amends ADR 0004's consequence that repeated working-tree
  queries rebuild. ADR 0004's decision and its whole-snapshot bypass are
  unchanged.
- On acceptance, both ADRs' Status lines get the amendment note, as ADR 0002
  and ADR 0003 show for earlier amendments.

**Other text this design makes false, each updated in the milestone that
changes it:**
- AGENTS.md: "always rebuilt and never cached" (:37), and its store section
  (:94-98);
- `docs/operations.md:136-145, 181`;
- `docs/agents.md`: working-tree queries bypass the cache;
- `docs/brain-and-graph-boundaries.md:50-53`;
- the `W_WORKTREE_SNAPSHOT` warning text ("working-tree reads are never
  cached");
- CHANGELOG entries for:
  - the cold first run after upgrade;
  - the fact cache;
  - the retired variable;
  - working-tree speed.

**Versions.**
- `searchSnapshotCacheVersion` bumps at M3, when the gz format is retired.
- `searchStoreVersion` bumps at every store schema change: v6 at M3, then at
  M5 and M6 if they add tables.
- The fact key includes the build fingerprint, so a facts format change needs
  no constant.

**New on-disk state.** The fact cache is one sqlite file per cache directory.
Each row holds one `FileFacts` record, keyed as above, JSON-encoded with the
repository's existing encoder and flate-compressed. It is bounded by total size
and evicted least-recently-used. The default bound is three times the facts of
the newest build, so a build never evicts its own facts. Stores keep their
count-based retention.

## Milestones

Each milestone ships on its own, with a perf-ledger row (baseline → result →
verdict). It is kept only if its numbers beat run-to-run noise, and every
invariant check passes. Otherwise it is reverted and the attempt is logged.

| | scope | expected effect |
|---|---|---|
| **M1** | regex → scanners on the profiled hot paths; worker cap removed; `cat-file` pool | less CPU on C/C++ preprocessing and data flow; more cores busy |
| **M2** | compact table and indexes, string interning, concatenation-order comparator; spill only if the profile still needs it | GC share and peak RSS fall |
| **M3** | store as the only on-disk form; gz retired; contract doc and AGENTS.md updated; ADR 0005 accepted | no-op ≤ 5 s; removes the gz encode, double hash and from-memory rebuild. The sqlite insert cost (177 s today) must also fall for the cold goal |
| **M4** | `FileFacts` + fact cache; working-tree reuse (its own ledger row) | edit floor ≤ 60 s; working-tree floor ≤ 60 s |
| **M5** | incremental resolution, per its own design | edit goal ≤ 10 s |
| **M6** | query: breakdown, then the store-backed selective view, then postings if needed | warm query ≤ 5 s floor, ≤ 2 s goal |

M1 and M2 come before M4 introduces `FileFacts`, so the stage boundaries are
drawn on a pipeline that has already been cut down.

**Re-plan trigger.** If cold index is still above the 240 s floor after M3,
stop tuning. Write a short proposal for a resolution-core or store-writer
rewrite and get approval before continuing. One option is a columnar symbol
store with SQL-side joins for the global passes. Each later milestone applies
the same rule to its own floor.

## Testing

Most checks are differential: they compare two runs and need no hand-written
expectation. Any new non-differential test uses hand-derived literal
expectations, per the plan's global constraints. Every test:
- names the break it catches;
- asserts only at public boundaries: CLI output, files written, exit status,
  and store rows read through the `brain_*_v1` views.

Expensive variants run behind an environment variable, as the Task 2 guard
does (`internal/cli/focused_equivalence_test.go`).

1. **Output identical to the base build** (output invariant).
   - The existing golden fixtures stay authoritative.
   - The lookup equivalence guard stays.
   - The eval harness diffs every verb's output against `perf-base` on
     orbweaver and the kernel at every milestone.
   - The existing store-contract tests (descriptor, views, digest check) and
     the truncation-refusal tests run against the new writer.
2. **Worker count does not matter** (emission order). On every guard fixture,
   a 1-worker build and an N-worker build give byte-identical results on three
   checks:
   - `index` and lookup output;
   - store rows in `rowid` order, read through the views;
   - `entry_digest`.
3. **Fact reuse is exact** (purity and key totality). Warm the fact cache on
   one tree and option set, then build a second tree warm and cold and compare
   outputs. Vary one input at a time:
   - an edit to another file that this file's facts must not depend on;
   - a renamed file;
   - the profile;
   - max-parse-bytes;
   - GOOS.

   A fact that reads a global, or a key that misses an input, shows up as a
   warm/cold difference.
4. **Edits re-index exactly** (M4). On a fixture repo, apply a seeded sequence
   of committed edits:
   - body edits;
   - renames;
   - added and removed symbols;
   - a moved method;
   - a changed import;
   - an added override;
   - a delete.

   After each step, the warm `index` and lookups equal a cold build of the
   same tree. M5's design extends this to every language family that has a
   cross-file pass.
5. **Working-tree reuse runs no repository programs** (ADR 0004).
   - Configure a filter that writes a marker file when it runs, using the
     pattern in `internal/sem/search_cache_worktree_test.go:40-100`. Build in working-tree
     mode and assert the marker is absent.
   - After an uncommitted edit, working-tree output equals a fresh working-tree
     build.
6. **Scanners match regexes** (M1). Each replaced regex and its scanner agree
   on every fixture file in the languages the detector runs on, and on a fixed
   kernel sample.
7. **Query is identical** (M6). `query` from the store-backed selective view
   equals the full-load path. This carries the old plan's planned query guard.
8. **The comparator agrees with the strings it replaces** (M2). The
   concatenation-order comparator agrees with comparing the joined strings on
   generated IDs. The generator includes IDs where one is a prefix of another.

## Plan of record

- **Old plan, Tasks 6–7.** They are superseded by this design, and both old
  documents get a status line saying so. Task 6's comparator property moves to
  test 8. Its `GOMEMLIMIT` note moves to M2.
- **Old plan, Task 9.** The status line's `HeadIndexed` reads gz entry paths.
  M3 changes it to read the store.
- **Old plan, Task 11.** The eval re-run runs after M3 and again at the end.

## Non-goals

- Accuracy changes, which are a separate follow-up pass:
  - C macro parse failures;
  - function-pointer refs;
  - Rust path calls;
  - impact closures;
  - tests labeled as production callers.
- A daemon, watch mode or file-system notifications.
- New output fields or commands.
- The records cache behind `snapshot`/`symbols`/`edges`
  (`provider-records-v24`).
- Replacing tree-sitter, or sqlite as the store engine. This is reconsidered
  only at the re-plan trigger.

## Risks

- **sqlite write throughput.** The cold goal of 120 s leaves room for about
  30 s of store writing, against 177 s of inserts today. Fewer JSON columns,
  larger batches and deferred indexes may not be enough. Writing from
  parallel encoders into one writer is the next lever. If that fails, this is
  the re-plan trigger.
- **Stage 2 content reads.** If any derivation in resolution cannot become a
  fact, an edit still re-reads that content, and the M4 floor depends on its
  size.
- **Byte identity under parallel resolution.** Passes stay serial until the
  1-vs-N check passes for them.
- **M5.** It has the most new mechanism. Its own design decides whether the
  10 s goal is reachable within the store contract.
