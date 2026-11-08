# Pre-registration: do these results survive a query a user would actually type?

**Status: NOT RUN, NOT AUTHORISED, NO DATA. Revision 2** — rewritten after three independent
adversarial reviews of revision 1, each told to assume the document was flawed. They were right;
revision 1 would have produced an uninterpretable number, and the reasons are recorded below
rather than quietly fixed.

## What revision 1 got wrong

1. **The rejection check tested the wrong mechanism.** It rejected paraphrases appearing
   verbatim in the file — a check against `rg -F`. The proposed arm is word co-occurrence. A
   paraphrase can fail the substring test with every content word still inside one doc-comment
   line, so the study would have re-measured the existing result and called it new.
2. **Nothing stripped the target's name from paraphrases**, and the lexical arm selects the
   longest non-stopword. One rare identifier turns the comparator into the oracle.
3. **"A generator never shown the doc comment or name" is unenforceable.** The body carries the
   identifiers, params, literals and the file path.
4. **`documented symbols only`** was constitutive when queries came from doc comments. Here it
   merely guarantees a prose restatement sits two lines above the declaration.
5. **The falsifier invoked a paired bootstrap CI. No bootstrap, percentile, resample or CI code
   exists in this harness** — zero occurrences. The rule referenced machinery that does not exist.
6. **No n, and sampling is a nested prefix**: `Random(7).shuffle` then `cases[:want]`, so n=40
   contains n=20 exactly. Growing n re-reads the same cases with no alpha spend.
7. **Only the graph is capped.** `graph_arm` takes a budget; `grep_arm` and `phrase_arm` do not.
   A byte win is the flag, not the tool.
8. **Admission requires every non-oracle arm to succeed** — including the doc-phrase arm that
   revision 1 retires from this study.

## The question

> When the query shares vocabulary with the code but is **not recoverable from it by contiguity**,
> how do a semantic graph and a word-based lexical baseline compare on locating a symbol, and at
> what cost **under an identical byte budget**?

## Preconditions that must land BEFORE the prereg is final

None of this may be decided after a pilot. Each is a commit, hashed into this document:

- **The comparator arm, as code.** Stopword list, ranking function, k, AND/OR semantics,
  casefolding, tie-break, read bound — all frozen, with its own falsifier tests. Revision 1 said
  "specified and frozen before the run" and specified nothing.
- **A shared byte budget**, one integer, applied to **every** arm, with output truncated to it
  and scored after truncation. Without this the cost axis measures a flag.
- **Bootstrap code**: 95% BCa, 10,000 draws, resampling unit = **fixture-clustered case**, paired
  differences. Case-level i.i.d. resampling across clustered fixtures is anti-conservative and is
  forbidden.
- **n and the fixture list**, hashed here before run 1. No prefix growth: a larger n requires a
  new seed and a new document.
- **A neutral symbol source** (tree-sitter or ctags), not the graph's own `symbols` output, which
  cannot sample what its indexer misses.

## Population

- Public, immutable fixtures at pinned revisions, clean trees. Non-public fixtures are **skipped,
  never substituted**.
- Rejection is on the **arm's own metric**: reject a paraphrase if ≥N of its stemmed content
  words fall within any K-line window of the target file. N and K frozen above. Per-case window
  overlap recorded.
- `strip_name` is applied to every paraphrase, and any token whose corpus document-frequency is
  below T is rejected. Per-token DF recorded.
- The generator's input is **redacted and stored verbatim** in the receipt: identifiers opaque,
  path hidden, comments and literals stripped. Paraphrases from unredacted input are marked, not
  pooled.
- Fixed k regenerations, then **drop the symbol**. Rejection rate reported as a denominator.
- **Two manifests, not one mixed population**: human-authored and model-authored are separate
  studies or the study does not run.
- **A paired control**: every arm run twice, once with doc comments stripped from the working
  tree. This is what isolates contiguity from vocabulary; revision 1 had no control at all.

## Arms — identical input, identical budget

1. **graph** — `query --format agent`, shared budget.
2. **lexical, word-based** — frozen source, shared budget, output truncated and scored after.
3. **oracle** — greps the name. Capability ceiling, outside every denominator and the gate.

Admission is gated on **the two comparators only**. Failures are reported as an intention-to-treat
sensitivity arm (scored as misses) alongside the complete-case estimate, because dropping on
failure is missing-not-at-random: a graph timeout removes a hard case, a punctuation-induced rg
exit removes an adversarial query.

Equal timeouts for every arm. A timeout is caught and marked `ok=False`, one rule for all.

## Metrics

- **PRIMARY: locator.** Everything else — declaration, median, mean, total — is **exploratory and
  labelled as such**. Revision 1 offered roughly twelve readable numbers against one falsifier.
- Report **discordant pair counts (b, c) and McNemar**, not only totals: for paired binary data
  the effective n is the discordant pairs, and at these sizes that is the number that decides
  whether anything is detectable.
- Report bytes **only among locator-hits** as well as overall, since cost on a miss is not
  comparable.
- All four denominators: selected / attempted / scored / invalid, plus rejection rate.
- **Bytes are not tokens.** No tokenizer is used; byte counts are reported as bytes and must not
  be quoted as token cost.

## Falsifier, stated before the data

> **Primary.** Positive only if the paired-difference 95% BCa CI for locator (10,000 draws,
> fixture-clustered) **excludes zero in the graph's favour**. Otherwise the result is negative and
> will be reported as negative.
>
> **Cost.** Positive only if the graph's iso-locator byte advantage CI excludes zero **under the
> shared budget**. Revision 1 had no falsifier for cost at all, which is how the flag-driven
> number would have survived.

MDE is stated before the run from the expected discordant-pair count. If MDE exceeds the effect
worth caring about, **the study does not run** — collecting an underpowered number and reporting
it is the failure mode this project has already committed several times.

## What this can never do

Rescue or be differenced against the existing result. Speak to session-token savings. Establish
an accuracy ceiling. Be read by sub-group — language, kind and doc length all sit in the
receipts, and one-repo-per-invocation means every printed number is already a sub-group unless
pooled deliberately.
