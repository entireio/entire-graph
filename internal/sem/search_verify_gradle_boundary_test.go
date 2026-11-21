package sem

import "testing"

// searchVerifyGradleBoundaryCase is one fixture read through BOTH tiers. The source and covering
// test are named explicitly because several rows put them in different projects.
type searchVerifyGradleBoundaryCase struct {
	name       string
	files      map[string]string
	sourcePath string
	testPath   string
	wantNarrow string
	wantSuite  string
}

func runSearchVerifyGradleBoundaryCases(t *testing.T, cases []searchVerifyGradleBoundaryCase) {
	t.Helper()
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{testCase.sourcePath: "", testCase.testPath: ""}
			for filePath, content := range testCase.files {
				files[filePath] = content
			}
			subject := searchVerifySubject{
				sourcePath:   testCase.sourcePath,
				testPath:     testCase.testPath,
				testEvidence: "covering test",
			}
			for _, tier := range []struct {
				name   string
				derive func(searchVerifySubject, *searchVerifyEvidence) *SearchVerifyCommand
				want   string
			}{
				{"narrow", deriveSearchVerifyCommand, testCase.wantNarrow},
				{"suite", deriveSearchVerifySuiteCommand, testCase.wantSuite},
			} {
				evidence := searchVerifyTestEvidence(files)
				command := ""
				if got := tier.derive(subject, &evidence); got != nil {
					command = got.Command
				}
				if command != tier.want {
					t.Errorf("%s command = %q, want %q", tier.name, command, tier.want)
				}
			}
		})
	}
}

// TestSearchVerifyGradleDeclinedProjectDoesNotClimb pins the boundary the settings-inclusion check
// was missing (cases adapted from #249).
//
// Both tiers decline at a `build.gradle` directory the enclosing settings script does not declare.
// The walk then carried on to the parent, and a Gradle build standing THERE answered instead:
// `./gradlew :test --tests 'ATest'` and `./gradlew test` at the root, for a file in `lib/` that the
// root build does not contain. The suite spelling is the bad one — it runs, and passes, without
// compiling the edit. A directory that is a Gradle project nobody can be shown to own ends the
// GRADLE walk for that subject; other build systems above it are still asked.
func TestSearchVerifyGradleDeclinedProjectDoesNotClimb(t *testing.T) {
	t.Parallel()
	const (
		libSource = "lib/src/main/java/A.java"
		libTest   = "lib/src/test/java/ATest.java"
	)
	runSearchVerifyGradleBoundaryCases(t, []searchVerifyGradleBoundaryCase{
		{
			name: "an undeclared project does not borrow the root build (Groovy)",
			files: map[string]string{
				"gradlew":          "",
				"settings.gradle":  "include ':app'\n",
				"build.gradle":     "",
				"app/build.gradle": "",
				"lib/build.gradle": "",
			},
			sourcePath: libSource, testPath: libTest,
		},
		{
			name: "an undeclared project does not borrow the root build (Kotlin)",
			files: map[string]string{
				"gradlew":              "",
				"settings.gradle.kts":  "include(\":app\")\n",
				"build.gradle.kts":     "",
				"app/build.gradle.kts": "",
				"lib/build.gradle.kts": "",
			},
			sourcePath: libSource, testPath: libTest,
		},
		{
			name: "an undeclared child does not borrow its declared parent",
			files: map[string]string{
				"gradlew":                   "",
				"settings.gradle":           "include ':parent'\n",
				"parent/build.gradle":       "",
				"parent/child/build.gradle": "",
			},
			sourcePath: "parent/child/src/main/java/A.java",
			testPath:   "parent/child/src/test/java/ATest.java",
		},
		{
			name: "a renamed project does not borrow the root build",
			files: map[string]string{
				"gradlew":          "",
				"settings.gradle":  "include ':lib'\nproject(':lib').name = 'core'\n",
				"build.gradle":     "",
				"lib/build.gradle": "",
			},
			sourcePath: libSource, testPath: libTest,
		},
		{
			// The narrow command names a test CLASS, and the class lives in a project the root build
			// does not contain. The source is the root project's, so its suite is still its suite.
			name: "a covering test in an undeclared project is not run through the root project",
			files: map[string]string{
				"gradlew":          "",
				"settings.gradle":  "include ':app'\n",
				"build.gradle":     "",
				"lib/build.gradle": "",
			},
			sourcePath: "src/main/java/A.java", testPath: libTest,
			wantSuite: "./gradlew test",
		},

		// --- the emit direction: a project the build owns still climbs and still emits ---
		{
			name: "a declared project keeps its project path",
			files: map[string]string{
				"gradlew":          "",
				"settings.gradle":  "include ':app', ':lib'\n",
				"build.gradle":     "",
				"lib/build.gradle": "",
			},
			sourcePath: libSource, testPath: libTest,
			wantNarrow: "./gradlew :lib:test --tests 'ATest'",
			wantSuite:  "./gradlew :lib:test",
		},
		{
			// The narrow tier declines at `lib` because the test is not inside it, not because of
			// ownership, so the root project — which does hold the test — still answers.
			name: "a declared project whose covering test is the root project's still climbs",
			files: map[string]string{
				"gradlew":             "",
				"settings.gradle.kts": "include(\":lib\")\n",
				"build.gradle.kts":    "",
				"lib/build.gradle":    "",
			},
			sourcePath: libSource, testPath: "src/test/java/ATest.java",
			wantNarrow: "./gradlew :test --tests 'ATest'",
			wantSuite:  "./gradlew :lib:test",
		},
		{
			name: "a directory with no build script is part of the project above it",
			files: map[string]string{
				"gradlew":         "",
				"settings.gradle": "include ':app'\n",
				"build.gradle":    "",
			},
			sourcePath: libSource, testPath: libTest,
			wantNarrow: "./gradlew :test --tests 'ATest'",
			wantSuite:  "./gradlew test",
		},

		// --- the boundary is Gradle's: another build system above it is still consulted ---
		{
			name: "a Maven build above an undeclared Gradle directory still answers",
			files: map[string]string{
				"gradlew":             "",
				"settings.gradle":     "include ':app'\n",
				"build.gradle":        "",
				"pom.xml":             "<project/>",
				"legacy/build.gradle": "",
			},
			sourcePath: "legacy/src/main/java/A.java",
			testPath:   "legacy/src/test/java/ATest.java",
			wantNarrow: "mvn -q -Dtest=ATest -DfailIfNoTests=false test",
			wantSuite:  "mvn -q test",
		},
	})
}

// TestSearchVerifyGradleDeclinedProjectReportsNone is the payload half: with no Gradle command and
// no other build system, the block says so instead of naming the ancestor build.
func TestSearchVerifyGradleDeclinedProjectReportsNone(t *testing.T) {
	t.Parallel()
	for _, settings := range []struct{ name, content, build string }{
		{"settings.gradle", "include ':app'\n", "build.gradle"},
		{"settings.gradle.kts", "include(\":app\")\n", "build.gradle.kts"},
	} {
		t.Run(settings.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{
				"gradlew":                      "",
				settings.name:                  settings.content,
				settings.build:                 "",
				"lib/" + settings.build:        "",
				"lib/src/main/java/A.java":     "",
				"lib/src/test/java/ATest.java": "",
			}
			got := buildSearchVerifyCommand([]SearchResult{
				{Rank: 1, FilePath: "lib/src/main/java/A.java", Section: searchSectionPrimary},
				{Rank: 2, FilePath: "lib/src/test/java/ATest.java", Section: searchSectionCoveringTest},
			}, searchVerifyTestEvidence(files))
			if got == nil {
				t.Fatal("expected an explicit verification result")
			}
			if got.Tier != searchVerifyTierNone || got.Command != searchVerifyNoneCommand {
				t.Fatalf("tier = %q command = %q, want the none outcome", got.Tier, got.Command)
			}
		})
	}
}

// TestSearchVerifyGradleRenamedProjectDeclines pins the other way a settings script breaks the
// directory-to-project-path derivation (cases adapted from #249).
//
// `project(':lib').name = 'core'` leaves the project where it is and changes what it is CALLED: its
// path is now `:core`, and `./gradlew :lib:test` answers "Project 'lib' not found". Renaming a
// PARENT does the same to every path below it, because a project path is the names of its
// ancestors. Relocating a parent is declined too: whether the children move with it depends on
// whether they were included before or after the assignment, which this does not model.
func TestSearchVerifyGradleRenamedProjectDeclines(t *testing.T) {
	t.Parallel()
	const (
		libNarrow     = "./gradlew :lib:test --tests 'ATest'"
		libSuite      = "./gradlew :lib:test"
		modulesNarrow = "./gradlew :modules:lib:test --tests 'ATest'"
		modulesSuite  = "./gradlew :modules:lib:test"
	)
	type row struct {
		name     string
		settings string
		kotlin   bool
		emits    bool
	}
	for _, group := range []struct {
		name       string
		dir        string
		wantNarrow string
		wantSuite  string
		rows       []row
	}{
		{
			name: "the project itself", dir: "lib", wantNarrow: libNarrow, wantSuite: libSuite,
			rows: []row{
				{name: "unchanged", settings: "include ':lib'\n", emits: true},
				{name: "renamed by assignment", settings: "include ':lib'\nproject(':lib').name = 'core'\n"},
				{name: "renamed by setter", settings: "include ':lib'\nproject(':lib').setName('core')\n"},
				{name: "renamed in a block", settings: "include ':lib'\nproject(':lib') {\n  name = 'core'\n}\n"},
				{name: "renamed in Kotlin", settings: "include(\":lib\")\nproject(\":lib\").name = \"core\"\n", kotlin: true},
				{
					name:     "renamed on a continued line in Kotlin",
					settings: "include(\":lib\")\nproject(\":lib\")\n    .name = \"core\"\n", kotlin: true,
				},
				{name: "a name that is only read", settings: "include ':lib'\nprintln(project(':lib').name)\n", emits: true},
				{
					name:     "a name that is only compared",
					settings: "include ':lib'\nif (project(':lib').name == 'lib') { }\n", emits: true,
				},
				{
					name:     "a rename that is only text",
					settings: "include ':lib'\nproject(':lib') { println(\"name = 'core'; setName('core')\") }\n", emits: true,
				},
				{
					name:     "a different property ending in name",
					settings: "include ':lib'\nproject(':lib').buildFileName = 'lib.gradle'\n", emits: true,
				},
				{
					name:     "a sibling that is renamed",
					settings: "include ':lib', ':app'\nproject(':app').name = 'core'\n", emits: true,
				},
			},
		},
		{
			name: "a parent project", dir: "modules/lib", wantNarrow: modulesNarrow, wantSuite: modulesSuite,
			rows: []row{
				{name: "unchanged", settings: "include ':modules:lib'\n", emits: true},
				{name: "renamed parent", settings: "include ':modules:lib'\nproject(':modules').name = 'components'\n"},
				{name: "renamed parent by setter", settings: "include ':modules:lib'\nproject(':modules').setName('components')\n"},
				{
					name:     "renamed parent in Kotlin",
					settings: "include(\":modules:lib\")\nproject(\":modules\").name = \"components\"\n", kotlin: true,
				},
				{name: "relocated parent", settings: "include ':modules:lib'\nproject(':modules').projectDir = file('other')\n"},
				{
					name:     "relocated parent by setter in Kotlin",
					settings: "include(\":modules:lib\")\nproject(\":modules\").setProjectDir(file(\"other\"))\n", kotlin: true,
				},
				{
					name:     "a similarly named sibling of the parent",
					settings: "include ':modules:lib', ':module'\nproject(':module').name = 'component'\n", emits: true,
				},
				{
					// The root project's name is not a segment of any project path.
					name:     "a renamed root project",
					settings: "include ':modules:lib'\nproject(':').name = 'components'\nrootProject.name = 'x'\n", emits: true,
				},
			},
		},
	} {
		cases := make([]searchVerifyGradleBoundaryCase, 0, len(group.rows))
		for _, row := range group.rows {
			settingsName, buildName := "settings.gradle", "build.gradle"
			if row.kotlin {
				settingsName, buildName = "settings.gradle.kts", "build.gradle.kts"
			}
			testCase := searchVerifyGradleBoundaryCase{
				name: group.name + "/" + row.name,
				files: map[string]string{
					"gradlew":                   "",
					settingsName:                row.settings,
					group.dir + "/" + buildName: "",
				},
				sourcePath: group.dir + "/src/main/java/A.java",
				testPath:   group.dir + "/src/test/java/ATest.java",
			}
			if row.emits {
				testCase.wantNarrow, testCase.wantSuite = group.wantNarrow, group.wantSuite
			}
			cases = append(cases, testCase)
		}
		runSearchVerifyGradleBoundaryCases(t, cases)
	}
}

// TestSearchVerifyGradleIntermediateBuildRoot covers a build root that is neither the directory
// being verified nor the wrapper's: `modules/settings.gradle` declaring `:core`, driven by the
// repository's root wrapper. That settings script is the one that owns `modules/core`, so the
// project is addressed through it with `-p`; and when it does NOT declare the directory, nothing
// above it is asked instead.
func TestSearchVerifyGradleIntermediateBuildRoot(t *testing.T) {
	t.Parallel()
	const (
		source = "modules/core/src/main/java/A.java"
		test   = "modules/core/src/test/java/ATest.java"
	)
	runSearchVerifyGradleBoundaryCases(t, []searchVerifyGradleBoundaryCase{
		{
			name: "an intermediate settings script that declares the project (Groovy)",
			files: map[string]string{
				"gradlew":                   "",
				"modules/settings.gradle":   "include ':core'\n",
				"modules/core/build.gradle": "",
			},
			sourcePath: source, testPath: test,
			wantNarrow: "./gradlew -p modules :core:test --tests 'ATest'",
			wantSuite:  "./gradlew -p modules :core:test",
		},
		{
			name: "an intermediate settings script that declares the project (Kotlin)",
			files: map[string]string{
				"gradlew":                       "",
				"settings.gradle.kts":           "include(\":app\")\n",
				"build.gradle.kts":              "",
				"modules/settings.gradle.kts":   "include(\":core\")\n",
				"modules/core/build.gradle.kts": "",
			},
			sourcePath: source, testPath: test,
			wantNarrow: "./gradlew -p modules :core:test --tests 'ATest'",
			wantSuite:  "./gradlew -p modules :core:test",
		},
		{
			name: "a build root whose name needs quoting stays quoted",
			files: map[string]string{
				"gradlew":                   "",
				"my mods/settings.gradle":   "include ':core'\n",
				"my mods/core/build.gradle": "",
			},
			sourcePath: "my mods/core/src/A.java", testPath: "my mods/core/src/ATest.java",
			wantNarrow: "./gradlew -p 'my mods' :core:test --tests 'ATest'",
			wantSuite:  "./gradlew -p 'my mods' :core:test",
		},
		{
			name: "the wrapper's own settings script still wins when it declares the project",
			files: map[string]string{
				"gradlew":                   "",
				"settings.gradle":           "include ':modules:core'\n",
				"modules/settings.gradle":   "include ':core'\n",
				"modules/core/build.gradle": "",
			},
			sourcePath: source, testPath: test,
			wantNarrow: "./gradlew :modules:core:test --tests 'ATest'",
			wantSuite:  "./gradlew :modules:core:test",
		},
		{
			name: "an intermediate settings script that does not declare the project declines",
			files: map[string]string{
				"gradlew":                   "",
				"settings.gradle":           "include ':app'\n",
				"build.gradle":              "",
				"modules/settings.gradle":   "include ':other'\n",
				"modules/build.gradle":      "",
				"modules/core/build.gradle": "",
			},
			sourcePath: source, testPath: test,
		},
		{
			name: "an intermediate settings script that renames the project declines",
			files: map[string]string{
				"gradlew":                   "",
				"modules/settings.gradle":   "include ':core'\nproject(':core').name = 'kernel'\n",
				"modules/core/build.gradle": "",
			},
			sourcePath: source, testPath: test,
		},
	})
}
