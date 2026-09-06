# Gate — plan

**Buildathon 2026 (Bengaluru Tech Week) · Track 2: Build with Graph Intelligence**
Fork: `anisha-singhal/entire-graph` · Entire mirror region: `aws-ap-south-1` (India)

---

## 1. One sentence

`entire graph gate` turns an agent-written pull request into a ranked reading order and a
keep / continue / revert verdict, using only evidence the agent did not produce.

---

## 2. User and problem

**User:** a developer who has to decide whether an AI agent's checkpoint is safe to merge.

A human PR is three files because a human got tired. An agent PR is forty files because the
agent did not. The reviewer has two options today:

1. Read all forty — which destroys the reason the agent was used at all.
2. Skim and approve — which is how regressions ship.

Git tells you *which lines* changed. It does not tell you what those lines can break, which of
them anyone verified, or which forty-first file everybody forgot. Nobody has a third option.

**Gate is the third option.** It reads the entity-level semantic diff, computes what structurally
depends on the change, resolves which tests actually cover it, and checks what the repository's
own history says should have changed alongside. It emits a verdict, an exit code, and — the part
a reviewer actually uses — a ranked list of which four of the forty entities deserve their eyes.

---

## 3. The moat — why this is not another change-risk tool

Every finding Gate emits comes from an artifact, never from an assertion.

| Signal | Evidence source | Can the agent fake it? |
|---|---|---|
| Blast radius | code graph — CALLS, USES_TYPE, PARAM_TYPE, RETURNS_TYPE, READS_FIELD, EXTENDS | No. The callers predate the change and were written by other people. |
| Coverage / unchecked | the repository's test tree and its own build files | Only by writing a test that asserts nothing — which is why `UNASSERTED` is on the roadmap. |
| Companion gap | git history — `FILE_CHANGES_WITH` edges + `git log` | No. History predates the session entirely. |
| Clone drift | `SIMILAR_TO` — MinHash near-duplicate symbol bodies | No. |

**Gate never asks the agent what it did.**

That single sentence is the product. An agent cannot make Gate say `keep` by describing its work
well, because Gate never reads the description. This is also why the tool can block a push: a
reviewer that changes its mind is not a gate, and anything with a model in the loop changes its
mind. Run Gate twice on the same commit and diff the bytes — they are identical.

The second half of the idea is **absence**. Every other review tool reports what it found. Gate
reports what nobody looked at — where "nobody" means the agent, the developer, and the test
suite. `unchecked` is not a hole in the product. It is the product.

> **Pitch:** Your agent wrote a 47-file PR. Gate tells you which 4 to read, and what nobody checked.

### Why intent-checking was rejected

An earlier design led with "did the agent do what it said?" — comparing the checkpoint's stated
intent against the entity diff. It was dropped deliberately, and the reasoning is recorded here
because a judge will ask:

1. **It checks a claim against a claim.** The diff is ground truth, but the agent's sentence sets
   the scope of what counts as *explained*. A confused agent widens its own permission.
2. **The mechanism is thin under inspection.** It would have been deterministic — `SearchRepository`
   is a lexical + graph ranker with no model — but "we run keyword similarity over the commit
   message" does not survive the follow-up question *"so if I write 'fix bug', everything is
   unexplained?"* Yes, it would be.
3. **It does nothing for human commits**, and nothing when the agent said nothing.
4. **Verified: there is no structured intent to read.** `sem.Result` carries only the checkpoint ID.
   `entire checkpoint explain` returns prose. A deterministic structured said-vs-did check has no
   input to work from in this system.

Provenance answers what intent could not: *"what happens when agents get better?"* Gate still
works, because it never depended on the agent in the first place.

---

## 4. Why Entire is essential

**The checkpoint is the input. The graph is the evidence engine. Remove either and there is no
product.**

- `entire graph gate --checkpoint <id>` resolves a checkpoint to a commit range through
  `gitutil.FindCommitWithCheckpoint` and `sem.AnalyzeCheckpoint`. The checkpoint is a first-class
  input type, not a way of naming a SHA.
- Three of the four signals are graph traversals over relation records. Delete the graph and Gate
  has nothing to report.

This is not Entire bolted onto a product for the sake of the rules. It is the substrate.

---

## 5. Architecture

One direction, no loops. Every layer passes forward and never calls back.

```
  --checkpoint <id> | --base <ref> --head <ref>
                │
                ▼
        ┌───────────────┐
        │    collect    │   the ONLY impure layer
        └───────┬───────┘   sem.AnalyzeCheckpoint / AnalyzeGitRange
                │           sem.BuildProviderSnapshotWithOptions
                ▼           sem.SearchRepository
        ┌───────────────┐
        │     index     │   reverse-edge map, file→symbol map,
        └───────┬───────┘   keyed by compound-v1 symbol IDs
                │
    ┌───────────┼───────────┬───────────┐
    ▼           ▼           ▼           ▼
  risk      coverage   companions    clones      pure functions, no I/O
    │           │           │           │
    └───────────┴─────┬─────┴───────────┘
                      ▼
              ┌───────────────┐
              │    verdict    │   pure: findings → verdict + exit code
              └───────┬───────┘
                      ▼
              ┌───────────────┐
              │    render     │   text | --json | review order
              └───────────────┘
```

Two reasons this shape, both load-bearing:

1. **It is a parallelization plan.** Layers that never call back are three people editing disjoint
   files behind one struct contract. This is the only way four signals fit in the time available.
2. **The noon curveball lands in one place.** A constraint about output, thresholds, or verdict
   semantics touches `verdict` or `render` — both pure, both unit-tested with synthetic records,
   neither requiring CGO to test.

### Package layout

| Path | Contents | CGO needed to test? |
|---|---|---|
| `internal/gate/` | `types.go`, `index.go`, `risk.go`, `coverage.go`, `companions.go`, `clones.go`, `verdict.go`, `render.go` | **No** |
| `internal/cli/gate.go` | flag parsing, `resolveRepo`, all `sem.*` calls | Yes |
| `internal/cli/root.go` | one `case "gate":` in the dispatch switch | — |
| `internal/cli/help.go` | one `commandDocs` entry (auto-wires `gate --help`) | — |

The `internal/gate` boundary means a builder whose laptop cannot compile tree-sitter can still
write and test two thirds of the product.

---

## 6. How we use the Entire Graph — in detail

This section is the answer to "show where the evidence came from."

### 6.1 Which APIs we call, and why in-process

All calls are **in-process Go**, not shell-outs to `entire graph`. The track rules say an
integration that merely calls an Entire command from another interface is not sufficient; Gate is
a subcommand *of the provider*, reusing its internals.

```go
// entity-level semantic diff — internal/sem/analyze.go
func AnalyzeCheckpoint(ctx, repo, checkpointID string) (Result, error)          // :1159
func AnalyzeGitRange(ctx, repo, base, head string, paths []string) (Result, error)

// checkpoint → commit — internal/gitutil/git.go
func FindCommitWithCheckpoint(ctx, repo, checkpointID string) (string, error)   // :146

// symbols + relations — internal/sem/provider.go
func BuildProviderSnapshotWithOptions(ctx, repo, ver string, o ProviderSnapshotOptions) (ProviderSnapshot, error)  // :929

// ranked search, covering tests, runnable verify command — internal/sem/search.go
func SearchRepository(ctx, repo, providerVersion, query string, o SearchOptions) (SearchResponse, error)  // :683
```

`EntityChange.DependentsCount` arrives already populated by `internal/sem/dependents.go`, so the
first cut of the risk signal costs nothing.

### 6.2 Which relations each signal traverses

Measured on this repository at `HEAD = 3a2a715`, full profile, 69,613 records, **17.6 s**:

| Relation | Count | Used by |
|---|---:|---|
| `CALLS` | 22,616 | risk |
| `DEFINES` | 11,397 | index |
| `DATA_FLOWS` | 5,942 | **excluded — see Known bugs 1** |
| `SIMILAR_TO` | 3,479 | **clone drift** |
| `USES_TYPE` | 2,853 | risk (type consumers) |
| `IMPORTS` | 2,286 | index, coverage fallback |
| `READS_FIELD` | 1,592 | risk |
| `PARAM_TYPE` | 1,504 | risk |
| `RETURNS_TYPE` | 993 | risk |
| `CONSTRUCTS` | 71 | risk |
| `FILE_CHANGES_WITH` | 57 | **companion gap** |
| `TESTS` | **9** | *unusable — see 6.4* |

### 6.3 Gate must run `--profile full`

`fast` emits only `CALLS, CONFIGURES, CONSTRUCTS, CONTAINS, DEFINES, HANDLES_ROUTE, HANDLES_TOOL,
IMPORTS, RESOURCE_DEPENDS_ON`. It has **no** `USES_TYPE`, `PARAM_TYPE`, `RETURNS_TYPE`,
`READS_FIELD`, `WRITES_FIELD`, `EXTENDS`, `FILE_CHANGES_WITH`, `TESTS` or `SIMILAR_TO`. That is
six of the twelve edge types the blast radius walks, plus the only two sources for the companion
gap and clone drift signals. 17.6 s is acceptable; `entire graph index --head --profile full`
warms a durable cache for repeated runs.

### 6.4 The measured finding that shapes the whole design

**`TESTS` resolves 9 edges against 11,397 definitions in this repository.**

The heuristic is *"test name maps to the unit under test by convention"* — `TestAnalyzeGitRange`
→ `AnalyzeGitRange`. Real Go test names here look like
`TestGoReceiverMethodResolvesAcrossPackageFiles`, which names no symbol. `capabilities --json`
also omits `TESTS` from Go's `relation_support_by_language` entirely.

So graph-native test coverage is unusable on this repo, and **we have the number**.

This is not a problem to hide. It is the reason `unchecked` exists as a first-class verdict state
rather than a footnote:

> The graph's TESTS relation resolves 9 symbols out of 11,397 in this repository. We measured it.
> That is exactly why Gate reports **unchecked** instead of pretending a symbol is covered.
> A gate with no test is not passing — it is unchecked.

Graph results are evidence, not an oracle. Gate is built around a measured statement of where its
own evidence runs out.

**Consequence:** the coverage signal does not use `TESTS` edges. It uses the covering-test
machinery already inside `search` (`internal/sem/search_covertest.go`), reached through
`SearchRepository`, which resolves tests by mirror-path, name convention, and body mention — and
which demonstrably works on this repo. `SearchResponse` hands back both:

- `CoverageNote *SearchCoverageNote` — `{Symbol, Total, Peers, More, FilePath}`
- `VerifyCommand *SearchVerifyCommand` — `{Command, Targets, DerivedFrom, Tier, RunnerMissing}`,
  a runnable command derived from the repo's own build files, with its own confidence tier
  (`narrow` / `suite` / `build-check` / `none`)

We do not write a test resolver. We consume one that already covers go, pytest, npm, cargo, maven,
gradle, composer and ruby, and that reports its own uncertainty.

### 6.5 Provenance — `--explain`

`RelationRecord.Evidence[]` carries `{Kind, FilePath, StartLine, EndLine, Detail}`. Every finding
Gate prints can be expanded to the exact call path and source location behind it:

```
$ entire graph gate --explain 2
2. VerifyToken @ internal/auth/token.go:88
   ← CALLS   middleware.Require   internal/http/mw.go:41   (evidence: call_site, mw.go:44)
   ← CALLS   router.Protected     internal/http/router.go:98
   14 dependents at depth ≤2 · UNCHECKED
```

A judge can open any one of those files and check it. That invitation is only safe because there
is no model in the loop.

### 6.6 Graph evidence we record for the submission

Saved under `docs/graph-findings/`:

1. **Search / definition lookup** — the orientation queries that located the diff engine, the
   dispatch switch and the snapshot builder.
2. **Relationship / impact analysis before a high-risk change** — `entire graph impact` on the
   area touched by the noon curveball, run *before* editing.
3. **Final semantic diff** — `entire graph diff --base <pre-noon> --head HEAD --json`.
4. **Gate on itself** — Gate's verdict on our own commits, including the curveball commit.

---

## 7. The four signals

### 7.1 Risk — blast radius

For every `removed`, `renamed` or `signature_changed` entity, walk **reverse** edges over
`CALLS, ASYNC_CALLS, CONSTRUCTS, USES_TYPE, PARAM_TYPE, RETURNS_TYPE, READS_FIELD, WRITES_FIELD,
EXTENDS, IMPLEMENTS, OVERRIDES, INHERITS`, depth ≤ 2, and rank by dependent count.

`DATA_FLOWS` is deliberately excluded and `CONTAINS`/`DEFINES` with it — see
Known bugs 1 for why including the first was wrong.

Depth is capped at 2 deliberately: without it, one utility function pulls in the whole repo and
the output becomes noise. `--hops 1|2` exposes the trade-off.

### 7.2 Coverage — verified / unchecked

Per changed entity, `SearchRepository` → `CoverageNote` + `VerifyCommand`. Emit one runnable test
command listing only the selected tests. Report three counts, not one:

- **verified** — a test covers it
- **unchecked** — no test covers it
- **no resolver** — the language has no derivable runner (honest, not a failure)

### 7.3 Companion gap

Files that historically co-change with the edited files but were not edited this time.
`FILE_CHANGES_WITH` edges carry the ratio already — `reason: "files changed together in 52 recent
commits"`, `evidence: [{kind: "git_log", detail: "52 commits"}]` — so the "14/15 past commits"
phrasing comes straight from the data. Only 57 such edges exist here, so `git log --name-only -n 200`
widens the set with a ≥ 70% / min-5-observation threshold.

*The graph says what structurally depends on the change; history says what habitually accompanies
it. Bugs hide in the gap between those two views.*

### 7.4 Clone drift

`SIMILAR_TO` (3,479 edges, `reason: "near-duplicate symbol body (MinHash estimate)"`,
confidence 1.0). If a changed symbol has near-duplicate siblings that did **not** change, say so:

> `_safe_id` changed. 3 near-duplicate siblings did not: `cmm_client.py:29`,
> `entire_client.py:31`, `graphify_client.py:28`.

*You fixed the bug in one copy. Three copies still have it.* This is an absence finding — exactly
the shape of the product — and it uses a relation type nobody else will touch.

---

## 8. Verdict model

Four states. Mirror-style honesty about the failure case, without Mirror's seven codes.

| Exit | Verdict | Meaning |
|---:|---|---|
| 0 | `keep` | Verified. Ship it. |
| 1 | `continue` | Mostly fine — check these specific things. |
| 2 | `revert` | Something is wrong. Roll back. |
| 5 | `unusable` | Gate ran, here is the full report, nothing downstream can build on it. |

`unusable` exists because when the graph cannot parse a file or the language has no test resolver,
the honest answer is *"we could not check"* — not `revert`, which is a false accusation.

**The rule is printed in every run**, so the tool is never a black box:

```
RULE  revert   = a removed or signature-changed entity with ≥1 dependent AND no covering test
      continue = risky change with tests, OR a companion gap, OR new entities with no tests
      keep     = none of the above
      unusable = the graph could not produce evidence for the changed files

DEGRADATION  a dimension that did not run cannot produce a finding against you.
             coverage unavailable  -> no finding may reach revert; cap at continue
             risk unavailable      -> cap at continue
             both unavailable      -> unusable (exit 5)
```

### Degradation is part of the rule, not an edge case

`revert` requires *≥1 dependent AND no covering test*. If the coverage dimension did not run, then
**nothing** has a covering test, and every change with a dependent reads as `revert`. That is not
strictness — it is a false accusation produced by a missing input, and it is exactly the failure
`unusable` exists to prevent.

So the verdict function takes a per-dimension availability flag, and an unavailable dimension can
never push a finding upward. Ten lines in a pure function, unit-testable with synthetic records,
and it is the honesty the rest of this document claims. **A dimension that did not run is reported
as not-run, never as a failure.**

Colour carries verdict only. Everything else is plain.

---

## 9. Review order — what a reviewer actually uses

The verdict decides *whether*. The review order decides *what to read*. Same computation, sorted
by **dependents descending, ties broken by unchecked first**. (Not a product — uncheckedness is
binary, so `dependents × unchecked` zeroes every verified entity and drops a 9-dependent change
below a 6-dependent one.)

```
REVIEW ORDER — 47 entities changed, read these 4 first

 1. VerifyToken     @ internal/auth/token.go:88
    14 dependents · UNCHECKED · 2 near-duplicate siblings untouched
 2. parseFlags      @ internal/cli/root.go:729
    9 dependents · verified (root_test.go:212)
 3. Config.Load     @ internal/config/load.go:31
    6 dependents · UNCHECKED
 4. handleList      @ internal/api/list.go:44
    3 dependents · companion gap: list_test.go (14/15 past commits)

 The remaining 43 entities have 0 dependents and no findings.
 Full list: --all
```

That last line is what earns trust: Gate is not hiding 43 things, it is saying why they do not need
your eyes.

Output is byte-budgeted per section with counts for what was elided, matching the repository's
existing `--max-context-bytes` discipline. Large PRs are the point, so the renderer must degrade
gracefully rather than dump.

---

## 10. Databricks — we are not opting in

Deliberate, and recorded so the decision is legible:

1. **It contradicts the thesis.** Gate's entire claim is deterministic, local, no-egress, no model.
   This repository's own contract is no-egress (`features_requiring_network_access` is all `false`).
   Adding a cloud dependency undermines the one sentence that makes the tool trustworthy.
2. **The Databricks rubric requires it to be essential** — 30 of its 100 points. You cannot make a
   hosted service essential to a local no-egress tool without breaking the tool.
3. **Focus.** Two 100-point rubrics, one afternoon, three builders, plus a mandatory constraint at
   noon. Splitting attention loses both.

A lakehouse aggregating Gate verdicts across many repositories — *risk debt over time* — is a
genuinely good next step and belongs in BUILDATHON.md's future work, not in today's build.

---

## 11. Build plan

### Lanes

Three disjoint file sets behind one struct contract. Agent choice is irrelevant; the parallelism
comes from the layering.

| Lane | Owner | Files | Depends on |
|---|---|---|---|
| **B1** | strongest Go / repo navigation | `internal/cli/gate.go`, `root.go`, `help.go` | nothing |
| **B2** | any | `internal/gate/{index,risk,coverage}.go` | `types.go` only |
| **B3** | any, no CGO required | `internal/gate/{companions,clones,verdict,render}.go` + all tests | `types.go` only |

**The unblocking rule: B1 commits a stub `collect` returning fixture records within 10 minutes of
`types.go` landing.** B2 and B3 then never wait on tree-sitter, and the fixtures double as the
synthetic records the unit tests run against.

### Schedule (revised for actual clock — it is ~10:50, not 9:00)

| Time | What | Cut line |
|---|---|---|
| now → 11:00 | `types.go` committed. **Checkpoint 1.** Push. Test the checkpoint link signed-out. | never cut |
| now → 11:25 | Three lanes. B1: dispatch + collect + stub. B2: index + **risk**. B3: **coverage** + verdict + render + tests. | **drop companions, drop clones** |
| 11:25 → 11:45 | Integrate. `mise run test`. **Checkpoint 2.** Save `gate` output to `docs/graph-findings/pre-noon-gate-on-self.txt`. **Pre-warm the full-profile cache. Record a 60s screen capture while it works.** | never cut |
| 12:00 → 12:15 | Fresh session. Reconstruct from checkpoint. Restate the constraint. Run `entire graph impact` on the target area **before editing**. Screenshot. | never cut |
| 12:15 → 14:15 | Curveball, smallest complete response, tests. **Checkpoint 3.** | — |
| 14:15 → 14:40 | Gate on our own curveball commit. Final `entire graph diff`. BUILDATHON.md. **Checkpoint 4.** | — |
| 14:40 | Stop. Verify links signed-out. Push. Confirm SHA. | — |

**The cut order is risk + coverage kept; companions and clones dropped.** This is not
preference — dropping coverage breaks the verdict rule (see §8) and would make the 11:45 artifact
report `revert` on everything with a dependent. Coverage is also *cheaper* than companions: it is
one `SearchRepository` call returning `CoverageNote` and `VerifyCommand`. We are not writing a
resolver.

**Twenty minutes to integrate, not ten.** Merging three lanes against a clock is where teams lose
the freeze commit.

Checkpoint 2 does not need a complete product — it needs a *runnable* one with a verdict that is
not nonsense. Two signals working beats four half-working.

### Deferred (in priority order)

**Not deferred — the pre-push hook is demo-critical.** §13 opens on a refused `git push`; that
beat cannot depend on something in this list. It is ~5 lines of shell wrapping the exit code, and
it must exist and be rehearsed by 14:40. If it slips, the opening beat becomes the byte-identical
`--json | shasum` instead — decide that consciously, not at 2:55.

1. `UNASSERTED` third coverage state — reimplement assertion detection in `internal/gate` (~25 lines).
   Not free: `searchCoveringTestBodyAsserts` is unexported, used only as a filter for weak
   candidates, skipped for mirror-named tests, and never surfaced.
2. Companion gap, then clone drift — the two signals cut from the morning.
3. `gate audit` — repo-wide "symbols with dependents and zero tests," no diff needed.
4. `.gate.yml` suppressions that require a written reason.

---

## 12. The Noon Curveball — protocol

At exactly 12:00 IST every team receives an additional, track-specific constraint. It is
mandatory, judged (15 points), and deliberately unpublished until the reveal. **It will challenge
an assumption in our current design.** The task is to adapt what we have already built, preserve
its useful behaviour, and demonstrate that the new requirement works.

The first version proves what we built. The second proves our process absorbs change without
losing intent.

### Before noon — preserve the stable state

Stop adding features and get back to runnable. Then:

1. `mise run test` green. Fix only what is red; no new work.
2. Update this file's **HANDOFF** section (below) so a session with no memory of the morning can
   resume: intent, the five layers and their files, what is done, what is not, known bugs, open
   risks, exact commands to build / install / run / test.
3. Commit: `stable: pre-noon Gate MVP`.
4. **Confirm the commit carries a valid Entire Checkpoint** — `entire checkpoint list` must
   increment. A commit without one is invisible to judging.
5. Save fallback evidence *before* the constraint can break anything:
   `entire graph gate --base <first-sha> --head HEAD > docs/graph-findings/pre-noon-gate-on-self.txt`
   and `entire graph commit HEAD --json > docs/graph-findings/pre-noon-review.json`.
   If the curveball hits Gate's own architecture, this file is the demo.

### At noon — do not code for the first 15 minutes

This is the step teams skip and it is worth points in two rubric rows.

1. **Close the morning session completely.** Start a fresh one. Do not paste the old transcript —
   reconstruct from the repository and from Entire.
2. Reconstruct: read `CLAUDE.md`, this plan (especially HANDOFF), `docs/how-gate-works.md`, then
   `entire checkpoint explain <id>` and
   `entire graph checkpoint <pre-noon-sha> --json` to see what the pre-noon commit changed at the
   entity level. Summarise in eight lines and confirm the summary is right before editing.
3. **Restate the constraint in one sentence** and name **which assumption of our design it breaks**.
   Write it down. This sentence goes into BUILDATHON.md verbatim.
4. **Run graph impact analysis on the area to be touched, before editing.** Screenshot it.
   `entire graph impact --repo . --symbol <X>` and
   `entire graph search --repo . --profile full --query "<constraint in our words>" --format json`.
   Save to `docs/graph-findings/curveball-search.json` and `curveball-impact.txt`.
5. **Run Gate on itself** over the pre-noon range to see dependents and coverage around the target.
   Dogfooding at the exact moment it matters.

### After noon — implement the smallest complete response

- Name which layer it lands in: `collect` / `index` / `signals` / `verdict` / `render`. State which
  existing behaviour must stay intact and which new tests prove the new behaviour. Estimate before
  starting.
- Work in 20–30 minute slices; `mise run test` and a commit after each.
- **Do not remove or weaken existing tests to make the build pass.** A green suite bought that way
  is the integrity failure the rules call out.
- Commit as `curveball: <short description>` — this is **Checkpoint 3**.
- Record a `CURVEBALL` section here: constraint verbatim, what changed, what stayed intact, how it
  is verified.

### Why our architecture should absorb this

The one-direction layering exists partly for this moment. Most plausible constraints land in one
pure layer:

| Constraint shape | Lands in | Insurance we already have |
|---|---|---|
| Output size / large PRs / context budget | `render` | byte-budgeted sections, `--all`, review order |
| Verdict semantics / stricter or softer gating | `verdict` | pure function, printed rule, four states |
| Partial or missing graph evidence | `verdict` + `render` | `unusable` (exit 5) already exists |
| Unsupported language / inventory-only files | `coverage` + `verdict` | `unchecked` is already first-class |
| New evidence source | new file under `internal/gate/` | signals are independent by construction |
| Performance / very large repo | `collect` | `--profile`, durable cache via `entire graph index` |

The exposed case is a constraint that hits `collect`, the only impure layer. That is what the
11:45 saved output insures against.

### What judges look for

A strong response preserves the core idea, changes a real architectural or reliability decision,
uses checkpoint context accurately, uses graph evidence, remains testable, and explains why the
revised behaviour can be trusted. "Show what changed, what stayed intact, and why the result can
be trusted" is the literal brief — answer those three in that order.

---

## HANDOFF — read this first if you are a fresh session

Written 2026-09-06 ~12:00, before the Noon Curveball. Everything below was
verified by running it, not inferred.

### Intent

Gate answers one question about an agent's change set: *should this survive?*
It emits a verdict, an exit code, and a ranked reading order, and every finding
comes from an artifact the agent did not write — the code graph for what depends
on the change, the test tree for what covers it. **Gate never asks the agent what
it did.** That is the whole product; §3 has the reasoning, including why an
earlier intent-checking design was rejected.

The second half of the idea is absence: `unchecked` is reported as its own
count, never folded into "verified". A gate with no test is not passing.

### Architecture — layer to file

| Layer | File | Pure? |
|---|---|---|
| collect | `internal/cli/gate.go` (298 lines) | **No** — the only layer that reads git, the snapshot, or the filesystem |
| index | `internal/gate/index.go` (161) | yes |
| signals | `internal/gate/risk.go` (99), `coverage.go` (102) | yes |
| verdict | `internal/gate/verdict.go` (83) | yes |
| render | `internal/gate/render.go` (159) | yes |
| contract | `internal/gate/types.go` (150) | yes |

Wiring touchpoints, three lines total: `internal/cli/root.go:76` (dispatch
`case "gate"`), `internal/cli/help.go` (commandDocs entry),
`cmd/entire-graph/main.go:23` (`errors.As` on `cli.ExitCodeError` so a verdict
sets the exit status without printing to stderr).

`internal/gate` deliberately does not import `internal/sem`: that would pull in
tree-sitter and CGO. It defines its own `Symbol` and `Relation`, and collect
projects the real records onto them. This is why its tests need neither git nor
a compiler toolchain.

### Done — with the command that proves each

- `entire graph gate --base <ref> --head <ref>` and `--checkpoint <id>`
  (the latter is nearly free: `sem.AnalyzeCheckpoint` already existed)
- **risk** — reverse dependency walk over 13 edge types, depth ≤2, `--hops 1|2`
- **coverage** — verified / unchecked / no-resolver, resolved from incoming
  edges whose source lives in a test file
- **review order** — dependents descending, ties broken by unchecked first
- text and `--json` renderers, `--all`, exit codes 0/1/2/5
- **degradation** — an unavailable dimension can never raise a verdict
- `gate --help`, registered in `commandDocs` and the root listing

```sh
go build -o entire-graph ./cmd/entire-graph
./entire-graph gate --repo . --base HEAD~3 --head HEAD      # 432 entities, verdict revert
./entire-graph gate --repo . --base HEAD~3 --head HEAD > /dev/null; echo $?   # 2
go test ./internal/gate/ -cover                             # 27 tests, 80.6%
```

### Not done

- **companion gap** and **clone drift** — cut from the morning as planned (§11).
  `SIMILAR_TO` (3,479 edges) and `FILE_CHANGES_WITH` (57) are already in the
  snapshot collect loads, so both are additive: a new file in `internal/gate`
  and one line in `collectGateReport`.
- **`UNASSERTED`** third coverage state (§11 deferred 1).
- **pre-push hook** — demo-critical, ~5 lines of shell, not yet written.
- **`--explain <n>`** — `RelationRecord.Evidence[]` is loaded but not surfaced.
- `docs/how-gate-works.md` does not exist yet.

### Known bugs and open risks

1. **FIXED — `DATA_FLOWS` was corrupting every dependent count.** Found by
   reading Gate's own output on this repo: `newStatsCollector` was reported as
   having `resolveRepo` and `unexpectedArgumentsError` as dependents, and
   neither calls it. The provider emits `DATA_FLOWS` with *data* direction, not
   *dependency* direction — `resolveRepo -> runStats`, reason "callee return
   value assigned to local and returned by caller". Reversed for a
   "who depends on me" walk that reads as the exact inverse of the truth, and it
   pulled every callee whose return value propagated into its caller's dependent
   list. Removing it took `runStats` from 156 dependents to 130 and
   `receiverCallRelations` from 6 to 4.
   **The lesson generalises: an edge type's direction is not automatically its
   dependency direction.** Check the `reason` field before adding a relation to
   `dependencyRelations` in `internal/gate/index.go`.
   Fixed alongside it: dependents reachable through two compound-v1 IDs were
   counted and printed twice (now `dedupeByLocation`), and five identical
   vendored-grammar parse warnings were burying the one warning about the change
   under review (now collapsed by code).

2. **Upstream nondeterminism, found by running Gate on this repo.**
   `internal/sem/analyze.go:1250` iterates a Go map in `compareEntities`, so
   when several deleted entities compete for one added entity the winner varies
   per run and `change_type` flips `added` <-> `renamed` on ~20 of ~400 entities.
   **The verdict is stable; the `--json` bytes are not.** Do not claim
   "byte-identical across runs" until this is addressed. Fix would be sorting
   the `deleted` keys before the rename loop — three lines, but in `internal/sem`,
   which this project's rules say not to modify. Current position: disclose it,
   and offer the fix upstream as a next step.
   Two determinism bugs *in Gate itself* were found and fixed the same way:
   parallel-snapshot edge order (now sorted at index build) and a Go map
   iteration in `verifyCommand` (now an ordered slice).

3. **Coverage is graph-derived, not search-derived.** The plan (§6.4) specified
   `SearchRepository` per entity for `CoverageNote`/`VerifyCommand`; at ~400
   entities that is minutes. It now reads incoming dependency edges from test
   files instead: cheaper, still agent-independent, but it misses a test that
   exercises code without a resolved edge. Disclose as a limitation.

4. **`TESTS` edges are unused** — 9 of 11,397 symbols on this repo (§6.4).

5. `verifyCommand` returns a whole-suite command, not a narrow one.

6. `dedupeByLocation` and `writeWarnings` have no direct unit tests yet, which
   is why package coverage moved 80.6% -> 78.9%. Both are exercised end to end
   by a real run; neither is pinned against regression.

### What running Gate on ourselves has already found

Kept as evidence that the tool works, and as demo material — none of it is
synthetic:

- Two determinism bugs in Gate itself (parallel edge order, map iteration).
- One correctness bug in Gate's edge selection (`DATA_FLOWS`, above).
- One nondeterminism in the upstream provider (`compareEntities`).
- One real finding in this repository's own history: the `stats` refactor
  changed `statsCollector`, `sessionAcc` and `runStats` — types and functions
  with substantial fan-out — and left them **unchecked**. Verdict: `revert`.

### Commands

```sh
go build -o entire-graph ./cmd/entire-graph          # needs CGO
go test ./internal/gate/                             # fast, no CGO needed
GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null go test -timeout 30m ./...
scripts/install-local.sh                             # install into the local Entire CLI
```

`mise` may not be on PATH; the env vars above are what `mise run test` sets.

### Git state — read this before assuming a baseline

**As of writing, nothing is committed.** `git log -1` is `3a2a715`, the upstream
merge this fork started from. All work is staged in the working tree. The only
checkpoint `entire checkpoint list` shows is the live session's `[temporary]`
one, which is not bound to a commit.

Two commits existed earlier and were deliberately soft-reset at the user's
request; they are recoverable from the reflog as `ca1bad4` (this plan) and
`0cd2c0f` (the gate implementation).

**Consequence:** the pre-noon checkpoint the guide requires does not exist yet.
Whoever reads this should establish the baseline commit before editing, so the
curveball response has something to diff against.

## 13. Demo (90 seconds)

**Pre-warm the cache before demoing** (`entire graph index --repo . --head --profile full`).
17.6 s of dead air destroys a 90-second demo. A 60-second screen capture recorded at 11:45 is the
fallback asset.

1. `git push` — **refused**. Verdict on screen. *(Requires the pre-push hook — see §11.)*
2. Point at an `unchecked` symbol with 14 dependents. *Nobody looked at this.*
3. `gate --explain 2` → the call path → judge opens the file → it is true.
4. `gate --json | shasum` twice → identical bytes.
5. Close: *"Every one of those findings came from the graph, git history, and the test tree.
   Gate never asked the agent what it did."*

Demo `continue` if only one verdict can be shown — it is the most common and the most honest.
`revert` looks staged.

**Demo owner should be whoever wrote the least code.** The guide requires every presenter to
understand the system; this forces it, and it is the strongest possible signal to a judge.

---

## 14. Known limitations — disclosed, not hidden

- **Dependent counts and CALLS resolution are heuristic.** Static relations can be incomplete;
  dynamic dispatch, reflection and generated code are invisible.
- **`TESTS` is unusable on Go in this repo** (9 edges / 11,397 symbols, measured). Coverage relies
  on the convention-based resolver inside `search`, which reports its own tier.
- **A test that asserts nothing still counts as covered.** `UNASSERTED` is designed but not yet
  built.
- **The companion-gap threshold (≥70%, min 5 observations) is hand-tuned**, not learned.
- **Verdicts are advisory.** Exit codes make them enforceable, but the evidence is heuristic and
  the tool says so in its own output.
- **`--profile full` costs ~17.6 s** on this repository. Larger repos will need the durable cache.
- Renames are reconciled by similarity threshold; a heavily rewritten rename may read as
  remove + add.

---

## 15. Risks

| Risk | Mitigation |
|---|---|
| Codex hooks unapproved → silent checkpoint loss on 15 points | 60-second verification (`codex exec` → commit → `entire checkpoint list` must be 1) before anyone builds. If it fails in 5 minutes, drop Codex. |
| Separate worktrees → commits not linked to the session | One clone, disjoint files. Otherwise `entire session adopt <id> --from <primary>` **before** the first commit. |
| Checkpoint links 404 at review | Test one in a signed-out browser before noon, not at 2:55. |
| Curveball hits `collect`, the only impure layer | The 11:45 saved Gate output is the fallback evidence. |
| Scope creep | Two signals working beats four half-working. |
| Time — 55 minutes to freeze | Checkpoint 2 needs *runnable*, not complete. |

---

## 16. How to run

```sh
mise run build                    # go build -o entire-graph ./cmd/entire-graph (needs CGO)
mise run test                     # go test ./...
scripts/install-local.sh          # install the plugin into the local Entire CLI

entire graph gate --repo . --base main --head HEAD
entire graph gate --repo . --checkpoint <id> --json
entire graph gate --repo . --base main --head HEAD --explain 2
echo $?                           # 0 keep · 1 continue · 2 revert · 5 unusable
```

---

## 17. Contract rules we must not break

Inherited from the upstream project and non-negotiable:

- Schema `1.x` is frozen and additive-only (`docs/adr/0001-ga-schema-contract.md`).
- The provider is **no-egress**: no remote fetches, hosted APIs, telemetry or runtime grammar
  downloads.
- `compound-v1` symbol IDs stay stable across ordinary edits.
- Unsupported or unparseable files surface as machine-readable partial failures, never silent drops.
- Do not modify `internal/sem` parsers or vendored grammars.
- The project was previously named `entire-sem`; do not reintroduce that name. **Entire Brain** is a
  separate downstream consumer, not an old name for this project.
