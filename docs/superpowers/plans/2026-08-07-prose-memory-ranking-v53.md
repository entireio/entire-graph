# Prose Memory Ranking v53 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Improve deterministic prose-session retrieval for safe lexical families and distributed list evidence while preserving ordinary code-search output.

**Architecture:** Extend only the existing Markdown prose-parent selection seam. Exact ranking remains primary; conservative word-family matching supplies missing per-term coverage, and a query-shape helper reserves more tail slots for independent parents when a prose question explicitly asks for multiple answers.

**Tech Stack:** Go 1.24, Entire Graph semantic search, tree-sitter Markdown fixtures, standard `testing` package.

## Global Constraints

- Base all work on commit `bb8bd49fb4039f489cc5977fe008af7dcead5050` in branch `codex/eg-prose-memory-v53-ranking`.
- Do not modify or merge PRs #64 or #84.
- Do not inspect a fresh holdout or make paid API/model calls.
- Do not add dataset IDs, people, answers, activity lists, or benchmark-specific synonyms.
- Keep all relaxed matching inside the existing prose-parent lane; code-search ordering must remain byte-identical.
- Add no dependencies, network access, telemetry, embeddings, or model-backed retrieval.
- Preserve deterministic ordering, bounded IO, result limits, schema 1.x, and zero graph-build LLM credits.

---

### Task 1: Safe prose lexical families

**Files:**
- Modify: `internal/sem/search_prose_parent.go:246-293`
- Test: `internal/sem/search_prose_session_test.go`

**Interfaces:**
- Consumes: `safeASCIIPlural(word string) (string, bool)` and `proseASCIIWord(word string) bool`.
- Produces: `safeProseInflectionMatch(left, right string) bool`, retaining the existing signature for callers in `proseParentTermLists` and `proseCandidateTermCoverage`.
- Produces: private helpers `safeASCIIProseVerbForms(word string) []string` and `safeASCIIProsePrefixFamily(left, right string) bool`.

- [ ] **Step 1: Add direct failing lexical-family tests**

Append this table-driven test to `internal/sem/search_prose_session_test.go`:

```go
func TestSafeProseInflectionMatchSupportsBoundedWordFamilies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		left  string
		right string
		want  bool
	}{
		{left: "archive", right: "archived", want: true},
		{left: "archive", right: "archiving", want: true},
		{left: "navigate", right: "navigation", want: true},
		{left: "orchard", right: "orchards", want: true},
		{left: "gas", right: "gases", want: false},
		{left: "archive", right: "architect", want: false},
		{left: "plan", right: "planet", want: false},
	}
	for _, testCase := range tests {
		if got := safeProseInflectionMatch(testCase.left, testCase.right); got != testCase.want {
			t.Errorf("safeProseInflectionMatch(%q, %q) = %t, want %t",
				testCase.left, testCase.right, got, testCase.want)
		}
	}
}
```

- [ ] **Step 2: Run the direct test and verify the new positive cases fail**

Run:

```bash
go test ./internal/sem -run '^TestSafeProseInflectionMatchSupportsBoundedWordFamilies$' -count=1
```

Expected: FAIL for `archive/archived`, `archive/archiving`, and `navigate/navigation`; existing plural and unsafe cases retain their current behavior.

- [ ] **Step 3: Add an end-to-end failing Markdown-session regression**

Append this test to `internal/sem/search_prose_session_test.go`:

```go
func TestSearchRepositoryRanksDerivedWordFamilyMarkdownSession(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "sessions/focus.md", `# Navigation archive

The expedition was carefully navigated and archived after sunset.
`)
	for index := 0; index < 11; index++ {
		write(t, repo, fmt.Sprintf("sessions/distractor-%02d.md", index), fmt.Sprintf(
			"# Navigation marker%d ridge%d vessel%d\n\nUnrelated archive index.\n",
			index, index, index,
		))
	}

	response, err := SearchRepository(
		t.Context(), repo, "test", "navigate archive sunset", SearchOptions{
			Worktree: true, Profile: ProfileSyntaxOnly, TopK: 10, MaxIndexedFiles: 32,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range response.Results {
		if result.FilePath == "sessions/focus.md" &&
			containsString(result.Signals, proseParentRetrievalSignal) {
			return
		}
	}
	t.Fatalf("derived word-family session missing from top 10: %#v", response.Results)
}
```

- [ ] **Step 4: Run the end-to-end test and verify it fails**

Run:

```bash
go test ./internal/sem -run '^TestSearchRepositoryRanksDerivedWordFamilyMarkdownSession$' -count=1
```

Expected: FAIL because exact/plural matching does not treat `navigate` and `navigated` as one prose family.

- [ ] **Step 5: Implement bounded prose verb and prefix-family matching**

In `internal/sem/search_prose_parent.go`, keep exact and plural logic first. Add conservative verb forms for words of at least five ASCII letters:

```go
func safeASCIIProseVerbForms(word string) []string {
	if len(word) < 5 || !proseASCIIWord(word) {
		return nil
	}
	if strings.HasSuffix(word, "e") {
		return []string{word + "d", strings.TrimSuffix(word, "e") + "ing"}
	}
	return []string{word + "ed", word + "ing"}
}
```

Add a prose-only derivational-family fallback. Require both words to be at least six letters, length difference no more than five, a common prefix of at least five letters, and the prefix to cover at least two thirds of the shorter word:

```go
func safeASCIIProsePrefixFamily(left, right string) bool {
	if len(left) < 6 || len(right) < 6 || !proseASCIIWord(left) || !proseASCIIWord(right) {
		return false
	}
	difference := len(left) - len(right)
	if difference < 0 {
		difference = -difference
	}
	if difference > 5 {
		return false
	}
	common := 0
	for common < len(left) && common < len(right) && left[common] == right[common] {
		common++
	}
	shorter := minInt(len(left), len(right))
	return common >= 5 && common*3 >= shorter*2
}
```

Update `safeProseInflectionMatch` after exact/plural checks:

```go
	for _, form := range safeASCIIProseVerbForms(left) {
		if form == right {
			return true
		}
	}
	for _, form := range safeASCIIProseVerbForms(right) {
		if form == left {
			return true
		}
	}
	return safeASCIIProsePrefixFamily(left, right)
```

- [ ] **Step 6: Run focused prose tests**

Run:

```bash
go test ./internal/sem -run '^(TestSafeProseInflectionMatchSupportsBoundedWordFamilies|TestSearchRepositoryRanksDerivedWordFamilyMarkdownSession|TestSearchRepositoryRanksSafePluralMarkdownSession|TestSearchRepositoryRanksSafeSingularEvidenceForPluralQuery|TestSearchRepositoryDoesNotConflateUnsafeProseSuffixes|TestSearchRepositoryCodeResultOrderRemainsByteIdentical)$' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit Task 1**

```bash
git add internal/sem/search_prose_parent.go internal/sem/search_prose_session_test.go
git commit -m "feat(search): match bounded prose word families"
```

---

### Task 2: Distributed-session reservation for list questions

**Files:**
- Modify: `internal/sem/search_prose_parent.go:57-130`
- Test: `internal/sem/search_prose_session_test.go`

**Interfaces:**
- Consumes: `searchQuery.rawLower`, `searchQuery.wordSequence`, `searchQuery.words`, and `safeASCIIPlural`.
- Produces: `proseQueryRequestsMultipleParents(q searchQuery) bool`.
- Produces: `proseParentHeadCount(q searchQuery, topK int) int`.

- [ ] **Step 1: Add failing query-shape and allocation tests**

Append:

```go
func TestProseParentHeadCountReservesMoreTailForListQuestions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		query string
		want  int
	}{
		{query: "find the archive implementation", want: 5},
		{query: "which archives contain lanterns", want: 3},
		{query: "what records exist other than the ledger", want: 3},
		{query: "where are the travel notes", want: 3},
	}
	for _, testCase := range tests {
		if got := proseParentHeadCount(buildSearchQuery(testCase.query), 10); got != testCase.want {
			t.Errorf("proseParentHeadCount(%q, 10) = %d, want %d",
				testCase.query, got, testCase.want)
		}
	}
}
```

- [ ] **Step 2: Run the test and verify it fails to compile**

Run:

```bash
go test ./internal/sem -run '^TestProseParentHeadCountReservesMoreTailForListQuestions$' -count=1
```

Expected: FAIL with `undefined: proseParentHeadCount`.

- [ ] **Step 3: Implement generic list-query detection and head reservation**

Add helpers in `internal/sem/search_prose_parent.go`:

```go
func proseQueryRequestsMultipleParents(q searchQuery) bool {
	words := q.words
	if words == nil {
		words = searchQueryWords(q.rawLower)
	}
	interrogative := words["what"] || words["which"] || words["where"]
	if !interrogative {
		return false
	}
	if words["else"] || (words["other"] && words["than"]) {
		return true
	}
	written := q.wordSequence
	if len(written) == 0 {
		written = searchQueryWordSequence(q.rawLower)
	}
	for _, word := range written {
		if len(word) <= 4 || !strings.HasSuffix(word, "s") || strings.HasSuffix(word, "ss") {
			continue
		}
		singular := strings.TrimSuffix(word, "s")
		if plural, ok := safeASCIIPlural(singular); ok && plural == word {
			return true
		}
	}
	return false
}

func proseParentHeadCount(q searchQuery, topK int) int {
	if topK <= 0 {
		return 0
	}
	if proseQueryRequestsMultipleParents(q) {
		return maxInt(1, topK/3)
	}
	return (topK + 1) / 2
}
```

Replace:

```go
	headParents := (topK + 1) / 2
```

with:

```go
	headParents := proseParentHeadCount(q, topK)
```

- [ ] **Step 4: Run query-shape, diversity, and code-identity regressions**

Run:

```bash
go test ./internal/sem -run '^(TestProseParentHeadCountReservesMoreTailForListQuestions|TestSearchRepositoryRanksDistributedMarkdownSession|TestDiverseSelectionDoesNotSpendBudgetOnClones|TestDiverseSelectionCoversFilesBeforeAddingRegions|TestSearchRepositoryCodeResultOrderRemainsByteIdentical)$' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit Task 2**

```bash
git add internal/sem/search_prose_parent.go internal/sem/search_prose_session_test.go
git commit -m "feat(search): reserve prose tail for list evidence"
```

---

### Task 3: Verification and no-API retired-tune audit

**Files:**
- Modify only if verification uncovers a defect: `internal/sem/search_prose_parent.go`, `internal/sem/search_prose_session_test.go`
- Verify: entire repository

**Interfaces:**
- Consumes: the Task 1 and Task 2 helpers.
- Produces: a clean branch with test evidence and a retrieval-only comparison; no scored benchmark artifact.

- [ ] **Step 1: Format and inspect the diff**

Run:

```bash
gofmt -w internal/sem/search_prose_parent.go internal/sem/search_prose_session_test.go
git diff --check
git diff --stat bb8bd49fb4039f489cc5977fe008af7dcead5050...HEAD
```

Expected: no formatting or whitespace errors; only the design, plan, prose-parent code, and prose tests differ.

- [ ] **Step 2: Run the full semantic-search package**

Run:

```bash
go test ./internal/sem -count=1
```

Expected: PASS.

- [ ] **Step 3: Run repository-wide static and test verification**

Run:

```bash
go vet ./...
go test -count=1 ./...
```

Expected: both commands exit 0.

- [ ] **Step 4: Build the isolated candidate binary**

Run:

```bash
go build -o /tmp/entire-graph-v53-ranking ./cmd/entire-graph
shasum -a 256 /tmp/entire-graph-v53-ranking
```

Expected: build exits 0 and prints one SHA-256 hash. The binary remains outside tracked files.

- [ ] **Step 5: Run the GraphMark no-API retrieval diagnostic on retired tune IDs only**

From `/Users/suhaan/devenv-worktrees/graphify-parity-memory-v52-release`, use the existing v52 `tune_locomo_case_ids` and `tune_longmemeval_case_ids`, materialized source corpora, and the new local binary. Retrieve 100 native candidates, normalize to the first 10 distinct sessions, and calculate recall@1/5/10 and MRR. Do not instantiate a reader or grader, load credentials, inspect holdout IDs, or write a scored artifact.

Expected promotion rule:

```text
LOCOMO recall@10 >= 0.9388888889
LongMemEval-S evidence recall@10 == 1.0
No previously correct retired-tune case becomes zero-recall
```

If the rule fails, preserve the complete per-case delta table, revert only the losing task commit, and rerun Steps 1-5. Do not add case-specific rules.

- [ ] **Step 6: Commit any verification-only correction, otherwise record clean state**

If source changed during verification:

```bash
git add internal/sem/search_prose_parent.go internal/sem/search_prose_session_test.go
git commit -m "fix(search): preserve prose ranking regressions"
```

Then run:

```bash
git status --short --branch
```

Expected: clean `codex/eg-prose-memory-v53-ranking` worktree.

---

### Task 4: Review and separate PR preparation

**Files:**
- Review: `docs/superpowers/specs/2026-08-07-prose-memory-ranking-v53-design.md`
- Review: `docs/superpowers/plans/2026-08-07-prose-memory-ranking-v53.md`
- Review: `internal/sem/search_prose_parent.go`
- Review: `internal/sem/search_prose_session_test.go`

**Interfaces:**
- Consumes: clean verified commits from Tasks 1-3.
- Produces: one unmerged Entire Graph PR based on PR #84's head.

- [ ] **Step 1: Audit prohibited leakage and branch isolation**

Run:

```bash
git diff bb8bd49fb4039f489cc5977fe008af7dcead5050...HEAD -- . ':!docs/superpowers/specs/2026-08-07-prose-memory-ranking-v53-design.md' ':!docs/superpowers/plans/2026-08-07-prose-memory-ranking-v53.md' | rg -n -i 'locomo|longmemeval|graphify|cmm|conv-[0-9]|session_[0-9]|answer_' && exit 1 || true
git status --short --branch
```

Expected: no benchmark identifiers or comparator names in production/test code and a clean worktree.

- [ ] **Step 2: Request independent code and spec review**

Reviewers must verify:

```text
1. The code implements the design without benchmark vocabulary.
2. Relaxed matching cannot affect non-prose corpora.
3. Determinism, limits, and code-result identity remain covered.
4. The retired-tune diagnostic used no reader, grader, API, or fresh selector.
5. Test commands were executed rather than inferred.
```

Expected: no unresolved correctness or fairness finding.

- [ ] **Step 3: Push and open a separate unmerged PR**

```bash
git push -u origin codex/eg-prose-memory-v53-ranking
gh pr create --base codex/eg-prose-memory-v52-release --head codex/eg-prose-memory-v53-ranking --title "feat: improve deterministic prose memory ranking" --body-file /tmp/eg-v53-pr-body.md
```

The PR body must state that retired-tune retrieval diagnostics are developmental, no paid QA benchmark was run, and a fresh publishable evaluation remains separately gated.
