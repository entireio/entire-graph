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
//
// WHICH command replaces it is decided by the settings script, not by the directory layout. Gradle
// locates the settings file by walking UP from its start directory and then KEEPS what it found only
// if that file declares a project AT the start directory; when it does not, Gradle discards the
// settings and runs the start directory as its own empty-settings build, where a sibling
// `project(":core")` dependency is no longer there to resolve. So `-p lib test` is not "the root
// build with a different default project" — for an ordinary multi-project layout it is either the
// root build (when the root settings declares `lib`) or a different build entirely (when it does
// not), and nothing in a `lib/build.gradle` says which. The documented spelling of a subproject task
// is the project path run from the root, so an included project gets that, `-p` is kept for a
// directory that is its OWN build root, and a directory nothing declares gets silence.
func TestSearchVerifySuiteGradleNamesTheProjectUnderAnAncestorWrapper(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		files       map[string]string
		wantCommand string
	}{
		{
			name: "an included subproject is addressed by its project path",
			files: map[string]string{
				"gradlew":                           "",
				"settings.gradle":                   "rootProject.name = 'app'\ninclude ':modules:core'\n",
				"modules/core/build.gradle":         "",
				"modules/core/src/main/java/A.java": "",
			},
			wantCommand: "./gradlew :modules:core:test",
		},
		{
			name: "the Kotlin DSL's parenthesised include declares it just as well",
			files: map[string]string{
				"gradlew":                           "",
				"settings.gradle.kts":               "include(\n    \":modules:core\",\n)\n",
				"modules/core/build.gradle.kts":     "",
				"modules/core/src/main/java/A.java": "",
			},
			wantCommand: "./gradlew :modules:core:test",
		},
		{
			name: "a directory carrying its own settings script is its own build, entered with -p",
			files: map[string]string{
				"gradlew":                           "",
				"modules/core/settings.gradle":      "rootProject.name = 'core'\n",
				"modules/core/build.gradle":         "",
				"modules/core/src/main/java/A.java": "",
			},
			wantCommand: "./gradlew -p modules/core test",
		},
		{
			name: "a build.gradle no settings script declares gets silence",
			files: map[string]string{
				"gradlew":                           "",
				"modules/core/build.gradle":         "",
				"modules/core/src/main/java/A.java": "",
			},
			wantCommand: "",
		},
		{
			name: "a commented-out include does not declare the project",
			files: map[string]string{
				"gradlew":                           "",
				"settings.gradle":                   "// include ':modules:core'\n",
				"modules/core/build.gradle":         "",
				"modules/core/src/main/java/A.java": "",
			},
			wantCommand: "",
		},
		{
			name: "a quoted path that is not an include argument does not declare the project",
			files: map[string]string{
				"gradlew":                           "",
				"settings.gradle":                   "def includes = [':modules:core']\n",
				"modules/core/build.gradle":         "",
				"modules/core/src/main/java/A.java": "",
			},
			wantCommand: "",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			evidence := searchVerifyTestEvidence(testCase.files)
			got := deriveSearchVerifySuiteCommand(
				searchVerifySubject{sourcePath: "modules/core/src/main/java/A.java"}, &evidence)
			if testCase.wantCommand == "" {
				if got != nil {
					t.Fatalf("command = %q, want silence", got.Command)
				}
				return
			}
			if got == nil {
				t.Fatal("expected a Gradle suite command, got silence")
			}
			if got.Command != testCase.wantCommand {
				t.Fatalf("command = %q, want %q", got.Command, testCase.wantCommand)
			}
		})
	}
}

// TestSearchVerifySuiteNodeHonorsTheDeclaredPackageManager pins the package-manager choice. `npm
// test` is not a portable spelling of "run this package's test script": a Yarn Plug'n'Play project
// has no node_modules/.bin for npm's lifecycle to find, so the hard gate fails in exactly the tree
// that said which manager to use.
//
// Proximity is the FIRST question and manager preference only the tie-break within one directory.
// A lockfile is a fact about the directory that holds it, so the nearest one is the one that governs
// the package being run; ordering the preference list ahead of the walk let a leaf's own lockfile
// lose to a differently-preferred lockfile several directories above it, which advertises a manager
// the leaf never declared.
func TestSearchVerifySuiteNodeHonorsTheDeclaredPackageManager(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		files       map[string]string
		source      string
		wantCommand string
	}{
		{
			name: "yarn lockfile",
			files: map[string]string{
				"package.json": `{"scripts":{"test":"jest"}}`,
				"yarn.lock":    "",
				"src/a.js":     "",
			},
			source:      "src/a.js",
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
			source:      "packages/ui/src/Button.js",
			wantCommand: "cd packages/ui && pnpm test",
		},
		{
			name: "a nearer npm lockfile beats a more-preferred pnpm lockfile further up",
			files: map[string]string{
				"pnpm-lock.yaml":                "",
				"package.json":                  `{"scripts":{"test":"jest"}}`,
				"packages/ui/package.json":      `{"scripts":{"test":"vitest"}}`,
				"packages/ui/package-lock.json": "",
				"packages/ui/src/Button.js":     "",
			},
			source:      "packages/ui/src/Button.js",
			wantCommand: "cd packages/ui && npm test",
		},
		{
			name: "two lockfiles in ONE directory still resolve by manager preference",
			files: map[string]string{
				"package.json":      `{"scripts":{"test":"jest"}}`,
				"package-lock.json": "",
				"pnpm-lock.yaml":    "",
				"src/a.js":          "",
			},
			source:      "src/a.js",
			wantCommand: "pnpm test",
		},
		{
			name: "corepack packageManager field wins over an npm lockfile",
			files: map[string]string{
				"package.json":      `{"packageManager":"yarn@4.1.0","scripts":{"test":"jest"}}`,
				"package-lock.json": "",
				"src/a.js":          "",
			},
			source:      "src/a.js",
			wantCommand: "yarn test",
		},
		{
			name: "a repository that declares nothing keeps npm",
			files: map[string]string{
				"package.json": `{"scripts":{"test":"jest"}}`,
				"src/a.js":     "",
			},
			source:      "src/a.js",
			wantCommand: "npm test",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			evidence := searchVerifyTestEvidence(testCase.files)
			got := deriveSearchVerifySuiteCommand(
				searchVerifySubject{sourcePath: testCase.source}, &evidence)
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
			name: "a parenthesised task declaration does too",
			files: map[string]string{
				"Gemfile":      "source 'x'\n",
				"Rakefile":     "desc 'run the tests'\ntask(:test => :compile) do\n  sh 'ruby -Itest'\nend\n",
				"lib/thing.rb": "",
			},
			wantCommand: "bundle exec rake test",
		},
		{
			name: "a parenthesised prerequisite still does not",
			files: map[string]string{
				"Gemfile":      "source 'x'\n",
				"Rakefile":     "task(:default => :test)\n",
				"lib/thing.rb": "",
			},
			wantCommand: "",
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

// TestSearchVerifyRakefileDeclarationFormsAreRecognised pins BOTH directions of the Rakefile
// predicate: the declaration syntaxes Rake actually accepts license `rake test`, and a file that
// merely mentions the word still does not.
//
// The declaration check exists because substring-matching "test" emitted `rake test` for Rakefiles
// that never define the task. The correction has the opposite failure mode: a pattern tight enough
// to reject prose can also reject `task(:test)`, which is ordinary Rake and defines the task. Both
// halves are load-bearing, so both are pinned here.
func TestSearchVerifyRakefileDeclarationFormsAreRecognised(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		content string
		want    bool
	}{
		// --- declarations: every one of these defines a task rake will run ---
		{name: "bare symbol", content: "task :test\n", want: true},
		{name: "symbol with a block", content: "task :test do\n  sh 'ruby'\nend\n", want: true},
		{name: "parenthesised symbol", content: "task(:test)\n", want: true},
		{name: "parenthesised symbol with a block", content: "task(:test) do\n  sh 'ruby'\nend\n", want: true},
		{name: "parenthesised with a space before the paren", content: "task (:test)\n", want: true},
		{name: "hashrocket dependency", content: "task :test => :compile\n", want: true},
		{name: "parenthesised hashrocket dependency", content: "task(:test => :compile)\n", want: true},
		{name: "hashrocket dependency list", content: "task :test => [:compile, :lint]\n", want: true},
		{name: "hash-argument dependency", content: "task test: :compile\n", want: true},
		{name: "parenthesised hash-argument dependency", content: "task(test: :compile)\n", want: true},
		{name: "hash-argument dependency list", content: "task test: %w[compile lint]\n", want: true},
		{name: "double-quoted name", content: "task \"test\"\n", want: true},
		{name: "single-quoted name", content: "task 'test' do\nend\n", want: true},
		{name: "parenthesised quoted name", content: "task(\"test\")\n", want: true},
		{name: "task arguments before the dependency", content: "task :test, [:pattern] => :compile do |t, args|\nend\n", want: true},
		{name: "multitask", content: "multitask :test\n", want: true},
		{name: "indented under a conditional", content: "if ENV['CI']\n  task(:test)\nend\n", want: true},
		{name: "declared after a desc line", content: "desc 'run the tests'\ntask(:test)\n", want: true},
		{name: "Rake::TestTask generator", content: "require 'rake/testtask'\nRake::TestTask.new(:test)\n", want: true},

		// --- mentions: none of these defines a task named `test` ---
		{name: "prerequisite of another task", content: "task default: %w[test rubocop]\n", want: false},
		{name: "parenthesised prerequisite of another task", content: "task(:default => :test)\n", want: false},
		{name: "comment mentioning the latest tests", content: "# run the latest tests with rake\ntask :lint\n", want: false},
		{name: "comment containing a declaration", content: "# task(:test) was removed\ntask :lint\n", want: false},
		{name: "prose using the word", content: "# the test suite lives elsewhere\n", want: false},
		{name: "a differently named task with the prefix", content: "task(:test_all)\n", want: false},
		{name: "a differently named symbol task", content: "task :testing\n", want: false},
		{name: "a differently named quoted task", content: "task(\"testing\")\n", want: false},
		{name: "a differently named hash-argument task", content: "task test_helper: :compile\n", want: false},
		{name: "the word inside a shell line", content: "task :lint do\n  sh 'rake test'\nend\n", want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := searchVerifyRakefileDefinesTest(testCase.content); got != testCase.want {
				t.Fatalf("searchVerifyRakefileDefinesTest(%q) = %v, want %v", testCase.content, got, testCase.want)
			}
		})
	}
}
