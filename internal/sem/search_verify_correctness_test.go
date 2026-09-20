package sem

import "testing"

// TestSearchVerifyGradleNarrowTierNeedsSettingsInclusion is the narrow half of the check the SUITE
// tier already makes (issues #219, #203).
//
// `deriveSearchVerifyGradle` derived `:lib:test` from the existence of `lib/build.gradle` alone.
// A project path is not a directory path: Gradle resolves `:lib` against the projects the SETTINGS
// script declares, so in a build whose `settings.gradle` says `include ':app'` and nothing else,
// `./gradlew :lib:test --tests 'ATest'` answers "Project 'lib' not found in root project" — a
// hard-gate command that cannot run, which is the failure this whole block exists to prevent.
//
// Both directions are pinned. The emit direction must keep working for a declared project, for the
// root project, and for a directory that carries its OWN settings script (a separate build root,
// which Gradle is pointed at with `-p`, exactly as the suite tier spells it). The decline direction
// must hold for an undeclared directory, for a build with no settings script at all, and for a
// project the settings script REMAPS onto a different tree — where the command would run, and pass,
// about code the edit never touched.
func TestSearchVerifyGradleNarrowTierNeedsSettingsInclusion(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		files       map[string]string
		wantCommand string
	}{
		{
			name: "a settings script that declares the project licenses its project path",
			files: map[string]string{
				"gradlew":                      "",
				"settings.gradle":              "include ':app'\ninclude ':lib'\n",
				"app/build.gradle":             "",
				"lib/build.gradle":             "",
				"lib/src/main/java/A.java":     "",
				"lib/src/test/java/ATest.java": "",
			},
			wantCommand: "./gradlew :lib:test --tests 'ATest'",
		},
		{
			name: "a settings script that declares a SIBLING declines",
			files: map[string]string{
				"gradlew":                      "",
				"settings.gradle":              "include ':app'\n",
				"app/build.gradle":             "",
				"lib/build.gradle":             "",
				"lib/src/main/java/A.java":     "",
				"lib/src/test/java/ATest.java": "",
			},
			wantCommand: "",
		},
		{
			name: "no settings script at all declines",
			files: map[string]string{
				"gradlew":                      "",
				"lib/build.gradle":             "",
				"lib/src/main/java/A.java":     "",
				"lib/src/test/java/ATest.java": "",
			},
			wantCommand: "",
		},
		{
			name: "a directory carrying its own settings script is a build root, addressed with -p",
			files: map[string]string{
				"gradlew":                      "",
				"settings.gradle":              "include ':app'\n",
				"app/build.gradle":             "",
				"lib/settings.gradle":          "rootProject.name = 'lib'\n",
				"lib/build.gradle":             "",
				"lib/src/main/java/A.java":     "",
				"lib/src/test/java/ATest.java": "",
			},
			wantCommand: "./gradlew -p lib test --tests 'ATest'",
		},
		{
			name: "a remapped project declines rather than test a different tree",
			files: map[string]string{
				"gradlew":                      "",
				"settings.gradle":              "include ':lib'\nproject(':lib').projectDir = file('other')\n",
				"lib/build.gradle":             "",
				"lib/src/main/java/A.java":     "",
				"lib/src/test/java/ATest.java": "",
			},
			wantCommand: "",
		},
		{
			name: "the ROOT project needs no inclusion: it is the build",
			files: map[string]string{
				"gradlew":                  "",
				"build.gradle":             "",
				"src/main/java/A.java":     "",
				"src/test/java/ATest.java": "",
			},
			wantCommand: "./gradlew :test --tests 'ATest'",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			evidence := searchVerifyTestEvidence(testCase.files)
			got := deriveSearchVerifyGradleForTest(&evidence, testCase.files)
			command := ""
			if got != nil {
				command = got.Command
			}
			if command != testCase.wantCommand {
				t.Fatalf("command = %q, want %q", command, testCase.wantCommand)
			}
		})
	}
}

// deriveSearchVerifyGradleForTest runs the NARROW derivation over a fixture whose source and test
// files follow the `src/main` / `src/test` convention, so the table above states only the build
// files that decide the answer.
func deriveSearchVerifyGradleForTest(
	evidence *searchVerifyEvidence, files map[string]string,
) *SearchVerifyCommand {
	subject := searchVerifySubject{testEvidence: "covering test"}
	for path := range files {
		switch {
		case searchVerifyStem(path) == "A":
			subject.sourcePath = path
		case searchVerifyStem(path) == "ATest":
			subject.testPath = path
		}
	}
	return deriveSearchVerifyCommand(subject, evidence)
}

// TestSearchVerifyRakeNamespacedTaskIsQualified pins issues #225 and #205.
//
// Rake scopes a task declared inside `namespace :foo do … end` as `foo:test`, so a Rakefile whose
// only declaration is namespaced answers `rake test` with "Don't know how to build task 'test'".
// The derivation now names the task Rake actually has.
//
// The other direction is the one that was traded away twice before: a declaration that merely LOOKS
// nested — indented inside `if`, `begin`, a `def` — is still top level, and must keep emitting the
// bare `rake test` it always did.
func TestSearchVerifyRakeNamespacedTaskIsQualified(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name     string
		content  string
		wantTask string
		wantOK   bool
	}{
		// --- namespaced: the task Rake declares is qualified ---
		{
			name:     "a task inside a namespace",
			content:  "namespace :foo do\n  task :test do\n  end\nend\n",
			wantTask: "foo:test", wantOK: true,
		},
		{
			name:     "define_task inside a namespace",
			content:  "namespace :foo do\n  Rake::Task.define_task(:test)\nend\n",
			wantTask: "foo:test", wantOK: true,
		},
		{
			name:     "a generator inside a namespace",
			content:  "require 'rake/testtask'\nnamespace :foo do\n  Rake::TestTask.new do |t|\n  end\nend\n",
			wantTask: "foo:test", wantOK: true,
		},
		{
			name:     "a quoted namespace name",
			content:  "namespace \"foo\" do\n  task :test\nend\n",
			wantTask: "foo:test", wantOK: true,
		},
		{
			name:     "nested namespaces qualify with every segment",
			content:  "namespace :a do\n  namespace :b do\n    task :test\n  end\nend\n",
			wantTask: "a:b:test", wantOK: true,
		},
		{
			// Ruby opens both blocks on one line here, and `rake -AT` on this Rakefile lists
			// `foo:bar:test` and nothing else. Naming only the first namespace produced
			// `rake foo:test`, which rake answers with "Don't know how to build task 'foo:test'".
			name:     "two namespaces opened on one line",
			content:  "namespace :foo do namespace :bar do\n  task :test\nend end\n",
			wantTask: "foo:bar:test", wantOK: true,
		},
		{
			name:     "three namespaces opened on one line",
			content:  "namespace :a do namespace :b do namespace :c do\n  task :test\nend end end\n",
			wantTask: "a:b:c:test", wantOK: true,
		},
		{
			name:     "a namespace opened after another block on the same line",
			content:  "[1].each do namespace :foo do\n  task :test\nend end\n",
			wantTask: "foo:test", wantOK: true,
		},
		{
			name:     "a quoted namespace opened second on the same line",
			content:  "namespace :foo do namespace \"bar\" do\n  task :test\nend end\n",
			wantTask: "foo:bar:test", wantOK: true,
		},

		// --- names this cannot read: report NO namespace rather than a half-qualified one ---
		{
			// `rake test` fails loudly on this Rakefile. `rake foo:test` would be the confident
			// wrong answer, and a half-qualified name is the failure this whole change exists to
			// end, so an unreadable name degrades to the answer that predates namespace tracking.
			name:     "a namespace named by a variable reports no namespace",
			content:  "NAME = :foo\nnamespace NAME do\n  task :test\nend\n",
			wantTask: "test", wantOK: true,
		},
		{
			name:     "a namespace named by a method call reports no namespace",
			content:  "namespace compute_name do\n  task :test\nend\n",
			wantTask: "test", wantOK: true,
		},
		{
			name:     "a namespace that closed before the declaration does not qualify it",
			content:  "namespace :foo do\n  task :lint\nend\n\ntask :test\n",
			wantTask: "test", wantOK: true,
		},
		{
			name:     "a top-level declaration wins over a namespaced one",
			content:  "task :test\n\nnamespace :foo do\n  task :test\nend\n",
			wantTask: "test", wantOK: true,
		},

		// --- top level, however it is indented: the bare task name is kept ---
		{name: "bare symbol", content: "task :test\n", wantTask: "test", wantOK: true},
		{
			name:     "indented under a conditional is still top level",
			content:  "if ENV['CI']\n  task(:test)\nend\n",
			wantTask: "test", wantOK: true,
		},
		{
			name:     "indented under begin is still top level",
			content:  "begin\n  task :test\nrescue LoadError\nend\n",
			wantTask: "test", wantOK: true,
		},
		{
			name:     "declared after a namespace block and a def",
			content:  "namespace :foo do\n  task :lint\nend\n\ndef helper\n  1\nend\n\ntask :test\n",
			wantTask: "test", wantOK: true,
		},
		{
			name:     "the word end inside a string does not close the namespace",
			content:  "namespace :foo do\n  desc 'run to the end'\n  task :test\nend\n",
			wantTask: "foo:test", wantOK: true,
		},
		{
			name:     "a commented end does not close the namespace",
			content:  "namespace :foo do\n  # end of the namespace\n  task :test\nend\n",
			wantTask: "foo:test", wantOK: true,
		},

		// --- no declaration at all ---
		{name: "a prerequisite is not a declaration", content: "task default: %w[test rubocop]\n", wantOK: false},
		{name: "prose is not a declaration", content: "# the test suite lives elsewhere\n", wantOK: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			task, ok := searchVerifyRakefileTestTask(testCase.content)
			if ok != testCase.wantOK {
				t.Fatalf("searchVerifyRakefileTestTask(%q) ok = %v, want %v", testCase.content, ok, testCase.wantOK)
			}
			if ok && task != testCase.wantTask {
				t.Fatalf("searchVerifyRakefileTestTask(%q) = %q, want %q", testCase.content, task, testCase.wantTask)
			}
		})
	}
}

// TestSearchVerifyRakeNamespacedSuiteCommandRuns is the end-to-end half: the command the payload
// advertises for a namespaced Rakefile is the one `rake` can run.
func TestSearchVerifyRakeNamespacedSuiteCommandRuns(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		rakefile    string
		wantCommand string
	}{
		{
			name:        "a namespaced task is advertised by its qualified name",
			rakefile:    "namespace :foo do\n  task :test do\n  end\nend\n",
			wantCommand: "rake foo:test",
		},
		{
			name:        "a top-level task keeps the bare name",
			rakefile:    "task :test do\nend\n",
			wantCommand: "rake test",
		},
		{
			// Verified against rake 13.0.6: this Rakefile declares `foo:bar:test` and nothing
			// else, so `rake foo:test` answers "Don't know how to build task 'foo:test'".
			name:        "both namespaces opened on one line are qualified",
			rakefile:    "namespace :foo do namespace :bar do\n  task :test do\n  end\nend end\n",
			wantCommand: "rake foo:bar:test",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			evidence := searchVerifyTestEvidence(map[string]string{
				"Rakefile":       testCase.rakefile,
				"lib/invoice.rb": "class Invoice\nend\n",
			})
			got := deriveSearchVerifySuiteCommand(
				searchVerifySubject{sourcePath: "lib/invoice.rb"}, &evidence)
			if got == nil {
				t.Fatal("expected a Ruby suite command, got silence")
			}
			if got.Command != testCase.wantCommand {
				t.Fatalf("command = %q, want %q", got.Command, testCase.wantCommand)
			}
		})
	}
}

// TestSearchVerifyWorkspaceBraceExpansion pins issue #224.
//
// `workspaces` globs are micromatch, whose brace ALTERNATION `path.Match` reads as literal text. The
// whole class was answered permissively — every leaf under a brace pattern kept the workspace's
// manager — which is right for a member and wrong for a non-member, where Yarn rejects the leaf.
// Expanding the braces first answers both rows correctly instead of picking a side.
//
// The residue is pinned too: an extglob group, and any brace construct this does NOT expand (a
// range, an unbalanced brace), stay unreadable and keep the permissive answer they had.
func TestSearchVerifyWorkspaceBraceExpansion(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		workspaces  string
		leaf        string
		wantCommand string
	}{
		{
			name:        "a member of the second alternative keeps the workspace manager",
			workspaces:  `["{packages,tools}/*"]`,
			leaf:        "tools/scratch",
			wantCommand: "cd tools/scratch && yarn test",
		},
		{
			name:        "a package the expanded pattern does not reach falls back to npm",
			workspaces:  `["{packages,tools}/*"]`,
			leaf:        "standalone",
			wantCommand: "cd standalone && npm test",
		},
		{
			name:        "nested groups expand too",
			workspaces:  `["{packages/{ui,api},tools}/*"]`,
			leaf:        "packages/ui/button",
			wantCommand: "cd packages/ui/button && yarn test",
		},
		{
			name:        "a nested group that does not reach the leaf falls back to npm",
			workspaces:  `["{packages/{ui,api},tools}/*"]`,
			leaf:        "packages/db/core",
			wantCommand: "cd packages/db/core && npm test",
		},
		{
			name:        "a group spanning a separator expands as written",
			workspaces:  `["{packages/*,tools/deep/*}"]`,
			leaf:        "tools/deep/scratch",
			wantCommand: "cd tools/deep/scratch && yarn test",
		},
		{
			name:        "an extglob group is still unreadable, so the manager is kept",
			workspaces:  `["+(packages|tools)/*"]`,
			leaf:        "standalone",
			wantCommand: "cd standalone && yarn test",
		},
		{
			name:        "a brace RANGE is not expanded, so the manager is kept",
			workspaces:  `["packages{1..3}/*"]`,
			leaf:        "standalone",
			wantCommand: "cd standalone && yarn test",
		},
		{
			name:        "an unbalanced brace is not expanded, so the manager is kept",
			workspaces:  `["{packages,tools/*"]`,
			leaf:        "standalone",
			wantCommand: "cd standalone && yarn test",
		},
		{
			name:        "a plain pattern is unaffected",
			workspaces:  `["packages/*"]`,
			leaf:        "packages/api",
			wantCommand: "cd packages/api && yarn test",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			evidence := searchVerifyTestEvidence(map[string]string{
				"package.json": `{"name":"root","workspaces":` + testCase.workspaces + `}`,
				"yarn.lock":    "__metadata:\n  version: 8\n",
				testCase.leaf + "/package.json": `{"name":"leaf","scripts":{"test":"jest"},` +
					`"devDependencies":{"jest":"^29.0.0"}}`,
				testCase.leaf + "/src/index.js": "",
			})
			got := deriveSearchVerifySuiteCommand(
				searchVerifySubject{sourcePath: testCase.leaf + "/src/index.js"}, &evidence)
			if got == nil {
				t.Fatal("expected a Node suite command, got silence")
			}
			if got.Command != testCase.wantCommand {
				t.Fatalf("command = %q, want %q", got.Command, testCase.wantCommand)
			}
		})
	}
}

// TestSearchVerifyExpandBracesIsBounded pins the expander itself, including the bound that keeps a
// pathological pattern from being expanded at all.
func TestSearchVerifyExpandBracesIsBounded(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		pattern string
		want    []string
		wantOK  bool
	}{
		{name: "no braces", pattern: "packages/*", want: []string{"packages/*"}, wantOK: true},
		{
			name: "one group", pattern: "{packages,tools}/*",
			want: []string{"packages/*", "tools/*"}, wantOK: true,
		},
		{
			name: "two groups", pattern: "{a,b}/{c,d}",
			want: []string{"a/c", "a/d", "b/c", "b/d"}, wantOK: true,
		},
		{
			name: "nested groups", pattern: "{a/{x,y},b}/*",
			want: []string{"a/x/*", "a/y/*", "b/*"}, wantOK: true,
		},
		{
			name: "an empty alternative", pattern: "pkg{,s}/*",
			want: []string{"pkg/*", "pkgs/*"}, wantOK: true,
		},
		{name: "a range is not alternation", pattern: "pkg{1..3}/*", wantOK: false},
		{name: "a single alternative is not alternation", pattern: "pkg{s}/*", wantOK: false},
		{name: "an unbalanced open brace", pattern: "{a,b/*", wantOK: false},
		{name: "an unbalanced close brace", pattern: "a,b}/*", wantOK: false},
		{
			name:    "past the expansion bound",
			pattern: "{a,b}/{a,b}/{a,b}/{a,b}/{a,b}/{a,b}/{a,b}",
			wantOK:  false,
		},
		{
			name: "an escaped brace is literal", pattern: `pkg\{s\}/*`,
			want: []string{`pkg\{s\}/*`}, wantOK: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, ok := searchVerifyExpandBraces(testCase.pattern)
			if ok != testCase.wantOK {
				t.Fatalf("searchVerifyExpandBraces(%q) ok = %v, want %v", testCase.pattern, ok, testCase.wantOK)
			}
			if !ok {
				return
			}
			if len(got) != len(testCase.want) {
				t.Fatalf("searchVerifyExpandBraces(%q) = %q, want %q", testCase.pattern, got, testCase.want)
			}
			for index := range got {
				if got[index] != testCase.want[index] {
					t.Fatalf("searchVerifyExpandBraces(%q) = %q, want %q", testCase.pattern, got, testCase.want)
				}
			}
		})
	}
}

// TestSearchProgramTextFixtureStaysPrimary pins issue #221, whose two halves have to land together.
//
// The docs-and-fixtures section's own contract is "holds no program text", but it admitted the whole
// fixture CLASS, which `classifySearchFile` assigns by DIRECTORY segment. So a `.go` file under
// `testdata/` — and `pkg/golden/report.go`, ordinary source under a directory named `golden` — were
// labelled "not fix sites" and excluded from related-site expansion. This repository's own
// `internal/sem/testdata` is exactly that case.
//
// Restricting the section to RECORDINGS alone moves the failure into VERIFY unless the second half
// lands with it: `searchVerifySubjectFor` reads the PRIMARY section, and `internal/sem/testdata/case.go`
// satisfies `searchTestArtifactPath`, so the moment a program-text fixture returns to primary the
// payload would instruct the agent to run a parser FIXTURE as its verification command.
func TestSearchProgramTextFixtureStaysPrimary(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		path string
		want bool
	}{
		// --- recordings: no program text, so the section is right about them ---
		{name: "a snapshot recording", path: "src/__snapshots__/Button.test.js.snap", want: true},
		{name: "a golden recording", path: "internal/sem/testdata/fixtures/go-basic.ndjson.golden", want: true},
		{name: "an ambr recording", path: "tests/__snapshots__/api.ambr", want: true},
		{name: "a fixture in no language this parses", path: "testdata/corpus/input.bin", want: true},

		// --- program text that merely lives in a fixture tree ---
		{name: "a Go fixture", path: "internal/sem/testdata/fixtures/go-basic/auth.go", want: false},
		{name: "a TypeScript fixture", path: "internal/sem/testdata/fixtures/typescript-basic/api.ts", want: false},
		{name: "a Python fixture", path: "crates/ruff_linter/resources/test/fixtures/flake8_simplify/SIM201.py", want: false},
		{name: "ordinary source under a directory named golden", path: "pkg/golden/report.go", want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if classifySearchFile(testCase.path) != searchFileClassFixture {
				t.Fatalf("classifySearchFile(%q) = %q, want %q",
					testCase.path, classifySearchFile(testCase.path), searchFileClassFixture)
			}
			got := searchDocsSectionPath(searchQuery{}, testCase.path)
			if got != testCase.want {
				t.Fatalf("searchDocsSectionPath(%q) = %v, want %v", testCase.path, got, testCase.want)
			}
		})
	}
}

// TestSearchVerifyNeverRunsAFixtureAsATest is the other half of #221: a program-text fixture back in
// the primary section must not be adopted as the test to run.
func TestSearchVerifyNeverRunsAFixtureAsATest(t *testing.T) {
	t.Parallel()
	subject, ok := searchVerifySubjectFor([]SearchResult{
		{FilePath: "internal/sem/parser.go", Section: searchSectionPrimary, SymbolName: "Parse"},
		{FilePath: "internal/sem/testdata/case.go", Section: searchSectionPrimary, SymbolName: "Case"},
	})
	if !ok {
		t.Fatal("expected a verify subject")
	}
	if subject.sourcePath != "internal/sem/parser.go" {
		t.Fatalf("sourcePath = %q, want %q", subject.sourcePath, "internal/sem/parser.go")
	}
	if subject.testPath != "" {
		t.Fatalf("testPath = %q, want none: a fixture is not a test to run", subject.testPath)
	}

	// The decline is about the FIXTURE class, not about test trees in general: an ordinary test file
	// in the primary section is still adopted.
	subject, ok = searchVerifySubjectFor([]SearchResult{
		{FilePath: "internal/sem/parser.go", Section: searchSectionPrimary, SymbolName: "Parse"},
		{FilePath: "internal/sem/parser_test.go", Section: searchSectionPrimary, SymbolName: "TestParse"},
	})
	if !ok {
		t.Fatal("expected a verify subject")
	}
	if subject.testPath != "internal/sem/parser_test.go" {
		t.Fatalf("testPath = %q, want %q", subject.testPath, "internal/sem/parser_test.go")
	}
}
