# EG Graphify Advertised Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add explicit entity/phrase-aware search, deterministic progressive cold preselection, a versioned public compact snapshot artifact, typed cold-build phase telemetry, and exactly one cold-build optimization selected only after the new profile proves where time is spent.

**Architecture:** Keep the existing search and provider contracts as the compatibility baseline. Search gains a bounded internal constraint model and a pure progressive selection planner; the ordinary unquoted query path and warm full-preindex behavior remain intact. Snapshot gains a separate dictionary-backed `compact-ndjson` streaming codec, canonical semantic hashing, a production load/query API, and a distinct cache namespace while current `ndjson` continues through the existing encoder. Build telemetry becomes typed and phase-local, and the benchmark harness runs a post-timing compact-artifact preflight through that production loader/query path before it gates a bounded parallel-parse optimization.

**Tech Stack:** Go 1.24, `encoding/json`, tree-sitter provider pipeline, existing `internal/sem` and `internal/cli` packages, Go `testing`, `cmd/graph-bench`.

## Global Constraints

- Work from base commit `90a3346a624d76f7fee21bd894721e5438dd9ac2` and preserve unrelated worktree changes.
- Keep all processing local-only and no-egress; do not add hosted services, embeddings, telemetry upload, or runtime downloads.
- Preserve `entire graph snapshot --format ndjson` as the default and preserve its existing object-per-line schema and cache key.
- Add compact output only as `entire graph snapshot --format compact-ndjson`; reject it for `symbols`, `edges`, and targeted `--to`/`--from`/`--relation` output.
- Compact format version 1 is a public native artifact. Unknown versions, wrong positional arity, a non-header first record, duplicate headers, and a missing trailing summary are hard decode errors.
- Compact encoding must be lossless for the current public NDJSON projection, byte-deterministic for the same ordered record stream, documented as a public artifact, and queryable through a production loader used outside the benchmark harness.
- Compact v1 uses deterministic first-seen string dictionaries. Every dictionary line, header, data record, and summary line is part of `compact_raw_bytes`; dictionaries are never excluded from size claims.
- Native and compact streams must produce the same canonical semantic SHA-256 and the same decoded public projection. The canonical input is normalized native NDJSON in record order, not compact bytes.
- Benchmark reports must expose native raw bytes, compact raw bytes, projected-fact counts, and bytes per projected fact. Per-repository ratios use exact raw bytes; aggregate ratios are recomputed from summed bytes and facts.
- Query agreement is a bounded scoring feature, never a hard filter. Entities
  include explicit identifier-shaped tokens plus general title-case proper-name
  tokens after sentence-initial interrogatives; phrases include explicit quoted
  spans plus ordered two- and three-token content-word windows. These rules are
  benchmark-agnostic and contain no LOCOMO names, questions, or answers.
- Bound a query to 8 distinct entities and 16 ordered phrase windows. Retain
  the exact raw query internally and report truncation in search stats; never
  silently pretend all features were retained.
- An entity/relationship agreement bonus is earned only when the same
  candidate contains the entity and the ordered relationship phrase. Cap the
  total adjustment so it cannot outrank an otherwise unsupported result. A
  candidate that misses agreement keeps its original score and is never
  removed solely by this feature.
- Keep exact-full-preindex committed search unbounded by `MaxIndexedFiles`, as required by the existing warm-preindex contract.
- Progressive widening is deterministic and capped at `4 * MaxIndexedFiles`; `IndexAllFiles` remains the explicit exhaustive opt-in.
- Do not implement the cold optimization until the typed phase profile and the decision gate in Task 6 have passed. If the gate fails, skip the optimization portion of Task 6 and proceed to final verification.
- Implement exactly one cold optimization in this plan: bounded parallel parsing with sequential hydration and ordered emission. Do not combine it with relation, inventory, cache, or encoding optimizations.

---

## File Structure

### Files to create

- `internal/sem/search_constraints.go` — bounded entity/phrase parsing and same-candidate agreement bonuses.
- `internal/sem/search_constraints_test.go` — pure parser, phrase-order, subject-agreement, bonus bounds, truncation, and end-to-end search tests.
- `internal/sem/search_preselection.go` — deterministic evidence fusion, progressive pass evaluation, and widening decision.
- `internal/sem/search_preselection_test.go` — confidence/coverage/diversity and deterministic widening tests.
- `internal/sem/compact_snapshot.go` — version-1 dictionary-backed positional compact NDJSON encoder/decoder and canonical semantic hasher.
- `internal/sem/compact_snapshot_test.go` — schema validation, dictionary accounting, semantic-hash/projection equivalence, determinism, and size tests.
- `internal/sem/compact_snapshot_query.go` — production compact snapshot loader, deterministic indexes, and exact symbol/relation query API.
- `internal/sem/compact_snapshot_query_test.go` — load, hash, projection, and deterministic query tests.
- `internal/cli/snapshot_query.go` — `snapshot-query` command that consumes compact artifacts through the production loader.
- `internal/cli/snapshot_query_test.go` — command validation and load/query integration tests.
- `internal/bench/compact_preflight.go` — post-timing native/compact artifact construction, load/query validation, and exact size metrics.
- `internal/bench/compact_preflight_test.go` — preflight consumer-path, dictionary-byte, hash, projection, and bytes-per-fact tests.
- `internal/sem/provider_parse_batch.go` — conditional bounded parallel parse batching, created only after the Task 6 gate passes.
- `internal/sem/provider_parse_batch_test.go` — ordering, concurrency cap, cancellation, semantic equivalence, and determinism tests for the selected optimization.

### Files to modify

- `internal/sem/search.go` — retain raw query, expose constraint truncation stats, apply constraints, and call the progressive selector.
- `internal/sem/search_test.go` — preserve full-preindex, top-K, and existing unquoted-query contracts.
- `internal/sem/search_cache_test.go` — prove cold/warm and selective/full results remain equivalent after progressive preselection.
- `internal/sem/provider.go` — typed phases, phase-local duration, inventory coverage, compact capability discovery, and gated parse-batch integration.
- `internal/sem/provider_test.go` — typed phase ordering and current NDJSON compatibility tests.
- `internal/sem/golden_test.go` — compare compact-decoded records with deterministic current provider records.
- `internal/sem/records_cache.go` — no algorithm change; accept the CLI-provided compact cache mode as a distinct hashed mode.
- `internal/sem/records_cache_test.go` — prove NDJSON and compact cache entries cannot collide.
- `internal/cli/root.go` — accept `compact-ndjson` for snapshot only, stream through the compact encoder, and register `snapshot-query`.
- `internal/cli/root_test.go` — CLI validation, default NDJSON compatibility, compact round trip, and cache reuse.
- `internal/bench/bench.go` — collect per-phase milliseconds, run compact preflight after cold timing, and aggregate exact artifact metrics.
- `internal/bench/bench_test.go` — deterministic phase reduction, preflight failure propagation, and report aggregation tests.
- `cmd/graph-bench/main.go` — print phase totals/shares already present in benchmark report data.
- `cmd/graph-bench/main_test.go` — profile JSON and summary-output coverage.
- `README.md` — advertise the additional snapshot format without changing the current NDJSON example.
- `docs/DETAILS.md` — document compact schema versioning, dictionaries, canonical hashing, load/query behavior, and decode equivalence.
- `docs/semantic_provider_requirements.md` — add the lossless compact artifact contract, deterministic query contract, and phase taxonomy.
- `docs/benchmarks.md` — document post-timing preflight, exact raw-byte accounting, bytes per projected fact, phase metrics, profiling commands, and the optimization decision gate.

---

### Task 1: Add bounded entity and ordered-phrase agreement scoring

**Files:**
- Create: `internal/sem/search_constraints.go`
- Create: `internal/sem/search_constraints_test.go`
- Modify: `internal/sem/search.go:364-393` (`searchQuery`)
- Modify: `internal/sem/search.go:3806-3904` (`buildSearchQuery`)
- Modify: `internal/sem/search.go:2353-2432` (`scoreSearchCandidates`)
- Modify: `internal/sem/search.go:2557-2606` (`searchStructuralAdjustment`)
- Modify: `internal/sem/search.go:568-590,1082-1100` (response stats)
- Test: `internal/sem/search_unmatched_terms_test.go`

**Interfaces:**
- Produces:

```go
const (
	maxSearchEntities = 8
	maxSearchPhrases  = 16
	maxSearchAgreementBonus = 4.0
)

type searchEntityConstraint struct {
	Raw        string
	Normalized string
}

type searchPhraseConstraint struct {
	Words    []string
	Explicit bool
}

type searchConstraints struct {
	Entities  []searchEntityConstraint
	Phrases   []searchPhraseConstraint
	Truncated bool
}

func parseSearchConstraints(raw string) searchConstraints
func orderedSearchPhraseMatch(text string, phrase searchPhraseConstraint) bool
func searchCandidateAgreement(candidate searchCandidate, constraints searchConstraints) (bonus float64, signals []string)
```

- Changes `searchQuery` to carry `raw string` and `constraints searchConstraints` in addition to current `rawLower`, terms, word sets, and identifier tokens.
- Adds `QueryConstraintsTruncated bool \`json:"query_constraints_truncated,omitempty"\`` to `SearchStats`.
- Consumes existing `identifierShapedToken`, `searchQueryWordSequence`,
  `searchTokenVariants`, `searchStopWords`, `SymbolName`, `QualifiedName`,
  `Signature`, `Snippet`, and symbol aliases already attached during indexing.

- [ ] **Step 1: Write failing parser and predicate tests**

Add table-driven tests with these exact cases:

```go
func TestParseSearchConstraintsFindsGeneralNamesAndOrderedWindows(t *testing.T) {
	q := `What movie did Avery recommend after the conference?`
	got := parseSearchConstraints(q)
	if !slices.Contains(got.Entities, searchEntityConstraint{Raw: "Avery", Normalized: "avery"}) ||
		!containsPhrase(got.Phrases, []string{"movie", "avery", "recommend"}) || got.Truncated {
		t.Fatalf("constraints = %#v", got)
	}
}

func TestOrderedSearchPhraseMatchRequiresAdjacentWrittenOrder(t *testing.T) {
	phrase := searchPhraseConstraint{Words: []string{"stale", "token", "order"}}
	for _, tc := range []struct{ text string; want bool }{
		{"the stale token order is rejected", true},
		{"token stale order", false},
		{"stale token insertion order", false},
	} {
		if got := orderedSearchPhraseMatch(tc.text, phrase); got != tc.want {
			t.Fatalf("match(%q) = %t, want %t", tc.text, got, tc.want)
		}
	}
}

func TestSearchCandidateAgreementRequiresEntityAndRelationshipOnOneCandidate(t *testing.T) {
	constraints := parseSearchConstraints(`What movie did Avery recommend?`)
	matching := searchCandidate{result: SearchResult{Snippet: "Avery recommended the movie Arrival."}}
	wrongSubject := searchCandidate{result: SearchResult{Snippet: "Blake recommended the movie Arrival."}}
	entityOnly := searchCandidate{result: SearchResult{Snippet: "Avery attended the festival."}}
	matchBonus, _ := searchCandidateAgreement(matching, constraints)
	wrongBonus, _ := searchCandidateAgreement(wrongSubject, constraints)
	entityBonus, _ := searchCandidateAgreement(entityOnly, constraints)
	if matchBonus <= entityBonus || wrongBonus != 0 || matchBonus > maxSearchAgreementBonus {
		t.Fatalf("bonuses = match %f wrong %f entity-only %f", matchBonus, wrongBonus, entityBonus)
	}
}
```

Also add a bounds case containing 9 distinct identifier/proper-name tokens and
enough content words to generate 17 windows. Assert exactly 8 entities, 16
phrases, and `Truncated == true`. Add negative cases proving sentence-initial
`What`, `When`, `Where`, `Who`, `Why`, and `How` never become entities and
single ordinary capitalized sentence starters do not create a subject.

- [ ] **Step 2: Run the focused tests to verify RED**

Run:

```bash
go test ./internal/sem -run 'Test(ParseSearchConstraints|OrderedSearchPhraseMatch|SearchCandidateAgreement)' -count=1
```

Expected: compilation fails because the parser, agreement types, and bonus function do not exist.

- [ ] **Step 3: Implement the bounded parser and pure predicates**

Implement these exact parsing rules in `search_constraints.go`:

1. Preserve the input byte-for-byte in `searchQuery.raw`.
2. Scan double-quoted spans without treating escaped `\"` as a closing quote;
   normalize them through `searchQueryWordSequence` and retain them as
   explicit ordered phrases.
3. Scan remaining tokens in written order. An entity is either an existing
   identifier-shaped token or a title-case/all-capital token that is not the
   first lexical token of a sentence and is not a stop word/interrogative.
   Normalize by Unicode case-folding, de-duplicate in written order, and cap at
   8. This rule is generic; no benchmark name list is allowed.
4. Build automatic ordered two- and three-token windows from non-stopword
   content tokens, preserving query order. Prefer three-token windows, then
   two-token windows, de-duplicate, retain explicit phrases first, and cap the
   combined list at 16. Set `Truncated` whenever a valid entity/window drops.
5. Agreement text is `SymbolName + "\n" + QualifiedName + "\n" + Signature +
   "\n" + Snippet + "\n" + aliases`. Match entities as whole normalized
   tokens; substring matches such as `Ann` in `Hannah` do not count.
6. Ordered phrase matching uses the same normalized token stream and requires
   written-order contiguous tokens for explicit phrases. Automatic windows may
   match in order with at most two intervening content tokens so natural
   question grammar does not demand verbatim prose.
7. Score entity coverage up to `1.5`, phrase coverage up to `1.5`, and add at
   most `1.0` relationship agreement only when the same candidate matches an
   entity plus an automatic or explicit phrase. Clamp the sum to
   `maxSearchAgreementBonus`.
8. Missing agreement returns zero bonus; it never filters or subtracts from the
   candidate's prior score. With no retained features, return zero and no
   signal so ordinary queries are rank-compatible.

In `buildSearchQuery`, set both `raw: query` and
`constraints: parseSearchConstraints(query)`. Feed retained entity and phrase
words through the existing weighted-term `add` closure at weight `1.0`, so
preselection can retrieve agreement-bearing files before scoring.

- [ ] **Step 4: Apply the bounded bonus before diversity/fusion**

In `scoreSearchCandidates`, add the agreement adjustment after the existing
lexical/structural score and append its signals. Apply the same pure adjustment
to sparse candidates after symbol attachment and before hybrid fusion. Never
drop a candidate for missing agreement. Copy `q.constraints.Truncated` into
both no-hit and normal `SearchStats` return paths.

Do not replace `matchesExactSymbolForm` or `wordSequence`; those continue to implement unquoted adjacent identifier spelling. Remove no existing exact-symbol or conceptual-query bonuses.

- [ ] **Step 5: Add end-to-end compatibility tests and verify GREEN**

Add:

```go
func TestSearchRepositoryBoostsRelationshipPhraseOnNamedSubject(t *testing.T) { /* generic Avery/Blake distractors; matching Avery relationship ranks first but every prior candidate remains */ }
func TestSearchRepositoryUnquotedConceptQueryKeepsIndependentRegions(t *testing.T) { /* repeat the MarshalCompact/MarshalIndented expectation from TestSearchRepositoryFindsConceptualBodyText */ }
func TestSearchResponseRetainsExactRawConstrainedQuery(t *testing.T) { /* include case, quotes, and surrounding spaces; assert response.Query is byte-identical */ }
func TestSearchAgreementHasNoBenchmarkSpecificVocabulary(t *testing.T) { /* reject benchmark markers such as locomo, longmemeval, session_, and qa: in production source; do not embed tuning names in the test */ }
```

Run:

```bash
go test ./internal/sem -run 'Test(SearchRepositoryBoostsRelationship|SearchRepositoryUnquotedConceptQuery|SearchResponseRetainsExactRaw|SearchAgreementHasNoBenchmark|MatchesExactSymbolForm|SpellsCompactIdentifier|SearchRepositoryFindsConceptualBodyText)' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit the query-constraint slice**

```bash
git add internal/sem/search.go internal/sem/search_constraints.go internal/sem/search_constraints_test.go internal/sem/search_unmatched_terms_test.go
git commit -m "feat(search): add bounded entity and phrase agreement"
```

---

### Task 2: Add deterministic progressive preselection and evidence fusion

**Files:**
- Create: `internal/sem/search_preselection.go`
- Create: `internal/sem/search_preselection_test.go`
- Modify: `internal/sem/search.go:205-240` (`SearchStats`)
- Modify: `internal/sem/search.go:1261-1640` (`searchFileCandidate`, `applySearchFileCoverage`, `searchFileSelection`, `preselectSearchFiles`)
- Modify: `internal/sem/search.go:1715-1810` (`committedSearchFiles`, `scoreSearchPaths`)
- Modify: `internal/sem/search.go:3421-3719` (hybrid selection, diversity, deterministic sort)
- Modify: `internal/sem/search_test.go`
- Modify: `internal/sem/search_cache_test.go`

**Interfaces:**
- Produces:

```go
const (
	searchPreselectionRRFConstant       = 60
	searchPreselectionMaxWideningFactor = 4
	searchPreselectionMinConfidence     = 0.15
	searchPreselectionDirectoryTarget   = 3
)

type searchPreselectionEvidence struct {
	Path            string
	OriginalScore   float64
	PathScore       float64
	ContentScore    float64
	ConstraintScore float64
	MatchedTerms    []bool
}

type searchPreselectionAssessment struct {
	Files      []string
	Passes     int
	Examined   int
	Confidence float64
	Coverage   float64
	Diversity  float64
	Widened    bool
	Bounded    bool
}

func fuseSearchPreselectionEvidence(evidence []searchPreselectionEvidence) []searchPreselectionEvidence
func selectProgressiveSearchFiles(evidence []searchPreselectionEvidence, matchedAnywhere []bool, q searchQuery, baseLimit int) searchPreselectionAssessment
```

- Extends `searchFileCandidate` with `pathScore`, `contentScore`, `constraintScore`, and an owned copy of `matchedTerms`.
- Adds `PreselectionConfidence`, `PreselectionCoverage`, `PreselectionDiversity`, `PreselectionWidened`, and `PreselectionBounded` JSON fields to `SearchStats` and matching fields to `searchFileSelection`.
- Consumes Task 1 agreement features only as an evidence channel; final bounded
  same-candidate scoring remains Task 1's responsibility and never filters.

- [ ] **Step 1: Write failing deterministic fusion and widening tests**

Cover these exact contracts:

```go
func TestFuseSearchPreselectionEvidenceUsesStableRRFAndPathTieBreak(t *testing.T) { /* three channels with crossed ranks; shuffle input 50 times; assert identical path order */ }
func TestProgressivePreselectionStopsAtConfidentCoveredDiverseBase(t *testing.T) { /* base=4, boundary gap >= .15, all terms, 3 dirs; assert Passes=1 and Widened=false */ }
func TestProgressivePreselectionWidensForLowConfidence(t *testing.T) { /* tied boundary; assert widths 4 then 8 and Widened=true */ }
func TestProgressivePreselectionWidensForCoverage(t *testing.T) { /* rare query term appears at rank 6; assert second pass includes it and Coverage=1 */ }
func TestProgressivePreselectionWidensForDirectoryDiversity(t *testing.T) { /* first four are one dir, next adds available dirs; assert widening */ }
func TestProgressivePreselectionStopsAtFourXBound(t *testing.T) { /* impossible coverage with base=2 and 20 rows; assert len=8 and Bounded=true */ }
```

The RRF channel order is fixed as path, content, constraint. Rank each channel by descending channel score, then `OriginalScore`, then `canonicalSearchPathLess`. A zero channel score does not participate. Sum `1 / (60 + rank)` for participating channels.

- [ ] **Step 2: Run the pure planner tests to verify RED**

```bash
go test ./internal/sem -run 'Test(FuseSearchPreselection|ProgressivePreselection)' -count=1
```

Expected: compilation fails because the evidence and planner APIs do not exist.

- [ ] **Step 3: Implement fusion and the progressive decision rule**

Use pass widths `base`, `2*base`, and `4*base`, each capped by the fused evidence length. Evaluate after every width:

- Coverage: weighted union of selected `MatchedTerms` divided by the weighted `matchedAnywhere` terms; when the denominator is zero, coverage is 1.
- Confidence: 1 when there is no excluded row; otherwise `max(0, 1-next.rrf/current.rrf)` at the selected boundary.
- Diversity: distinct canonical first directory segments divided by `min(3, distinct directories available in evidence)`; root-level files use `"."`.
- Stop when confidence is at least 0.15, coverage is 1, and diversity is 1.
- At the `4*base` ceiling, return the capped selection even when a metric misses; set `Bounded=true`.
- Set `Examined` to the number of evidence rows assessed across passes, not repository inventory size. Existing `PreselectionFilesExamined` keeps its current physical scan meaning.

- [ ] **Step 4: Integrate planner evidence into cold preselection**

In `scoreFile`, retain the exact per-term `matched` slice and calculate:

```go
pathScore := 2 * pathSearchScore(q, filePath)
contentScore := matchedWeight + searchFileCoverageBonus(q, matched, matchedAnywhere)
constraintScore := searchFileConstraintEvidence(content, q.constraints)
originalScore := pathScore + contentScore + constraintScore + searchPathPrior(q, filePath)
```

`searchFileConstraintEvidence` awards 1 per entity token present in the
path/content and 2 per complete ordered phrase present anywhere in the file;
it only widens the candidate pool and never replaces Task 1's same-candidate
bounded bonus.

Call `selectProgressiveSearchFiles` only on cold `go-content` and `git-index-grep+go-content` paths. Preserve these paths unchanged:

- `IndexAllFiles`.
- repositories already at or below `MaxIndexedFiles`.
- exact full-preindex `git-tree-grep`, which continues returning every matched/path-evidenced file.
- sparse deep corpus enumeration; only the semantic selective graph file list is widened.

Copy assessment metrics through `searchFileSelection` into both SearchResponse stats paths.

- [ ] **Step 5: Add regression tests for warm, deep, and Top-K contracts**

Extend or add tests asserting:

- `TestCommittedPreselectionRequiresExactFullPreindexForUnboundedCandidates` still returns the unbounded warm match set.
- `TestFullPreindexSearchMatchesColdSelectiveGraphExpansion` still has identical results.
- `TestWarmCommittedSearchMatchesExhaustiveResultsWithoutFullContentRescan` still reports zero preselection content reads.
- `TestSearchIsContinuousAcrossTheTopKBoundary` and `TestSearchDeepModeKeepsRealScoresAndDedupes` still pass.
- A cold low-confidence fixture reports two or three preselection passes, returns deterministic files/results across 20 runs, and never indexes more than `4*MaxIndexedFiles`.

Run:

```bash
go test ./internal/sem -run 'Test(ProgressivePreselection|CommittedPreselection|FullPreindexSearch|WarmCommittedSearch|SearchIsContinuous|SearchDeepMode|SearchRepositoryProgressive)' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit progressive preselection**

```bash
git add internal/sem/search.go internal/sem/search_preselection.go internal/sem/search_preselection_test.go internal/sem/search_test.go internal/sem/search_cache_test.go
git commit -m "feat(search): widen cold preselection deterministically"
```

---

### Task 3: Add the lossless, deterministic, queryable compact snapshot artifact

**Files:**
- Create: `internal/sem/compact_snapshot.go`
- Create: `internal/sem/compact_snapshot_test.go`
- Create: `internal/sem/compact_snapshot_query.go`
- Create: `internal/sem/compact_snapshot_query_test.go`
- Modify: `internal/sem/golden_test.go`
- Test: `internal/sem/provider_test.go:10796` (`TestWriteSnapshotNDJSON`)

**Interfaces:**
- Produces the streaming codec and canonical hasher:

```go
const CompactSnapshotFormatVersion = 1

type CompactSnapshotEncoder struct {
	encoder        *json.Encoder
	strings        map[string]int
	dictionary     []string
	dictionaryBytes int64
	wroteHeader    bool
	wroteSummary   bool
}

func NewCompactSnapshotEncoder(out io.Writer) *CompactSnapshotEncoder
func (encoder *CompactSnapshotEncoder) Encode(record any) error
func (encoder *CompactSnapshotEncoder) DictionaryBytes() int64
func DecodeCompactSnapshot(in io.Reader, emit func(any) error) error

type SnapshotSemanticHasher struct {
	digest hash.Hash
	buffer bytes.Buffer
}

func NewSnapshotSemanticHasher() *SnapshotSemanticHasher
func (hasher *SnapshotSemanticHasher) Add(record any) error
func (hasher *SnapshotSemanticHasher) SumHex() string
```

- Produces the production loader/query API:

```go
type CompactSnapshotIndex struct {
	Snapshot              ProviderSnapshot
	Summary               SnapshotSummary
	CanonicalSemanticHash string
	// Private exact-match maps are built during load.
}

type CompactSnapshotQuery struct {
	Symbol   string
	FromID   string
	Relation string
}

type CompactSnapshotQueryResult struct {
	Symbols   []SymbolRecord
	Relations []RelationRecord
}

func LoadCompactSnapshot(in io.Reader) (*CompactSnapshotIndex, error)
func (index *CompactSnapshotIndex) Query(query CompactSnapshotQuery) CompactSnapshotQueryResult
```

- Compact v1 is line-delimited, positional, dictionary-backed, and fixed:

```text
["h",1,<SnapshotHeader>]
["d",base,[strings...]]
["f",id_idx,path_idx,blob_idx,language_idx,bytes]
["x",id_idx,kind_idx,value_idx,file_idx,start_line,end_line,signature_idx,language_idx,external,source_symbol_idx,source_details_idx]
["s",id_idx,stable_id_version_idx,kind_idx,name_idx,qualified_name_idx,file_idx,start_line,end_line,signature_idx,body_hash_idx,language_idx,container_idx,alias_indices]
["r",from_idx,to_idx,type_idx,confidence,reason_idx,scope_idx,resolution_idx,target_kind_idx,evidence,warning_indices]
["m",<SnapshotSummary>]
```

Dictionary index 0 is the empty string. A `d` line's `base` must equal the current dictionary length, and its nonempty strings are appended in first-seen record/field order. The encoder emits a dictionary update immediately before the first data record that needs those strings, so output remains streaming and byte-deterministic without a full pre-scan. Evidence entries are positional `[kind_idx,file_idx,start_line,end_line,detail_idx]`. Header and summary remain their full public JSON objects. Every dictionary line is part of the artifact and its bytes are included in total compact bytes.

`FileRecord.Lines`, `SymbolRecord.Local`, byte ranges, and parameter metadata remain absent because current public NDJSON omits them. Decode restores the fixed public record types and must reproduce the normal NDJSON public projection exactly, including empty slices and maps.

Canonical hashing is SHA-256 over normalized native NDJSON public records in original record order. `SnapshotSemanticHasher.Add` projects only public fields, uses the same fixed record-type normalization as native NDJSON, disables HTML escaping, and appends one newline per record. Both the original native stream and the compact-decoded stream must produce the same hash; hashing compact bytes directly is forbidden.

`LoadCompactSnapshot` decodes the artifact into `ProviderSnapshot` plus `SnapshotSummary`, builds exact indexes, and stores the decoded canonical hash. `Query` matches `Symbol` against exact stable ID, `Name`, or `QualifiedName`; matches `FromID` against exact stable ID; treats `Relation` as an optional uppercase relation-type filter; sorts symbols by ID and relations by `(Type, FromID, ToID)`; and never depends on map iteration order.

- [ ] **Step 1: Write failing codec, accounting, hash, and query tests**

Add:

```go
func TestCompactSnapshotRoundTripPreservesPublicProjection(t *testing.T)
func TestCompactSnapshotEncodingIsDeterministic(t *testing.T)
func TestCompactSnapshotDictionariesAreMeasuredInRawBytes(t *testing.T)
func TestCompactSnapshotCanonicalHashMatchesNativeNDJSON(t *testing.T)
func TestCompactSnapshotIsSmallerThanNDJSONIncludingDictionaries(t *testing.T)
func TestCompactSnapshotDecoderRejectsUnknownVersion(t *testing.T)
func TestCompactSnapshotDecoderRejectsWrongArity(t *testing.T)
func TestCompactSnapshotDecoderRequiresHeaderDictionaryThenSummary(t *testing.T)
func TestLoadCompactSnapshotQueriesSymbolsAndRelations(t *testing.T)
func TestCompactSnapshotQueryResultsAreDeterministic(t *testing.T)
```

Build one fixture containing every optional public field: symbol aliases/container, localized external source, relation evidence/scope/resolution/target kind/warning codes, header maps/warnings/failures/completeness, and summary maps. Encode the native public records once, compact encode/decode them, and assert both `reflect.DeepEqual` over unmarshaled public JSON and canonical-hash equality. The test must fail if only the hash matches or only the projection matches.

For dictionary accounting, capture the exact bytes written for every `d` line and assert `encoder.DictionaryBytes()` equals that sum. Then assert total compact bytes equal header bytes + dictionary bytes + data-record bytes + summary bytes. For size, repeat 20 symbols and 20 relations and require compact bytes, including all dictionaries, `< 0.80 * native NDJSON bytes`.

The query fixture must include duplicate symbol names in different containers and at least two relation types from one stable ID. Assert exact-ID/name/qualified-name lookups, uppercase relation filtering, stable sort order, canonical hash retention, and no results for partial names.

- [ ] **Step 2: Run focused tests to verify RED**

```bash
go test ./internal/sem -run 'Test(CompactSnapshot|LoadCompactSnapshot)' -count=1
```

Expected: compilation fails because the codec, dictionary accounting, semantic hasher, loader, and query index do not exist.

- [ ] **Step 3: Implement the stateful streaming codec and canonical projection**

Encoder rules:

- Require `SnapshotHeader` first and `SnapshotSummary` last; reject later records and unsupported Go types with an error naming `%T`.
- Intern every record string in fixed field order, emit a `d` line before the referring record, and update `dictionaryBytes` with the exact encoded line length.
- Use `json.Encoder` with `SetEscapeHTML(false)` for every line; rely on `encoding/json` deterministic string-key map ordering for header/summary maps.
- Encode every positional field, including empty optional positions, so v1 arity is invariant.

Decoder rules:

- Scan one line at a time with a bounded 16 MiB scanner buffer, decode first into `[]json.RawMessage`, validate tag/arity, and then decode fields.
- Require exactly one `h` first, version exactly 1, contiguous dictionary bases, valid dictionary indices, and one `m` last.
- Reject blank lines, empty strings after index 0, duplicate dictionary strings, duplicate headers, records after summary, missing summary, and unknown tags.
- Emit decoded records incrementally and retain only the dictionary. Keep snapshot materialization exclusively in `LoadCompactSnapshot`.

Factor the native public projection into one helper shared by `SnapshotSemanticHasher` and tests so normal and decoded streams cannot silently hash different field sets.

- [ ] **Step 4: Implement the loader/query consumer and prove provider-stream equivalence**

Build the full `CompactSnapshotIndex` only in `LoadCompactSnapshot`, compute its canonical hash while decoding, and populate deterministic exact-match maps. Reject a decoded stream without a header/summary even if the low-level decoder callback emitted earlier records.

Extend the provider golden fixture to compact encode twice, assert byte equality, decode/load once, assert public projection equality and canonical-hash equality against the original `StreamSnapshot` records, and execute one exact symbol query plus one relation query through `CompactSnapshotIndex.Query`. Keep `TestStreamSnapshotOrderIsDeterministic` unchanged for normal NDJSON.

Run:

```bash
go test ./internal/sem -run 'Test(CompactSnapshot|LoadCompactSnapshot|StreamSnapshotOrderIsDeterministic)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the queryable artifact core**

```bash
git add internal/sem/compact_snapshot.go internal/sem/compact_snapshot_test.go internal/sem/compact_snapshot_query.go internal/sem/compact_snapshot_query_test.go internal/sem/golden_test.go
git commit -m "feat(snapshot): add queryable compact ndjson v1"
```

---

### Task 4: Expose compact snapshot production and query commands without changing NDJSON

**Files:**
- Modify: `internal/cli/root.go:256-396` (`runProviderRecords`)
- Modify: `internal/cli/root.go:458-480,528-603` (`providerFlags`, `parseProviderFlags`)
- Modify: `internal/cli/root_test.go`
- Create: `internal/cli/snapshot_query.go`
- Create: `internal/cli/snapshot_query_test.go`
- Modify: `internal/sem/records_cache.go:23-37,107-161`
- Modify: `internal/sem/records_cache_test.go`
- Modify: `internal/sem/provider.go:293-308` (`CapabilityReport` construction)
- Modify: `README.md`
- Modify: `docs/DETAILS.md`
- Modify: `docs/semantic_provider_requirements.md`

**Interfaces:**
- Consumes `NewCompactSnapshotEncoder` from Task 3.
- Consumes `LoadCompactSnapshot` and `CompactSnapshotIndex.Query` from Task 3; `snapshot-query` must not contain a second decoder or query implementation.
- Keeps `LoadProviderRecords` and `StoreProviderRecords` signatures unchanged; passes mode `snapshot:compact-ndjson-v1` only for compact output and legacy `snapshot` for NDJSON.
- Adds capability discovery key `compact_snapshot_ndjson_v1: true` to `CapabilityReport.OptionalLocalOnlyFeatures`.
- Adds `entire graph snapshot-query --input <file> --symbol <id-or-name> [--from <stable-id> --relation <TYPE>] --format ndjson`. Require at least `--symbol` or `--from`; allow `--relation` only with `--from`; write normal NDJSON symbol/relation records in deterministic query order.

- [ ] **Step 1: Write failing CLI format and compatibility tests**

Add:

```go
func TestSnapshotCompactNDJSONRoundTripsToNativeRecords(t *testing.T)
func TestSnapshotNDJSONRemainsDefaultObjectFormat(t *testing.T)
func TestCompactNDJSONIsSnapshotOnly(t *testing.T)
func TestCompactNDJSONRejectsTargetedRelationFilters(t *testing.T)
func TestCompactAndNativeRecordCachesDoNotCollide(t *testing.T)
func TestSnapshotQueryCommandLoadsCompactArtifact(t *testing.T)
func TestSnapshotQueryRejectsInvalidFilterCombinations(t *testing.T)
```

The default-format test runs `snapshot --worktree` without `--format`, asserts the first byte is `{`, and decodes every line as the current object schema. The compact test runs with `--format compact-ndjson`, asserts the first line starts `["h",1,`, loads through `sem.LoadCompactSnapshot`, and compares public projections and canonical semantic hashes with a second normal NDJSON run. The query-command test writes that compact output to a temporary file, queries one exact symbol and one relation type, and compares its NDJSON output with `CompactSnapshotIndex.Query`.

- [ ] **Step 2: Run CLI/cache tests to verify RED**

```bash
go test ./internal/cli ./internal/sem -run 'Test(SnapshotCompact|SnapshotNDJSONRemains|CompactNDJSON|CompactAndNativeRecordCaches)' -count=1
```

Expected: CLI rejects `compact-ndjson`, so compact tests fail while the compatibility test passes.

- [ ] **Step 3: Add format validation and encoder selection**

In `runProviderRecords`:

```go
compact := flags.Format == "compact-ndjson"
if flags.Format != "ndjson" && !compact {
	return fmt.Errorf("%s requires --format ndjson%s", mode, map[bool]string{true: " or compact-ndjson"}[mode == "snapshot"])
}
if compact && mode != "snapshot" {
	return fmt.Errorf("--format compact-ndjson is only valid for snapshot")
}
if compact && filterActive {
	return errors.New("--format compact-ndjson requires a complete snapshot; remove --to/--from/--relation")
}
```

Refactor the existing output closure to an `encodeRecord func(any) error`. For NDJSON, it calls the unchanged `json.Encoder.Encode`; for compact output, it calls `CompactSnapshotEncoder.Encode`. When caching, build the chosen encoder on the existing `io.MultiWriter` so cached bytes exactly equal stdout.

Set `cacheMode := mode` for NDJSON and `cacheMode := "snapshot:compact-ndjson-v1"` for compact. Pass `cacheMode` to load/store; do not change `providerRecordsCacheVersion` or the legacy mode.

- [ ] **Step 4: Update discovery and documentation**

Document:

- Normal NDJSON remains the interoperable default.
- Compact v1 positional schema exactly as listed in Task 3.
- Version is carried only by the first `h` line and every decoder must reject an unknown version.
- Compact is full snapshot only and has semantic projection equivalence, deterministic bytes, and a separate cache namespace.
- Example command: `entire graph snapshot --repo . --format compact-ndjson > graph.compact.ndjson`.
- Example consumer: `entire graph snapshot-query --input graph.compact.ndjson --symbol Cache.Refresh --from <stable-id> --relation CALLS --format ndjson`.
- Every `d` line is part of the versioned artifact and counted in raw compact bytes; compact size claims never subtract dictionary overhead.
- Canonical SHA-256 and decoded public projection must both equal native NDJSON; hash equality alone is not a losslessness proof.

Do not add compact format text to `leanHeader.Capabilities`; doing so would change every normal NDJSON header. Advertise through `capabilities --json`, CLI help, README, and detailed docs instead.

- [ ] **Step 5: Verify both formats and cache separation**

```bash
go test ./internal/cli ./internal/sem -run 'Test(Snapshot|ProviderRecordsCache|Compact)' -count=1
```

Expected: PASS, including existing `TestWriteSnapshotNDJSON` and provider-record cache tests.

- [ ] **Step 6: Commit the public compact artifact**

```bash
git add internal/cli/root.go internal/cli/root_test.go internal/cli/snapshot_query.go internal/cli/snapshot_query_test.go internal/sem/records_cache.go internal/sem/records_cache_test.go internal/sem/provider.go README.md docs/DETAILS.md docs/semantic_provider_requirements.md
git commit -m "feat(cli): expose compact snapshot ndjson"
```

---

### Task 5: Add typed cold-build phases and benchmark phase metrics

**Files:**
- Modify: `internal/sem/provider.go:318-355` (`ProviderSnapshotOptions`, `ProgressEvent`)
- Modify: `internal/sem/provider.go:867-1237` (`StreamSnapshot`)
- Modify: `internal/sem/provider_test.go`
- Modify: `internal/bench/bench.go:19-56,67-155,264-349`
- Modify: `internal/bench/bench_test.go`
- Create: `internal/bench/compact_preflight.go`
- Create: `internal/bench/compact_preflight_test.go`
- Modify: `cmd/graph-bench/main.go:153-190,363-380`
- Modify: `cmd/graph-bench/main_test.go`
- Modify: `docs/benchmarks.md`
- Modify: `docs/semantic_provider_requirements.md`

**Interfaces:**
- Produces:

```go
type BuildPhase string

const (
	BuildPhaseInventory BuildPhase = "inventory"
	BuildPhaseParse     BuildPhase = "parse"
	BuildPhaseRelations BuildPhase = "relations"
	BuildPhaseFinalize  BuildPhase = "finalize"
)

type ProgressEvent struct {
	Phase        BuildPhase
	FilesTotal   int
	FilesDone    int
	Symbols      int
	Relations    int
	HeapAlloc    uint64
	MaxRSSBytes  uint64
	PhaseElapsed time.Duration
	Elapsed      time.Duration
}
```

- Adds `PhaseMS map[string]float64 \`json:"phase_ms"\`` to `bench.RepoMetrics` and `bench.Aggregate`.
- Adds exact per-repository fields `NDJSONRawBytes`, `CompactRawBytes`, `CompactDictionaryBytes`, `ProjectedFacts`, `NDJSONBytesPerProjectedFact`, `CompactBytesPerProjectedFact`, and `CanonicalSemanticHash` with matching snake-case JSON names. Keep `OutputBytes` as a compatibility metric.
- Adds `runCompactPreflight`, which builds native and compact artifacts from a second snapshot stream, loads the compact artifact through `sem.LoadCompactSnapshot`, performs exact symbol/relation queries through `CompactSnapshotIndex.Query`, and returns exact byte/hash/projection metrics.
- Adds pure reducer:

```go
func reducePhaseEvents(events []sem.ProgressEvent) map[string]float64
```

The reducer keeps the maximum `PhaseElapsed` observed for each phase and rounds milliseconds with existing `round2`.

- [ ] **Step 1: Write failing typed-phase and reducer tests**

Add tests that assert:

1. A one-file snapshot emits phases in nondecreasing order `inventory`, `parse`, `relations`, `finalize`.
2. Repeated progress within parse/relations retains the same typed phase.
3. Every phase's last event has nonnegative `PhaseElapsed`; total `Elapsed` is nondecreasing.
4. `reducePhaseEvents` maps synthetic durations exactly: inventory 5 ms, parse events 10/25 ms -> 25, relations 7 ms, finalize 3 ms.
5. `aggregate` sums phase milliseconds from successful repos only, matching its current error-row exclusion.
6. `TestCompactPreflightUsesLoadAndQueryPath` fails when the production loader/query rejects the artifact.
7. `TestCompactPreflightReportsRawBytesAndBytesPerProjectedFact` includes all dictionary lines in `CompactRawBytes` and uses `ProjectedFacts = files + externals + symbols + relations`.
8. `TestCompactPreflightCanonicalHashAndProjectionMatchNative` requires both semantic-hash and public-projection equality.

- [ ] **Step 2: Run focused tests to verify RED**

```bash
go test ./internal/sem ./internal/bench -run 'Test(StreamSnapshotReportsTypedBuildPhases|ReducePhaseEvents|BuildReportAggregatesPhase)' -count=1
```

Expected: compilation fails because `BuildPhase`, `PhaseElapsed`, `PhaseMS`, and reducer do not exist.

- [ ] **Step 3: Instrument inventory, parse, relations, and finalize**

Move the overall start timestamp before `prepareSource`. Emit inventory only after `prepareSource` returns, using its actual duration and discovered file total. Start a fresh phase timestamp immediately before the parse loop, relations phase, and final summary/external sorting work.

Replace current string values `start` and `summary` with typed `inventory` and `finalize`. Periodic parse and relation callbacks keep their current 1024-unit cadence. `PhaseElapsed` is duration since the current phase start; `Elapsed` remains duration since function entry.

No callback may run concurrently, and callback overhead stays outside the next phase by resetting the next phase start after the previous terminal callback.

- [ ] **Step 4: Persist phase data and run compact preflight after timing**

In `MeasureRepoWithOptions`, wrap `opts.Progress` rather than replacing it:

```go
var phaseEvents []sem.ProgressEvent
progress := func(event sem.ProgressEvent) {
	phaseEvents = append(phaseEvents, event)
	if opts.Progress != nil {
		opts.Progress(event)
	}
}
```

Pass `progress` to the timed `StreamSnapshot` and capture `wall := time.Since(start)` before preflight. Then run a second `StreamSnapshot` outside the cold wall interval, writing every record to exact native NDJSON and compact buffers while computing the native semantic hash and retaining the native public projection. Load compact bytes through `sem.LoadCompactSnapshot`; require canonical-hash and decoded-projection equality; query the first sorted symbol and first relation through the loaded index and require exact matches. Any load, hash, projection, or query mismatch makes the repository row an error.

Set raw-byte fields from the exact buffer lengths. `CompactRawBytes` includes header, every dictionary update, every data record, and summary; `CompactDictionaryBytes` is only an included breakdown, and component bytes must sum to total compact bytes. Compute per-repo bytes per fact with `max(1, ProjectedFacts)`. Aggregate successful rows by summing raw bytes and facts and recomputing ratios, never by averaging ratios. Keep canonical hash per repo only. Print phase shares, raw bytes, and bytes per projected fact.

- [ ] **Step 5: Update telemetry documentation and verify GREEN**

Document the phase boundaries exactly and state that phase values are process-local performance diagnostics, not provider semantic schema fields. State that preflight runs after measured cold `wall_ms`, adds harness runtime but does not contaminate the cold measurement, exercises the same production load/query path as `snapshot-query`, and includes every dictionary byte. Update CLI progress formatting to include `phase_elapsed=` while retaining `elapsed=`.

Run:

```bash
go test ./internal/sem ./internal/bench ./cmd/graph-bench -run 'Test(StreamSnapshotReportsTypedBuildPhases|MeasureRepo|BuildReport|SkippedRepo|Guardrail)' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit typed instrumentation**

```bash
git add internal/sem/provider.go internal/sem/provider_test.go internal/bench/bench.go internal/bench/bench_test.go internal/bench/compact_preflight.go internal/bench/compact_preflight_test.go cmd/graph-bench/main.go cmd/graph-bench/main_test.go docs/benchmarks.md docs/semantic_provider_requirements.md
git commit -m "feat(bench): report typed cold build phases"
```

---

### Task 6: Profile first and enforce the optimization decision gate

**Files:**
- No product-code changes before the gate.
- Read generated reports outside Git from `bench/profile-results/` or another ignored directory.
- Modify `docs/benchmarks.md` only if the gate passes, to record the chosen phase and reproducible command without committing machine-specific result files.

**Interfaces:**
- Consumes `RepoMetrics.PhaseMS` and `Aggregate.PhaseMS` from Task 5.
- Produces a binary execution decision for the conditional optimization portion of this task; no ambiguous “probably” outcome is allowed.

- [ ] **Step 1: Verify the profiling build and unit tests**

```bash
go test ./internal/sem ./internal/bench ./cmd/graph-bench -count=1
go build ./cmd/graph-bench ./cmd/entire-graph
```

Expected: PASS with no parse-batch files present.

- [ ] **Step 2: Run three cold profiles on the fixed fast manifest**

Use already cloned pinned repositories; cloning is outside the measured path:

```bash
mkdir -p bench/profile-results
go run ./cmd/graph-bench -manifest bench/repos.fast.json -cache bench/.cache -lock bench/repos.lock.json -out bench/profile-results/full -skip-clone -profile full -progress -exact-output-bytes
go run ./cmd/graph-bench -manifest bench/repos.fast.json -cache bench/.cache -lock bench/repos.lock.json -out bench/profile-results/fast -skip-clone -profile fast -progress -exact-output-bytes
go run ./cmd/graph-bench -manifest bench/repos.fast.json -cache bench/.cache -lock bench/repos.lock.json -out bench/profile-results/syntax -skip-clone -profile syntax-only -progress -exact-output-bytes
```

Expected: every successful repo row has all four `phase_ms` keys. Missing clones remain explicit error rows and are excluded from gate arithmetic.

- [ ] **Step 3: Apply the parse-optimization gate exactly**

Proceed to the conditional optimization steps below only when all conditions are true:

1. At least 9 repositories succeed in each of the three profile reports.
2. Parse is the largest `Aggregate.PhaseMS` phase in at least two profiles.
3. Across all successful repo rows from those two or three parse-dominant profiles, parse is the largest phase for at least 60% of rows.
4. In every parse-dominant profile, aggregate parse milliseconds are at least 45% of aggregate wall milliseconds.
5. The same source revision and `bench/repos.lock.json` are used for all three runs.

Tie-breaking: a phase is “largest” only when it exceeds the next phase by at least 5% of wall time. A closer result fails the gate because the evidence does not isolate a target.

If any condition fails, skip the conditional optimization, do not create `provider_parse_batch.go`, preserve the reports locally, and proceed to Task 7 verification.

- [ ] **Step 4: Record only the reproducible decision when the gate passes**

Add a short “Cold optimization gate” section to `docs/benchmarks.md` containing:

- the three commands above,
- the source commit and lockfile hash,
- the statement that bounded parallel parsing was selected because all five gate conditions passed,
- the before/after comparison command that the conditional optimization must use.

Do not copy workstation-specific absolute paths or the complete JSON reports into Git.

- [ ] **Step 5: Commit the gate record**

Only when the gate passes:

```bash
git add docs/benchmarks.md
git commit -m "docs(bench): record cold parse optimization gate"
```

---

#### Conditional Task 6 continuation: implement the one selected cold optimization — bounded parallel parsing

**Precondition:** Execute these steps only when every gate condition above passed. These steps are invalid without that evidence.

**Files:**
- Create: `internal/sem/provider_parse_batch.go`
- Create: `internal/sem/provider_parse_batch_test.go`
- Modify: `internal/sem/provider.go:901-1134` (parse phase only)
- Modify: `internal/sem/provider_test.go`
- Modify: `internal/sem/golden_test.go`
- Modify: `docs/benchmarks.md`

**Interfaces:**
- Produces:

```go
const (
	coldParseWorkersMax = 4
	coldParseBatchFiles = 16
	coldParseBatchBytes = 16 << 20
)

type snapshotParseInput struct {
	Index   int
	Path    string
	Content string
}

type snapshotParseOutput struct {
	Index              int
	Path               string
	File               *FileRecord
	Symbols            []SymbolRecord
	Structural         []structuralSymbol
	Retained           []SymbolRecord
	PrecomputedImports []string
	Language           string
	Failures           []PartialFailure
	Skipped            bool
}

func parseSnapshotBatch(
	ctx context.Context,
	inputs []snapshotParseInput,
	workers int,
	parse func(snapshotParseInput) snapshotParseOutput,
) ([]snapshotParseOutput, error)
```

- Adds private `parseWorkers int` to `ProviderSnapshotOptions` for same-package tests. Zero selects `min(4, max(1, runtime.GOMAXPROCS(0)))`; one forces the prior sequential semantics.
- Hydration remains sequential through `sc.read`; only CPU parsing/extraction runs concurrently. Emission and shared-index mutation remain in original path order on the caller goroutine.

- [ ] **Step 1: Write failing batch primitive tests**

Add:

```go
func TestParseSnapshotBatchReturnsInputOrder(t *testing.T) { /* block workers and release in reverse order; assert output indices ascending */ }
func TestParseSnapshotBatchCapsConcurrency(t *testing.T) { /* atomically track in-flight work; workers=3; assert peak==3 */ }
func TestParseSnapshotBatchStopsOnCancellation(t *testing.T) { /* cancel after first start; assert context error and bounded starts */ }
func TestStreamSnapshotParallelParseMatchesSequentialProjection(t *testing.T) { /* same mixed-language fixture, parseWorkers 1 vs 4, compare public JSON records */ }
func TestStreamSnapshotParallelParseIsByteDeterministic(t *testing.T) { /* run workers=4 twenty times; compare NDJSON bytes */ }
```

- [ ] **Step 2: Run optimization tests to verify RED**

```bash
go test ./internal/sem -run 'Test(ParseSnapshotBatch|StreamSnapshotParallelParse)' -count=1
```

Expected: compilation fails because the batch types/function and private option do not exist.

- [ ] **Step 3: Implement the bounded ordered batch primitive**

`parseSnapshotBatch` starts at most `workers` goroutines, sends indexed inputs, collects indexed outputs, returns `ctx.Err()` on cancellation, and sorts results by `Index` before returning. It never mutates provider maps or emits records.

Build batches sequentially in `StreamSnapshot`:

- Read through `sc.read` on the caller goroutine.
- Flush when adding a file would exceed 16 files or 16 MiB of held content.
- A single allowed file larger than 16 MiB forms a one-file batch; existing `MaxParseBytes` remains authoritative.
- Handle unsupported, unreadable, oversized, build-excluded, and minified files through the same ordered output structure so file/failure ordering cannot change.
- Parse batch items concurrently using independent `TreeSitterParser{}` values.
- After each batch completes, emit files/symbols and mutate `files`, `recordsByFile`, `structuralByFile`, `precomputedImports`, completeness, counts, and failures strictly in ascending original path index.
- Preserve the existing progress cadence based on completed original file indices.

- [ ] **Step 4: Prove semantic, byte, memory-bound, and race safety**

Run:

```bash
go test -race ./internal/sem -run 'Test(ParseSnapshotBatch|StreamSnapshotParallelParse|StreamSnapshotOrderIsDeterministic)' -count=1
go test ./internal/sem -run 'TestBuildProviderSnapshot|TestStreamSnapshot' -count=1
```

Expected: PASS. The race detector must report no races.

- [ ] **Step 5: Re-run the identical profiles and enforce improvement guardrails**

Run the same three Task 6 commands into `bench/profile-results/*-parallel`. Accept the optimization only when:

- each compared report uses the same source commit, lockfile, profile, and successful repo set,
- aggregate parse milliseconds improve by at least 15% in at least two parse-dominant profiles,
- aggregate wall milliseconds do not regress in any profile by more than 3%,
- symbols, relations, relation-type counts, resolution counts, confidence bands, failures, and completeness are identical per repository,
- exact output bytes are identical per repository,
- peak RSS does not increase by more than 10%.

If any guardrail fails, revert only the optimization commit; retain Tasks 1–5 plus the Task 6 gate record and note the rejected experiment in review notes, not as a product claim.

- [ ] **Step 6: Update benchmark findings and commit the accepted optimization**

Update `docs/benchmarks.md` with the fixed commands and aggregate before/after percentages. Do not generalize beyond the measured manifest/profile/hardware.

```bash
git add internal/sem/provider.go internal/sem/provider_parse_batch.go internal/sem/provider_parse_batch_test.go internal/sem/provider_test.go internal/sem/golden_test.go docs/benchmarks.md
git commit -m "perf(provider): parallelize bounded cold parsing"
```

---

### Task 7: Final compatibility and advertised-contract verification

**Files:**
- Verify all files changed by Tasks 1–6.
- Modify documentation only if verification exposes a factual mismatch.

**Interfaces:**
- Verifies current NDJSON, compact v1, search response validation, provider cache, benchmark schema, and no-egress contracts together.

- [ ] **Step 1: Run formatting and static checks**

```bash
gofmt -w internal/sem/search.go internal/sem/search_constraints.go internal/sem/search_constraints_test.go internal/sem/search_preselection.go internal/sem/search_preselection_test.go internal/sem/compact_snapshot.go internal/sem/compact_snapshot_test.go internal/sem/compact_snapshot_query.go internal/sem/compact_snapshot_query_test.go internal/sem/provider.go internal/sem/provider_test.go internal/sem/golden_test.go internal/sem/records_cache.go internal/sem/records_cache_test.go internal/cli/root.go internal/cli/root_test.go internal/cli/snapshot_query.go internal/cli/snapshot_query_test.go internal/bench/bench.go internal/bench/bench_test.go internal/bench/compact_preflight.go internal/bench/compact_preflight_test.go cmd/graph-bench/main.go cmd/graph-bench/main_test.go
go vet ./...
```

If the Task 6 optimization was gated off, omit its two absent parse-batch files from the formatting command.

- [ ] **Step 2: Run the full test suite**

```bash
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run the nearest race-sensitive suites**

```bash
go test -race ./internal/sem ./internal/bench -count=1
```

Expected: PASS.

- [ ] **Step 4: Smoke-test both snapshot formats against this repository**

```bash
go run ./cmd/entire-graph snapshot --repo . --format ndjson --no-cache > /tmp/entire-graph-native.ndjson
go run ./cmd/entire-graph snapshot --repo . --format compact-ndjson --no-cache > /tmp/entire-graph-compact.ndjson
go run ./cmd/entire-graph snapshot-query --input /tmp/entire-graph-compact.ndjson --symbol CompactSnapshotEncoder --format ndjson > /tmp/entire-graph-compact-query.ndjson
go test ./internal/sem ./internal/bench -run 'Test(CompactSnapshotRoundTripPreservesPublicProjection|CompactSnapshotCanonicalHashMatchesNativeNDJSON|CompactPreflight)' -count=1
```

Expected: production and query commands succeed; native starts with `{`, compact starts with `["h",1,`; the query produces a normal symbol NDJSON record; hash, projection, load/query preflight, dictionary accounting, and bytes-per-fact tests pass.

- [ ] **Step 5: Check the diff for scope and compatibility**

```bash
git diff --check
git status --short
git diff --stat
```

Confirm no generated benchmark reports, cache contents, binaries, or `/tmp` outputs are staged. Confirm current NDJSON tests and docs still describe it as the default.

- [ ] **Step 6: Commit final verification-only corrections if needed**

Only when formatting or factual documentation corrections changed tracked files:

```bash
git add README.md docs/DETAILS.md docs/semantic_provider_requirements.md docs/benchmarks.md
git commit -m "docs: align graphify advertised contracts"
```

---

## Execution Handoff

Plan complete. Execute Tasks 1–5 in order, run the Task 6 profile gate, and execute its conditional optimization only when every gate condition passes. Task 7 verification is required in either branch. Use subagent-driven development for isolated review gates or execute inline with checkpoints before the Task 6 decision.
