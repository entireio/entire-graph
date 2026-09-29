package cli

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// exactNameLangCase is one declaration shape: its snippet (starting at line 1 of the snippet), the
// 1-based snippet line the index reports as the symbol's first line, the line that NAMES the
// symbol (the signature line an answer must show), and the doc comment lines above it, if any.
type exactNameLangCase struct {
	name, lang, file, symbol string
	snippet                  string
	symbolStart, signature   int
	doc                      []string
}

func (c exactNameLangCase) lines() []string {
	return strings.Split(strings.TrimSuffix(c.snippet, "\n"), "\n")
}

func (c exactNameLangCase) signatureLine() string { return c.lines()[c.signature-1] }

// exactNameLangCases covers six languages, with annotations, attributes and decorators on either
// side of the index's symbol start, multi-line signatures, and doc comments.
var exactNameLangCases = []exactNameLangCase{
	{name: "go-multiline", lang: "go", file: "internal/cfg/parse.go", symbol: "Parse", symbolStart: 3, signature: 3,
		doc:     []string{"// Parse reads a config.", "// It returns an error on bad input."},
		snippet: "// Parse reads a config.\n// It returns an error on bad input.\nfunc Parse(\n\tctx context.Context,\n\tinput string,\n) (*Config, error) {\n\tcfg := &Config{}\n\tif input == \"\" {\n\t\treturn nil, errEmpty\n\t}\n\treturn cfg, nil\n}\n"},
	{name: "java-annotations", lang: "java", file: "src/main/java/a/Svc.java", symbol: "toString", symbolStart: 4, signature: 6,
		doc:     []string{"    /**", "     * Renders it.", "     */"},
		snippet: "    /**\n     * Renders it.\n     */\n    @Override\n    @SuppressWarnings(\"unchecked\")\n    public String toString() {\n        return \"Svc(\" + value + \")\";\n    }\n"},
	{name: "java-multiline-annotated", lang: "java", file: "src/main/java/a/Repo.java", symbol: "findByOwner", symbolStart: 2, signature: 4,
		doc:     []string{"    /** Finds rows. */"},
		snippet: "    /** Finds rows. */\n    @Transactional(readOnly = true)\n    @Query(\"select r from Repo r where r.owner = :owner\")\n    public List<Repo> findByOwner(\n            @Param(\"owner\") String owner,\n            Pageable page) {\n        return em.find(owner, page);\n    }\n"},
	{name: "csharp-attributes", lang: "cs", file: "src/Cfg/Loader.cs", symbol: "Load", symbolStart: 2, signature: 4,
		doc:     []string{"    /// <summary>Loads the config.</summary>"},
		snippet: "    /// <summary>Loads the config.</summary>\n    [Obsolete(\"use LoadAsync\")]\n    [MethodImpl(MethodImplOptions.NoInlining)]\n    public static Config Load(\n        string path,\n        bool strict)\n    {\n        return Parse(File.ReadAllText(path), strict);\n    }\n"},
	{name: "python-decorated-start", lang: "py", file: "app/settings.py", symbol: "Settings", symbolStart: 2, signature: 4,
		doc:     []string{"# Settings for the app."},
		snippet: "# Settings for the app.\n@dataclass(frozen=True)\n@register(\"settings\")\nclass Settings(BaseModel):\n    \"\"\"Settings docstring.\"\"\"\n    name: str\n    debug: bool = False\n"},
	{name: "python-decorators-above", lang: "py", file: "app/views.py", symbol: "index", symbolStart: 3, signature: 3,
		doc:     []string{"@app.route(\"/\")", "@login_required"},
		snippet: "@app.route(\"/\")\n@login_required\ndef index(\n    request,\n    page=1,\n):\n    return render(request, \"index.html\")\n"},
	{name: "rust-attributes", lang: "rs", file: "src/builder.rs", symbol: "Builder", symbolStart: 2, signature: 4,
		doc:     []string{"/// Builds the thing."},
		snippet: "/// Builds the thing.\n#[derive(Debug, Clone)]\n#[serde(rename_all = \"camelCase\")]\npub struct Builder {\n    name: String,\n    size: usize,\n}\n"},
	{name: "rust-fn-multiline", lang: "rs", file: "src/run.rs", symbol: "run", symbolStart: 3, signature: 3,
		doc:     []string{"/// Runs it.", "#[inline]"},
		snippet: "/// Runs it.\n#[inline]\npub fn run<T: Send>(\n    input: T,\n) -> Result<(), Error> {\n    Ok(())\n}\n"},
	{name: "ts-decorated-class", lang: "ts", file: "src/handler.ts", symbol: "Handler", symbolStart: 2, signature: 3,
		doc:     []string{"/** Handles it. */"},
		snippet: "/** Handles it. */\n@Injectable()\nexport class Handler {\n  constructor(\n    private readonly svc: Service,\n  ) {}\n}\n"},
	{name: "ts-multiline-fn", lang: "ts", file: "src/user.ts", symbol: "fetchUser", symbolStart: 2, signature: 2,
		doc:     []string{"// fetchUser loads one user."},
		snippet: "// fetchUser loads one user.\nexport async function fetchUser(\n  id: string,\n  opts?: Options,\n): Promise<User> {\n  return api.get(id, opts);\n}\n"},
}

// exactNameLangResponse puts c's declaration at each of ranks (1-based) of the ten-row fixture.
func exactNameLangResponse(c exactNameLangCase, ranks ...int) sem.SearchResponse {
	response := exactNameFixture(c.symbol, ranks...)
	n := len(c.lines())
	for _, rank := range ranks {
		row := &response.Results[rank-1]
		base := 100 * rank
		row.FilePath = strings.TrimSuffix(c.file, filepath.Ext(c.file)) + fmt.Sprintf("%d", rank) + filepath.Ext(c.file)
		row.Snippet = c.snippet
		row.SnippetStartLine, row.StartLine = base, base
		row.SymbolStartLine = base + c.symbolStart - 1
		row.FocusLine = base + c.signature - 1
		row.SymbolEndLine, row.SnippetEndLine, row.EndLine = base+n-1, base+n-1, base+n-1
	}
	return response
}

// exactNameLineShown reports whether text is a body line of a block whose header names path.
func exactNameLineShown(payload, path, text string) bool {
	inBlock := false
	for _, line := range strings.Split(payload, "\n") {
		if m := exactNameBlockHeader.FindStringSubmatch(line); m != nil && strings.Contains(m[1], "/") {
			inBlock = m[1] == path
			continue
		}
		if inBlock && line == text {
			return true
		}
	}
	return false
}

// THE SIGNATURE IS NEVER LOST. For every language shape, one and two exact rows, and every budget
// from 1 byte to past the roomiest ordinary payload: a signature line the ordinary answer showed is
// shown by the new answer; whenever the exact answer is taken it shows every signature line and
// VERIFY; a doc comment or annotation never appears without its signature; and at a roomy cap the
// doc comment and annotations are shown. The sweep must see the exact answer taken, or it tests
// nothing.
func TestAgentSearchExactNameAnchorsOnTheSignatureAcrossLanguages(t *testing.T) {
	t.Parallel()
	for _, c := range exactNameLangCases {
		for _, ranks := range [][]int{{1}, {2, 6}} {
			response := exactNameLangResponse(c, ranks...)
			rows, _ := agentExactNameRows(orderAgentSearchResults(response.Results), response.Query)
			if len(rows) != len(ranks) {
				t.Fatalf("%s %v: %d exact rows selected", c.name, ranks, len(rows))
			}
			sig := c.signatureLine()
			roomy := len(renderAgentSearchForTest(t, response, 1<<20, false))
			fired := 0
			for budget := 1; budget <= roomy+64; budget += exactNameSweepScale() {
				before := renderAgentSearchForTest(t, response, budget, false)
				after := renderAgentSearchForTest(t, response, budget, true)
				if len(after) > budget {
					t.Fatalf("%s %v budget %d: payload is %d bytes", c.name, ranks, budget, len(after))
				}
				exact := after != before
				if exact {
					fired++
					if !strings.Contains(after, "VERIFY: go test") {
						t.Fatalf("%s %v budget %d: exact answer without VERIFY\n%s", c.name, ranks, budget, after)
					}
				}
				for _, row := range rows {
					was, is := exactNameLineShown(before, row.FilePath, sig), exactNameLineShown(after, row.FilePath, sig)
					if was && !is {
						t.Fatalf("%s %v budget %d: signature of rank %d shown before, not after\n--- before ---\n%s\n--- after ---\n%s",
							c.name, ranks, budget, row.Rank, before, after)
					}
					if exact && !is {
						t.Fatalf("%s %v budget %d: exact answer does not show the signature of rank %d\n%s", c.name, ranks, budget, row.Rank, after)
					}
					for _, lead := range append(append([]string{}, c.doc...), c.lines()[c.symbolStart-1:c.signature-1]...) {
						if exact && exactNameLineShown(after, row.FilePath, lead) && !is {
							t.Fatalf("%s %v budget %d: %q shown without the signature\n%s", c.name, ranks, budget, lead, after)
						}
					}
				}
			}
			if fired == 0 {
				t.Fatalf("%s %v: the exact answer was never taken; the sweep tests nothing", c.name, ranks)
			}
			roomyAfter := renderAgentSearchForTest(t, response, 8192, true)
			for _, row := range rows {
				for _, lead := range append(append([]string{}, c.doc...), c.lines()[c.symbolStart-1:c.signature]...) {
					if !exactNameLineShown(roomyAfter, row.FilePath, lead) {
						t.Errorf("%s %v: at 8192 bytes rank %d lacks %q\n%s", c.name, ranks, row.Rank, lead, roomyAfter)
					}
				}
			}
		}
	}
}

// A symbol whose snippet never names it within the bounded search (an index span that starts on
// something else entirely) is not anchored on a guess: the mode does not fire and the answer is
// byte-identical to the ordinary one at every budget.
func TestAgentSearchExactNameUnanchorableRowFallsBack(t *testing.T) {
	t.Parallel()
	c := exactNameLangCases[1]
	response := exactNameLangResponse(c, 1)
	row := &response.Results[0]
	// A literal count, not one derived from exactNameAnchorLines: the fixture must not grow with the
	// bound it tests.
	const annotations = 18
	var snippet strings.Builder
	for i := 0; i < annotations; i++ {
		snippet.WriteString("    @Annotation\n")
	}
	snippet.WriteString("    public String toString() {\n        return \"\";\n    }\n")
	row.Snippet = snippet.String()
	row.SymbolStartLine = row.SnippetStartLine
	row.SymbolEndLine = row.SnippetStartLine + annotations + 2
	if rows, _ := agentExactNameRows(response.Results, response.Query); rows != nil {
		t.Fatalf("a row with no named line inside the bounded search was anchored")
	}
	for budget := 1; budget <= 4096; budget += 3 * exactNameSweepScale() {
		if before, after := renderAgentSearchForTest(t, response, budget, false), renderAgentSearchForTest(t, response, budget, true); before != after {
			t.Fatalf("budget %d: output changed although the mode must not fire", budget)
		}
	}
}

// A doc comment longer than exactNameDocLines is left out whole, never cut to its last lines, and
// that costs the annotations between the symbol's start and its signature nothing: they belong to
// the declaration, not to the comment above it.
func TestAgentSearchExactNameLongDocLeavesAnnotations(t *testing.T) {
	t.Parallel()
	c := exactNameLangCases[1]
	var doc strings.Builder
	doc.WriteString("    /**\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&doc, "     * line %d of a long comment.\n", i)
	}
	doc.WriteString("     */\n")
	c.snippet = doc.String() + "    @Override\n    @SuppressWarnings(\"unchecked\")\n    public String toString() {\n        return \"\";\n    }\n"
	c.symbolStart, c.signature = 23, 25
	response := exactNameLangResponse(c, 1)
	got := renderAgentSearchForTest(t, response, 8192, true)
	file := response.Results[0].FilePath
	for _, want := range []string{"    @Override", "    @SuppressWarnings(\"unchecked\")", "    public String toString() {"} {
		if !exactNameLineShown(got, file, want) {
			t.Fatalf("roomy exact answer lacks %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "long comment") {
		t.Fatalf("a doc comment over the bound was shown (in part)\n%s", got)
	}
}

// A definition inside another symbol counts only in that file's own language: `class Animal {` in a
// Go test's string literal is fixture text, and showing its test as a definition of Animal cost
// ~1.3 KB per query on fx-graph (Animal, TokenService, Drawable) for no declaration.
func TestAgentExactNameDefinitionsAreLanguageAware(t *testing.T) {
	t.Parallel()
	cases := []struct {
		line, ext string
		want      bool
	}{
		{"\tresolveRef := func(ref string) string { return ref }", ".go", true},
		{"func (r *recv) resolveRef(ctx context.Context) error {", ".go", true},
		{"\twriteFile(t, repo, \"A.java\", `class resolveRef {", ".go", false},
		{"    class resolveRef:", ".py", true},
		{"    resolveRef := 1", ".py", false},
		{"  const resolveRef = (x: string) => x;", ".ts", true},
		{"    class resolveRef implements Named {}", ".java", true},
		{"    let resolveRef = |x| x;", ".rs", true},
		{"    def resolveRef", ".txt", false},
		{"\tresolveRef = other", ".go", false},
		{"\t// var resolveRef is gone", ".go", false},
	}
	for _, c := range cases {
		if got := exactNameLineDefines(c.line, "resolveRef", c.ext); got != c.want {
			t.Errorf("%s %q: defines = %v, want %v", c.ext, c.line, got, c.want)
		}
	}
	response := exactNameFixture("resolveMessageRef", 1)
	row := &response.Results[1]
	row.Snippet = "// Helper2 documents row 2.\n//\n//\nfunc (r *recv2) Helper2(t *testing.T) error {\n\twriteFile(t, repo, \"A.java\", `class resolveMessageRef {}`)\n\treturn nil\n}\n"
	if rows, _ := agentExactNameRows(response.Results, response.Query); len(rows) != 1 {
		t.Fatalf("a Go row whose string literal holds a Java class was kept as a definition (%d rows)", len(rows))
	}
}

// The name must occur as a whole identifier: an annotation that mentions `toStringHelper` does not
// name `toString`, and anchoring on it would print the annotation in place of the signature.
func TestAgentSearchExactNameMatchesWholeIdentifiers(t *testing.T) {
	t.Parallel()
	c := exactNameLangCases[1]
	response := exactNameLangResponse(c, 1)
	row := &response.Results[0]
	row.Snippet = "    @Delegate(to = \"toStringHelper\")\n    public String toString() {\n        return \"\";\n    }\n"
	row.SymbolStartLine, row.SymbolEndLine = row.SnippetStartLine, row.SnippetStartLine+3
	anchors, _ := agentExactNameAnchors(response.Results, response.Query)
	if len(anchors) != 1 || anchors[0].lines[anchors[0].named] != "    public String toString() {" {
		t.Fatalf("anchored on %q, want the signature", anchors[0].lines[anchors[0].named])
	}
}

// VERIFY IS NEVER TRADED FOR THE EXACT ANSWER. On every fixture layout and every budget: if the
// ordinary answer carried VERIFY, the new answer carries it. (The reviewer's sweep asks the same of
// three layouts; this one adds the one-row-per-language shapes and the ten-exact-row layout.)
func TestAgentSearchExactNameNeverDropsVerify(t *testing.T) {
	t.Parallel()
	responses := []sem.SearchResponse{
		exactNameFixture("resolveMessageRef", 1, 2, 3, 4, 5, 6, 7, 8, 9, 10),
		exactNameFixtureBody("resolveMessageRef", 40, 2, 6, 9),
	}
	for _, c := range exactNameLangCases {
		responses = append(responses, exactNameLangResponse(c, 1))
	}
	for i, response := range responses {
		roomy := len(renderAgentSearchForTest(t, response, 1<<20, false))
		for budget := 1; budget <= roomy+64; budget += exactNameSweepScale() {
			before := renderAgentSearchForTest(t, response, budget, false)
			after := renderAgentSearchForTest(t, response, budget, true)
			if strings.Contains(before, "VERIFY: go test") && !strings.Contains(after, "VERIFY: go test") {
				t.Fatalf("fixture %d budget %d: VERIFY shown before, not after\n--- before ---\n%s\n--- after ---\n%s", i, budget, before, after)
			}
		}
	}
}

// THE OMISSION LINE SAYS WHAT WAS LEFT OUT, AND ONLY THAT.
func TestAgentExactNameOmittedLineIsHonest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		omitted, shown, topK int
		capped               bool
		want                 string
	}{
		{9, 1, 10, false, "exact name: 9 other results omitted; search a phrase to see them\n"},
		{1, 3, 4, false, "exact name: 1 other result omitted; search a phrase to see them\n"},
		{9, 1, 10, true, "exact name: 9 other results omitted\n"},
		{0, 10, 10, false, "exact name: showing 10 exact definitions from the top 10; more may exist; raise --top-k\n"},
		{0, 1, 1, false, "exact name: showing 1 exact definition from the top 1; more may exist; raise --top-k\n"},
		{0, 10, 10, true, "exact name: showing 10 exact definitions from the top 10; more may exist\n"},
		// Nothing omitted and the ranking stopped short of --top-k: nothing to say.
		{0, 3, 10, false, ""},
		{0, 3, 10, true, ""},
		// Top-k unknown (a caller that does not pass it): never claim more may exist.
		{0, 10, 0, false, ""},
	}
	for _, c := range cases {
		got := string(agentExactNameOmittedLine(c.omitted, c.shown, c.topK, c.capped))
		if got != c.want {
			t.Errorf("omitted=%d shown=%d topK=%d capped=%v: %q, want %q", c.omitted, c.shown, c.topK, c.capped, got, c.want)
		}
		if got != "" && agentSearchLineIsLocator([]byte(strings.TrimSuffix(got, "\n"))) {
			t.Errorf("%q reads as a locator", got)
		}
	}
	// End to end: ten exact rows cut at --top-k 10 say more may exist; the same ranking at --top-k
	// 20 (it stopped short) says nothing; one exact row among ten counts the other nine.
	all := exactNameFixture("resolveMessageRef", 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	render := func(response sem.SearchResponse, options agentSearchRender) string {
		var out bytes.Buffer
		options.exactName = true
		if _, err := writeAgentSearchPayload(&out, response, 8192, options); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if got := render(all, agentSearchRender{topK: 10}); !strings.Contains(got, "exact name: showing 10 exact definitions from the top 10; more may exist; raise --top-k\n") {
		t.Errorf("top-k-full exact answer lacks the more-may-exist line\n%s", got)
	}
	if got := render(all, agentSearchRender{topK: 20}); strings.Contains(got, "exact name:") {
		t.Errorf("a ranking that stopped short of --top-k printed an omission line\n%s", got)
	}
	one := exactNameFixture("resolveMessageRef", 3)
	if got := render(one, agentSearchRender{topK: 10, sessionCapped: true}); !strings.HasSuffix(got, "exact name: 9 other results omitted\n") {
		t.Errorf("capped exact answer's omission line is wrong\n%s", got)
	}
}

// DOC COMMENTS COME BACK, BELOW THE SIGNATURE IN PRIORITY. The ordinary snippet showed the doc
// comment above a declaration; the exact answer shows it once its budget allows, and never in place
// of the signature (asserted densely by the language sweep above). Here: at a roomy cap the whole
// doc comment sits directly above the signature; at the smallest budget where the answer is taken
// the signature is shown without it.
func TestAgentSearchExactNameKeepsDocCommentBelowSignature(t *testing.T) {
	t.Parallel()
	response := exactNameFixture("resolveMessageRef", 1)
	roomy := renderAgentSearchForTest(t, response, 8192, true)
	want := "// resolveMessageRef documents row 1.\n// It exists so the fixture has a doc comment.\n//\nfunc (r *recv1) resolveMessageRef(ctx context.Context, input string) error {\n"
	if !strings.Contains(roomy, want) {
		t.Fatalf("roomy exact answer lacks the doc comment above the signature\n%s", roomy)
	}
	for budget := 1; budget <= 8192; budget++ {
		got := renderAgentSearchForTest(t, response, budget, true)
		if got == renderAgentSearchForTest(t, response, budget, false) {
			continue
		}
		if !strings.Contains(got, "func (r *recv1) resolveMessageRef(ctx context.Context, input string) error {\n") {
			t.Fatalf("budget %d: exact answer without the signature\n%s", budget, got)
		}
		if strings.Contains(got, "// resolveMessageRef documents row 1.") {
			t.Fatalf("budget %d: the first exact answer already carries the doc comment; the priority is not exercised\n%s", budget, got)
		}
		return
	}
	t.Fatalf("the exact answer was never taken")
}

// UNDER A SEARCH SESSION THE EXACT ANSWER IS NOT THE REPLAYED ONE, AND IT INVITES NOTHING. The first
// search of a capped task is an exact identifier: it is answered with the exact definition, counted,
// and not stored, and its omission line names no further search. The task's next search (a phrase)
// therefore runs for real and is the one stored; the one after it replays that phrase answer.
func TestSearchSessionDoesNotReplayTheExactAnswer(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Tests")
	git(t, repo, "config", "user.email", "tests@entire.local")
	write(t, repo, "target.py", "def exact_session_target(value):\n    return value + 1\n")
	write(t, repo, "caller.py", "from target import exact_session_target\n\n\ndef use_exact_session_target():\n    return exact_session_target(2)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "exact session fixture")
	session := filepath.Join(t.TempDir(), "session.json")

	search := func(query string) string {
		t.Helper()
		return exactNameSessionSearch(t, repo, session, query, "5")
	}
	first := search("exact_session_target")
	if !strings.Contains(first, "def exact_session_target(value):\n") || !strings.Contains(first, "exact name:") {
		t.Fatalf("first search did not take the exact answer:\n%s", first)
	}
	for _, invite := range []string{"search a phrase", "raise --top-k"} {
		if strings.Contains(first, invite) {
			t.Fatalf("capped exact answer invites another search (%q):\n%s", invite, first)
		}
	}
	state, err := (&searchSession{path: session, limit: 1}).load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Searches != 1 || state.Payload != "" || state.ReplaySchema != searchSessionReplaySchema {
		t.Fatalf("exact answer was stored for replay or not counted: %#v", state)
	}
	second := search("where the exact session target is called")
	if strings.Contains(second, "not run") || strings.Contains(second, "exact name:") {
		t.Fatalf("the phrase search after an exact answer did not run as an ordinary search:\n%s", second)
	}
	third := search("exact_session_target")
	header, replayed, ok := strings.Cut(third, "\n")
	if !ok || !strings.Contains(header, "not run") || replayed != second {
		t.Fatalf("the cap no longer holds after the phrase search:\n got %q\nwant replay of %q", third, second)
	}

	// A schema-2 state file (the build before this one) may hold an exact answer whose omission line
	// invites a phrase search. It must not be replayed: schema 3 is what says the slot never holds one.
	rewriteSearchSessionState(t, session, func(state map[string]any) {
		state["replay_schema"] = 2
		state["query"] = "exact_session_target"
		state["payload"] = "1. target.py:1 exact_session_target *\ndef exact_session_target(value):\nexact name: 1 other result omitted; search a phrase to see them\n"
	})
	fourth := search("who calls the exact session target")
	if strings.Contains(fourth, "not run") {
		t.Fatalf("a schema-2 session payload was replayed:\n%s", fourth)
	}
}

func exactNameSessionSearch(t *testing.T, repo, session, query, topK string) string {
	t.Helper()
	var out bytes.Buffer
	err := Run(t.Context(), Options{
		Version: "0.1.0",
		Env:     EntireEnv{RepoRoot: repo, SearchSession: session},
		Stdout:  &out,
	}, []string{"search", "--repo", repo, "--query", query, "--format", "agent", "--profile", "syntax-only", "--head", "--top-k", topK})
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	return out.String()
}

// The CLI passes --top-k to the renderer: a ranking cut at --top-k 1 whose one row is the exact
// definition says more may exist and names the knob (no session, so the knob may be named).
func TestSearchExactNameReportsTheTopKCut(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Tests")
	git(t, repo, "config", "user.email", "tests@entire.local")
	write(t, repo, "a.py", "def topk_cut_target(value):\n    return value\n")
	write(t, repo, "b.py", "class Other:\n    def topk_cut_target(self):\n        return 2\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "top-k fixture")
	got := exactNameSessionSearch(t, repo, "", "topk_cut_target", "1")
	if !strings.Contains(got, "exact name: showing 1 exact definition from the top 1; more may exist; raise --top-k\n") {
		t.Fatalf("the top-k cut is not reported:\n%s", got)
	}
}
