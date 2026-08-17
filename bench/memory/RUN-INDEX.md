# Run index — every complete LoCoMo run behind the published numbers

Extracted directly from each run artifact's `metadata` and `metrics_by_cutoff.top_200.overall`.
Nothing here is transcribed by hand. Regenerate it from a results directory with:

```python
import json, glob
for f in sorted(glob.glob("results/locomo/locomo_results_*.json")):
    d = json.load(open(f)); m = d["metadata"]
    o = (d.get("metrics_by_cutoff", {}).get("top_200", {}) or {}).get("overall", {})
    ec = m.get("env_capture", {}) or {}
    print(m.get("run_id"), str(m.get("timestamp"))[:15], o.get("total"),
          round(o.get("accuracy", 0), 4), o.get("correct"), ec.get("fair_mode"),
          ec.get("asymmetric_settings_active"), (ec.get("env") or {}).get("EG_INGEST_GRANULARITY"))
```

`fair_mode` and `asym` come from the `runmeta` block described in `README.md` §1. **`asym` is `{}`
for every run listed below** — no arm-asymmetric setting was active in any of them.

## Complete runs (n=1540)

Windows are separated by rules. **Only rows inside the same window are orderable** — see
`README.md` §5.

| run id | UTC | arm / config | score | correct | `fair_mode` |
|---|---|---|---|---|---|
| `field_eg_loco` | 08-13 01:05 | entire-graph, default `session` | 92.73 | 1428/1540 | not stamped |
| `field_mem0_loco` | 08-13 02:44 | mem0-OSS, **pre-`top_k` fix** | 87.40 | 1346/1540 | not stamped |
| `field_cognee_loco2` | 08-13 06:15 | cognee, **pre-buffer fix** | 79.09 | 1218/1540 | not stamped |
| `field_sm_loco` | 08-13 11:35 | supermemory | 77.60 | 1195/1540 | not stamped |
| `field_letta_loco` | 08-13 13:52 | letta | 80.58 | 1241/1540 | not stamped |
| `field_cognee_loco2` | 08-13 15:56 | cognee, **post-buffer fix** | 92.86 | 1430/1540 | not stamped |
| `field_mem0_loco` | 08-13 17:55 | mem0-OSS, **post-`top_k` fix** | 93.44 | 1439/1540 | `false`¹ |
| | | | | | |
| `field_eg_fair2` | 08-14 03:15 | entire-graph, default `session` — the drift re-run | 90.52 | 1394/1540 | `true` |
| `field_eg_deep2` | 08-14 03:20 | entire-graph `--deep` | 87.14 | 1342/1540 | `true` |
| | | | | | |
| `plan_f_base` | 08-14 14:08 | entire-graph, default `session` | 92.53 | 1425/1540 | `true` |
| `plan_f_turn` | 08-14 14:09 | entire-graph, `turn` | 92.66 | 1427/1540 | `true` |
| `plan_f_hyb` | 08-14 14:08 | entire-graph, `turn+session` | 94.74 | 1459/1540 | `true` |
| `plan_f_ceil` | 08-14 14:09 | **ORACLE — `EG_FULL_CONTEXT=1`, never publish** | 96.23 | 1482/1540 | **`false`** |
| | | | | | |
| `plan_g_base` | 08-14 14:42 | entire-graph, default `session` | 92.14 | 1419/1540 | `true` |
| `plan_g_mem0` | 08-14 14:42 | mem0-OSS, post-`top_k` fix | **93.77** | 1444/1540 | `true` |
| `plan_g_hyb` | 08-14 14:45 | entire-graph, `turn+session` | **94.68** | 1458/1540 | `true` |
| | | | | | |
| `full_graphify` | 08-14 16:25 | graphify | 87.34 | 1345/1540 | `true` |
| `full_cmm` | 08-14 16:34 | **cmm (patched, Markdown-Section)** | 91.30 | 1406/1540 | `true` |
| | | | | | |
| `mrq_mres` | 08-14 17:41 | **entire-graph, shipped + PR #100** | **93.83** | 1445/1540 | `true` |
| `mrq_base` | 08-14 17:43 | entire-graph, shipped current default | 91.56 | 1410/1540 | `true` |

¹ `field_mem0_loco` at 17:55 was launched without `FAIR_MODE=1` but its `runmeta` block records
`asymmetric_settings_active: {}` — no asymmetric knob was set. The flag governs whether the guard
*hard-exits*; the recorded env is the evidence either way.

**`fair_mode: not stamped`** on the 08-13 rows means those runs predate the `runmeta` capture
landing in the harness (`patches/0003`). Their fairness rests on the code-fingerprint audit in
`FAIR-CONFIG.md` §7 rather than on a stamped flag. Later runs carry the stamp. Treat the stamped
rows as the stronger evidence, and note that `runmeta` exists precisely because reconstructing the
08-13 configurations by hand was the problem that motivated it.

## The `mrq` window — the PR #100 result

`mrq_mres` and `mrq_base` ran on the same corpus in the same window, same answerer, same judge,
same `top_k`, **binary the only variable**. Both n=1540, `drops=0`, `zero-context=0`,
`fair_mode: true`, `asymmetric_settings_active: {}`.

Paired over all 1540 questions (ALL-PAIRS, the pre-registered endpoint):
**91.5584 → 93.8312, +2.2727pp, discordant 46–11, exact McNemar p = 3.3e-06.**

Retrieval-budget usage in the same two runs — the mechanism behind the gain:

| | items returned / question | context chars / question |
|---|---|---|
| `mrq_base` | mean 27.7, median 29, min 7, max 32 | 24,487 |
| `mrq_mres` | mean 198.7, median 200, min 8, max 200 | 56,891 |

## The `plan_g` window

`plan_g_base`, `plan_g_mem0` and `plan_g_hyb` completed within four minutes of one another against
one endpoint. This is the only three-way entire-graph-vs-mem0 comparison in the set that is immune
to the drift documented in `RESULTS.md` §1. Its ordering — hybrid 94.68 > mem0 93.77 > default
92.14 — is a direct measurement, including the part where entire-graph's default configuration
loses to mem0 by 1.62pp.

## Oracle rows

`plan_f_ceil` (96.23, n=1540) and `plan_s_ceil` (94.00, n=200) ran with `EG_FULL_CONTEXT=1` and
`fair_mode: false`. They feed the answerer the whole haystack to bound what perfect retrieval could
buy. **They are not results and must never appear in a comparison table.** They are listed here so
that the number 96.23 is never mistaken for an entire-graph score.

## Incomplete, preflight, and invalid runs

Present in the artifact directories, **not scoreable**, listed so nothing looks hidden:

| run id | n | score | why not scoreable |
|---|---|---|---|
| `plan_s_base` / `plan_s_w2` / `plan_s_turn` | 200 | 86.00 / 86.50 / 86.50 | 200-question sweep, superseded by the `plan_f` / `plan_g` n=1540 runs |
| `plan_s_ceil` | 200 | 94.00 | oracle, `fair_mode: false` |
| `field_eg_loco_fair` / `field_eg_loco_deepfair` | 100 / 211 | 9.00 / 18.96 | `entire-graph` not on `PATH`; every retrieval failed |
| `field_cognee_loco_v2` | 684 | 20.18 | killed mid-run |
| `egopt_deep` / `egopt_sx2c10` | 1334 / 1333 | 0.00 | killed before judging |
| `field_graphiti_loco` | 589 | 70.46 | graphiti never completable (~160h at observed rate) |
| `paired_eg_r1` / `paired_mem0_r1` | 1540 / 762 | 72.66 / 69.03 | paired-harness experiment, different pipeline, not the published spine |
| `mrp_base` / `mrp_mres` | 568 / 228 | 88.56 / 53.51 | earlier partial pass, superseded by the completed `mrq_*` pair |
| `sw_mem0` / `sw_eg_tsmr` | in flight | — | same-window control, still running; see `LOCOMO-COMPARISON.md` §7 |
| `preflight_*`, `*_smoke` | 2–8 | — | smoke tests; never a publishable row |
