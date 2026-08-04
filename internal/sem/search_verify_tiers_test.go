package sem

import (
	"errors"
	"strings"
	"testing"
)

// verifyTierFixture builds the evidence and a one-hit payload for a repository skeleton. lookPath is
// stubbed so the runner check is a fact about the fixture rather than about the machine running it.
func verifyTierFixture(files map[string]string, sourcePath string, installed ...string) (
	[]SearchResult, searchVerifyEvidence,
) {
	present := map[string]bool{}
	for _, name := range installed {
		present[name] = true
	}
	evidence := searchVerifyEvidence{
		read: func(path string) (string, bool) {
			content, ok := files[path]
			return content, ok
		},
		lookPath: func(name string) (string, error) {
			if present[name] {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
	}
	results := []SearchResult{{
		Rank: 1, FilePath: sourcePath, Section: searchSectionPrimary,
		StartLine: 1, EndLine: 2, FocusLine: 1, SymbolName: "target", QualifiedName: "Target.target",
	}}
	// Any test file in the skeleton is also RANKED, which is how the real payloads resolve their test
	// path: searchVerifySubjectFor reads it out of the ranking when the covering-test section declined
	// to repeat a test the ranking already shows.
	for path := range files {
		if path != sourcePath && searchTestArtifactPath(searchLowerPath(path)) {
			results = append(results, SearchResult{
				Rank: len(results) + 1, FilePath: path, Section: searchSectionPrimary,
				StartLine: 1, EndLine: 2, FocusLine: 1,
			})
		}
	}
	return results, evidence
}

// TestSearchVerifyTierLadderAnswersEveryEcosystem is the golden table. Every row is an ecosystem that
// measurably produced NO verify line before this round, and the tier is the claim the block makes.
//
// The four gap instances the blueprint names are the first four rows: fmtlib/fmt twice (CMake, which
// no derivation recognised, so C and C++ had no tier at all), three.js (a repository whose ranked hits
// are generated bundles, which fell through every rung to silence) and preact (a package.json whose
// scripts.test the suite tier could not read even though the narrow tier could).
func TestSearchVerifyTierLadderAnswersEveryEcosystem(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		files       map[string]string
		source      string
		installed   []string
		wantTier    string
		wantCommand string
	}{
		{
			name: "cmake narrow - the fmt gap, C++ had no tier at all",
			files: map[string]string{
				"CMakeLists.txt":       "project(fmt)\nenable_testing()\nadd_executable(ranges-test test/ranges-test.cc)\n",
				"include/fmt/ranges.h": "template <typename T> struct formatter {};\n",
				"test/ranges-test.cc":  "TEST(ranges_test, join) {}\n",
			},
			source: "include/fmt/ranges.h", installed: []string{"cmake"},
			wantTier: searchVerifyTierNarrow, wantCommand: "ctest --test-dir build -R ranges-test",
		},
		{
			name: "cmake suite - tests declared but no per-file target",
			files: map[string]string{
				"CMakeLists.txt": "project(x)\ninclude(CTest)\n",
				"src/core.cc":    "int main() { return 0; }\n",
			},
			source: "src/core.cc", installed: []string{"cmake"},
			wantTier: searchVerifyTierSuite, wantCommand: "ctest --test-dir build --output-on-failure",
		},
		{
			name: "node suite from devDependencies - the preact parity gap",
			files: map[string]string{
				"package.json": `{"scripts":{"test":"echo none"},"devDependencies":{"jest":"^29"}}`,
				"src/index.js": "export function target() {}\n",
			},
			source: "src/index.js", installed: []string{"npm"},
			wantTier: searchVerifyTierSuite, wantCommand: "npm test",
		},
		{
			name: "build check - the three.js case, no manifest licenses a test",
			files: map[string]string{
				"src/cameras/PerspectiveCamera.js": "export class PerspectiveCamera {}\n",
			},
			source: "src/cameras/PerspectiveCamera.js", installed: []string{"node"},
			wantTier: searchVerifyTierBuildCheck, wantCommand: "node --check src/cameras/PerspectiveCamera.js",
		},
		{
			name: "residual floor - never silent absence",
			files: map[string]string{
				"src/main.zig": "pub fn target() void {}\n",
			},
			source:   "src/main.zig",
			wantTier: searchVerifyTierNone, wantCommand: searchVerifyNoneCommand,
		},
		{
			// searchVerifyMaxDepth 8 -> 32. The manifest is 10 directories above the source, which is
			// ordinary in a monorepo and used to terminate the walk — indistinguishable from "no build
			// system here", one of the four measured causes of a non-derivable VERIFY.
			name: "depth 32 - a manifest ten levels up is still found",
			files: map[string]string{
				"go.mod":                          "module example.com/x\n",
				"a/b/c/d/e/f/g/h/i/j/svc.go":      "package j\nfunc Target() {}\n",
				"a/b/c/d/e/f/g/h/i/j/svc_test.go": "package j\nfunc TestTarget(t *testing.T) {}\n",
			},
			source: "a/b/c/d/e/f/g/h/i/j/svc.go", installed: []string{"go"},
			wantTier: searchVerifyTierNarrow, wantCommand: "go test",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			results, evidence := verifyTierFixture(testCase.files, testCase.source, testCase.installed...)
			command := buildSearchVerifyCommand(results, evidence)
			if command == nil {
				t.Fatal("no VERIFY command — the residual floor must make this impossible")
			}
			if command.Tier != testCase.wantTier {
				t.Fatalf("tier = %q, want %q (command %q)", command.Tier, testCase.wantTier, command.Command)
			}
			if !strings.Contains(command.Command, testCase.wantCommand) {
				t.Fatalf("command = %q, want it to contain %q", command.Command, testCase.wantCommand)
			}
			rendered := string(RenderSearchVerifyCommand(command))
			if !strings.HasPrefix(rendered, "VERIFY: ") {
				t.Fatalf("the line start must stay byte-identical: %q", rendered)
			}
			if !strings.Contains(rendered, "tier="+testCase.wantTier) {
				t.Fatalf("rendered block omits the tier:\n%s", rendered)
			}
			if cost := searchVerifyCommandCost(command); cost > searchVerifyCommandMaxBytes {
				t.Fatalf("cost %d over the %d cap", cost, searchVerifyCommandMaxBytes)
			}
		})
	}
}

// TestSearchVerifyMirrorTestFindsPrefixedNames pins the other half of the world's naming conventions.
// Suffix-only probing (`parser_test.go`) missed Python's `test_<stem>.py`, Ruby's `test_<stem>.rb` and
// JUnit's `Test<Stem>.java` entirely, so repositories with perfectly conventional test trees reported
// "no covering test" — one of the four measured causes of a non-derivable VERIFY.
func TestSearchVerifyMirrorTestFindsPrefixedNames(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		files  map[string]string
		source string
		want   string
	}{
		{name: "python test_ prefix beside the file",
			files:  map[string]string{"pkg/parser.py": "", "pkg/test_parser.py": ""},
			source: "pkg/parser.py", want: "pkg/test_parser.py"},
		{name: "python test_ prefix in a tests directory",
			files:  map[string]string{"pkg/parser.py": "", "pkg/tests/test_parser.py": ""},
			source: "pkg/parser.py", want: "pkg/tests/test_parser.py"},
		{name: "ruby test_ prefix in a parallel test tree",
			files:  map[string]string{"lib/parser.rb": "", "test/test_parser.rb": ""},
			source: "lib/parser.rb", want: "test/test_parser.rb"},
		{name: "capitalised Test prefix keeps the stem's own capital",
			files:  map[string]string{"src/main/java/Parser.java": "", "src/test/java/TestParser.java": ""},
			source: "src/main/java/Parser.java", want: "src/test/java/TestParser.java"},
		{name: "spec swap in the other direction",
			files:  map[string]string{"test/support/helper.rb": "", "spec/support/helper_spec.rb": ""},
			source: "test/support/helper.rb", want: "spec/support/helper_spec.rb"},
		{name: "the suffix forms still work",
			files:  map[string]string{"pkg/parser.go": "", "pkg/parser_test.go": ""},
			source: "pkg/parser.go", want: "pkg/parser_test.go"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, evidence := verifyTierFixture(testCase.files, testCase.source)
			if got := searchVerifyMirrorTest(testCase.source, &evidence); got != testCase.want {
				t.Fatalf("mirror = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestRenderSearchVerifyAnnotatesAMissingRunnerAndNeverSuppresses pins annotation-not-suppression, and
// the cost exclusion that makes it possible: letting the note's bytes push a command over the cap would
// delete the block, reintroducing through a byte budget the suppression that measured worse on Haiku.
func TestRenderSearchVerifyAnnotatesAMissingRunnerAndNeverSuppresses(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"Gemfile":             "gem 'rspec'\n",
		"Gemfile.lock":        "  specs:\n    rspec (3.0)\n",
		"lib/parser.rb":       "class Parser; end\n",
		"spec/parser_spec.rb": "describe Parser do; end\n",
	}
	// `bundle` is deliberately NOT installed.
	results, evidence := verifyTierFixture(files, "lib/parser.rb")
	command := buildSearchVerifyCommand(results, evidence)
	if command == nil {
		t.Fatal("a missing runner suppressed the whole block")
	}
	if !command.RunnerMissing {
		t.Fatalf("runner %q was not reported missing", command.Command)
	}
	rendered := string(RenderSearchVerifyCommand(command))
	if !strings.Contains(rendered, "runner is not installed in this checkout") {
		t.Fatalf("no annotation:\n%s", rendered)
	}
	// The note rides free: cost is measured without it.
	bare := *command
	bare.RunnerMissing = false
	if searchVerifyCommandCost(command) != searchVerifyCommandCost(&bare) {
		t.Fatal("the missing-runner note was charged against the cap")
	}
	// Installed runners are not annotated. The skeleton derives a `ruby -c` build check (its Gemfile
	// licenses no per-file rspec selector), so `ruby` is what has to be present for the negative case.
	_, installed := verifyTierFixture(files, "lib/parser.rb", "bundle", "ruby", "rspec")
	if got := buildSearchVerifyCommand(results, installed); got == nil || got.RunnerMissing {
		t.Fatalf("an installed runner was reported missing: %+v", got)
	}
}

// TestSearchVerifyPrefixDecoratesAfterTheCd pins --verify-prefix: the token lands on the command that
// runs, not on the `cd`, and "VERIFY: " stays byte-identical at line start.
func TestSearchVerifyPrefixDecoratesAfterTheCd(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct{ command, want string }{
		{command: "go test ./...", want: "VERIFY: EGTOK go test ./..."},
		{command: "cd sub && npm test", want: "VERIFY: cd sub && EGTOK npm test"},
		{command: "cd a && cd b && pytest", want: "VERIFY: cd a && cd b && EGTOK pytest"},
	} {
		rendered := string(RenderSearchVerifyCommand(&SearchVerifyCommand{
			Command: testCase.command, Targets: "t", DerivedFrom: "d",
			Tier: searchVerifyTierSuite, Prefix: "EGTOK",
		}))
		if !strings.HasPrefix(rendered, testCase.want+"\n") {
			t.Fatalf("rendered %q, want prefix %q", rendered, testCase.want)
		}
	}
}

// TestSearchVerifyCommandDegradesInsteadOfVanishing pins the overflow behaviour: provenance yields
// first, then the target, and the command line itself never does.
func TestSearchVerifyCommandDegradesInsteadOfVanishing(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", searchVerifyCommandMaxBytes)
	files := map[string]string{
		"go.mod":                   "module example.com/x\n",
		"pkg/" + long + ".go":      "package pkg\n",
		"pkg/" + long + "_test.go": "package pkg\nfunc TestTarget(t *testing.T) {}\n",
	}
	results, evidence := verifyTierFixture(files, "pkg/"+long+".go", "go")
	command := buildSearchVerifyCommand(results, evidence)
	if command == nil || command.Command == "" {
		t.Fatal("an oversized block was deleted instead of degraded")
	}
	if !strings.Contains(string(RenderSearchVerifyCommand(command)), "VERIFY: ") {
		t.Fatal("the command line did not survive degradation")
	}
}
