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
	// Raised from 320 to hold searchVerifyContractNote, a fixed-size addition. It is the cheapest block
	// in the payload measured against what it prevents: post-edit verify churn cost +$4.11 across three
	// sessions, and on google__gson-1014 a hand-assembled javac/JUnit classpath was 55.6% of the whole
	// session's output tokens.
	searchVerifyCommandMaxBytes = 640

	// searchVerifyContractNote is the CONTRACT on the emitted command, and every clause is a measured
	// failure mode rather than general advice:
	//
	//   - "once, then fix and re-run the same command" — sessions ran the command, hit an environment
	//     error, and started looking for a different way to test instead of reading the failure.
	//   - "no alternative harness or hand-assembled classpath" — gson-1014 built its own javac +
	//     JUnit invocation from scratch; that one detour was 55.6% of its output tokens.
	//   - "no revert-and-reapply confirmation" — several sessions undid a correct edit to "prove" it
	//     was the cause, then reapplied it, paying twice for no new information.
	//
	// It does not change WHEN a VERIFY line is emitted, only what the line promises about how to use it.
	searchVerifyContractNote = "  run it ONCE after editing; if it fails, fix the code and re-run THIS command.\n" +
		"  do not build an alternative test harness, hand-assemble a classpath, or re-verify by\n" +
		"  reverting and reapplying the edit.\n"

	// searchVerifyMaxDepth bounds the ancestor walk for a build manifest.
	//
	// Raised 8 -> 32. Eight levels is not "deep enough to be a directory rather than a module" in the
	// repositories this actually runs on: monorepo test trees reach 9-14 segments
	// (packages/docusaurus-theme-classic/src/theme/CodeBlock/Content/String.tsx is 7 before the file,
	// and Java's src/test/java/<group>/<artifact>/... routinely passes 12), and the walk terminating
	// early is indistinguishable from "no build system here" — which is one of the four measured causes
	// of VERIFY being non-derivable. The walk is cheap: each level is a handful of cached path probes
	// bounded by searchVerifyMaxReads, and it stops at the first manifest that licenses a command.
	searchVerifyMaxDepth = 32

	// VERIFY TIERS. The block reports which rung of the ladder produced it, because "does this behave"
	// and "does this parse" are different claims and a reader that cannot tell them apart has to
	// re-derive the command to find out. The harness also buckets sessions by tier, which is how the
	// 12/12 unrunnable-command finding was measured in the first place.
	searchVerifyTierNarrow     = "narrow"
	searchVerifyTierSuite      = "suite"
	searchVerifyTierBuildCheck = "build-check"
	searchVerifyTierNone       = "none"

	// searchVerifyNoneCommand is the residual floor. A payload with ranked results and no derivable
	// command used to emit NOTHING, and silent absence is the worst outcome available: paired with the
	// stop-early doctrine it lets an agent ship unverified, and it is indistinguishable from a bug in
	// the deriver. One line that states the fact and prescribes the fallback costs 96 bytes.
	searchVerifyNoneCommand = "none derivable (no build manifest found) - syntax-check the file you edited and stop."

	// searchVerifyRunnerNote annotates a command whose runner is not installed HERE. It is an
	// annotation and never a suppression: the command is still the right one, and a caller who cannot
	// run it needs to be told to stop rather than to go toolchain-hunting. Measured: suppressing such
	// commands instead of annotating them moved Haiku from -49.5% to -42.5%.
	searchVerifyRunnerNote = "\n  NOTE: this command's runner is not installed in this checkout. Run it once; if it fails" +
		" on the invocation rather than on your code, do NOT go looking for a toolchain — syntax-check" +
		" the file you changed and stop."

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
	// Tier is which rung of the ladder produced this command: narrow, suite, build-check or none.
	Tier string `json:"tier,omitempty"`
	// RunnerMissing marks a command whose executable is not present in this checkout. It is rendered
	// as a NOTE beside the command and is deliberately excluded from the byte cap — see
	// searchVerifyCommandCost.
	RunnerMissing bool `json:"runner_missing,omitempty"`
	// Prefix is a caller-supplied decorator inserted after any `cd <dir> &&` the derivation added, so
	// a harness can bake in a grep-able token without changing the "VERIFY: " line start.
	Prefix string `json:"prefix,omitempty"`
}

// searchVerifyEvidence is the bounded view of the repository the derivation is allowed to consult.
// It caches misses as well as hits: a manifest that is not there is probed once per path.
type searchVerifyEvidence struct {
	read  contentReader
	cache map[string]string
	miss  map[string]bool
	reads int
	// lookPath resolves an executable name the way exec.LookPath does. Injected so the runner check is
	// a pure function of its inputs in tests instead of a fact about the machine running them.
	lookPath func(string) (string, error)
	// prefix is the caller's --verify-prefix decorator, carried here because the derivations build the
	// command string and the renderer must not have to re-parse it.
	prefix string
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
	// symbolName is the top hit's own symbol, used only to recover a test CASE name out of a test file
	// when the payload's covering-test section could not supply one.
	symbolName string
}

// buildSearchVerifyCommand derives the command, or returns nil.
func buildSearchVerifyCommand(
	results []SearchResult,
	evidence searchVerifyEvidence,
) *SearchVerifyCommand {
	subject, ok := searchVerifySubjectFor(results)
	if !ok {
		// ZERO PRIMARY RESULTS (or all of them non-program text) still gets the residual floor. Silent
		// absence is the worst outcome available: it is indistinguishable from a deriver bug, and paired
		// with the stop-early doctrine it lets an agent ship unverified. Measured on three.js-25687,
		// whose every ranked hit is a generated bundle and which emitted no VERIFY line at all.
		return searchVerifyResidualFloor("", evidence.prefix)
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
	if command != nil && command.Tier == "" {
		command.Tier = searchVerifyTierNarrow
	}
	if command == nil {
		// No narrow command could be derived — most often because the payload found no covering test
		// (a `test_<name>` minitest file the mirror lookup cannot name, a suite with no per-file
		// selector). Rather than emit nothing — which, paired with the stop-early doctrine, is what
		// lets an agent skip verification and ship a confident-but-wrong patch — fall back to the
		// repository's own WHOLE-SUITE invocation. These are canonical commands (`go test ./...`,
		// `bundle exec rake test`) that are guaranteed to RUN, so the "silence over a wrong command"
		// rule still holds: the failure mode it guards against (an error about the invocation, not
		// the code) cannot happen here. The command is slower and unfiltered, so it is labeled as the
		// whole suite; a slow-but-real verification beats a whole failed task.
		command = deriveSearchVerifySuiteCommand(subject, &evidence)
		if command != nil {
			command.Tier = searchVerifyTierSuite
		}
	}
	if command == nil {
		// Third rung: does what I just wrote PARSE? A different question from "does it behave", labelled
		// as such, and the only rung that fires when no manifest in the tree licenses a test command.
		command = deriveSearchVerifyBuildCheck("", subject, &evidence)
	}
	if command == nil {
		return searchVerifyResidualFloor(subject.sourcePath, evidence.prefix)
	}
	command.Prefix = evidence.prefix
	// The runner check ANNOTATES; it never suppresses. See searchVerifyRunnerNote.
	command.RunnerMissing = searchVerifyRunnerMissing(command.Command, &evidence)
	// DEGRADE, never delete. Returning nil on overflow reintroduced silent absence through a byte
	// budget — the exact failure the residual floor exists to prevent. The command line is the
	// irreducible part, so provenance yields first and the target second.
	if searchVerifyCommandCost(command) > searchVerifyCommandMaxBytes {
		command.DerivedFrom = "(derivation omitted for length)"
	}
	if searchVerifyCommandCost(command) > searchVerifyCommandMaxBytes {
		command.Targets = ""
	}
	return command
}

// searchVerifyResidualFloor is the last rung: no manifest, no test file, no single-file checker. It
// states the fact and prescribes the fallback, which is strictly better than the silence it replaces.
func searchVerifyResidualFloor(sourcePath, prefix string) *SearchVerifyCommand {
	targets := sourcePath
	if targets == "" {
		targets = "the file you edited"
	}
	return &SearchVerifyCommand{
		Command:     searchVerifyNoneCommand,
		Targets:     targets,
		DerivedFrom: "no manifest, no test file, no single-file checker for this language",
		Tier:        searchVerifyTierNone,
		Prefix:      prefix,
	}
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
				if subject.symbolName == "" {
					subject.symbolName = result.QualifiedName
					if subject.symbolName == "" {
						subject.symbolName = result.SymbolName
					}
				}
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

// searchVerifyTestAffixes are the ways a test file is named after the file it tests, and
// searchVerifyTestDirs the directory swaps that put it in a parallel tree. The two are combined, so
// `src/Foo.php` -> `tests/FooTest.php` and `hooks/src/index.js` -> `hooks/test/index.test.js` are the
// same rule rather than two.
var (
	// SUFFIX affixes: `parser_test.go`, `parser.test.ts`, `ParserTest.java`, `parser_spec.rb`.
	searchVerifyTestAffixes = []string{"_test", ".test", "Test", "_spec", ".spec", "Spec", "-test"}
	// PREFIX affixes are the other half of the world's conventions and were entirely missing, which is
	// one of the four measured causes of a non-derivable VERIFY: Python and Ruby name the file
	// `test_<stem>.py` / `test_<stem>.rb`, JUnit and xUnit ports name the class `Test<Stem>`, and RSpec
	// uses `<stem>_spec` under spec/ but `spec_<stem>` in some layouts. A suffix-only probe finds none
	// of them, so a repository with a perfectly conventional test tree reported "no covering test".
	searchVerifyTestPrefixes = []string{"test_", "test-", "Test", "spec_", "spec-"}
	searchVerifyTestDirs     = [][2]string{
		{"src/main/java/", "src/test/java/"},
		{"src/main/kotlin/", "src/test/kotlin/"},
		{"src/main/", "src/test/"},
		{"src/", "tests/"},
		{"src/", "test/"},
		{"src/", "spec/"},
		{"lib/", "test/"},
		{"lib/", "spec/"},
		// test/ <-> spec/ in BOTH directions, and at any depth: a source file already under `test/`
		// (a fixture, a helper) has its real spec under `spec/`, and Ruby/Elixir projects mix the two
		// conventions inside one tree. Without these the mirror lookup could only ever walk src -> test.
		{"test/", "spec/"},
		{"spec/", "test/"},
		{"tests/", "spec/"},
		{"spec/", "tests/"},
		{"app/", "spec/"},
		{"app/", "test/"},
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
	// Every affix is probed in BOTH positions. `names` is the affixed basenames to look for; the
	// candidate directories below are the same in either case, so prefix support costs one extra loop
	// over a bounded list and no new IO shape.
	names := make([]string, 0, len(searchVerifyTestAffixes)+len(searchVerifyTestPrefixes))
	for _, affix := range searchVerifyTestAffixes {
		names = append(names, stem+affix)
	}
	for _, prefix := range searchVerifyTestPrefixes {
		names = append(names, prefix+stem)
		// `Test<Stem>` needs the stem's own capital: `parser` -> `TestParser`, not `Testparser`.
		if prefix == "Test" && stem != "" {
			names = append(names, prefix+strings.ToUpper(stem[:1])+stem[1:])
		}
	}
	for _, affixed := range names {
		candidates := []string{
			searchVerifyJoin(dir, affixed+"."+extension),
			searchVerifyJoin(searchVerifyJoin(dir, "__tests__"), affixed+"."+extension),
			searchVerifyJoin(searchVerifyJoin(dir, "__tests__"), stem+"."+extension),
			searchVerifyJoin(searchVerifyJoin(dir, "test"), affixed+"."+extension),
			searchVerifyJoin(searchVerifyJoin(dir, "tests"), affixed+"."+extension),
			searchVerifyJoin(searchVerifyJoin(dir, "spec"), affixed+"."+extension),
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
				searchVerifyJoin(mirrorDir, affixed+"."+extension),
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
}

// deriveSearchVerifySuiteCommand is the whole-suite fallback consulted only when no narrow command
// exists. It walks from the subject's directory towards the root exactly like the narrow derivation
// and returns the first recognized ecosystem's canonical suite command. Unlike the narrow tier it
// does NOT require a covering test: its whole point is the case where the payload found none.
func deriveSearchVerifySuiteCommand(subject searchVerifySubject, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	dir := path.Dir(subject.sourcePath)
	if dir == "." || dir == "/" {
		dir = ""
	}
	for depth := 0; depth <= searchVerifyMaxDepth; depth++ {
		for _, derive := range searchVerifySuiteDerivations {
			if command := derive(dir, evidence); command != nil {
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

// searchVerifySuiteDerivations mirrors searchVerifyDerivations, language-specific before generic, so
// a Rust crate with a convenience Makefile still yields `cargo test`, not `make test`.
var searchVerifySuiteDerivations = []func(string, *searchVerifyEvidence) *SearchVerifyCommand{
	deriveSearchVerifySuiteCargo,
	deriveSearchVerifySuiteGo,
	deriveSearchVerifySuiteMaven,
	deriveSearchVerifySuiteGradle,
	deriveSearchVerifySuiteNode,
	deriveSearchVerifySuiteComposer,
	deriveSearchVerifySuitePytest,
	deriveSearchVerifySuiteRuby,
	deriveSearchVerifySuiteCMake,
	deriveSearchVerifySuiteMake,
}

// searchVerifySuiteCommand builds a whole-suite command, labeling both the target (no covering test
// was found) and the derivation (whole suite) so the reader knows it is unfiltered and slow.
func searchVerifySuiteCommand(dir, command, derived string) *SearchVerifyCommand {
	return &SearchVerifyCommand{
		Command:     searchVerifyRunIn(dir, command),
		Targets:     "whole test suite (no covering test identified)",
		DerivedFrom: derived + " (whole suite)",
	}
}

func deriveSearchVerifySuiteCargo(dir string, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	manifest := searchVerifyJoin(dir, "Cargo.toml")
	content, ok := evidence.file(manifest)
	if !ok || (!strings.Contains(content, "[package]") && !strings.Contains(content, "[workspace]")) {
		return nil
	}
	return searchVerifySuiteCommand(dir, "cargo test", manifest)
}

func deriveSearchVerifySuiteGo(dir string, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	manifest := searchVerifyJoin(dir, "go.mod")
	if !evidence.exists(manifest) {
		return nil
	}
	return searchVerifySuiteCommand(dir, "go test ./...", manifest+" module")
}

func deriveSearchVerifySuiteMaven(dir string, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	manifest := searchVerifyJoin(dir, "pom.xml")
	if !evidence.exists(manifest) {
		return nil
	}
	return searchVerifySuiteCommand(dir, "mvn -q test", manifest)
}

func deriveSearchVerifySuiteGradle(dir string, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	manifest := ""
	for _, name := range []string{"build.gradle", "build.gradle.kts"} {
		if evidence.exists(searchVerifyJoin(dir, name)) {
			manifest = searchVerifyJoin(dir, name)
			break
		}
	}
	if manifest == "" {
		return nil
	}
	wrapperDir, wrapperPath, ok := searchVerifyAncestorFile(dir, "gradlew", evidence)
	if !ok {
		return nil
	}
	return searchVerifySuiteCommand(wrapperDir, "./gradlew test", manifest+" + "+wrapperPath)
}

func deriveSearchVerifySuiteNode(dir string, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	manifest := searchVerifyJoin(dir, "package.json")
	content, ok := evidence.file(manifest)
	if !ok {
		return nil
	}
	var parsed searchVerifyNodeManifest
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil
	}
	// devDeps-aware, matching the NARROW tier. A package.json whose scripts.test is `echo "no test"` or
	// absent still declares jest/mocha/vitest in devDependencies, and that declaration is the
	// repository's own statement of its runner. Reading only scripts.test made the suite tier silent on
	// exactly the manifests the narrow tier could already read — a parity gap, not a policy.
	runner, evidenceKind := searchVerifyNodeRunnerFromManifest(parsed)
	if runner == "" {
		return nil
	}
	return searchVerifySuiteCommand(dir, "npm test", manifest+" "+evidenceKind)
}

func deriveSearchVerifySuiteComposer(dir string, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	if !evidence.exists(searchVerifyJoin(dir, "composer.json")) {
		return nil
	}
	for _, name := range []string{"phpunit.xml", "phpunit.xml.dist"} {
		if evidence.exists(searchVerifyJoin(dir, name)) {
			return searchVerifySuiteCommand(dir, "vendor/bin/phpunit", searchVerifyJoin(dir, name))
		}
	}
	return nil
}

func deriveSearchVerifySuitePytest(dir string, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	for _, name := range searchVerifyPytestConfigs {
		candidate := searchVerifyJoin(dir, name)
		content, ok := evidence.file(candidate)
		if ok && strings.Contains(content, "pytest") {
			return searchVerifySuiteCommand(dir, "python -m pytest", candidate+" pytest config")
		}
	}
	return nil
}

// deriveSearchVerifySuiteRuby prefers RSpec when the tree is configured for it (`.rspec`), otherwise
// falls to `rake test` when the Rakefile names a test task — the same order the narrow Ruby
// derivation uses, decided by the repository's own layout.
func deriveSearchVerifySuiteRuby(dir string, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	hasGemfile := evidence.exists(searchVerifyJoin(dir, "Gemfile"))
	hasRakefile := evidence.exists(searchVerifyJoin(dir, "Rakefile"))
	if !hasGemfile && !hasRakefile {
		return nil
	}
	if evidence.exists(searchVerifyJoin(dir, ".rspec")) {
		return searchVerifySuiteCommand(dir, "bundle exec rspec", searchVerifyJoin(dir, ".rspec"))
	}
	if hasRakefile {
		content, _ := evidence.file(searchVerifyJoin(dir, "Rakefile"))
		if strings.Contains(content, "test") || strings.Contains(content, "TestTask") {
			return searchVerifySuiteCommand(dir, "bundle exec rake test", searchVerifyJoin(dir, "Rakefile")+" test task")
		}
	}
	return nil
}

func deriveSearchVerifySuiteMake(dir string, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	content, ok := evidence.file(searchVerifyJoin(dir, "Makefile"))
	if !ok {
		return nil
	}
	for _, target := range []string{"test", "check", "tests"} {
		if searchMakefileHasTarget(content, target) {
			return searchVerifySuiteCommand(dir, "make "+target, searchVerifyJoin(dir, "Makefile")+" target "+target)
		}
	}
	return nil
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

func searchVerifyAncestorFile(dir, name string, evidence *searchVerifyEvidence) (string, string, bool) {
	for depth := 0; depth <= searchVerifyMaxDepth; depth++ {
		candidate := searchVerifyJoin(dir, name)
		if evidence.exists(candidate) {
			return dir, candidate, true
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
	return "", "", false
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
			// RECOVER the case name from the test file itself when the payload could not supply one. A
			// file-level command re-runs everything in the file; a named case is what makes VERIFY
			// narrow. The recovery reads the file's own `#[test]` declarations and picks the one
			// covering MOST of the symbol's words, so it can only ever name a test that exists.
			testName := subject.testName
			if testName == "" {
				testName = searchVerifyRecoveredTestName(
					subject.testPath, subject.symbolName, evidence, searchVerifyRustTestNames,
				)
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
	if subject.testPath != "" {
		if _, inside := searchVerifyRelative(dir, subject.testPath); inside {
			packagePath = path.Dir(subject.testPath)
			targets = subject.testPath
			testName := subject.testName
			if testName == "" {
				// Same recovery as Cargo: `go test -run` needs a name, and the test file declares them.
				testName = searchVerifyRecoveredTestName(
					subject.testPath, subject.symbolName, evidence, searchVerifyGoTestNames,
				)
			}
			if strings.HasPrefix(testName, "Test") {
				filter = fmt.Sprintf(" -run '^%s$'", testName)
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
		derived += " + " + subject.testEvidence + " name"
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

type searchVerifyNodeManifest struct {
	Scripts         map[string]string `json:"scripts"`
	DevDependencies map[string]string `json:"devDependencies"`
	Dependencies    map[string]string `json:"dependencies"`
}

func searchVerifyNodeRunnerFromManifest(parsed searchVerifyNodeManifest) (string, string) {
	if runner, ok := searchVerifyNodeRunnerFromScript(parsed.Scripts["test"]); ok {
		return runner, "scripts.test"
	}
	for _, candidate := range searchVerifyNodeRunners {
		if _, declared := parsed.DevDependencies[candidate.name]; declared {
			return candidate.command, "devDependencies"
		}
		if _, declared := parsed.Dependencies[candidate.name]; declared {
			return candidate.command, "dependencies"
		}
	}
	return "", ""
}

func searchVerifyNodeRunnerFromScript(script string) (string, bool) {
	for _, candidate := range searchVerifyNodeRunners {
		if strings.Contains(script, candidate.name) {
			return candidate.command, true
		}
	}
	return "", false
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
	var parsed searchVerifyNodeManifest
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil
	}
	runner, evidenceKind := searchVerifyNodeRunnerFromManifest(parsed)
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
	// The missing-runner NOTE is excluded from the cost. It is a fixed addendum that exists to stop a
	// caller hunting for a toolchain, and letting its bytes push a command over the cap would delete the
	// whole block — reintroducing, through a byte budget, exactly the suppression that measured worse on
	// Haiku (-49.5% -> -42.5%). The cap governs the command, its target and its derivation; the note
	// rides along.
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
	// "VERIFY: " stays byte-identical at line start whatever else changes — every harness and every
	// prior measurement keys on that prefix.
	rendered := "VERIFY: " + searchVerifyDecorated(command) + "\n"
	evidenceLine := "  targets " + command.Targets
	if command.Targets == "" {
		evidenceLine = "  targets (omitted for length)"
	}
	if command.DerivedFrom != "" {
		evidenceLine += " (from " + command.DerivedFrom + ")"
	}
	if command.Tier != "" {
		evidenceLine += " tier=" + command.Tier
	}
	if command.RunnerMissing {
		evidenceLine += searchVerifyRunnerNote
	}
	rendered += evidenceLine + "\n"
	if command.Tier == searchVerifyTierNone {
		// The residual floor prescribes its own action; the full contract note would be advice about a
		// command that does not exist.
		return []byte(rendered)
	}
	return []byte(rendered + searchVerifyContractNote)
}

// filePathToSlash normalizes a repository path for the string handling above. Repository paths are
// already slash-separated everywhere in this package; this states it at the boundary.
func filePathToSlash(filePath string) string {
	return strings.ReplaceAll(filePath, "\\", "/")
}

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
	// NOTE: the sibling implementation returned early when subject.testPath != "", on the reasoning
	// that a parse check would be a downgrade from a real test. That is true of the NARROW tier and
	// false here: this function is only ever reached after both the narrow and the suite tier declined,
	// so a covering test existing does not mean any runnable command was derivable from it. Keeping the
	// early return is one of the four measured causes of a non-derivable VERIFY — a repository with a
	// test file but no recognised manifest got nothing at all.
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
		DerivedFrom: "build check only - no runnable test command derivable; this parses the file, it runs no tests",
		Tier:        searchVerifyTierBuildCheck,
	}
}

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
		Tier:        searchVerifyTierNarrow,
	}
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

// deriveSearchVerifySuiteCMake is the CMake suite twin. It is the whole reason C and C++ had NO verify
// tier at all: neither ecosystem has a package manifest the other derivations recognise, so
// fmtlib/fmt, three.js's native bits and preact's build tooling all fell through every rung to nothing.
//
// Gated on the project's own `enable_testing()` / `include(CTest)` exactly like the narrow twin — that
// declaration is what makes `ctest` mean something here rather than a command invented for the reader.
func deriveSearchVerifySuiteCMake(dir string, evidence *searchVerifyEvidence) *SearchVerifyCommand {
	manifest := searchVerifyJoin(dir, "CMakeLists.txt")
	content, ok := evidence.file(manifest)
	if !ok {
		return nil
	}
	if !strings.Contains(content, "enable_testing") && !strings.Contains(content, "include(CTest") {
		return nil
	}
	return searchVerifySuiteCommand(dir,
		"cmake -S . -B build >/dev/null && cmake --build build -j4 && ctest --test-dir build --output-on-failure",
		manifest+" enable_testing")
}

// searchVerifyDecorated inserts the caller's --verify-prefix token AFTER any `cd <dir> &&` stages the
// derivation added, so the decorator lands on the command that actually runs rather than on the `cd`.
// The "VERIFY: " line start is untouched, which is what lets a harness grep for its own token without
// changing how anything else parses the block.
func searchVerifyDecorated(command *SearchVerifyCommand) string {
	if command.Prefix == "" {
		return command.Command
	}
	remainder := command.Command
	head := ""
	for strings.HasPrefix(remainder, "cd ") {
		separator := strings.Index(remainder, "&&")
		if separator < 0 {
			break
		}
		head += remainder[:separator+2] + " "
		remainder = strings.TrimSpace(remainder[separator+2:])
	}
	return head + command.Prefix + " " + remainder
}
