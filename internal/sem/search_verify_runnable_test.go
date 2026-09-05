package sem

import "testing"

// A VERIFY command is a HARD GATE: the agent runs it and reads the result as the answer about its
// edit. Every case here is a command the deriver used to advertise that could not run at all in
// the repository it was derived from, which costs strictly more than the silence this block
// otherwise prefers.

// mavenAggregator renders a root POM that declares the named modules, which is what puts them in
// the reactor `-pl` selects out of.
func mavenAggregator(modules ...string) string {
	rendered := "<project><modules>"
	for _, module := range modules {
		rendered += "<module>" + module + "</module>"
	}
	return rendered + "</modules></project>"
}

// TestSearchVerifyMavenPicksTheReactorOnlyWhenThereIsOne pins both halves of the Maven invocation.
//
// `-pl <module> -am` selects a module of the ROOT REACTOR and is run from the repository root. It is
// right in a multi-module build and unrunnable without a root aggregator POM ("there is no POM in
// this directory"); `cd <module> && mvn test` is the mirror image — right for a standalone module,
// and wrong in a reactor, where the module's siblings are then resolved from the local repository
// instead of being built.
//
// A root POM merely EXISTING does not decide it. `-pl` selects out of a reactor, and the reactor is
// what the aggregator DECLARES in `<modules>`, transitively through nested aggregators; a polyglot
// tree can hold an unrelated root project above a standalone service, and selecting that service
// fails with "Could not find the selected project in the reactor". So membership is what picks, and
// every way of not proving it — undeclared, declared only under a `<profile>`, or a POM that does
// not parse — falls back to the `cd` form, which runs.
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
			name: "narrow: a declared reactor module is still addressed from the root",
			files: map[string]string{
				"pom.xml":              mavenAggregator("services/api"),
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
			name:  "suite: a declared reactor module builds its dependencies instead of assuming them",
			files: map[string]string{
				"pom.xml":              mavenAggregator("services/api"),
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
		{
			suite: true,
			name:  "suite: a root POM that does not declare the module leaves it standalone",
			files: map[string]string{
				// The reviewer's polyglot case: an unrelated root project, and a service the
				// reactor never names. `mvn -pl services/api -am` cannot select it.
				"pom.xml":              mavenAggregator("tools"),
				"tools/pom.xml":        "<project/>",
				"services/api/pom.xml": "<project/>",
				"services/api/src/main/java/com/example/Handler.java": "",
			},
			subject:     searchVerifySubject{sourcePath: "services/api/src/main/java/com/example/Handler.java"},
			wantCommand: "cd services/api && mvn -q test",
		},
		{
			suite: true,
			name:  "suite: a module declared through a nested aggregator is in the reactor",
			files: map[string]string{
				"pom.xml":              mavenAggregator("services"),
				"services/pom.xml":     mavenAggregator("api"),
				"services/api/pom.xml": "<project/>",
				"services/api/src/main/java/com/example/Handler.java": "",
			},
			subject:     searchVerifySubject{sourcePath: "services/api/src/main/java/com/example/Handler.java"},
			wantCommand: "mvn -q -pl services/api -am test",
		},
		{
			suite: true,
			name:  "suite: a module spelled as its POM file is the same declaration",
			files: map[string]string{
				"pom.xml":              mavenAggregator("services/api/pom.xml"),
				"services/api/pom.xml": "<project/>",
				"services/api/src/main/java/com/example/Handler.java": "",
			},
			subject:     searchVerifySubject{sourcePath: "services/api/src/main/java/com/example/Handler.java"},
			wantCommand: "mvn -q -pl services/api -am test",
		},
		{
			suite: true,
			name:  "suite: a module declared only inside a profile is not selectable by default",
			files: map[string]string{
				"pom.xml": "<project><profiles><profile><id>all</id>" +
					"<modules><module>services/api</module></modules></profile></profiles></project>",
				"services/api/pom.xml":                                "<project/>",
				"services/api/src/main/java/com/example/Handler.java": "",
			},
			subject:     searchVerifySubject{sourcePath: "services/api/src/main/java/com/example/Handler.java"},
			wantCommand: "cd services/api && mvn -q test",
		},
		{
			suite: true,
			name:  "suite: a root POM that does not parse declares nothing",
			files: map[string]string{
				"pom.xml":              "<project><modules><module>services/api",
				"services/api/pom.xml": "<project/>",
				"services/api/src/main/java/com/example/Handler.java": "",
			},
			subject:     searchVerifySubject{sourcePath: "services/api/src/main/java/com/example/Handler.java"},
			wantCommand: "cd services/api && mvn -q test",
		},
		{
			name: "narrow: an undeclared nested module keeps its own test class command",
			files: map[string]string{
				"pom.xml":              mavenAggregator("tools"),
				"tools/pom.xml":        "<project/>",
				"services/api/pom.xml": "<project/>",
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
		{
			name: "the word include inside a string literal is text, not a call",
			files: map[string]string{
				"gradlew": "",
				// Groovy. The settings file declares nothing; a scanner that searched for the word
				// read this as a declaration and emitted `./gradlew :modules:core:test`.
				"settings.gradle":                   "println(\"include ':modules:core'\")\n",
				"modules/core/build.gradle":         "",
				"modules/core/src/main/java/A.java": "",
			},
			wantCommand: "",
		},
		{
			name: "the Kotlin DSL's string literals are text too",
			files: map[string]string{
				"gradlew":                           "",
				"settings.gradle.kts":               "logger.lifecycle(\"include(':modules:core')\")\n",
				"modules/core/build.gradle.kts":     "",
				"modules/core/src/main/java/A.java": "",
			},
			wantCommand: "",
		},
		{
			name: "a real include still declares the project next to a literal that mentions another",
			files: map[string]string{
				"gradlew": "",
				"settings.gradle": "println(\"include ':modules:other'\")\n" +
					"include ':modules:core'\n",
				"modules/core/build.gradle":         "",
				"modules/core/src/main/java/A.java": "",
			},
			wantCommand: "./gradlew :modules:core:test",
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

// TestSearchVerifyRakeTestTaskGeneratorMustNameTheTestTask is the other half of the declaration
// check.
//
// The `task` forms were tightened to a line-anchored declaration; the generator arm was left as a
// bare substring search for "TestTask.new", so it licensed `rake test` on the two shapes that most
// obviously do not declare it:
//
//   - a commented-out generator — `# Rake::TestTask.new` — which is the shape a Rakefile is left in
//     when the task is retired, exactly the case the `task` half already rejects; and
//   - a NAMED generator. Rake::TestTask#initialize takes the task name as its first argument and
//     only DEFAULTS to :test, so `Rake::TestTask.new(:spec)` defines `spec` and nothing else. The
//     emitted `rake test` then dies on "Don't know how to build task 'test'" — the hard-gate failure
//     the declaration check exists to prevent.
//
// The rule is the same one the `task` half uses: line-anchored (so a comment, prose or a shell line
// inside another task cannot license it), and the name must actually be `test` — either omitted, in
// which case Rake's default applies, or written out as `:test` / `"test"` / the `test:` dependency
// key.
//
// Line anchoring is also what made the tightened arm too strict: `Rake::TestTask.new(\n  :test\n)`
// is an ordinary way to format the call, and declining it loses a `rake test` that would have run.
// The separator therefore spans newlines INSIDE the parentheses only, so the multi-line spellings
// are read and the decline cases — a commented-out generator, prose, a generator naming something
// else — are declined for the same reason as before, however they are wrapped. Both directions of
// that widening are pinned below.
//
// Not fixed here, and deliberately: a generator nested in `namespace :foo do` defines `foo:test` and
// still matches, because separating it needs block tracking rather than a regex (issue #205). Line
// anchoring admits leading whitespace precisely so that case behaves exactly as it did before.
func TestSearchVerifyRakeTestTaskGeneratorMustNameTheTestTask(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		content string
		want    bool
	}{
		// --- generators that define `test` ---
		{name: "bare generator takes Rake's default name", content: "require 'rake/testtask'\nRake::TestTask.new\n", want: true},
		{name: "default name with a block", content: "Rake::TestTask.new do |t|\n  t.libs << 'test'\nend\n", want: true},
		{name: "default name with a brace block", content: "Rake::TestTask.new { |t| t.verbose = true }\n", want: true},
		{name: "default name with empty parentheses", content: "Rake::TestTask.new()\n", want: true},
		{name: "empty parentheses and a block", content: "Rake::TestTask.new() do |t|\nend\n", want: true},
		{name: "default name with a trailing comment", content: "Rake::TestTask.new # defines :test\n", want: true},
		{name: "explicit symbol name", content: "Rake::TestTask.new(:test)\n", want: true},
		{name: "explicit symbol name without parentheses", content: "Rake::TestTask.new :test\n", want: true},
		{name: "explicit symbol name with a block", content: "Rake::TestTask.new(:test) do |t|\n  t.warning = false\nend\n", want: true},
		{name: "explicit double-quoted name", content: "Rake::TestTask.new(\"test\")\n", want: true},
		{name: "explicit single-quoted name", content: "Rake::TestTask.new('test') do |t|\nend\n", want: true},
		{name: "hashrocket dependency form", content: "Rake::TestTask.new(:test => :compile)\n", want: true},
		{name: "hash-argument dependency form", content: "Rake::TestTask.new(test: :compile)\n", want: true},
		{name: "Minitest's generator", content: "require 'minitest/test_task'\nMinitest::TestTask.new(:test)\n", want: true},
		{name: "unqualified after include Rake", content: "include Rake::DSL\nTestTask.new\n", want: true},
		{name: "indented under a conditional", content: "if ENV['CI']\n  Rake::TestTask.new(:test)\nend\n", want: true},

		// --- the same generators formatted across lines: still declarations ---
		{name: "multiline symbol name", content: "Rake::TestTask.new(\n  :test\n)\n", want: true},
		{name: "multiline symbol name with a block", content: "Rake::TestTask.new(\n  :test\n) do |t|\n  t.warning = false\nend\n", want: true},
		{name: "multiline quoted name", content: "Rake::TestTask.new(\n  \"test\"\n)\n", want: true},
		{name: "multiline single-quoted name", content: "Rake::TestTask.new(\n  'test'\n)\n", want: true},
		{name: "multiline hashrocket dependency", content: "Rake::TestTask.new(\n  :test => :compile\n)\n", want: true},
		{name: "multiline hash-argument dependency", content: "Rake::TestTask.new(\n  test: :compile,\n)\n", want: true},
		{name: "multiline empty parentheses", content: "Rake::TestTask.new(\n)\n", want: true},
		{name: "multiline default name with a block", content: "Rake::TestTask.new(\n) do |t|\n  t.libs << 'test'\nend\n", want: true},
		{name: "multiline under a conditional", content: "if ENV['CI']\n  Rake::TestTask.new(\n    :test\n  )\nend\n", want: true},
		{name: "multiline Minitest generator", content: "Minitest::TestTask.new(\n  :test\n)\n", want: true},

		// --- generators that define something else, or nothing at all ---
		{name: "commented-out generator", content: "# Rake::TestTask.new\ntask :lint\n", want: false},
		{name: "commented-out named generator", content: "  # Rake::TestTask.new(:test)\ntask :lint\n", want: false},
		{name: "prose mentioning the generator", content: "# use Rake::TestTask.new to add one\n", want: false},
		{name: "generator named spec", content: "require 'rake/testtask'\nRake::TestTask.new(:spec)\n", want: false},
		{name: "generator named spec as a string", content: "Rake::TestTask.new(\"spec\")\n", want: false},
		{name: "generator named integration with a block", content: "Rake::TestTask.new(:integration) do |t|\nend\n", want: false},
		{name: "generator named with the test prefix", content: "Rake::TestTask.new(:test_all)\n", want: false},
		{name: "generator named with a hash key that is not test", content: "Rake::TestTask.new(spec: :compile)\n", want: false},
		{name: "generator named from a variable", content: "name = :spec\nRake::TestTask.new(name)\n", want: false},
		{name: "a different generator entirely", content: "require 'rspec/core/rake_task'\nRSpec::Core::RakeTask.new(:spec)\n", want: false},
		{name: "a generator inside a shell line", content: "task :lint do\n  sh 'ruby -e \"Rake::TestTask.new\"'\nend\n", want: false},

		// --- and formatting across lines does not license a generator that names something else ---
		{name: "multiline generator named spec", content: "Rake::TestTask.new(\n  :spec\n)\n", want: false},
		{name: "multiline generator named spec as a string", content: "Rake::TestTask.new(\n  \"spec\"\n)\n", want: false},
		{name: "multiline generator named with the test prefix", content: "Rake::TestTask.new(\n  :test_all\n)\n", want: false},
		{name: "multiline generator named from a variable", content: "name = :spec\nRake::TestTask.new(\n  name\n)\n", want: false},
		{name: "commented-out multiline generator", content: "# Rake::TestTask.new(\n#   :test\n# )\ntask :lint\n", want: false},
		{name: "a different multiline generator entirely", content: "RSpec::Core::RakeTask.new(\n  :test\n)\n", want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := searchVerifyRakefileDefinesTest(testCase.content); got != testCase.want {
				t.Fatalf("searchVerifyRakefileDefinesTest(%q) = %v, want %v", testCase.content, got, testCase.want)
			}
		})
	}
}

// TestSearchVerifyGradleUnparenthesisedIncludeEndsAtTheStatement is the regression for the
// command-expression form of `include` running past its own statement.
//
// A line is not a statement in Groovy. `include ':app'; project(':app').projectDir = file('lib')` is
// the ordinary spelling for "declare :app, and its directory is lib", and a scanner that ended the
// argument list at the newline read `'lib'` — the argument of a different call, past the separator —
// as a third included project. `./gradlew :lib:test` was then advertised for a project the settings
// file never declares, and the hard gate this derivation exists to satisfy fails at run time.
//
// The parenthesised form always stopped at its own `)`, so the two spellings of the SAME include
// disagreed about everything that followed on the line. Both are asserted here, with the same
// expectation, because agreeing is the property that was missing.
func TestSearchVerifyGradleUnparenthesisedIncludeEndsAtTheStatement(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name     string
		settings string
	}{
		{
			name:     "the command-expression form",
			settings: "include ':app'; project(':app').projectDir = file('lib')\n",
		},
		{
			name:     "the parenthesised form it must agree with",
			settings: "include(':app'); project(':app').projectDir = file('lib')\n",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{
				"gradlew":                  "",
				"settings.gradle":          testCase.settings,
				"lib/build.gradle":         "",
				"lib/src/main/java/A.java": "",
				"app/build.gradle":         "",
			}
			evidence := searchVerifyTestEvidence(files)
			got := deriveSearchVerifySuiteCommand(
				searchVerifySubject{sourcePath: "lib/src/main/java/A.java"}, &evidence)
			if got != nil {
				t.Fatalf("command = %q, want silence: nothing in the settings script declares :lib, "+
					"and %q is the argument of the call after the include", got.Command, "lib")
			}
			// The include the statement really does declare is still read, so the terminator ends the
			// argument list rather than discarding it.
			if !searchVerifyGradleSettingsIncludes(testCase.settings, ":app") {
				t.Fatalf("the settings script no longer declares :app: %q", testCase.settings)
			}
		})
	}
}

// TestSearchVerifyGradleIncludeRequiresACallShape is the regression for `include` read as a
// declaration when it is an ordinary NAME.
//
// `val include = ":modules:core"` and `def include = ':modules:core'` bind a variable in the two
// settings DSLs. The scanner matched the identifier and then collected the literal on the right of
// the `=` as an argument, so the settings script was read as declaring `:modules:core` and
// `./gradlew :modules:core:test` was advertised for a project that does not exist — Gradle answers
// "Project 'modules' not found in root project", and the hard gate this derivation exists to satisfy
// cannot run. An argument list starts at `(` or, in the command-expression form, at the literal
// itself; nothing else after the identifier is a call.
func TestSearchVerifyGradleIncludeRequiresACallShape(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name     string
		settings string
		declares bool
	}{
		{
			name:     "a Kotlin val named include is not a call",
			settings: "val include = \":modules:core\"\n",
		},
		{
			name:     "a Groovy def named include is not a call",
			settings: "def include = ':modules:core'\n",
		},
		{
			name:     "an assignment to include is not a call",
			settings: "include = ':modules:core'\n",
		},
		{
			name:     "the parenthesised call still declares",
			settings: "include(\":modules:core\")\n",
			declares: true,
		},
		{
			name:     "the command-expression call still declares",
			settings: "include ':modules:core'\n",
			declares: true,
		},
		{
			name:     "a call whose parenthesis is spaced off still declares",
			settings: "include (':modules:core')\n",
			declares: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := searchVerifyGradleSettingsIncludes(testCase.settings, ":modules:core"); got != testCase.declares {
				t.Fatalf("declares(:modules:core) = %v, want %v for %q",
					got, testCase.declares, testCase.settings)
			}
			files := map[string]string{
				"gradlew":                           "",
				"settings.gradle":                   testCase.settings,
				"modules/core/build.gradle":         "",
				"modules/core/src/main/java/A.java": "",
			}
			evidence := searchVerifyTestEvidence(files)
			got := deriveSearchVerifySuiteCommand(
				searchVerifySubject{sourcePath: "modules/core/src/main/java/A.java"}, &evidence)
			if !testCase.declares {
				if got != nil {
					t.Fatalf("command = %q, want silence: %q binds a variable and declares no project",
						got.Command, testCase.settings)
				}
				return
			}
			if got == nil {
				t.Fatal("expected the declared project's command, got silence")
			}
			if want := "./gradlew :modules:core:test"; got.Command != want {
				t.Fatalf("command = %q, want %q", got.Command, want)
			}
		})
	}
}

// TestSearchVerifyMavenReactorWalkIsNotStoppedByAWideRoot is the regression for the reactor walk
// giving up on breadth.
//
// Membership is the transitive closure of the `<modules>` lists, and the walk bounded the set of
// DISCOVERED modules rather than the POMs it opened. A root aggregator declaring more modules than
// that bound queued them all on its first pass and the walk stopped there, so nothing declared one
// level down was ever reached: a real reactor module was reported undeclared and handed
// `cd <dir> && mvn test`, which resolves the module's siblings from the local repository instead of
// building them. Breadth costs no reads; `visited` is what makes the walk terminate.
func TestSearchVerifyMavenReactorWalkIsNotStoppedByAWideRoot(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"services/pom.xml":     mavenAggregator("api"),
		"services/api/pom.xml": "<project/>",
		"services/api/src/main/java/com/example/Handler.java": "",
	}
	// A flat root wide enough that the nested aggregator is the last thing the walk would reach.
	modules := make([]string, 0, 129)
	for index := 0; index < 128; index++ {
		module := "leaf" + string(rune('a'+index/26)) + string(rune('a'+index%26))
		files[module+"/pom.xml"] = "<project/>"
		modules = append(modules, module)
	}
	files["pom.xml"] = mavenAggregator(append(modules, "services")...)

	evidence := searchVerifyTestEvidence(files)
	got := deriveSearchVerifySuiteCommand(
		searchVerifySubject{sourcePath: "services/api/src/main/java/com/example/Handler.java"}, &evidence)
	if got == nil {
		t.Fatal("expected a Maven command, got silence")
	}
	if want := "mvn -q -pl services/api -am test"; got.Command != want {
		t.Fatalf("command = %q, want %q: services/api is declared through services, "+
			"and %d sibling modules at the root do not unmake that", got.Command, want, len(modules))
	}
}

// TestSearchVerifyNodeAncestorLockfileNeedsWorkspaceMembership is the regression for adopting an
// ancestor's manager for a package that is not in its project.
//
// Yarn ≥2 resolves the project by walking up to the nearest lockfile and then REFUSES to run when
// the package it was invoked in is not part of it: "The nearest package directory (…) doesn't seem
// to be part of the project declared in (…)". So a standalone package under an unrelated Yarn
// project was handed `cd <leaf> && yarn test`, a command that cannot run at all, where `npm test` —
// the floor this block already falls back to — runs.
//
// Membership is TRANSITIVE, because a workspace may declare workspaces of its own: under a root
// declaring `packages/*`, whether `packages/app/examples/demo` is in the project is answered by
// `packages/app`'s manifest, not by the fact that some ancestor matched. Both nested directions are
// pinned below.
//
// Where a declaration cannot be READ the check is permissive, because declining wrongly replaces a
// working command with one Plug'n'Play cannot run.
func TestSearchVerifyNodeAncestorLockfileNeedsWorkspaceMembership(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		rootPackage string
		leaf        string
		extraFiles  map[string]string
		wantCommand string
	}{
		{
			name:        "an unrelated Yarn project above a standalone package",
			rootPackage: `{"name":"root","private":true}`,
			leaf:        "tools/scratch",
			wantCommand: "cd tools/scratch && npm test",
		},
		{
			name:        "a workspace whose patterns do not reach the package",
			rootPackage: `{"name":"root","workspaces":["packages/*"]}`,
			leaf:        "tools/scratch",
			wantCommand: "cd tools/scratch && npm test",
		},
		{
			name:        "a declared workspace keeps the workspace's manager",
			rootPackage: `{"name":"root","workspaces":["packages/*"]}`,
			leaf:        "packages/api",
			wantCommand: "cd packages/api && yarn test",
		},
		{
			name:        "the object spelling of the same declaration",
			rootPackage: `{"name":"root","workspaces":{"packages":["packages/*"]}}`,
			leaf:        "packages/api",
			wantCommand: "cd packages/api && yarn test",
		},
		{
			name:        "a nested workspace the matched workspace declares in turn",
			rootPackage: `{"name":"root","workspaces":["packages/*"]}`,
			leaf:        "packages/app/examples/demo",
			extraFiles: map[string]string{
				"packages/app/package.json": `{"name":"app","workspaces":["examples/*"]}`,
			},
			wantCommand: "cd packages/app/examples/demo && yarn test",
		},
		{
			name:        "a package nested below a workspace that does not declare it",
			rootPackage: `{"name":"root","workspaces":["packages/*"]}`,
			leaf:        "packages/app/examples/demo",
			extraFiles: map[string]string{
				"packages/app/package.json": `{"name":"app"}`,
			},
			wantCommand: "cd packages/app/examples/demo && npm test",
		},
		{
			name:        "a globstar reaches any depth",
			rootPackage: `{"name":"root","workspaces":["**"]}`,
			leaf:        "tools/scratch",
			wantCommand: "cd tools/scratch && yarn test",
		},
		{
			// Yarn globs with micromatch, so brace expansion is a valid pattern that path.Match
			// reads as literal text. A pattern this cannot express must not be read as a decline.
			name:        "brace expansion is not answered, so the manager is kept",
			rootPackage: `{"name":"root","workspaces":["{packages,tools}/*"]}`,
			leaf:        "tools/scratch",
			wantCommand: "cd tools/scratch && yarn test",
		},
		{
			name:        "an extglob group is not answered either",
			rootPackage: `{"name":"root","workspaces":["+(packages|tools)/*"]}`,
			leaf:        "tools/scratch",
			wantCommand: "cd tools/scratch && yarn test",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{
				"package.json":                  testCase.rootPackage,
				"yarn.lock":                     "__metadata:\n  version: 8\n",
				testCase.leaf + "/package.json": `{"name":"leaf","devDependencies":{"jest":"^29.0.0"}}`,
				testCase.leaf + "/src/index.js": "",
			}
			for name, content := range testCase.extraFiles {
				files[name] = content
			}
			evidence := searchVerifyTestEvidence(files)
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

// TestSearchVerifyGradleTripleQuotedBlockIsNotCode is the regression for a multi-line string read as
// code by the settings scanner.
//
// Both DSLs spell a multi-line string with a triple delimiter, and an ordinary one-line literal
// terminates at the newline — so the body of such a block sat in code position and an `include` in
// it was read as a declaration. `./gradlew :lib:test` was then advertised for a project the settings
// script never declares, which Gradle answers with "Project 'lib' not found in root project".
//
// The single-LINE spelling was already declined, for a different reason: the scanner consumed
// `"include("` as an ordinary literal, so the identifier never reached code position. It is kept
// below so both spellings are pinned to the same answer rather than agreeing by accident.
func TestSearchVerifyGradleTripleQuotedBlockIsNotCode(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name     string
		settings string
	}{
		{
			name:     "the multi-line Kotlin spelling",
			settings: "include(\":app\")\nval example = \"\"\"\ninclude(\":lib\")\n\"\"\"\n",
		},
		{
			name:     "the multi-line Groovy spelling",
			settings: "include ':app'\ndef example = '''\ninclude ':lib'\n'''\n",
		},
		{
			name:     "the single-line spelling it must agree with",
			settings: "include(\":app\")\nval example = \"\"\"include(\":lib\")\"\"\"\n",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if searchVerifyGradleSettingsIncludes(testCase.settings, ":lib") {
				t.Fatalf("settings declare :lib, but the only mention of it is inside a string: %q",
					testCase.settings)
			}
			// The include outside the block is still read, so the block is skipped rather than the
			// scan being abandoned at it.
			if !searchVerifyGradleSettingsIncludes(testCase.settings, ":app") {
				t.Fatalf("the settings script no longer declares :app: %q", testCase.settings)
			}
			files := map[string]string{
				"gradlew":                  "",
				"settings.gradle":          testCase.settings,
				"app/build.gradle":         "",
				"lib/build.gradle":         "",
				"lib/src/main/java/A.java": "",
			}
			evidence := searchVerifyTestEvidence(files)
			got := deriveSearchVerifySuiteCommand(
				searchVerifySubject{sourcePath: "lib/src/main/java/A.java"}, &evidence)
			if got != nil {
				t.Fatalf("command = %q, want silence: :lib appears only inside a string literal",
					got.Command)
			}
		})
	}
}

// TestSearchVerifyGradleTripleQuotedIncludeArgumentIsRead pins the other half: a triple-quoted
// literal PASSED to include is an ordinary argument once its delimiter is stripped.
func TestSearchVerifyGradleTripleQuotedIncludeArgumentIsRead(t *testing.T) {
	t.Parallel()
	settings := "include(\"\"\":lib\"\"\")\n"
	if !searchVerifyGradleSettingsIncludes(settings, ":lib") {
		t.Fatalf("settings %q declare :lib and the scanner did not read it", settings)
	}
}

// TestSearchVerifyGradleRemappedProjectIsNotThisDirectory is the regression for a project path that
// is declared but does not live where it was derived from.
//
// The suite tier derives `:lib` from the directory `lib/` and confirms it against `include`. Gradle
// lets a settings script move it — `project(':lib').projectDir = file('other')` — and then `:lib` is
// a different tree. `./gradlew :lib:test` for an edit in `lib/` RUNS, and passes, about code the
// edit never touched: worse than a command that cannot run, because nothing announces it.
//
// The control matters as much as the cases: an ordinary `include ':lib'` with no remap must keep its
// command, so the check declines on the remap rather than on the mention of a project() call.
func TestSearchVerifyGradleRemappedProjectIsNotThisDirectory(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name     string
		settings string
		want     string
	}{
		{
			name:     "the Groovy assignment",
			settings: "include ':lib'\nproject(':lib').projectDir = file('other')\n",
		},
		{
			name:     "the Kotlin assignment",
			settings: "include(\":lib\")\nproject(\":lib\").projectDir = file(\"other\")\n",
		},
		{
			name:     "the block spelling of the same assignment",
			settings: "include ':lib'\nproject(':lib') {\n    projectDir = file('other')\n}\n",
		},
		{
			name:     "the same statement, semicolon separated",
			settings: "include ':lib'; project(':lib').projectDir = file('other')\n",
		},
		{
			name:     "a chained access continued onto the next line",
			settings: "include(\":lib\")\nproject(\":lib\")\n    .projectDir = file(\"other\")\n",
		},
		{
			name:     "an unremapped project keeps its command",
			settings: "include ':lib'\n",
			want:     "./gradlew :lib:test",
		},
		{
			name:     "a remap of a DIFFERENT project is not this one's",
			settings: "include ':lib'\ninclude ':app'\nproject(':app').projectDir = file('other')\n",
			want:     "./gradlew :lib:test",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{
				"gradlew":                  "",
				"settings.gradle":          testCase.settings,
				"lib/build.gradle":         "",
				"lib/src/main/java/A.java": "",
				"app/build.gradle":         "",
				"other/build.gradle":       "",
			}
			evidence := searchVerifyTestEvidence(files)
			got := deriveSearchVerifySuiteCommand(
				searchVerifySubject{sourcePath: "lib/src/main/java/A.java"}, &evidence)
			if testCase.want == "" {
				if got != nil {
					t.Fatalf("command = %q, want silence: the settings script moves :lib to another "+
						"directory, so it does not name this one", got.Command)
				}
				return
			}
			if got == nil {
				t.Fatal("expected the declared project's command, got silence")
			}
			if got.Command != testCase.want {
				t.Fatalf("command = %q, want %q", got.Command, testCase.want)
			}
		})
	}
}
