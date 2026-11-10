package sem

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func assertGoRelationScannersEqual(t *testing.T, path, source string) (used, fallback int) {
	t.Helper()
	entities, language, _ := (TreeSitterParser{}).ParseWithStatus(path, source)
	for _, symbol := range entitySymbols("test", path, language, entities) {
		if typeLikeKind(symbol.Kind) {
			continue
		}
		legacy := newSymbolBody(symbolBlock(source, symbol))
		optimized := legacy
		optimized.facts = collectRelationFacts(optimized.stripped)
		if strings.Contains(optimized.stripped, "$") {
			fallback++
		}
		used++
		assertRelationBodyEqual(t, "Go", fmt.Sprintf("%s:%d %s", path, symbol.StartLine, symbol.Name), legacy, optimized)
		assertRelationReturnFlowsEqual(t, language, path, legacy, optimized, symbolFlowParameterNames(symbol))
	}
	return used, fallback
}

func assertRelationBodyEqual(t *testing.T, language, label string, legacy, optimized symbolBody) {
	t.Helper()
	// Check captures and their original byte offsets as well as final edges.
	// Assignment ordering depends on offsets, including leading whitespace.
	patterns := []*regexp.Regexp{receiverCallRe, fieldAccessRe}
	for re := range relationAnchoredPatterns {
		patterns = append(patterns, re)
	}
	for re := range relationFlowPatterns {
		patterns = append(patterns, re)
	}
	for _, re := range patterns {
		want, got := re.FindAllStringSubmatchIndex(legacy.stripped, -1), optimized.relationMatchIndices(re)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s pattern %s\nbody: %q\nwant: %#v\n got: %#v", label, re, legacy.stripped, want, got)
		}
	}
	params := map[string]bool{}
	for _, name := range destructuredAssignmentVariablesRe.FindAllString(legacy.stripped, -1) {
		params[strings.TrimPrefix(name, "$")] = true
	}
	assertRelationReturnFlowsEqual(t, language, label, legacy, optimized, params)
	assertRelationReturnFlowsEqual(t, language, label, legacy, optimized, nil)
	factories := map[string]map[string][]string{}
	for _, match := range factoryReturnAssignRe.FindAllStringSubmatch(legacy.stripped, -1) {
		factories[match[2]] = map[string][]string{"fixture.go": {"Result"}}
	}
	checks := []struct {
		name string
		read func(symbolBody) any
	}{
		{"receivers", func(b symbolBody) any { return receiverCalls(b) }},
		{"constructors", func(b symbolBody) any { return chainedConstructorCalls(b) }},
		{"returned", func(b symbolBody) any { return returnedReceiverCalls(b) }},
		{"constructor-chain", func(b symbolBody) any { return chainedConstructorReturnCalls(b) }},
		{"constructor-deep-chain", func(b symbolBody) any { return chainedConstructorDeepReturnCalls(b) }},
		{"returned-chain", func(b symbolBody) any { return returnedReceiverChainCalls(b) }},
		{"returned-deep-chain", func(b symbolBody) any { return returnedReceiverDeepChainCalls(b) }},
		{"fields", func(b symbolBody) any { return fieldAccesses(b) }},
		{"locals", func(b symbolBody) any { return localVarTypes(b) }},
		{"factories", func(b symbolBody) any { return factoryReturnVarTypes(b, "fixture.go", factories) }},
	}
	for _, check := range checks {
		want, got := check.read(legacy), check.read(optimized)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s %s\nbody: %q\nwant: %#v\n got: %#v", label, check.name, legacy.stripped, want, got)
		}
	}
}

func assertRelationReturnFlowsEqual(t *testing.T, language, label string, legacy, optimized symbolBody, params map[string]bool) {
	t.Helper()
	want := returnFlowCalls(legacy, params, flowSyntaxFor(language))
	got := returnFlowCalls(optimized, params, flowSyntaxFor(language))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s return flows\nbody: %q\nparams: %v\nwant: %#v\n got: %#v", label, legacy.stripped, params, want, got)
	}
}

func TestGoRelationFactsScannersMatchReference(t *testing.T) {
	source := "package sample\n" + `
type Client struct { Field int }
func example(c *Client, out Client) {
 c.Save(1, nested(2))
 c.Save(3)
 c.Child.Save()
 a := Client{}
 b := &Client{}
 var d = Client(1)
 e := factory()
 x, y := factory()
 c.Field = 1
 c.Field += 1
 _ = &c.Field
 _ = c.Child.Field
 _ = factory().First().Second().Third()
 _ = Factory().First().Second().Third()
 _ = factory(nested()).First()
 _ = factory().First(nested()).Second()
 _ = c.Generic[int](1)
 _ = (*Client)(nil)
 _ = func() int { c.Save(9); return 1 }()
 // fake.Call() and fake.Field = Factory()
 _ = "fake.Call()"
 _ = 'x'
 _ = a; _ = b; _ = d; _ = e; _ = x; _ = y
}
func unicode(π *Client) { π.Save(); _ = π.Field }
func multiline(c *Client) { c.
 Save(); _ = c.
 Field }
func first() { factory().Save() }; func second() { factory().Save() }
func malformed( { broken.Call()
`
	// Compare both complete functions and whatever the parser recovers from errors.
	valid := source[:strings.Index(source, "func malformed")]
	used, _ := assertGoRelationScannersEqual(t, "fixture.go", valid)
	if used < 3 {
		t.Fatalf("optimized not exercised: %d symbols", used)
	}
	assertGoRelationScannersEqual(t, "broken.go", source)
}

// Differential oracle over the actual repository (including Go fixtures).
// Opt-in keeps ordinary unit tests independent of an enclosing git checkout.
func TestGoRelationFactsRepositoryDifferential(t *testing.T) {
	repo := os.Getenv("GO_RELATION_FACTS_REPO")
	if repo == "" {
		t.Skip("set GO_RELATION_FACTS_REPO for the full repository differential")
	}
	paths, err := exec.Command("git", "-C", repo, "ls-files", "-z", "--", "*.go").Output()
	if err != nil {
		t.Fatal(err)
	}
	files, used, fallback := 0, 0, 0
	for _, path := range strings.Split(string(paths), "\x00") {
		if path == "" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(repo, path))
		if err != nil {
			t.Fatal(err)
		}
		if looksMinified(string(content)) {
			continue
		}
		u, f := assertGoRelationScannersEqual(t, path, string(content))
		files++
		used += u
		fallback += f
	}
	if files == 0 || used == 0 {
		t.Fatal("differential corpus did not exercise optimized")
	}
	t.Logf("files=%d optimized_symbols=%d fallback_symbols=%d", files, used, fallback)
}

// Compare the complete ordered relation stream, including resolution, evidence
// and duplicate emissions, with independent input slices for each run.
func TestGoRelationFactsRelationsMatchReference(t *testing.T) {
	repo := writeProviderParallelFixture(t)
	writeParallelSharedStateFixture(t, repo)
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
					var out bytes.Buffer
					encoder := json.NewEncoder(&out)
					forEachRelation(t.Context(), "test", snapshot.Files, records, read, nil, spec, workers, nil,
						func(record RelationRecord) {
							if err := encoder.Encode(record); err != nil {
								t.Error(err)
							}
						}, func(failure PartialFailure) { t.Errorf("unexpected failure: %+v", failure) })
					if reference == nil {
						reference = append([]byte(nil), out.Bytes()...)
						if len(reference) == 0 {
							t.Fatal("empty relation oracle")
						}
					} else if !bytes.Equal(reference, out.Bytes()) {
						t.Fatalf("shared=%v workers=%d: %s", shared, workers, firstParallelMatrixDifference(reference, out.Bytes()))
					}
				}
			}
		})
	}
}

func TestGoRelationFactsActivationAndMasking(t *testing.T) {
	const source = "func f(p *Client) { p.Save(); send(p); return load() }"
	for _, profile := range []Profile{"", ProfileFull, ProfileFast, ProfileSyntaxOnly} {
		for _, language := range []string{"Go", "JavaScript", "Python", "Rust"} {
			body := newRelationSymbolBody(source, language, resolveProfile(profile))
			want := profile == "" || profile == ProfileFull
			if (body.facts != nil) != want {
				t.Fatalf("%s/%s: facts enabled=%v, want %v", language, profile, body.facts != nil, want)
			}
		}
	}
	body := newRelationSymbolBody(source, "Go", resolveProfile(ProfileFull))
	if body.masked(source).facts != body.facts {
		t.Fatal("unchanged mask discarded shared facts")
	}
	masked := body.masked("func f() { other.Call() }")
	if masked.facts != nil {
		t.Fatal("changed mask reused stale facts")
	}
	if !reflect.DeepEqual(receiverCalls(masked), receiverCalls(newSymbolBody(masked.text))) {
		t.Fatal("changed mask used stale matches")
	}
	// Malformed Go can contain dollar-prefixed names. The generic reference
	// matcher remains authoritative for this unsupported lexical case.
	fallback := newRelationSymbolBody("func f() { $x.Call(); return $load() }", "Go", resolveProfile(ProfileFull))
	if fallback.facts == nil {
		t.Fatal("dollar-prefixed snippet must share reference matches")
	}
	assertRelationBodyEqual(t, "Go", "fallback", newSymbolBody(fallback.text), fallback)
	// Unindexed patterns must still scan, even when facts exist for other patterns.
	re := regexp.MustCompile(`send\((p)\)`)
	if !reflect.DeepEqual(body.relationMatchIndices(re), re.FindAllStringSubmatchIndex(body.stripped, -1)) {
		t.Fatal("unindexed pattern lost matches")
	}
}

func FuzzGoRelationFactsMatchesReference(f *testing.F) {
	for _, seed := range []string{
		"a.b.c(); &a.b; a.b += 1; a.b\n(c)",
		"source :=      \nfactory().First().Second().Third()",
		"a := &Client{}; b = Factory(); c := new Client(); out Thing item",
		"if (f()).Method(); Factory(nested()).Method(); factory().First(nested()).Second()",
		"x.Foo<T>(); x.Foo < T > (v); éfoo.Bar(); 1foo.Bar(); f$oo.Bar()",
		"x := a; key: item = factory(); x = await f(); x = new Thing()",
		"a != nil && a.b.c == 1; a->b(); & \n a.Field",
		"out Type name; Nullable<T>? local; Return? v = f(); new Factory().Next()",
		"func f(p) { x := fetch(); send(p, x); return x }",
		"123 \n a := fetch(); return a.Field; return f() || fallback()",
		"var a, b = fetch(); (x, y) := load(); return x; return y",
		"if (p) { x = first() } else { x = second() }; return x",
		"alias := p\nobj := {}; obj.value = alias; send(obj); items := []; items.push(p); send(items)",
		"var {a, b} = p; var [x, y] = f(); items.map(v => send(v)); send({value: p}, [p])",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 8192 {
			t.Skip()
		}
		legacy := symbolBody{text: source, stripped: source}
		optimized := legacy
		optimized.facts = collectRelationFacts(source)
		assertRelationBodyEqual(t, "Go", "fuzz", legacy, optimized)
	})
}

// Include construction in the timing: measuring only cached lookups hides the
// cost paid for each new symbol, especially small functions.
func BenchmarkGoRelationScanners(b *testing.B) {
	for _, repeats := range []int{1, 16, 128} {
		source := "func process(p *Client) {\n" + strings.Repeat(`
 value := load(p)
 alias := p
 client := &Client{}
 client.Field = value
 client.Save(alias)
 send(p, value)
 factory().First().Second()
`, repeats) + "return value\n}"
		for _, shared := range []bool{false, true} {
			b.Run(fmt.Sprintf("blocks=%d/shared=%v", repeats, shared), func(b *testing.B) {
				spec := resolveProfile(ProfileFull)
				spec.shareRelationFacts = shared
				params := map[string]bool{"p": true}
				b.ReportAllocs()
				b.SetBytes(int64(len(source)))
				for b.Loop() {
					body := newRelationSymbolBody(source, "Go", spec)
					receiverCalls(body)
					fieldAccesses(body)
					localVarTypes(body)
					chainedConstructorCalls(body)
					chainedConstructorReturnCalls(body)
					chainedConstructorDeepReturnCalls(body)
					returnedReceiverCalls(body)
					returnedReceiverChainCalls(body)
					returnedReceiverDeepChainCalls(body)
					returnFlowCalls(body, params, flowSyntaxFor("Go"))
				}
			})
		}
	}
}
