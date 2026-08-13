package sem

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// searchVerifyTestEvidence builds an evidence view over a fixed set of repository files.
func searchVerifyTestEvidence(files map[string]string) searchVerifyEvidence {
	return searchVerifyTestEvidenceWithout(files)
}

// searchVerifyTestEvidenceWithout builds the same view, but reports the named executables as absent
// from PATH so the probe can be exercised deterministically.
func searchVerifyTestEvidenceWithout(files map[string]string, missing ...string) searchVerifyEvidence {
	absent := make(map[string]bool, len(missing))
	for _, name := range missing {
		absent[name] = true
	}
	return searchVerifyEvidence{
		lookPath: func(name string) (string, error) {
			if absent[name] {
				return "", exec.ErrNotFound
			}
			return "/usr/bin/" + name, nil
		},
		read: func(path string) (string, bool) {
			content, ok := files[path]
			return content, ok
		},
	}
}

// A known test FILE with no known test NAME used to degrade to `go test ./pkg` — a whole-package run,
// which is the measured expensive case (6.86 build turns against 5.11 for commands carrying a
// selector, +31% session tokens). The name is recoverable from the file itself.
func TestDeriveSearchVerifyGoRecoversTestNameFromTheTestFile(t *testing.T) {
	t.Parallel()
	evidence := searchVerifyTestEvidence(map[string]string{
		"go.mod": "module x\n",
		// TestUnrelatedThing is declared FIRST and is the shorter name, so it wins on both tie-breaks
		// unless the generic word "test" is excluded from matching.
		"internal/configs/parser_config_dir_test.go": "package configs\n\n" +
			"func TestUnrelatedThing(t *testing.T) {}\n\n" +
			"func TestParserLoadTestFiles(t *testing.T) {}\n",
	})
	results := []SearchResult{
		{Rank: 1, FilePath: "internal/configs/parser_config_dir.go", SymbolName: "Parser.loadTestFiles", Section: searchSectionPrimary},
		{Rank: 2, FilePath: "internal/configs/parser_config_dir_test.go", Section: searchSectionPrimary},
	}
	got := buildSearchVerifyCommand(results, evidence)
	if got == nil {
		t.Fatal("expected a command")
	}
	if want := "go test ./internal/configs -run '^TestParserLoadTestFiles$'"; got.Command != want {
		t.Fatalf("command:\n got %q\nwant %q", got.Command, want)
	}
	if !strings.Contains(got.DerivedFrom, "test name read from parser_config_dir_test.go") {
		t.Fatalf("derivation should say the name was read from the file, got %q", got.DerivedFrom)
	}
}

// Narrowing must never point at an unrelated test: a wrong narrow command costs more than the broad
// run it replaced, because the failure it reports is about code the agent never touched.
func TestDeriveSearchVerifyGoKeepsPackageScopeWhenNoTestNameMatches(t *testing.T) {
	t.Parallel()
	evidence := searchVerifyTestEvidence(map[string]string{
		"go.mod": "module x\n",
		"internal/configs/parser_config_dir_test.go": "package configs\n\nfunc TestSomethingElseEntirely(t *testing.T) {}\n",
	})
	results := []SearchResult{
		{Rank: 1, FilePath: "internal/configs/parser_config_dir.go", SymbolName: "Parser.loadFiles", Section: searchSectionPrimary},
		{Rank: 2, FilePath: "internal/configs/parser_config_dir_test.go", Section: searchSectionPrimary},
	}
	got := buildSearchVerifyCommand(results, evidence)
	if got == nil {
		t.Fatal("expected a command")
	}
	if want := "go test ./internal/configs"; got.Command != want {
		t.Fatalf("command:\n got %q\nwant %q", got.Command, want)
	}
}

func TestDeriveSearchVerifyCommandFromBuildEvidence(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		files       map[string]string
		subject     searchVerifySubject
		wantCommand string
		wantTargets string
		wantDerived string
	}{
		{
			name: "cargo workspace member with a unit test",
			files: map[string]string{
				"Cargo.toml":                      "[workspace]\nmembers = [\"crates/printer\"]\n",
				"crates/printer/Cargo.toml":       "[package]\nname = \"grep-printer\"\nversion = \"0.1.0\"\n",
				"crates/printer/src/util.rs":      "fn f() {}\n",
				"crates/printer/src/util_test.rs": "",
			},
			subject: searchVerifySubject{
				sourcePath: "crates/printer/src/util.rs",
				testPath:   "crates/printer/src/util.rs",
				testName:   "test_trim", testEvidence: "covering test",
			},
			wantCommand: "cargo test -p grep-printer --lib test_trim",
			wantTargets: "crates/printer/src/util.rs",
			wantDerived: "root Cargo.toml [workspace] + crates/printer/Cargo.toml package name + covering test path",
		},
		{
			name: "cargo integration test names its own target",
			files: map[string]string{
				"Cargo.toml":              "[workspace]\n",
				"axum/Cargo.toml":         "[package]\nname = \"axum\"\n",
				"axum/src/routing/mod.rs": "",
				"axum/tests/fallback.rs":  "",
			},
			subject: searchVerifySubject{
				sourcePath: "axum/src/routing/mod.rs",
				testPath:   "axum/tests/fallback.rs", testEvidence: "mirror test file",
			},
			wantCommand: "cargo test -p axum --test fallback",
			wantTargets: "axum/tests/fallback.rs",
			wantDerived: "root Cargo.toml [workspace] + axum/Cargo.toml package name + mirror test file path",
		},
		{
			name: "a crate outside a workspace runs in its own directory",
			files: map[string]string{
				"tools/lint/Cargo.toml": "[package]\nname = \"lint\"\n",
				"tools/lint/src/lib.rs": "",
			},
			subject:     searchVerifySubject{sourcePath: "tools/lint/src/lib.rs"},
			wantCommand: "cd tools/lint && cargo test",
			wantTargets: "package lint",
			wantDerived: "Cargo.toml [package] name",
		},
		{
			name: "a workspace-inherited package name is not a selector",
			files: map[string]string{
				"crates/x/Cargo.toml": "[package]\nname = { workspace = true }\n",
				"crates/x/src/lib.rs": "",
			},
			subject: searchVerifySubject{sourcePath: "crates/x/src/lib.rs"},
		},
		{
			name: "go package with an anchored run filter",
			files: map[string]string{
				"go.mod":                                 "module example.com/app\n",
				"caddyconfig/httpcaddyfile/addresses.go": "",
				"caddyconfig/httpcaddyfile/addresses_test.go": "",
			},
			subject: searchVerifySubject{
				sourcePath: "caddyconfig/httpcaddyfile/addresses.go",
				testPath:   "caddyconfig/httpcaddyfile/addresses_test.go",
				testName:   "TestParseBind", testEvidence: "covering test",
			},
			wantCommand: "go test ./caddyconfig/httpcaddyfile -run '^TestParseBind$'",
			wantTargets: "caddyconfig/httpcaddyfile/addresses_test.go",
			wantDerived: "go.mod module root + covering test name",
		},
		{
			name: "go root package",
			files: map[string]string{
				"go.mod": "module example.com/gin\n",
				"gin.go": "",
			},
			subject:     searchVerifySubject{sourcePath: "gin.go"},
			wantCommand: "go test .",
			wantTargets: "package ./.",
			wantDerived: "go.mod module root",
		},
		{
			name: "a nested go module runs inside itself",
			files: map[string]string{
				"go.mod":            "module example.com/root\n",
				"tools/go.mod":      "module example.com/tools\n",
				"tools/pkg/lint.go": "",
			},
			subject:     searchVerifySubject{sourcePath: "tools/pkg/lint.go"},
			wantCommand: "cd tools && go test ./pkg",
			wantTargets: "package ./tools/pkg",
			wantDerived: "tools/go.mod module root",
		},
		{
			name: "maven module with a test class",
			files: map[string]string{
				"pom.xml":      "<project/>",
				"gson/pom.xml": "<project/>",
				"gson/src/main/java/com/google/gson/GsonBuilder.java":     "",
				"gson/src/test/java/com/google/gson/GsonBuilderTest.java": "",
			},
			subject: searchVerifySubject{
				sourcePath: "gson/src/main/java/com/google/gson/GsonBuilder.java",
				testPath:   "gson/src/test/java/com/google/gson/GsonBuilderTest.java",
				testName:   "testCustomAdapter", testEvidence: "covering test",
			},
			wantCommand: "mvn -q -pl gson -am -Dtest=GsonBuilderTest -DfailIfNoTests=false test",
			wantTargets: "gson/src/test/java/com/google/gson/GsonBuilderTest.java",
			wantDerived: "gson/pom.xml module + covering test class",
		},
		{
			name: "gradle needs the wrapper and a test class",
			files: map[string]string{
				"gradlew":                      "",
				"lib/build.gradle.kts":         "",
				"lib/src/main/kotlin/A.kt":     "",
				"lib/src/test/kotlin/ATest.kt": "",
			},
			subject: searchVerifySubject{
				sourcePath: "lib/src/main/kotlin/A.kt",
				testPath:   "lib/src/test/kotlin/ATest.kt", testEvidence: "mirror test file",
			},
			wantCommand: "./gradlew :lib:test --tests 'ATest'",
			wantTargets: "lib/src/test/kotlin/ATest.kt",
			wantDerived: "lib/build.gradle.kts + gradlew + mirror test file class",
		},
		{
			name: "gradle without the wrapper emits nothing",
			files: map[string]string{
				"lib/build.gradle":             "",
				"lib/src/main/kotlin/A.kt":     "",
				"lib/src/test/kotlin/ATest.kt": "",
			},
			subject: searchVerifySubject{
				sourcePath: "lib/src/main/kotlin/A.kt",
				testPath:   "lib/src/test/kotlin/ATest.kt", testEvidence: "mirror test file",
			},
		},
		{
			name: "a monorepo leaf without a runner defers to the workspace root",
			files: map[string]string{
				"package.json":                         `{"devDependencies":{"jest":"29"}}`,
				"packages/theme/package.json":          `{"name":"theme"}`,
				"packages/theme/src/CodeBlock.ts":      "",
				"packages/theme/src/CodeBlock.test.ts": "",
			},
			subject: searchVerifySubject{
				sourcePath: "packages/theme/src/CodeBlock.ts",
				testPath:   "packages/theme/src/CodeBlock.test.ts", testEvidence: "mirror test file",
			},
			wantCommand: "npx jest packages/theme/src/CodeBlock.test.ts",
			wantTargets: "packages/theme/src/CodeBlock.test.ts",
			wantDerived: "package.json devDependencies + mirror test file path",
		},
		{
			name: "a vitest script wins over a dependency scan",
			files: map[string]string{
				"package.json":                                   `{"scripts":{"test":"vitest run"}}`,
				"packages/reactivity/src/reactive.ts":            "",
				"packages/reactivity/__tests__/reactive.spec.ts": "",
			},
			subject: searchVerifySubject{
				sourcePath: "packages/reactivity/src/reactive.ts",
				testPath:   "packages/reactivity/__tests__/reactive.spec.ts", testEvidence: "mirror test file",
			},
			wantCommand: "npx vitest run packages/reactivity/__tests__/reactive.spec.ts",
			wantTargets: "packages/reactivity/__tests__/reactive.spec.ts",
			wantDerived: "package.json scripts.test + mirror test file path",
		},
		{
			name: "phpunit needs a configuration file next to composer.json",
			files: map[string]string{
				"composer.json":    `{}`,
				"phpunit.xml.dist": "<phpunit/>",
				// The runner must be INSTALLED, not merely configured: a fresh checkout with no
				// `composer install` has no vendor/ and the command cannot run.
				"vendor/bin/phpunit":              "#!/usr/bin/env php\n",
				"src/Calculation/Round.php":       "",
				"tests/Calculation/RoundTest.php": "",
			},
			subject: searchVerifySubject{
				sourcePath: "src/Calculation/Round.php",
				testPath:   "tests/Calculation/RoundTest.php", testEvidence: "mirror test file",
			},
			wantCommand: "vendor/bin/phpunit tests/Calculation/RoundTest.php",
			wantTargets: "tests/Calculation/RoundTest.php",
			wantDerived: "phpunit.xml.dist + mirror test file path",
		},
		{
			name: "composer without a phpunit configuration emits nothing",
			files: map[string]string{
				"composer.json":                   `{}`,
				"src/Calculation/Round.php":       "",
				"tests/Calculation/RoundTest.php": "",
			},
			subject: searchVerifySubject{
				sourcePath: "src/Calculation/Round.php",
				testPath:   "tests/Calculation/RoundTest.php", testEvidence: "mirror test file",
			},
		},
		{
			name: "pytest filters by name rather than by node id",
			files: map[string]string{
				"pyproject.toml":    "[tool.pytest.ini_options]\n",
				"pkg/mod.py":        "",
				"tests/test_mod.py": "",
			},
			subject: searchVerifySubject{
				sourcePath: "pkg/mod.py",
				testPath:   "tests/test_mod.py", testName: "test_rounds", testEvidence: "covering test",
			},
			wantCommand: "python -m pytest tests/test_mod.py -k test_rounds",
			wantTargets: "tests/test_mod.py",
			wantDerived: "pyproject.toml pytest config + covering test path",
		},
		{
			name: "rspec when the repository declares it",
			files: map[string]string{
				"Gemfile":            "",
				".rspec":             "--require spec_helper\n",
				"lib/thing.rb":       "",
				"spec/thing_spec.rb": "",
			},
			subject: searchVerifySubject{
				sourcePath: "lib/thing.rb",
				testPath:   "spec/thing_spec.rb", testEvidence: "mirror test file",
			},
			wantCommand: "bundle exec rspec spec/thing_spec.rb",
			wantTargets: "spec/thing_spec.rb",
			wantDerived: ".rspec + mirror test file path",
		},
		{
			name: "minitest single file when the tests live under test/",
			files: map[string]string{
				"Rakefile":                     "task :test\n",
				"lib/fluent/plugin/in_tail.rb": "",
				"test/plugin/test_in_tail.rb":  "",
			},
			subject: searchVerifySubject{
				sourcePath: "lib/fluent/plugin/in_tail.rb",
				testPath:   "test/plugin/test_in_tail.rb", testEvidence: "mirror test file",
			},
			wantCommand: "bundle exec ruby -Itest test/plugin/test_in_tail.rb",
			wantTargets: "test/plugin/test_in_tail.rb",
			wantDerived: "Rakefile + mirror test file path under test/",
		},
		{
			name: "a Makefile target is emitted and labelled as the whole suite",
			files: map[string]string{
				"Makefile":              "all:\n\tcc\n\ntest: all\n\t./runtest\n",
				"src/bitops.c":          "",
				"tests/unit/bitops.tcl": "",
			},
			subject: searchVerifySubject{
				sourcePath: "src/bitops.c",
				testPath:   "tests/unit/bitops.tcl", testEvidence: "covering test",
			},
			wantCommand: "make test",
			wantTargets: "tests/unit/bitops.tcl (whole suite; Makefile states no narrower target)",
			wantDerived: "Makefile target test",
		},
		{
			name: "a Makefile without a test target emits nothing",
			files: map[string]string{
				"Makefile":    "all:\n\tcc\n",
				"src/x.c":     "",
				"tests/x.tcl": "",
			},
			subject: searchVerifySubject{
				sourcePath: "src/x.c", testPath: "tests/x.tcl", testEvidence: "covering test",
			},
		},
		{
			name: "an unrecognized build system emits nothing",
			files: map[string]string{
				"build.xml":                  "<project/>",
				"CMakeLists.txt":             "enable_testing()\n",
				"src/core/lombok/Thing.java": "",
				"test/core/ThingTest.java":   "",
			},
			subject: searchVerifySubject{
				sourcePath: "src/core/lombok/Thing.java",
				testPath:   "test/core/ThingTest.java", testEvidence: "covering test",
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			evidence := searchVerifyTestEvidence(testCase.files)
			got := deriveSearchVerifyCommand(testCase.subject, &evidence)
			if testCase.wantCommand == "" {
				if got != nil {
					t.Fatalf("expected silence, got %q (from %s)", got.Command, got.DerivedFrom)
				}
				return
			}
			if got == nil {
				t.Fatal("expected a command, got silence")
			}
			if got.Command != testCase.wantCommand {
				t.Fatalf("command = %q, want %q", got.Command, testCase.wantCommand)
			}
			if got.Targets != testCase.wantTargets {
				t.Fatalf("targets = %q, want %q", got.Targets, testCase.wantTargets)
			}
			if got.DerivedFrom != testCase.wantDerived {
				t.Fatalf("derived_from = %q, want %q", got.DerivedFrom, testCase.wantDerived)
			}
			if searchVerifyCommandCost(got) > searchVerifyCommandMaxBytes {
				t.Fatalf("cost %d exceeds the cap %d", searchVerifyCommandCost(got), searchVerifyCommandMaxBytes)
			}
		})
	}
}

func TestSearchVerifyMirrorTestOnlyReturnsFilesThatExist(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		files  map[string]string
		source string
		want   string
	}{
		{
			name:   "go sibling",
			files:  map[string]string{"pkg/tree_test.go": ""},
			source: "pkg/tree.go",
			want:   "pkg/tree_test.go",
		},
		{
			name:   "javascript sibling with a dotted affix",
			files:  map[string]string{"hooks/src/index.test.js": ""},
			source: "hooks/src/index.js",
			want:   "hooks/src/index.test.js",
		},
		{
			name:   "a __tests__ directory beside the file",
			files:  map[string]string{"src/__tests__/reactive.spec.ts": ""},
			source: "src/reactive.ts",
			want:   "src/__tests__/reactive.spec.ts",
		},
		{
			name:   "the java parallel source tree",
			files:  map[string]string{"gson/src/test/java/com/google/gson/GsonBuilderTest.java": ""},
			source: "gson/src/main/java/com/google/gson/GsonBuilder.java",
			want:   "gson/src/test/java/com/google/gson/GsonBuilderTest.java",
		},
		{
			name:   "the php tests tree",
			files:  map[string]string{"tests/Calculation/RoundTest.php": ""},
			source: "src/Calculation/Round.php",
			want:   "tests/Calculation/RoundTest.php",
		},
		{
			name:   "nothing there is nothing returned",
			files:  map[string]string{"pkg/other_test.go": ""},
			source: "pkg/tree.go",
			want:   "",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			evidence := searchVerifyTestEvidence(testCase.files)
			if got := searchVerifyMirrorTest(testCase.source, &evidence); got != testCase.want {
				t.Fatalf("mirror = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestSearchVerifyTomlPackageName(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		content string
		want    string
	}{
		{name: "plain", content: "[package]\nname = \"ripgrep\"\n", want: "ripgrep"},
		{name: "single quotes", content: "[package]\nname = 'ripgrep'\n", want: "ripgrep"},
		{name: "another table's name is not the package's", content: "[dependencies]\nname = \"x\"\n", want: ""},
		{name: "inherited from the workspace", content: "[package]\nname = { workspace = true }\n", want: ""},
		{name: "workspace only", content: "[workspace]\nmembers = []\n", want: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, found := searchVerifyTomlPackageName(testCase.content)
			if testCase.want == "" {
				if found {
					t.Fatalf("expected no name, got %q", got)
				}
				return
			}
			if !found || got != testCase.want {
				t.Fatalf("name = %q/%v, want %q", got, found, testCase.want)
			}
		})
	}
}

func TestSearchMakefileHasTargetIgnoresRecipesAndVariables(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		content string
		target  string
		want    bool
	}{
		{name: "declared target", content: "test:\n\t./run\n", target: "test", want: true},
		{name: "target with prerequisites", content: "test: all\n\t./run\n", target: "test", want: true},
		{name: "a variable assignment is not a target", content: "test := 1\n", target: "test", want: false},
		{name: "a mention in a recipe is not a target", content: "all:\n\ttest x\n", target: "test", want: false},
		{name: "an indented line is not a target", content: "  test:\n", target: "test", want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := searchMakefileHasTarget(testCase.content, testCase.target); got != testCase.want {
				t.Fatalf("hasTarget = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestSearchVerifyTestNameRejectsUnusableFilters(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		result SearchResult
		want   string
	}{
		{name: "plain name", result: SearchResult{SymbolName: "TestBind"}, want: "TestBind"},
		{
			name:   "a qualified name is reduced to the member",
			result: SearchResult{QualifiedName: "GsonBuilderTest.testAdapter"},
			want:   "testAdapter",
		},
		{
			name:   "a name a shell would mangle is no filter at all",
			result: SearchResult{SymbolName: "test bind (ipv6)"},
			want:   "",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := searchVerifyTestName(testCase.result); got != testCase.want {
				t.Fatalf("test name = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestRenderSearchVerifyCommandIsOneCopyableLine(t *testing.T) {
	t.Parallel()
	rendered := string(RenderSearchVerifyCommand(&SearchVerifyCommand{
		Command:     "cargo test -p grep-printer --lib test_trim",
		Targets:     "crates/printer/src/util.rs",
		DerivedFrom: "Cargo.toml [package] name + covering test path",
	}))
	lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	// The COMMAND is still exactly one line, on the first line, so it stays copy-pasteable. The lines
	// below it are the contract on using it, and they are asserted separately.
	if lines[0] != "VERIFY: cargo test -p grep-printer --lib test_trim" {
		t.Fatalf("first line = %q", lines[0])
	}
	if !strings.Contains(lines[1], "crates/printer/src/util.rs") ||
		!strings.Contains(lines[1], "Cargo.toml [package] name") {
		t.Fatalf("second line must state target and derivation: %q", lines[1])
	}
	if len(lines) > 5 {
		t.Fatalf("VERIFY block grew past five lines, got %d:\n%s", len(lines), rendered)
	}
	// The three measured churn modes must each be named. Post-edit verify churn cost +$4.11 across
	// three sessions, and a hand-built classpath was 55.6% of one session's output tokens.
	contract := strings.Join(lines[2:], " ")
	for _, want := range []string{"ONCE", "re-run THIS command", "classpath", "reverting"} {
		if !strings.Contains(contract, want) {
			t.Fatalf("contract does not name %q:\n%s", want, rendered)
		}
	}
	if cost := searchVerifyCommandCost(&SearchVerifyCommand{
		Command:     "cargo test -p grep-printer --lib test_trim",
		Targets:     "crates/printer/src/util.rs",
		DerivedFrom: "Cargo.toml [package] name + covering test path",
	}); cost > searchVerifyCommandMaxBytes {
		t.Fatalf("VERIFY block costs %d bytes, over its own cap of %d", cost, searchVerifyCommandMaxBytes)
	}
}

// C/C++ had NO verify inference at all, and a missing VERIFY block is the largest measured effect in
// the benchmark: VERIFY present n=11 -> eg -35.3%; VERIFY absent n=16 -> eg -14.9%, with every
// >200k-token regression falling in the absent group. fmtlib/fmt-2457 was the only C++ instance and
// got nothing.
func TestCMakeVerifyIsSelfConfiguringAndTargetScoped(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"CMakeLists.txt":       "project(fmt)\nenable_testing()\nadd_subdirectory(test)\n",
		"test/CMakeLists.txt":  "add_fmt_test(ranges-test)\n",
		"include/fmt/ranges.h": "template <typename T> struct formatter {};\n",
		"test/ranges-test.cc":  "TEST(ranges_test, join_tuple) {}\n",
	}
	results := []SearchResult{
		{Rank: 1, FilePath: "include/fmt/ranges.h", SymbolName: "formatter.format_args", Section: searchSectionPrimary},
		{Rank: 2, FilePath: "test/ranges-test.cc", Section: searchSectionPrimary},
	}
	got := buildSearchVerifyCommand(results, searchVerifyTestEvidence(files))
	if got == nil {
		t.Fatal("expected a CMake command for a project declaring enable_testing()")
	}
	// The configure step must be IN the string: a bare `cmake --build build` fails in a fresh
	// checkout, which is what cost fmt a cold build plus five turns re-finding the directory.
	for _, want := range []string{"cmake -S . -B build", "--target ranges-test", "ctest --test-dir build -R ranges-test"} {
		if !strings.Contains(got.Command, want) {
			t.Fatalf("command missing %q:\n%s", want, got.Command)
		}
	}
	// No enable_testing() -> NEVER an invented ctest run. The CMake derivation declines, and since
	// TestBuildSearchVerifyCommandFallsBackToTheResidualFloor the ladder answers with the residual
	// floor instead of silence: the assertion that matters here is that nothing was guessed.
	quiet := map[string]string{
		"CMakeLists.txt":      "project(fmt)\n",
		"test/CMakeLists.txt": "add_fmt_test(ranges-test)\n",
		"test/ranges-test.cc": "",
	}
	assertNoInventedCTest(t, buildSearchVerifyCommand(results, searchVerifyTestEvidence(quiet)))
	// A target the CMake sources never declare -> the floor rather than a guess.
	undeclared := map[string]string{
		"CMakeLists.txt":      "project(fmt)\nenable_testing()\n",
		"test/CMakeLists.txt": "add_fmt_test(other-test)\n",
		"test/ranges-test.cc": "",
	}
	assertNoInventedCTest(t, buildSearchVerifyCommand(results, searchVerifyTestEvidence(undeclared)))
}

// assertNoInventedCTest pins the CMake derivation's real contract: a TARGET the sources never
// declare may never be manufactured. What the ladder answers with instead is deliberately not
// pinned here — the suite tier (`ctest` over whatever `enable_testing()` declared) and the residual
// floor are both honest answers derived from evidence; `-R ranges-test` against sources that never
// declared ranges-test is not.
func assertNoInventedCTest(t *testing.T, got *SearchVerifyCommand) {
	t.Helper()
	if got == nil {
		return
	}
	if got.Tier == searchVerifyTierNarrow || strings.Contains(got.Command, "-R ranges-test") ||
		strings.Contains(got.Command, "--target ranges-test") {
		t.Fatalf("a target-scoped cmake/ctest command was invented from insufficient evidence: %q", got.Command)
	}
}

// Sessions whose payload carried no VERIFY block spent 15.2% fewer tokens than the no-tool baseline
// against 30.6% for sessions that had one, and every test derivation requires a covering test, so
// 16 of 30 measured sessions got nothing. The build check closes that gap without pretending to be
// a test run.
func TestDeriveSearchVerifyBuildCheckFiresWhenNoCoveringTestExists(t *testing.T) {
	t.Parallel()
	evidence := searchVerifyTestEvidence(map[string]string{
		"composer.json":          "{\"require\":{}}\n",
		"src/Illuminate/Bus.php": "<?php\n",
	})
	results := []SearchResult{
		{Rank: 1, FilePath: "src/Illuminate/Bus.php", SymbolName: "Bus::dispatch", Section: searchSectionPrimary},
	}
	got := buildSearchVerifyCommand(results, evidence)
	if got == nil {
		t.Fatal("expected a build check when no covering test exists")
	}
	if want := "php -l src/Illuminate/Bus.php"; got.Command != want {
		t.Fatalf("command:\n got %q\nwant %q", got.Command, want)
	}
	if !strings.Contains(got.DerivedFrom, "runs no tests") {
		t.Fatalf("derivation must say it is not a test run, got %q", got.DerivedFrom)
	}
}

// The build check is a fallback, never a replacement: a payload that found a real covering test must
// still emit the test command, or the fix would trade behaviour coverage for a parse check.
func TestDeriveSearchVerifyBuildCheckYieldsToARealTestCommand(t *testing.T) {
	t.Parallel()
	evidence := searchVerifyTestEvidence(map[string]string{
		"go.mod":               "module x\n",
		"internal/a/a.go":      "package a\n",
		"internal/a/a_test.go": "package a\n\nfunc TestDispatch(t *testing.T) {}\n",
	})
	results := []SearchResult{
		{Rank: 1, FilePath: "internal/a/a.go", SymbolName: "Dispatch", Section: searchSectionPrimary},
		{Rank: 2, FilePath: "internal/a/a_test.go", Section: searchSectionPrimary},
	}
	got := buildSearchVerifyCommand(results, evidence)
	if got == nil {
		t.Fatal("expected a command")
	}
	if strings.Contains(got.Command, "py_compile") || strings.Contains(got.DerivedFrom, "runs no tests") {
		t.Fatalf("build check displaced a real test command: %q (%s)", got.Command, got.DerivedFrom)
	}
}

// A language with no safe single-file check must stay silent rather than guess an invocation: javac
// needs a classpath and gcc needs include paths, and a command that fails on its own invocation is
// the failure mode the whole block is designed to avoid.
func TestDeriveSearchVerifyBuildCheckStaysSilentWithoutASafeCheck(t *testing.T) {
	t.Parallel()
	evidence := searchVerifyTestEvidence(map[string]string{
		"pom.xml":           "<project></project>\n",
		"src/main/App.java": "class App {}\n",
	})
	results := []SearchResult{
		{Rank: 1, FilePath: "src/main/App.java", SymbolName: "App.run", Section: searchSectionPrimary},
	}
	// Maven legitimately emits a whole-suite `mvn -q test` here without a covering test; what must
	// not happen is a fabricated per-file Java check, which has no safe single-file form.
	got := buildSearchVerifyCommand(results, evidence)
	if got != nil && strings.Contains(got.DerivedFrom, "runs no tests") {
		t.Fatalf("build check fabricated a Java command: %q", got.Command)
	}
	if got != nil && strings.Contains(got.Command, "App.java") {
		t.Fatalf("emitted a per-file Java compile command: %q", got.Command)
	}
}

// Two models wanted opposite things from a command whose runner is missing: suppressing it moved
// Sonnet -40.7% -> -25.5% (helped) and Haiku -42.5% -> -49.5% (hurt). One caller wants something to
// run, the other wants to be told to stop. The block serves both by emitting the command AND flagging
// the runner — no branch, no model named.
func TestSearchVerifyAnnotatesAMissingRunnerInsteadOfSuppressing(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"go.mod":               "module x\n",
		"internal/a/a.go":      "package a\n",
		"internal/a/a_test.go": "package a\n\nfunc TestDispatch(t *testing.T) {}\n",
	}
	results := []SearchResult{
		{Rank: 1, FilePath: "internal/a/a.go", SymbolName: "Dispatch", Section: searchSectionPrimary},
		{Rank: 2, FilePath: "internal/a/a_test.go", Section: searchSectionPrimary},
	}
	present := searchVerifyTestEvidenceWithout(files)
	got := buildSearchVerifyCommand(results, present)
	if got == nil {
		t.Fatal("expected a command when the runner is present")
	}
	if got.RunnerMissing {
		t.Fatal("runner is on PATH; must not be flagged missing")
	}
	if strings.Contains(string(RenderSearchVerifyCommand(got)), "not installed") {
		t.Fatal("no note should render when the runner resolves")
	}

	absent := searchVerifyTestEvidenceWithout(files, "go")
	got = buildSearchVerifyCommand(results, absent)
	if got == nil {
		t.Fatal("the command must still be EMITTED when the runner is missing, not suppressed")
	}
	if !got.RunnerMissing {
		t.Fatal("expected RunnerMissing to be set when `go` is absent")
	}
	rendered := string(RenderSearchVerifyCommand(got))
	if !strings.Contains(rendered, "go test ./internal/a") {
		t.Fatalf("command must still be present and runnable-as-written: %q", rendered)
	}
	if !strings.Contains(rendered, "do NOT go looking for a toolchain") {
		t.Fatalf("expected the stop-hunting note, got %q", rendered)
	}
}

// The launcher forms resolve their tool themselves, so probing only the launcher misses the cases that
// cost the most turns: `bundle exec rspec` with bundle installed but rspec absent from the lockfile.
func TestSearchVerifyRunnerMissingLooksThroughLaunchers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		command string
		files   map[string]string
		want    bool
	}{
		{"bundler resolves the gem", "bundle exec rspec spec/a_spec.rb",
			map[string]string{"Gemfile.lock": "GEM\n  specs:\n    rspec-core (3.13.0)\n"}, false},
		{"bundler cannot resolve the gem", "bundle exec rspec spec/a_spec.rb",
			map[string]string{"Gemfile.lock": "GEM\n  specs:\n    rake (13.0.6)\n"}, true},
		{"npx finds the local binary", "npx jest src/a.test.ts",
			map[string]string{"node_modules/.bin/jest": "#!/usr/bin/env node\n"}, false},
		{"npx has nothing to run", "npx jest src/a.test.ts", map[string]string{}, true},
		{"vendored binary present", "vendor/bin/phpunit --filter A",
			map[string]string{"vendor/bin/phpunit": "#!/usr/bin/env php\n"}, false},
		{"vendored binary absent", "vendor/bin/phpunit --filter A", map[string]string{}, true},
		{"cd prefix does not confuse the runner", "cd fuzz && cargo test", map[string]string{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			evidence := searchVerifyTestEvidenceWithout(tc.files)
			if got := searchVerifyRunnerMissing(tc.command, &evidence); got != tc.want {
				t.Fatalf("searchVerifyRunnerMissing(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

// A generic suffix must not out-rank the specific match. Measured on caddyserver/caddy-4943:
// `CookieFilter.Filter` yields {cookie, filter}; `TestHashFilter` matches only the generic `filter`
// and is shorter than `TestCookieFilter`, so a shortest-name tie-break emitted a command exercising an
// unrelated filter. The instance regressed on BOTH Haiku (+6.4% usd) and Sonnet (+75.3% usd).
func TestDeriveSearchVerifyPrefersTheTestMatchingMostOfTheSymbol(t *testing.T) {
	t.Parallel()
	evidence := searchVerifyTestEvidence(map[string]string{
		"go.mod": "module caddy\n",
		// TestHashFilter is declared FIRST and is SHORTER, so it wins on both old tie-breaks.
		"modules/logging/filters_test.go": "package logging\n\n" +
			"func TestHashFilter(t *testing.T) {}\n\n" +
			"func TestCookieFilter(t *testing.T) {}\n",
	})
	results := []SearchResult{
		{Rank: 1, FilePath: "modules/logging/filters.go", SymbolName: "CookieFilter.Filter", Section: searchSectionPrimary},
		{Rank: 2, FilePath: "modules/logging/filters_test.go", Section: searchSectionPrimary},
	}
	got := buildSearchVerifyCommand(results, evidence)
	if got == nil {
		t.Fatal("expected a command")
	}
	if !strings.Contains(got.Command, "TestCookieFilter") {
		t.Fatalf("expected the test matching BOTH words of CookieFilter.Filter, got %q", got.Command)
	}
}

// Length must remain the tie-break WITHIN an equal match count, so a narrow test still beats a
// long variant of the same behaviour.
func TestDeriveSearchVerifyStillPrefersTheShorterNameAtEqualCoverage(t *testing.T) {
	t.Parallel()
	evidence := searchVerifyTestEvidence(map[string]string{
		"go.mod": "module x\n",
		"internal/a/a_test.go": "package a\n\n" +
			"func TestDispatchWithUnrelatedOptionAndTimeout(t *testing.T) {}\n\n" +
			"func TestDispatch(t *testing.T) {}\n",
	})
	results := []SearchResult{
		{Rank: 1, FilePath: "internal/a/a.go", SymbolName: "Dispatch", Section: searchSectionPrimary},
		{Rank: 2, FilePath: "internal/a/a_test.go", Section: searchSectionPrimary},
	}
	got := buildSearchVerifyCommand(results, evidence)
	if got == nil {
		t.Fatal("expected a command")
	}
	if !strings.Contains(got.Command, "'^TestDispatch$'") {
		t.Fatalf("expected the shorter equal-coverage name, got %q", got.Command)
	}
}

func TestBuildSearchVerifyCommandFallsBackToTheResidualFloor(t *testing.T) {
	t.Parallel()
	evidence := searchVerifyTestEvidence(map[string]string{"go.mod": "module x\n"})
	// SUPERSEDES the previous expectation of SILENCE for a payload with no candidate fix site.
	// Silent absence is indistinguishable from a deriver bug and, paired with the stop-early doctrine,
	// lets an agent ship unverified — measured as a non-derivable VERIFY in 12 of 12 sessions. The
	// residual floor states the fact and prescribes the fallback in one line.
	docsOnly := []SearchResult{{Rank: 1, FilePath: "docs/guide.md", Section: searchSectionDocs}}
	command := buildSearchVerifyCommand(docsOnly, evidence)
	if command == nil {
		t.Fatal("no VERIFY block at all — the residual floor must make this impossible")
	}
	if command.Tier != searchVerifyTierNone {
		t.Fatalf("tier = %q, want %q", command.Tier, searchVerifyTierNone)
	}
	if command.Command != searchVerifyNoneCommand {
		t.Fatalf("command = %q, want the residual floor", command.Command)
	}
	// The floor carries no contract note: the note is advice about running a command, and there is
	// no command to run.
	rendered := string(RenderSearchVerifyCommand(command))
	if strings.Contains(rendered, "run it ONCE") {
		t.Fatalf("the floor carried the contract note:\n%s", rendered)
	}
	if !strings.HasPrefix(rendered, "VERIFY: ") {
		t.Fatalf("the line start must stay byte-identical:\n%s", rendered)
	}
}

// TestBuildSearchVerifyCommandWithholdsAnUnprintableCommand pins the gate that
// keeps "runnable exactly as written" true. A command whose path holds a byte the
// renderer escapes is neither runnable as shown nor honest about not being, so it
// is withheld and the residual floor answers instead.
//
// The C1 cases are the ones that regressed: the gate scanned for C0 and DEL while
// the renderer escapes C1 as well, so a path carrying a raw 0x9b passed the gate,
// was emitted as runnable, and was then rewritten on the way to the terminal. It
// asks termsafe now, so the two cannot drift apart again.
func TestBuildSearchVerifyCommandWithholdsAnUnprintableCommand(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct{ name, directory string }{
		{"ESC", "internal/con\x1bfigs"},
		{"a raw C1 byte", "internal/con\x9bfigs"},
		{"C1 in its two-byte UTF-8 form", "internal/con\xc2\x9bfigs"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			evidence := searchVerifyTestEvidence(map[string]string{
				"go.mod":                               "module x\n",
				testCase.directory + "/parser_test.go": "package configs\n\nfunc TestParse(t *testing.T) {}\n",
			})
			results := []SearchResult{
				{Rank: 1, FilePath: testCase.directory + "/parser.go", SymbolName: "Parse", Section: searchSectionPrimary},
				{Rank: 2, FilePath: testCase.directory + "/parser_test.go", Section: searchSectionPrimary},
			}
			command := buildSearchVerifyCommand(results, evidence)
			if command == nil {
				t.Fatal("no VERIFY block at all — the residual floor must make this impossible")
			}
			if command.Command != searchVerifyNoneCommand {
				t.Fatalf("emitted a command display would have rewritten: %q", command.Command)
			}
			rendered := string(RenderSearchVerifyCommand(command))
			if strings.IndexByte(rendered, 0x1b) >= 0 || strings.IndexByte(rendered, 0x9b) >= 0 {
				t.Fatalf("the rendered VERIFY block carried a control byte: %q", rendered)
			}
		})
	}
}

func TestSearchVerifySuiteFallback(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		files       map[string]string
		subject     searchVerifySubject
		wantCommand string
	}{
		{
			name: "ruby minitest repo with no covering test falls back to rake test",
			files: map[string]string{
				"Gemfile":                       "source 'https://rubygems.org'\n",
				"Rakefile":                      "task default: %w[test rubocop]\n",
				"lib/faker/default/internet.rb": "module Faker\nend\n",
			},
			subject:     searchVerifySubject{sourcePath: "lib/faker/default/internet.rb"},
			wantCommand: "bundle exec rake test",
		},
		{
			name: "ruby rspec repo falls back to bundle exec rspec",
			files: map[string]string{
				"Gemfile":      "source 'x'\n",
				".rspec":       "--require spec_helper\n",
				"lib/thing.rb": "class Thing; end\n",
			},
			subject:     searchVerifySubject{sourcePath: "lib/thing.rb"},
			wantCommand: "bundle exec rspec",
		},
		{
			name: "node repo with a test script falls back to npm test",
			files: map[string]string{
				"package.json": "{\"scripts\":{\"test\":\"jest\"}}",
				"src/a.js":     "",
			},
			subject:     searchVerifySubject{sourcePath: "src/a.js"},
			wantCommand: "npm test",
		},
		{
			name: "node leaf default test script defers to root runner",
			files: map[string]string{
				"package.json":              "{\"scripts\":{\"test\":\"jest\"}}",
				"packages/ui/package.json":  "{\"scripts\":{\"test\":\"echo \\\"Error: no test specified\\\" && exit 1\"}}",
				"packages/ui/src/Button.js": "",
			},
			subject:     searchVerifySubject{sourcePath: "packages/ui/src/Button.js"},
			wantCommand: "npm test",
		},
		{
			name: "node package with an unrecognized test script stays silent",
			files: map[string]string{
				"packages/ui/package.json":  "{\"scripts\":{\"test\":\"custom-test\"}}",
				"packages/ui/src/Button.js": "",
			},
			subject:     searchVerifySubject{sourcePath: "packages/ui/src/Button.js"},
			wantCommand: "",
		},
		{
			name: "gradle nested module uses root wrapper",
			files: map[string]string{
				"gradlew":                  "",
				"lib/build.gradle":         "",
				"lib/src/main/kotlin/A.kt": "",
			},
			subject:     searchVerifySubject{sourcePath: "lib/src/main/kotlin/A.kt"},
			wantCommand: "./gradlew test",
		},
		{
			name: "gradle nested wrapper runs from module directory",
			files: map[string]string{
				"lib/gradlew":              "",
				"lib/build.gradle":         "",
				"lib/src/main/kotlin/A.kt": "",
			},
			subject:     searchVerifySubject{sourcePath: "lib/src/main/kotlin/A.kt"},
			wantCommand: "cd lib && ./gradlew test",
		},
		{
			name: "an unrecognized build system stays silent",
			files: map[string]string{
				"CMakeLists.txt": "project(x)\n",
				"src/a.c":        "",
			},
			subject:     searchVerifySubject{sourcePath: "src/a.c"},
			wantCommand: "",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			evidence := searchVerifyTestEvidence(testCase.files)
			got := deriveSearchVerifySuiteCommand(testCase.subject, &evidence)
			if testCase.wantCommand == "" {
				if got != nil {
					t.Fatalf("expected silence, got %q", got.Command)
				}
				return
			}
			if got == nil {
				t.Fatal("expected a whole-suite command, got silence")
			}
			if got.Command != testCase.wantCommand {
				t.Fatalf("command = %q, want %q", got.Command, testCase.wantCommand)
			}
			if !strings.Contains(got.Targets, "whole test suite") {
				t.Fatalf("targets must flag the whole suite, got %q", got.Targets)
			}
			if searchVerifyCommandCost(got) > searchVerifyCommandMaxBytes {
				t.Fatalf("cost %d exceeds the cap %d", searchVerifyCommandCost(got), searchVerifyCommandMaxBytes)
			}
		})
	}
}

// TestBuildSearchVerifyCommandPrefersNarrowOverSuite guards the ordering: a narrow single-file
// command always wins over the whole-suite fallback when a covering test exists.

func TestBuildSearchVerifyCommandPrefersNarrowOverSuite(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"Gemfile":            "source 'x'\n",
		"Rakefile":           "task default: %w[test]\n",
		"lib/thing.rb":       "class Thing; end\n",
		"test/thing_test.rb": "",
	}
	results := []SearchResult{
		{Rank: 1, FilePath: "lib/thing.rb", Section: searchSectionPrimary},
		{Rank: 2, FilePath: "test/thing_test.rb", Section: searchSectionCoveringTest},
	}
	got := buildSearchVerifyCommand(results, searchVerifyTestEvidence(files))
	if got == nil {
		t.Fatal("expected a command")
	}
	if !strings.Contains(got.Command, "ruby -Itest") {
		t.Fatalf("expected the narrow single-file command, got %q", got.Command)
	}
}

func TestShellQuotePreservesSafeTokensAndQuotesUnsafeTokens(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "simple path stays byte-identical", input: "src/file.py", want: "src/file.py"},
		{name: "cargo package stays byte-identical", input: "grep-printer", want: "grep-printer"},
		{name: "gradle task stays byte-identical", input: ":lib:test", want: ":lib:test"},
		{name: "maven property stays byte-identical", input: "-Dtest=GsonBuilderTest", want: "-Dtest=GsonBuilderTest"},
		{name: "go recursive selector stays byte-identical", input: "./...", want: "./..."},
		{name: "empty token is retained", input: "", want: "''"},
		{
			name:  "semicolon",
			input: "evil; touch pwned; #.py",
			want:  "'evil; touch pwned; #.py'",
		},
		{
			name:  "apostrophe",
			input: "file's_name.py",
			want:  "'file'\\''s_name.py'",
		},
		{name: "dollar expansion", input: "file$var.py", want: "'file$var.py'"},
		{name: "backticks", input: "file`cmd`.py", want: "'file`cmd`.py'"},
		{name: "command substitution", input: "file$(cmd).py", want: "'file$(cmd).py'"},
		{name: "space", input: "my file.py", want: "'my file.py'"},
		{name: "newline", input: "first\nsecond", want: "'first\nsecond'"},
		{name: "non ASCII", input: "café.py", want: "'café.py'"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := shellQuote(testCase.input)
			if got != testCase.want {
				t.Fatalf("shellQuote(%q) = %q, want %q", testCase.input, got, testCase.want)
			}
		})
	}
}

func TestDeriveSearchVerifyBuildCheckQuotesFilePath(t *testing.T) {
	t.Parallel()
	const sourcePath = "evil; touch pwned; #.py"
	evidence := searchVerifyTestEvidence(map[string]string{sourcePath: "# malicious file\n"})
	got := deriveSearchVerifyBuildCheck("", searchVerifySubject{sourcePath: sourcePath}, &evidence)
	if got == nil {
		t.Fatal("expected a build check command")
	}
	if want := "python -m py_compile 'evil; touch pwned; #.py'"; got.Command != want {
		t.Fatalf("command = %q, want %q", got.Command, want)
	}
}

func TestShellQuoteRoundTripsThroughPOSIXShell(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell quoting is exercised on non-Windows platforms")
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell is not installed")
	}
	for _, input := range []string{
		"plain-token",
		"two words",
		"it's still one token",
		"$(printf injected)",
		"x; printf injected",
		"first\nsecond",
	} {
		input := input
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			output, err := exec.Command(shell, "-c", "printf %s "+shellQuote(input)).Output()
			if err != nil {
				t.Fatalf("shell failed to parse %q: %v", shellQuote(input), err)
			}
			if got := string(output); got != input {
				t.Fatalf("shell decoded %q as %q, want %q", shellQuote(input), got, input)
			}
		})
	}
}

// TestSearchVerifyCommandsDoNotExecuteRepositoryData exercises the actual command strings through
// the same POSIX-shell boundary used by `entire graph verify`. Every runner is a no-op stub. If any
// repository-derived token becomes shell syntax, the redirection in attack creates VERIFY_MARKER.
func TestSearchVerifyCommandsDoNotExecuteRepositoryData(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell quoting is exercised on non-Windows platforms")
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell is not installed")
	}
	const attack = `x;>"$VERIFY_MARKER";#`
	const unquotedAttack = `x;>${VERIFY_MARKER};#`

	testCases := []struct {
		name    string
		derive  func() *SearchVerifyCommand
		rejects string
	}{
		{
			name: "working directory",
			derive: func() *SearchVerifyCommand {
				evidence := searchVerifyTestEvidence(map[string]string{
					attack + "/go.mod":   "module example.com/unsafe\n",
					attack + "/pkg/x.go": "package pkg\n",
				})
				return deriveSearchVerifyGo(attack, searchVerifySubject{sourcePath: attack + "/pkg/x.go"}, &evidence)
			},
		},
		{
			name: "cargo package",
			derive: func() *SearchVerifyCommand {
				evidence := searchVerifyTestEvidence(map[string]string{
					"Cargo.toml":       "[workspace]\nmembers = [\"crate\"]\n",
					"crate/Cargo.toml": "[package]\nname = \"" + attack + "\"\n",
					"crate/src/lib.rs": "",
				})
				return deriveSearchVerifyCargo("crate", searchVerifySubject{
					sourcePath: "crate/src/lib.rs", testPath: "crate/src/lib.rs",
				}, &evidence)
			},
		},
		{
			name: "cargo integration target",
			derive: func() *SearchVerifyCommand {
				evidence := searchVerifyTestEvidence(map[string]string{
					"Cargo.toml":                    "[workspace]\nmembers = [\"crate\"]\n",
					"crate/Cargo.toml":              "[package]\nname = \"safe\"\n",
					"crate/src/lib.rs":              "",
					"crate/tests/" + attack + ".rs": "",
				})
				return deriveSearchVerifyCargo("crate", searchVerifySubject{
					sourcePath: "crate/src/lib.rs", testPath: "crate/tests/" + attack + ".rs",
				}, &evidence)
			},
		},
		{
			name: "cargo test filter",
			derive: func() *SearchVerifyCommand {
				evidence := searchVerifyTestEvidence(map[string]string{
					"Cargo.toml":       "[workspace]\nmembers = [\"crate\"]\n",
					"crate/Cargo.toml": "[package]\nname = \"safe\"\n",
					"crate/src/lib.rs": "",
				})
				return deriveSearchVerifyCargo("crate", searchVerifySubject{
					sourcePath: "crate/src/lib.rs", testPath: "crate/src/lib.rs", testName: attack,
				}, &evidence)
			},
		},
		{
			name: "cargo feature",
			derive: func() *SearchVerifyCommand {
				testFile := "#[cfg(feature = \"" + unquotedAttack + "\")]\n#[test]\nfn test_safe() {}\n"
				evidence := searchVerifyTestEvidence(map[string]string{
					"Cargo.toml":       "[workspace]\nmembers = [\"crate\"]\n",
					"crate/Cargo.toml": "[package]\nname = \"safe\"\n[features]\n" + unquotedAttack + " = []\n",
					"crate/src/lib.rs": testFile,
				})
				return deriveSearchVerifyCargo("crate", searchVerifySubject{
					sourcePath: "crate/src/lib.rs", testPath: "crate/src/lib.rs", testName: "test_safe",
				}, &evidence)
			},
		},
		{
			name: "go package selector",
			derive: func() *SearchVerifyCommand {
				evidence := searchVerifyTestEvidence(map[string]string{"go.mod": "module example.com/unsafe\n"})
				return deriveSearchVerifyGo("", searchVerifySubject{sourcePath: attack + "/x.go"}, &evidence)
			},
		},
		{
			name: "go run filter",
			derive: func() *SearchVerifyCommand {
				evidence := searchVerifyTestEvidence(map[string]string{"go.mod": "module example.com/unsafe\n"})
				return deriveSearchVerifyGo("", searchVerifySubject{
					sourcePath: "pkg/x.go", testPath: "pkg/x_test.go", testName: "Test" + attack,
				}, &evidence)
			},
		},
		{
			name: "maven module",
			derive: func() *SearchVerifyCommand {
				evidence := searchVerifyTestEvidence(map[string]string{attack + "/pom.xml": "<project/>"})
				return deriveSearchVerifyMaven(attack, searchVerifySubject{sourcePath: attack + "/src/X.java"}, &evidence)
			},
		},
		{
			name: "maven test property",
			derive: func() *SearchVerifyCommand {
				evidence := searchVerifyTestEvidence(map[string]string{"pom.xml": "<project/>"})
				return deriveSearchVerifyMaven("", searchVerifySubject{
					sourcePath: "src/main/X.java", testPath: "src/test/" + attack + ".java",
				}, &evidence)
			},
		},
		{
			name: "gradle project task",
			derive: func() *SearchVerifyCommand {
				evidence := searchVerifyTestEvidence(map[string]string{
					"gradlew": "", attack + "/build.gradle": "",
				})
				return deriveSearchVerifyGradle(attack, searchVerifySubject{
					sourcePath: attack + "/src/A.kt", testPath: attack + "/src/ATest.kt",
				}, &evidence)
			},
		},
		{
			name: "gradle test pattern",
			derive: func() *SearchVerifyCommand {
				evidence := searchVerifyTestEvidence(map[string]string{"gradlew": "", "lib/build.gradle": ""})
				return deriveSearchVerifyGradle("lib", searchVerifySubject{
					sourcePath: "lib/src/A.kt", testPath: "lib/src/" + attack + ".kt",
				}, &evidence)
			},
		},
		{
			name: "node test path",
			derive: func() *SearchVerifyCommand {
				evidence := searchVerifyTestEvidence(map[string]string{
					"package.json": `{"devDependencies":{"jest":"1"}}`,
				})
				return deriveSearchVerifyNode("", searchVerifySubject{testPath: "test/" + attack + ".js"}, &evidence)
			},
		},
		{
			name: "composer test path",
			derive: func() *SearchVerifyCommand {
				evidence := searchVerifyTestEvidence(map[string]string{
					"composer.json": "{}", "phpunit.xml": "<phpunit/>",
				})
				return deriveSearchVerifyComposer("", searchVerifySubject{testPath: "test/" + attack + ".php"}, &evidence)
			},
		},
		{
			name: "pytest test path",
			derive: func() *SearchVerifyCommand {
				evidence := searchVerifyTestEvidence(map[string]string{"pytest.ini": "[pytest]\n"})
				return deriveSearchVerifyPytest("", searchVerifySubject{testPath: "test/" + attack + ".py"}, &evidence)
			},
		},
		{
			name: "pytest name filter",
			derive: func() *SearchVerifyCommand {
				evidence := searchVerifyTestEvidence(map[string]string{"pytest.ini": "[pytest]\n"})
				return deriveSearchVerifyPytest("", searchVerifySubject{
					testPath: "test/safe.py", testName: attack,
				}, &evidence)
			},
		},
		{
			name: "ruby test path",
			derive: func() *SearchVerifyCommand {
				evidence := searchVerifyTestEvidence(map[string]string{"Gemfile": "", ".rspec": ""})
				return deriveSearchVerifyRuby("", searchVerifySubject{testPath: "spec/" + attack + ".rb"}, &evidence)
			},
		},
		{
			name: "build check path",
			derive: func() *SearchVerifyCommand {
				evidence := searchVerifyTestEvidence(map[string]string{attack + ".py": ""})
				return deriveSearchVerifyBuildCheck("", searchVerifySubject{sourcePath: attack + ".py"}, &evidence)
			},
		},
		{
			name: "cmake target",
			derive: func() *SearchVerifyCommand {
				evidence := searchVerifyTestEvidence(map[string]string{
					"CMakeLists.txt": "enable_testing()\nadd_executable(" + attack + " test/x.cc)\n",
				})
				return deriveSearchVerifyCMake("", searchVerifySubject{testPath: "test/" + attack + ".cc"}, &evidence)
			},
		},
		{
			name: "cmake rejects a non-identifier define",
			derive: func() *SearchVerifyCommand {
				evidence := searchVerifyTestEvidence(map[string]string{
					"CMakeLists.txt":    "enable_testing()\nadd_executable(safe-test test/safe-test.cc)\n",
					"test/safe-test.cc": "#ifdef " + unquotedAttack + "\nTEST(suite, safe_case) {}\n#endif\n",
				})
				return deriveSearchVerifyCMake("", searchVerifySubject{
					testPath: "test/safe-test.cc", testName: "safe_case",
				}, &evidence)
			},
			rejects: "$VERIFY_MARKER",
		},
	}

	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	for _, name := range []string{"bundle", "cargo", "cmake", "ctest", "go", "mvn", "npx", "python"} {
		writeSearchVerifyShellStub(t, filepath.Join(binDir, name))
	}
	writeSearchVerifyShellStub(t, filepath.Join(root, "gradlew"))
	writeSearchVerifyShellStub(t, filepath.Join(root, "vendor", "bin", "phpunit"))
	if err := os.MkdirAll(filepath.Join(root, attack), 0o755); err != nil {
		t.Fatalf("create literal malicious directory: %v", err)
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			marker := filepath.Join(root, "VERIFY_MARKER")
			if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
				t.Fatalf("clear marker: %v", err)
			}
			command := testCase.derive()
			if command == nil {
				t.Fatal("fixture did not derive a command")
			}
			if testCase.rejects != "" && strings.Contains(command.Command, testCase.rejects) {
				t.Fatalf("rejected repository syntax reached the command: %q", command.Command)
			}
			process := exec.Command(shell, "-c", command.Command)
			process.Dir = root
			process.Env = searchVerifyShellTestEnv(binDir, marker)
			if output, err := process.CombinedOutput(); err != nil {
				t.Fatalf("command %q failed: %v\n%s", command.Command, err, output)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatalf("repository data executed as shell syntax: %q", command.Command)
			} else if !os.IsNotExist(err) {
				t.Fatalf("inspect marker: %v", err)
			}
		})
	}
}

func writeSearchVerifyShellStub(t *testing.T, filePath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatalf("create stub directory: %v", err)
	}
	if err := os.WriteFile(filePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write shell stub: %v", err)
	}
}

func searchVerifyShellTestEnv(binDir, marker string) []string {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, variable := range os.Environ() {
		if strings.HasPrefix(variable, "PATH=") || strings.HasPrefix(variable, "VERIFY_MARKER=") {
			continue
		}
		environment = append(environment, variable)
	}
	return append(environment, "PATH="+binDir, "VERIFY_MARKER="+marker)
}

// TestSearchVerifyCommandsNeutralizeOptionShapedPaths covers the half of the threat model that
// shell quoting does not reach. A repository path may begin with a dash, and quoting it keeps the
// SHELL honest while leaving the invoked tool to read it as options: `python -m pytest
// -weird/test_app.py` hands pytest -w, -e, -i, -r, -d rather than a file. Every derivation that
// interpolates a repo-relative path routes it through shellQuotePath, which prefixes `./`.
func TestSearchVerifyCommandsNeutralizeOptionShapedPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		files   map[string]string
		derive  func(string, searchVerifySubject, *searchVerifyEvidence) *SearchVerifyCommand
		subject searchVerifySubject
		want    string
	}{
		{
			name:    "pytest path",
			files:   map[string]string{"pytest.ini": "[pytest]\n", "-weird/test_app.py": "def test_app():\n    pass\n"},
			derive:  deriveSearchVerifyPytest,
			subject: searchVerifySubject{sourcePath: "-weird/app.py", testPath: "-weird/test_app.py", testEvidence: "ranked test"},
			want:    "python -m pytest ./-weird/test_app.py",
		},
		{
			name:    "phpunit path",
			files:   map[string]string{"composer.json": "{}", "phpunit.xml": "<phpunit/>", "-src/AppTest.php": "<?php\n"},
			derive:  deriveSearchVerifyComposer,
			subject: searchVerifySubject{sourcePath: "-src/App.php", testPath: "-src/AppTest.php", testEvidence: "ranked test"},
			want:    "vendor/bin/phpunit ./-src/AppTest.php",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			evidence := searchVerifyTestEvidence(test.files)
			got := test.derive("", test.subject, &evidence)
			if got == nil {
				t.Fatalf("no command derived for %+v", test.subject)
			}
			if !strings.Contains(got.Command, test.want) {
				t.Errorf("command = %q, want it to contain %q", got.Command, test.want)
			}
		})
	}
}

// TestSearchVerifyRunInNeutralizesOptionShapedDir pins the `cd` half: a module directory whose
// name starts with a dash is read by cd as its own options, so the command would never enter it.
func TestSearchVerifyRunInNeutralizesOptionShapedDir(t *testing.T) {
	t.Parallel()
	if got := searchVerifyRunIn("-weird", "go test ./..."); !strings.HasPrefix(got, "cd ./-weird && ") {
		t.Errorf("searchVerifyRunIn = %q, want it to cd into ./-weird", got)
	}
	if plain := searchVerifyRunIn("sub", "go test ./..."); plain != "cd sub && go test ./..." {
		t.Errorf("ordinary directory changed shape: %q", plain)
	}
}
