package sem

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

const goNewExpressionFixture = `package fixture

func value() int { return 1 }
func helper(v int) int { return v + 1 }

func allocate() *int {
	return new(helper(value()))
}
`

func TestPrepareGoNewExpressionParseSourceIsNarrowAndPositionPreserving(t *testing.T) {
	source := `package fixture

// new(comment()) must remain authored.
var text = "new(stringLiteral())"
var typed = new(int)
var value = new(makeValue())
`
	prepared, changed := prepareGoNewExpressionParseSource(context.Background(), source)
	if !changed {
		t.Fatal("valid new(expression) source was not prepared")
	}
	if len(prepared) != len(source) || strings.Count(prepared, "\n") != strings.Count(source, "\n") {
		t.Fatalf("parse view changed source coordinates:\nsource=%q\nprepared=%q", source, prepared)
	}
	for _, unchanged := range []string{"new(comment())", `"new(stringLiteral())"`, "new(int)"} {
		if !strings.Contains(prepared, unchanged) {
			t.Errorf("parse view changed %q: %q", unchanged, prepared)
		}
	}
	if !strings.Contains(prepared, "n_w(makeValue())") {
		t.Fatalf("expression call was not prepared: %q", prepared)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, ok := prepareGoNewExpressionParseSource(ctx, source); ok || got != source {
		t.Fatalf("cancelled preparation = (%q, %v), want unchanged source", got, ok)
	}
	oversized := source + strings.Repeat(" ", defaultMaxParseBytes-len(source)+1)
	if got, ok := prepareGoNewExpressionParseSource(context.Background(), oversized); ok || got != oversized {
		t.Fatal("compatibility parser accepted input above its bounded file size")
	}
}

func TestGoNewExpressionParsesWithOriginalEntitiesAndRanges(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("fixture.go", goNewExpressionFixture)
	if language != "Go" || status.ParseError {
		t.Fatalf("parse = language %q status %+v", language, status)
	}
	byName := map[string]Entity{}
	for _, entity := range entities {
		byName[entity.Name] = entity
	}
	allocate, ok := byName["allocate"]
	if !ok {
		t.Fatalf("allocate entity missing: %#v", entities)
	}
	if allocate.StartLine != 6 || allocate.EndLine != 8 || allocate.Signature != "func allocate() *int" {
		t.Fatalf("allocate coordinates/signature = %#v", allocate)
	}
	const authored = "func allocate() *int {\n\treturn new(helper(value()))\n}"
	if got := goNewExpressionFixture[allocate.sourceStartByte:allocate.sourceEndByte]; got != authored {
		t.Fatalf("entity source range = %q, want authored %q", got, authored)
	}
	if allocate.BodyHash != hash(normalize(authored)) {
		t.Fatalf("body hash = %q, want hash of authored body %q", allocate.BodyHash, authored)
	}
	if strings.Contains(allocate.Signature, "n_w") || strings.Contains(authored, "n_w") {
		t.Fatal("private compatibility callee leaked into entity text")
	}

	symbols := entitySymbols("fixture/repo", "fixture.go", language, entities)
	for _, symbol := range symbols {
		if symbol.StableIDVersion != StableSymbolIDVersion {
			t.Fatalf("stable ID version = %q, want %q", symbol.StableIDVersion, StableSymbolIDVersion)
		}
		if strings.Contains(symbol.ID, "n_w") || strings.Contains(symbol.Signature, "n_w") {
			t.Fatalf("private compatibility callee leaked into symbol: %#v", symbol)
		}
	}
}

func TestGoNewExpressionKeepsNestedCallsAndCacheReuseStable(t *testing.T) {
	repo := t.TempDir()
	cacheDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module fixture.local/newexpr\n\ngo 1.26\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "fixture.go"), []byte(goNewExpressionFixture), 0600); err != nil {
		t.Fatal(err)
	}
	options := ProviderSnapshotOptions{Worktree: true, ExtractionReuse: true, ExtractionCacheDir: cacheDir}
	build := func() ProviderSnapshot {
		t.Helper()
		snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "fixture-version", options)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}

	cold := build()
	if got := cold.Header.Stats.Extraction; got == nil || got.FilesParsed != 1 || got.FilesReused != 0 {
		t.Fatalf("cold extraction stats = %#v", got)
	}
	warm := build()
	if got := warm.Header.Stats.Extraction; got == nil || got.FilesParsed != 0 || got.FilesReused != 1 {
		t.Fatalf("warm extraction stats = %#v", got)
	}
	if !reflect.DeepEqual(cold.Symbols, warm.Symbols) || !reflect.DeepEqual(cold.Relations, warm.Relations) {
		t.Fatal("cache reuse changed symbols, IDs, body hashes, ranges, or relations")
	}
	if !hasRelationByLastSegment(cold.Relations, "CALLS", "allocate", "helper") ||
		!hasRelationByLastSegment(cold.Relations, "CALLS", "allocate", "value") {
		t.Fatalf("nested real calls missing from snapshot: %#v", cold.Relations)
	}
	for _, relation := range cold.Relations {
		if strings.Contains(relation.FromID, "n_w") || strings.Contains(relation.ToID, "n_w") {
			t.Fatalf("private compatibility callee leaked into relation: %#v", relation)
		}
	}
	for _, failure := range cold.Header.PartialFailures {
		if failure.FilePath == "fixture.go" {
			t.Fatalf("valid new(expression) retained a partial failure: %#v", failure)
		}
	}
}

func TestGoNewExpressionDoesNotHideInvalidSyntax(t *testing.T) {
	for name, source := range map[string]string{
		"broken nested call": "package fixture\nfunc value() int { return 1 }\nfunc broken() { _ = new(value(} }\n",
		"multiple arguments": "package fixture\nfunc value() int { return 1 }\nfunc broken() { _ = new(value(), value()) }\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, status := TreeSitterParser{}.ParseWithStatus("broken.go", source)
			if !status.ParseError || status.Code != "E_PARSE_ERROR" {
				t.Fatalf("invalid source status = %+v, want E_PARSE_ERROR", status)
			}
		})
	}
}

func TestGoNewExpressionRetryPreservesTimeoutStatus(t *testing.T) {
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(treeSitterLanguages[".go"].grammar)
	tree, err := parser.ParseCtx(context.Background(), nil, []byte(goNewExpressionFixture))
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	root := tree.RootNode()
	if root == nil || !root.HasError() {
		t.Fatal("fixture must exercise the Go compatibility retry")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	retryTree, retryRoot, status := retryGoNewExpressionParse(ctx, parser, root, goNewExpressionFixture)
	if retryTree != nil || retryRoot != root {
		t.Fatal("cancelled retry replaced the original tree")
	}
	if status == nil || !status.ParseError || status.Code != "E_PARSE_TIMEOUT" || status.DeterministicSyntaxError {
		t.Fatalf("cancelled retry status = %#v, want non-deterministic E_PARSE_TIMEOUT", status)
	}
}

func TestExtractionFormatVersionInvalidatesPreCompatibilityRecord(t *testing.T) {
	const previousVersion = 4
	if extractionFormatVersion != previousVersion+1 {
		t.Fatalf("fixture requires one parser ABI step: current=%d previous=%d", extractionFormatVersion, previousVersion)
	}
	cache := &extractionCache{
		ctx:         context.Background(),
		directory:   t.TempDir(),
		repository:  "fixture/repo",
		build:       "fixture-build",
		maxBytes:    extractionDiskLimit,
		maxEntries:  extractionEntryLimit,
		limitsReady: true,
	}
	spec := resolveProfile(ProfileFull)
	language, ok := languageForPath("fixture.go")
	if !ok {
		t.Fatal("Go language unavailable")
	}
	source := captureSource("fixture.go", goNewExpressionFixture)
	oldKey := extractionIdentity(cache.repository, source.path, source.digest, language.language, string(spec.name), strconv.Itoa(defaultMaxParseBytes), strconv.Itoa(previousVersion), cache.build)
	oldEntry, err := newCacheEntry(cache.directory, "extraction-"+extractionIdentity(cache.repository), "v1", oldKey)
	if err != nil {
		t.Fatal(err)
	}
	oldRecord := extractionRecord{
		Version:      previousVersion,
		Language:     "Go",
		Declarations: []extractedDeclaration{{Kind: "function", Name: "stale", StartLine: 1, EndLine: 1}},
	}
	payload, err := json.Marshal(oldRecord)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := marshalCacheJSON(extractionEnvelope{Key: oldKey, PayloadDigest: contentHash(payload), Record: oldRecord})
	if err != nil {
		t.Fatal(err)
	}
	if err := oldEntry.writeEncoded("extract", encoded); err != nil {
		t.Fatal(err)
	}

	extraction, hit := cache.extract(spec, language, source, defaultMaxParseBytes)
	if hit {
		t.Fatal("pre-compatibility extraction record was reused")
	}
	for _, entity := range extraction.entities {
		if entity.Name == "stale" {
			t.Fatalf("stale v%d declaration escaped invalidation: %#v", previousVersion, extraction.entities)
		}
	}
	cache.flush()
	warm, hit := cache.extract(spec, language, source, defaultMaxParseBytes)
	if !hit || !reflect.DeepEqual(warm, extraction) {
		t.Fatalf("rebuilt v%d extraction did not reuse: hit=%v warm=%#v cold=%#v", extractionFormatVersion, hit, warm, extraction)
	}
	cache.flush()
}
