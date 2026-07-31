package sem

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path"
	"strings"
)

// The verify command
// ==================
//
// Measured across real agent sessions, 3.79 turns per session go on test-and-build — and a large
// part of that is not running the tests, it is finding out HOW to run them: the wrong package
// selector, the wrong filter syntax, the whole suite when one module was meant, a command that has
// to run from a subdirectory. An agent asked for this directly: "bundle test artifacts with the
// located symbol (fixture path, snapshot path, the exact `cargo test` filter) — saves 1.5 turns. On
// a snapshot-test repo this is the difference between verifying and hoping."
//
// So this block emits ONE line: the narrowest test invocation for the file the top hit is in, plus
// the file it targets and the evidence it was derived from.
//
// Every input is repository evidence — a build manifest that exists, and the covering test the
// payload already found. Nothing is guessed, and the block's most important behavior is SILENCE: a
// wrong command costs strictly more than no command, because the agent runs it, reads a failure
// that is about the invocation rather than the code, and then does the discovery anyway. Whenever
// the manifest does not license a narrow command, nothing is emitted. Ant, plain CMake and every
// unrecognized build system therefore get nothing, on purpose.
//
// The command is always runnable from the repository root: when the manifest that licenses it is not
// at the root, the emitted line carries the `cd` that makes it true.
const (
	// searchVerifyCommandMaxBytes caps the block on the larger of its two wire forms. One command
	// line, one target line, one short derivation.
	searchVerifyCommandMaxBytes = 320

	// searchVerifyMaxDepth bounds the ancestor walk for a build manifest. Deeper than this and the
	// "nearest module" is not a module, it is a directory.
	searchVerifyMaxDepth = 8

	// searchVerifyMaxReads bounds manifest and mirror-test probes. Most of them MISS, and a miss is a
	// path lookup rather than a file, which is why the bound can be generous: it buys a build system
	// identified exactly and a test file that provably exists.
	searchVerifyMaxReads = 256
)

// SearchVerifyCommand is the narrowest verification command for the top hit's file.
type SearchVerifyCommand struct {
	// Command is runnable as written, from the repository root.
	Command string `json:"command"`
	// Targets is the test file the command runs, or the package when no covering test was found.
	Targets string `json:"targets"`
	// DerivedFrom names the repository evidence behind the command, so a reader can judge it
	// instead of trusting it.
	DerivedFrom string `json:"derived_from"`
	// RunnerMissing reports that the executable this command invokes could not be resolved here. The
	// command is still emitted — see the note in buildSearchVerifyCommand — but a caller that would
	// otherwise go hunting for a toolchain is told, in the block itself, not to.
	RunnerMissing bool `json:"runner_missing,omitempty"`
}

// searchVerifyEvidence is the bounded view of the repository the derivation is allowed to consult.
// It caches misses as well as hits: a manifest that is not there is probed once per path.
type searchVerifyEvidence struct {
	read  contentReader
	cache map[string]string
	miss  map[string]bool
	reads int
	// lookPath resolves an executable on PATH. nil means exec.LookPath. Injected so tests state which
	// runners a machine has rather than inheriting whatever the test box happens to install.
	lookPath func(string) (string, error)
}

// file returns a repository file's content, or false. It never reads more than
// searchVerifyMaxReads distinct paths, so a deep tree cannot turn manifest discovery into IO.
func (evidence *searchVerifyEvidence) file(filePath string) (string, bool) {
	if evidence == nil || evidence.read == nil || filePath == "" {
		return "", false
	}
	if evidence.cache == nil {
		evidence.cache = map[string]string{}
		evidence.miss = map[string]bool{}
	}
	if content, cached := evidence.cache[filePath]; cached {
		return content, true
	}
	if evidence.miss[filePath] {
		return "", false
	}
	if evidence.reads >= searchVerifyMaxReads {
		return "", false
	}
	evidence.reads++
	content, ok := evidence.read(filePath)
	if !ok {
		evidence.miss[filePath] = true
		return "", false
	}
	evidence.cache[filePath] = content
	return content, true
}

func (evidence *searchVerifyEvidence) exists(filePath string) bool {
	_, ok := evidence.file(filePath)
	return ok
}

// searchVerifySubject is what the derivation knows about the edit it has to verify.
type searchVerifySubject struct {
	// sourcePath is the file the top hit is in: the file the patch lands in.
	sourcePath string
	// testPath and testName come from the covering test, and are empty when the payload found none.
	testPath string
	testName string
	// testEvidence names where testPath came from, so the derivation the block reports is the truth
	// rather than a label: the payload's covering test, or a conventional mirror file that exists.
	testEvidence string
	// sourceSymbol is the top hit's own symbol: the thing being changed. It is what a recovered test
	// name has to share a word with, so that narrowing a command can never silently point the agent
	// at an unrelated test.
	sourceSymbol string
}

// buildSearchVerifyCommand derives the command, or returns nil.
func buildSearchVerifyCommand(
	results []SearchResult,
	evidence searchVerifyEvidence,
) *SearchVerifyCommand {
	subject, ok := searchVerifySubjectFor(results)
	if !ok {
		return nil
	}
	if subject.testPath == "" {
		// The payload found no covering test, but the repository's own layout often names one anyway.
		// A conventional mirror path that EXISTS is repository evidence in exactly the same sense a
		// build manifest is — the file is either there or it is not, nothing is inferred.
		if mirror := searchVerifyMirrorTest(subject.sourcePath, &evidence); mirror != "" {
			subject.testPath = mirror
			subject.testEvidence = "mirror test file"
		}
	}
	command := deriveSearchVerifyCommand(subject, &evidence)
	if command == nil {
		return nil
	}
	if searchVerifyCommandCost(command) > searchVerifyCommandMaxBytes {
		return nil
	}
	command.RunnerMissing = searchVerifyRunnerMissing(command.Command, &evidence)
	// NOTE ON WHY THIS ANNOTATES RATHER THAN SUPPRESSES.
	//
	// The prior was strong: sessions whose emitted VERIFY then failed spent a mean 4.50 turns hunting
	// for a toolchain against 0.00 when it ran clean, on all four models. Suppressing the command when
	// the runner was absent made things WORSE on both models measured:
	//
	//   Sonnet, instances whose VERIFY was silenced (n=3):  +87.3% -> +103.9%   (worse 16.6pt)
	//   Haiku,  instances whose VERIFY was silenced (n=4):  -62.3% ->  -30.1%   (worse 32.1pt)
	//
	// carbon-2752 is the clean case: silencing `php -l` moved it -75.6% -> -23.4% and 15 -> 34 turns.
	// The env-hunt correlation was real but the causation ran the other way. A command that FAILS is a
	// cheap dead end — the agent runs it once, reads the error, and stops. NO command is an open-ended
	// question, and the prompt rule telling it not to look for a runner does not hold. Emitting a
	// best-effort command is therefore better than silence even when it cannot run here.
	//
	// But suppression and silence are not the only options, and the two failures point in opposite
	// directions rather than at one answer:
	//
	//   suppressing a command whose runner is missing   Sonnet -40.7% -> -25.5%  (helped)
	//                                                   Haiku  -42.5% -> -49.5%  (hurt)
	//
	// One caller wants something to run; the other wants to be told to stop. Those are not
	// contradictory demands — they are one emission carrying more information. So the command is
	// always emitted AND flagged when its runner cannot be resolved. Neither behaviour is chosen for
	// the caller, and no model is named anywhere: the flag is a fact about this checkout's PATH and
	// manifests, which is the same class of evidence every other part of this derivation uses.
	return command
}

// searchVerifySubjectFor reads the subject out of the payload: the top candidate fix site, and
// the narrowest test the payload can point at.
//
// A ranked hit that is ITSELF a test file counts twice over, and both readings matter:
//
//   - It is not the fix site. VERIFY answers "how do I exercise the file I am changing", so a
//     test at rank 1 must not become sourcePath — the manifest search would then run from the
//     test tree and the mirror lookup would look for the test's own test.
//   - It IS a test path, and a better one than a guess. The covering-test BLOCK deliberately
//     declines to print a test the ranking already shows (searchCoveringTestAlreadySurfaced),
//     so a payload whose ranking promoted the test has no covering-test section at all. Reading
//     testPath only out of that section made VERIFY degrade to the module root in exactly the
//     case where the payload knew the test file best — it was printing it at rank 1.
//
// The covering-test section still wins when both exist: it carries the test NAME, which narrows
// the command from a file to a single case.
func searchVerifySubjectFor(results []SearchResult) (searchVerifySubject, bool) {
	subject := searchVerifySubject{}
	rankedTestPath, rankedTestName := "", ""
	for _, result := range results {
		switch result.Section {
		case searchSectionPrimary:
			if result.FilePath == "" || NonProgramTextPath(result.FilePath) {
				continue
			}
			if searchTestArtifactPath(searchLowerPath(result.FilePath)) {
				if rankedTestPath == "" {
					rankedTestPath = filePathToSlash(result.FilePath)
					rankedTestName = searchVerifyTestName(result)
				}
				continue
			}
			if subject.sourcePath == "" {
				subject.sourcePath = filePathToSlash(result.FilePath)
				subject.sourceSymbol = searchVerifyTestName(result)
			}
		case searchSectionCoveringTest:
			if subject.testPath == "" && result.FilePath != "" {
				subject.testPath = filePathToSlash(result.FilePath)
				subject.testName = searchVerifyTestName(result)
				subject.testEvidence = "covering test"
			}
		}
	}
	if subject.testPath == "" && rankedTestPath != "" {
		subject.testPath = rankedTestPath
		subject.testName = rankedTestName
		subject.testEvidence = "ranked test"
	}
	// A payload whose every program-text hit is a test still has a subject: the test is the thing
	// to run, and it is also the path the manifest search has to start from.
	if subject.sourcePath == "" {
		subject.sourcePath = rankedTestPath
	}
	return subject, subject.sourcePath != ""
}

// searchVerifyTestName is the covering test's own function name, unqualified: it is what every test
// runner's filter takes. A qualified name is deliberately reduced — `Class.method` is not a filter
// in most runners, and half a filter is worse than none.
func searchVerifyTestName(result SearchResult) string {
	name := result.SymbolName
	if name == "" && result.QualifiedName != "" {
		parts := strings.Split(result.QualifiedName, ".")
		name = parts[len(parts)-1]
	}
	if strings.ContainsAny(name, " ()<>{}'\"`$&|;") {
		return ""
	}
	return name
}

// searchVerifyRecoveredTestName recovers a test function name from the covering test FILE when the
// payload knew the file but not a name. Without it the command degrades to a whole-package run, and
// that is the expensive failure mode rather than a cosmetic one: over the 16 sessions of a 30-instance
// haiku run that were offered a VERIFY line, the 7 package-scope commands spent 6.86 build/test turns
// against 5.11 for the 9 that carried a selector, at +31% session tokens. A package sweep in a
// monorepo surfaces failures that predate the patch, and the agent then debugs those instead of its
// own edit — the edit->test->edit loop this block exists to prevent.
//
// It is deliberately conservative. A narrow run of the WRONG test reports a failure about code the
// agent never touched, which costs strictly more than the broad run it replaced, so a recovered name
// is used ONLY when it shares a whole word with the symbol being changed. When nothing matches, the
// caller keeps its package-scope command: this function widens what can be narrowed, it never
// invents a selector.
func searchVerifyRecoveredTestName(
	testPath, symbol string,
	evidence *searchVerifyEvidence,
	declared func(string) []string,
) string {
	if testPath == "" || symbol == "" {
		return ""
	}
	content, ok := evidence.file(testPath)
	if !ok {
		return ""
	}
	words := searchVerifyNameWords(symbol)
	if len(words) == 0 {
		return ""
	}
	// Rank by HOW MUCH of the symbol a candidate matches, not by whether it matches at all.
	//
	// Accepting any single word and then preferring the shortest name actively selects the wrong test
	// whenever the symbol ends in a common suffix. Measured on caddyserver/caddy-4943, where
	// `CookieFilter.Filter` yields the words {cookie, filter}: `TestHashFilter` matches only the
	// generic `filter` and is SHORTER than `TestCookieFilter`, which matches both — so the old
	// tie-break emitted a command exercising an unrelated filter. The same shape appears on
	// hashicorp/terraform-34580. Both instances lose on Haiku AND Sonnet, so this is not a
	// model-specific effect.
	//
	// Counting distinct matched words fixes it without a vocabulary list: a candidate that covers more
	// of the symbol is more specific to it, and length remains the tie-break WITHIN an equal count, so
	// "TestFoo" still beats "TestFooWithUnrelatedOptionAndTimeout".
	best, bestMatches := "", 0
	for _, name := range declared(content) {
		lower := strings.ToLower(name)
		matches := 0
		for _, word := range words {
			if strings.Contains(lower, word) {
				matches++
			}
		}
		if matches == 0 {
			continue
		}
		if matches > bestMatches || (matches == bestMatches && len(name) < len(best)) {
			best, bestMatches = name, matches
		}
	}
	return best
}

// searchVerifyNameWords splits an identifier into lowercase words of four characters or more, on both
// camelCase boundaries and separators, so `Parser.loadTestFiles` yields load/files. Short words are
// dropped because a two-character fragment matches almost any test name, which would defeat the whole
// point of requiring a match.
//
// The test vocabulary itself is dropped for the same reason, and it is the sharper trap: a symbol
// named `loadTestFiles` contributes the word "test", which matches EVERY name in a Go test file — so
// the match would succeed on an unrelated test and the shortest-name rule would then prefer it.
func searchVerifyNameWords(symbol string) []string {
	generic := map[string]bool{"test": true, "tests": true, "spec": true, "specs": true, "case": true, "cases": true}
	var words []string
	var current strings.Builder
	flush := func() {
		if current.Len() >= 4 {
			if word := strings.ToLower(current.String()); !generic[word] {
				words = append(words, word)
			}
		}
		current.Reset()
	}
	for _, letter := range symbol {
		switch {
		case letter == '_' || letter == '-' || letter == '.' || letter == ':':
			flush()
		case letter >= 'A' && letter <= 'Z':
			flush()
			current.WriteRune(letter)
		default:
			current.WriteRune(letter)
		}
	}
	flush()
	return words
}

// searchVerifyGoTestNames are the `func TestXxx` declarations in a Go test file. Only the `Test`
// prefix is accepted, because that is the only prefix `go test -run` selects.
func searchVerifyGoTestNames(content string) []string {
	var names []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "func Test") {
			continue
		}
		rest := strings.TrimPrefix(trimmed, "func ")
		open := strings.Index(rest, "(")
		if open <= 0 {
			continue
		}
		name := rest[:open]
		if strings.ContainsAny(name, " \t*[]") {
			continue
		}
		names = append(names, name)
	}
	return names
}

// searchVerifyRustTestNames are the functions declared under a `#[test]` (or `#[tokio::test]`)
// attribute. Cargo's positional filter is a substring over the test's path, so the bare function
// name is a valid selector.
func searchVerifyRustTestNames(content string) []string {
	var names []string
	marked := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#[test") || strings.HasPrefix(trimmed, "#[tokio::test") {
			marked = true
			continue
		}
		if !marked {
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.HasPrefix(trimmed, "#[") {
			// Another attribute on the same item — still the same test.
			continue
		}
		marked = false
		start := strings.Index(trimmed, "fn ")
		if start < 0 {
			continue
		}
		rest := trimmed[start+len("fn "):]
		end := strings.IndexAny(rest, "(<")
		if end <= 0 {
			continue
		}
		names = append(names, rest[:end])
	}
	return names
}

// searchVerifyTestAffixes are the ways a test file is named after the file it tests, and
// searchVerifyTestDirs the directory swaps that put it in a parallel tree. The two are combined, so
// `src/Foo.php` -> `tests/FooTest.php` and `hooks/src/index.js` -> `hooks/test/index.test.js` are the
// same rule rather than two.
var (
	searchVerifyTestAffixes = []string{"_test", ".test", "Test", "_spec", ".spec", "Spec", "-test"}
	searchVerifyTestDirs    = [][2]string{
		{"src/main/java/", "src/test/java/"},
		{"src/main/kotlin/", "src/test/kotlin/"},
		{"src/main/", "src/test/"},
		{"src/", "tests/"},
		{"src/", "test/"},
		{"src/", "spec/"},
		{"lib/", "test/"},
		{"lib/", "spec/"},
	}
)

// searchVerifyMirrorTest probes the conventional test-file names for a source file and returns the
// first one that EXISTS, or "".
//
// It is bounded: a handful of names beside the file, the same names inside a `__tests__`/`test`
// subdirectory, and the same names in a parallel test tree. Every probe is a read that either finds a
// file or does not, so the result is a fact about the repository and never a guess.
func searchVerifyMirrorTest(sourcePath string, evidence *searchVerifyEvidence) string {
	dir := path.Dir(sourcePath)
	if dir == "." || dir == "/" {
		dir = ""
	}
	base := path.Base(sourcePath)
	stem := searchVerifyStem(base)
	extension := strings.TrimPrefix(strings.TrimPrefix(base, stem), ".")
	if stem == "" || extension == "" {
		return ""
	}
	for _, affix := range searchVerifyTestAffixes {
		candidates := []string{
			searchVerifyJoin(dir, stem+affix+"."+extension),
			searchVerifyJoin(searchVerifyJoin(dir, "__tests__"), stem+affix+"."+extension),
			searchVerifyJoin(searchVerifyJoin(dir, "__tests__"), stem+"."+extension),
			searchVerifyJoin(searchVerifyJoin(dir, "test"), stem+affix+"."+extension),
			searchVerifyJoin(searchVerifyJoin(dir, "tests"), stem+affix+"."+extension),
		}
		for _, swap := range searchVerifyTestDirs {
			if !strings.HasPrefix(sourcePath, swap[0]) && !strings.Contains(sourcePath, "/"+swap[0]) {
				continue
			}
			mirrored := strings.Replace(sourcePath, swap[0], swap[1], 1)
			mirrorDir := path.Dir(mirrored)
			if mirrorDir == "." || mirrorDir == "/" {
				mirrorDir = ""
			}
			candidates = append(candidates,
				searchVerifyJoin(mirrorDir, stem+affix+"."+extension),
				searchVerifyJoin(mirrorDir, stem+"."+extension),
			)
		}
		for _, candidate := range candidates {
			if candidate == sourcePath {
				continue
			}
			if evidence.exists(candidate) {
				return candidate
			}
		}
	}
	return ""
}

// deriveSearchVerifyCommand walks from the subject's own directory towards the repository root and
// stops at the FIRST manifest that licenses a narrow command.
//
// A manifest that exists but licenses nothing does not stop the walk: a `Cargo.toml` that is only a
// workspace stanza, or a monorepo leaf `package.json` with no test runner in it, is evidence about
// the tree, not about how to run a test, so the walk continues outward to the manifest that is.
func deriveSearchVerifyCommand(subject searchVerifySubject, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	dir := path.Dir(subject.sourcePath)
	if dir == "." || dir == "/" {
		dir = ""
	}
	for depth := 0; depth <= searchVerifyMaxDepth; depth++ {
		for _, derive := range searchVerifyDerivations {
			if command := derive(dir, subject, evidence); command != nil {
				return command
			}
		}
		if dir == "" {
			break
		}
		parent := path.Dir(dir)
		if parent == "." || parent == "/" || parent == dir {
			dir = ""
			continue
		}
		dir = parent
	}
	return nil
}

// searchVerifyDerivations is the ordered list of build systems consulted at each directory level.
// Language manifests come before generic ones: a Rust crate with a convenience Makefile must yield
// the crate's own test command, not `make test`.
var searchVerifyDerivations = []func(string, searchVerifySubject, *searchVerifyEvidence) *SearchVerifyCommand{
	deriveSearchVerifyCargo,
	deriveSearchVerifyGo,
	deriveSearchVerifyMaven,
	deriveSearchVerifyGradle,
	deriveSearchVerifyNode,
	deriveSearchVerifyComposer,
	deriveSearchVerifyPytest,
	deriveSearchVerifyRuby,
	deriveSearchVerifyCMake,
	deriveSearchVerifyMake,
	deriveSearchVerifyBuildCheck,
}

// searchVerifyBuildChecks maps a source extension to a check that compiles or parses THAT ONE FILE.
// Every entry is a pure syntax/bytecode check: it needs no build directory, no classpath, no test
// fixture and no network, so it cannot fail for a reason unrelated to the edit. Anything needing a
// resolved build graph (javac, gcc, tsc) is deliberately absent — a command that fails on its own
// invocation costs strictly more than no command, which is why the test derivations above stay
// silent rather than guess.
var searchVerifyBuildChecks = map[string]string{
	".php": "php -l ",
	".rb":  "ruby -c ",
	".js":  "node --check ",
	".jsx": "node --check ",
	".mjs": "node --check ",
	".cjs": "node --check ",
	".py":  "python -m py_compile ",
}

// deriveSearchVerifyBuildCheck is the last tier, and the only one that fires when the payload found
// no covering test. It answers a different question from the derivations above — "does what I just
// wrote parse?" rather than "does it behave?" — and it is labelled as such so the block is never
// read as a test run.
//
// Measured on 30 paired haiku sessions: sessions whose payload carried a VERIFY block spent 30.6%
// fewer tokens than the no-tool baseline, sessions without one only 15.2%. Every derivation above
// requires subject.testPath, so 16 of 30 sessions got nothing and paid the difference re-deriving an
// invocation by hand. Operating rule 8 already tells the agent to "compile what you touched" in that
// case; this emits the command for it instead of leaving it a shell hunt.
func deriveSearchVerifyBuildCheck(dir string, subject searchVerifySubject, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	// Only the root pass may emit: the check names an absolute repo-relative path, so walking to a
	// parent directory would re-emit the identical command once per level.
	if dir != "" {
		return nil
	}
	if subject.testPath != "" {
		return nil // a covering test exists; a parse check would be a downgrade, not a fallback
	}
	if subject.sourcePath == "" || !evidence.exists(subject.sourcePath) {
		return nil
	}
	check, ok := searchVerifyBuildChecks[strings.ToLower(path.Ext(subject.sourcePath))]
	if !ok {
		return nil
	}
	return &SearchVerifyCommand{
		Command:     check + subject.sourcePath,
		Targets:     subject.sourcePath,
		DerivedFrom: "build check only - no covering test found; this parses the file, it runs no tests",
	}
}

// searchVerifyJoin builds a repository-relative path inside a directory.
func searchVerifyJoin(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// searchVerifyRunIn prefixes a command with the `cd` it needs when the manifest is not at the
// repository root. Without it the emitted line is only true for someone who already knows where to
// stand, which is the discovery this block exists to remove.
func searchVerifyRunIn(dir, command string) string {
	if dir == "" {
		return command
	}
	return "cd " + dir + " && " + command
}

// searchVerifyRelative expresses a path relative to a directory, or reports false when the path is
// not inside it.
func searchVerifyRelative(dir, filePath string) (string, bool) {
	if dir == "" {
		return filePath, true
	}
	if !strings.HasPrefix(filePath, dir+"/") {
		return "", false
	}
	return strings.TrimPrefix(filePath, dir+"/"), true
}

func searchVerifyStem(filePath string) string {
	base := path.Base(filePath)
	if index := strings.IndexByte(base, '.'); index > 0 {
		return base[:index]
	}
	return base
}

// deriveSearchVerifyCargo derives a Cargo command. The package name comes from the manifest's own
// `[package] name`, and the test selector from where the covering test lives: a test under `src/` is
// a unit test in the lib target, a test under `tests/` is its own integration target named after
// the file.
func deriveSearchVerifyCargo(dir string, subject searchVerifySubject, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	manifest := searchVerifyJoin(dir, "Cargo.toml")
	content, ok := evidence.file(manifest)
	if !ok {
		return nil
	}
	name, found := searchVerifyTomlPackageName(content)
	if !found {
		return nil
	}
	// `-p` only selects from a workspace. When the crate is not part of one, the command has to run
	// in the crate itself.
	root, rootFound := evidence.file("Cargo.toml")
	workspace := rootFound && strings.Contains(root, "[workspace]")
	selector := ""
	if workspace {
		selector = " -p " + name
	}
	target, filter, targets := "", "", "package "+name
	if relative, inside := searchVerifyRelative(dir, subject.testPath); inside && subject.testPath != "" {
		switch {
		case strings.HasPrefix(relative, "tests/"):
			target = " --test " + searchVerifyStem(relative)
		case strings.HasPrefix(relative, "src/"):
			target = " --lib"
		}
		if target != "" {
			targets = subject.testPath
			testName := subject.testName
			if testName == "" {
				// Same reasoning as the Go derivation: `cargo test -p <crate>` builds and runs the
				// crate's whole test set, which is where the pre-existing failures live.
				testName = searchVerifyRecoveredTestName(subject.testPath, subject.sourceSymbol, evidence, searchVerifyRustTestNames)
			}
			if testName != "" {
				filter = " " + testName
			}
		}
	}
	command := "cargo test" + selector + target + filter
	if !workspace {
		command = searchVerifyRunIn(dir, "cargo test"+target+filter)
	}
	derived := "Cargo.toml [package] name"
	if workspace {
		derived = "root Cargo.toml [workspace] + " + manifest + " package name"
	}
	if target != "" {
		derived += " + " + subject.testEvidence + " path"
	}
	return &SearchVerifyCommand{Command: command, Targets: targets, DerivedFrom: derived}
}

// searchVerifyTomlPackageName reads `name` out of a Cargo manifest's `[package]` table. It is a
// line scanner rather than a TOML parser because it needs exactly one key from one table, and a
// dependency on a parser to read one line is a worse trade than a scanner that gives up.
func searchVerifyTomlPackageName(content string) (string, bool) {
	inPackage := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inPackage = trimmed == "[package]"
			continue
		}
		if !inPackage || !strings.HasPrefix(trimmed, "name") {
			continue
		}
		_, value, found := strings.Cut(trimmed, "=")
		if !found {
			continue
		}
		name := strings.Trim(strings.TrimSpace(value), "\"'")
		// `name = { workspace = true }` inherits the name from the workspace, so this manifest does
		// not state it and cannot be used as a selector.
		if name == "" || strings.ContainsAny(name, "{} \t") {
			return "", false
		}
		return name, true
	}
	return "", false
}

// deriveSearchVerifyGo derives a `go test` command for the package the edit lands in, filtered to
// the covering test when there is one. The `-run` pattern is anchored: an unanchored name also runs
// every test whose name contains it.
func deriveSearchVerifyGo(dir string, subject searchVerifySubject, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	manifest := searchVerifyJoin(dir, "go.mod")
	if !evidence.exists(manifest) {
		return nil
	}
	packagePath := path.Dir(subject.sourcePath)
	targets := "package ./" + packagePath
	filter := ""
	recovered := false
	if subject.testPath != "" {
		if _, inside := searchVerifyRelative(dir, subject.testPath); inside {
			packagePath = path.Dir(subject.testPath)
			targets = subject.testPath
			name := subject.testName
			if !strings.HasPrefix(name, "Test") {
				// The test FILE is known but not its function name — recover it from the file rather
				// than degrading to `go test ./pkg`, which runs every test in the package.
				name = searchVerifyRecoveredTestName(subject.testPath, subject.sourceSymbol, evidence, searchVerifyGoTestNames)
				recovered = name != ""
			}
			if strings.HasPrefix(name, "Test") {
				filter = fmt.Sprintf(" -run '^%s$'", name)
			}
		}
	}
	relative, inside := searchVerifyRelative(dir, packagePath)
	if !inside {
		if dir != "" || packagePath == "" {
			return nil
		}
		relative = packagePath
	}
	selector := "./" + relative
	if relative == "" || relative == "." {
		selector = "."
	}
	derived := manifest + " module root"
	if filter != "" {
		if recovered {
			// Say where the name came from. A recovered name is weaker evidence than a covering-test
			// name and the agent is entitled to judge it rather than trust it.
			derived += " + test name read from " + path.Base(subject.testPath)
		} else {
			derived += " + " + subject.testEvidence + " name"
		}
	}
	return &SearchVerifyCommand{
		Command:     searchVerifyRunIn(dir, "go test "+selector+filter),
		Targets:     targets,
		DerivedFrom: derived,
	}
}

// deriveSearchVerifyMaven derives a Maven command scoped to the module the edit lands in. `-am`
// builds the module's dependencies, without which a scoped build in a multi-module repository fails
// for reasons that have nothing to do with the patch; `-DfailIfNoTests=false` is required whenever
// `-Dtest` is combined with `-pl`.
func deriveSearchVerifyMaven(dir string, subject searchVerifySubject, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	manifest := searchVerifyJoin(dir, "pom.xml")
	if !evidence.exists(manifest) {
		return nil
	}
	scope := ""
	if dir != "" {
		scope = " -pl " + dir + " -am"
	}
	if subject.testPath != "" {
		if _, inside := searchVerifyRelative(dir, subject.testPath); inside {
			class := searchVerifyStem(subject.testPath)
			return &SearchVerifyCommand{
				Command: "mvn -q" + scope + " -Dtest=" + class +
					" -DfailIfNoTests=false test",
				Targets:     subject.testPath,
				DerivedFrom: manifest + " module + " + subject.testEvidence + " class",
			}
		}
	}
	return &SearchVerifyCommand{
		Command:     "mvn -q" + scope + " test",
		Targets:     "module " + searchVerifyModuleLabel(dir),
		DerivedFrom: manifest + " module",
	}
}

func searchVerifyModuleLabel(dir string) string {
	if dir == "" {
		return "(root)"
	}
	return dir
}

// deriveSearchVerifyGradle derives a Gradle command. It requires the wrapper — a bare `gradle` may
// not be installed and would not be the repository's own version — and a covering test class,
// because `:module:test` without `--tests` is the whole module and Gradle's project path is only
// worth deriving when it buys a filter.
func deriveSearchVerifyGradle(dir string, subject searchVerifySubject, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	manifest := ""
	for _, name := range []string{"build.gradle", "build.gradle.kts"} {
		if evidence.exists(searchVerifyJoin(dir, name)) {
			manifest = searchVerifyJoin(dir, name)
			break
		}
	}
	if manifest == "" || !evidence.exists("gradlew") {
		return nil
	}
	if subject.testPath == "" {
		return nil
	}
	if _, inside := searchVerifyRelative(dir, subject.testPath); !inside {
		return nil
	}
	project := ":"
	if dir != "" {
		project = ":" + strings.ReplaceAll(dir, "/", ":") + ":"
	}
	class := searchVerifyStem(subject.testPath)
	return &SearchVerifyCommand{
		Command:     fmt.Sprintf("./gradlew %stest --tests '%s'", project, class),
		Targets:     subject.testPath,
		DerivedFrom: manifest + " + gradlew + " + subject.testEvidence + " class",
	}
}

// searchVerifyNodeRunners maps a declared dependency or test script to the invocation that runs ONE
// file with it. Only runners whose single-file form is unambiguous are here.
var searchVerifyNodeRunners = []struct {
	name    string
	command string
}{
	{name: "vitest", command: "npx vitest run"},
	{name: "jest", command: "npx jest"},
	{name: "mocha", command: "npx mocha"},
}

// deriveSearchVerifyNode derives a JS/TS command from the nearest package.json that actually names
// a test runner. A monorepo leaf package with no runner in it is skipped, which is what sends the
// walk out to the workspace root where the runner is configured.
func deriveSearchVerifyNode(dir string, subject searchVerifySubject, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	manifest := searchVerifyJoin(dir, "package.json")
	content, ok := evidence.file(manifest)
	if !ok {
		return nil
	}
	var parsed struct {
		Scripts         map[string]string `json:"scripts"`
		DevDependencies map[string]string `json:"devDependencies"`
		Dependencies    map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil
	}
	runner, evidenceKind := "", ""
	for _, candidate := range searchVerifyNodeRunners {
		if strings.Contains(parsed.Scripts["test"], candidate.name) {
			runner, evidenceKind = candidate.command, "scripts.test"
			break
		}
		if _, declared := parsed.DevDependencies[candidate.name]; declared {
			runner, evidenceKind = candidate.command, "devDependencies"
			break
		}
		if _, declared := parsed.Dependencies[candidate.name]; declared {
			runner, evidenceKind = candidate.command, "dependencies"
			break
		}
	}
	if runner == "" || subject.testPath == "" {
		return nil
	}
	relative, inside := searchVerifyRelative(dir, subject.testPath)
	if !inside {
		return nil
	}
	return &SearchVerifyCommand{
		Command:     searchVerifyRunIn(dir, runner+" "+relative),
		Targets:     subject.testPath,
		DerivedFrom: manifest + " " + evidenceKind + " + " + subject.testEvidence + " path",
	}
}

// deriveSearchVerifyComposer derives a PHPUnit command. The evidence that licenses it is a PHPUnit
// configuration file next to the manifest — without one the vendored binary may not exist and the
// suite's bootstrap is unknown.
func deriveSearchVerifyComposer(dir string, subject searchVerifySubject, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	if !evidence.exists(searchVerifyJoin(dir, "composer.json")) {
		return nil
	}
	config := ""
	for _, name := range []string{"phpunit.xml", "phpunit.xml.dist"} {
		if evidence.exists(searchVerifyJoin(dir, name)) {
			config = searchVerifyJoin(dir, name)
			break
		}
	}
	if config == "" || subject.testPath == "" {
		return nil
	}
	relative, inside := searchVerifyRelative(dir, subject.testPath)
	if !inside {
		return nil
	}
	// PROBE THE RUNNER. A `composer.json` plus a `phpunit.xml` says the project USES phpunit; it does
	// not say the binary is installed. In a fresh checkout with no `composer install`, `vendor/` does
	// not exist and this command cannot run.
	//
	// Measured on phpoffice/phpspreadsheet-3463: the emitted `vendor/bin/phpunit ...` failed, and the
	// agent spent SIX consecutive turns on environment archaeology (`ls -la`, `which phpunit`,
	// `cat composer.json | grep scripts`, `php -v`, `php -m`, `find -name phpunit.xml*`) before
	// giving up on tests entirely. Silence would have cost none of them: the block's own contract is
	// that a wrong command costs strictly more than no command.
	if !evidence.exists(searchVerifyJoin(dir, "vendor/bin/phpunit")) {
		return nil
	}
	return &SearchVerifyCommand{
		Command:     searchVerifyRunIn(dir, "vendor/bin/phpunit "+relative),
		Targets:     subject.testPath,
		DerivedFrom: config + " + " + subject.testEvidence + " path",
	}
}

// searchVerifyPytestConfigs are the files that mean "this tree's tests are run by pytest".
var searchVerifyPytestConfigs = []string{"pytest.ini", "tox.ini", "setup.cfg", "pyproject.toml"}

// deriveSearchVerifyPytest derives a pytest command. `-k` is used rather than a `::` node id: a node
// id has to name every enclosing class correctly and a wrong one selects nothing, while `-k` on the
// function name is exact enough to be narrow and cannot be malformed.
func deriveSearchVerifyPytest(dir string, subject searchVerifySubject, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	config := ""
	for _, name := range searchVerifyPytestConfigs {
		candidate := searchVerifyJoin(dir, name)
		content, ok := evidence.file(candidate)
		if !ok {
			continue
		}
		if !strings.Contains(content, "pytest") {
			continue
		}
		config = candidate
		break
	}
	if config == "" || subject.testPath == "" {
		return nil
	}
	relative, inside := searchVerifyRelative(dir, subject.testPath)
	if !inside {
		return nil
	}
	filter := ""
	if subject.testName != "" {
		filter = " -k " + subject.testName
	}
	return &SearchVerifyCommand{
		Command:     searchVerifyRunIn(dir, "python -m pytest "+relative+filter),
		Targets:     subject.testPath,
		DerivedFrom: config + " pytest config + " + subject.testEvidence + " path",
	}
}

// deriveSearchVerifyRuby derives an RSpec or a single-file Minitest command. Which one is decided by
// where the covering test lives, because that is what the repository itself decided.
func deriveSearchVerifyRuby(dir string, subject searchVerifySubject, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	hasGemfile := evidence.exists(searchVerifyJoin(dir, "Gemfile"))
	hasRakefile := evidence.exists(searchVerifyJoin(dir, "Rakefile"))
	if !hasGemfile && !hasRakefile {
		return nil
	}
	if subject.testPath == "" {
		return nil
	}
	relative, inside := searchVerifyRelative(dir, subject.testPath)
	if !inside {
		return nil
	}
	manifest := searchVerifyJoin(dir, "Rakefile")
	if !hasRakefile {
		manifest = searchVerifyJoin(dir, "Gemfile")
	}
	switch {
	case strings.HasPrefix(relative, "spec/") && evidence.exists(searchVerifyJoin(dir, ".rspec")):
		return &SearchVerifyCommand{
			Command:     searchVerifyRunIn(dir, "bundle exec rspec "+relative),
			Targets:     subject.testPath,
			DerivedFrom: searchVerifyJoin(dir, ".rspec") + " + " + subject.testEvidence + " path",
		}
	case strings.HasPrefix(relative, "test/"):
		return &SearchVerifyCommand{
			Command:     searchVerifyRunIn(dir, "bundle exec ruby -Itest "+relative),
			Targets:     subject.testPath,
			DerivedFrom: manifest + " + " + subject.testEvidence + " path under test/",
		}
	}
	return nil
}

// deriveSearchVerifyCMake derives a self-configuring CMake/CTest invocation.
//
// entire-graph had NO C/C++ verify inference, and a missing VERIFY block is the largest single
// measured effect in the benchmark:
//
//	VERIFY present   n=11   eg -35.3% vs baseline
//	VERIFY absent    n=16   eg -14.9% vs baseline
//
// Every regression above 200k tokens was a no-VERIFY cell. On fmtlib/fmt-2457 (the only C++ instance,
// worst regression at +107%) the agent got no command, edited blind for ten turns before its first
// compile, adopted the whole pre-test-patch suite as its oracle, read "PASSED 20" as success, and
// shipped a patch with zero functional change.
//
// The CONFIGURE STEP IS PART OF THE STRING deliberately: a bare `cmake --build build` fails in a
// fresh checkout because build/ does not exist, which is exactly what happened -- a cold 13,085-char
// build, then five turns lost re-finding the directory after `cd build` did not persist.
func deriveSearchVerifyCMake(dir string, subject searchVerifySubject, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	manifest := searchVerifyJoin(dir, "CMakeLists.txt")
	content, ok := evidence.file(manifest)
	if !ok {
		return nil
	}
	// Only when the project declares tests: `enable_testing()` / `include(CTest)` is the repository's
	// own statement that ctest means something here. Without it a test command would be invented.
	if !strings.Contains(content, "enable_testing") && !strings.Contains(content, "include(CTest") {
		return nil
	}
	if subject.testPath == "" {
		return nil
	}
	relative, inside := searchVerifyRelative(dir, subject.testPath)
	if !inside {
		return nil
	}
	// The target is the test file's stem by CMake convention (test/ranges-test.cc -> ranges-test),
	// and it is VERIFIED against the CMake sources rather than guessed: no declared target, no block.
	target := searchVerifyStem(relative)
	if target == "" {
		return nil
	}
	declared := strings.Contains(content, target)
	if !declared {
		if nested, found := evidence.file(searchVerifyJoin(dir, "test/CMakeLists.txt")); found && strings.Contains(nested, target) {
			declared = true
		}
	}
	if !declared {
		return nil
	}
	command := "cmake -S . -B build >/dev/null && cmake --build build --target " + target +
		" -j4 && ctest --test-dir build -R " + target + " --output-on-failure"
	return &SearchVerifyCommand{
		Command:     searchVerifyRunIn(dir, command),
		Targets:     subject.testPath,
		DerivedFrom: manifest + " enable_testing + " + subject.testEvidence + " target",
	}
}

// deriveSearchVerifyMake derives `make <target>` and is the coarsest derivation here: a Makefile
// states that a target exists, not how to narrow it. It is still emitted, because the measured cost
// is agents fumbling the INVOCATION, and it is emitted only when a covering test exists — otherwise
// there is no evidence the target runs anything relevant to this edit.
//
// The derivation names the target so a reader can see it is the whole suite, not a filter.
func deriveSearchVerifyMake(dir string, subject searchVerifySubject, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	manifest := searchVerifyJoin(dir, "Makefile")
	content, ok := evidence.file(manifest)
	if !ok {
		return nil
	}
	if subject.testPath == "" {
		return nil
	}
	if _, inside := searchVerifyRelative(dir, subject.testPath); !inside {
		return nil
	}
	target := ""
	for _, candidate := range []string{"test", "check", "tests"} {
		if searchMakefileHasTarget(content, candidate) {
			target = candidate
			break
		}
	}
	if target == "" {
		return nil
	}
	return &SearchVerifyCommand{
		Command:     searchVerifyRunIn(dir, "make "+target),
		Targets:     subject.testPath + " (whole suite; Makefile states no narrower target)",
		DerivedFrom: manifest + " target " + target,
	}
}

// searchMakefileHasTarget reports whether a Makefile declares a target at the start of a line. A
// mention inside a recipe or a variable is not a target.
func searchMakefileHasTarget(content, target string) bool {
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, target) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, target))
		if strings.HasPrefix(rest, ":") && !strings.HasPrefix(rest, ":=") {
			return true
		}
	}
	return false
}

// searchVerifyCommandCost measures the block on the larger of its two wire forms.
func searchVerifyCommandCost(command *SearchVerifyCommand) int {
	if command == nil {
		return 0
	}
	// The missing-runner note is deliberately EXCLUDED from the cost. It is a fixed addendum that
	// exists to stop a caller hunting for a toolchain, and letting its bytes push a command over
	// searchVerifyCommandMaxBytes would delete the whole block — reintroducing, through a byte
	// budget, exactly the suppression that measured worse on Haiku (-42.5% -> -49.5%). The cap
	// governs the command, its target and its derivation; the note rides along.
	bare := *command
	bare.RunnerMissing = false
	encoded, err := json.Marshal(&bare)
	if err != nil {
		return 0
	}
	return maxInt(len(encoded), len(RenderSearchVerifyCommand(&bare)))
}

// RenderSearchVerifyCommand renders the block for a text reader: the command on its own line so it
// can be copied, then what it targets and where it came from.
func RenderSearchVerifyCommand(command *SearchVerifyCommand) []byte {
	if command == nil || command.Command == "" {
		return nil
	}
	note := ""
	if command.RunnerMissing {
		note = "\n  NOTE: this command's runner is not installed in this checkout. Run it once; if it fails" +
			" on the invocation rather than on your code, do NOT go looking for a toolchain — syntax-check" +
			" the file you changed and stop."
	}
	return []byte(fmt.Sprintf("VERIFY: %s\n  targets %s (from %s)%s\n",
		command.Command, command.Targets, command.DerivedFrom, note))
}

// searchVerifyRunner extracts the executable a command actually invokes: the first token of the first
// stage, after any `cd <dir> &&` prefixes the derivation added to reach a manifest.
func searchVerifyRunner(command string) string {
	remainder := strings.TrimSpace(command)
	for strings.HasPrefix(remainder, "cd ") {
		separator := strings.Index(remainder, "&&")
		if separator < 0 {
			return ""
		}
		remainder = strings.TrimSpace(remainder[separator+2:])
	}
	fields := strings.Fields(remainder)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// searchVerifyRunnerMissing reports whether the command's runner cannot be executed here. A
// path-shaped runner (`vendor/bin/phpunit`, `./gradlew`) is answered by the repository itself;
// a bare name is resolved on PATH. `bundle exec X` and `npx X` resolve X themselves, so the launcher
// being present says nothing — look through it at the tool it is asked to run.
func searchVerifyRunnerMissing(command string, evidence *searchVerifyEvidence) bool {
	runner := searchVerifyRunner(command)
	if runner == "" {
		return false
	}
	if strings.ContainsRune(runner, '/') {
		return !evidence.exists(strings.TrimPrefix(runner, "./"))
	}
	lookPath := evidence.lookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath(runner); err != nil {
		return true
	}
	inner := searchVerifyLaunchedTool(command, runner)
	if inner == "" {
		return false
	}
	switch runner {
	case "bundle", "bundler":
		lock, ok := evidence.file("Gemfile.lock")
		return !ok || !strings.Contains(lock, inner)
	case "npx", "pnpm", "yarn":
		return !evidence.exists("node_modules/.bin/" + inner)
	}
	return false
}

// searchVerifyLaunchedTool returns the tool a launcher is being asked to run — `rspec` for
// `bundle exec rspec` — or "" when the command is not of that shape.
func searchVerifyLaunchedTool(command, runner string) string {
	fields := strings.Fields(strings.TrimSpace(command))
	for index, field := range fields {
		if field != runner {
			continue
		}
		rest := fields[index+1:]
		if len(rest) > 0 && rest[0] == "exec" {
			rest = rest[1:]
		}
		for _, candidate := range rest {
			if strings.HasPrefix(candidate, "-") {
				continue
			}
			return candidate
		}
		return ""
	}
	return ""
}

// filePathToSlash normalizes a repository path for the string handling above. Repository paths are
// already slash-separated everywhere in this package; this states it at the boundary.
func filePathToSlash(filePath string) string {
	return strings.ReplaceAll(filePath, "\\", "/")
}
