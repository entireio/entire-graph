# Retained query correctness runner

Runs only the frozen trial-0 search request in syntax-only, fast and full,
OFF then ON. First error stops all remaining arms. Execute as graphcheck on
the task VM; use fresh cache paths and a separate empty output directory.
Product source stays 1c0b8e24; this runner does not change the evaluator.

Before execution, root froze exact partial and warning membership from
`../retained-linux-05ad9842/raw-evidence/raw/off.json`. The same 11 partial
records occur in trial-0 OFF search rows in each of the three original
`../paused-raw/worker-{1,2,3}.tar.gz` archives, `results/campaign.ndjson`.
Canonical membership uses JSON with sorted keys, compact separators and UTF-8.
This is separate from the evaluator's Go-encoded digest. Any new, missing or
changed member stops before the next arm. These reviewed partials remain
coverage limitations, not full-coverage evidence.

The runner also checks full observed member lists/counts, valid digest formats,
semantic parity, warnings and completeness. Freshness uses independent corpus
fingerprints before/between/after requests. Process exits, deadlines and RSS
are measured externally; no nonexistent product fields are fabricated.
Raw product output is saved before validation. The tests use retained partials
plus independently constructed failure/process fixtures, not product timing.

Root verification: 8 tests passed, 0.460 seconds:
`python3 -m unittest discover -s docs/implementation/graph-advantage/p1-corpus-20260905/corrective-query-runner -p 'test_*.py'`.
The initial stricter-digest test failure exposed invalid placeholder hashes
in the fake evaluator; it was corrected to use the retained-fixture helper.
No product requests ran during preparation. No latency or RSS score is made.

The first replay completed syntax-only and fast pairs, then correctly paused
before full ON. Root found an expectation error: full OFF's extra
`W_DATA_FLOW_EVIDENCE_UNMERGED` warning is byte-for-byte identical to the
original worker-3 trial-0 OFF row. Warning membership is now frozen per profile;
partial membership remains identical across all profiles. A new invocation may
select only unresolved `--profile full`, retaining completed evidence and fresh
full cache paths. This corrects the oracle from pre-existing evidence; it does
not accept a new product warning or change the corpus. Nine tests passed in
0.678 seconds, including rejection of changed full-warning detail.
