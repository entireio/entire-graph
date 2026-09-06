# Retained query correctness protocol — 1c0b8e24

This is a focused correctness replay of exactly three retained Kubernetes
cache-off/cache-on search pairs. It is not a performance campaign, score, or
tuning step. The three pairs are one trial-0 request for each profile:
`syntax-only`, `fast`, and `full`.

The retained archive contains 55 repeated pairs over those same three profile
paths: 20 syntax-only, 20 fast, and 15 full. This preparation intentionally
selects one representative pair per path. There is no repetition stage in
this protocol. The remaining 52 historical pairs stay retained evidence and
are not silently treated as resolved.

## Frozen identity and inputs

- Source commit: `1c0b8e24f3b6a5bb880e7b306c1c74d818614782`.
- Evaluation binary SHA-256:
  `b51728ed2c0840081c0921e6ec29931d2b10dce749802fb4f6fb341a60852e37`.
- Repository: `kubernetes-kubernetes`.
- Repository path: `/opt/p1/corpus/kubernetes-kubernetes`.
- Captured source digest:
  `d7a25ec35c9720efead0ac3f3dccc493385f6f4bc8c42d2f0313e2afbc9e4db4`.
- Provider version: `p1-corpus-20260905`.
- Operation: `SearchRepository`, query and `top_k=8` are identical in both
  arms, with `max_indexed_files=0`.
- Compiler is off and ranking is current. `mutation_id` and `scenario` are
  both `cold`; trial is `0`.
- Each arm has a separate cache directory. No cache directory is shared
  between profiles or arms.

The six arm request files are listed in `manifest.json`. They preserve the
request schema used by the retained campaign and differ only in profile and
cache arm/path.

## Pair procedure and stop rule

Before each pair, verify the binary SHA, source digest, repository identity,
and clean corpus identity. Run the OFF arm first, then the ON arm only if OFF
produces a well-formed observation. Use a hard 120-second bound per arm and
retain process status, output, logs, and any partial observation.

The first malformed output, identity drift, timeout, process failure, missing
digest, or semantic/partial mismatch pauses all remaining profiles. Do not
retry, expand to the other retained trials, tune selection, or start a
performance campaign from this preparation. No latency, RSS, cache, or speed
claim is scored here.

## Correctness contract

For each OFF/ON pair, require exact equality of:

1. current binary and source/policy identity;
2. semantic result membership and canonical `semantic_digest`;
3. warnings and warning digest;
4. the complete partial-failure membership, details, count, and digest; and
5. completeness fields and any result ordering promised by the operation.

Partial results are retained verbatim and are never dropped, promoted to
success, or counted as full-coverage evidence. Equal partial outputs provide
parity diagnosis only; they do not close the separate partial-coverage or
release gate. A missing or unequal partial set is a correctness failure for
this focused replay.

The old retained pairs were unequal in all three profile paths. Their old
digests are provenance only and are not expected corrected outputs:

| profile | retained OFF digest | retained ON digest | historical trials |
| --- | --- | --- | --- |
| syntax-only | `f137b6d3…` | `74d177f3…` | 0–19 |
| fast | `dd72c002…` | `4c200037…` | 0–19 |
| full | `abcd5c59…` | `2efa0c6e…` | 0–14 |

## Retained provenance

The source rows are in `../paused-raw/worker-1.tar.gz`,
`worker-2.tar.gz`, and `worker-3.tar.gz`, under
`results/campaign.ndjson`; each archive also retains its campaign manifest
and `results/request/request.json`. The original 55-pair selection is the
intersection of `repository=kubernetes-kubernetes`,
`verb=search`, `scenario=cold`, `mutation_id=cold`, and the profile-specific
trial ranges above, with one row for each `reuse=false/true` arm.

This directory contains preparation metadata only. Execution, collection,
measurement, and campaign admission remain outside this protocol.
