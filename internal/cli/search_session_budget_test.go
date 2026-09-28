package cli

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAgentSessionReplayHonorsCurrentByteBudget(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, asked string
		budget      int
		payload     string
	}{
		{name: "smaller budget", asked: "beta_gadget", budget: 256},
		{name: "tiny budget", asked: "beta_gadget", budget: 1},
		{name: "long unicode replay header", asked: strings.Repeat("βeta ", 79) + "βeta", budget: 128},
		{name: "escaped stored payload", asked: "beta_gadget", budget: 1024, payload: strings.Repeat("\x7f", 300)},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := t.TempDir()
			write(t, repo, "alpha.py", "def alpha_widget():\n    return True\n")
			write(t, repo, "beta.py", "def beta_gadget():\n    return False\n")
			commitSearchSessionFixture(t, repo, "agent session budget")
			session := filepath.Join(t.TempDir(), "session.json")
			first := searchInSessionViewFormat(t, repo, session, "", "alpha_widget", false, "agent", "--max-context-bytes", "4096")
			if len(first) <= 256 || !strings.Contains(first, "alpha.py") {
				t.Fatalf("seed did not exercise a larger real response: %q", first)
			}
			if test.payload != "" {
				rewriteSearchSessionState(t, session, func(state map[string]any) { state["payload"] = test.payload })
			}
			second := searchInSessionViewFormat(t, repo, session, "", test.asked, false, "agent", "--max-context-bytes", strconv.Itoa(test.budget))
			if len(second) > test.budget {
				t.Errorf("replay exceeded current stdout budget: got %d bytes, cap %d: %q", len(second), test.budget, second)
			}
			if !utf8.ValidString(second) {
				t.Errorf("bounded response is invalid UTF-8: %q", second)
			}
			if strings.Contains(second, "not run") {
				t.Errorf("oversized opaque replay should decline, not truncate its attribution or body: %q", second)
			}
			state, err := (&searchSession{path: session, limit: 1}).load()
			if err != nil {
				t.Fatal(err)
			}
			if state.Query != test.asked {
				t.Errorf("incompatible replay was not replaced by the requested search: stored %q, asked %q", state.Query, test.asked)
			}
		})
	}
}

func TestAgentSessionReplayPreservesFittingAndUnboundedPayloads(t *testing.T) {
	t.Parallel()
	for _, budget := range []string{"4096", "0"} {
		t.Run(budget, func(t *testing.T) {
			repo := t.TempDir()
			write(t, repo, "alpha.py", "def alpha_widget():\n    return True\n")
			write(t, repo, "beta.py", "def beta_gadget():\n    return False\n")
			commitSearchSessionFixture(t, repo, "fitting agent session replay")
			session := filepath.Join(t.TempDir(), "session.json")
			first := searchInSessionViewFormat(t, repo, session, "", "alpha_widget", false, "agent", "--max-context-bytes", "4096")
			second := searchInSessionViewFormat(t, repo, session, "", "beta_gadget", false, "agent", "--max-context-bytes", budget)
			header, body, ok := strings.Cut(second, "\n")
			if !ok || !strings.Contains(header, "not run") || !strings.Contains(header, "beta_gadget") || body != first {
				t.Fatalf("fitting replay changed attribution or payload: first=%q second=%q", first, second)
			}
			if budget == "4096" && len(second) > 4096 {
				t.Errorf("fitting replay exceeded cap: %d", len(second))
			}
		})
	}
}
