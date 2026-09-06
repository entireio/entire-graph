# ADR 0044: Remove redundant cold-cache work without changing validity

Status: accepted implementation decision; performance remains unverified.

Plan tasks: P1.2/P1.3/P1.6. A retained Linux snapshot pair at `05ad9842`
matched semantic, warning and partial-failure digests but exceeded the cold
latency screen. Its parse/extraction phase contains most of the observed gap.
This one observation does not attribute cost or establish a population result.

Three independently identified operations can be removed while preserving
their correctness boundaries:

1. Replace production JSON round-trip/deep-equality admission with complete
   UTF-8 validation of the explicit extraction payload. For the current
   exported string/bool/integer/slice/struct shape, invalid UTF-8 replacement
   is the identified silent JSON loss. Keep round-trip tests and add a
   structural test covering every string field and rejecting unreviewed
   custom serializers, tags or other kinds. Never simply remove admission
   protection: invalid byte strings must continue to bypass persistence.
2. Retain a fresh capture's original bytes for its first caller after a
   successful spill, instead of immediately opening and reading back the
   same bytes. The store still releases retained memory and keeps private
   backing; subsequent consumers continue to verify backing length/digest.
   Spill failures and cancellation remain explicit failures.
3. Keep the locked full cache inventory and quota calculation, but sort for
   eviction only when eviction is actually required. Do not reuse mutable
   filesystem accounting between lock acquisitions or change batch quotas.

Alternatives rejected: dropping invalid-input validation, skipping backing
integrity checks, increasing resource limits, or caching unprotected quota
headroom. No mount/path decisions, source reads, payload fields, cache keys,
defaults or public schema meanings change.

Distinguishing tests cover all payload string fields including invalid UTF-8,
nil/empty collections, immutable first-capture bytes, later backing corruption,
spill/cancellation failure, under-quota no-eviction behavior and over-quota
deterministic eviction. Existing race, cross-process quota, no-follow and
source-freshness tests remain required. A new comparative run is not authorized
by passing those tests; the cold screen still needs subsequent evidence.

Rollback: disable extraction reuse, or revert these separate private-path
changes. Sources: supplied plan and Entire source/evidence only. New fixtures
are derived from the payload, capture and quota contracts.
