# RFD 0006 - Reference positions: what the graph can honestly tell a SCIP consumer

Status: Proposed (RFC, open for comment)
Date: 2026-08-27

## TLDR

`snapshot --format scip` emits 25,716 SCIP reference occurrences for this repository. **None of them
is located at a reference.** Every one carries the enclosing caller's whole declaration span,
averaging ~100 lines, because that is what relation evidence records. The relations themselves are
correct; only the positions are not.

A SCIP `Occurrence` is a positional claim, so emitting one from a span we did not measure asserts
something the graph does not know. This RFD proposes stopping that, and treats reference-position
fidelity as the provider question it actually is.

Definitions, symbol metadata and implementation relationships are unaffected and remain sound.

## The measurement

Reproduced independently three times — by hand, by a Fable 5 agent, and by Codex (`gpt-5.6-sol`,
xhigh) — against `8935cfe9`.

| | |
|---|---|
| `CALLS` relations | 19,233 |
| evidence records carrying a position | 18,988 |
| **whose span is exactly the `from_id` symbol's own span** | **18,988** |
| whose span is narrower | **0** |
| evidence records carrying no position at all | 246 (138 `route_endpoint_match`, 108 `top_level_call_site`) |

`call_site` evidence: 11,886 records, 11,883 multi-line, mean width **100.66 lines**.

The counts above are what the command in the appendix prints at `8935cfe9`. Independent runs landed
within one record of each other (18,987 vs 18,988) as the tree moved; the column that matters is the
third, and it was **0** every time.

Concrete instances:

```
caller '_safe_id'  spans 102-104;  CALLS evidence is 102-104
caller '_iso'      spans 111-117;  CALLS evidence is 111-117
caller '__init__'  spans 121-141;  CALLS evidence is 121-141
```

**It is not language-specific.** Multi-line share of evidence: Go 99.9%, Python 100%, TypeScript
100%, Java 100%, C 98.1%, C# 100%, Zig 100%, Kotlin 90%. Only JSON and TOML evidence is single-line,
and that is config, not code. The apparent per-language variation in *width* (Go 75 lines, Java 2.8)
is average function size in the sampled files, not a parser difference — the rule is identical
everywhere.

Across all SCIP-mappable relation types, **48** relations in this repository carry single-line
evidence. Everything else the export emits as a reference is a whole-body span.

## What this means for a consumer

The relations are true. Only the positions are wrong.

| Consumer question | Answer today |
|---|---|
| Where is `Foo` defined? | correct — definitions come from symbol records, and are declaration-scoped |
| Which symbols reference `Foo`? | correct set |
| What symbol is at `file.go:50`? | **wrong** — line 50 inside any caller resolves as a reference to everything that caller calls |

The third row is the defect. It also happens to be the row RFD 0005 depends on: that document
specifies peregrine joining the feed **on name plus position**.

## Why the positions are missing

`Evidence` carries file, start line, end line, kind and detail — no columns, no offsets
(`provider.go:317`).

Every call-family builder copies the enclosing symbol's span into it: the bare-call resolver
(`provider.go:3375`), receiver calls (`provider.go:5188`), imported external calls
(`provider.go:6821`), Python's dotted-call resolver (`python_calls.go:128`), and the rest follow the
same construction.

The positions exist during scanning and are deliberately dropped:

- `callLikeIdentifiers` receives regex match offsets and reduces them to a `map[string]struct{}`
  (`provider.go:19588`).
- `receiverCalls` receives match indexes, deduplicates by `receiver.method`, and returns no location
  (`types.go:2795`).
- The JS/TS namespace scanner keeps each call's **tree-sitter byte position and line**
  (`js_scopes.go:31`) and then collapses calls to a set of qualified names before relation
  construction (`js_scopes.go:262`).

No profile offers a narrower alternative: `full` includes this evidence, `fast` strips evidence
entirely (10,669 evidence-free calls measured), `syntax-only` emits no calls at all
(`provider.go:518`).

There is a second, independent gap. Relation deduplication keys on `(from, to, type)` and discards
later records regardless of evidence (`provider.go:1415`), and `receiverCalls` dedupes by
`receiver.method` before that. **Repeated calls to the same callee collapse into one relation**, so
even perfect spans could not yield one occurrence per reference without also emitting multiple
evidence entries.

## The workaround that is not a fix

`neighbors` and `impact` already compensate. `resolveCallSite` (`cli/callsite.go:19,340`) re-reads
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

- The export keeps definitions (9,877), symbol metadata, and implementation relationships (45),
  which are positionless by construction and unaffected.
- Reference occurrences drop from 25,716 to ~48 for this repository.
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

Sized medium-to-large: ~40 `Evidence{` construction sites plus per-language extractors, several of
which (Clojure, SQL, Erlang, Haskell, OCaml, Objective-C) return position-less name sets today.

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

Current omission note for this repository, for reference:

```json
{"emitted_definitions":9877,"emitted_references":25716,"emitted_implementations":45,
 "missing_evidence_relations":1959,"missing_target_relations":125}
```
