# One-request full-diagnostics collector

Prepared, not executed. Eight synthetic tests passed (root run, 0.019s).
Run local plumbing tests with `python3 -m unittest discover -s . -p 'test_*.py'`.
The collector source is pinned to aab356ae; re-pin only after the next clean
immutable check as described in ../../shutdown-handoff-20260906.md.

One cache-OFF Kubernetes syntax-only request, fixed120-second deadline,
before/after input identities and complete raw diagnostic-array digest checks.
No repeats, ON arm, comparison, admission or automatic resume. Invocation
requires explicit binary/source/corpus identities; use `run_remote.py --help`
for arguments. The controller must retain its source-archive/build provenance.
Run as the owner of the pinned corpus (graphcheck on the task VM), collect
raw artifacts before deallocation, and keep the result unreviewed until every
partial has a source-grounded classification. No corpus execution occurred
while preparing or unit-testing this directory.
