package sem

import (
	"fmt"
	"strings"
	"testing"
)

func TestSearchRepositoryRanksExactSymbol(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "config/service.go", `package config

type ServiceConfig struct { Name string }

func NewServiceConfig(name string) ServiceConfig {
	return ServiceConfig{Name: name}
}
`)
	write(t, repo, "docs/example.go", `package docs

// This example discusses constructing a service configuration.
func Example() {}
`)

	response, err := SearchRepository(t.Context(), repo, "test", "NewServiceConfig", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) == 0 {
		t.Fatal("search returned no results")
	}
	first := response.Results[0]
	if first.FilePath != "config/service.go" || first.SymbolName != "NewServiceConfig" {
		t.Fatalf("first result = %#v", first)
	}
	if !containsString(first.Signals, "exact-symbol") {
		t.Fatalf("exact symbol signal missing: %#v", first.Signals)
	}
}

func TestSearchRepositoryFindsConceptualBodyText(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "encoding/serializer.go", `package encoding

// MarshalCompact emits minified serialization output for network transport.
func MarshalCompact(value any) []byte { return nil }

// MarshalIndented provides pretty printing for human-readable output.
func MarshalIndented(value any) []byte { return nil }
`)
	write(t, repo, "transport/socket.go", `package transport

// Send writes bytes to a socket.
func Send(value []byte) error { return nil }
`)

	response, err := SearchRepository(t.Context(), repo, "test", "minified and pretty printing serialization output", SearchOptions{
		Worktree:          true,
		Profile:           ProfileSyntaxOnly,
		TopK:              5,
		MaxRegionsPerFile: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) < 2 {
		t.Fatalf("results = %#v stats = %#v", response.Results, response.Stats)
	}
	seen := map[string]bool{}
	for _, result := range response.Results {
		if result.FilePath == "encoding/serializer.go" {
			seen[result.SymbolName] = true
		}
	}
	if !seen["MarshalCompact"] || !seen["MarshalIndented"] {
		t.Fatalf("conceptual query did not preserve both relevant regions: %#v", response.Results)
	}
}

func TestSearchRepositoryPreservesDistinctRegionsInOneFile(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "timing/wheel.go", `package timing

// NewTimingWheel creates the production timing wheel.
func NewTimingWheel(interval int) *Wheel {
	return newTimingWheelWithClock(interval, systemClock{})
}

// newTimingWheelWithClock creates the timing wheel with an injected clock.
func newTimingWheelWithClock(interval int, clock Clock) *Wheel {
	return &Wheel{}
}

type Wheel struct{}
type Clock interface{}
type systemClock struct{}
`)

	response, err := SearchRepository(t.Context(), repo, "test", "create timing wheel with injected clock", SearchOptions{
		Worktree:          true,
		Profile:           ProfileSyntaxOnly,
		TopK:              5,
		MaxRegionsPerFile: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]SearchResult{}
	for _, result := range response.Results {
		seen[result.SymbolName] = result
	}
	constructor, constructorOK := seen["NewTimingWheel"]
	helper, helperOK := seen["newTimingWheelWithClock"]
	if !constructorOK || !helperOK {
		t.Fatalf("missing distinct same-file regions: %#v", response.Results)
	}
	if constructor.StartLine == helper.StartLine {
		t.Fatalf("regions collapsed to one location: %#v", response.Results)
	}
}

func TestSearchRepositoryExpandsIssueConceptsToAPIVocabulary(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "reader/collection.go", `package reader

// newCollectionReader selects the collection implementation, including EnumSet.
func newCollectionReader(kind string) *CollectionReader { return &CollectionReader{} }

// readObject parses values from the input stream.
func (r *CollectionReader) readObject(input []byte) any { return nil }

type CollectionReader struct{}
`)

	response, err := SearchRepository(t.Context(), repo, "test", "EnumSet deserialization failure", SearchOptions{
		Worktree:          true,
		Profile:           ProfileSyntaxOnly,
		TopK:              10,
		MaxRegionsPerFile: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	seenReader := false
	for _, result := range response.Results {
		if result.SymbolName == "readObject" {
			seenReader = true
		}
	}
	if !seenReader {
		t.Fatalf("deserialization did not expand to reader API vocabulary: %#v", response.Results)
	}
}

func TestSearchRepositoryExpandsSemanticNeighbor(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "auth/auth.go", `package auth

// Authenticate creates a user session after successful login.
func Authenticate(raw string) bool {
	return checkSignature(raw)
}

func checkSignature(raw string) bool {
	return len(raw) > 4
}
`)

	response, err := SearchRepository(t.Context(), repo, "test", "authenticate user session login", SearchOptions{
		Worktree: true,
		Profile:  ProfileFast,
		TopK:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	var helper SearchResult
	for _, result := range response.Results {
		if result.SymbolName == "checkSignature" {
			helper = result
			break
		}
	}
	if helper.SymbolName == "" {
		t.Fatalf("graph neighbor missing: %#v", response.Results)
	}
	foundGraphSignal := false
	for _, signal := range helper.Signals {
		if strings.HasPrefix(signal, "graph:") {
			foundGraphSignal = true
		}
	}
	if !foundGraphSignal {
		t.Fatalf("helper was not identified through graph expansion: %#v", helper)
	}
}

func TestSearchRepositoryRejectsStopWordsOnly(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "main.go", "package main\n")
	_, err := SearchRepository(t.Context(), repo, "test", "the and with", SearchOptions{Worktree: true})
	if err == nil {
		t.Fatal("expected an error for a stop-word-only query")
	}
}

func TestSearchRepositoryReusesCommittedIndexCache(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")
	write(t, repo, "auth.go", `package auth

// ValidateToken verifies an authentication token.
func ValidateToken(token string) bool { return token != "" }
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	options := SearchOptions{
		Profile:  ProfileFast,
		TopK:     5,
		CacheDir: t.TempDir(),
	}
	first, err := SearchRepository(t.Context(), repo, "test-version", "validate authentication token", options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Stats.IndexCacheHit {
		t.Fatal("first search unexpectedly hit the index cache")
	}
	second, err := SearchRepository(t.Context(), repo, "test-version", "validate authentication token", options)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Stats.IndexCacheHit {
		t.Fatal("second search did not reuse the committed index cache")
	}
	if len(first.Results) == 0 || len(second.Results) == 0 || first.Results[0].SymbolID != second.Results[0].SymbolID {
		t.Fatalf("cache changed retrieval: first=%#v second=%#v", first.Results, second.Results)
	}
}

func TestSearchRepositorySelectivelyIndexesLargeRepositories(t *testing.T) {
	repo := t.TempDir()
	for index := 0; index < 20; index++ {
		write(t, repo, fmt.Sprintf("noise/file_%02d.go", index), fmt.Sprintf("package noise\nfunc Noise%d() int { return %d }\n", index, index))
	}
	write(t, repo, "target/needle.go", `package target

// NeedleTarget handles the rare selective-indexing request.
func NeedleTarget() bool { return true }
`)
	response, err := SearchRepository(t.Context(), repo, "test", "NeedleTarget selective indexing", SearchOptions{
		Worktree:        true,
		Profile:         ProfileSyntaxOnly,
		TopK:            5,
		MaxIndexedFiles: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Stats.FilesScanned != 21 || response.Stats.FilesIndexed > 4 {
		t.Fatalf("unexpected selective-index stats: %#v", response.Stats)
	}
	if len(response.Results) == 0 || response.Results[0].SymbolName != "NeedleTarget" {
		t.Fatalf("selective index lost the target: %#v", response.Results)
	}
}

func TestSearchTermMatcherIsCaseInsensitiveAndFindsOverlaps(t *testing.T) {
	terms := []string{"list", "listitem", "secondaryaction", "missing"}
	matched := newSearchTermMatcher(terms).match("export const ListItemSecondaryAction = true")
	for _, index := range []int{0, 1, 2} {
		if !matched[index] {
			t.Fatalf("term %q was not matched: %#v", terms[index], matched)
		}
	}
	if matched[3] {
		t.Fatalf("absent term matched: %#v", matched)
	}
}

func TestSearchTokenVariantsKeepQualifiedCompoundIdentifiers(t *testing.T) {
	variants := searchTokenVariants("d.cc.NewServiceConfig")
	for _, want := range []string{"d.cc.newserviceconfig", "newserviceconfig", "new", "service", "config"} {
		if !containsString(variants, want) {
			t.Fatalf("variant %q missing from %#v", want, variants)
		}
	}
}

func TestCodeLikeSearchTokenIgnoresProsePunctuation(t *testing.T) {
	for _, token := range []string{"documentation.", "Currently,", "spaces."} {
		if codeLikeSearchToken(token) {
			t.Fatalf("prose token %q classified as code", token)
		}
	}
	for _, token := range []string{"NewServiceConfig", "resolver_conn_wrapper", "foo/bar.go", "--head", "DOM"} {
		if !codeLikeSearchToken(token) {
			t.Fatalf("code token %q was not classified as code", token)
		}
	}
}

func TestCodeLikeSearchWeightDoesNotOverweightShortAcronyms(t *testing.T) {
	if got := codeLikeSearchWeight("DOM"); got >= 2 {
		t.Fatalf("short acronym weight = %v", got)
	}
	if got := codeLikeSearchWeight("NewServiceConfig"); got <= 2 {
		t.Fatalf("compound identifier weight = %v", got)
	}
}

func TestDiverseSelectionDoesNotSpendBudgetOnClones(t *testing.T) {
	candidates := []searchCandidate{
		{score: 10, result: SearchResult{FilePath: "bench/a/item.go", StartLine: 1, EndLine: 2, SymbolName: "same"}},
		{score: 9.9, result: SearchResult{FilePath: "bench/b/item.go", StartLine: 1, EndLine: 2, SymbolName: "same"}},
		{score: 8, result: SearchResult{FilePath: "src/implementation.go", StartLine: 1, EndLine: 2, SymbolName: "implementation"}},
	}
	selected := selectDiverseCandidates(candidates, 2, 3)
	if len(selected) != 2 || selected[1].result.SymbolName != "implementation" {
		t.Fatalf("clone consumed diversity budget: %#v", selected)
	}
}

func TestSearchPathPriorPrefersProductCodeUnlessWorkflowRequested(t *testing.T) {
	issue := buildSearchQuery("pretty print DOM with documentation")
	if source, workflow := searchPathPrior(issue, "include/dom/serialization.h"), searchPathPrior(issue, ".github/workflows/documentation.yml"); source <= workflow {
		t.Fatalf("source prior %v did not exceed workflow prior %v", source, workflow)
	}
	workflowIssue := buildSearchQuery("fix CI workflow pipeline")
	if got := searchPathPrior(workflowIssue, ".github/workflows/test.yml"); got < 0 {
		t.Fatalf("explicit workflow query was penalized: %v", got)
	}
	testIssue := buildSearchQuery("add regression test for parser")
	if got := searchPathPrior(testIssue, "tests/parser_test.go"); got < 0 {
		t.Fatalf("explicit test query was penalized: %v", got)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
