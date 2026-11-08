package sem

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// searchVerifyScopedEvidence is the evidence a search rooted at `prefix` sees over a checkout whose
// files are given in toplevel coordinates: only the in-scope files through its own reader, and the
// rest of the checkout through the above-scope resolver. calls counts how often that resolver ran.
func searchVerifyScopedEvidence(files map[string]string, prefix string, calls *int) searchVerifyEvidence {
	inScope := map[string]string{}
	for filePath, content := range files {
		if relative, inside := searchVerifyRelative(prefix, filePath); inside {
			inScope[relative] = content
		}
	}
	evidence := searchVerifyTestEvidence(inScope)
	evidence.aboveScope = func() (searchVerifyAboveScope, bool) {
		if calls != nil {
			*calls++
		}
		return searchVerifyAboveScope{
			prefix: prefix,
			up:     strings.TrimSuffix(strings.Repeat("../", strings.Count(prefix, "/")+1), "/"),
			read: func(filePath string) (string, bool) {
				content, ok := files[filePath]
				return content, ok
			},
		}, true
	}
	return evidence
}

func searchVerifyScopeResults(source, test, testName string) []SearchResult {
	results := []SearchResult{{Rank: 1, FilePath: source, SymbolName: "Subject", Section: searchSectionPrimary}}
	if test != "" {
		results = append(results, SearchResult{
			Rank: 2, FilePath: test, SymbolName: testName, Section: SearchSectionCoveringTest,
		})
	}
	return results
}

// TestSearchVerifyFromASubdirectoryIsTheToplevelCommandBehindACd pins the invariant the above-scope
// walk is built on: a search rooted in a subdirectory whose build manifest sits above it emits
// EXACTLY the command a search at the git toplevel emits, behind the one `cd` that gets there. Each
// build system's toplevel command is pinned too, so the invariant cannot pass by both sides
// degrading to the same silence.
func TestSearchVerifyFromASubdirectoryIsTheToplevelCommandBehindACd(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name             string
		files            map[string]string
		prefix           string
		source, test     string
		testName         string
		wantTopCommand   string
		wantScopeCommand string
		wantTier         string
	}{
		{
			name: "go module root above the package",
			files: map[string]string{
				"go.mod":                        "module example.com/app\n",
				"cmd/app/session/state.go":      "package session\n",
				"cmd/app/session/state_test.go": "package session\n\nfunc TestTransition(t *testing.T) {}\n",
			},
			prefix: "cmd/app",
			source: "session/state.go", test: "session/state_test.go", testName: "TestTransition",
			wantTopCommand:   "go test ./cmd/app/session -run '^TestTransition$'",
			wantScopeCommand: "cd ../.. && go test ./cmd/app/session -run '^TestTransition$'",
			wantTier:         searchVerifyTierNarrow,
		},
		{
			name: "npm workspace root runs jest on the leaf's test file",
			files: map[string]string{
				"package.json":                         `{"devDependencies":{"jest":"29"}}`,
				"package-lock.json":                    "{}",
				"packages/theme/src/CodeBlock.ts":      "",
				"packages/theme/src/CodeBlock.test.ts": "",
			},
			prefix: "packages/theme",
			source: "src/CodeBlock.ts", test: "src/CodeBlock.test.ts",
			wantTopCommand:   "npx jest packages/theme/src/CodeBlock.test.ts",
			wantScopeCommand: "cd ../.. && npx jest packages/theme/src/CodeBlock.test.ts",
			wantTier:         searchVerifyTierNarrow,
		},
		{
			name: "pnpm suite from the workspace root",
			files: map[string]string{
				"package.json":   `{"scripts":{"test":"jest"}}`,
				"pnpm-lock.yaml": "",
				"src/ui/a.js":    "",
			},
			prefix: "src/ui",
			source: "a.js",
			// No covering test: the narrow tier declines everywhere, so this is the suite rung.
			wantTopCommand:   "pnpm test",
			wantScopeCommand: "cd ../.. && pnpm test",
			wantTier:         searchVerifyTierSuite,
		},
		{
			name: "yarn suite from the root",
			files: map[string]string{
				"package.json": `{"scripts":{"test":"jest"}}`,
				"yarn.lock":    "",
				"lib/a.js":     "",
			},
			prefix:           "lib",
			source:           "a.js",
			wantTopCommand:   "yarn test",
			wantScopeCommand: "cd .. && yarn test",
			wantTier:         searchVerifyTierSuite,
		},
		{
			name: "cargo workspace member with the crate manifest above the scope",
			files: map[string]string{
				"Cargo.toml":                 "[workspace]\nmembers = [\"crates/printer\"]\n",
				"crates/printer/Cargo.toml":  "[package]\nname = \"grep-printer\"\nversion = \"0.1.0\"\n",
				"crates/printer/src/util.rs": "fn trim() {}\n#[test]\nfn test_trim() {}\n",
			},
			prefix: "crates/printer/src",
			source: "util.rs", test: "util.rs", testName: "test_trim",
			wantTopCommand:   "cargo test -p grep-printer --lib test_trim",
			wantScopeCommand: "cd ../../.. && cargo test -p grep-printer --lib test_trim",
			wantTier:         searchVerifyTierNarrow,
		},
		{
			name: "pytest configured at the root",
			files: map[string]string{
				"pyproject.toml":      "[tool.pytest.ini_options]\n",
				"src/pkg/mod.py":      "",
				"src/pkg/test_mod.py": "",
			},
			prefix: "src/pkg",
			source: "mod.py", test: "test_mod.py", testName: "test_rounds",
			wantTopCommand:   "python -m pytest src/pkg/test_mod.py -k test_rounds",
			wantScopeCommand: "cd ../.. && python -m pytest src/pkg/test_mod.py -k test_rounds",
			wantTier:         searchVerifyTierNarrow,
		},
		{
			name: "maven reactor module addressed from the root aggregator",
			files: map[string]string{
				"pom.xml":      mavenAggregator("gson"),
				"gson/pom.xml": "<project/>",
				"gson/src/main/java/com/google/gson/GsonBuilder.java":     "",
				"gson/src/test/java/com/google/gson/GsonBuilderTest.java": "",
			},
			prefix: "gson/src",
			source: "main/java/com/google/gson/GsonBuilder.java", test: "test/java/com/google/gson/GsonBuilderTest.java",
			testName:         "testCustomAdapter",
			wantTopCommand:   "mvn -q -pl gson -am -Dtest=GsonBuilderTest -DfailIfNoTests=false test",
			wantScopeCommand: "cd ../.. && mvn -q -pl gson -am -Dtest=GsonBuilderTest -DfailIfNoTests=false test",
			wantTier:         searchVerifyTierNarrow,
		},
		{
			name: "gradle wrapper and settings at the root",
			files: map[string]string{
				"gradlew":                      "#!/bin/sh\n",
				"settings.gradle.kts":          "include(\":lib\")\n",
				"lib/build.gradle.kts":         "",
				"lib/src/main/kotlin/A.kt":     "",
				"lib/src/test/kotlin/ATest.kt": "",
			},
			prefix: "lib/src",
			source: "main/kotlin/A.kt", test: "test/kotlin/ATest.kt",
			wantTopCommand:   "./gradlew :lib:test --tests 'ATest'",
			wantScopeCommand: "cd ../.. && ./gradlew :lib:test --tests 'ATest'",
			wantTier:         searchVerifyTierNarrow,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			topEvidence := searchVerifyTestEvidence(testCase.files)
			lift := func(filePath string) string {
				if filePath == "" {
					return ""
				}
				return testCase.prefix + "/" + filePath
			}
			top := buildSearchVerifyCommand(
				searchVerifyScopeResults(lift(testCase.source), lift(testCase.test), testCase.testName), topEvidence)
			if top == nil || top.Command != testCase.wantTopCommand {
				t.Fatalf("toplevel command = %#v, want %q", top, testCase.wantTopCommand)
			}

			calls := 0
			scoped := buildSearchVerifyCommand(
				searchVerifyScopeResults(testCase.source, testCase.test, testCase.testName),
				searchVerifyScopedEvidence(testCase.files, testCase.prefix, &calls))
			if scoped == nil || scoped.Command != testCase.wantScopeCommand {
				t.Fatalf("subdirectory command = %#v, want %q", scoped, testCase.wantScopeCommand)
			}
			if calls != 1 {
				t.Fatalf("above-scope resolver ran %d times, want once", calls)
			}
			if scoped.Tier != testCase.wantTier || top.Tier != testCase.wantTier {
				t.Fatalf("tier: subdirectory %q toplevel %q, want %q", scoped.Tier, top.Tier, testCase.wantTier)
			}
			if scoped.Targets != top.Targets {
				t.Fatalf("targets = %q, want the toplevel's %q", scoped.Targets, top.Targets)
			}
			if want := top.DerivedFrom + " (above --repo; paths relative to "; !strings.HasPrefix(scoped.DerivedFrom, want) {
				t.Fatalf("derived_from = %q, want it to start %q", scoped.DerivedFrom, want)
			}
			// The runner probe has to look where the command runs: `./gradlew` is at the toplevel,
			// not in the scope, so asking the scope's evidence would call it missing.
			if scoped.RunnerMissing != top.RunnerMissing {
				t.Fatalf("runner_missing: subdirectory %v, toplevel %v", scoped.RunnerMissing, top.RunnerMissing)
			}
			// Provenance is replay metadata in scope coordinates: nothing above the scope can be
			// spelled there, and nothing may escape it.
			for _, filePath := range scoped.provenancePaths {
				if strings.HasPrefix(filePath, "../") || strings.HasPrefix(filePath, testCase.prefix+"/") {
					t.Fatalf("provenance path %q is not scope-relative: %v", filePath, scoped.provenancePaths)
				}
			}
		})
	}
}

// A subdirectory that is its own build root keeps the command it always had: the above-scope walk
// is consulted only after every in-scope rung has declined, so it is never even resolved.
func TestSearchVerifyFromAnOwnBuildRootNeverLooksAboveTheScope(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"go.mod":                  "module example.com/root\n",
		"tools/go.mod":            "module example.com/tools\n",
		"tools/lint/lint.go":      "package lint\n",
		"tools/lint/lint_test.go": "package lint\n\nfunc TestLint(t *testing.T) {}\n",
	}
	calls := 0
	got := buildSearchVerifyCommand(
		searchVerifyScopeResults("lint/lint.go", "lint/lint_test.go", "TestLint"),
		searchVerifyScopedEvidence(files, "tools", &calls))
	if got == nil || got.Command != "go test ./lint -run '^TestLint$'" {
		t.Fatalf("command = %#v, want the in-scope module's own command", got)
	}
	if calls != 0 {
		t.Fatalf("above-scope resolver ran %d times for a scope that is its own build root", calls)
	}

	// The in-scope SUITE rung outranks a narrow command above the scope too. From the toplevel the
	// root crate's narrow `cargo test` would win the walk; a scope that is its own yarn package keeps
	// its own suite instead of being sent to an unrelated build one directory up.
	files = map[string]string{
		"Cargo.toml":       "[package]\nname = \"root\"\n",
		"web/package.json": `{"scripts":{"test":"jest"}}`,
		"web/yarn.lock":    "",
		"web/src/a.js":     "",
	}
	calls = 0
	got = buildSearchVerifyCommand(
		searchVerifyScopeResults("src/a.js", "", ""),
		searchVerifyScopedEvidence(files, "web", &calls))
	if got == nil || got.Command != "yarn test" || got.Tier != searchVerifyTierSuite {
		t.Fatalf("command = %#v, want the scope's own yarn suite", got)
	}
	if calls != 0 {
		t.Fatalf("above-scope resolver ran %d times for a scope with its own suite", calls)
	}
}

// Nothing above the scope licenses a command either: the residual floor stays exactly as before.
func TestSearchVerifyAboveScopeWithNoManifestKeepsTheFloor(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"README.md":        "",
		"docs/notes/a.tcl": "",
	}
	calls := 0
	got := buildSearchVerifyCommand(
		searchVerifyScopeResults("notes/a.tcl", "", ""),
		searchVerifyScopedEvidence(files, "docs", &calls))
	if got == nil || got.Tier != searchVerifyTierNone || got.Command != searchVerifyNoneCommand {
		t.Fatalf("command = %#v, want the residual floor", got)
	}
	if calls != 1 {
		t.Fatalf("above-scope resolver ran %d times, want once", calls)
	}
}

// TestSearchRepositoryVerifyFromASubdirectoryRunsAgainstTheRealModule is the end-to-end form of the
// bug: a real checkout with go.mod at the toplevel, searched from a package directory, through both
// committed-tree and worktree reads. It also RUNS the emitted command from the scope root, because
// "the command is runnable from --repo" is the contract, and only the toolchain can confirm it.
func TestSearchRepositoryVerifyFromASubdirectoryRunsAgainstTheRealModule(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "go.mod", "module example.com/fixture\n\ngo 1.22\n")
	write(t, repo, "cmd/tool/ledger/ledger.go", `package ledger

// ReconcileLedger reconciles the ledger balance.
func ReconcileLedger(balance int) int {
	return balance * 2
}
`)
	write(t, repo, "cmd/tool/ledger/ledger_test.go", `package ledger

import "testing"

func TestReconcileLedger(t *testing.T) {
	if ReconcileLedger(2) != 4 {
		t.Fatal("reconcile")
	}
}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	scope := filepath.Join(repo, "cmd", "tool")
	physicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	want := searchVerifyRunIn(physicalRepo, "go test ./cmd/tool/ledger -run '^TestReconcileLedger$'")
	for _, worktree := range []bool{false, true} {
		response, err := SearchRepository(t.Context(), scope, "test-version", "reconcile ledger balance", SearchOptions{
			Profile: ProfileFull, Worktree: worktree, DisableCache: true, TopK: 5,
		})
		if err != nil {
			t.Fatal(err)
		}
		if response.VerifyCommand == nil || response.VerifyCommand.Command != want {
			t.Fatalf("worktree=%v verify = %#v, want %q", worktree, response.VerifyCommand, want)
		}
		for _, filePath := range SearchResponsePaths(response) {
			if _, ok := cleanSearchReplayPath(filePath); !ok {
				t.Fatalf("worktree=%v response carries an unreplayable path %q", worktree, filePath)
			}
		}
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; the derivation is pinned above")
	}
	command := exec.CommandContext(t.Context(), "sh", "-c", strings.Replace(want, "go test", "go test -v", 1))
	command.Dir = scope
	command.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("emitted command failed from the scope root: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "--- PASS: TestReconcileLedger") {
		t.Fatalf("emitted command did not run the covering test:\n%s", output)
	}
}
