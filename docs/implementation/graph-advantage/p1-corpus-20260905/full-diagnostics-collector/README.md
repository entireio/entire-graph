# One-request full-diagnostics collector

Prepared, not executed. Eleven synthetic collector tests pass locally (0.045s),
including exact-cell refusal, durable pre-spawn accounting, failed-attempt
retention and restart refusal.
Run local plumbing tests with `python3 -m unittest discover -s . -p 'test_*.py'`.
The evaluator source is pinned to the clean immutable 1f20f694 check. The
focused Linux diagnostics race test passed and the evaluator was built from
that source; see `../../evidence/diagnostics-linux-1f20f694/manifest.json`.

One cache-OFF Kubernetes syntax-only request, fixed120-second deadline,
before/after input identities and complete raw diagnostic-array digest checks.
No repeats, ON arm, comparison, admission or automatic resume. Invocation
requires explicit binary/source/corpus identities and an exact one-cell
`bounded-diagnostic` manifest. The collector creates the fixed
`/opt/p1/batch-claims/<batch-id>/worker-<n>.json` ledger and durably records
`arm:false` immediately before process creation; use `run_remote.py --help`
for arguments. The controller must retain its source-archive/build provenance.
Run as the owner of the pinned corpus (graphcheck on the task VM), collect
raw artifacts before deallocation, and keep the result unreviewed until every
partial has a source-grounded classification. No corpus execution occurred
while preparing or unit-testing this directory.
