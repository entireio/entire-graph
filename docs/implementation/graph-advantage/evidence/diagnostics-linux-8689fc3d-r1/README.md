# Pinned Linux boundary correctness and build

The checked source archive is pinned to
`8689fc3d790e4290ce2b62500d270c96c02087b3` and was extracted at
`/opt/graph-validation/diagnostics-linux-8689fc3d-r1/src`. The evaluator was
built from that extracted tree at
`/opt/graph-validation/diagnostics-linux-8689fc3d-r1/p1-evaluator`.

`stage-results.json` binds the ordered affected normal, affected race,
correctness, and build stages to their exact commands, logs, exits, test names,
and hashes. The `review` directory contains only the sanitized launcher
templates, diffs, and dummy binding proof reviewed before the run. No actual SAS
URL or reversible encoding of one is retained.

This evidence establishes focused correctness and build success only. It does
not contain a product, corpus, benchmark, admission, stability, or performance
claim.
