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
