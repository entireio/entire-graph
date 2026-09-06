# Historical ledger checkpoints

Superseded by ledger.md. Text below is retained checkpoint history; phrases
such as current, pending or latest describe their original checkpoint only.

Final validation at the original implementation checkpoint: source commit
`88dd1dc9` passed the complete repository check and pinned Linux correctness.
The initial Linux Git-version prerequisite failure is retained; the upgraded
run passed without product changes. The validation VM and the P1 workers were
deallocated after correctness/diagnostic work. Later source and evidence
commits are recorded separately below and do not inherit that checkpoint's
verification. No comparative campaign ran during implementation. No merge was
performed.

P1 campaign sources: the user-approved pinned chi, Kubernetes, Zod and Requests
repositories are evaluation inputs only, alongside frozen Entire source and an
independent synthetic fixture. See `corpus/corpus-manifest.json` for revisions,
licenses, selected paths and fixture origins. Official OpenAI model guidance
was consulted only for the requested cheaper subagent selection.

P1 execution monitoring: heartbeat `monitor-p1-corpus-campaign` checks every
15 minutes. Three Luna subagents prepared the corpus, harness and protocol.
The earlier `039d213b` and `dc0ddce7` checks are historical. Current
verification is summarized at the top of this ledger; no comparative gate
pass is claimed from a paused campaign.

P1 baseline freeze: `2840a152`, authoritative `frozen-baseline.json`.
Kubernetes fast/full snapshots each timed out three times at 120 seconds;
the protocol blocks those two strata and preserves 960 planned unrun cells.
The remaining 16,320 paired requests retain the original settings. The
`039d213b` check passed historically in 1126.765 seconds with source hashes
unchanged; see `p1-corpus-20260905/correctness.json`. The later `dc0ddce7` and `05ad9842`
checks supersede that historical verification. No release gate has
passed.

Historical P1 monitor (`monitor-20260905T2234.json`): all three workers were active;
66 measured requests (63 partial, 3 timeouts), 1,434 explicitly unrun, and
15,780 pending planned cells. Kubernetes syntax-only snapshot cache-on requests
also hit the 120-second limit three times; its six actual requests are retained
and its other 474 cells are unrun. The two baseline-blocked strata retain
960 unrun cells. Frozen binary, input and baseline hashes still match.
No interim performance or quality conclusion is drawn. P4.1 remains pending;
a previous ledger wording error is corrected without changing P4 status.

Historical user-directed diagnostic stop: all three services verified inactive on
2026-09-05 at approximately 22:55 UTC; recurring continuation was paused at that checkpoint.
Retained 116 measured requests (113 partial, 3 timeouts), 1,434 protocol-unrun
cells, and 15,730 remaining planned cells. Interrupted in-flight requests
are preserved separately and are not fabricated as completed observations.
No further measurement is authorized by the old continuation; diagnose and
correct the findings before defining a separately versioned evaluation.

## Current evidence and pause state

Product source and verification are recorded once in Current phase above.

The retained Kubernetes syntax-only diagnostic was run from `5a60fc8f` on
Darwin/arm64. The cache-off arm exceeded its 120-second context deadline and
returned an error after 161.18 seconds; the cache-on arm was unrun. The
ten-second stack diagnostic is retained under
`p1-corpus-20260905/retained-search-timeout-trace/` and only locates work in
listed Git-directory observation. It is not a campaign observation and does
not establish semantic equivalence or a performance result.

All campaign services remain stopped. Validation VM deallocation was requested after collecting the diagnostics;
both campaign worker VMs remain off. No
campaign has resumed. Historical campaign counts remain 116 measured requests
(113 partial, 3 timeouts), 1,434 protocol-unrun cells, and 15,730 remaining
planned cells; interrupted requests remain separate evidence. No release gate
has passed.

## Historical checkpoint record

The following entries are retained as dated or explicitly versioned historical
evidence. They do not override the current source, pause, or release status
above.

- `f96483ef` and `9bfafe17` added bounded cache-publication batches; focused
  extraction race tests passed. `4f582425` fixed parallel relation ordering and
  `f6b8a9d1` added cache/fresh selective equivalence checks. These checks did
  not explain all retained real-corpus mismatches.
- The stopgap protocol in `stopgaps-v2.md` added first-issue worker pause,
  cross-worker supervision, expiring leases, immutable run directories and
  canary admission. Stopgap evidence is preserved by `3b242da7`, `5957f604`
  and `69268ec4`; no campaign was restarted from those checks.
- At `0381ea6d`, 56 harness unit/local subprocess tests passed in 4.561s with
  mocked Azure transport. This checkpoint required successful worker exits,
  terminal-state verification and upload acknowledgement before collection.
- The captured-selection prototype `134c3752` retained a failing
  binary-attribute parity regression; oversized and locale-sensitive matching
  remained unresolved. It was not a release candidate and had no full
  repository check.
- At `5a60fc8f`, focused semantic race (35.322s) and Git utility (22.896s)
  checks passed with unchanged source hashes. The checkpoint added bounded
  oversized-file evidence and operation-bound Git-attribute decisions; it did
  not complete repository-subdirectory, locale/platform or full retained-corpus
  parity.
- The verified `dc0ddce7` checkpoint retained 55 off/on pairs with distinct
  preselection paths. Access telemetry included selected-pool hydration, and
  the combined large parity/mutation run passed in 16.860s. A reduced Git
  matcher contract still failed because a `.gitattributes` binary-classified
  file was included; this remains historical diagnosis, not current release
  evidence.

Corrective diagnostic `retained-snapshot-d793b2be/`: one pair at fully verified
source, identical semantic/partial/warning digests and unchanged corpus before,
between and after requests. OFF53.492s and ON73.462s exceed the 1.10 cold
screen. RSS is unavailable. This failed screen is retained without a statistical
claim or campaign expansion; prior failures remain untouched.

Remaining-cost diagnosis: `cold-profile-d793b2be/` retains one ON-only CPU
profile with exact semantic digest/194 partials. Publication accounts for20.86
cumulative CPU-seconds, including11.27 in quota maintenance; these samples
are not a wall-time comparison. Two collection failures and their recovery
are retained; the product request was not rerun. The subsequent encoded-envelope correction removed duplicate encoding
within existing bounded publication.
Quota-scan alternatives require explicit correctness review; no quota limit,
corpus, threshold or default is being tuned.

Admission-session correction `3a8ac22f` and `0c9e80f5`: held-directory
locking/accounting, one bounded inventory per session, exact installed sizes,
idempotent final/cancel release and fresh reacquisition are implemented.
Root review caught future-shrink reservation undercounting; the follow-up
reserves existing bytes plus all pending bounds, including temporary overlap.
Focused correctness/subprocess quota and race fixtures passed, followed by
the immutable full check and pinned Linux checks at `0c9e80f5`. The current
compressor follow-up at `1c0b8e24` passed full and pinned correctness
verification. The separate live stop smoke passed with all three fake services
stopped; it ran no product queries.

Corrective measurement plumbing `31cc145d` adds Linux peak RSS to a new
versioned diagnostic runner without changing old artifacts. Root review fixed
acceptance of a valid plus malformed duplicate RSS line. Four fake-process/
parser tests passed (1.510 seconds); no product measurement was run. Missing,
malformed, nonpositive, duplicate or overflowing values stop before the next
arm. The campaign remains paused; this is plumbing evidence only.

The original 55 search mismatches are repetitions of three profile paths for
one retained Kubernetes query. `retained-query-correctness-1c0b8e24/` freezes
one trial-0 pair per profile, with strict semantic/partial/warning membership
parity and first-failure pause. These are correctness cases, not a performance
sweep; execution and complete coverage are not yet claimed.

Current follow-up: ADR 0048 bounds detached publication batches operation-wide.
The source audit found that up to eight workers can each detach a whole batch
before waiting for the serialized writer. A cancellation-aware publication
gate is accepted and implementation is active; this is not yet verified or
claimed to explain the RSS difference. `1c0b8e24` remains the latest fully
verified product. Syntax-only and fast retained query pairs now have exact
semantic/partial/warning parity; the corrected full-only replay is active.

Retained query closeout at `1c0b8e24`: all three distinct profile paths now
have exact semantic, warning, completeness and full 11-record partial parity,
with 381 indexed files per arm and unchanged inputs. Seven requests ran: two
completed pairs, one full OFF halted on an oracle error, then the corrected
full-only pair. Historical repetitions were not rerun or relabeled. See
`p1-corpus-20260905/retained-query-correctness-1c0b8e24/summary.json`.

ADR 0048 is implemented at `6cf92c9c`; focused correctness and race checks
passed. Its full immutable and pinned Linux verification is next. Prior
`1c0b8e24` checks do not verify the new gate, and the RSS screen remains failed.
