# Reproduction index

Implementation-first source commit: `88dd1dc95a996999ae4e456879b6dd86d8027f71`.

Current correctness evidence: [source freeze](evidence/review-source-freeze.json),
[repository log](evidence/review-mise-check.txt), [Linux source](evidence/review-linux-source.json),
[Linux log](evidence/review-linux-correctness.txt), [ordinary query responses](evidence/review-linux-queries.json),
and [combined feature responses](evidence/review-linux-combination.json).
The [initial Linux failure](evidence/review-linux-correctness-initial.txt) used an
older Git than the documented 2.36 minimum; [prerequisite upgrade](evidence/review-linux-git.json)
records the correction. These are correctness runs, not comparative campaigns. Both passed; the
[final result manifest](evidence/review-final-validation.json) binds their source,
tools, logs and local binary. The VM is [verified deallocated](evidence/review-azure-final-state.json).

Replay from that commit using `mise run check`; the retained correctness runners
in `probes/run_review_checks.py` and `probes/run_review_linux.py` preserve logs and
source identities. The Linux runner uses the existing task-owned validation VM
and its explicit installed tool paths.

Historical product source commit: `0038ef70ca5e829c150a977d8021ee9fe9c7103a`.
[Earlier source manifest](evidence/final-source-freeze-v2.json) pins every source,
script and fixture used by the earlier check. Earlier experiment manifests identify
their own source/binary, rather than claiming they ran this later revision.

| Evidence | Inputs/settings and replay |
|---|---|
| Required repository check | [Final log](evidence/check-integration-final-v2.txt); `mise run check` from product commit. [Initial version-golden failure](evidence/check-integration-final.txt) retained. |
| P1 paired development | [Raw](evidence/extraction-paired-v2.ndjson), [summary](evidence/extraction-paired-v2-summary.json); frozen29520508, `TestExtractionEvaluation` with exclusive output path; [scorer](probes/summarize_extraction.py). Three generated sizes/profiles/scenarios,30 alternating pairs each; no cold-process/RSS claim. |
| P1 measured raw family | [Raw](evidence/relation-profile-v1.ndjson), [manifest](evidence/relation-profile-v1-manifest.json), [analysis](p1-relation-cost-audit.md), [scorer](probes/summarize_relation_profile.py).30 samples per language/probe, separate semantic equality before timing. |
| P2 pinned live backend | [Validation](compiler-validation.md), [Linux race](evidence/linux-p2-v4.txt), source manifests and ADR0034/0036. Go1.26.1, goplsv0.20.0, Ubuntu22.04, Bubblewrap0.6.1, Standard_D4s_v5; no public VM IP. |
| P2 fixture quality | [Preregistered cases](compiler-quality-manifest.md), [raw](evidence/compiler-quality-v1.json), [summary](evidence/compiler-quality-v1-summary.json), [scorer](probes/summarize_compiler_quality.py). Exact site/category denominators; author/compiler-checked labels. |
| Final combinations | [Raw CLI responses](evidence/advantage-combination-v2.json), [Linux log](evidence/linux-combination-v2.txt), [source hashes](evidence/linux-source-combination-v2.json). Test executable SHA256 `d7401051105f003a611f04393b544a9a5b7b13e089d499cfca4227e1af6f7f7d`. |
| P3 paths | [Validation](p3-validation.md), [race](evidence/p3-focused.txt), [isolated traversal samples](evidence/p3-stress-benchmark.txt), [source](evidence/p3-source-manifest.json). Source-authored labels and seed30905 proof reconstruction, not realistic change-quality adjudication. |
| P4 retrieval | GraphMark branch `codex/graph-advantage-evaluation`, commit `c22eda1`, directory `advantage-ranking-20260905`. Manifest/tasks/source hashes, CLI120 observations, API180 observations, script/binary hashes, raw failures and scoring included there. [Product gate review](p4-review.md). |

[Scoring reproduction](evidence/scoring-reproduction.txt) confirms retained P1 and
P4 summaries reproduce exactly without re-querying or tuning. Failed runs and
corrected fixture assumptions remain in the evidence directory. Timing artifacts
state warm-process/host-contention/byte-budget limits; none is a release advantage
claim. The earlier Azure shutdown is [retained](evidence/azure-final-state.json); the
implementation-first phase records its own final VM state.
