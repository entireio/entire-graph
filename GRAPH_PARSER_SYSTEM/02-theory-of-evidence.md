# Chapter 2: Theory of Evidence

## Why Intent Must Be Explicit

Code structure can show that one function calls another, but it cannot prove
which business requirement a function implements. Similarly, a nearby test name
is not proof that the test is required or that it passed. GPS addresses this gap
by requiring explicit, versioned repository inputs for product intent and test
obligations.

## Evidence Classes

GPS keeps evidence classes separate in both reasoning and output:

| Class | Meaning | Example |
| --- | --- | --- |
| Confirmed structural | A selected repository input resolves deterministically. | A reviewed anchor resolves to a parsed symbol; a `CALLS` edge exists. |
| Heuristic or incomplete | A candidate exists, or analysis may have omitted facts. | A name-matched `Test*` function; a partial parse. |
| Requires verification | The claim is about runtime or product behavior. | A requirement is fulfilled; a route works in production. |

A candidate test never fulfills a declared test mapping. A passing execution
result never proves every requirement. These distinctions prevent an attractive
but unsafe graph result from being treated as a final answer.

## Authority Model

Different inputs answer different questions:

| Input | Authority | Establishes |
| --- | --- | --- |
| Specification YAML | Developer-confirmed intent | Requirements and acceptance criteria. |
| Reviewed anchor binding | Approved traceability | The implementation symbol selected for a requirement. |
| Semantic graph | Observed structure | Definitions, relations, types, routes, and semantic deltas. |
| Verification result | Observed execution | The recorded result of one authorized command and scope. |
| Git history or checkpoint trailer | Local provenance | Historical context only. |

No authority substitutes for another. A checkpoint explains historical context;
it does not create product intent. A graph edge shows structure; it does not
prove behavior.

## Decision Semantics

GPS uses a non-binary disposition so uncertainty remains visible:

| Disposition | Meaning | Recommended action |
| --- | --- | --- |
| `PASS` | Selected static checks found no blocking finding. | Continue with normal review. |
| `REVIEW_REQUIRED` | Drift, mappings, or change impact require assessment. | Inspect cited source and obligations. |
| `INCOMPLETE` | Partial analysis or changing inputs block a reliable conclusion. | Restore complete inputs or investigate the gap. |
| `FAIL` | Invalid intent or broken traceability prevents the requested join. | Repair the input or binding. |
| `NOT_CONFIGURED` | No GPS specifications are selected. | Use code-only graph workflows or adopt GPS intent. |

This turns graph facts into a concrete, auditable next step.
