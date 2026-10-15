package sem

import "testing"

// Exercise the actual ancestor/manifest dispatch, rather than only its extension
// predicate: every suite must accept its subject and reject an unrelated one.
func TestSuiteManifestDispatchRequiresMatchingSubject(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, source, test, want string
		files                    map[string]string
	}{
		{"Cargo", "src/widget.rs", "tests/widget.rs", "cargo test", map[string]string{"Cargo.toml": "[package]\nname = \"widget\"\n"}},
		{"Maven", "src/Widget.java", "tests/WidgetTest.java", "mvn -q test", map[string]string{"pom.xml": "<project/>"}},
		{"Gradle", "src/Widget.kt", "tests/WidgetTest.kt", "./gradlew test", map[string]string{"build.gradle": "plugins { id 'java' }\ntest {}", "gradlew": "#!/bin/sh\n"}},
		{"Node", "src/widget.ts", "tests/widget.test.ts", "npm test", map[string]string{"package.json": `{"scripts":{"test":"vitest"}}`}},
		{"Composer", "src/Widget.php", "tests/WidgetTest.php", "vendor/bin/phpunit", map[string]string{"composer.json": "{}", "phpunit.xml": "<phpunit/>"}},
		{"Pytest", "src/widget.py", "tests/test_widget.py", "python -m pytest", map[string]string{"pytest.ini": "[pytest]\n"}},
		{"Ruby", "lib/widget.rb", "spec/widget_spec.rb", "bundle exec rspec", map[string]string{"Gemfile": "source 'https://example.invalid'", ".rspec": ""}},
		{"CMake", "src/widget.cpp", "tests/widget_test.cpp", "cmake -S . -B build >/dev/null && cmake --build build -j4 && ctest --test-dir build --output-on-failure", map[string]string{"CMakeLists.txt": "enable_testing()\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, subject := range []searchVerifySubject{{sourcePath: tc.source}, {sourcePath: "src/widget.unknown", testPath: tc.test}} {
				evidence := searchVerifyTestEvidenceWithout(tc.files)
				got := deriveSearchVerifySuiteCommand(subject, &evidence)
				if got == nil || got.Command != tc.want {
					t.Fatalf("subject %+v: got %+v, want %q", subject, got, tc.want)
				}
			}
			evidence := searchVerifyTestEvidenceWithout(tc.files)
			if got := deriveSearchVerifySuiteCommand(searchVerifySubject{sourcePath: "src/unrelated.unknown"}, &evidence); got != nil {
				t.Fatalf("unrelated subject selected %s: %+v", tc.name, got)
			}
		})
	}
}

func TestSuiteSubjectCannotBeCapturedByUnrelatedManifest(t *testing.T) {
	for _, kind := range []string{"Cargo", "Maven", "Gradle", "Node", "Composer", "Ruby", "CMake"} {
		if searchVerifySuiteApplies(searchVerifySubject{sourcePath: "service.py"}, kind) {
			t.Errorf("Python source selected %s", kind)
		}
	}
	if !searchVerifySuiteApplies(searchVerifySubject{sourcePath: "service.py"}, "Pytest") {
		t.Fatal("Python not recognized")
	}
	if !searchVerifySuiteApplies(searchVerifySubject{sourcePath: "native.c", testPath: "wrapper.test.ts"}, "Node") {
		t.Fatal("covering test not recognized")
	}
}

func TestCargoSuiteRecognizesNativeBridgeHeaders(t *testing.T) {
	for _, extension := range []string{".hh", ".hpp", ".hxx", ".cxx", ".m", ".mm"} {
		t.Run(extension, func(t *testing.T) {
			evidence := searchVerifyTestEvidenceWithout(map[string]string{
				"Cargo.toml": "[package]\nname = \"native_bridge\"\nversion = \"0.1.0\"\n",
			})
			command := deriveSearchVerifySuiteCommand(searchVerifySubject{sourcePath: "include/bridge" + extension}, &evidence)
			if command == nil || command.Command != "cargo test" {
				t.Fatalf("Cargo-only native bridge header: command = %+v, want cargo test", command)
			}
		})
	}
}

func TestRubySuiteRecognizesBuildAndPackageInputs(t *testing.T) {
	for _, subject := range []string{"tasks/check.rake", "widget.gemspec", "Rakefile"} {
		evidence := searchVerifyTestEvidenceWithout(map[string]string{"Rakefile": "task :test do\nend\n"})
		got := deriveSearchVerifySuiteCommand(searchVerifySubject{sourcePath: subject}, &evidence)
		if got == nil || got.Command != "rake test" {
			t.Fatalf("%s: got %+v, want rake test", subject, got)
		}
	}
}
