# AGENTS.md — operating guide for coding agents

Hand this to any coding agent working in a repo where the `entire graph` plugin is installed. It is the difference between the graph saving tokens and not: it moves you from grep/read exploration to graph queries, which is where most of a session's token budget goes.

## What this gives you

A precomputed, **deterministic** code graph is available through the `entire graph` command — functions, classes, methods, types, routes, and the calls/inheritance/field/service relations between them, parsed with tree-sitter, 100% locally (no network, no model, no keys). Use it to **LOCATE** and **UNDERSTAND** code *before* any grep / find / cat / whole-file read. Every command is no-egress and safe to run inside a sandboxed session. The same commit always yields the same graph, so once the graph shows you *where* code is, you can trust that and act — no need to re-confirm the location with a second tool. (That is a licence to skip re-grepping, not to skip checking that the edit you then write actually builds — see the doctrine below.)

Default flags to remember: pass `--repo .` when you're not inside an Entire session; the graph reads your **working tree by default** (your uncommitted edits are visible), and `--head` switches to committed-tree semantics with a cached, reusable index.

---

## The parts of the graph

Reach for the smallest tool that answers your question.

### 🔍 search — *find the code for a task* (your first move)
Ranked source regions for a plain-language description, with the source and `file:line` inline. Hybrid ranking over bodies, identifiers (camelCase/snake_case aware), signatures, paths, and graph neighbors. Output is budgeted (24 KiB by default) to drop straight into context.

```sh
entire graph search --repo . --query "<the task or bug in one plain sentence>" --format text --top-k 8
```

- `--format agent` for compact ranked output with latency telemetry; `json`/`ndjson` for the full schema (completeness, partial failures, diagnostics).
- `--top-k N` result count (default 10). It changes ONLY how many results come back — never the retrieval strategy or the meaning of `score`. `--deep` opts into the exhaustive sparse (BM25) pass fused with the semantic ranking: better recall on long tails, but it reads every eligible file and is much slower.
- `--max-context-bytes N` byte budget (`0` = unbounded, default 24576 — see "the budget is sized in turns" below).
- `--container-map`, `--signature-types`, `--type-card`, `--reference-blocks all` re-enable the three reference blocks that are OFF by default (env `ENTIRE_GRAPH_REFERENCE_BLOCKS` for a session). See "the reference blocks are OFF by default" below for the measurement behind the default.
- Working tree by default; add `--head` for committed-tree + cache reuse.
- `--profile syntax-only|fast|full` (default `fast`); `--index-all-files` or `--max-indexed-files N` to widen/bound cold-search parsing.

**Ranking priors you should expect (they are deliberate, not bugs):**

- **Source outranks non-source.** Prose documentation (`.md`/`.mdx`/`.rst`/`.adoc`/`.txt`, `docs/`, `website/`, `versioned_docs/`, README/CHANGELOG), vendored trees (`vendor/`, `node_modules/`, `third_party/`), generated artifacts (`dist/`, `single_include/`, lock files), serialized data and configuration (`.json`/`.yaml`/`.toml`/`.xml`/`.ini` — package manifests, command schemas, option tables) and `examples/` carry a **multiplicative** relevance prior below 1, so they must be clearly more relevant than the best source hit to outrank it. Nothing is filtered: a documentation hit still ranks first when it is the only match, and the prior switches off entirely when your query asks for that class ("update the **docs** for…", "fix the **example**", "regenerate the **dist** bundle", "the **yaml** **config** parses the wrong timeout"). Demoted hits are labelled with a `doc-prior` / `vendored-prior` / `generated-prior` / `data-prior` / `example-prior` signal.
- **Intent is read from the words you wrote.** The switch-off above triggers on words, not on fragments of identifiers you happen to quote: mentioning `NamedByteArrayTest.java` does not turn off the test-file demotion, and "a **regression** in routing" is a report about behaviour, not a request for the regression suite. Write "add a **test**", "fix the **docs**" when you do mean the artifact.
- **Near-duplicate copies are collapsed.** Two hits that are the same content in different files — versioned documentation trees, vendored snapshots, generated mirrors — are merged into the best-ranked copy, which then reports a `+N similar` signal. The freed result slots go to genuinely different code.
- **A hit is named after the smallest thing that contains it.** A matching region deep inside a 3,000-line class is attributed to the method it actually lies in, not to the class — so the class name cannot lend its score to every region in the file, and `symbol_name` describes what you are looking at.

**Snippets are allocated by rank, not spread evenly.** The **top 5 hits** come back as the
**complete body of their enclosing function/method** — snapped to the graph's own symbol
bounds, marked with the `complete-symbol` signal, and counted in
`stats.complete_symbol_snippets`. Those results need no follow-up read: `snippet_start_line` ..
`snippet_end_line` is the whole callable, verbatim. **Edit straight from the search output.**
To pay for that, results below the head are reduced to a two-line **locator** window (counted
in `stats.locator_snippets`) — still exact `file:line` + symbol identity, just not reading
material. Symbols too large to return whole (>160 lines) keep their focused window.

**Three blocks each replace a tool call you would otherwise spend a turn on.** They are the default
payload's reason to exist, and they are instructions rather than background reading.

- **SAME-CONCEPT LITERAL** (`literal_cluster` in JSON, `stats.literal_cluster_bytes`) is `grep`
  folded into `search`. When the top hit contains one distinctive literal that names the queried
  concept — an enum constant's value, an option string, a compound identifier — the block lists every
  occurrence of it in the repository, each annotated with its enclosing symbol and one of three
  roles: **`EDIT`** (a declaration or registry position — this is where a change to the concept
  lands), **`CONSUMER`** (inside a callable body; the code only passes or reads it, so it needs no
  change), **`DOC`** (prose or serialized data). The header states the repository's own totals
  (`N in M files repo-wide`), so **this IS your sweep** — fix the `EDIT` sites, ignore the
  `CONSUMER` ones, do not grep for either. The block refuses far more often than it fires: it needs
  a literal that shares a word with your query, that occurs in few enough files to be a concept name
  rather than a lexical magnet, and whose repository-wide total is EXACT. A total it cannot know
  would destroy the block's only value, so in that case there is no block. Occurrences inside source
  the payload already printed are counted but not repeated.
- **VERIFY** (`verify_command` in JSON) is the narrowest test invocation for the file the top hit is
  in, derived from the repository's own build evidence — a `Cargo.toml` workspace member, a `go.mod`
  module root, a `pom.xml` module, a `package.json` test runner, a `phpunit.xml`, a pytest config, a
  `Rakefile`/`.rspec`, a `Makefile` target — plus the test file the payload identified. That test
  comes from the COVERING TEST block when there is one, and otherwise from a ranked hit that is
  itself a test file (`derived_from` says which), because the covering-test block deliberately
  declines to reprint a test the ranking already shows — and a payload printing the test at rank 1
  is the last place that should lose its narrow command. A conventional mirror path that exists is
  the final fallback. It carries the command, what it targets and `derived_from` so you can judge it
  instead of trusting it, and it is always runnable from the repository root (it carries its own `cd`
  when the manifest is not at the root). **Silence is the design:** when the build files do not
  license a narrow command there is no block, because a wrong command costs strictly more than none —
  you run it, read a failure about the invocation rather than the code, and do the discovery anyway.
- **CLOSED-SET WARNING** (`closed_set` in JSON) fires when the top hit is, or belongs to, a closed
  variant set: an `enum` (Java, Kotlin, C#, C, C++, Rust, PHP, TypeScript, Swift), a TypeScript/Flow
  string-union alias, a Java `sealed ... permits` hierarchy, or a Go typed const group. It names the
  switch/match sites over that set — found from their ARMS, which is direct textual evidence and
  needs no type inference — and reports for each whether it is exhaustive, what its fall-through arm
  does (`throws` / `absent` / `silent`), and whether a missing arm is caught by the **compiler** or
  only at **runtime**. Rust `match` without a wildcard, an exhaustive Kotlin `when` expression and a
  TypeScript `never` assertion are compiler-checked; `switch` in Java/C#/Go/JS/TS/PHP/C/C++ is not.
  The block exists ONLY for the runtime cases: adding a variant there compiles and then throws in
  production, so if you are adding one, add the missing arm before you finish. Its silence on a Rust
  `match` is a verdict, not a gap.

**The reference blocks are OFF by default.** Six session-level runs on real agents added the
container map (1119 B), the signature-type block (197 B) and the declaration card (300 B) to the
first search response: turns went UP (23.1 vs 21.0 on one model, 30.1 vs 26.8 on another), cost rose
14–19%, and resolve rate was flat to worse, while a leaner competing tool answered the same prompt in
18.5 turns for less money. The discriminator was not how informative a block reads — it is whether
the block REPLACES A TOOL CALL the agent was going to make. These three answer questions an agent was
not about to ask, and their bytes are replayed on every later turn. They are kept, and kept tested,
for interactive human reading: `--container-map`, `--signature-types`, `--type-card`, or
`--reference-blocks all`; `ENTIRE_GRAPH_REFERENCE_BLOCKS=all` sets it for a whole session. Everything
below about those three blocks describes what you get when you ask for them.

**The top hit comes with a CONTAINER MAP, so a range read can be sized without opening the
file.** *(off by default; `--container-map`)* Ahead of the ranking the payload carries `container_map`: the file's total line count,
the enclosing container (class / struct / module / file top level) with its own line range, its
data members collapsed to `name:Type`, and every other member as `start-end name(params)` plus at
most two structural flags — with the hit's own member marked. It prints **no source**: every
region in the payload appears exactly once, and the map is an index, not a second copy.

This is the one place a member-ranked list can tell you about members it cannot rank. A private
nested enum whose constants are the variant set an issue asks you to extend never wins a
relevance contest against the method that consumes it, but it appears in the map as
`213-282 enum Ops  NESTED,PRIVATE  +12 members` — an exact range you can read. Bounded to 1.5 kB
(~1% of one agent turn) and reported in `stats.container_map_bytes`; when a container is too wide
for that, parameter lists go first, then flags, then rows furthest from the hit, and the block
says how many members it dropped. Names, line ranges and the file extent survive every stage.

**Results are grouped, and the groups answer different questions.** Every hit stays in
`results` with its rank; a `section` field says how to read it, and the text renderer prints
each group under its own header.

- **(no `section`) — candidate fix sites.** The ranked answer to "where is it?".
- **`section: "related"` — RELATED SITES.** Not a second ranking: the other places the change
  usually has to land, one graph hop from the head of the ranking. Each entry is a one-line
  locator (`file:line`, symbol, and a `related:<kind>` signal saying why): **near-dupe** — a
  near-duplicate body, which needs the *identical* edit; **sibling** — the same member on a
  sibling implementation, or a member declared beside the anchor in a small unit; **caller** —
  an incoming call, reported at the **call site**, which needs adjusting to a changed contract.
  Check the block before you finish: a patch applied to one site of a family is the commonest
  way a correct fix still fails review. The block is funded out of the tail of the ranking, so
  it costs no extra bytes and never displaces the head or the only mention of a file; its size
  is in `stats.related_sites`.
- **`section: "docs-and-fixtures"`.** Hits that matched your words but hold no program text
  (prose, HTML templates, changelogs, serialized config, recorded fixtures). They are never
  suppressed, dropped or re-ranked — a fixture or a rule document is sometimes exactly the file
  that has to change — but they are not presented as fix sites, so do not spend a read there
  looking for the bug. When a payload has *nothing but* non-code hits, they stay the primary
  list: they are the answer.

**The types in the top hit's signature come with it.** *(off by default; `--signature-types`)* A located symbol is not usable on its
own: `Edit::range_replacement(content: String, range: TextRange) -> Self` says nothing about what
else can build an `Edit`. A **TYPES IN THIS SIGNATURE** block (`signature_types` in JSON) resolves
the types named in the anchor's OWN signature — the declaring type first — to their declaration,
and lists their fields plus their members' SIGNATURES, no bodies, capped per type with the omitted
count. It is deliberately **not transitive**: in a language with rich generic types the transitive
closure is hundreds of kilobytes, replayed on every later turn. It is **funded, not added** — it
is only seated when redundant tail locators can be displaced to pay for it, so a payload carrying
the block is never larger than the same payload without it, and the last mention of a file is
never displaced.

**The top hit also comes with the test that covers it and the declarations its body uses.** A
**COVERING TEST** block (`section: "covering-test"`, counted in `stats.covering_tests`) carries the
existing test that exercises hit 1 — the statement of what your fix has to *achieve*, which the
ranker deliberately demotes and which is therefore the one entry in the payload whose file nothing
else names. It is labelled away from the fix sites, prints no relevance score (it is not a ranked
answer), and is always appended after every ranked hit, so it can never be read as "where do I
edit". It stays ON by default, both on its own measured merit and because the VERIFY command is
derived from its path. A **DECLARATIONS** block (`type_card` in JSON, `stats.type_card_entries`,
*off by default; `--type-card`*) is the compact answer to "what is this identifier" for the names
hit 1's body uses — one line each, with the lines that body uses them on, which a snippet full of
*uses* never tells you. Neither is a fix site.

**One budget covers every one of those blocks, and it yields in a fixed order.** Seven blocks can
live outside `results` — closed set, container map, literal cluster, verify command, signature types,
declarations, covering test — and they all spend the same scarce thing: bytes replayed into the model
on every later turn. There is ONE ceiling, `--max-context-bytes`. The ranking, the covering test, the
declaration card and the signature types are funded from inside it; the container map, the literal
cluster, the verify command and the closed-set warning are additive and each separately capped
(1.5 kB / 560 B / 320 B / 420 B), because a payload that spent its budget on complete head bodies
must not lose one to buy a navigation aid or a warning. `stats.context_block_bytes` reports the total
of everything outside `results`, and each block reports its own cost (`container_map_bytes`,
`signature_type_bytes`, `type_card_bytes`, `literal_cluster_bytes`, `verify_command_bytes`,
`closed_set_bytes`), so the price of every section is attributable rather than emergent. The extra
file reads the three agent-asked blocks need are reported separately too
(`files_content_read_for_context_blocks`), because the query read counters answer a different
question — how tightly selective indexing bounded the ranking's own reads.

Section order, which is a *reading* order — be warned, navigate, edit, check the goal, check how to
prove it, sweep the concept, check the contract, check the names, check the neighbours:

```text
LOW CONFIDENCE  ->  CLOSED SET  ->  [CONTAINER MAP]  ->  candidate fix sites  ->  COVERING TEST
                ->  VERIFY  ->  SAME-CONCEPT LITERAL  ->  [TYPES IN THIS SIGNATURE]
                ->  [DECLARATIONS]  ->  RELATED SITES  ->  DOCS & FIXTURES
```

(`[bracketed]` = off by default.) Two placements are load-bearing rather than aesthetic. The
closed-set warning comes FIRST of the content blocks: it is the only block that changes what the
patch has to *contain*, and a warning read after the edit is written has already failed. VERIFY sits
immediately after the COVERING TEST it is derived from — what the edit has to achieve and the command
that proves it are one thought. The rest are all about hit 1, so they stay together and stay ahead of
the last two, which are about other places; the signature types precede the declaration card because
a signature is what callers can see and your patch must not break, while the card is about
identifiers internal to the body.

When bytes run short, sections yield in this order, and the rule behind it is **a block yields in
proportion to how cheaply you could get it back** — bytes buy avoided turns, so what survives is
whatever costs the most extra tool calls to replace:

| yields | section | why it goes first / survives |
|---|---|---|
| 1st | **DECLARATIONS** | every entry is one `def NAME` away, and it is the block loosest to the edit — it names identifiers the snippet already shows in use. Sheds its last entry first, so pressure shortens the card before removing it. |
| 2nd | **CONTAINER MAP** | reproducible with one `def`/`neighbors` on the container, and being the additive block it is the only one whose loss returns bytes without costing the ranking. Degrades full → names-and-ranges → absent. |
| 3rd | **TYPES IN THIS SIGNATURE** | one `def` per named type recovers it, and at ~286 B median there is little to win by dropping it. Shrinks per-type member lists (15 → 8 → 5 → 3 → 2) before dropping entries. |
| 4th | **COVERING TEST** | yields **last**. It is the only block naming a file and range nothing else in the payload names, so replacing it costs a search *and* a read — and it is a single entry, cheap to keep. |
| never | **candidate fix sites** | ranks 1..5, plus the last mention of *any* file at any rank. A location you never see is a file you never open, and no other block can compensate for that. |

Truncation is honest, never silent: a block that does not fit is **smaller** first (fewer entries,
narrower windows, shorter member lists) and says what it omitted; only a block that cannot shrink
further is dropped whole, and its `stats` counter then reads 0 so the absence is visible. No block
is ever quietly traded for another.

**The relation commands have a separate budget — do not add the two together.** `neighbors` and
`impact` spend bytes on call-site guard blocks, capped independently of the search payload: **6
guards** per chain, a **10-line** window around the call, **3 blocks** per response, **2000 B**
total, and `impact` quotes no source at all. That is a different command with a different response,
so a single combined "context bytes" figure across search and relations would be meaningless.

**The budget is sized in turns, not in bytes.** A search payload is ~0.6% of what a session
spends; one extra agent turn is ~42.5k tokens, because 95.9% of billed tokens are context
re-read. A search that stops one Read short of an edit therefore costs about 40x the whole
payload that caused it. The 24 KiB default exists so the ceiling is never the reason a head
result comes back as half a function — it is a ceiling, not a target: payloads only grow to
buy complete head bodies, and the allocator always picks the cheapest plan that delivers them
(measured across 14 repos: mean payload 11.8 KiB, half the ceiling). Pass
`--max-context-bytes` to tighten it; bodies then degrade to focused windows, shallowest ranks
kept last.

**A weak payload says so.** Every ranked block carries its relevance score (`s=<n>` in
`--format agent`), and when the top score is weak AND the head of the ranking agrees on
nothing, the payload is prefixed with a `LOW CONFIDENCE:` line. Calibrated over 14 repos in 9
languages: it never fired on a query whose target existed, and caught 64% of queries naming a
technology the repo did not contain. Treat it as "check that this repo really does what you
asked about before editing", not as "no results".

**In practice it almost never fires.** Measured over 143 real benchmark sessions on issue-derived
queries: **0 firings**, because the lowest top score observed was 20.3 against a ceiling of 12.0. It
is calibrated for queries naming things the repo does not contain, which is not what a bug report
looks like. Do not treat its silence as confirmation that the top hit is right.

**When:** the start of essentially every task. One good query lands you on the fix area.

### 🧾 def — *what is this name, and what can I do with it* (structural declaration lookup)
One declaration, with everything the graph structurally attaches to it. For a **type**: its fields and the SIGNATURES of its associated functions and methods — including the ones written outside the type declaration, which is where most languages actually put a type's API. For a **method**: its owning type, so `deletion` reports as `Edit::deletion`. For a **trait/interface**: who implements it.

```sh
entire graph def --repo . Edit          # fields + impl surface of a type
entire graph def --repo . Edit::deletion  # a member, with its owning type
entire graph def --repo . Ranged       # a trait, with its implementors
```

- Membership is **structural, never a name match**. A member belongs to a type because a `CONTAINS` edge or a `container_id` says so, or because the type acquires it from a supertype one hop up. Asking for `Edit` can never return members of `Fix` because `edits` happens to contain `edit`.
- The member set is joined across every place a language writes it: Rust inherent `impl` and `impl Trait for Type` blocks, Go receiver methods, Swift extensions, C# extension methods and further `partial` declarations, Kotlin extension functions/properties, PHP `use SomeTrait`, Ruby `include SomeModule` and `def self.name`. Acquired members are labelled `[via Super]`, extension members `[ext]`, and a member that satisfies a trait/interface declaration `[impl Trait]`.
- Inheritance is followed **one hop only** and never transitively: the point is this type's surface, and a transitive walk in a deep hierarchy is unbounded.
- Ambiguous names list each declaration separately (bounded, with a count). Partial declarations of ONE type are merged; two unrelated types that share a name are not.
- `--members N` caps each member list (default 15, truncated lists report the omitted count); `--max-context-bytes N` is a ceiling that shrinks member lists before dropping the identity line; `--file`/`--line`/`--kind` select one declaration; `--format text|json`.

**When:** you have a type or member name and need its API before writing the patch — instead of opening its file. Not a routine follow-up to every search: search's own `TYPES IN THIS SIGNATURE` block already carries the surface of the types the top hit's signature names.

### 🕸️ neighbors — *who calls this / what does it call* (targeted relations)
Direct incoming/outgoing relations for **one** symbol, with definition locations, plus bounded two-hop paths at `--depth 2`. For the full blast radius of a change, prefer `impact`; use `neighbors` when you want one specific relation/direction. Never `edges` for this (full stream).

```sh
entire graph neighbors --repo . --symbol NAME --relation CALLS --direction in   # who calls NAME
entire graph neighbors --repo . --symbol NAME --relation CALLS --direction out  # what NAME calls
```

- Ambiguous names (multiple defs) return the definition list only, each line ending in the exact selector that picks it: `--symbol NAME --file <path> --line <n>`, plus `--kind <kind>` when two records sit on the same line. **Copy that selector verbatim.** `--file` alone is not enough when two definitions share a file, and a qualified `--symbol` is not enough when they share a qualified name; name + location + kind always separates them. `--symbol <file>:<line>` is also accepted as a positional shorthand, but it selects EVERY definition on that line.
- `--symbol` also takes a definition's stable `compound-v1` ID — the `id` field of `symbols --format ndjson`. An exact ID match beats every other filter (it already encodes the file and the kind, so a stale `--file` cannot veto it) and it is the only selector that stays valid after an edit shifts line numbers. Use it when you are holding an ID already; the ambiguity listing above prints selectors instead, because an ID restates the path and name that line already shows.
- `--relation CALLS` (default is the call family) — pick another relation to follow it instead.
- `--direction both|in|out`, `--depth 1|2`, `--limit N`.
- `--internal-only` drops unresolved external endpoints; `--exclude-tests` drops test-only neighbors.
- `--format agent|text|json`; `--head` for cached committed-tree; `--profile fast` for shallow call resolution (default `full` favors correctness).

**A caller is reported at its CALL SITE, not at its definition.** `- expression
(checkers/ast/analyze/expression.rs:476, def :24)` means the call is written on line 476 inside a
function that starts on line 24. Go to the first number; the second is only there so you can find
the caller as a symbol. (When the two coincide, only one is printed.) Repeated calls of the same
callee in one caller are reported as `+N more call sites`.

**Incoming calls also come with the conditions in force at the call** — the enclosing `if let` /
`match` / `else if` / arm-pattern chain, verbatim with line numbers, plus a ~10-line window around
the call. That block is what tells you which branch you are in and what the call's inputs are
already narrowed to, which is usually the thing a patch has to stay correct under. The chain is
quoted, never summarized: no invariant is asserted for you. The caller's **body is never inlined**
(a dispatch function can be thousands of lines); the window is bounded and so is the total.

**A direction you did not ask for is reported as not queried, never as empty.** `--direction in`
prints `Callees: not queried (--direction in)`. Use `--direction both` when you want both halves —
it costs nothing extra once the index is loaded.

**The focus line labels both of its numbers:** `Focus: name (file.rs:781) def=781 span=781-848`.
`def=` is the line the definition starts on; `span=` is the range it covers, which is what a ranked
`search` result prints for the same symbol. They are one fact, not two.

**When:** "what breaks if I change X", "who uses this", tracing a call chain — after search has given you a concrete symbol name.

### 💥 impact — *one-shot blast radius for a change*
Everything the graph knows about changing **one** symbol, in a single bounded explanation: direct + transitive callers (depth ≤ 2), callees, type consumers (`USES_TYPE`/`PARAM_TYPE`/`RETURNS_TYPE`), data flows, files that historically change together with the symbol's file, and same-container siblings. Text output is sectioned, `file:line` per entry, capped per section and ~4 KB total.

```sh
entire graph impact --repo . --symbol NAME|<file>:<line> [--file path] [--line n] [--kind kind] [--depth 1|2] [--format text|json]
```

- Ambiguous names return the definition list, with a working selector printed beside each one (see `neighbors` above).
- Callers matched only by NAME in a file that holds no program text (a design doc, a changelog, a recorded fixture) are labelled `[doc-mention, name_only]`, sorted behind every resolved caller, and never expanded transitively — a document cannot break when behavior changes, so treating it as an intermediate invents callers that never mention the symbol.
- `--limit N` per-section entry cap; `--max-context-bytes N` total text budget; `--exclude-tests`; `--head` / `--profile` as in `neighbors`.
- Callers are reported at their **call site** with the definition line alongside, exactly as in
  `neighbors`. `impact` stays a bounded overview and does **not** quote source; for the conditions
  around a specific call, run `neighbors --symbol X --direction in`.

**When:** before changing behavior of a specific function/type — "you're changing ordering: here is every place results are ordered, limited, or consumed downstream" — one command instead of chaining neighbors + edges + git log.

### 📇 symbols — *definitions*
Full stream of symbol records (stable `compound-v1` ID, kind, qualified name, source range, signature, language, container). This is a **bulk NDJSON stream of the whole repo**, filtered to the symbol record type — there is **no positional name argument** and no server-side name filter; grep the stream client-side, or prefer `search`/`neighbors` for a targeted single-symbol lookup.

`container_id` is set from a symbol's qualified-name prefix where the source spells one out, and
otherwise from **lexical containment** — the smallest symbol of the same file that strictly
encloses it. That second rule is what makes a nested declaration a member of the thing it sits
in: a Java/C#/Kotlin nested or inner type, a Python or Ruby inner class, a Go or Rust type
declared inside a function, and the method set of a JS/TS object literal all take their name
verbatim from the source, so none of them names an owner. The CONTAINS relation says which rule
applied (`symbol qualified name is nested in container` vs `symbol is lexically nested in
container`).

```sh
entire graph symbols --repo . --format ndjson [--worktree]
```

**When:** you need the complete definition inventory (e.g. ingesting into a store), not a single lookup.

### 🔗 edges — *relations*
Full stream of relation records across all 30 types (`CALLS`, `IMPORTS`, `EXTENDS`, `HANDLES_ROUTE`, …), each tagged with resolution and confidence. Like `symbols`, this is the **whole-repo stream** — there is **no `--to`/`--from`/`--relation` filter**; for one symbol's callers/callees use `neighbors`, not `edges`.

```sh
entire graph edges --repo . --format ndjson [--worktree]
```

**When:** you want every relation (bulk export / ingestion). For a targeted question, use `neighbors`.

### 🗺️ snapshot — *the whole graph*
One header record, then file, external-endpoint, symbol, and relation records, streamed so memory stays bounded. Superset of `symbols` + `edges` + files.

```sh
entire graph snapshot --repo . --format ndjson [--worktree]
```

**When:** ingesting the full graph into agent memory or a store such as Entire Brain.

### 🧬 diff / analyze / commit / checkpoint — *what changed + risk*
Entity-level change list (added / removed / renamed / signature-changed / body-changed) with a heuristic **dependent count**, so a signature change with many dependents stands out.

```sh
entire graph commit HEAD --json                     # a commit vs its first parent
entire graph diff --base main --head HEAD --json    # between two refs (analyze is an alias)
entire graph checkpoint <id> --json                 # the commit behind an Entire-Checkpoint trailer
```

**When:** judging whether a change is safe to keep / revert / continue, or reviewing a branch/PR. High dependent counts on a signature change = run tests first.

### 🏗️ index — *build / warm the cache*
Prebuilds the durable, query-independent committed-tree index and verifies it was written, before latency-sensitive work.

```sh
entire graph index --repo . --head --profile full --cache-dir /path/to/cache --format json
```

**When:** once, up front, on a large repo before a batch of `--head` searches/neighbors queries. Re-running it is also how you "refresh" a committed-tree cache — same tree hits, changed tree rebuilds.

**Budget the first relation query, not the rest.** `search` parses only the files its query
preselects, so it is fast cold. `neighbors` and `impact` need the whole call graph, so they index
the **whole repository** — tens of seconds on a large repo. That cost is now paid once: a working
tree byte-identical to HEAD reuses the tree-keyed index, so the second and later relation queries
are sub-second (measured on ruff, 4,340 files: 27s cold, 0.8s warm, and `impact` warms off
`neighbors`' index). A dirty working tree deliberately keeps re-indexing, because a cached
committed-tree graph would hide your uncommitted edits. Two things to know: pass `--cache-dir` (or
set `ENTIRE_PLUGIN_DATA_DIR`) or there is nowhere to store it and every call is cold — the output
says so when that happens; and do NOT batch several questions into one command to "amortize" the
index, which costs more turns, not fewer.

### 🧭 capabilities / doctor / version — *feature-detect*
```sh
entire graph capabilities --json    # semantic vs inventory-only languages, relation types, features
entire graph doctor --json          # environment, repo resolution, no_egress=true
entire graph version [--json]       # provider name + plugin version
```

**When:** before assuming a language is semantically parsed, or to confirm the no-egress environment.

### 📊 stats — *did the graph actually save anything?* (for humans, not for you)
```sh
entire graph stats --repo . [--since 30d|7d|all] [--format text|json] [--sessions-dir path|--transcript path]
```

Local, read-only report over the coding-agent session transcripts already on disk
(`~/.claude/projects/<path-slug>/*.jsonl`; `--sessions-dir` overrides the lookup). Reports graph
calls per verb vs. exploration calls (`Read` whole-file / `Read` line-range / `Grep` / `Glob` /
shell `grep|find|cat|head|tail|sed|awk`), the bytes each path pulled into context, billed session
tokens read from transcript `usage`, a graph-first rate (share of sessions whose first locate-ish
tool call was a graph call), and an **estimated** token saving. The savings model is an explicit
assumption printed next to the number: each `search`/`neighbors`/`impact` call is credited with the
one whole-file read it replaced — on-disk size of the top-hit file it pointed at (repo median
tracked-file size when unresolvable) minus the bytes that call returned, floored at 0, at 4 bytes =
1 token. It is not a measured counterfactual. No network, no writes. `--transcript <path>` narrows
the whole report to one session (that transcript plus its `<session>/subagents/*.jsonl`), which is
what `scripts/entire-graph-statusline.sh` renders as a live Claude Code status line badge.

**When:** a human asks what the graph is buying them. Agents should not run it as part of a coding task.

---

## The agent prompt (copy-paste this)

Give this to any coding agent that has `entire graph` available — substitute your search
invocation for `<search-cmd>`:

This is the wording the harness measures (`agentic-swebench/tools/run_3arm.sh`,
`ops_clamp_eg_prompt`, `PROMPT_FAMILY=briefed`). Keep the two in step — a prompt that promises
blocks the tool no longer returns is worse than no prompt.

```text
A precomputed code graph is available: <search-cmd> . Use it to LOCATE before any grep/find.
Your FIRST action is ONE search:
  <search-cmd> "<the bug in one sentence>"
Then go straight to the top hit and edit from the body it printed — do not re-read the file to
confirm what you were just shown, and do not re-search to 'make sure'. Reach the edit in as FEW
turns as you can: every turn re-reads your whole context, and that replay is what this task costs.
The result also hands you three things that each replace a round-trip you would otherwise spend:
  SAME-CONCEPT LITERAL - every place this concept is spelled out, each tagged EDIT / CONSUMER /
      DOC. This IS your sweep: fix the EDIT sites, ignore the CONSUMER ones. Do not grep for them.
  VERIFY - the narrowest command that exercises the file you are changing. Run it ONCE when your
      edits are in. Read the error, fix exactly what it names, re-run at most once. Never hunt a
      green suite, never write a throwaway test script. A patch that does not build fails the
      whole task.
  CLOSED-SET WARNING - when you are adding a variant to an enum/sealed set, the switches over it
      that will throw at RUNTIME rather than fail to compile. Add the missing arm before you stop.
Because the search already named every location — the fix site, the EDIT-tagged literals, the
related sites — none of your reads or edits depend on each other. Ask for them ALL IN ONE MESSAGE:
every range you need to see, then every edit you need to make. That is the difference between two
turns and ten, and turns are the entire cost.
Then STOP. No further searching, no grep to double-check, no re-reading your own edits.
If the top hit is clearly wrong - a LOW CONFIDENCE marker, or a body that does not match the issue -
search ONCE more with different words. If that misses too, fall back to the shell in ONE batched
call rather than staying stuck.
```

**What was measured. The 54.9% figure is WITHDRAWN.** It was measured with a frugality clamp on the
graph arm against a baseline that received *no working-policy instructions at all*, and the same
configuration resolved **131/300 (43.7%)** against the baseline's **150/300 (50.0%)** (McNemar
p=0.013) — it lost on accuracy while the headline claimed parity. An adversarial audit of the harness
then found eight further defects, every one of which distorted the comparison rather than the tool:
the cmm arm was missing three discipline clauses the other arms had, its source snippets were clamped
to 6 lines while ours returned whole function bodies (19.4 lines mean), a concurrency fix had been
applied to our arm only, tokens were summed from a lossy accumulator that undercounted cmm ~25%, and
a grading race submitted 7 real graph-arm patches as empty and 0 baseline patches.

**The 31.6% figure is ALSO withdrawn** — it was measured with a 119-word operating-rules paragraph
in the graph arm's prompt that the comparison arm never received, *on top of* a frugality block all
three arms already shared. Removing that redundant restatement moved the same cell from −18.9% to
+26.9%: a ~45-point swing from prompt text. The advantage was our prompt, not our index.

Current measurement — 48 language-stratified instances × **3 replicate runs** (144 matched
instance-runs), matched prompt discipline in every arm, billing-truth tokens, Haiku:
**−17.4% total tokens** (CI [−27.8, −6.5]), **−32.0% geomean** (CI [−41.5, −21.2]), **−11.8% USD**,
resolving **54 vs the baseline's 57** of 144 (McNemar p=0.42 — a tie), so **cost per resolved issue
−6.9%** ($0.539 vs $0.579). Against codebase-memory-mcp: **−22.8%** total, **−27.1%** geomean.
cmm does not measurably beat no-tool either (+7.0% total, CI crosses zero).

Quote the **per-resolved** figure, not the raw token cut: a token saving that costs resolves is not
a saving.

**The most robust result is retrieval**, because no agent is in the loop: the file the gold patch
edits reaches the payload in **96.3%** of sessions (52/54) against cmm's **81.5%** (44/54), 10
exclusive wins to 2, sign test p=0.0386, at comparable bytes and with fewer search calls (81 vs
112). It is an end-to-end arm comparison, not a retriever-isolated one — the arms wrote different
queries — and part of cmm's deficit is our wrapper's own hit cap.

**Outside Haiku we cannot measure the effect at all.** Re-running a byte-identical configuration
moves total tokens by up to **±20%**; at 80% power the minimum detectable effect is **31% at n=29,
59% at n=10, 93% at n=5**. Sonnet (+0.9% geomean, CI [−33, +41]), Opus (−6.4%) and Fable (+6.7%)
are all inside that noise, so treat them as unmeasured rather than as results. Break-even on turns
is near a 20-turn baseline: the graph pays by deleting locate turns, and a capable agent on a small
task never spends them.

**A ≥35% saving is refuted, not unproven**: the favourable CI bound on total tokens is −27.8%.

**Correction (measured later, 3 runs on 50 language-stratified instances, Haiku).** The block
above says "do NOT re-search or grep to 'confirm'". That is right for the common case and wrong for
the tail, and the tail is expensive. Of 135 instance-run pairs, **8 (6%)** were sessions where the
no-tool baseline finished comfortably and the graph-assisted agent hit the 50-turn cap at **2.2x the
baseline's tokens**. Those 8 alone cost **4.8 points** of the headline token saving (-33.5% with
them, -38.3% without). Cause, from the wrapper call logs: they ran a mean of **8.4 searches** against
2.7 for normal sessions (worst case **23**), each a near-identical rephrasing — e.g. "zero padding
applied to infinity and NaN" then "zero padding NaN format spec" then "write nan padding zeros".
The agent used search as a synonym generator because nothing told it when to stop.

Hence rule **2a** in `entire graph agent-guide`: **two searches maximum, then switch tools** — grep
for a literal from the issue (error text, identifier, flag, rule or error code, a constant) and read
around the hit. Distribution over 142 sessions: 59.2% use exactly ONE search, 87.3% use <=4, and the
>4 tail averages **38.5 turns against 22.2** for the rest. Search is how you start; it is not how
you recover. A worked case: ruff `SIM201` — the gold file `flake8_simplify/rules/ast_unary_op.rs`
ranked outside the top 20 in *every* ranking configuration tried (higher top-k, a fixture-class
prior, a wider preselection pool), and a single `grep SIM201` returns it in 4 hits.

Paired analysis of the 31 losses where the baseline fixed the bug and the graph-assisted agent did
not, both having found the correct file, shows the clamp was the cause: the graph agent ran **zero
builds or tests on 22 of the 31** (baseline ran them on 26/31) and made a **single edit on 22/31**
(baseline 8/31), and two of the losing patches could not compile at all (a left-behind
`declared and not used` variable; a member access missing its required index). Rules (5) and (6)
above exist to remove that cost. The savings figure has **not** been re-measured with them — nor
with search's later change to return the **top 5 hits as complete function bodies**, which
removed the "then open the top hit's file" Read (~3.8 turns per session at ~42.5k tokens each).
The clamp prompt exactly as measured is preserved in the graphmark repo for reproduction only —
do not use it for real work.

For bug-fix/locate tasks, run search at `--profile full` (call-graph expansion active) with default
text output (tiered: full snippet for the top hits, terse locators after). Measured detail that
matters: chaining `search -> def -> callers` to "explore the tool" was the #1 measured token
waste — prefer the search-only fast path above, then verify. (Full methodology, prompts, fairness
controls and caveats: the graphmark repo, `agentic-swebench/REPRODUCE.md` +
`BEAT-CMM-VERDICT.md`.)

## Operating doctrine (the token-saving rules)

1. **Search first — always.** Your first move on any task is one `entire graph search --query "<task>"`. Do **not** grep / find / cat to locate code before you have searched. Exploration is where ~90% of a session's tokens are wasted.
2. **Then narrow, only as needed.** Search exposes concrete identifiers → use at most one `impact --symbol X` (blast radius) or read the returned line ranges. Don't fan out.
3. **Trust the graph.** Once search or neighbors shows you the function and its source, **edit it**. Do not re-read the whole file or re-grep to "confirm" what the graph already showed — the graph is deterministic.
4. **Never read a whole file to explore.** If you must read, read the line range around the symbol. To understand a type/class, query it — don't open its file.
5. **Impact = one targeted query.** For "what breaks if I change X", use `neighbors --symbol X --relation CALLS --direction in` — not a whole-graph `snapshot`/`edges` dump, and not a repo-wide grep.
6. **Minimise turns — in discovery, not in verification.** Token cost is roughly turns × context, so prefer one precise query over three broad ones and stop *discovery* once you can defend the edit with a focused hypothesis. Turn economy applies to finding code; it is not a licence to skip the check that your edit builds.
7. **Complete the fix.** A fix is often not one edit in one place. The **SAME-CONCEPT LITERAL** block is your repo-wide sweep — fix its `EDIT` sites, ignore its `CONSUMER` sites, and do not grep for either; its header states the repository's own totals, so when the block is there you have seen the whole set. For structural neighbours (callers, siblings, near-duplicates) the RELATED SITES block is already in the payload; one `impact --symbol X` covers anything it missed. Measured: single-edit patches were 22 of 31 paired losses (baseline 8/31).
8. **Verify once — always, with the command you were given.** Run the **VERIFY** line the search printed; when there was none, compile what you touched or run the nearest existing test, at the narrowest scope that would still catch a syntax, type, name, or arity error. Measured: the clamped agent ran zero builds/tests on 22 of 31 paired losses, two of which could not compile. One verification turn is far cheaper than a wrong patch. Measured separately: test/build accounts for 3.79 turns per session, much of it spent finding the right invocation rather than running it — which is what the VERIFY block removes.
8b. **Adding a variant? Read the CLOSED-SET WARNING first.** When it reports a switch/match site as `checked at runtime`, add the missing arm before you finish: that failure is a runtime throw, not a compile error, so verification will not catch it either. The block only appears when the compiler would not catch it.
9. **Verify, don't chase.** Verification is bounded: run it, read the error, fix exactly what the error names, re-run — a couple of iterations, not fifty. Do not enter an edit→test→edit loop hunting a green suite, and do not "fix" failures that predate your change.
10. **Feature-detect before you trust.** If a language might be inventory-only, check `capabilities --json` first — inventory-only files have file records but no semantic relations.
11. **Read the `Completeness:` line as scoped, and believe it.** A relation answer's coverage banner is relative to the language of the symbol you asked about, because relations here do not cross language boundaries. `Completeness: no parse failures in Rust ...; 273 elsewhere (Python 273) cannot affect this answer` means the answer is complete — that is not a warning, and it is not a reason to fall back to grep. Failures that *could* have removed a fact your query needed are itemized instead, and every diagnostic is always in `--format json` in full.

Quick mental model:

```text
locate  →  entire graph search --query "..."          (ranked code + file:line)
surface →  entire graph def NAME                       (what a name IS: a type's fields + member signatures, a method's owning type, a trait's implementors)
impact  →  entire graph impact --symbol X              (one-shot blast radius: callers, types, data flow, co-change)
callers →  entire graph neighbors --symbol X ...       (targeted callers/callees of X)
change  →  entire graph diff --base A --head B          (entity-level, with dependents)
ingest  →  entire graph snapshot --format ndjson        (whole graph)
report  →  entire graph stats --repo .                  (human-facing: graph vs grep/read usage + estimated token savings)
verify  →  the VERIFY line search printed, run once      (else the project's own narrowest build/test cmd)
extras  →  entire graph search ... --reference-blocks all (container map, signature types, declaration card — off by default)
```

---

## Working on entire-graph itself

If your task is modifying this repository (not just using it), the build/test surface is in `mise.toml`:

```sh
mise run build   # go build -o entire-graph ./cmd/entire-graph  (needs CGO for tree-sitter)
mise run test    # go test ./...
mise run check   # fmt + vet + race tests + build
```

Contract rules that must not break: schema `1.x` is frozen and additive-only (`docs/adr/0001-ga-schema-contract.md`); the provider is **no-egress** (never add remote fetches, hosted API calls, telemetry, or runtime grammar downloads); `compound-v1` symbol IDs must stay stable across ordinary edits; unsupported/unparseable files must surface as machine-readable partial failures, never silent drops. All logic lives under `internal/` (`sem` = parsing/graph/search, `cli` = hand-rolled dispatch, `gitutil` = git subprocess); `cmd/entire-graph/main.go` is a thin entry point. The plugin manifest (`entire-plugin.yml`) registers the subcommand `graph`, so users type `entire graph ...`. This project was **previously named `entire-sem`** — do not reintroduce the old name. **Entire Brain** (`entire-brain`) is the separate downstream consumer of this provider's NDJSON — not an old name for this project.
