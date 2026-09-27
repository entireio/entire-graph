# Pre-registration: do these results survive a query a user would actually type?

**Status: NOT RUN. Not authorised. No data exists.** This document is written before any
measurement so that the decisions in it cannot be made after seeing one. Nothing here may be
edited once a run begins; if something is wrong, the run is abandoned and the document revised
with the change recorded.

## The question, and why the existing result cannot answer it

The audited result on the development population is real: against queries built from a symbol's
own doc comment with the name stripped, a doc-phrase grep baseline beat the graph on locating
(19/19 vs 12/19), on showing the declaration (19/19 vs 7/19), and on bytes.

It generalises to nothing beyond that population, and we measured why. The baseline's search
string is a **verbatim substring of the target's own file**: it matches the target file 38 times
in 39, and **0 times in 39** when the same words are shuffled. Its entire retrieval is
contiguity. That is a fact about the queries, not about lexical search.

So the open question is narrow and specific:

> When the query shares vocabulary with the code but is **not a substring of it**, how do a
> semantic graph and a lexical baseline compare on cost to locate?

## Population (frozen before the first query)

- Public, immutable fixtures only, at pinned revisions, clean trees. A fixture whose repository
  is not public is **skipped, never substituted** — `entirehq/entiredb` was skipped for this
  reason and the run reported 2 fixtures rather than 3.
- Same symbol-selection rule as the existing harness (seed 7, documented symbols, ≥8 surviving
  words), so the *symbols* are comparable even though the *queries* are not.
- **Paraphrases must not be authored by whoever reads the results**, and must be produced
  without seeing either arm's output. Preferred: a held-out set written by someone else.
  Acceptable: a model that is shown the symbol's behaviour and never its doc comment or name.
  Not acceptable: any transformation of the doc comment, including shuffling — shuffled words
  are not a paraphrase, they are the same text with the contiguity removed, and a lexical arm
  should not be handicapped to make a point.
- Every paraphrase is checked against the file and **rejected if it appears verbatim**. That
  check is the point of the study and is recorded per case.

## Arms — byte-identical input, search derived from it alone

1. **graph** — `query --format agent`, fixed budget.
2. **lexical, word-based** — content-word co-occurrence, ranked, with a bounded read. Specified
   and frozen *before* the run. This replaces the doc-phrase arm, which cannot participate:
   with no verbatim substring there is nothing for `-F` to find, and reporting its zero would
   be theatre.
3. **oracle** — greps the target's name. A capability ceiling, **not a comparator**: outside
   every denominator and outside the admission gate.

The doc-phrase arm is retained in the harness and **not deleted**, because the existing result
belongs to it. It simply has no role in this population.

## Metrics, denominators, and what will be reported

- **locator** and **declaration**, scored identically for every arm, both bound to the target's
  file and registered span, declaration matched against the **source line read from the file**.
- **selected / attempted / scored / invalid**, all four, always.
- **median AND mean AND total** bytes. A median payload win is not a token saving, and grep is
  heavy-tailed enough that one common symbol name sets the mean.
- Oracle reported separately with its own denominator over **attempts**, not admitted cases.

## Falsifier, stated before the data

> If the graph does not beat the word-based lexical arm on **locator** by a margin that exceeds
> the paired bootstrap CI, the result is **negative** and will be reported as such.

A negative result here is the likely outcome and is worth publishing: it would mean the graph's
value is bounded cost and nothing else, which is already this project's surviving claim.

Additionally: **no sub-group may be read.** Power at these n is too low for per-fixture or
per-language claims, and this project has already withdrawn headlines built from exactly that.

## What this study cannot do

- It cannot rescue the existing result. Different population, different number, **never
  differenced against it**.
- It cannot speak to session-token savings. Cost-to-locate for one executed call is not what an
  agent would otherwise have read; that needs a randomised A/B nobody has run.
- It cannot speak to accuracy. Published work reports 60–69% of agent failures occur *after* the
  correct code is reached, so a search-stage tool has little accuracy to move.

## Preconditions

Clean-source binary (refused otherwise), sealed before the first arm, receipts per attempt,
VALID-or-VOID manifest, `--require-orchestration`. All enforced by the harness rather than by
intention.
