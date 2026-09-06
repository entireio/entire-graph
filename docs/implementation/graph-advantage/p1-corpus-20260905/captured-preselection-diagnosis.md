# Captured preselection diagnosis — campaign remains paused

The retained 55 unequal pairs identified different selected inputs. Fixes through
`5a60fc8f` now preserve the bounded candidate pool using captured source, observe
oversized tails without widening parser limits, and apply Git binary attributes
from captured policy. The previously failing binary-attribute fixture now passes.
C/POSIX folding and matched-file retention were corrected using discriminating
locale fixtures. Stream, mutation, policy-identity and no-egress regressions pass.
See `captured-selection-checks.json` for the latest pinned focused check outcome;
it is not a complete repository check or a comparative measurement.

Remaining: repository-subdirectory policy capture is explicitly unsupported by
the new helper; broader locale/platform equivalence is unproven; later identifier
lookup and the retained corpus request still need end-to-end confirmation.
The 55 historical mismatches must not be called resolved from narrow fixtures.

An initial large-fixture run returned a repository/HEAD identity refusal; its
isolated rerun passed in14.707s. No source change was justified by that isolated
failure. Recurrence requires diagnosis, not suppressing the identity guard.

Design and provenance: ADR0042, the authorized plan and Entire implementation.
All new fixtures are independently authored correctness cases. No benchmark
campaign restarted. Defaults and release status remain unchanged.
