package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// collectDiagnostics walks decoded JSON and returns every object that is a
// warning or partial failure: it carries both a code and the prose effect.
func collectDiagnostics(value any, out *[]map[string]any) {
	switch typed := value.(type) {
	case map[string]any:
		_, hasCode := typed["code"]
		_, hasEffect := typed["effect_on_semantic_completeness"]
		if hasCode && hasEffect {
			*out = append(*out, typed)
		}
		for _, child := range typed {
			collectDiagnostics(child, out)
		}
	case []any:
		for _, child := range typed {
			collectDiagnostics(child, out)
		}
	}
}

// Every warning and partial failure every JSON surface writes carries
// effect_scope, and it is the classification of its code and path; that
// includes `graph diff`, whose warnings a downstream consumer fails closed on.
func TestEffectScopeOnEveryDiagnostic(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.name", "Effect Scope Test")
	git(t, repo, "config", "user.email", "effect-scope@example.invalid")
	write(t, repo, "lib.go", "package demo\n\nfunc Helper() int { return 2 }\n")
	gap := "package demo\n\nfunc AfterNew() int {\n\tp := new(0)\n\t_ = p\n\treturn Helper()\n}\n"
	write(t, repo, "demo.go", gap)
	write(t, repo, "broken/broken.go", "package broken\n\nfunc broken( {\n}\n")
	write(t, repo, "fixtures/minified.json", "{\"k\":\""+strings.Repeat("x", 6000)+"\"}\n")
	commit := func(message string) {
		git(t, repo, "add", "-A")
		git(t, repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-q", "-m", message)
	}
	commit("base")
	write(t, repo, "demo.go", strings.Replace(gap, "return Helper()", "return Helper() + 1", 1))
	write(t, repo, "lib.go", "package demo\n\nfunc Helper() int { return 3 }\n")
	commit("change")

	cacheDir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		err := Run(t.Context(), Options{
			Version: "test-version",
			Env:     EntireEnv{RepoRoot: repo, PluginDataDir: cacheDir},
			Stdout:  &stdout,
			Stderr:  &stderr,
		}, args)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, stderr.String())
		}
		return stdout.String()
	}
	type surface struct {
		name string
		args []string
		want map[string]string // code -> scope that must appear
	}
	fileScoped := map[string]string{
		sem.GoGrammarGapCode: sem.EffectScopeFile,
		"E_PARSE_ERROR":      sem.EffectScopeFile,
		"E_MINIFIED":         sem.EffectScopeFile,
	}
	surfaces := []surface{
		{"snapshot", []string{"snapshot", "--repo", repo, "--worktree", "--format", "ndjson"},
			map[string]string{sem.GoGrammarGapCode: sem.EffectScopeFile, "E_PARSE_ERROR": sem.EffectScopeFile, "E_MINIFIED": sem.EffectScopeFile, "W_WORKTREE_SNAPSHOT": sem.EffectScopeSnapshot}},
		{"index", []string{"index", "--repo", repo, "--head", "--format", "json", "--cache-dir", cacheDir}, fileScoped},
		{"query", []string{"query", "--repo", repo, "--head", "--format", "json", "--cache-dir", cacheDir, "--query", "AfterNew"}, fileScoped},
		{"neighbors", []string{"neighbors", "--repo", repo, "--head", "--format", "json", "--cache-dir", cacheDir, "--symbol", "Helper", "--direction", "in"},
			map[string]string{sem.GoGrammarGapCode: sem.EffectScopeFile, "W_VALUE_REFERENCES_UNMODELLED": sem.EffectScopeSnapshot}},
		{"diff", []string{"diff", "--repo", repo, "--base", "HEAD~1", "--head", "HEAD", "--json"},
			map[string]string{sem.GoGrammarGapCode: sem.EffectScopeFile}},
	}
	for _, s := range surfaces {
		raw := run(s.args...)
		var diagnostics []map[string]any
		scanner := bufio.NewScanner(strings.NewReader(raw))
		scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)
		decoded := 0
		if s.name == "snapshot" {
			for scanner.Scan() {
				var record any
				if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
					t.Fatalf("%s: decode %q: %v", s.name, scanner.Text(), err)
				}
				collectDiagnostics(record, &diagnostics)
				decoded++
			}
		} else {
			var payload any
			if err := json.Unmarshal([]byte(raw), &payload); err != nil {
				t.Fatalf("%s: decode %q: %v", s.name, raw, err)
			}
			collectDiagnostics(payload, &diagnostics)
			decoded++
		}
		if decoded == 0 || len(diagnostics) == 0 {
			t.Fatalf("%s: no diagnostics found in:\n%s", s.name, raw)
		}
		seen := map[string]string{}
		for _, diagnostic := range diagnostics {
			code, _ := diagnostic["code"].(string)
			path, _ := diagnostic["file_path"].(string)
			scope, _ := diagnostic["effect_scope"].(string)
			if want := sem.DiagnosticEffectScope(code, path); scope != want {
				t.Fatalf("%s: %s %q has effect_scope %q, want %q: %v", s.name, code, path, scope, want, diagnostic)
			}
			seen[code] = scope
		}
		for code, scope := range s.want {
			if seen[code] != scope {
				t.Fatalf("%s: want %s with effect_scope %q; saw %v", s.name, code, scope, seen)
			}
		}
	}

	var capabilities struct {
		Features map[string]bool `json:"optional_local_only_features"`
	}
	if err := json.Unmarshal([]byte(run("capabilities", "--json")), &capabilities); err != nil {
		t.Fatal(err)
	}
	if !capabilities.Features["partial_failure_effect_scope"] {
		t.Fatalf("capabilities do not advertise partial_failure_effect_scope: %v", capabilities.Features)
	}
}
