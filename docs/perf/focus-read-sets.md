# Focused lookups: what each verb reads from the snapshot

This is the read set of every lookup verb that `sem.LoadFocusedProviderSnapshot`
may serve. For each one it lists every snapshot field the verb reads and every
whole-slice scan it makes, traced from its `LoadOrBuildProviderSnapshot` call
to its output. Line numbers are at commit 48995067. The focused loader's
closure (`internal/sem/search_store_focus.go`) is checked against this table: a
focused snapshot is correct only when everything listed here that can reach the
answer is in it.

The verbs read the snapshot only through `sem.ProviderSnapshot`, so the
inventory is complete when it covers `Header`, `Files`, `Symbols`, `Relations`
and `Externals`.

## Shared by every verb

| Read | Where | Fields / scan | Served by |
| --- | --- | --- | --- |
| Header provenance | `neighbors.go:389-392`, `impact.go:413-416`, `def.go:702-705`, `explain.go:295-296` | `Header.RepoRoot`, `Commit`, `Tree`, `Profile` | meta `header` + post-load chain (root rebind, commit restamp) |
| Header diagnostics | `neighbors.go:381-384,403-406`, `impact.go:407-410,430-433`, `def.go:696-699,708-710` | `Header.Warnings`, `PartialFailures`, `Stats` (neighbors `:405`, impact `:432`), `Completeness` | meta `header` + post-load chain (`Stats.Relations`, `Completeness.Relations["FILE_CHANGES_WITH"]`, warning retirement) |
| Completeness scope | `completeness.go:62-107` via `neighbors.go:407`, `impact.go:434` | `Header.Stats.CompletenessLevel`, `Header.Completeness.Languages`, `Header.PartialFailures`, `Header.Warnings`; **scan of all `Files`** (`Path` → `Language`) | header + all file rows |
| Source reader | `callsite.go:92-164` via `neighbors.go:170`, `impact.go:196`, `def.go:195` | `Header.Commit`, `Header.RepoRoot` | header |
| `--symbol path:line` | `symbolref.go:51-79`, `matchSnapshotFilePath` `:150-171`, `snapshotFilePaths` `:174-180` | **scan of all `Files`** (`Path`), case-insensitive exact or unique suffix | the loader returns the full snapshot for any name ending `:<positive int>` (a superset of what `parseSymbolRef` treats as positional) |
| Exact resolution | `resolveFocusSymbols` `symbolref.go:197-222`, `resolveFocusSymbolsByID` `:228-236`, `filterFocusSymbolsByLine` `:238-272` | **scan of all `Symbols`**: exact `ID` wins outright; else `strings.EqualFold` on `Name` OR `QualifiedName`; then `--file` (`EqualFold(FilePath)`), `--kind` (`EqualFold(Kind)`), `--line` (`StartLine`, `EndLine`) narrow | starting set: `id IN names` OR `name_fold IN fold(names)` OR `qname_fold IN fold(names)`. Filters only narrow, so this is a superset of every exact answer |
| Fuzzy ladder | `resolveFocusSymbolsOrFuzzy` `symbolref.go:606-676`, `symbolNameMatchTier` `:680-713` | **ranks over every symbol** (`Name`, `QualifiedName`, `FilePath`, `Kind`, `StartLine`, `ID`) | not served focused: when exact resolution on a focused snapshot is empty, the verb reloads with `LoadOrBuildProviderSnapshot` before the ladder (Task 5) |
| Match bodies | `symbolMatchBodiesFromReader` `symbolref.go:754-799` | matched symbols' `FilePath`, `StartLine`, `EndLine`, `Name`, `QualifiedName`, `Kind` | starting set |

## neighbors (`neighbors.go`)

Load at `:155-161`; answer built by `buildNeighborResponseFromReader` `:335-499`.

| Read | Where | Fields / scan |
| --- | --- | --- |
| Endpoint index | `:340-349` | **scan of all `Files`, `Symbols`, `Externals`** into an ID map; only relation endpoints are looked up in it |
| Name resolution | `:350-355` | `parseSymbolRef` + `resolveFocusSymbolsOrFuzzy` (see shared rows) over `Symbols`, `Files` |
| Disambiguation | `:409-419` | more than one focus: matched symbols only, no relations |
| Hop 1 | `:432-452` | **scan of all `Relations`**: `Type` matches `--relation` (`CALLS` also admits `CONSTRUCTS`, `neighborRelationMatches` `:621-626`); `ToID` in focus for `in`, `FromID` in focus for `out` |
| Relation fields | `edgeForRelation` `:639-646` | `Type`, `Confidence`, `Resolution`, `Reason`, `Evidence`, `WarningCodes`, `EvidenceDropped` |
| Endpoint fields | `endpointForSymbol` `:501-507`, `endpointForFile` `:509-514`, `endpointForExternal` `:628-637` | other end's `ID`, `Name`, `QualifiedName`, `Kind`, `FilePath`, `StartLine`, `EndLine`, `Language`; file `Path`, `Language`; external `Value`, `Kind`, `FilePath`, `StartLine` |
| `--depth 2` | `:475-490` | **no second hop**: paths are the product of the focus's hop-1 incoming and outgoing edges |
| Call sites | `annotateNeighborCallSites` `:532-563`, `callEvidenceSpan` `callsite.go:276` | incoming call-family edges' `Evidence` (span and written callee), source through the reader |

Hop walk: hop 1 only, the requested relation type (plus `CONSTRUCTS` for
`CALLS`), `In` unless `--direction out`, `Out` unless `--direction in`. No
containment reads.

## impact (`impact.go`)

Load at `:183`; answer built by `buildImpactResponseFromReader` `:367-633`.

| Read | Where | Fields / scan |
| --- | --- | --- |
| Endpoint and language index | `:372-386` | **scan of all `Files`, `Symbols`, `Externals`**; `endpointLanguages` from file and symbol `Language` |
| Name resolution | `:388-405` | as neighbors |
| Disambiguation | `:439-449` | more than one focus: matched symbols only |
| Container | `:457-461` | `endpoints[focus.ContainerID]`: the focus's container, by `ContainerID` (a symbol, or a file, which is always loaded) |
| Focus file | `:462-468` | **scan of all `Files`** for `Path == focus.FilePath` → file `ID` |
| Relation pass | `:505-542` | **scan of all `Relations`**: call family `CALLS`/`CONSTRUCTS`/`ASYNC_CALLS` (`:336-338`) indexed by `ToID` for every relation, callees where `FromID == focus`; `USES_TYPE`/`PARAM_TYPE`/`RETURNS_TYPE` (`:340-342`) and `DATA_FLOWS` touching the focus in either direction; `FILE_CHANGES_WITH` touching the focus FILE id, with `Reason` as the co-change detail (`:538`) |
| Caller walk | `:555-607` | breadth-first over `callsIn`: depth 1 is incoming call family of the focus, depth 2 (`--depth 2`) incoming call family of each depth-1 caller; reads `Evidence` (`:584-593`), `Resolution` through `impactNonCodeMention` (`:357-365`, doc-mention label `name_only`), the caller's `FilePath` and language, and the callee endpoint's `Name` |
| Siblings / members | `:609-620` | **scan of all `Symbols`**: `ContainerID == focus.ContainerID` (siblings) and `ContainerID == focus.ID` (members) |
| Call sites | `annotateImpactCallSites` `:640-658` | caller entries' evidence span, source through the reader |

Hop walk: hop 1 is call family + type family + `DATA_FLOWS`, both directions.
Hop 2 (depth 2 only) is incoming call family only, from the hop-1 callers.
`FILE_CHANGES_WITH` is never a hop: the focus file's co-change edges come from
the post-load history splice, which covers every file.

## def (`def.go`)

Load at `:172-178`; one snapshot answers every name of `def A B C` in the loop at
`:211-214`, each through `buildDefResponse` `:695-729`.

| Read | Where | Fields / scan |
| --- | --- | --- |
| Name forms | `defQualifiedNameForms` `:679-693` | as typed, plus `::`/`->`/`#` rewritten to `.`; tried in order, first non-empty wins (`resolve` `:847-856`) |
| Several names | `runDef` `:155-158,211-214` | every name, every form |
| Symbol index | `newDefIndex` `:772-774` | **scan of all `Symbols`** into `symbolsByID` |
| CONTAINS members | `:792-797` | **scan of all `Relations`**, `CONTAINS`: `FromID` is the owner, `ToID` must be a loaded symbol; `RelationScope == "extension"` marks an extension member |
| Supertypes / subtypes | `:798-814` | `EXTENDS`/`INHERITS`/`IMPLEMENTS`: `FromID` must be a loaded symbol; `ToID` symbol name, or the external type name from the ID; recorded both ways |
| `impl:` labels | `:815-822` | `OVERRIDES`: target `ToID` symbol, then the target's `ContainerID` symbol's `Name`; keyed by `FromID`, last write wins in relation order |
| container_id members | `:827-831` | **scan of all `Symbols`** by `ContainerID` |
| Resolution | `resolve` `:847-888` | as the shared rows, per form |
| Partial grouping | `groupPartials` `:902-936`, `sharesMemberOwner` `:951-971` | the matched symbols' members (`membersByOwner`) and their `FilePath`; `Signature` for `partial` |
| Anchor | `declaration` `:1013-1017` | member count of each part |
| Owner | `:1037-1044` | non-type anchor: `symbolsByID[anchor.ContainerID]` (`Name`, `Kind`, `FilePath`, `StartLine`) |
| Declared members | `:1053-1072` | every part's members: `Name`, `QualifiedName`, `Kind`, `Signature`, `FilePath`, `StartLine`; extension flag (`:1061-1062`); `impl:` from the member's `OVERRIDES` (`:1063-1067`) |
| Inherited members | `:1078-1087` | **second hop**: each supertype's members, by `CONTAINS` and by `ContainerID` |
| Implementors | `:1088-1090`, `relatedFor` `:1106-1114` | subtypes' `Kind`, `FilePath`, `StartLine` |

Hop walk: hop 1 from the matched symbols is `CONTAINS` out (members) and
`EXTENDS`/`INHERITS`/`IMPLEMENTS` both ways (supertypes, subtypes). Hop 2 is
`OVERRIDES` out from the members, then the override target's container.
Supertypes' members (by `CONTAINS` and `ContainerID`) are the second
containment hop. `ContainerID` gives members (`:827-831`), the owner
(`:1038`) and the override target's owner (`:820`).

## explain (`explain.go`)

Guarded by Task 2 (stdin synthesized). Load at `:281-287`; answer built by
`buildExplainResponse` `:492-534`. Only a `--head` run can reach the cache; the
working-tree default is never served focused.

| Read | Where | Fields / scan |
| --- | --- | --- |
| Header | `:295-296` | `Header.Commit`, `Header.Tree` |
| Symbol index | `:495-503` | **scan of all `Symbols`**: `byID`, and `byName` on the exact (case-sensitive) `Name` |
| Ranking | `explainRank` `:568-583` | each same-named symbol's `Signature`, `StartLine`, `EndLine`, `FilePath`, and its container `byID[ContainerID].Name` (`:575`) |
| Owner | `:528` | anchor's container `byID[anchor.ContainerID].Name` |

No relations, no files. Names are the candidate names scanned from stdin, after
the last-dot cut (`:414-417`). The starting set's fold match is a superset of
the exact `Name` match. Explain needs the starting set plus the containers of
its symbols.

## Closure the loader implements

| Need | Closure element |
| --- | --- |
| Every exact resolution | starting set over `id`, `name_fold`, `qname_fold` |
| Positional `path:line` | full snapshot (`focused=false`) |
| Header, `Stats`, `Completeness` | meta header through the shared post-load chain, which takes the stored relation counts rather than `len(Relations)` |
| `Files` scans | all file rows |
| Relations a verb walks | `Hops`: hop i takes every relation of the hop's types with the frontier on the requested side, uncapped, full rows. The symbols among their endpoints are the next frontier, unless the hop is `Terminal` |
| Relation endpoints | every symbol and external a retained relation names; files are always loaded and never expanded |
| Containers (`impact.go:457`, `def.go:820,1038`, `explain.go:528,575`) | `Kinship`: the container of every retained symbol |
| Members and siblings (`impact.go:609-620`, `def.go:827-831`) | `Kinship`: `container_id` children of the starting set and of its containers. When hop 1 walks `CONTAINS` outward (def), the starting set's children join the hop-2 frontier, as if reached by `CONTAINS` at hop 1, so their `OVERRIDES` are walked (`def.go:1063-1067`) |
| Supertype members (`def.go:1078-1087`) | `Kinship`: for every symbol reached through a retained `EXTENDS`/`INHERITS`/`IMPLEMENTS`/`OVERRIDES` relation, its `CONTAINS` out-relations and its `container_id` children |
| Co-change (`impact.go:525-541`) | the post-load history splice, which covers every file |
| Order | every slice is read by `rowid`, the snapshot's own order |

Not loaded: the sidecar's `unmerged_data_flow_edges`. Only the selective
derivation for `query` reads them (`search_cache.go`), and it never consumes a
focused snapshot.

## The focus each verb builds (Task 5)

A `FocusHop` applies one direction set to all of its types, so a hop that
needs `Out` for one type and both directions for another asks for both. That
loads extra rows but never drops a needed one. A `Terminal` hop retains its
relations and their endpoints but does not move the frontier: the next hop
expands the same frontier again.

| Verb | `Names` | `Hops` | `Kinship` |
| --- | --- | --- | --- |
| neighbors | `--symbol` | hop 1: `--relation` (+`CONSTRUCTS` for `CALLS`); `In` unless `--direction out`, `Out` unless `--direction in` | no |
| impact | `--symbol` | terminal: `USES_TYPE`, `PARAM_TYPE`, `RETURNS_TYPE`, `DATA_FLOWS`, `In`+`Out`; terminal: `CALLS`, `CONSTRUCTS`, `ASYNC_CALLS`, `Out` (callees); then `CALLS`, `CONSTRUCTS`, `ASYNC_CALLS`, `In` (callers), once per level of `--depth` | yes |
| def | every `defQualifiedNameForms` form of every name | hop 1: `CONTAINS`, `EXTENDS`, `INHERITS`, `IMPLEMENTS`, `In`+`Out`; hop 2: `OVERRIDES`, `Out` | yes |
| explain | the candidate names | none | yes |
