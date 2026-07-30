package sem

import (
	"strings"
	"testing"
)

// searchVerifyTestEvidence builds an evidence view over a fixed set of repository files.
func searchVerifyTestEvidence(files map[string]string) searchVerifyEvidence {
	return searchVerifyEvidence{read: func(path string) (string, bool) {
		content, ok := files[path]
		return content, ok
	}}
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
				"composer.json":                   `{}`,
				"phpunit.xml.dist":                "<phpunit/>",
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
	if len(lines) != 2 {
		t.Fatalf("expected two lines, got %d:\n%s", len(lines), rendered)
	}
	if lines[0] != "VERIFY: cargo test -p grep-printer --lib test_trim" {
		t.Fatalf("first line = %q", lines[0])
	}
	if !strings.Contains(lines[1], "crates/printer/src/util.rs") ||
		!strings.Contains(lines[1], "Cargo.toml [package] name") {
		t.Fatalf("second line must state target and derivation: %q", lines[1])
	}
}

func TestBuildSearchVerifyCommandNeedsACandidateFixSite(t *testing.T) {
	t.Parallel()
	evidence := searchVerifyTestEvidence(map[string]string{"go.mod": "module x\n"})
	// A payload whose only entry is documentation names no file a patch can land in.
	docsOnly := []SearchResult{{Rank: 1, FilePath: "docs/guide.md", Section: searchSectionDocs}}
	if got := buildSearchVerifyCommand(docsOnly, evidence); got != nil {
		t.Fatalf("expected silence, got %q", got.Command)
	}
}

// TestSearchVerifySuiteFallback covers the whole-suite fallback: when no narrow command can be
// derived (typically because the payload found no covering test the mirror lookup could name — the
// faker `test_<name>.rb` minitest case that shipped an EMPTY verify command), the block still emits
// the repository's own canonical suite command rather than nothing.
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
