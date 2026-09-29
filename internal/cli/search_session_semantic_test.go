package cli

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// A session payload is rendered output: one recorded with the semantic channel on carries rows and a
// stats line a lexical payload does not, and the reverse. The echo must never replay a payload under
// a different effective channel configuration — on/off, --no-semantic, model or endpoint.
func TestSearchEchoRefusesSemanticConfigurationChange(t *testing.T) {
	repo := semanticCLIRepo(t)
	fake := &cliFakeEmbedder{}
	server := httptest.NewServer(fake)
	defer server.Close()
	other := httptest.NewServer(&cliFakeEmbedder{})
	defer other.Close()
	cacheDir := t.TempDir()
	on := EntireEnv{RepoRoot: repo, SemanticEndpoint: server.URL, SemanticModel: "fake-embed"}
	if _, err := runGraph(t, on, "index", "--repo", repo, "--cache-dir", cacheDir, "--semantic", "--format", "json"); err != nil {
		t.Fatal(err)
	}
	type config struct {
		name  string
		env   EntireEnv
		extra []string
	}
	off := config{name: "unconfigured", env: EntireEnv{RepoRoot: repo}}
	onA := config{name: "on", env: on}
	flagOff := config{name: "--no-semantic", env: on, extra: []string{"--no-semantic"}}
	otherModel := config{name: "on/other-model", env: EntireEnv{RepoRoot: repo, SemanticEndpoint: server.URL, SemanticModel: "fake-embed-2"}}
	otherEndpoint := config{name: "on/other-endpoint", env: EntireEnv{RepoRoot: repo, SemanticEndpoint: other.URL, SemanticModel: "fake-embed"}}
	search := func(t *testing.T, c config, session, format, query string) string {
		t.Helper()
		env := c.env
		env.SearchSession = session
		args := append([]string{"search", "--repo", repo, "--head", "--cache-dir", cacheDir, "--format", format,
			"--top-k", "2", "--query", query}, c.extra...)
		out, err := runGraph(t, env, args...)
		if err != nil {
			t.Fatalf("%s search %q: %v", c.name, query, err)
		}
		return out
	}
	replayed := func(out string) bool {
		header, _, _ := strings.Cut(out, "\n")
		return strings.Contains(header, "not run")
	}
	for _, format := range []string{"text", "agent"} {
		for _, pair := range [][2]config{
			{off, onA}, {onA, off}, {flagOff, onA}, {onA, flagOff}, {off, flagOff},
			{onA, otherModel}, {onA, otherEndpoint},
		} {
			first, second := pair[0], pair[1]
			t.Run(format+"/"+first.name+"->"+second.name, func(t *testing.T) {
				session := filepath.Join(t.TempDir(), "session.json")
				recorded := search(t, first, session, format, "stalled backend")
				// Positive control: the same configuration DOES replay, so a refusal below is the
				// semantic identity and not a broken echo.
				control := search(t, first, session, format, "validate token")
				if !replayed(control) {
					t.Fatalf("control: same configuration did not replay:\n%s", control)
				}
				crossed := search(t, second, session, format, "validate token")
				if replayed(crossed) || strings.Contains(crossed, recorded) {
					t.Fatalf("payload recorded %s replayed to %s:\n%s", first.name, second.name, crossed)
				}
			})
		}
	}
}

func TestSemanticReplayIdentity(t *testing.T) {
	t.Parallel()
	endpoint := "http://user:secret@127.0.0.1:11434"
	on := &sem.SemanticConfig{Endpoint: endpoint, Model: "nomic-embed-text"}
	identities := map[string]string{
		"nil":            (*sem.SemanticConfig)(nil).ReplayIdentity(),
		"endpoint-only":  (&sem.SemanticConfig{Endpoint: endpoint}).ReplayIdentity(),
		"on":             on.ReplayIdentity(),
		"disabled":       (&sem.SemanticConfig{Endpoint: endpoint, Model: "nomic-embed-text", Disabled: true}).ReplayIdentity(),
		"other-model":    (&sem.SemanticConfig{Endpoint: endpoint, Model: "mxbai-embed-large"}).ReplayIdentity(),
		"other-endpoint": (&sem.SemanticConfig{Endpoint: "http://127.0.0.1:11435", Model: "nomic-embed-text"}).ReplayIdentity(),
	}
	if identities["nil"] != "" || identities["endpoint-only"] != "" {
		t.Fatalf("unconfigured identity must be empty: %q", identities)
	}
	seen := map[string]string{}
	for name, identity := range identities {
		if name == "nil" || name == "endpoint-only" {
			continue
		}
		if previous, dup := seen[identity]; dup {
			t.Fatalf("%s and %s share identity %q", name, previous, identity)
		}
		seen[identity] = name
		for _, leak := range []string{"secret", "127.0.0.1", "11434", "http"} {
			if strings.Contains(identity, leak) {
				t.Fatalf("%s identity %q leaks the endpoint (%q)", name, identity, leak)
			}
		}
	}
	if on.ReplayIdentity() != (&sem.SemanticConfig{Endpoint: " " + endpoint + " ", Model: "nomic-embed-text"}).ReplayIdentity() {
		t.Fatal("identity is not the effective (trimmed) configuration")
	}
}

// An unconfigured search persists no semantic identity at all, and a configured one persists only
// the hash — the state file never holds the endpoint.
func TestSearchSessionStateOmitsSemanticWhenUnconfigured(t *testing.T) {
	repo := semanticCLIRepo(t)
	server := httptest.NewServer(&cliFakeEmbedder{})
	defer server.Close()
	for _, c := range []struct {
		name string
		env  EntireEnv
		want bool
	}{
		{"unconfigured", EntireEnv{RepoRoot: repo}, false},
		{"configured", EntireEnv{RepoRoot: repo, SemanticEndpoint: server.URL, SemanticModel: "fake-embed"}, true},
	} {
		session := filepath.Join(t.TempDir(), "session.json")
		env := c.env
		env.SearchSession = session
		if _, err := runGraph(t, env, "search", "--repo", repo, "--head", "--cache-dir", t.TempDir(),
			"--format", "text", "--query", "stalled backend"); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(session)
		if err != nil {
			t.Fatal(err)
		}
		var state map[string]any
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		// Schema 4 readers are semantic-unaware and compare the schema for equality: a record this
		// writer persists must be invisible to them, or a downgraded binary would replay it across
		// channel configurations.
		if schema, _ := state["replay_schema"].(float64); schema <= 4 {
			t.Fatalf("%s: record uses semantic-unaware schema %v", c.name, state["replay_schema"])
		}
		_, has := state["semantic"]
		if has != c.want {
			t.Fatalf("%s: semantic field present=%v, want %v: %s", c.name, has, c.want, raw)
		}
		if strings.Contains(string(raw), server.URL) {
			t.Fatalf("%s: state file holds the raw endpoint: %s", c.name, raw)
		}
	}
}
