package sem

import (
	"encoding/json"
	"fmt"
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
}

// searchVerifyEvidence is the bounded view of the repository the derivation is allowed to consult.
// It caches misses as well as hits: a manifest that is not there is probed once per path.
type searchVerifyEvidence struct {
	read  contentReader
	cache map[string]string
	miss  map[string]bool
	reads int
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
	return command
}

// searchVerifySubjectFor reads the subject out of the payload: the top candidate fix site, and the
// covering test if one is in the payload.
func searchVerifySubjectFor(results []SearchResult) (searchVerifySubject, bool) {
	subject := searchVerifySubject{}
	for _, result := range results {
		switch result.Section {
		case searchSectionPrimary:
			if subject.sourcePath == "" && result.FilePath != "" && !NonProgramTextPath(result.FilePath) {
				subject.sourcePath = filePathToSlash(result.FilePath)
			}
		case searchSectionCoveringTest:
			if subject.testPath == "" && result.FilePath != "" {
				subject.testPath = filePathToSlash(result.FilePath)
				subject.testName = searchVerifyTestName(result)
				subject.testEvidence = "covering test"
			}
		}
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
	deriveSearchVerifyMake,
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
			if subject.testName != "" {
				filter = " " + subject.testName
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
			if strings.HasPrefix(subject.testName, "Test") {
				filter = fmt.Sprintf(" -run '^%s$'", subject.testName)
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
	encoded, err := json.Marshal(command)
	if err != nil {
		return 0
	}
	return maxInt(len(encoded), len(RenderSearchVerifyCommand(command)))
}

// RenderSearchVerifyCommand renders the block for a text reader: the command on its own line so it
// can be copied, then what it targets and where it came from.
func RenderSearchVerifyCommand(command *SearchVerifyCommand) []byte {
	if command == nil || command.Command == "" {
		return nil
	}
	return []byte(fmt.Sprintf("VERIFY: %s\n  targets %s (from %s)\n",
		command.Command, command.Targets, command.DerivedFrom))
}

// filePathToSlash normalizes a repository path for the string handling above. Repository paths are
// already slash-separated everywhere in this package; this states it at the boundary.
func filePathToSlash(filePath string) string {
	return strings.ReplaceAll(filePath, "\\", "/")
}
