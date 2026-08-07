# Prose Memory Ranking v53 Design

## Goal

Improve Entire Graph's general prose retrieval for conversational-memory corpora
without adding benchmark-specific answers, evidence identifiers, prompts, or
vocabulary. The change must preserve code-search behavior and keep Graphify and
Codebase Memory MCP (CMM) as independent comparators.

The publishable success criteria remain a fresh, blinded comparison on:

- LOCOMO recall@10;
- LOCOMO QA accuracy;
- LongMemEval-S QA accuracy; and
- graph-build LLM credits, where zero is the best possible result and therefore
  a tie rather than a strict win.

No fresh paid evaluation is authorized by this design. Development may use
synthetic fixtures and already-retired tune cases only.

## Evidence and root cause

Stock CMM v0.9.0 excludes Markdown `Section` nodes from natural-language BM25
results. GraphMark materializes every conversation message as a Markdown
heading, so that filter removes the actual message text from retrieval. The
separately reviewed CMM patch exposes those existing Section nodes without
injecting evidence or changing questions.

A local, no-API diagnostic over the retired v52 tune set measured patched CMM
at 0.906 LOCOMO recall@10 and 1.000 LongMemEval-S evidence recall@10. Entire
Graph measured 0.939 and 1.000 on the same retired cases. These values guide
development only and are not publishable benchmark results.

The remaining Entire misses fall into general language patterns:

1. a base-form query does not always cover a past or gerund evidence token;
2. a compound query token may hide a useful standalone evidence word;
3. temporally phrased or ordinal questions may describe an event using a
   related word family rather than an exact token; and
4. list questions may require evidence distributed across multiple sessions.

## Constraints

- Work only on an isolated branch based on `bb8bd49`.
- Do not modify or merge the existing release PR.
- Do not inspect a fresh holdout or tune on its outcomes.
- Do not add named people, answers, dataset IDs, activity lists, or benchmark
  synonyms to production code or tests.
- Do not add network access, hosted embeddings, model calls, or build credits.
- Apply relaxed lexical matching only to the existing prose-parent lane. Code
  and identifier ranking must remain byte-identical under its regression test.
- Preserve deterministic ordering, bounded IO, result limits, and schema 1.x.

## Considered approaches

### 1. Benchmark vocabulary or synonym tables

Rejected. A table mapping individual benchmark words would leak development
cases into the product and would not generalize.

### 2. Hosted embeddings or reader-driven retrieval

Rejected. This would add egress, model dependence, cost, and a different graph
build-credit surface.

### 3. Bounded prose-only lexical families and session coverage

Selected. Extend the existing `safeProseInflectionMatch` seam with conservative,
deterministic word-family rules, and make any list/temporal coverage behavior a
generic query-shape policy. The current prose-parent lane already aggregates
weak evidence by Markdown file and allocates passages round-robin across
sessions, so the change can stay local and bounded.

## Proposed behavior

### Safe prose word families

Rename the helper conceptually from plural-only matching to prose lexeme
matching while retaining its current callers. It may match:

- singular and plural forms already supported;
- conservative ASCII `-ed` and `-ing` forms in either direction; and
- a standalone evidence word contained at the beginning or end of a longer
  prose query token, subject to minimum-length and length-ratio guards.

The helper must reject short stems and known unsafe suffix collisions. It is
used only when ordinary exact term counts have not already matched.

### Temporal and list coverage

Do not create a world-knowledge ontology. Instead, preserve more independent
session candidates when a prose query contains a bounded generic cue such as an
ordinal, `before`/`after`, or a plural/list interrogative. Admission must still
come from ordinary lexical/entity retrieval; the cue may affect diversity or a
small additive score, never manufacture a candidate.

If synthetic tests show the current diversity mechanism already provides the
needed behavior, make no production change for this section.

## Test strategy

Add red tests before implementation:

1. a base-form query retrieves a Markdown session containing a safe past-tense
   form;
2. a compound query token can recover a sufficiently long standalone prose
   word without promoting unsafe short-suffix collisions;
3. a temporal/ordinal query preserves a related session admitted through a
   real entity/topic match;
4. a list query returns distinct relevant Markdown sessions before repetitive
   distractors; and
5. the existing code-result byte-identity regression remains unchanged.

Run the focused prose tests first, then `go test ./internal/sem -count=1`,
`go vet ./...`, and `go test -count=1 ./...`. After local tests pass, run only a
no-API retrieval diagnostic over retired tune cases and report every regression,
not merely the average.

## Release and evaluation

The implementation will be proposed in a separate, unmerged PR. A new benchmark
protocol must seal source commits, binaries, prompts, models, selectors, and the
patched CMM identity before any paid execution. A fresh evaluation may run only
after explicit authorization and must be reported honestly even if Entire loses.
