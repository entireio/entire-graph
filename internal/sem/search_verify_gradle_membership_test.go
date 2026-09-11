package sem

import "testing"

func TestBuildSearchVerifyGradleUndeclaredProjectFallback(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rootBuild bool
	}{
		{"without_root_build", false},
		{"with_root_build", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{
				"gradlew":                      "",
				"settings.gradle":              "include ':app'\n",
				"app/build.gradle":             "",
				"lib/build.gradle":             "",
				"lib/src/main/java/A.java":     "",
				"lib/src/test/java/ATest.java": "",
			}
			if tc.rootBuild {
				files["build.gradle"] = ""
			}
			results := []SearchResult{
				{Rank: 1, FilePath: "lib/src/main/java/A.java", Section: searchSectionPrimary},
				{Rank: 2, FilePath: "lib/src/test/java/ATest.java", Section: searchSectionCoveringTest},
			}
			got := buildSearchVerifyCommand(results, searchVerifyTestEvidence(files))
			if got == nil {
				t.Fatal("expected an explicit verification result")
			}
			t.Logf("tier = %q; generated command = %q", got.Tier, got.Command)
			if got.Tier != searchVerifyTierNone {
				t.Fatalf("tier = %q, want %q: no command is established for the undeclared project", got.Tier, searchVerifyTierNone)
			}
		})
	}
}

func TestSearchVerifyGradleNarrowRootProject(t *testing.T) {
	for _, manifest := range []string{"build.gradle", "build.gradle.kts"} {
		t.Run(manifest, func(t *testing.T) {
			evidence := searchVerifyTestEvidence(map[string]string{
				"gradlew":                  "",
				manifest:                   "",
				"src/main/java/A.java":     "",
				"src/test/java/ATest.java": "",
			})
			got := deriveSearchVerifyCommand(searchVerifySubject{
				sourcePath:   "src/main/java/A.java",
				testPath:     "src/test/java/ATest.java",
				testEvidence: "covering test",
			}, &evidence)
			if got == nil {
				t.Fatal("expected a root project command")
			}
			want := "./gradlew :test --tests 'ATest'"
			t.Logf("generated command = %q", got.Command)
			if got.Command != want {
				t.Fatalf("command = %q, want %q", got.Command, want)
			}
		})
	}
}

func TestSearchVerifyGradleNarrowProjectMembership(t *testing.T) {
	for _, tc := range []struct {
		name      string
		settings  string
		rootBuild bool
		want      string
	}{
		{"included_project", "include ':app', ':lib'\n", false, "./gradlew :lib:test --tests 'ATest'"},
		{"remapped_project", "include ':lib'\nproject(':lib').projectDir = file('other')\n", false, ""},
		{"undeclared_project", "include ':app'\n", false, ""},
		{"undeclared_project_with_root_build", "include ':app'\n", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{
				"gradlew":                      "",
				"settings.gradle":              tc.settings,
				"app/build.gradle":             "",
				"lib/build.gradle":             "",
				"lib/src/main/java/A.java":     "",
				"lib/src/test/java/ATest.java": "",
			}
			if tc.rootBuild {
				files["build.gradle"] = ""
			}
			evidence := searchVerifyTestEvidence(files)
			got := deriveSearchVerifyCommand(searchVerifySubject{
				sourcePath:   "lib/src/main/java/A.java",
				testPath:     "lib/src/test/java/ATest.java",
				testEvidence: "covering test",
			}, &evidence)
			command := ""
			if got != nil {
				command = got.Command
			}
			t.Logf("settings = %q; generated command = %q", tc.settings, command)
			if command != tc.want {
				t.Fatalf("command = %q, want %q", command, tc.want)
			}
		})
	}
}
