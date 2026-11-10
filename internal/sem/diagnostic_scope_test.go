package sem

import "testing"

func TestDiagnosticEffectScope(t *testing.T) {
	t.Parallel()

	cases := []struct {
		code, path, want string
	}{
		{"E_PARSE_ERROR", "pkg/a.go", EffectScopeFile},
		{"E_PARSE_TIMEOUT", "pkg/a.go", EffectScopeFile},
		{"E_PARSE_DEPTH_EXCEEDED", "pkg/a.py", EffectScopeFile},
		{"E_FILE_TOO_LARGE", "SNAPSHOT.json", EffectScopeFile},
		{"E_FILE_READ", "pkg/a.go", EffectScopeFile},
		{"E_MINIFIED", "bench/fixture.json", EffectScopeFile},
		{"E_UNSUPPORTED_LANGUAGE", "a.cob", EffectScopeFile},
		{"E_GO_ROUTE_BUDGET_EXCEEDED", "routes.go", EffectScopeFile},
		{"E_GO_SIGNATURE_BUDGET_EXCEEDED", "types.go", EffectScopeFile},
		// A file-scoped code with nothing to confine it to.
		{"E_PARSE_ERROR", "", EffectScopeSnapshot},
		// The repo-ignore shortfalls name a path as an example, not as a bound.
		{"E_REPO_IGNORE_UNREADABLE", "vendor/x.json", EffectScopeSnapshot},
		{"E_REPO_IGNORE_COUNT_INCOMPLETE", "vendor", EffectScopeSnapshot},
		{"E_REPO_IGNORE_GIT_UNAVAILABLE", "", EffectScopeSnapshot},
		{AnalysisBudgetExceededCode, "", EffectScopeSnapshot},
		// Unclassified codes never claim a file bound.
		{"E_SOMETHING_NEW", "a.go", EffectScopeSnapshot},
	}
	for _, tc := range cases {
		if got := DiagnosticEffectScope(tc.code, tc.path); got != tc.want {
			t.Errorf("DiagnosticEffectScope(%q, %q) = %q, want %q", tc.code, tc.path, got, tc.want)
		}
	}
}

func TestPartialFailureAffectsCode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		failure PartialFailure
		want    bool
	}{
		{"minified JSON fixture", PartialFailure{Code: "E_MINIFIED", FilePath: "benchmarks/agent-brain/case.json"}, false},
		{"oversized JSON snapshot", PartialFailure{Code: "E_FILE_TOO_LARGE", FilePath: "SNAPSHOT.json"}, false},
		{"YAML parse error", PartialFailure{Code: "E_PARSE_ERROR", FilePath: ".github/workflows/ci.yml"}, false},
		{"TOML parse error", PartialFailure{Code: "E_PARSE_ERROR", FilePath: "Cargo.toml"}, false},
		{"XML parse error", PartialFailure{Code: "E_PARSE_ERROR", FilePath: "pom.xml"}, false},
		{"lockfile in JSON", PartialFailure{Code: "E_FILE_TOO_LARGE", FilePath: "package-lock.json"}, false},
		{"Markdown parse error", PartialFailure{Code: "E_PARSE_ERROR", FilePath: "README.md"}, false},
		{"recorded language wins over the path", PartialFailure{Code: "E_FILE_READ", FilePath: "bin/tool", Language: "Python"}, true},
		{"Go parse error", PartialFailure{Code: "E_PARSE_ERROR", FilePath: "pkg/a.go"}, true},
		{"minified JavaScript", PartialFailure{Code: "E_MINIFIED", FilePath: "dist/app.min.js"}, true},
		{"unresolvable language", PartialFailure{Code: "E_PARSE_ERROR", FilePath: "x.qqq"}, true},
		{"file-scoped code without a path", PartialFailure{Code: "E_PARSE_ERROR"}, true},
		{"snapshot scope beats a data-format path", PartialFailure{Code: "E_REPO_IGNORE_UNREADABLE", FilePath: "vendor/x.json"}, true},
		{"unclassified code on a data file", PartialFailure{Code: "E_SOMETHING_NEW", FilePath: "a.json"}, true},
	}
	for _, tc := range cases {
		if got := PartialFailureAffectsCode(tc.failure); got != tc.want {
			t.Errorf("%s: PartialFailureAffectsCode(%+v) = %v, want %v", tc.name, tc.failure, got, tc.want)
		}
	}
}
