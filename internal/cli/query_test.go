package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestQueryAndSearchCompatibility(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")
	var canonical string
	for _, command := range []string{"query", "search"} {
		var out bytes.Buffer
		err := Run(t.Context(), Options{Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
			command, "--repo", repo, "--query", "validate_token", "--format", "text", "--profile", "syntax-only", "--index-all-files",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "auth.py:1") {
			t.Fatalf("%s did not return the definition: %s", command, &out)
		}
		if command == "query" {
			canonical = out.String()
		} else if out.String() != canonical {
			t.Fatalf("search alias output differs from query:\n%s", &out)
		}
		if err := checkPreflight("dev", command+" --query validate_token --format text"); err != nil {
			t.Fatal(err)
		}
		if verb, ok := graphVerbFromCommand("entire graph " + command + " --query validate_token"); !ok || !graphLocateVerbs[verb] {
			t.Fatalf("%s is not counted as a locate call", command)
		}
	}
}

func TestQueryHelpHidesSearchAlias(t *testing.T) {
	var root bytes.Buffer
	renderRootHelp(&root)
	if !strings.Contains(root.String(), "  query ") || strings.Contains(root.String(), "  search ") {
		t.Fatalf("unexpected command listing:\n%s", &root)
	}
	for _, args := range [][]string{{"query", "--help"}, {"search", "--help"}} {
		var out bytes.Buffer
		if err := Run(t.Context(), Options{Stdout: &out}, args); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "entire graph query --query") || strings.Contains(out.String(), "entire graph search") {
			t.Fatalf("unexpected help for %v:\n%s", args, &out)
		}
	}
}

func TestQueryTrailingArgument(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")
	for _, command := range []string{"query", "search"} {
		t.Run(command, func(t *testing.T) {
			var explicit, positional bytes.Buffer
			base := []string{command, "--repo", repo, "--format", "text", "--profile", "syntax-only", "--index-all-files"}
			for _, tc := range []struct {
				args []string
				out  *bytes.Buffer
			}{
				{append(append([]string{}, base...), "--query", "validate token"), &explicit},
				{append(append([]string{}, base...), "validate token"), &positional},
			} {
				if err := Run(t.Context(), Options{Env: EntireEnv{RepoRoot: repo}, Stdout: tc.out}, tc.args); err != nil {
					t.Fatal(err)
				}
			}
			if !strings.Contains(positional.String(), "auth.py:1") || positional.String() != explicit.String() {
				t.Fatalf("positional query differs:\n%s", &positional)
			}
			if err := checkPreflight("dev", command+" --format text validate_token"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestQueryTrailingArgumentValidation(t *testing.T) {
	for _, args := range [][]string{
		{"--query", "explicit", "extra"},
		{"--query", "", "extra"},
		{"first", "second"},
		{"query", "--format", "text"},
		{"--unknown"},
		{"--unknown", "query"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			for _, command := range []string{"query", "search"} {
				if err := Run(t.Context(), Options{}, append([]string{command}, args...)); err == nil {
					t.Fatalf("accepted invalid arguments: %v", args)
				}
			}
		})
	}
	for _, args := range [][]string{{"--query"}, {"--repo"}, {"--format"}} {
		if _, _, err := parseSearchFlags(args); err == nil {
			t.Fatalf("accepted missing flag value: %v", args)
		}
	}
}
