package sem

import (
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// lexicalInvarianceRun is what one arm of the invariance tests observed: the pre-fusion lexical
// ranking as (file, span, symbol, score, signals) rows, and the idf statistics it was scored with.
type lexicalInvarianceRun struct {
	response SearchResponse
	rows     []string
	df       map[string]int
	files    int
	exact    bool
}

func runLexicalInvariance(t *testing.T, repo, query string, options SearchOptions) lexicalInvarianceRun {
	t.Helper()
	var run lexicalInvarianceRun
	options.idfObserver = func(df map[string]int, files int, exact bool) {
		run.df = map[string]int{}
		for term, count := range df {
			run.df[term] = count
		}
		run.files, run.exact = files, exact
	}
	options.preFusionObserver = func(selected []searchCandidate) {
		run.rows = nil
		for _, candidate := range selected {
			result := candidate.result
			run.rows = append(run.rows, fmt.Sprintf("%s:%d-%d sym=%s score=%.6f signals=%v",
				result.FilePath, result.StartLine, result.EndLine, result.SymbolID, candidate.score, result.Signals))
		}
	}
	response, err := SearchRepository(t.Context(), repo, "test-version", query, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
	run.response = response
	return run
}

// lexicalInvarianceRepo: twelve lexical matches for "quit ... server" and one file the lexical pass
// cannot see (no query word) that the channel nominates. The nominated function CALLS three of the
// lexical functions, so if its symbols or call edges reached the lexical pipeline they would raise
// those functions' caller degree and move their scores — the channel changing lexical scoring
// through the graph rather than through the file set.
func lexicalInvarianceRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	initRepo(t, repo)
	// Twelve lexical matches: more symbols than the channel's top-k, so on the cold path some
	// lexical tail file is not itself a hit and can yield its slot.
	for i := 1; i <= 12; i++ {
		writeFile(t, repo, fmt.Sprintf("srv/s%d.go", i),
			fmt.Sprintf("package srv\n\n// Zz%d answers.\nfunc Zz%d() string {\n\treturn \"quit server\"\n}\n", i, i))
	}
	writeFile(t, repo, "srv/pool.go", `package srv

// AbandonStalledBackend drops a stalled backend: abandon it.
func AbandonStalledBackend() {
	Zz1()
	Zz2()
	Zz3()
}
`)
	// Eight more near neighbours the lexical pass cannot see, so the channel's top-k is not simply
	// "every symbol in the repository" and the cold path has lexical tail files that are not hits.
	var near strings.Builder
	near.WriteString("package util\n\n")
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&near, "// Stalled%c is stalled.\nfunc Stalled%c() {}\n\n", 'A'+i, 'A'+i)
	}
	writeFile(t, repo, "util/near.go", near.String())
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "fixture")
	return repo
}

// N1, WARM path (preindexed at the search's own profile, git-tree-grep keeps every matched file):
// the ON arm's pre-fusion lexical ranking — file, span, symbol, score and signals, in order — and
// its idf statistics equal the OFF arm's exactly, while the channel is genuinely used and genuinely
// nominated a file.
func TestSemanticWarmPathLexicalRankingIsInvariant(t *testing.T) {
	repo := lexicalInvarianceRepo(t)
	server := httptest.NewServer(&fakeEmbedder{})
	defer server.Close()
	cacheDir := t.TempDir()
	snapshot, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version",
		ProviderSnapshotOptions{NoNetwork: true, Profile: ProfileFull}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildSemanticIndex(t.Context(), repo, snapshot, cacheDir,
		SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel}, false); err != nil {
		t.Fatal(err)
	}
	const query = "quit sluggish server"
	options := func(semantic *SemanticConfig) SearchOptions {
		return SearchOptions{CacheDir: cacheDir, MaxIndexedFiles: 2, TopK: 10, Profile: ProfileFull, Semantic: semantic}
	}
	off := runLexicalInvariance(t, repo, query, options(nil))
	on := runLexicalInvariance(t, repo, query, options(&SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel}))

	// Non-vacuity: this is the warm path, the channel ran, nominated the unseen file and seated it.
	if off.response.Stats.PreselectionBackend != "git-tree-grep" || !off.response.Stats.IndexCacheHit {
		t.Fatalf("fixture drift: not the warm git-tree-grep path: backend=%s hit=%v",
			off.response.Stats.PreselectionBackend, off.response.Stats.IndexCacheHit)
	}
	if off.response.Stats.FilesIndexed <= 2 {
		t.Fatalf("fixture drift: warm path did not keep every matched file past the cap: %d", off.response.Stats.FilesIndexed)
	}
	if on.response.Stats.SemanticStatus != SemanticStatusUsed || on.response.Stats.SemanticNominatedFiles == 0 {
		t.Fatalf("channel not used/nominating: %+v", on.response.Stats)
	}
	if on.response.Stats.SemanticEvictedFiles != 0 {
		t.Fatalf("warm path evicted %d lexical files; nominations must be additive", on.response.Stats.SemanticEvictedFiles)
	}
	if !slices.ContainsFunc(on.response.Results, func(result SearchResult) bool {
		return result.SymbolName == "AbandonStalledBackend" && result.SemanticOnly()
	}) {
		t.Errorf("nominated symbol not delivered as a semantic-only row: %v", resultFiles(on.response.Results))
	}
	if len(off.rows) == 0 {
		t.Fatal("observer saw no lexical rows")
	}

	if fmt.Sprint(off.df) != fmt.Sprint(on.df) || off.files != on.files || off.exact != on.exact {
		t.Errorf("idf differs: off=%v/%d/%v on=%v/%d/%v", off.df, off.files, off.exact, on.df, on.files, on.exact)
	}
	if off.response.Stats.LexicalCandidates != on.response.Stats.LexicalCandidates {
		t.Errorf("lexical candidate pool differs: off=%d on=%d", off.response.Stats.LexicalCandidates, on.response.Stats.LexicalCandidates)
	}
	if strings.Join(off.rows, "\n") != strings.Join(on.rows, "\n") {
		t.Errorf("pre-fusion lexical ranking differs\nOFF:\n%s\nON:\n%s", strings.Join(off.rows, "\n"), strings.Join(on.rows, "\n"))
	}
	// And every lexical row the ON arm delivers carries the score the OFF arm reported for it.
	offScores := map[string]float64{}
	for _, result := range off.response.Results {
		offScores[fmt.Sprintf("%s:%d-%d", result.FilePath, result.StartLine, result.EndLine)] = result.Score
	}
	for _, result := range on.response.Results {
		if result.SemanticOnly() || result.Section != "" {
			continue
		}
		key := fmt.Sprintf("%s:%d-%d", result.FilePath, result.StartLine, result.EndLine)
		if score, ok := offScores[key]; ok && score != result.Score {
			t.Errorf("%s delivered score %v with the channel on, %v off", key, result.Score, score)
		}
	}
}

// N1, COLD path (no complete snapshot at the search's profile): nominations are spent inside
// --max-indexed-files and evict lexical tail files. What the channel may cost here is exactly the
// evicted files' rows (plus what the lexical snapshot loses with their symbols: BM25's average
// document length over the candidate pool and any call edges they carried — so surviving scores can
// drift, and the payload discloses the eviction). What it may NOT change is idf or the corpus
// presence of a query word: both still come from the pre-nomination selection.
//
// The fixture makes presence observable. "yak" occurs only in srv/other.go, a lexical tail file the
// plan evicts. Were presence computed from the post-eviction files, "yak" would become an
// unmatchable word and drop out of the query's matchable sequence, which would then spell the head
// symbol ZebraMoose ("zebra" "moose" adjacent) and award it the exact-symbol bonus and signal that
// OFF — where "yak" sits between them — does not.
func TestSemanticColdPathEvictionKeepsLexicalIDF(t *testing.T) {
	repo := lexicalInvarianceRepo(t)
	writeFile(t, repo, "srv/zebra.go", "package srv\n\n// ZebraMoose answers.\nfunc ZebraMoose() string {\n\treturn \"quit zebra server moose\"\n}\n")
	writeFile(t, repo, "srv/other.go", "package srv\n\n// Other answers.\nfunc Other() string {\n\treturn \"quit server yak\"\n}\n")
	// The nominated file also mentions "server", so it WOULD yield lexical rows if nominated files
	// ever entered the lexical passes; the cap keeps it out of the cold lexical selection.
	writeFile(t, repo, "srv/pool.go", "package srv\n\n// AbandonStalledBackend drops a stalled server backend: abandon it.\nfunc AbandonStalledBackend() {\n\tZz1()\n}\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "presence")
	server := httptest.NewServer(&fakeEmbedder{})
	defer server.Close()
	cacheDir := t.TempDir()
	// The index (and its corpus snapshot) is prepared at the FULL profile; the search runs at FAST,
	// whose complete snapshot was never built, so its lexical files are parsed cold.
	buildSemanticFixtureIndex(t, repo, cacheDir, server.URL)
	const query = "quit sluggish server zebra yak moose"
	options := func(semantic *SemanticConfig) SearchOptions {
		return SearchOptions{CacheDir: cacheDir, MaxIndexedFiles: 3, TopK: 10, Profile: ProfileFast, Semantic: semantic}
	}
	off := runLexicalInvariance(t, repo, query, options(nil))
	on := runLexicalInvariance(t, repo, query, options(&SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel}))
	t.Logf("idf exact=%v N=%d df=%v evicted=%d\noff rows:\n%s\non rows:\n%s", on.exact, on.files, on.df,
		on.response.Stats.SemanticEvictedFiles, strings.Join(off.rows, "\n"), strings.Join(on.rows, "\n"))
	if on.response.Stats.SemanticStatus != SemanticStatusUsed || on.response.Stats.SemanticNominatedFiles == 0 {
		t.Fatalf("channel not used/nominating: %+v", on.response.Stats)
	}
	if on.response.Stats.SemanticEvictedFiles == 0 {
		t.Fatalf("fixture drift: nothing evicted (lexical files=%d)", off.response.Stats.FilesIndexed)
	}
	if on.response.Stats.FilesIndexed > max(3, off.response.Stats.FilesIndexed) {
		t.Fatalf("cold cap breached: indexed %d", on.response.Stats.FilesIndexed)
	}
	if !slices.ContainsFunc(off.rows, func(row string) bool { return strings.HasPrefix(row, "srv/other.go:") }) ||
		slices.ContainsFunc(on.rows, func(row string) bool { return strings.HasPrefix(row, "srv/other.go:") }) {
		t.Fatalf("fixture drift: srv/other.go must be a lexical row OFF and evicted ON")
	}
	if slices.ContainsFunc(off.rows, func(row string) bool { return strings.HasPrefix(row, "srv/pool.go:") }) ||
		!slices.ContainsFunc(on.response.Results, func(result SearchResult) bool {
			return result.FilePath == "srv/pool.go" && result.SemanticOnly()
		}) {
		t.Fatalf("fixture drift: srv/pool.go must be lexically unselected and delivered as a semantic-only row")
	}
	if fmt.Sprint(off.df) != fmt.Sprint(on.df) || off.files != on.files || off.exact != on.exact {
		t.Errorf("idf differs: off=%v/%d/%v on=%v/%d/%v", off.df, off.files, off.exact, on.df, on.files, on.exact)
	}
	// Every surviving ON row is an OFF row with the same span, symbol and signals (the signals carry
	// the presence judgement; the score may drift with BM25's average length, as documented).
	identity := func(row string) string {
		scoreAt := strings.Index(row, " score=")
		signalsAt := strings.Index(row, " signals=")
		return row[:scoreAt] + row[signalsAt:]
	}
	offIdentities := map[string]bool{}
	for _, row := range off.rows {
		offIdentities[identity(row)] = true
	}
	if len(on.rows) == 0 {
		t.Fatal("no surviving lexical rows")
	}
	for _, row := range on.rows {
		if !offIdentities[identity(row)] {
			t.Errorf("ON lexical row has no OFF twin (span/symbol/signals): %s", row)
		}
	}
}

// The warm plan is additive: whatever the cap, no lexical file yields, and the nominations are the
// same files the cold plan would choose first.
func TestSemanticPlanIsAdditiveWhenWarm(t *testing.T) {
	t.Parallel()
	selected := []string{"l1.go", "l2.go", "l3.go"}
	corpus := []string{"l1.go", "l2.go", "l3.go", "n1.go", "n2.go"}
	hits := []semanticHit{{FilePath: "n1.go"}, {FilePath: "l2.go"}, {FilePath: "n2.go"}}
	warm := planSemanticNominations(selected, corpus, hits, semanticTopK, 1, true)
	if strings.Join(warm.nominated, ",") != "n1.go,n2.go" || len(warm.evicted) != 0 {
		t.Fatalf("warm plan: nominated %v evicted %v, want n1.go,n2.go and none", warm.nominated, warm.evicted)
	}
	if got := warm.withoutEvicted(selected); strings.Join(got, ",") != "l1.go,l2.go,l3.go" {
		t.Fatalf("warm plan changed the lexical selection: %v", got)
	}
	cold := planSemanticNominations(selected, corpus, hits, semanticTopK, 1, false)
	if strings.Join(cold.nominated, ",") != "n1.go" || !cold.evicted["l3.go"] || len(cold.evicted) != 1 {
		t.Fatalf("cold plan: nominated %v evicted %v, want n1.go / l3.go", cold.nominated, cold.evicted)
	}
}
