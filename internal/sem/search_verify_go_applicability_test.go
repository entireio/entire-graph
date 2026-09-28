package sem

import (
	"fmt"
	"strings"
	"testing"
)

// A sibling Go module is not evidence that a Python path is a Go package.
func TestSearchVerifyGoRequiresGoSubject(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, subject, config, want, tier string
	}{
		{
			name:    "python test without pytest configuration",
			subject: "agentic-swebench/tests/test_launcher.py",
			want:    "python -m py_compile agentic-swebench/tests/test_launcher.py",
			tier:    searchVerifyTierBuildCheck,
		},
		{
			name:    "python source without pytest configuration",
			subject: "tools/launcher.py",
			want:    "python -m py_compile tools/launcher.py",
			tier:    searchVerifyTierBuildCheck,
		},
		{
			name:    "python test with competing pytest and go manifests",
			subject: "agentic-swebench/tests/test_launcher.py", config: "[tool.pytest.ini_options]\n",
			want: "python -m pytest agentic-swebench/tests/test_launcher.py",
			tier: searchVerifyTierNarrow,
		},
		{
			name:    "go source keeps go despite pytest configuration",
			subject: "internal/widget.go", config: "[tool.pytest.ini_options]\n",
			want: "go test ./internal", tier: searchVerifyTierNarrow,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{"go.mod": "module example.com/mixed\n", tc.subject: ""}
			if tc.config != "" {
				files["pyproject.toml"] = tc.config
			}
			evidence := searchVerifyTestEvidence(files)
			got := buildSearchVerifyCommand([]SearchResult{{
				Rank: 1, FilePath: tc.subject, Section: searchSectionPrimary,
			}}, evidence)
			if got == nil || got.Command != tc.want || got.Tier != tc.tier {
				t.Fatalf("VERIFY = %+v, want %q at tier %q", got, tc.want, tc.tier)
			}
		})
	}
}

func TestSearchVerifyGoSuiteRequiresGoSubject(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		subject searchVerifySubject
		wantGo  bool
	}{
		{name: "python", subject: searchVerifySubject{sourcePath: "pkg/widget.py"}},
		{name: "python test", subject: searchVerifySubject{sourcePath: "tests/test_widget.py", testPath: "tests/test_widget.py"}},
		{name: "javascript", subject: searchVerifySubject{sourcePath: "web/widget.js"}},
		{name: "go source", subject: searchVerifySubject{sourcePath: "pkg/widget.go"}, wantGo: true},
		{name: "go covering test", subject: searchVerifySubject{sourcePath: "pkg/widget.c", testPath: "pkg/widget_test.go"}, wantGo: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			evidence := searchVerifyTestEvidence(map[string]string{"go.mod": "module example.com/mixed\n"})
			got := deriveSearchVerifySuiteCommand(tc.subject, &evidence)
			if tc.wantGo {
				if got == nil || got.Command != "go test ./..." {
					t.Fatalf("Go suite = %+v, want go test ./...", got)
				}
			} else if got != nil {
				t.Fatalf("unrelated Go module licensed a suite for %+v: %+v", tc.subject, got)
			}
		})
	}
}

func TestSearchVerifyGoDoesNotUsePythonCoveringTestAsGoPackage(t *testing.T) {
	t.Parallel()
	evidence := searchVerifyTestEvidence(map[string]string{
		"go.mod":               "module example.com/mixed\n",
		"pyproject.toml":       "[tool.pytest.ini_options]\n",
		"pkg/widget.go":        "package pkg\n",
		"tests/test_widget.py": "",
	})
	got := deriveSearchVerifyCommand(searchVerifySubject{
		sourcePath: "pkg/widget.go", testPath: "tests/test_widget.py", testEvidence: "covering test",
	}, &evidence)
	if got == nil || got.Command != "python -m pytest tests/test_widget.py" {
		t.Fatalf("VERIFY = %+v, want the selected Python test, not a Go package named tests", got)
	}
}

func TestSearchVerifyGoMixedLanguageCoveringTests(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, source, test, want, tier string
	}{
		{
			name:   "go source with python test and no pytest runner",
			source: "pkg/widget.go", test: "tests/test_widget.py",
			want: "go test ./...", tier: searchVerifyTierSuite,
		},
		{
			name:   "python source with go covering test",
			source: "tools/widget.py", test: "pkg/widget_test.go",
			want: "go test ./pkg", tier: searchVerifyTierNarrow,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			evidence := searchVerifyTestEvidence(map[string]string{
				"go.mod": "module example.com/mixed\n", tc.source: "", tc.test: "",
			})
			got := buildSearchVerifyCommand([]SearchResult{
				{Rank: 1, FilePath: tc.source, Section: searchSectionPrimary},
				{Rank: 2, FilePath: tc.test, Section: searchSectionCoveringTest},
			}, evidence)
			if got == nil || got.Command != tc.want || got.Tier != tc.tier {
				t.Fatalf("VERIFY = %+v, want %q at tier %q", got, tc.want, tc.tier)
			}
			if tc.tier == searchVerifyTierSuite && got.Targets != "whole test suite (no narrow test command derivable)" {
				t.Fatalf("suite target = %q; a covering test exists, but its runner was not derivable", got.Targets)
			}
		})
	}
}

func TestSearchVerifyGoNativePackageSources(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, source, goSource, goBody string
		wantGo                         bool
		assembly                       bool
	}{
		{name: "cgo C", source: "pkg/wrap.c", goSource: "pkg/wrap.go", goBody: "package pkg\nimport \"C\"\n", wantGo: true},
		{name: "cgo header", source: "pkg/wrap.h", goSource: "pkg/wrap.go", goBody: "package pkg\nimport \"C\"\n", wantGo: true},
		{name: "Go assembly", source: "pkg/sum_amd64.s", goSource: "pkg/sum.go", goBody: "package pkg\nfunc sum()\n", wantGo: true},
		{name: "assembly header", source: "pkg/asm.h", goSource: "pkg/sum.go", goBody: "package pkg\nfunc sum()\n", wantGo: true, assembly: true},
		{name: "cgo C++", source: "pkg/wrap.cpp", goSource: "pkg/wrap.go", goBody: "package pkg\nimport (\"C\")\n", wantGo: true},
		{name: "cgo assembly", source: "pkg/wrap.S", goSource: "pkg/wrap.go", goBody: "package pkg\nimport \"C\"\n", wantGo: true},
		{name: "header without native input", source: "pkg/wrap.h", goSource: "pkg/wrap.go", goBody: "package pkg\n"},
		{name: "C with no package", source: "native/wrap.c", goSource: "pkg/wrap.go", goBody: "package pkg\nimport \"C\"\n"},
		{name: "C with no cgo import", source: "pkg/wrap.c", goSource: "pkg/wrap.go", goBody: "package pkg\n"},
		{name: "cgo import inside comment", source: "pkg/wrap.c", goSource: "pkg/wrap.go", goBody: "package pkg\n/*\nimport \"C\"\n*/\n"},
		{name: "cgo import inside string", source: "pkg/wrap.c", goSource: "pkg/wrap.go", goBody: "package pkg\nconst text = `\nimport \"C\"\n`\n"},
		{name: "test-only cgo import", source: "pkg/wrap.c", goSource: "pkg/wrap_test.go", goBody: "package pkg\nimport \"C\"\n"},
		{name: "Python beside Go", source: "pkg/script.py", goSource: "pkg/wrap.go", goBody: "package pkg\nimport \"C\"\n"},
		{name: "JavaScript beside Go", source: "pkg/script.js", goSource: "pkg/wrap.go", goBody: "package pkg\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{
				"go.mod": "module example.com/mixed\n", tc.source: "", tc.goSource: tc.goBody,
			}
			if tc.assembly {
				files["pkg/sum_amd64.s"] = "// Go assembly input\n"
			}
			evidence := searchVerifyTestEvidence(files)
			evidence.files = []string{tc.source, tc.goSource, "go.mod"}
			if tc.assembly {
				evidence.files = append(evidence.files, "pkg/sum_amd64.s")
			}
			got := buildSearchVerifyCommand([]SearchResult{{
				Rank: 1, FilePath: tc.source, Section: searchSectionPrimary,
			}}, evidence)
			if tc.wantGo {
				if got == nil || got.Command != "go test ./pkg" || got.Tier != searchVerifyTierNarrow {
					t.Fatalf("native Go package VERIFY = %+v, want go test ./pkg", got)
				}
				foundSource := false
				for _, file := range got.provenancePaths {
					foundSource = foundSource || file == tc.goSource
				}
				if !foundSource {
					t.Fatalf("Go ownership evidence %q missing from provenance: %v", tc.goSource, got.provenancePaths)
				}
				if tc.assembly && !containsString(got.provenancePaths, "pkg/sum_amd64.s") {
					t.Fatalf("assembly header witness missing from provenance: %v", got.provenancePaths)
				}
			} else if got != nil && strings.HasPrefix(got.Command, "go test ") {
				t.Fatalf("unrelated Go evidence licensed a command: %+v", got)
			}
		})
	}
}

func TestSearchVerifyGoNativeInventoryReachesPublicSearch(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "go.mod", "module example.com/native\n\ngo 1.22\n")
	write(t, repo, "pkg/wrap.go", "package pkg\nimport \"C\"\n")
	write(t, repo, "pkg/wrap.c", "int NativeAccumulator(int input) { return input + 1; }\n")
	response, err := SearchRepository(t.Context(), repo, "native-verify-fixture", "NativeAccumulator", SearchOptions{
		Worktree: true, Profile: ProfileFull, TopK: 1, CacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) == 0 || response.Results[0].FilePath != "pkg/wrap.c" {
		t.Fatalf("fixture must select the native input, got %+v", response.Results)
	}
	if got := response.VerifyCommand; got == nil || got.Command != "go test ./pkg" || got.Tier != searchVerifyTierNarrow {
		t.Fatalf("public search lost native-package evidence: %+v", got)
	}
}

func TestSearchVerifyGoNativeEvidenceBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, candidate, body string
		missing, exhausted    bool
	}{
		{name: "missing inventory file", candidate: "pkg/wrap.go", missing: true},
		{name: "invalid Go package", candidate: "pkg/wrap.go", body: "not a package\n"},
		{name: "ignored Go file", candidate: "pkg/_wrap.go", body: "package pkg\nimport \"C\"\n"},
		{name: "hidden Go file", candidate: "pkg/.wrap.go", body: "package pkg\nimport \"C\"\n"},
		{name: "exhausted read budget", candidate: "pkg/wrap.go", body: "package pkg\nimport \"C\"\n", exhausted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{}
			if !tc.missing {
				files[tc.candidate] = tc.body
			}
			evidence := searchVerifyTestEvidence(files)
			evidence.files = []string{tc.candidate}
			if tc.exhausted {
				evidence.reads = searchVerifyMaxReads
			}
			if searchVerifyGoApplies("pkg/wrap.c", &evidence) {
				t.Fatal("missing or unusable package evidence licensed Go")
			}
			reads := evidence.reads
			if searchVerifyGoApplies("pkg/wrap.c", &evidence) || evidence.reads != reads {
				t.Fatal("repeated applicability probe changed its answer or read again")
			}
			if evidence.reads > searchVerifyMaxReads {
				t.Fatal("native evidence exceeded the shared read budget")
			}
		})
	}
}

func TestSearchVerifyGoNativeFixtureIsNotVerificationSubject(t *testing.T) {
	t.Parallel()
	evidence := searchVerifyTestEvidence(map[string]string{
		"go.mod":                 "module example.com/fixtures\n",
		"pkg/testdata/prog.go":   "package main\nimport \"C\"\n",
		"pkg/testdata/fixture.c": "int fixture(void) { return 1; }\n",
	})
	evidence.files = []string{"go.mod", "pkg/testdata/prog.go", "pkg/testdata/fixture.c"}
	for _, subject := range evidence.files[1:] {
		got := buildSearchVerifyCommand([]SearchResult{{
			Rank: 1, FilePath: subject, Section: searchSectionPrimary,
		}}, evidence)
		if got == nil || got.Tier != searchVerifyTierNone {
			t.Fatalf("fixture %q became a verification subject: %+v", subject, got)
		}
	}
}

func TestSearchVerifyGoNativeProbePreservesManifestBudget(t *testing.T) {
	t.Parallel()
	for _, initialReads := range []int{0, searchVerifyMaxReads - 4} {
		t.Run(fmt.Sprint(initialReads), func(t *testing.T) {
			t.Parallel()
			files := map[string]string{"go.mod": "module example.com/native\n"}
			var inventory []string
			for i := 0; i <= searchVerifyMaxReads; i++ {
				file := fmt.Sprintf("pkg/file%03d.go", i)
				files[file] = "package pkg\n"
				inventory = append(inventory, file)
			}
			evidence := searchVerifyTestEvidence(files)
			evidence.files, evidence.reads = inventory, initialReads
			if searchVerifyGoApplies("pkg/native.c", &evidence) {
				t.Fatal("package without cgo licensed C")
			}
			if reads := evidence.reads - initialReads; reads > 8 {
				t.Errorf("native ownership used %d reads, want at most 8", reads)
			}
			if !evidence.exists("go.mod") {
				t.Fatal("native ownership starved the manifest reader")
			}
		})
	}
}
