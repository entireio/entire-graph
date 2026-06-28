# Plan: Hybrid LSP call resolution for entire-sem (a parallel, opt-in path)

## Goal & constraint

Add an **optional, parallel** LSP-backed call-resolution path that augments — does
not replace — the existing tree-sitter heuristic resolver. The fast, no-toolchain,
portable heuristic path stays the **default**; the LSP path is a higher-fidelity
**tier** you opt into when you have the language's server and can pay its cost.

This is the deliberate counterpart to the "#2 / real type resolution" tradeoff we
kept deferring: rather than rebuild a type system inside entire-sem, drive the
language's own analyzer (the same move CBM's "Hybrid LSP" makes), but keep it as
an add-on so entire-sem's identity — fast and LSP-free by default — is preserved.

## Why (grounded in the brain-bench results)

The four-language eval pinpoints where the heuristic resolver hits a ceiling, and
it's calls that need real type/macro resolution. **Crucially, the Rust numbers
below are now measured against the independent compiler oracle (rustc HIR), not
against rust-analyzer — see Phase 0, which is done.** That re-grounding overturned
the original motivation, so this section is corrected:

| language | call-graph F1 (entire-sem / CBM) | the gap is… |
|---|---|---|
| Go | 0.79 / 0.62 | (entire-sem ahead — LSP would mostly raise the exact tier) |
| TypeScript | 0.79 / 0.71 | typed-receiver `obj.method()` |
| Python | 0.82 / 0.64 | `var.method()` receiver type inference |
| Rust (byteorder) | 0.49 / **0.75** | **generic/trait dispatch** — LSP wins |
| Rust (httparse) | **0.51** / 0.28 | cfg-gated SIMD — LSP *over-emits*, loses to heuristic AND naive (0.90) |

The Rust story is **not** uniform, and that's the corrected finding: against
compiler ground truth an LSP only wins where the heuristic genuinely can't resolve
types — **generic/trait dispatch** (byteorder: CBM 0.75 vs entire-sem 0.49). On
cfg-gated code (httparse's `target_arch` SIMD) an LSP that takes an all-arch source
view *over-emits* edges for paths the target doesn't compile, craters precision
(CBM 0.28), and loses to both the heuristic (0.51) and naive name-matching (0.90).

So the LSP path is scoped to its real payoff: **generic/trait-dispatch resolution**
(the byteorder cell, and the analogous typed-receiver cells in TS/Python
`change-impact`). It is *not* a universal win for Rust call graphs, and it must
respect the oracle's target/cfg scope or it loses on precision.

## Design principles

1. **Additive & opt-in.** A new resolution mode; the default profile is untouched.
2. **Heuristic stays the backbone.** Tree-sitter remains the source of symbols,
   structural relations (DEFINES/CONTAINS/IMPORTS/EXTENDS/…), and the cheap call
   edges. The LSP augments only *call resolution* — the hard part.
3. **Per-language, gracefully degrading.** An LSP server registry; if a language's
   server is absent/unconfigured, that language silently falls back to heuristic.
4. **Provenance + confidence.** Every CALLS edge is tagged with its resolution
   source (`heuristic` vs `lsp`); LSP edges are the `exact` tier (mirrors the
   existing exact/`lsp_direct` notion and CBM's tiering).
5. **Merge, never silently drop.** Final edges = heuristic ∪ LSP, deduped by
   `(from,to)`; on conflict the LSP/higher-confidence edge wins. (An `lsp-only`
   call mode is also useful for clean A/B measurement.)

## Architecture

### A generic LSP client — `internal/sem/lsp`
JSON-RPC over stdio: `Content-Length` framing, request/response correlation,
**auto-ack of server→client requests** (`client/registerCapability`,
`window/workDoneProgress/create`) so it can't deadlock, and **index-readiness
tracking** via `$/progress`. Reference implementation already exists and is
proven: brain-bench's `oracle-rust/oracle.py` is exactly this client for
rust-analyzer's call hierarchy — port its logic to Go (it encodes the two hard-won
lessons: wait for `cachePriming` end, not the earlier `Roots Scanned`; bound every
request and skip-on-stall so one wedged symbol can't sink the run).

### An LSP call resolver
Per repo, one server session. For each tree-sitter function/method symbol, use its
`selectionRange` (name) position to run `textDocument/prepareCallHierarchy` +
`callHierarchy/outgoingCalls`. Map each `CallHierarchyItem.selectionRange.start`
back to entire-sem's `(file, name-line)` identity — the same mapping the
oracle-rust client uses — and keep only in-project callable targets (drop
std/external), matching the existing call scope.

### Server registry (`language → LSP`)
Parallel to the existing tree-sitter grammar registry:

| language | server | availability check |
|---|---|---|
| Rust | `rust-analyzer` | rustup component / PATH |
| Go | `gopls` | PATH |
| TypeScript/JS | `typescript-language-server --stdio` (tsserver) | PATH/npm |
| Python | `pyright-langserver --stdio` (or `pylsp`) | PATH/pip |

Each entry: command, args, availability probe, and the project-staging step it
needs (below).

### Integration point
Add `callResolution: "hybrid"` (and `"lsp"`) to `profileSpec` and a `ProfileHybrid`
in `resolveProfile`. In `forEachRelation`'s call scan, when the mode is hybrid/lsp
and a server exists for the file's language, run the LSP resolver and merge its
CALLS edges (tagged `resolution:"lsp_exact"`) with the heuristic edges. Everything
else (symbols, structural relations, the default `full`/`fast`/`syntax-only`
profiles) is unchanged. The snapshot header records the resolution mode + server
versions (provenance/reproducibility, like brain-bench's tool stamp).

## Operational concerns (learned the hard way building the oracles)

- **Latency is real and variable.** rust-analyzer indexing took seconds on
  byteorder but **wedged past timeout on semver** (its optional `serde` dep). So
  the LSP path is opt-in only; one reused session per repo; index-readiness wait
  with a hard cap; per-request timeouts with skip-on-stall.
- **Build/deps precondition.** LSP servers need the project to resolve/build
  (`cargo metadata`, `npm install`, a Python env). Reuse a brain-bench-style
  `install_deps` staging step; if it fails, fall back to heuristic for that repo.
- **Caching.** Key LSP results by file content (entire-sem already has `BodyHash`)
  so unchanged files aren't re-queried across runs — essential to make repeat runs
  affordable.
- **Determinism.** Pin server versions; record them in the header.

## Independent ground truth (the precondition that makes the eval clean)

brain-bench's premise is a **tool-independent oracle: the compiler**. As long as
the oracle is the language's *authoritative batch compiler analysis* and the
tools (entire-sem heuristic, CBM, the new hybrid) are separate, the comparison is
sound — including for the hybrid path. Three of four oracles already satisfy this;
one doesn't, and fixing it is part of this plan (Phase 0).

**The actual problem was the Rust oracle, and it was a pre-existing bug, not an LSP
issue — now fixed (Phase 0 done).** The Rust oracle *was* rust-analyzer — an IDE
engine, not the compiler. That violated "tool-independent oracle" (any
rust-analyzer-based tool, including CBM's LSP, scored against itself) and would
have made a rust-analyzer hybrid trivially circular.

**Fix (shipped): a compiler-grade oracle driven by rustc, reading the resolved
call graph from typed HIR** (`oracle-rust-rustc/` — a `rustc_driver` that, in
`after_analysis`, resolves every call to a fn/trait-method `DefId` via the typeck
tables). HIR-after-typeck is post-macro and trait/generic-resolved, so it's
independent of every IDE engine and resolves the macro-hidden and generic/trait
calls the heuristic approximates. (We first tried reading MIR, but pre-mono MIR
leaves library generics unresolved as `<Self as Trait>::m` — libraries don't
monomorphize their own generics — so HIR-after-typeck is the correct layer.) The
oracle is **pinned to a build target** (arm64 for now; other arches via GitHub
Actions later), so target_arch-gated code is deterministically in/out.

**What re-grounding revealed:** measuring against the compiler instead of
rust-analyzer overturned the headline "CBM 0.69 beats entire-sem 0.50 on Rust."
The truth is split (byteorder 0.49/0.75, httparse 0.51/0.28) — the LSP wins only on
generic-trait dispatch and loses on cfg-gated code. The eval is now sound; the
hybrid's expected payoff is correspondingly narrowed (above).

**The other three are already compiler ground truth, distinct from their IDE
LSPs:**
- **Python** — oracle `jedi`, LSP `pyright`: different engines entirely. Fully
  independent.
- **Go** — oracle is `go/types` resolution computed in `cmd/oracle`; the LSP is
  `gopls`. Same type checker, but the oracle is the compiler's authoritative
  resolution and gopls is a separate tool that re-derives a call graph and *can
  diverge* — that divergence is exactly what we measure, not a tautology.
- **TypeScript** — oracle is the tsc compiler API (`getResolvedSignature`), LSP is
  `tsserver`'s call-hierarchy provider: the same type checker via two different
  code paths that demonstrably disagree on edge cases. Not self-referential.

So after Phase 0 the rule is simply: **the oracle is the compiler; every tool
(heuristic, CBM, hybrid) is measured against it, none of them IS it.** No caveat.

## Validation — brain-bench is the harness

With independent oracles in place, add an `entire-sem-hybrid` system alongside
`entire-sem` / `cbm` / `naive` and measure across all four languages directly.
**Re-scoped expected outcome** (corrected by the Phase 0 re-grounding):

- **Primary, falsifiable target — byteorder-class generic-trait dispatch.** Hybrid
  should lift entire-sem's byteorder call-graph F1 from **0.49 toward CBM's 0.75**
  (CBM, an LSP tool, already demonstrates the ceiling against the *independent*
  oracle, so this is a genuine, non-circular target). If the hybrid can't approach
  0.75 here, it isn't worth its cost.
- **Guardrail — don't regress cfg-gated code.** On httparse the heuristic (0.51)
  already beats CBM's LSP (0.28) because the LSP over-emits cross-arch edges. The
  hybrid must scope LSP edges to the oracle's pinned target (or keep the heuristic
  edge) so it doesn't inherit CBM's precision collapse. Net: hybrid ≥ heuristic on
  httparse, not ≤.
- **Secondary — TS/Python `change-impact`** (the analogous typed-receiver gap).

The existing cost block quantifies the **accuracy-vs-latency tradeoff** — the whole
reason the heuristic path stays the default. Best isolating metric: **coverage of
the heuristic's miss set** (what fraction of the edges the heuristic misses does the
LSP recover, minus what it wrongly adds), which measures the LSP's contribution
independent of absolute F1 — and would have caught the httparse over-emission.

## Phasing

0. **Compiler-grade, independent Rust oracle (brain-bench). — DONE.** Replaced the
   rust-analyzer Rust oracle with a `rustc_driver` that walks typed HIR after
   analysis (`oracle-rust-rustc/`): calls resolved to fn/trait-method `DefId`s via
   typeck, post-macro and trait/generic-resolved, pinned to a build target.
   Rebaselined `results-rust`. (We tried MIR first; pre-mono MIR can't resolve
   library generics, so HIR-after-typeck is the right layer.) This fixed
   tool-independence for Rust and re-grounded the numbers — which **overturned the
   uniform-LSP-win premise** and re-scoped the rest of this plan (see Why /
   Validation above).
1. **Spike (Rust hybrid), narrowed to the byteorder/generic-trait cell.** Go LSP
   client (port of brain-bench's proven rust-analyzer call-hierarchy client) +
   resolver; wire `ProfileHybrid` for Rust only. Target: byteorder F1 0.49 → ~0.75
   against the independent HIR oracle, **while not regressing httparse** (scope LSP
   edges to the oracle's pinned target so we don't inherit CBM's 0.28 over-emission).
   Go/no-go gate: if byteorder doesn't approach 0.75 or httparse regresses below the
   heuristic's 0.51, stop — the LSP isn't worth its cost.
2. **Generalize.** Server registry + gopls / tsserver / pyright; per-language
   availability + staging.
3. **Merge, provenance, profile, header stamp.** `hybrid` + `lsp-only` modes;
   edge `resolution` source; server versions in the snapshot header.
4. **Caching + perf hardening.** Content-hash cache; session reuse; bounded
   readiness/requests.
5. **brain-bench `entire-sem-hybrid` system.** Full four-language measurement +
   the accuracy/latency tradeoff writeup, all against independent oracles.

## Non-goals
- Replacing or weakening the heuristic path (it stays the default and the fallback).
- Making entire-sem depend on any LSP to function.
- Building a type system inside entire-sem (the LSP *is* the type system, on demand).
