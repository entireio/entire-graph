package sem

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Reuse the maintained language matrix and graph fixtures, so new supported
// syntax joins the differential oracle without a second set of copied samples.
func relationFactsCorpus(t testing.TB) map[string]string {
	t.Helper()
	sources := map[string]string{}
	specs := supportedLanguageSpecs()
	for _, root := range []string{"testdata/langmatrix", "testdata/fixtures"} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !callExtractionLanguage(specs[strings.ToLower(filepath.Ext(path))].language) {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sources[path] = string(content)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return sources
}

func TestRelationFactsLanguageCorpus(t *testing.T) {
	counts := map[string]int{}
	for path, source := range relationFactsCorpus(t) {
		t.Run(path, func(t *testing.T) {
			entities, language, _ := (TreeSitterParser{}).ParseWithStatus(path, source)
			for _, symbol := range entitySymbols("test", path, language, entities) {
				if typeLikeKind(symbol.Kind) {
					continue
				}
				block := symbolBlock(source, symbol)
				if language == "JavaScript" || language == "TypeScript" || language == "Julia" {
					if exact, ok := exactSymbolSource(source, symbol); ok {
						block = exact
					}
				}
				legacy := newSymbolBody(block)
				shared := newRelationSymbolBody(block, language, resolveProfile(ProfileFull))
				if shared.facts == nil {
					t.Fatalf("%s did not activate shared facts", language)
				}
				assertRelationBodyEqual(t, language, symbol.Name, legacy, shared)
				assertRelationReturnFlowsEqual(t, language, symbol.Name, legacy, shared, symbolFlowParameterNames(symbol))
				counts[language]++
			}
		})
	}
	var languages []string
	for language := range counts {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	for _, language := range languages {
		t.Logf("%s: %d bodies", language, counts[language])
	}
	for _, spec := range supportedLanguageSpecs() {
		if callExtractionLanguage(spec.language) && counts[spec.language] == 0 {
			t.Errorf("no differential bodies for %s", spec.language)
		}
	}
}

// Resolution is compared too: sharing raw facts must preserve complete ordered
// relations, including evidence and misses, in both serial and parallel passes.
func TestRelationFactsAllLanguageRelationsMatchReference(t *testing.T) {
	repo := t.TempDir()
	for path, source := range relationFactsCorpus(t) {
		target := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	read := func(path string) (string, bool) {
		content, err := os.ReadFile(filepath.Join(repo, path))
		return string(content), err == nil
	}
	for _, profile := range []Profile{ProfileFull, ProfileFast, ProfileSyntaxOnly} {
		t.Run(string(profile), func(t *testing.T) {
			var reference []byte
			for _, shared := range []bool{false, true} {
				for _, workers := range []int{1, 8} {
					spec := resolveProfile(profile)
					if !shared {
						spec.shareRelationFacts = false
					}
					records := map[string][]SymbolRecord{}
					for _, symbol := range snapshot.Symbols {
						records[symbol.FilePath] = append(records[symbol.FilePath], symbol)
					}
					var output bytes.Buffer
					encoder := json.NewEncoder(&output)
					forEachRelation(t.Context(), "test", snapshot.Files, records, read, nil, spec, workers, nil, func(record RelationRecord) {
						if err := encoder.Encode(record); err != nil {
							t.Error(err)
						}
					}, func(failure PartialFailure) {
						if err := encoder.Encode(failure); err != nil {
							t.Error(err)
						}
					})
					if reference == nil {
						reference = append([]byte(nil), output.Bytes()...)
						if len(reference) == 0 {
							t.Fatal("empty relation stream")
						}
					} else if !bytes.Equal(reference, output.Bytes()) {
						t.Fatalf("shared=%v workers=%d: %s", shared, workers, firstParallelMatrixDifference(reference, output.Bytes()))
					}
				}
			}
		})
	}
}

func FuzzRelationFactsAllLanguages(f *testing.F) {
	for _, source := range []string{
		"const $x = fetch($p); send($x, $p); return $x;",
		"$x = new Client(); $x->save($p); return $x->field;",
		"return a() if ready else b()\nx = a() or b(); return x",
		"const x = ready ? a() : b(); return x; return a() ?? b()",
		"let {a, b} = p; const list = [a]; list.map(x => send(x)); send(list)",
		"await $f(x); $$a.$b(); f$g.h(); 0$w.foo(); a.$_b(0)",
		"fn run(p) { let x = load(); client.field = p; send(p); return x; }",
	} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 8192 {
			t.Skip()
		}
		legacy := symbolBody{text: source, stripped: source}
		shared := legacy
		shared.facts = collectRelationFacts(source)
		// These cover every distinct flow-syntax policy, plus the conservative
		// unknown-language policy, against the same byte-level matcher oracle.
		for _, language := range []string{"Go", "Python", "JavaScript", "PHP", "Lua", "Kotlin", "unknown"} {
			assertRelationBodyEqual(t, language, "fuzz", legacy, shared)
		}
	})
}

// Include construction and masking in the cost, rather than timing only warm
// lookups. These small fixtures supplement whole-repository cold profiles.
func BenchmarkRelationFactsLanguageCorpus(b *testing.B) {
	type sample struct {
		text   string
		params map[string]bool
	}
	corpus := map[string][]sample{}
	for path, source := range relationFactsCorpus(b) {
		entities, language, _ := (TreeSitterParser{}).ParseWithStatus(path, source)
		for _, symbol := range entitySymbols("test", path, language, entities) {
			if typeLikeKind(symbol.Kind) {
				continue
			}
			block := symbolBlock(source, symbol)
			if language == "JavaScript" || language == "TypeScript" || language == "Julia" {
				if exact, ok := exactSymbolSource(source, symbol); ok {
					block = exact
				}
			}
			corpus[language] = append(corpus[language], sample{block, symbolFlowParameterNames(symbol)})
		}
	}
	var languages []string
	for language := range corpus {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	for _, language := range languages {
		for _, shared := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/shared=%v", language, shared), func(b *testing.B) {
				spec := resolveProfile(ProfileFull)
				spec.shareRelationFacts = shared
				b.ReportAllocs()
				for b.Loop() {
					for _, sample := range corpus[language] {
						body := newRelationSymbolBody(sample.text, language, spec)
						receiverCalls(body)
						fieldAccesses(body)
						localVarTypes(body)
						chainedConstructorCalls(body)
						chainedConstructorReturnCalls(body)
						chainedConstructorDeepReturnCalls(body)
						returnedReceiverCalls(body)
						returnedReceiverChainCalls(body)
						returnedReceiverDeepChainCalls(body)
						returnFlowCalls(body, sample.params, flowSyntaxFor(language))
					}
				}
			})
		}
	}
}
