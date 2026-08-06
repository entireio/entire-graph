package cli

import (
	"bytes"
	"strings"
	"testing"
)

// dispatchCommands mirrors the real command tokens handled by Run's switch in
// root.go. `help` and `version` are included — they are commands with their own
// doc entries; only the flag aliases (--help, -h, --version, -v) are left out,
// since they carry no command word. If you add a command to the switch, add it
// here and give it a commandDoc.
var dispatchCommands = []string{
	"diff", "commit", "checkpoint", "analyze", "doctor", "capabilities",
	"snapshot", "symbols", "edges", "search", "index", "def", "neighbors",
	"impact", "verify", "stats", "agent-guide", "init-agents", "version", "help",
}

// TestRegistryMatchesDispatch enforces that the help registry and the dispatch
// switch cover exactly the same set of commands, so help never drifts from what
// the CLI actually accepts.
func TestRegistryMatchesDispatch(t *testing.T) {
	docNames := map[string]bool{}
	for _, d := range commandDocs {
		if docNames[d.name] {
			t.Fatalf("duplicate commandDoc for %q", d.name)
		}
		docNames[d.name] = true
	}

	dispatch := map[string]bool{}
	for _, c := range dispatchCommands {
		dispatch[c] = true
	}

	for _, c := range dispatchCommands {
		if !docNames[c] {
			t.Errorf("dispatched command %q has no commandDoc (missing help)", c)
		}
	}
	for name := range docNames {
		if !dispatch[name] {
			t.Errorf("commandDoc %q is not dispatched by Run (stale help)", name)
		}
	}
}

// TestEveryCommandDocResolvesToUsage makes sure each command (aliases resolved)
// renders a Usage block — i.e. no listed command has an empty doc.
func TestEveryCommandDocResolvesToUsage(t *testing.T) {
	for _, name := range commandNames() {
		var out bytes.Buffer
		renderCommandHelp(&out, name)
		if !strings.Contains(out.String(), "Usage:") {
			t.Errorf("%s --help produced no Usage block:\n%s", name, out.String())
		}
	}
}

// TestRootHelpGroupsAndCommands checks the grouped root listing shows every
// non-hidden command and the three group headers in order.
func TestRootHelpGroupsAndCommands(t *testing.T) {
	var out bytes.Buffer
	renderRootHelp(&out)
	root := out.String()

	last := -1
	for _, g := range groupOrder {
		title := groupTitles[g]
		idx := strings.Index(root, title+":")
		if idx < 0 {
			t.Fatalf("root help missing group header %q", title)
		}
		if idx < last {
			t.Fatalf("group header %q out of order", title)
		}
		last = idx
	}

	for _, name := range commandNames() {
		if !strings.Contains(root, "\n  "+name+" ") && !strings.Contains(root, "\n  "+name+"\n") {
			t.Errorf("root help omitted command %q", name)
		}
	}

	// The hidden `analyze` alias must not appear as its own listing row.
	if strings.Contains(root, "\n  analyze ") {
		t.Error("root help listed the hidden alias `analyze`")
	}
}

// TestRunRendersPerCommandHelp exercises the --help interception wired into Run.
func TestRunRendersPerCommandHelp(t *testing.T) {
	cases := []struct {
		args     []string
		contains []string
	}{
		{[]string{"search", "--help"}, []string{"Usage:", "Flags:", "--query"}},
		{[]string{"neighbors", "-h"}, []string{"Flags:", "16384"}},
		{[]string{"analyze", "--help"}, []string{"entire graph diff"}}, // alias resolves
	}
	for _, tc := range cases {
		var stdout, stderr bytes.Buffer
		err := Run(t.Context(), Options{Stdout: &stdout, Stderr: &stderr}, tc.args)
		if err != nil {
			t.Fatalf("Run(%v) returned error: %v", tc.args, err)
		}
		for _, want := range tc.contains {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("Run(%v) help missing %q:\n%s", tc.args, want, stdout.String())
			}
		}
	}
}

// TestRunUnknownCommandHints keeps the unknown-command error pointing users at
// the help listing.
func TestRunUnknownCommandHints(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"frobnicate"})
	if err == nil {
		t.Fatal("expected an error for an unknown command")
	}
	if !strings.Contains(err.Error(), "unknown command") || !strings.Contains(err.Error(), "entire graph help") {
		t.Errorf("unknown-command error lacks a help hint: %v", err)
	}
}
