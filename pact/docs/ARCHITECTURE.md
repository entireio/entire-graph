# PACT architecture

`checkpoints.py` reads actual Entire Checkpoint metadata and transcript excerpts. The immutable SQLite registry in `storage.py` keeps proposals, human confirmations and revisions. It never derives a policy from the candidate implementation. Missing original source associations remain visible.

`graph_adapter.py` resolves separate immutable baseline/candidate snapshots using Entire Graph. It retains raw Graph bytes and hashes. It reconciles semantic-diff files with Git's changed-file inventory and adds bounded source precautions for Python runtime lookups, indirect/computed calls, decorators, wildcard imports and module configuration. Source precautions are never fabricated Graph edges.

`selector.py` traverses reverse CALLS within one version. Bound relations with source evidence, no warnings and reported confidence at least 0.8 are labelled confirmed structural evidence; other internal relations remain heuristic. This threshold is a conservative pilot policy, not a probability that a runtime call occurs. Missing/ambiguous bindings, omissions, diagnostics and traversal limits trigger all-confirmed-registered-check fallback for the Graph strategy. The comparison's changed-file baseline remains a visibly limited strategy.

`review.py` locks fixture contents, policy revisions, scenarios and evidence context into a hashed execution/replay bundle. `runners/local.py` executes each trusted fixture in a separate subprocess. `evaluator.py` independently applies approved predicates and classifies baseline/candidate outcomes. A regression requires an applicable passing baseline and failing candidate; uncertainty in Graph selection does not erase an actually observed failure.

`runners/databricks.py` uploads a content-hashed shared runtime and bundle to the existing workspace. `remote_worker.py` runs the same evaluator, persists five Delta tables, checks identities/cardinality and returns a receipt. New receipts preserve selection context and its hash independently of execution state. Recovery reuses saved receipts without silently changing inputs.

`evidence.py` provides sealed portable replay. It preserves analysis gaps; legacy bundles are not upgraded. `web.py` and the local JavaScript render confirmation/revision, reviews, filters, qualified structural paths, fallback diagnostics, history and recorded Delta receipts. Old reports remain immutable and receive an unassessed-quality label when the newer metadata is absent.
