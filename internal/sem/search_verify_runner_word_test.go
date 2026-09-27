package sem

import "testing"

// The derived command is printed to the agent as VERIFY, with an instruction to run it once and
// trust the result. A runner detected from a SUBSTRING rather than an invocation therefore does
// not merely mislabel a repository -- it hands the agent the wrong command to believe.

func TestAMentionedRunnerIsNotAnInvokedRunner(t *testing.T) {
	t.Parallel()
	// Every one of these ran no jest at all and was reported as jest.
	for _, script := range []string{
		"playwright test --reporter=jest-junit", // a jest REPORTER, a Playwright suite
		"node scripts/jest-shim.js",             // a filename
		"echo skipping jest for now && exit 0",  // a script saying it is NOT running jest
		"eslint --rule no-jest-globals .",       // a lint rule name
		"cp fixtures/vitest.config.ts .",        // a config file being copied
	} {
		if cmd, ok := searchVerifyNodeRunnerFromScript(script); ok {
			t.Errorf("script %q names no runner but produced VERIFY %q", script, cmd)
		}
	}
}

// The other half. A test that only refuses things passes trivially on a matcher that refuses
// everything, and this one has to keep working for every shape a package.json actually uses.
func TestAnInvokedRunnerIsStillFound(t *testing.T) {
	t.Parallel()
	for script, want := range map[string]string{
		"jest":                             "npx jest",
		"jest --coverage":                  "npx jest",
		"npx jest":                         "npx jest",
		"yarn jest --ci":                   "npx jest",
		"npm run build && jest":            "npx jest",
		"cross-env NODE_ENV=test jest":     "npx jest",
		"NODE_ENV=test jest":               "npx jest",
		"vitest run":                       "npx vitest run",
		"tsc --noEmit && vitest run --cov": "npx vitest run",
		"mocha 'test/**/*.spec.js'":        "npx mocha",
	} {
		got, ok := searchVerifyNodeRunnerFromScript(script)
		if !ok || got != want {
			t.Errorf("script %q: got (%q,%v), want %q", script, got, ok, want)
		}
	}
}

// Declared dependencies are stronger evidence than a string, and used to lose to one: scripts
// are consulted first, so a substring in the test script overrode an explicit devDependency.
func TestADeclaredDependencyIsNotOverriddenByAMention(t *testing.T) {
	t.Parallel()
	manifest := searchVerifyNodeManifest{
		Scripts:         map[string]string{"test": "playwright test --reporter=jest-junit"},
		DevDependencies: map[string]string{"mocha": "^10.0.0"},
	}
	cmd, source := searchVerifyNodeRunnerFromManifest(manifest)
	if cmd != "npx mocha" {
		t.Errorf("got %q from %q; the declared devDependency should decide, not a reporter flag",
			cmd, source)
	}
}

// The same class in the Python derivation: setup.cfg and pyproject.toml are general manifests,
// and the word "pytest" appears in them for reasons that are not configuration.
func TestPytestIsDetectedFromASectionNotAMention(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label, file, content string
		want                 bool
	}{
		{"a description mentioning pytest", "pyproject.toml",
			"[project]\nname = \"x\"\ndescription = \"a plugin for pytest users\"\n", false},
		{"a comment saying they LEFT pytest", "pyproject.toml",
			"[project]\nname = \"x\"\n# migrated off pytest in 2024; we use unittest\n", false},
		{"a metadata keyword", "setup.cfg", "[metadata]\nname = x\nkeywords = pytest, testing\n", false},
		{"a dep of a unittest tox env", "tox.ini",
			"[tox]\nenvlist = py311\n[testenv]\ncommands = python -m unittest discover\ndeps = pytest-cov\n", false},
		{"a real pytest.ini", "pytest.ini", "[pytest]\ntestpaths = tests\n", true},
		{"a real setup.cfg section", "setup.cfg", "[tool:pytest]\ntestpaths = tests\n", true},
		{"a real pyproject section", "pyproject.toml", "[tool.pytest.ini_options]\ntestpaths = [\"tests\"]\n", true},
		{"a real tox pytest section", "tox.ini", "[pytest]\ntestpaths = tests\n", true},
	} {
		evidence := searchVerifyTestEvidenceWithout(map[string]string{"/r/" + tc.file: tc.content})
		got := deriveSearchVerifySuitePytest("/r", &evidence)
		if (got != nil) != tc.want {
			t.Errorf("%s (%s): got %v, want detected=%v", tc.label, tc.file, got, tc.want)
		}
	}
}
