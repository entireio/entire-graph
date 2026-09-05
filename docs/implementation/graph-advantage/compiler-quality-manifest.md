# Frozen P2 contract-quality evaluation v1

Frozen before first execution of `TestLiveCompilerQualityEvaluationV1`.
Fixture origin: independently authored from plan P2.3/P2.5, with hand-derived
exact declaration-token labels. Pinned gopls checks those labels; there is no
independent human adjudication or held-out real-repository claim.

| Category | Required declaration at sole caller site | Forbidden |
|---|---|---|
| aliased import | library `Target` | any other direct declaration |
| promoted method | embedded concrete `One.Work` | `Two.Work` or interface declaration |
| generic call | generic `Generic` declaration | other declarations |
| method expression | concrete `One.Work` | other declarations |
| pointer receiver | concrete `One.Pointer` | other declarations |
| interface declaration | declared `Worker.Work` | asserting either concrete implementation as direct |
| interface candidates | `One.Work`, `Two.Work`, distinctly candidates | any other candidate; promotion to direct |
| dynamic function parameter | no confirmed callable declaration | any asserted runtime function target |

The fixed source set is created in the test and retained verbatim, with SHA-256,
required/allowed/forbidden site metadata and raw observed target sets in JSON.
One named caller contains one call expression, making attribution of existing
aggregate static caller-target facts unambiguous; no proximity/nearest mapping.
Static non-call relations do not count. Compiler evidence is scored separately
from the static-plus-overlay union, avoiding an unearned claim that positive
compiler precision deletes an unrelated heuristic fact.

For each category and arm record required, true-positive, false-positive,
returned and missed counts. Recall = TP/required, precision = TP/returned;
zero denominators are null/not applicable. Macro averages exclude only null
categories and publish their denominator. Interface candidates use a separate
arm and denominator; dynamic unsupported call has zero required and forbids
confirmed runtime targets. Direct declarations are not runtime dispatch claims.

No parameter tuning, case replacement or threshold changes after observing the
run. Correctness gate: zero compiler false-confirmed declarations/candidates and
all required contract targets. Advantage threshold is strictly greater direct
recall than static with no direct precision reduction, reported only for this
fixed synthetic slice. This slice cannot establish the plan's independently
adjudicated real-world recall gate or generalize to all Go calls. Failures remain
in evidence; defaults stay off regardless of outcome.

## First frozen execution result

Linux/amd64, pinned Go 1.26.1 and gopls v0.20.0; race-enabled execution passed.
No fixture or product adjustment followed this run.

| Arm/category | Required | Correct returned | False confirmed | Recall |
|---|---:|---:|---:|---:|
| Static direct declarations | 6 | 4 | 0 | 4/6 |
| Compiler direct declarations | 6 | 6 | 0 | 6/6 |
| Compiler interface candidates (separate) | 2 | 2 | 0 | 2/2 |
| Dynamic function parameter | 0 | 0 | 0 | not applicable |

The additional direct declarations are the promoted method and generic call.
Both direct arms have precision 1.0 on their returned declarations (static 4/4,
compiler 6/6). Macro recall includes all six positive direct categories; macro
precision includes four static and six compiler categories with returned results.
The dynamic case remains explicitly unmapped, so overall overlay coverage is
partial; that limitation is retained rather than converted to success.

This meets the frozen synthetic contract-slice correctness and advantage rule.
It does **not** establish an independently adjudicated real-world release gate.
There are only six positive direct sites and no confidence claim about broader
Go workloads. Compiler remains default off.

Raw sources, hashes, labels, complete static relations, compiler responses and
mapped evidence: `evidence/compiler-quality-v1.json`. Metrics and per-category
denominators: `evidence/compiler-quality-v1-summary.json`. Race run:
`evidence/linux-p2-quality-v1.txt`; executable-source manifest:
`evidence/linux-source-p2-quality-v1.json`.

Reproduce scores without rerunning the server:

```
python3 docs/implementation/graph-advantage/probes/summarize_compiler_quality.py docs/implementation/graph-advantage/evidence/compiler-quality-v1.json
```
