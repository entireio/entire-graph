package sem

import "testing"

// A VERIFY command is a HARD GATE: the agent runs it and reads the result as the answer about its
// edit. Every case here is a command the deriver used to advertise that could not run at all in
// the repository it was derived from, which costs strictly more than the silence this block
// otherwise prefers.

// TestSearchVerifyMavenPicksTheReactorOnlyWhenThereIsOne pins both halves of the Maven invocation.
//
// `-pl <module> -am` selects a module of the ROOT REACTOR and is run from the repository root. It is
// right in a multi-module build and unrunnable without a root aggregator POM ("there is no POM in
// this directory"); `cd <module> && mvn test` is the mirror image — right for a standalone module,
// and wrong in a reactor, where the module's siblings are then resolved from the local repository
// instead of being built.
func TestSearchVerifyMavenPicksTheReactorOnlyWhenThereIsOne(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		files       map[string]string
		subject     searchVerifySubject
		wantCommand string
		suite       bool
	}{
		{
			name: "narrow: a nested POM with no root aggregator runs inside the module",
			files: map[string]string{
				"services/api/pom.xml":                                    "<project/>",
				"services/api/src/main/java/com/example/Handler.java":     "",
				"services/api/src/test/java/com/example/HandlerTest.java": "",
			},
			subject: searchVerifySubject{
				sourcePath:   "services/api/src/main/java/com/example/Handler.java",
				testPath:     "services/api/src/test/java/com/example/HandlerTest.java",
				testEvidence: "covering test",
			},
			wantCommand: "cd services/api && mvn -q -Dtest=HandlerTest -DfailIfNoTests=false test",
		},
		{
			name: "narrow: a reactor module is still addressed from the root",
			files: map[string]string{
				"pom.xml":              "<project/>",
				"services/api/pom.xml": "<project/>",
				"services/api/src/main/java/com/example/Handler.java":     "",
				"services/api/src/test/java/com/example/HandlerTest.java": "",
			},
			subject: searchVerifySubject{
				sourcePath:   "services/api/src/main/java/com/example/Handler.java",
				testPath:     "services/api/src/test/java/com/example/HandlerTest.java",
				testEvidence: "covering test",
			},
			wantCommand: "mvn -q -pl services/api -am -Dtest=HandlerTest -DfailIfNoTests=false test",
		},
		{
			suite: true,
			name:  "suite: a reactor module builds its dependencies instead of assuming them",
			files: map[string]string{
				"pom.xml":              "<project/>",
				"services/api/pom.xml": "<project/>",
				"services/api/src/main/java/com/example/Handler.java": "",
			},
			subject:     searchVerifySubject{sourcePath: "services/api/src/main/java/com/example/Handler.java"},
			wantCommand: "mvn -q -pl services/api -am test",
		},
		{
			suite: true,
			name:  "suite: a standalone nested POM runs inside the module",
			files: map[string]string{
				"services/api/pom.xml":                                "<project/>",
				"services/api/src/main/java/com/example/Handler.java": "",
			},
			subject:     searchVerifySubject{sourcePath: "services/api/src/main/java/com/example/Handler.java"},
			wantCommand: "cd services/api && mvn -q test",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			evidence := searchVerifyTestEvidence(testCase.files)
			var got *SearchVerifyCommand
			if testCase.suite {
				got = deriveSearchVerifySuiteCommand(testCase.subject, &evidence)
			} else {
				got = deriveSearchVerifyCommand(testCase.subject, &evidence)
			}
			if got == nil {
				t.Fatal("expected a Maven command, got silence")
			}
			if got.Command != testCase.wantCommand {
				t.Fatalf("command = %q, want %q", got.Command, testCase.wantCommand)
			}
		})
	}
}

// TestSearchVerifySuiteGradleNamesTheProjectUnderAnAncestorWrapper pins the nested-build case: the
// wrapper may sit above the build the manifest named, and `./gradlew test` from the wrapper's own
// directory then tests whatever build is THERE — Gradle does not walk down to find a descendant.
func TestSearchVerifySuiteGradleNamesTheProjectUnderAnAncestorWrapper(t *testing.T) {
	t.Parallel()
	evidence := searchVerifyTestEvidence(map[string]string{
		"gradlew":                           "",
		"modules/core/build.gradle":         "",
		"modules/core/src/main/java/A.java": "",
	})
	got := deriveSearchVerifySuiteCommand(
		searchVerifySubject{sourcePath: "modules/core/src/main/java/A.java"}, &evidence)
	if got == nil {
		t.Fatal("expected a Gradle suite command, got silence")
	}
	if got.Command != "./gradlew -p modules/core test" {
		t.Fatalf("command = %q, want %q", got.Command, "./gradlew -p modules/core test")
	}
}

// TestSearchVerifySuiteNodeHonorsTheDeclaredPackageManager pins the package-manager choice. `npm
// test` is not a portable spelling of "run this package's test script": a Yarn Plug'n'Play project
// has no node_modules/.bin for npm's lifecycle to find, so the hard gate fails in exactly the tree
// that said which manager to use.
func TestSearchVerifySuiteNodeHonorsTheDeclaredPackageManager(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		files       map[string]string
		wantCommand string
	}{
		{
			name: "yarn lockfile",
			files: map[string]string{
				"package.json": `{"scripts":{"test":"jest"}}`,
				"yarn.lock":    "",
				"src/a.js":     "",
			},
			wantCommand: "yarn test",
		},
		{
			name: "pnpm lockfile at the workspace root above the leaf package",
			files: map[string]string{
				"pnpm-lock.yaml":            "",
				"package.json":              `{"scripts":{"test":"jest"}}`,
				"packages/ui/package.json":  `{"scripts":{"test":"vitest"}}`,
				"packages/ui/src/Button.js": "",
			},
			wantCommand: "cd packages/ui && pnpm test",
		},
		{
			name: "corepack packageManager field wins over an npm lockfile",
			files: map[string]string{
				"package.json":      `{"packageManager":"yarn@4.1.0","scripts":{"test":"jest"}}`,
				"package-lock.json": "",
				"src/a.js":          "",
			},
			wantCommand: "yarn test",
		},
		{
			name: "a repository that declares nothing keeps npm",
			files: map[string]string{
				"package.json": `{"scripts":{"test":"jest"}}`,
				"src/a.js":     "",
			},
			wantCommand: "npm test",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			evidence := searchVerifyTestEvidence(testCase.files)
			source := "src/a.js"
			if _, ok := testCase.files["packages/ui/src/Button.js"]; ok {
				source = "packages/ui/src/Button.js"
			}
			got := deriveSearchVerifySuiteCommand(searchVerifySubject{sourcePath: source}, &evidence)
			if got == nil {
				t.Fatal("expected a Node suite command, got silence")
			}
			if got.Command != testCase.wantCommand {
				t.Fatalf("command = %q, want %q", got.Command, testCase.wantCommand)
			}
		})
	}
}

// TestSearchVerifyRubyRequiresADeclaredTestTaskAndAGemfile pins the two Ruby gates.
//
// A Rakefile that merely MENTIONS `test` — as a prerequisite of another task, or in a comment — does
// not have a `test` task, and `rake test` answers "Don't know how to build task 'test'". And
// `bundle exec` without a Gemfile is "Could not locate Gemfile" every time.
func TestSearchVerifyRubyRequiresADeclaredTestTaskAndAGemfile(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		files       map[string]string
		wantCommand string
	}{
		{
			name: "a prerequisite mention does not declare the task",
			files: map[string]string{
				"Gemfile":      "source 'x'\n",
				"Rakefile":     "task default: %w[test rubocop]\n",
				"lib/thing.rb": "",
			},
			wantCommand: "",
		},
		{
			name: "a comment mentioning tests does not declare the task",
			files: map[string]string{
				"Gemfile":      "source 'x'\n",
				"Rakefile":     "# run the latest tests with rake\ntask :lint\n",
				"lib/thing.rb": "",
			},
			wantCommand: "",
		},
		{
			name: "an explicit task declaration does",
			files: map[string]string{
				"Gemfile":      "source 'x'\n",
				"Rakefile":     "task :test do\n  sh 'ruby -Itest'\nend\n",
				"lib/thing.rb": "",
			},
			wantCommand: "bundle exec rake test",
		},
		{
			name: "Rake::TestTask declares it too",
			files: map[string]string{
				"Gemfile":      "source 'x'\n",
				"Rakefile":     "require 'rake/testtask'\nRake::TestTask.new(:test)\n",
				"lib/thing.rb": "",
			},
			wantCommand: "bundle exec rake test",
		},
		{
			name: "a standalone Rake project does not go through Bundler",
			files: map[string]string{
				"Rakefile":     "task :test do\n  sh 'ruby -Itest'\nend\n",
				"lib/thing.rb": "",
			},
			wantCommand: "rake test",
		},
		{
			name: "an rspec tree without a Gemfile does not go through Bundler either",
			files: map[string]string{
				".rspec":       "--require spec_helper\n",
				"Rakefile":     "task :lint\n",
				"lib/thing.rb": "",
			},
			wantCommand: "rspec",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			evidence := searchVerifyTestEvidence(testCase.files)
			got := deriveSearchVerifySuiteCommand(searchVerifySubject{sourcePath: "lib/thing.rb"}, &evidence)
			if testCase.wantCommand == "" {
				if got != nil {
					t.Fatalf("expected silence, got %q", got.Command)
				}
				return
			}
			if got == nil {
				t.Fatal("expected a Ruby suite command, got silence")
			}
			if got.Command != testCase.wantCommand {
				t.Fatalf("command = %q, want %q", got.Command, testCase.wantCommand)
			}
		})
	}
}

// TestSearchVerifyStageSeparatorIgnoresQuotedAmpersands pins the two consumers that split a command
// on its `cd` stage. searchVerifyRunIn single-quotes a directory whose name is not shell-safe, so a
// repository under `foo&&bar` emits a CORRECT `cd 'foo&&bar' && npm test`; a quote-unaware split
// cuts inside that operand.
func TestSearchVerifyStageSeparatorIgnoresQuotedAmpersands(t *testing.T) {
	t.Parallel()
	command := searchVerifyRunIn("foo&&bar", "npm test")
	if command != "cd 'foo&&bar' && npm test" {
		t.Fatalf("derivation emitted %q, want %q", command, "cd 'foo&&bar' && npm test")
	}
	if runner := searchVerifyRunner(command); runner != "npm" {
		t.Fatalf("runner = %q, want %q", runner, "npm")
	}
	decorated := searchVerifyDecorated(&SearchVerifyCommand{Command: command, Prefix: "EGTOK"})
	if decorated != "cd 'foo&&bar' && EGTOK npm test" {
		t.Fatalf("decorated = %q, want %q", decorated, "cd 'foo&&bar' && EGTOK npm test")
	}

	// An apostrophe in the directory name is spelled by closing, escaping and reopening the quote.
	// A scanner that reads that escaped apostrophe as a quote inverts its state for the rest of the
	// command.
	apostrophe := searchVerifyRunIn("it's&&here", "npm test")
	if runner := searchVerifyRunner(apostrophe); runner != "npm" {
		t.Fatalf("runner over %q = %q, want %q", apostrophe, runner, "npm")
	}
}
