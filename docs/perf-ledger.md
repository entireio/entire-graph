# Performance ledger

Keep/revert record for performance changes. Each row states what was measured
before and after, on which repository, and why the change was kept or reverted.
Raw records live in the graph-eval workspace (`results/perf.jsonl`), by label.

| idea | baseline → result | verdict | why |
| --- | --- | --- | --- |
| SQLite sidecar on by default, no-op repair checks store meta instead of loading the store twice (Task 3) | kernel `index` no-op: base (store off) 50.7 s, re-run same day 56.3 / 56.4 s; base-store 104.0 s → 57.7 / 57.5 / 58.0 s, peak RSS 42.0 GB → 15.9 GB. Kernel cold `index`: base 854 s, base-store 1073 s → 1150 s (+35% over base, 44.2 GB peak). orbweaver cold `index` 2.43 s → 3.12 s, no-op 1.02 s → 1.19 s, cold `def --head` 2.39 s (store off) → 2.80 s | keep | No-op is within noise of base and no longer pays the second full decode base-store paid. The cold-index increase is the sidecar build, accepted as the cost of the focused lookup reads (measured in Task 5). |
| Lookup verbs (neighbors, def, impact, explain) answer from a focused sidecar subgraph; an exact miss reloads the full snapshot for the fuzzy ladder; impact's callee and type hops are terminal, so only callers are walked a second hop (Task 5) | kernel `--head` lookups (perf_run targets), base / base-store → w1: `neighbors` 53.4 s, 43.1 GB / 54.0 s, 16.3 GB → 1.14 s, 164 MB; `def` 54.5 s, 46.0 GB / 57.7 s, 18.1 GB → 1.02 s, 158 MB; `impact` 55.2 s, 44.3 GB / 56.4 s, 18.6 GB → 1.15 s, 167 MB. The w1 figures were measured on the final tree, with the entry stamp (row below) included; without it, each focused kernel lookup costs about 0.3 s more (1.4-1.5 s, see the hub table, build v1). orbweaver: `neighbors` 0.84 / 0.77 → 0.74 s, `def` 0.85 / 0.84 → 0.71 s, `impact` 0.79 / 0.85 → 0.73 s. Kernel hubs (table below): `kmalloc`, `file_operations`, `spin_lock` 1.4-1.6 s, 149-175 MB in build v1 (no stamp); the worst found, `impact` on struct `dev` (112k type consumers), 7.5-7.8 s, 986-1059 MB in v1 (second hop from every hop-1 endpoint) → 4.7 s, 604 MB in the final build (terminal hops and stamp). Kernel cold `index` 1147 s (Task 3: 1150 s) | keep | Output is byte-identical to base and base-store on both repos (latency masked), and the equivalence guard (14,398 lookups, store on vs off) passes. Lookups drop from about 55 s and 16-46 GB to about 1 s and under 200 MB. |
| Stat-validated entry digest: the sidecar records the gz entry's size and modification time (ns) beside its digest (meta `entry_stamp`), and a focused lookup takes the recorded digest when both still match instead of hashing the entry (Task 5) | kernel `neighbors --depth 2` on `kmalloc`, `file_operations`, `spin_lock`, three alternating runs each, same sidecar: hashing 1.38-1.44 s (median 1.39 s) → stamp 1.07-1.11 s (median 1.09 s), -0.30 s (-22%). CPU profile before: the hash was 0.28-0.31 s of a 1.43-1.46 s lookup. Cost: one more hash per sidecar write (0.3 s on the kernel, against a 7-8 min sidecar build) | keep | The hash was a fifth of every focused kernel lookup. It is an integrity cache, not a security boundary: every writer renames a fresh file over the entry (new mtime), same-key bytes are deterministic, and `index --force` rewrites both files. The stamp is written only when the entry's bytes hash to the recorded digest and its stamp held across that read; any mismatch hashes as before. No sidecar version bump: the meta key is optional and a sidecar without it is hashed. |
| The full-sidecar read (warm `query --head`, a lookup's fallback) takes the recorded digest when the gz entry's stamp still matches, as the focused read already did (`entryDigestForStore`), and opens the sidecar once. In the same change, neighbors and impact narrow the focused starting set by `--file`/`--kind` (Task 9g) | orbweaver, `perf_run.py ... orbweaver store`, `index-cold` once per build (base 4235d78f 2.65 s, this build 2.62 s), then `query` seven rounds alternating builds: base 2.836-2.894 s (median 2.858 s) → this build 2.833-2.922 s (median 2.853 s). The orbweaver entry is 784 KB, so its hash is a few milliseconds, inside the 0.06 s spread. Output identical apart from the latency line. Not measured on the kernel (no kernel jobs in this task); there the same hash is 0.28-0.31 s on the 400 MB entry (stamp row above). `index --head` on an unchanged tree does not speed up: its hit no longer hashes, but the repair step's meta check hashes the entry once instead | keep, unproven at orbweaver scale | The change removes one whole-entry hash per warm full read, the cost the stamp row measured at a fifth of a kernel lookup; orbweaver's entry is too small to show it above noise, so the gain rests on that earlier kernel measurement of the same hash. Safety is unchanged: a stamp that does not match hashes as before (`TestFullLoadHashesAnEntryWhoseStampChanged`). The `--file`/`--kind` narrowing only shrinks what a filtered kernel lookup reads (review finding #2); orbweaver's perf targets pass no `--file`, so it is not measured here. |

### Task 5: kernel hub closure sizes (Step 2b)

Focused lookups on the Linux kernel v5 sidecar, one run each, wall time /
peak tree RSS (MiB, `measure.py`) / relations retained in the focused
snapshot. Build v1 is the verb wiring alone: every lookup hashes the gz
entry, and impact's second hop expands from every hop-1 endpoint. Build final
is both Task 5 commits: impact's callee and type hops are terminal, and the
entry stamp replaces the hash. `kmalloc`, `spin_lock` and `file_operations`
are ambiguous (7, 17 and 2 definitions), so their answer is the definition
list; adding `--file` loads the same closure (1.44-1.51 s in v1). The other
hubs are `--file`-disambiguated: the most-called function (`pr_err`, 11k
callers), a 6.8k-caller allocator (`devm_kzalloc`), and the structs with the
most type consumers (`device` 62k, `dev` 112k). `impact` runs at its default
`--depth 2`; `neighbors` runs `--depth 2` in both directions.

| lookup | v1 | final |
| --- | --- | --- |
| `impact kmalloc` | 1.59 s / 149 / 62 | 1.08 s / 164 / 59 |
| `neighbors kmalloc` | 1.50 s / 158 / 6 | 1.07 s / 162 / 6 |
| `impact file_operations` | 1.52 s / 175 / 49 | 1.09 s / 163 / 4 |
| `neighbors file_operations` | 1.55 s / 175 / 0 | 1.08 s / 169 / 0 |
| `impact spin_lock` | 1.51 s / 150 / 33 | 1.11 s / 160 / 29 |
| `neighbors spin_lock` | 1.52 s / 161 / 9 | 1.09 s / 161 / 9 |
| `impact devm_kzalloc` | 4.60 s / 550 / 11,281 | 3.69 s / 549 / 11,124 |
| `neighbors devm_kzalloc` | 1.70 s / 191 / 6,825 | 1.32 s / 181 / 6,825 |
| `impact pr_err` | 7.99 s / 926 / 40,999 | 7.39 s / 908 / 33,140 |
| `neighbors pr_err` | 1.93 s / 178 / 11,148 | 1.43 s / 192 / 11,148 |
| `impact device` | 5.09 s / 585 / 165,448 | 2.81 s / 348 / 62,782 |
| `neighbors device` | 1.69 s / 171 / 22 | 1.25 s / 164 / 22 |
| `impact dev` | 7.84 s / 986 / 285,447 (rerun 7.51 s / 1059) | 4.67 s / 604 / 112,640 |
| `neighbors dev` | 1.96 s / 168 / 37 | 1.53 s / 165 / 37 |

`impact dev` over 1 GiB in v1 is what narrowed impact to terminal callee and
type hops. Of `impact pr_err`'s 7.4 s, 4.9 s is the call-site source reads
for its callers, which the full load does too.
