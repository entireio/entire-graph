package sem

import (
	"path/filepath"
	"strings"
)

// File-class prior
// ================
//
// `search` is a CODE search: the caller's next action is an edit to a source file.
// Some file classes are systematically over-scored by a body-text match yet are
// almost never the edit site:
//
//   - prose documentation restates an issue in the issue's own vocabulary, so it
//     wins BM25 against the code that expresses the same idea in identifiers;
//   - vendored / third-party trees are an upstream copy — editing them is wrong;
//   - generated artifacts are overwritten by the next codegen run;
//   - serialized data and configuration (JSON/YAML/TOML/XML/INI) DECLARE the names an
//     issue talks about — the package, the command, the option — so they match the
//     issue's vocabulary exactly while containing no behaviour to fix;
//   - examples/samples are real code, but secondary to the library they exercise.
//
// The correction is a MULTIPLICATIVE prior on the positive part of a candidate's
// score, not a filter: a non-source hit stays reachable (and still ranks first
// when nothing else matches at all), it just has to be clearly more relevant than
// the best source hit to outrank it.
//
// Defaults are principled, not tuned:
//
//	nonSource = 0.5   "must be twice as relevant as the best source hit to win"
//	secondary = 0.75  "a mild tilt away from example code toward the library"
//
// The prior is switched OFF for a class whenever the query itself asks for that
// class (`searchQuerySupplied`), which is how a genuine documentation task keeps
// full-strength documentation ranking.
const (
	searchNonSourceClassPrior = 0.5
	searchSecondaryClassPrior = 0.75

	// searchReferenceDeclPrior demotes a one-line declaration that names a type without initialising
	// it — `private final DruidLeaderClient druidLeaderClient;`. Such a line is a REFERENCE to the
	// concept, never a place a behavioural bug can live: it holds no value and runs no code.
	//
	// It is below the secondary prior because a fixture or a benchmark is real code a fix may edit,
	// whereas an uninitialised declaration can only change if its TYPE changes, which the query would
	// have had to ask about.
	//
	// Measured on apache/druid-14092: four such lines (two of them `private final DruidLeaderClient
	// druidLeaderClient;` in unrelated modules) took the two HIGHEST scores at 78.32, with the actual
	// fix site at rank 3. The agent read the ranked list as an edit set and patched 5 files across 4
	// Maven modules, paying a separate compile for each: 31 -> 48 turns, the single largest excess in
	// the cell. Haiku, given the same payload, edited one file.
	searchReferenceDeclPrior = 0.6
)

// searchFileClass is the content class a path belongs to, from the point of view
// of "would a coding agent edit this file to fix a defect?".
type searchFileClass string

const (
	searchFileClassSource      searchFileClass = "source"
	searchFileClassDoc         searchFileClass = "doc"
	searchFileClassVendored    searchFileClass = "vendored"
	searchFileClassGenerated   searchFileClass = "generated"
	searchFileClassData        searchFileClass = "data"
	searchFileClassExample     searchFileClass = "example"
	searchFileClassFixture     searchFileClass = "fixture"
	searchFileClassDeclaration searchFileClass = "declaration"
	searchFileClassHarness     searchFileClass = "harness"
)

// Serialized-data / configuration file types. These hold declarations, not behaviour:
// a package manifest names the very package an issue is about, a command schema names
// the very command, an option table names the very option — so on a body match they
// outrank the implementation that the reported behaviour actually lives in. Deliberately
// NOT here: file types that are executable program text even when used for configuration
// (`.js`, `.ts`, `.py`, `.rb`, `.gradle`, `Package.swift`, `*.cmake`) — those are code and
// a fix really can live in them.
var searchDataExtensions = []string{
	".json", ".jsonc", ".json5", ".yaml", ".yml", ".toml",
	".ini", ".cfg", ".conf", ".properties", ".plist", ".xml",
	".csv", ".tsv",
}

// Ambient DECLARATION MIRRORS: files that restate an API's shape with no executable statements —
// TypeScript `.d.ts` family and Python `.pyi` stubs. They are the same hazard the data class exists
// for, in program-text clothing: a declaration names the very option an issue is about, in one line,
// so on a body match it outranks the implementation whose behaviour the issue actually describes.
//
// Measured on axios#4731, an issue about `maxBodyLength` handling in the http adapter. Ranked:
//
//  1. index.d.ts:340   score 66.2  AxiosRequestConfig.maxRedirects  kind=field  `maxRedirects?: number;`
//  2. lib/adapters/http.js:449     score 61.3  dispatchHttpRequest   <- the gold fix site
//
// A one-line type field beat the fix site, and the damage compounds three ways: the head body is one
// line, so the whole payload came to 1,194 bytes carrying no code; the SAME-CONCEPT LITERAL block
// anchored on `maxRedirects` instead of `maxBodyLength`; and ranks 3 and 6 were declaration mirrors
// too. The agent then read the file and grepped twice for the literal the block should have swept —
// on that instance the graph arm spent 1.62x the no-tool baseline's tokens for the same 8 tool calls.
//
// Demoted at the SECONDARY strength, not the non-source strength, for the same reason fixtures are:
// a real fix often updates the declaration alongside the implementation, so these must sit below the
// code without being pushed out of the ranking. Asking about types switches the prior off entirely.
var searchDeclarationSuffixes = []string{".d.ts", ".d.tsx", ".d.cts", ".d.mts", ".pyi"}

// searchDeclarationIntentTerms turn the prior off: when the query is about the declared surface
// itself, the mirror IS the fix site.
var searchDeclarationIntentTerms = []string{
	"type", "types", "typing", "typings", "typed", "declaration", "declarations",
	"d.ts", "pyi", "stub", "stubs", "typescript type", "type definition", "type definitions",
}

func searchDeclarationClassPath(lower string) bool {
	for _, suffix := range searchDeclarationSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// Path segments that mark a documentation tree. Matched as whole path SEGMENTS
// (so `versioned_docs/` and `website/` are caught while `my-docs-parser.go` is
// not), which is strictly more precise than substring matching.
var searchDocDirSegments = map[string]bool{
	"doc": true, "docs": true, "documentation": true,
	"man": true, "manual": true, "manuals": true,
	"website": true, "websites": true, "wiki": true, "handbook": true,
	"guide": true, "guides": true, "tutorial": true, "tutorials": true,
	"versioned_docs": true, "versioned-docs": true, "versioned_sidebars": true,
	"changelog": true, "changelogs": true, "javadoc": true, "godoc": true,
}

// Prose / documentation file types. These describe a feature in the same words
// the issue uses, so on a body match they outrank the code that implements it.
// Note: .yml/.yaml are deliberately absent — they are usually config/CI.
var searchDocExtensions = []string{
	".md", ".markdown", ".mdx", ".mdoc", ".rst", ".adoc", ".asciidoc",
	".txt", ".text", ".org", ".pod", ".rdoc", ".textile", ".creole", ".wiki",
}

// Basenames that are repository prose regardless of where they live.
var searchDocBasePrefixes = []string{
	"readme", "changelog", "changes", "history", "news", "contributing",
	"authors", "maintainers", "code_of_conduct", "code-of-conduct",
	"license", "licence", "copying", "notice", "governance", "upgrading",
}

// Path segments that mark a vendored / third-party tree.
var searchVendoredDirSegments = map[string]bool{
	"vendor": true, "vendors": true, "_vendor": true,
	"third_party": true, "third-party": true, "thirdparty": true,
	"dependencies": true, "node_modules": true, "bower_components": true,
	"jspm_packages": true, "site-packages": true, "dist-packages": true,
	"godeps": true, ".venv": true, "venv": true, "virtualenv": true,
	"eggs": true, ".eggs": true, "external": true, "externals": true,
}

// Path segments that mark example / sample code.
// searchHarnessDirSegments name trees that EXERCISE the code rather than implement it. A fuzz target
// or a benchmark mentions the API under test as densely as the implementation does, so it competes on
// pure lexical grounds and sometimes wins — measured: a query about signal handling in uutils/coreutils
// returned `fuzz/fuzz_targets/fuzz_env.rs` at ranks 1 AND 2, which then produced the derived command
// `cd fuzz && cargo test` pointing at a separate workspace, and the session ran 40 -> 60 turns.
//
// They are demoted, not filtered: a fix legitimately updates a fuzz target or a benchmark alongside
// the code, so they must rank below the implementation without leaving the ranking.
var searchHarnessDirSegments = map[string]bool{
	"fuzz": true, "fuzzing": true, "fuzz_targets": true, "fuzz-targets": true,
	"bench": true, "benches": true, "benchmark": true, "benchmarks": true,
}

var searchExampleDirSegments = map[string]bool{
	"example": true, "examples": true, "sample": true, "samples": true,
	"demo": true, "demos": true, "cookbook": true, "cookbooks": true,
	"recipes": true, "playground": true,
}

// Snapshot / golden-output file types. These are machine-written RECORDINGS of expected
// output, so they quote the reported symptom — error codes, messages, rule identifiers —
// verbatim, which is exactly what a query built from an issue matches on. That makes them
// outrank the implementation whose behaviour the issue is actually about: measured on
// astral-sh/ruff, a query naming rule code SIM201 ranked two
// `snapshots/..._SIM201_SIM201.py.snap` files above the rule source `ast_unary_op.rs`,
// which contains the same literal and is the file the fix edits.
//
// Scope, measured — this is a small win, not a fix for that ruff case. On 52 disjoint
// SWE-bench Multilingual instances with agent-realistic queries at top-k 20, gold-file
// recall went 75.0% -> 76.9% (+1 instance, no regressions), and mean rank of the gold file
// was unchanged within noise (4.59 -> 4.74; improved on 11, worsened on 12, equal on 16).
// The ruff rule-code queries it was built for did NOT recover: Rust stayed at 33%.
// Demoting a snapshot only reorders candidates, and in those cases the gold file was
// missing from the ranking for an unrelated reason (an exact rare-literal match in real
// source scoring below generic word matches elsewhere). That scoring gap is the open
// issue; this class only stops recorded expectations from occupying ranked slots.
var searchFixtureExtensions = []string{
	".snap", ".snapshot", ".golden", ".ambr", ".approved", ".expected", ".received",
}

// Path segments that mark a test-fixture / golden-output tree.
var searchFixtureDirSegments = map[string]bool{
	"fixture": true, "fixtures": true, "__fixtures__": true,
	"snapshot": true, "snapshots": true, "__snapshots__": true,
	"testdata": true, "test-data": true, "test_data": true,
	"test-fixtures": true, "test_fixtures": true,
	"golden": true, "goldens": true, "baseline": true, "baselines": true,
	"expected": true, "__expected__": true,
}

// Basenames that are machine-written lock/manifest artifacts.
var searchGeneratedBases = map[string]bool{
	"package-lock.json": true, "npm-shrinkwrap.json": true, "yarn.lock": true,
	"pnpm-lock.yaml": true, "cargo.lock": true, "composer.lock": true,
	"gemfile.lock": true, "poetry.lock": true, "pdm.lock": true,
	"go.sum": true, "flake.lock": true, "packages.lock.json": true,
}

// Query terms that switch the prior off for each class: if the caller asked for
// documentation, documentation ranks at full strength.
var (
	searchDocIntentTerms = []string{
		"doc", "docs", "documentation", "readme", "readmes", "guide", "guides",
		"tutorial", "tutorials", "manual", "manpage", "changelog", "website",
		"handbook", "wiki", "prose", "comment", "comments", "docstring", "docstrings",
	}
	searchVendoredIntentTerms = []string{
		"vendor", "vendored", "vendoring", "dependency", "dependencies",
		"third", "party", "upstream", "node_modules", "lockfile", "lock",
	}
	searchGeneratedIntentTerms = []string{
		"generated", "generator", "generators", "codegen", "codegens",
		"build", "builds", "dist", "bundle", "bundles",
		"amalgamation", "amalgamated", "single", "onefile", "lockfile", "lock",
	}
	// searchHarnessIntentTerms switch the harness prior off, exactly as every other class prior can be
	// switched off by asking for that class.
	searchHarnessIntentTerms = []string{
		"fuzz", "fuzzer", "fuzzing", "bench", "benches", "benchmark", "benchmarks",
		"harness", "profiling", "profiler",
	}

	searchExampleIntentTerms = []string{
		"example", "examples", "sample", "samples", "demo", "demos",
		"snippet", "snippets", "cookbook", "recipe", "recipes", "playground",
	}
	// Deliberately WITHOUT "expected": an intent term switches the whole class prior off, and
	// "expected" is the most common word in a bug report ("expected X, got Y"). Including it
	// handed snapshot and golden files full ranking strength on exactly the ordinary defect
	// queries the fixture prior exists to correct, so they could again outrank the implementation.
	// The words that remain all NAME the artifact rather than describe a symptom. The OTHER sense
	// of the word — a caller asking to update an expected-output artifact — is read as a phrase by
	// searchFixtureArtifactRequest instead, so neither intent is answered with the other's ranking.
	searchFixtureIntentTerms = []string{
		"fixture", "fixtures", "snapshot", "snapshots", "golden", "goldens",
		"testdata", "baseline", "baselines", "insta", "approvals",
	}
	// Words that mean "I am asking about a config/data file". Kept narrow on purpose:
	// terms like "package", "version" or "data" appear in almost every bug report
	// (issue templates ask for a version) and would switch the prior off universally.
	searchDataIntentTerms = []string{
		"config", "configs", "configuration", "configure", "setting", "settings",
		"json", "yaml", "yml", "toml", "xml", "ini", "manifest", "manifests",
		"schema", "schemas", "metadata", "dependency", "dependencies", "lockfile",
	}
)

// Nouns that make "expected" NAME an artifact rather than describe a symptom. `expected output`
// and `expected results` are files on disk; `expected a 200 response` is a sentence about a defect.
var searchExpectedArtifactNouns = map[string]bool{
	"output": true, "outputs": true, "result": true, "results": true,
	"file": true, "files": true, "fixture": true, "fixtures": true,
	"snapshot": true, "snapshots": true, "golden": true, "goldens": true,
	"baseline": true, "baselines": true, "data": true, "json": true, "yaml": true,
	"xml": true, "text": true, "tree": true, "dump": true, "dumps": true,
	"log": true, "logs": true, "transcript": true, "transcripts": true,
}

// Verbs that make a query a REQUEST TO EDIT the artifact it names. Deliberately without "fix" or
// "change": both appear in ordinary defect reports, and it is the request — not the mention — that
// earns the fixture class its full ranking strength.
var searchArtifactEditVerbs = map[string]bool{
	"update": true, "updates": true, "updated": true, "updating": true,
	"regenerate": true, "regenerates": true, "regenerated": true, "regenerating": true,
	"refresh": true, "refreshes": true, "refreshed": true, "refreshing": true,
	"rewrite": true, "rewrites": true, "rewrote": true, "rewriting": true,
	"record": true, "records": true, "recorded": true, "rerecord": true, "rerecording": true,
	"bless": true, "blessed": true, "accept": true, "approve": true, "approved": true,
	"sync": true, "resync": true, "replace": true, "replaced": true, "edit": true, "edited": true,
}

// searchFixtureArtifactRequest reports whether the query asks to EDIT an expected-output artifact,
// which is the sense of "expected" the fixture intent terms deliberately cannot carry.
//
// It is a PHRASE, not a word: "expected" must directly modify an artifact noun ("update the
// expected output for the parser"), and the query must also carry a verb that edits artifacts.
// Both halves are required because either alone still matches the sentence the word was dropped
// for: "expected output 42 but the parser returns 43" names the artifact and asks for nothing, and
// "update the parser, it expected a flush" asks for an edit to something else entirely.
func searchFixtureArtifactRequest(q searchQuery) bool {
	written := q.wordSequence
	if len(written) == 0 {
		written = searchQueryWordSequence(q.rawLower)
	}
	named := false
	for index, word := range written {
		if word == "expected" && index+1 < len(written) && searchExpectedArtifactNouns[written[index+1]] {
			named = true
			break
		}
	}
	if !named {
		return false
	}
	for _, word := range written {
		if searchArtifactEditVerbs[word] {
			return true
		}
	}
	return false
}

// searchFixtureClassPath reports whether a path is a snapshot / golden-output artifact,
// either by extension (.snap, .ambr, ...) or by living in a fixture/snapshot/testdata tree.
func searchFixtureClassPath(lower string, dirs []string) bool {
	for _, ext := range searchFixtureExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	for _, segment := range dirs {
		if searchFixtureDirSegments[segment] {
			return true
		}
	}
	return false
}

// classifySearchFile maps a repository-relative path to its content class.
// Precedence runs from "definitely not editable by this task" downwards so a
// vendored markdown file classifies as vendored rather than doc (the prior is
// the same for both; the label is what changes).
func classifySearchFile(filePath string) searchFileClass {
	lower := strings.ToLower(filepath.ToSlash(filePath))
	segments := searchPathSegments(lower)
	base := filepath.Base(lower)
	dirs := segments
	if len(dirs) > 0 {
		dirs = dirs[:len(dirs)-1]
	}
	for _, segment := range dirs {
		if searchVendoredDirSegments[segment] {
			return searchFileClassVendored
		}
	}
	if searchGeneratedBases[base] || searchGeneratedArtifactPath("/"+lower) {
		return searchFileClassGenerated
	}
	if searchDocumentationClassPath(lower, base, dirs) {
		return searchFileClassDoc
	}
	if searchFixtureClassPath(lower, dirs) {
		return searchFileClassFixture
	}
	if searchDeclarationClassPath(lower) {
		return searchFileClassDeclaration
	}
	for _, ext := range searchDataExtensions {
		if strings.HasSuffix(lower, ext) {
			return searchFileClassData
		}
	}
	for _, segment := range dirs {
		if searchHarnessDirSegments[segment] {
			return searchFileClassHarness
		}
	}
	for _, segment := range dirs {
		if searchExampleDirSegments[segment] {
			return searchFileClassExample
		}
	}
	return searchFileClassSource
}

// searchDocumentationClassPath reports whether a path is prose documentation,
// either by file type (anywhere in the tree) or by living in a documentation
// tree. `lower` is the slash-normalized lowercase path, `base` its basename and
// `dirs` its directory segments.
func searchDocumentationClassPath(lower, base string, dirs []string) bool {
	for _, segment := range dirs {
		if searchDocDirSegments[segment] {
			return true
		}
	}
	for _, prefix := range searchDocBasePrefixes {
		if !strings.HasPrefix(base, prefix) {
			continue
		}
		// A PREFIX, NOT A SUBSTRING OF A FILETYPE. `license_check.go`, `history_store.py` and
		// `readme_parser.ts` all begin with a prose prefix and are ordinary executable sources.
		// The raw prefix match halved their score AND — because NonProgramTextPath consumes this
		// same classification — declared them incapable of holding a relation, so a real fix site
		// was pushed out of the ranking and out of call-chain reasoning at once.
		//
		// Nothing prose is lost by the narrowing: every prose FILETYPE is caught by
		// searchDocExtensions (and the roff/manpage rules) below, so this rule only has to cover
		// the extensionless repository files it was written for -- README, LICENSE,
		// LICENSE-APACHE, COPYING, AUTHORS, NEWS, CHANGES.
		if _, known := languageForPath(base); known {
			continue
		}
		return true
	}
	for _, ext := range searchDocExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	// man pages: foo.1 .. foo.9, plus pre-rendered roff variants.
	if strings.HasSuffix(lower, ".prebuilt") || strings.HasSuffix(lower, ".roff") || strings.HasSuffix(lower, ".man") {
		return true
	}
	if n := len(base); n >= 2 && base[n-2] == '.' && base[n-1] >= '1' && base[n-1] <= '9' {
		return true
	}
	return false
}

func searchPathSegments(lower string) []string {
	parts := strings.Split(strings.Trim(lower, "/"), "/")
	out := parts[:0]
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		out = append(out, part)
	}
	return out
}

// searchFileClassPrior returns the multiplicative relevance prior for a path,
// in (0, 1]. 1 means "no correction" (source files, and any class the query
// explicitly asked for).
func searchFileClassPrior(q searchQuery, filePath string) float64 {
	switch classifySearchFile(filePath) {
	case searchFileClassDoc:
		if searchQuerySupplied(q, searchDocIntentTerms...) {
			return 1
		}
		return searchNonSourceClassPrior
	case searchFileClassVendored:
		if searchQuerySupplied(q, searchVendoredIntentTerms...) {
			return 1
		}
		return searchNonSourceClassPrior
	case searchFileClassGenerated:
		if searchQuerySupplied(q, searchGeneratedIntentTerms...) {
			return 1
		}
		return searchNonSourceClassPrior
	case searchFileClassData:
		if searchQuerySupplied(q, searchDataIntentTerms...) {
			return 1
		}
		return searchNonSourceClassPrior
	case searchFileClassExample:
		if searchQuerySupplied(q, searchExampleIntentTerms...) {
			return 1
		}
		return searchSecondaryClassPrior
	case searchFileClassDeclaration:
		if searchQuerySupplied(q, searchDeclarationIntentTerms...) {
			return 1
		}
		// Same strength as a fixture, and for the same reason: a fix often updates the declared
		// surface alongside the implementation, so the mirror must rank below the code without
		// leaving the ranking.
		return searchSecondaryClassPrior
	case searchFileClassHarness:
		if searchQuerySupplied(q, searchHarnessIntentTerms...) {
			return 1
		}
		// Same strength as a fixture: a fuzz target or benchmark is real, compiled code that a fix may
		// well touch, so it is demoted below the implementation rather than pushed out of the ranking.
		return searchSecondaryClassPrior
	case searchFileClassFixture:
		if searchQuerySupplied(q, searchFixtureIntentTerms...) || searchFixtureArtifactRequest(q) {
			return 1
		}
		// Milder than the non-source prior: a fix legitimately updates a fixture
		// alongside the code, so these must be demoted below the implementation
		// without being pushed out of the ranking altogether.
		return searchSecondaryClassPrior
	}
	return 1
}

// applySearchFileClassPrior scales the positive part of every candidate score by
// its file-class prior. Negative scores are left alone: they are already the
// result of an explicit demotion and multiplying them would UNDO it.
// searchReferenceDeclaration reports whether a hit is a one-line declaration that only names a type.
// A declaration WITH an initialiser is excluded: `static final int MAX = 5;` is a constant whose value
// is exactly the kind of thing a fix changes, and multi-line declarations are excluded because a
// constant LIST — lombok's NONNULL_ANNOTATIONS, say — is a genuine fix site.
func searchReferenceDeclaration(result SearchResult) bool {
	body := result.Snippet
	if index := strings.Index(body, "//"); index >= 0 {
		body = body[:index]
	}
	switch result.Kind {
	case "field", "variable", "property", "member", "constant":
		start, end := result.SymbolStartLine, result.SymbolEndLine
		if start == 0 && end == 0 {
			start, end = result.StartLine, result.EndLine
		}
		if end > start {
			return false
		}
		return !strings.ContainsRune(body, '=')
	case "method", "function":
		// A callable with no body is a CONTRACT, not an implementation: an interface or abstract
		// method, or a header prototype. It cannot hold a behavioural bug, and ranking it displaces the
		// implementation that can.
		//
		// Measured on laravel/framework-46234 (+308.7% usd on Sonnet, 20.5% of that cell's excess): the
		// payload carried `Contracts/Routing/UrlGenerator.php:20 UrlGenerator.previous` — one line, the
		// interface declaration — at rank 3 and `Contracts/Session/Session.php previousUrl` at rank 2,
		// while the file the agent actually had to edit, `Routing/UrlGenerator.php::previous`, never
		// appeared at all. redis-10068 shows the C form: `src/server.h:3328`, a bare prototype line.
		//
		// Detection needs no language table: a definition that opens no block has no body to contain a
		// bug. Anything with a brace, or a Python/Ruby style body under a colon, is a real definition
		// and keeps full weight.
		// NARROWED after this rule caused a regression. The first version accepted any single-line
		// method snippet, or one ending in `)`, as a declaration. Both are true of a real method whose
		// snippet was rendered short or cut mid-signature, so it demoted implementations: on
		// apache/lucene-12022 the correct geo hits (Line2D, SpatialQuery, Circle2D) were pushed out and
		// the payload came back holding store/index classes instead, on BOTH models.
		//
		// A declaration is now only recognised on positive evidence: the symbol occupies at most two
		// lines, the snippet shows the WHOLE symbol rather than a window into it, and it opens no block.
		// Anything truncated is treated as an implementation, because a truncated body is still a body.
		start, end := result.SymbolStartLine, result.SymbolEndLine
		if start == 0 || end == 0 || end-start > 1 {
			return false
		}
		if result.SnippetStartLine != 0 && result.SnippetEndLine != 0 &&
			(result.SnippetStartLine > start || result.SnippetEndLine < end) {
			return false
		}
		if strings.ContainsRune(body, '{') {
			return false
		}
		// An EXPRESSION BODY opens no block and is still an implementation. Kotlin and Scala write
		// `fun f() = expr`, C# and Java write `T F() => expr`, and both are exactly the executable
		// code a behavioural fix edits. Multiplying them by the reference-declaration prior can push
		// the real fix site out of the result set, which is the failure this rule exists to prevent
		// -- in the opposite direction.
		if searchExpressionBodiedCallable(body) {
			return false
		}
		return strings.TrimSpace(body) != ""
	}
	return false
}

// searchExpressionBodiedCallable reports whether a braceless callable carries an EXPRESSION body.
//
// `=>` is unambiguous. A bare `=` is the Kotlin/Scala form, and it is read as an implementation
// unless what follows is one of the three C++ special-member forms that assign no expression at
// all: `= 0` (pure virtual), `= delete` and `= default`. Those remain declarations.
//
// The bias is deliberate. Treating an implementation as a declaration DEMOTES a fix site out of
// the ranking; treating a declaration as an implementation merely leaves it at full weight, where
// it competes on its score like anything else.
func searchExpressionBodiedCallable(body string) bool {
	// What the scan looks for is an ASSIGNMENT IN THE CALLABLE'S TAIL: an `=` or `=>` that stands
	// OUTSIDE every parenthesised group and after the callable's parentheses have closed. Both
	// halves are load-bearing, and each excludes a different `=` that is not a body.
	//
	// DEPTH excludes every bracketed `=`. A DEFAULT ARGUMENT is one — `public function
	// previous($fallback = false);` is a PHP interface method and nothing else — and so is an
	// ANNOTATION or ATTRIBUTE argument, which is why the FIRST parenthesised group cannot simply be
	// skipped as the parameter list: in `@JvmName("f") fun f(x: Int = 3);` the first group belongs
	// to the annotation, and skipping it left the default argument in the scanned tail and read the
	// declaration as an expression body. Reading depth instead does not care which group is which.
	//
	// POSITION excludes an `=` written before any parentheses at all, which is where a GENERIC TYPE
	// PARAMETER writes its default: `template <class T = int> void reset();` and TypeScript's
	// `function f<T = string>(x: T): void;` are declarations. An expression body always follows the
	// parameter list, so requiring one closed group costs it nothing — `const f = (x: number) => x`
	// is still caught by the `=>` after that group. A callable written without parentheses at all
	// (Scala's `def f: Int = 42`) has no group to wait for and is scanned whole.
	depth := 0
	closedGroup := !strings.ContainsRune(body, '(')
	for index := 0; index < len(body); index++ {
		switch body[index] {
		case '(':
			depth++
			continue
		case ')':
			if depth > 0 {
				depth--
				closedGroup = closedGroup || depth == 0
			}
			continue
		case '=':
		default:
			continue
		}
		if depth > 0 || !closedGroup {
			continue
		}
		// Skip the comparison operators: `==`, `!=`, `<=`, `>=`.
		if index > 0 && strings.IndexByte("=!<>", body[index-1]) >= 0 {
			continue
		}
		if index+1 < len(body) && body[index+1] == '>' {
			return true
		}
		if index+1 < len(body) && body[index+1] == '=' {
			index++
			continue
		}
		rest := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(body[index+1:]), ";"))
		switch rest {
		case "", "0", "delete", "default":
			return false
		}
		return true
	}
	return false
}

func applySearchFileClassPrior(candidates []searchCandidate, q searchQuery) {
	priors := make(map[string]float64, len(candidates))
	for index := range candidates {
		candidate := &candidates[index]
		if candidate.score <= 0 {
			continue
		}
		path := candidate.result.FilePath
		prior, cached := priors[path]
		if !cached {
			prior = searchFileClassPrior(q, path)
			priors[path] = prior
		}
		if prior < 1 {
			candidate.score *= prior
			candidate.result.Signals = appendUnique(candidate.result.Signals, string(classifySearchFile(path))+"-prior")
		}
		if searchReferenceDeclaration(candidate.result) {
			candidate.score *= searchReferenceDeclPrior
			candidate.result.Signals = appendUnique(candidate.result.Signals, "reference-decl-prior")
		}
	}
}
