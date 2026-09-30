package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// The test binary carries no VCS stamp, which by design has no replay identity. The package's
// replay tests need an identifiable producer, so the default here is a clean stamped build; the
// producer tests below override it per call through Options.buildInfo.
func init() {
	searchSessionBuildInfo = func() (*debug.BuildInfo, bool) { return stampedBuild("testrev0001", false), true }
}

func stampedBuild(revision string, modified bool) *debug.BuildInfo {
	info := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}
	if revision != "" {
		info.Settings = append(info.Settings, debug.BuildSetting{Key: "vcs.revision", Value: revision})
		mod := "false"
		if modified {
			mod = "true"
		}
		info.Settings = append(info.Settings, debug.BuildSetting{Key: "vcs.modified", Value: mod})
	}
	return info
}

func TestSearchSessionProducerIdentityFromBuildInfo(t *testing.T) {
	t.Parallel()
	module := &debug.BuildInfo{Main: debug.Module{Path: "github.com/entireio/entire-graph", Version: "v0.5.0"}}
	replaced := &debug.BuildInfo{Main: debug.Module{Path: "github.com/entireio/entire-graph", Version: "v0.5.0",
		Replace: &debug.Module{Path: "../local-graph", Version: "(devel)"}}}
	revisionOnly := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}}}
	for _, c := range []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{"clean stamped revision", stampedBuild("abc123", false), true, "rev:abc123"},
		{"modified tree refuses", stampedBuild("abc123", true), true, ""},
		{"module version alone refuses (tags collide across modules and forks)", module, true, ""},
		{"local replacement refuses (same path and version, different source)", replaced, true, ""},
		{"revision without a modified stamp refuses", revisionOnly, true, ""},
		{"a modified stamp other than the literal false refuses", &debug.BuildInfo{Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc123"}, {Key: "vcs.modified", Value: "unknown"}}}, true, ""},
		{"unstamped devel build refuses", stampedBuild("", false), true, ""},
		{"no build info refuses", nil, false, ""},
	} {
		if got := searchSessionProducerFrom(c.info, c.ok); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func producerFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Tests")
	git(t, repo, "config", "user.email", "tests@entire.local")
	write(t, repo, "alpha.py", "def producer_alpha_item():\n    return True\n")
	write(t, repo, "beta.py", "def producer_beta_item():\n    return True\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "producer fixture")
	return repo
}

func runProducerSearch(t *testing.T, repo, session, version, query string, info *debug.BuildInfo) string {
	t.Helper()
	var out bytes.Buffer
	opts := Options{Version: version, Env: EntireEnv{RepoRoot: repo, SearchSession: session}, Stdout: &out}
	if info != nil {
		opts.buildInfo = func() (*debug.BuildInfo, bool) { return info, true }
	}
	if err := Run(t.Context(), opts, []string{"search", "--repo", repo, "--query", query, "--format", "text",
		"--profile", "syntax-only", "--top-k", "1", "--head"}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func replayed(output string) bool { return strings.Contains(output, "not run") }

// Control: the same identifiable producer still replays.
func TestSearchEchoReplaysForTheSameProducer(t *testing.T) {
	t.Parallel()
	repo, session := producerFixture(t), filepath.Join(t.TempDir(), "s.json")
	runProducerSearch(t, repo, session, "0.1.0", "producer_alpha_item", nil)
	if out := runProducerSearch(t, repo, session, "0.1.0", "producer_beta_item", nil); !replayed(out) {
		t.Fatalf("same producer did not replay:\n%s", out)
	}
}

// A different producer answers for real, and the session re-arms to IT: its payload is what the
// next call from the same producer replays.
func TestSearchEchoReArmsOnProducerChange(t *testing.T) {
	t.Parallel()
	repo, session := producerFixture(t), filepath.Join(t.TempDir(), "s.json")
	runProducerSearch(t, repo, session, "0.1.0", "producer_alpha_item", stampedBuild("rev-a", false))
	second := runProducerSearch(t, repo, session, "0.1.0", "producer_beta_item", stampedBuild("rev-b", false))
	if replayed(second) || !strings.Contains(second, "beta.py") {
		t.Fatalf("a different producer replayed the old payload:\n%s", second)
	}
	third := runProducerSearch(t, repo, session, "0.1.0", "producer_alpha_item", stampedBuild("rev-b", false))
	if !replayed(third) || !strings.Contains(third, "beta.py") {
		t.Fatalf("session did not re-arm to the new producer's payload:\n%s", third)
	}
}

// Through Run, where an empty version becomes "dev": an unstamped build, whatever version string it
// reports, and a modified build never replay.
func TestSearchEchoNeverReplaysForAnUnidentifiableProducer(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		version string
		info    *debug.BuildInfo
	}{
		{"empty version unstamped", "", stampedBuild("", false)},
		{"dev version unstamped", "dev", stampedBuild("", false)},
		{"modified build", "0.1.0", stampedBuild("rev-a", true)},
	} {
		repo, session := producerFixture(t), filepath.Join(t.TempDir(), "s.json")
		runProducerSearch(t, repo, session, c.version, "producer_alpha_item", c.info)
		if out := runProducerSearch(t, repo, session, c.version, "producer_beta_item", c.info); replayed(out) || !strings.Contains(out, "beta.py") {
			t.Fatalf("%s: replayed without an identity:\n%s", c.name, out)
		}
	}
}

// Legacy readers compare replay_schema for equality (base schema 2; PR #292 schema 3). A record the
// producer-aware writer persists must carry neither value, so a downgraded binary cannot ignore the
// producer field and replay it; and this reader must reject those legacy schemas.
func TestSearchEchoProducerRecordsAreInvisibleToLegacyReaders(t *testing.T) {
	t.Parallel()
	repo, session := producerFixture(t), filepath.Join(t.TempDir(), "s.json")
	runProducerSearch(t, repo, session, "0.1.0", "producer_alpha_item", nil)
	raw, err := os.ReadFile(session)
	if err != nil {
		t.Fatal(err)
	}
	var persisted struct {
		ReplaySchema int    `json:"replay_schema"`
		Producer     string `json:"producer"`
	}
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Producer == "" {
		t.Fatalf("fixture drift: no producer persisted: %s", raw)
	}
	for _, legacy := range []int{0, 1, 2, 3, 4, 5} {
		if persisted.ReplaySchema == legacy {
			t.Fatalf("producer-bound record uses legacy schema %d, which a producer-unaware reader accepts", legacy)
		}
	}
	state, err := (&searchSession{path: session, limit: 1}).load()
	if err != nil {
		t.Fatal(err)
	}
	live := searchSessionScope{Repo: state.Repo, Tree: state.Tree, PolicyFingerprint: state.PolicyFingerprint, Format: state.Format, Producer: state.Producer}
	if !state.matches(live) {
		t.Fatal("fixture drift: the persisted record does not match its own scope")
	}
	for _, legacy := range []int{0, 1, 2, 3, 4, 5} {
		old := state
		old.ReplaySchema = legacy
		if old.matches(live) {
			t.Fatalf("this reader accepted a legacy schema-%d record", legacy)
		}
	}
}
