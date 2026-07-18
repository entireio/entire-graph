package sem

import "testing"

func TestSearchRepositoryDedupesTrackedSemanticMirror(t *testing.T) {
	repo := t.TempDir()
	implementation := `export function isAsync(value: unknown): boolean {
  return value != null && typeof value === "object";
}
`
	write(t, repo, "src/helpers/parseUtil.ts", implementation)
	write(t, repo, "runtime-copy/lib/helpers/parseUtil.ts", implementation)

	response, err := SearchRepository(t.Context(), repo, "test", "isAsync", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     5,
	})
	if err != nil {
		t.Fatal(err)
	}
	var definitions []SearchResult
	for _, result := range response.Results {
		if result.QualifiedName == "isAsync" {
			definitions = append(definitions, result)
		}
	}
	if len(definitions) != 1 || definitions[0].FilePath != "src/helpers/parseUtil.ts" {
		t.Fatalf("search mirror definitions = %#v, want one authored source", definitions)
	}
}

func TestSearchRepositoryPrefersAuthoredUsagesBeforeMirrorQuota(t *testing.T) {
	repo := t.TempDir()
	definition := `export function isAsync(value: unknown): boolean {
  return value != null && typeof value === "object";
}
`
	usages := `function checkFirst(value: unknown) { return isAsync(value); }
function checkSecond(value: unknown) { return isAsync(value); }
function checkThird(value: unknown) { return isAsync(value); }
function checkFourth(value: unknown) { return isAsync(value); }
`
	write(t, repo, "src/helpers/parseUtil.ts", definition)
	write(t, repo, "runtime-copy/lib/helpers/parseUtil.ts", definition)
	write(t, repo, "src/core/types.ts", usages)
	write(t, repo, "runtime-copy/lib/core/types.ts", usages)

	response, err := SearchRepository(t.Context(), repo, "test", "isAsync", SearchOptions{
		Worktree:          true,
		Profile:           ProfileSyntaxOnly,
		TopK:              10,
		MaxRegionsPerFile: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	foundAuthoredUsage := false
	for _, result := range response.Results {
		if !containsString(result.Signals, "symbol-usage") {
			continue
		}
		if result.FilePath == "runtime-copy/lib/core/types.ts" {
			t.Fatalf("bounded usage scan selected runtime mirror before authored source: %#v", response.Results)
		}
		if result.FilePath == "src/core/types.ts" {
			foundAuthoredUsage = true
		}
	}
	if !foundAuthoredUsage {
		t.Fatalf("authored source usage missing from results: %#v", response.Results)
	}
}

func TestIdentifierUsageExpansionPrefersAuthoredPathBeforePerSeedLimit(t *testing.T) {
	const sourcePath = "src/core/types.ts"
	const mirrorPath = "runtime-copy/lib/core/types.ts"
	content := `function checkFirst(value: unknown) { return isAsync(value); }
function checkSecond(value: unknown) { return isAsync(value); }
function checkThird(value: unknown) { return isAsync(value); }
function checkFourth(value: unknown) { return isAsync(value); }
`
	seed := SymbolRecord{
		ID:            "is-async",
		FilePath:      "src/helpers/parseUtil.ts",
		Language:      "TypeScript",
		Kind:          "function",
		Name:          "isAsync",
		QualifiedName: "isAsync",
	}
	read := func(filePath string) (string, bool) {
		if filePath == sourcePath || filePath == mirrorPath {
			return content, true
		}
		return "", false
	}

	got := expandIdentifierUsageCandidates(
		t.Context(),
		[]searchCandidate{{score: 20, result: SearchResult{SymbolID: seed.ID}}},
		buildSearchQuery("isAsync"),
		map[string]SymbolRecord{seed.ID: seed},
		nil,
		read,
		map[string]string{sourcePath: "TypeScript", mirrorPath: "TypeScript"},
		SearchOptions{ContextLines: 2, MaxSnippetLines: 40},
	)
	if len(got) != 3 {
		t.Fatalf("usage count = %d, want per-seed limit 3: %#v", len(got), got)
	}
	for _, candidate := range got {
		if candidate.result.FilePath != sourcePath {
			t.Fatalf("usage limit admitted mirror before authored source: %#v", got)
		}
	}
}

func TestDedupeSemanticMirrorCandidatesPrefersAuthoredSource(t *testing.T) {
	symbols := map[string]SymbolRecord{
		"mirror": searchMirrorTestSymbol("mirror", "build/generated/helpers/promise.ts", "same-body"),
		"source": searchMirrorTestSymbol("source", "src/helpers/promise.ts", "same-body"),
	}
	candidates := []searchCandidate{
		searchMirrorTestCandidate(symbols["mirror"], 20, "body", "mirror-signal"),
		searchMirrorTestCandidate(symbols["source"], 10, "body", "source-signal"),
	}

	got := dedupeSemanticMirrorCandidates(candidates, buildSearchQuery("parse user"), symbols)
	if len(got) != 1 {
		t.Fatalf("deduped candidates = %#v, want one", got)
	}
	if got[0].result.FilePath != "src/helpers/promise.ts" {
		t.Fatalf("canonical path = %q, want authored source", got[0].result.FilePath)
	}
	if got[0].score != 20 {
		t.Fatalf("canonical candidate lost strongest score: %v", got[0].score)
	}
	for _, signal := range []string{"mirror-signal", "source-signal"} {
		if !containsString(got[0].result.Signals, signal) {
			t.Fatalf("canonical candidate signals = %#v, missing %q", got[0].result.Signals, signal)
		}
	}
}

func TestDedupeSemanticMirrorCandidatesRecognizesNestedRuntimeMirror(t *testing.T) {
	symbols := map[string]SymbolRecord{
		"runtime": searchMirrorTestSymbol("runtime", "runtime-copy/lib/helpers/promise.ts", "same-body"),
		"source":  searchMirrorTestSymbol("source", "src/helpers/promise.ts", "same-body"),
	}
	candidates := []searchCandidate{
		searchMirrorTestCandidate(symbols["runtime"], 12, "body"),
		searchMirrorTestCandidate(symbols["source"], 12, "body"),
	}

	got := dedupeSemanticMirrorCandidates(candidates, buildSearchQuery("parse user"), symbols)
	if len(got) != 1 || got[0].result.FilePath != "src/helpers/promise.ts" {
		t.Fatalf("runtime mirror was not collapsed to authored path: %#v", got)
	}
}

func TestDedupeSemanticMirrorCandidatesRecognizesAuthoredAnchorWithoutSharedDirectory(t *testing.T) {
	symbols := map[string]SymbolRecord{
		"runtime": searchMirrorTestSymbol("runtime", "runtime-copy/lib/types.ts", "same-body"),
		"source":  searchMirrorTestSymbol("source", "src/types.ts", "same-body"),
	}
	candidates := []searchCandidate{
		searchMirrorTestCandidate(symbols["runtime"], 12, "body"),
		searchMirrorTestCandidate(symbols["source"], 12, "body"),
	}

	got := dedupeSemanticMirrorCandidates(candidates, buildSearchQuery("parse user"), symbols)
	if len(got) != 1 || got[0].result.FilePath != "src/types.ts" {
		t.Fatalf("authored anchor did not collapse runtime mirror: %#v", got)
	}
}

func TestDedupeSemanticMirrorCandidatesKeepsDistinctTrackedLibAndDistCode(t *testing.T) {
	symbols := map[string]SymbolRecord{
		"source": searchMirrorTestSymbol("source", "src/services/client.ts", "source-body"),
		"lib":    searchMirrorTestSymbol("lib", "lib/services/client.ts", "lib-body"),
		"dist":   searchMirrorTestSymbol("dist", "dist/services/client.ts", "dist-body"),
	}
	candidates := []searchCandidate{
		searchMirrorTestCandidate(symbols["source"], 12, "source"),
		searchMirrorTestCandidate(symbols["lib"], 11, "lib"),
		searchMirrorTestCandidate(symbols["dist"], 10, "dist"),
	}

	got := dedupeSemanticMirrorCandidates(candidates, buildSearchQuery("parse user"), symbols)
	if len(got) != len(candidates) {
		t.Fatalf("distinct tracked lib/dist code was excluded: %#v", got)
	}
}

func TestDedupeSemanticMirrorCandidatesKeepsUnrelatedExactCopies(t *testing.T) {
	symbols := map[string]SymbolRecord{
		"left":  searchMirrorTestSymbol("left", "src/alpha/client.ts", "same-body"),
		"right": searchMirrorTestSymbol("right", "src/beta/client.ts", "same-body"),
	}
	candidates := []searchCandidate{
		searchMirrorTestCandidate(symbols["left"], 12, "body"),
		searchMirrorTestCandidate(symbols["right"], 11, "body"),
	}

	got := dedupeSemanticMirrorCandidates(candidates, buildSearchQuery("parse user"), symbols)
	if len(got) != 2 {
		t.Fatalf("unrelated same-basename definitions were collapsed: %#v", got)
	}
}

func TestDedupeSemanticMirrorCandidatesKeepsSeparateMonorepoSources(t *testing.T) {
	symbols := map[string]SymbolRecord{
		"left":  searchMirrorTestSymbol("left", "packages/alpha/src/services/client.ts", "same-body"),
		"right": searchMirrorTestSymbol("right", "packages/beta/src/services/client.ts", "same-body"),
	}
	candidates := []searchCandidate{
		searchMirrorTestCandidate(symbols["left"], 12, "body"),
		searchMirrorTestCandidate(symbols["right"], 11, "body"),
	}

	got := dedupeSemanticMirrorCandidates(candidates, buildSearchQuery("parse user"), symbols)
	if len(got) != 2 {
		t.Fatalf("separate monorepo package sources were collapsed: %#v", got)
	}
}

func TestDedupeSemanticMirrorCandidatesUsesStrongResultFallback(t *testing.T) {
	source := searchMirrorTestCandidate(searchMirrorTestSymbol("", "src/schema/user.ts", ""), 9, "export function parseUser() { return true }", "source")
	mirror := searchMirrorTestCandidate(searchMirrorTestSymbol("", "gen/schema/user.ts", ""), 10, "export  function parseUser() {\n  return true\n}", "mirror")

	got := dedupeSemanticMirrorCandidates([]searchCandidate{mirror, source}, buildSearchQuery("parse user"), nil)
	if len(got) != 1 || got[0].result.FilePath != "src/schema/user.ts" {
		t.Fatalf("result-field fallback did not collapse mirror: %#v", got)
	}
}

func TestDedupeSemanticMirrorCandidatesRequiresMatchingIdentity(t *testing.T) {
	left := searchMirrorTestSymbol("left", "src/schema/user.ts", "same-body")
	right := searchMirrorTestSymbol("right", "generated/schema/user.ts", "same-body")
	right.QualifiedName = "parseAdmin"
	right.Name = "parseAdmin"
	right.Signature = "function parseAdmin(value: unknown): User"

	got := dedupeSemanticMirrorCandidates([]searchCandidate{
		searchMirrorTestCandidate(left, 10, "body"),
		searchMirrorTestCandidate(right, 9, "body"),
	}, buildSearchQuery("parse user"), map[string]SymbolRecord{"left": left, "right": right})
	if len(got) != 2 {
		t.Fatalf("different symbol identities were collapsed: %#v", got)
	}
}

func TestDedupeSemanticMirrorCandidatesAllowsBodyHashWithoutSignature(t *testing.T) {
	left := searchMirrorTestSymbol("left", "src/schema/user.ts", "same-body")
	right := searchMirrorTestSymbol("right", "generated/schema/user.ts", "same-body")
	left.Signature = ""
	right.Signature = ""

	got := dedupeSemanticMirrorCandidates([]searchCandidate{
		searchMirrorTestCandidate(left, 10, "body"),
		searchMirrorTestCandidate(right, 9, "body"),
	}, buildSearchQuery("parse user"), map[string]SymbolRecord{"left": left, "right": right})
	if len(got) != 1 || got[0].result.FilePath != "src/schema/user.ts" {
		t.Fatalf("body-hash mirror without signature was not collapsed: %#v", got)
	}
}

func TestDedupeSemanticMirrorCandidatesPreservesExplicitPathVariant(t *testing.T) {
	symbols := map[string]SymbolRecord{
		"source": searchMirrorTestSymbol("source", "src/schema/user.ts", "same-body"),
		"dist":   searchMirrorTestSymbol("dist", "dist/schema/user.ts", "same-body"),
	}
	candidates := []searchCandidate{
		searchMirrorTestCandidate(symbols["source"], 10, "body"),
		searchMirrorTestCandidate(symbols["dist"], 9, "body"),
	}

	got := dedupeSemanticMirrorCandidates(candidates, buildSearchQuery("dist parse user"), symbols)
	if len(got) != 2 {
		t.Fatalf("query-requested path variant was collapsed: %#v", got)
	}
}

func searchMirrorTestSymbol(id, filePath, bodyHash string) SymbolRecord {
	return SymbolRecord{
		ID:            id,
		FilePath:      filePath,
		Language:      "TypeScript",
		Kind:          "function",
		Name:          "parseUser",
		QualifiedName: "parseUser",
		Signature:     "function parseUser(value: unknown): User",
		BodyHash:      bodyHash,
	}
}

func searchMirrorTestCandidate(symbol SymbolRecord, score float64, snippet string, signals ...string) searchCandidate {
	return searchCandidate{
		score:     score,
		baseScore: score,
		result: SearchResult{
			FilePath:      symbol.FilePath,
			Language:      symbol.Language,
			Kind:          symbol.Kind,
			SymbolID:      symbol.ID,
			SymbolName:    symbol.Name,
			QualifiedName: symbol.QualifiedName,
			Signature:     symbol.Signature,
			Signals:       signals,
			Snippet:       snippet,
		},
	}
}
