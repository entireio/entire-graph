# Canonical Graph Advantage evidence

This directory is the compact, versioned evidence layer used by the cleanup
migration. It records historical observations only. Normalization does not run
the product, establish a release gate, or turn an unknown, partial, failed, or
timed-out result into a pass.

`schema.json` defines `graph-advantage-evidence/v1`. Each file under `runs/`
contains one run record. A run with recomputable observations points to a
sorted `graph-advantage-evidence-case/v1` stream under `cases/`. Each case keeps
its original `observed_order`, explicit status, arm/trial identity, and the
sanitized structured observation. `sources.json` proves that the eight removed
source archives are exact reconstructible Git subsets. `critical-observations.json`
is an independently reviewed four-run oracle used by the validator.
The six diagnostic run records with retained CPU profiles point by digest to
`docs/implementation/graph-advantage/cleanup/profile-summaries.json`, whose
complete offline pprof tables replace those raw binaries. The baseline and P1
profile entries live in that same public summary and are mapped directly by the
cleanup inventory because they are not standalone canonical diagnostic runs.

The converter reads only structured JSON/NDJSON and named archive members. It
does not infer results from Markdown, command logs, or cloud transport output.
Absolute local paths, network locators, signed query values, and infrastructure
names are omitted with a reason recorded in `redactions`. Missing measurements
remain `null` with an unavailable reason. Legacy artifact references carry
their original digest; while the source remains present it must hash-match, and
after reviewed migration it may be absent only when marked `legacy`.

Regeneration requires the admitted pre-cleanup source revision, or a checkout
that still contains every indexed source artifact. Do not rerun the converter
against a partially cleaned tree: strict validation requires the retained
66-run index, verification counts, reviewed oracle, source reconstruction,
profile summary, and both P1 datasets, and rejects any silent shrinkage.

The normalized set contains:

- integration, correctness, diagnostic, build, and statusline evidence dirs;
- extraction v1/v2 and relation-profile observations;
- compiler-quality category rows;
- six combination/query response datasets;
- 108 P1 baseline observations; and
- the 116 measured P1 campaign observations, while preserving the 1,434
  explicitly unrun rows as status counts rather than measurements.

The paused completion diagnostic remains a `not_run` record and retains its
source package. P2/P3/P4 source fixtures, labels, policy inputs, and other
branch-added evidence that is not a converted run remain explicit KEEP paths;
the cleanup does not treat correctness query projections as GraphRank ranking
evaluation coverage.

Run from this directory:

```sh
python3 tools/convert.py --evidence-root .. --output-root .
python3 -m unittest -v tools/test_canonical.py
python3 tools/validate.py --canonical-root . --evidence-root ..
```

Consumers use `tools.compat.load_run(run_path)` and
`tools.compat.load_legacy_rows(run_path)`. The latter verifies the case digest
and count, then returns sanitized observed rows in their original order for
dual-read summary parity.
