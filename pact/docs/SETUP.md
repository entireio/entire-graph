# Run PACT for Entire

Use the genuine fork's India mirror. All commands run from its repository root:

```sh
entire repo clone entire://aws-ap-south-1.entire.io/gh/Shaurya002800/entire-graph PACT-fork
cd PACT-fork
git switch pact/implementation
python3.12 -m venv .venv
.venv/bin/python -m pip install './pact[test]'
entire plugin install graph
entire graph version
PYTHONPATH=pact/src .venv/bin/python -m btw_pact.cli serve --port 8765
```

Open http://127.0.0.1:8765. A new installation has proposals, not automatic confirmations. Inspect the four policies and explicitly confirm with your own identity before running. Existing local confirmations and runs remain in the ignored `pact/runs/pact.sqlite` database. Never copy credentials or private transcripts to Git.

The pilot supports Python 3.11+; local verification uses 3.12 and the existing Databricks notebook used 3.11. Entire CLI 0.10.5 and Graph 0.4.0 were inspected. Only the registered team-owned fixture executes; this is not a hostile-code sandbox.

## Local review and portable replay

```sh
PYTHONPATH=pact/src .venv/bin/python -m pytest -q -c pact/pyproject.toml pact/tests
PYTHONPATH=pact/src .venv/bin/python -m btw_pact.cli review --request pact/demo/request-d1.json
PYTHONPATH=pact/src .venv/bin/python -m btw_pact.cli review --request pact/demo/request-d2.json
.venv/bin/btw-pact reproduce --bundle pact/docs/evidence/d1-reproducer.json
```

The example requests preserve Shaurya's actual prior confirmations and explicitly missing source associations. Test fixtures use synthetic actors. D1 should demonstrate two guest-export regressions. D2 should have ten passing candidate assertions. Both retain incomplete analysis because the export caller uses a runtime lookup. H1/H2 remain the fully resolved structural-path demonstration.

Review exit 2 means incomplete evidence, including missing source associations; it can coexist with demonstrated assertion failures. Replay exit 1 means candidate assertions failed, 0 means executed assertions passed, and 2 means execution was inconclusive. Replay returns saved analysis context separately and does not recompute or certify Graph/source coverage. Legacy bundles remain readable with unassessed evidence quality.

## Databricks

The existing workspace uses official CLI profile `PACT`, host `https://dbc-c3d496ed-7dbd.cloud.databricks.com`, and `workspace.pact`. Authenticate through the official CLI if needed; never paste a token into the repository. Select Databricks in the workbench. It uploads the registered synthetic fixture, shared runtime, policy parameters and selection metadata. Original private excerpt text is stripped.

The remote worker writes five Delta tables and re-reads results before issuing an integrity-checked receipt. Execution completion and analysis completeness are separate. New receipts preserve the full selection-quality context; legacy receipts are explicitly unassessed. The UI can recover recorded receipts directly from Delta.

If a job exceeds the UI's wait budget, retain its run ID and use:

```sh
PYTHONPATH=pact/src .venv/bin/python -m btw_pact.cli recover --run-id <PACT_RUN_ID>
```

Recovery reuses the remote job and requires the original request/runtime hashes. Use its original implementation version if the runtime changed. Do not resubmit blindly. A recorded cloud report is not a fresh cloud execution.
