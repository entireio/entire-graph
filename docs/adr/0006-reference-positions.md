# RFD 0006 - Reference positions: what the graph can honestly tell a SCIP consumer

Status: Proposed (RFC, open for comment)
Date: 2026-08-27
Refreshed: 2026-11-10 against `main` at `0e6bbba3`. Every `file:line` citation and every count
below was re-verified or re-measured at that commit (binary built from `main` at `0e6bbba3` with
this document added; Markdown contributes no relations). The proposal, options and recommendation
are unchanged. Neither Option A nor Option D is implemented on `main`: the encoder still emits one
occurrence per positioned evidence entry (`compact_snapshot.go:696-704`). The original measurement
was taken at `8935cfe9`; where a figure moved, the original is given in the refresh notes at the
end.

Numbering: `main` holds ADR 0001-0004 only, so `0006` collides with nothing. `0005` is claimed by
the convergence RFD in PR #146, which is still unmerged; until it lands, the link to
`0005-converge-code-search.md` below does not resolve, and if it is abandoned the series keeps a
gap at 0005 unless this document is renumbered.

## TLDR

`snapshot --format scip` emits 43,548 SCIP reference occurrences for this repository. **None of them
is located at a reference.** Every one carries the enclosing symbol's whole declaration span,
averaging ~62 lines (~78 for `call_site` evidence), because that is what relation evidence records. The relations themselves are
correct; only the positions are not.

A SCIP `Occurrence` is a positional claim, so emitting one from a span we did not measure asserts
something the graph does not know. This RFD proposes stopping that, and treats reference-position
fidelity as the provider question it actually is.

Definitions, symbol metadata and implementation relationships are unaffected and remain sound.

## The measurement

Reproduced independently three times — by hand, by a Fable 5 agent, and by Codex (`gpt-5.6-sol`,
xhigh) — against `8935cfe9`, and re-measured once on 2026-11-10 against `main` at `0e6bbba3`. The
figures below are the 2026-11-10 run.

| | |
|---|---|
| `CALLS` relations | 33,456 |
| evidence records carrying a position | 33,037 |
| **whose span is exactly the `from_id` symbol's own span** | **33,037** |
| whose span is narrower | **0** |
| evidence records carrying no position at all | 419 (308 `route_endpoint_match`, 111 `top_level_call_site`) |

`call_site` evidence on `CALLS`: 20,346 records, 20,331 multi-line, mean width **78.24 lines**.

The first two body rows of the table are what the command in the appendix prints at `0e6bbba3`
(`identical to caller span: 33037  narrower: 0`). The original independent runs at `8935cfe9` landed
within one record of each other (18,987 vs 18,988) as the tree moved; the column that matters is the
"narrower" one, and it was **0** every time, including in the refresh.

Concrete instances:

```
caller '_safe_id'  spans 102-104;  CALLS evidence is 102-104
caller '_iso'      spans 111-117;  CALLS evidence is 111-117
caller '__init__'  spans 121-141;  CALLS evidence is 121-141
```

**It is not language-specific.** Multi-line share of positioned evidence, all relation types, by the
language of the evidence file: Go 99.8%, Python 100%, TypeScript 100%, Java 100%, C 98.2%, C# 100%,
Zig 100%, Kotlin 86.7%. The languages whose evidence is entirely single-line are JSON, TOML,
Kustomize, Protocol Buffers, Make and XML — config and inventory, not code — plus five records in
the Haskell and OCaml fixtures, where the enclosing declaration is itself one line. The apparent
per-language variation in *width* (Go 67.8 lines, Java 3.8) is average function size in the sampled
files, not a parser difference — the rule is identical everywhere.

Across all SCIP-mappable relation types, **115** relations in this repository carry single-line
evidence, and in every one of the 115 that line is the whole of a one-line `from_id` declaration
rather than a measured reference site. Everything else the export emits as a reference is a
multi-line whole-body span.

## What this means for a consumer

The relations are true. Only the positions are wrong.

| Consumer question | Answer today |
|---|---|
| Where is `Foo` defined? | correct — definitions come from symbol records, and are declaration-scoped |
| Which symbols reference `Foo`? | correct set |
| What symbol is at `file.go:50`? | **wrong** — line 50 inside any caller resolves as a reference to everything that caller calls |

The third row is the defect. It also happens to be the row RFD 0005 depended on when this was
written: that document then specified peregrine joining the feed **on name plus position** (see the
refresh notes for its current wording).

## Why the positions are missing

`Evidence` carries file, start line, end line, kind and detail — no columns, no offsets
(`provider.go:499`).

Every call-family builder copies the enclosing symbol's span into it: the bare-call resolver
(`provider.go:6080`), receiver calls (`provider.go:8276`), imported external calls
(`provider.go:10478`), Python's dotted-call resolver (`python_calls.go:128`), and the rest follow
the same construction.

The positions exist during scanning and are deliberately dropped:

- `callLikeIdentifiers` receives regex match offsets and reduces them to a `map[string]struct{}`
  (`provider.go:24897`).
- `receiverCalls` receives match indexes, deduplicates by `receiver.method`, and returns no location
  (`types.go:2846`).
- The JS/TS namespace scanner keeps each call's **tree-sitter byte position and line**
  (`js_scopes.go:31`, `jsDottedCall` at `:69`) and then collapses calls to a set of qualified names
  before relation construction (`js_scopes.go:322`).

No profile offers a narrower alternative: `full` includes this evidence, `fast` strips evidence
entirely (18,020 evidence-free calls measured), `syntax-only` emits no calls at all
(`provider.go:773`).

There is a second, independent gap. Relation deduplication keys on `(from, to, type)`
(`relationDedupKey`, `provider.go:1990`) and discards later records regardless of evidence
(`provider.go:1614`; since the original measurement `main` counts and discloses the `DATA_FLOWS`
case, but still discards), and `receiverCalls` dedupes by `receiver.method` before that. **Repeated
calls to the same callee collapse into one relation**, so even perfect spans could not yield one
occurrence per reference without also emitting multiple evidence entries.

## The workaround that is not a fix

`neighbors` and `impact` already compensate. `resolveCallSite` (`cli/callsite.go:19,359`) re-reads
the source at query time, masks comments and strings, extracts a token from `Evidence.Detail`, and
scans the enclosing span for it — returning a call line and an "additional sites" count.

It is tempting to port this into the export path. **This RFD recommends against it.**

It is a heuristic display aid, and an honest one in its context: a human reading `neighbors` output
gets a plausible line plus a hint that there are others. As the positional claim in an interchange
artifact it is worse than what it replaces, because it converts an obviously-unusable 100-line span
into a *confidently precise* position that may be wrong, and picks arbitrarily among repeated calls.
A consumer can detect and discount a whole-body occurrence. It cannot detect a narrow occurrence
pointing at the wrong line.

Derived enrichment is not evidence. If the graph does not know where a reference is, the export
should not claim to.

## Options

### Option A: stop emitting reference occurrences we cannot place (recommended)

Emit an occurrence only where evidence is genuinely a single line. Count the rest in the omission
note as relations that could not be placed, using the existing `missing_evidence_relations` idiom.

- The export keeps definitions (17,205), symbol metadata, and implementation relationships (103),
  which are positionless by construction and unaffected.
- Reference occurrences drop from 43,548 to ~115 for this repository.
- The artifact stops asserting positions it did not measure. Consumers that want "which symbols
  reference `Foo`" are better served by `--format ndjson`, which never claimed positions.
- Cost: `--format scip` is not a find-references index until the provider gap is closed. That is a
  true statement about today, not a regression introduced by saying it.

### Option B: keep emitting whole-body occurrences, document the imprecision

Cheapest, and preserves a correct reference *set*.

- The note is a stderr sidecar. A consumer may discard stderr and treat the protobuf as complete,
  which is exactly the failure mode this projection's own design notes warn about elsewhere.
- The protobuf continues to make a claim the sidecar retracts. Documentation does not unmake an
  assertion inside the artifact.

### Option C: port the CLI's re-resolution into the export

Rejected above. Trades a detectable wrong answer for an undetectable one.

### Option D: fix the provider

The correct end state, and out of scope for a document:

1. Thread match and node offsets through the call scanners instead of reducing to name sets. The
   information is not irretrievably lost — `maskBytes` preserves length and newlines, so match
   offsets map 1:1 back to the original block, and JS/TS already carries tree-sitter positions.
2. Convert offsets to evidence lines.
3. Stop collapsing repeated call sites; emit one evidence entry per site. The `evidence` field is
   already an array and ADR 0001's `1.x` rules are additive-only, so this is schema-safe.
4. Update the CLI enrichment, which currently assumes a wide span to scan.

Sized medium-to-large: ~40 call-family `Evidence{` construction sites (42 with a `*call*` evidence
kind, of 86 `Evidence{` literals in non-test `internal/sem`) plus per-language extractors, several
of which (Clojure, SQL, Erlang, Haskell, OCaml, Objective-C) return position-less name sets or
call-site structs with no location today.

True identifier ranges *with columns* need more: additive provider fields for columns or offsets,
SCIP encoder support, and a compact-snapshot change, since its evidence tuple is a fixed five-field
shape.

With exact lines populated, the current SCIP encoder needs no change to emit narrow full-line
occurrences.

## Decision

**Option A now, Option D as the real fix.**

Option A is a small encoder change that makes the artifact honest today. Option D is the work that
makes reference navigation possible, and it should be scheduled on its own merits rather than
rushed to prop up a format.

## Consequences for RFD 0005

[RFD 0005](0005-converge-code-search.md) proposes entire-graph as peregrine's precision symbol
producer, with the feed supplying "**symbols and resolution**".

What the feed can honestly supply today is **symbols, definitions and the type hierarchy** — not
reference positions. Since RFD 0005 has peregrine joining on name **plus position**, this bears
directly on its open question 1 (per-language SCIP fidelity) and on the parity gates in its test
plan: a recall comparison against peregrine's native layer would measure the feed's references as
present but mispositioned, which neither "recall" nor "precision" captures cleanly.

This does not sink Option A of that RFD. peregrine's own persisted resolution is documented there as
an arbitrary-tiebreak heuristic, so a feed supplying correct definitions and a correct reference
*set* may still be an improvement. It does mean the RFD should say which of the two it is buying,
and should not assume positional reference navigation arrives with the first feed.

## Appendix: how to reproduce

```sh
entire graph snapshot --repo . --format ndjson \
  | python3 -c 'import json,sys
syms={}; same=diff=0
for line in sys.stdin:
    r=json.loads(line)
    if r.get("record_type")=="symbol": syms[r["id"]]=(r.get("file_path"),r.get("start_line"),r.get("end_line"))
    elif r.get("record_type")=="relation" and r.get("type")=="CALLS":
        for e in r.get("evidence") or []:
            s=syms.get(r.get("from_id"))
            if not s or e.get("start_line") is None: continue
            if (s[0],s[1],s[2])==(e.get("file_path"),e.get("start_line"),e.get("end_line")): same+=1
            else: diff+=1
print("identical to caller span:", same, " narrower:", diff)'
```

Printed at `0e6bbba3` on 2026-11-10: `identical to caller span: 33037  narrower: 0`.

Current omission note for this repository (same commit and date, selected fields), for reference:

```json
{"emitted_definitions":17205,"emitted_references":43548,"emitted_implementations":103,
 "missing_evidence_relations":4584,"missing_target_relations":252}
```

## Refresh notes (2026-11-10, `main` at `0e6bbba3`)

Facts only; nothing here changes the proposal.

| Figure | At `8935cfe9` | At `0e6bbba3` |
|---|---|---|
| SCIP reference occurrences | 25,716 | 43,548 |
| Definitions / implementation relationships | 9,877 / 45 | 17,205 / 103 |
| `CALLS` relations | 19,233 | 33,456 |
| Positioned `CALLS` evidence, all identical to the caller span | 18,988 | 33,037 |
| Narrower than the caller span | 0 | 0 |
| Unpositioned `CALLS` evidence | 246 | 419 |
| `call_site` records / multi-line / mean width | 11,886 / 11,883 / 100.66 | 20,346 / 20,331 / 78.24 |
| Single-line evidence, SCIP-mappable relation types | 48 | 115 |
| Evidence-free `CALLS` under `--profile fast` | 10,669 | 18,020 |

- The three concrete instances (`_safe_id`, `_iso`, `__init__` in
  `bench/memory/benchmarks/common/bm25_client.py`) still reproduce with the same line spans.
- The 115 single-line relations are `USES_TYPE` 56, `READS_FIELD` 23, `CALLS` 21, `PARAM_TYPE` 6,
  `RETURNS_TYPE` 5, `CONSTRUCTS` 2, `WRITES_FIELD` 1, `OVERRIDES` 1. Each one's evidence span equals
  its `from_id` symbol's own one-line span, which bears on what "genuinely a single line" selects
  under Option A.
- `docs/snapshot-format.md` on `main` now states that occurrence ranges "cover the complete
  one-based inclusive line spans supplied by Entire Graph; the export does not invent unavailable
  columns". It does not say that those spans are the enclosing declaration rather than the
  reference site.
- RFD 0005 (PR #146, head `e58013dc`) no longer contains the sentence "peregrine joins on name plus
  position"; its open question 2 was rewritten when the SCIP export review was folded in. The
  phrases "symbols and resolution" and open question 1 (per-language SCIP fidelity) are still
  there. Whether the "Consequences for RFD 0005" section should be reworded against the current
  text of that RFD is left to the authors of the two documents.
