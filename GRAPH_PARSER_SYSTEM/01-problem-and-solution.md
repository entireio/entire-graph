# Chapter 1: Problem and Solution

## The Problem

A code graph can surface definitions, call relationships, types, routes, and
semantic changes. That raw output is useful but incomplete: a developer still
needs to know why a symbol matters, what decision follows from a change, which
tests are obligations, and how to verify the conclusion.

The required product must therefore provide more than graph retrieval. It must:

1. Produce a useful decision, recommendation, or workflow.
2. Show the evidence behind that result.
3. Let the user inspect source code or run tests to verify it.
4. Support review, test selection, migration planning, onboarding, and
   checkpoint-aware investigation.

## GPS Solution

GPS extends the existing Entire Graph semantic graph with repository-authored
intent and reviewed traceability bindings:

```text
Specification -> requirement -> acceptance criterion -> declared test
       |                 |
       |                 +-> reviewed anchor -> code symbol -> graph relations
       |
       +-> decision records and local checkpoint provenance
```

The result is an evidence-backed workflow rather than a raw relationship list.
For example, when a bound implementation changes, GPS reports the affected
requirement, its declared test obligations, the code citation, the anchor's
drift state, and a `REVIEW_REQUIRED` disposition. The reviewer can then inspect
the cited symbol and run the explicitly selected test command.

## Product Outcome

GPS answers four practical questions:

| Question | GPS response |
| --- | --- |
| What should I change? | `context` returns matched requirements, approved symbols, ranked code, dependencies, and tests. |
| What could be affected? | `impact --intent`, `check`, and `review` expose graph and requirement consequences. |
| Why does this code exist? | `why` joins a symbol to its requirement, specification, declared tests, decisions, and optional local history. |
| How do I verify the change? | Declared mappings identify obligations; `verify` records results from a caller-authorized command. |

GPS preserves developer judgment. Its dispositions identify whether review is
needed; they never automatically approve a change or claim a requirement is
behaviorally satisfied.
