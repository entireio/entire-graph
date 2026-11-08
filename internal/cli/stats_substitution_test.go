package cli

import (
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestIsExploringShellCommandMasksCommandSubstitution: an operator inside `$(...)` or backticks
// belongs to the substituted command, not to the outer statement. Splitting on it cut the head
// stage short and hid the outer `> file` redirect, so a write counted as exploration.
func TestIsExploringShellCommandMasksCommandSubstitution(t *testing.T) {
	t.Parallel()
	writes := []string{
		`cat $(git log --oneline | head -5) > summary.txt`,
		"cat `ls *.go | head -1` > out.txt",
		`cat $(ls; echo x) > out.txt`,
		`cat $(echo $(ls | head -1) | tail -1) > out.txt`,
		`cat $(echo ")" | head -1) > out.txt`,
	}
	for _, command := range writes {
		if isExploringShellCommand(command) {
			t.Fatalf("%q writes a file; it is not a locate call (heads %q)", command, pipelineHeadStages(command))
		}
	}
	reads := []string{
		`cat $(git ls-files | head -5)`,
		`echo $(date) ; grep -rn foo .`,
		"echo `date` && grep -rn foo .",
		`x=$((1+2)); grep -rn foo .`,
	}
	for _, command := range reads {
		if !isExploringShellCommand(command) {
			t.Fatalf("%q reads the repo; it must still count as exploration (heads %q)", command, pipelineHeadStages(command))
		}
	}
	// The quoted `)` must not close the substitution early: the outer head stage still carries its
	// `> out.txt` redirect, so the OUTER statement is a write. The inner `grep -l` is a search of
	// its own, though, and counts once substitution bodies are classified.
	quoted := `cat $(grep -l ")" x | head -1) > out.txt`
	if heads := pipelineHeadStages(quoted); len(heads) != 1 || !isWritingShellStage(heads[0]) {
		t.Fatalf("%q: the outer statement must stay one write stage, got heads %q", quoted, heads)
	}
	if !isExploringShellCommand(quoted) {
		t.Fatalf("%q: the substituted grep -l locates files and must count", quoted)
	}
	// An unterminated substitution falls back to the unmasked reading, like an unbalanced quote.
	for index, masked := range shellQuoteMask([]rune(`echo $(unterminated; grep -rn foo .`)) {
		if masked {
			t.Fatalf("an unbalanced substitution masked index %d; the whole mask must fall back to false", index)
		}
	}
}

// TestShellClassifiersRecurseIntoCommandSubstitution: masking a substitution as one opaque word
// fixed top-level operator detection, but it also hid every command INSIDE the substitution from
// both classifiers. Each body is now classified recursively, identically on the graph and the
// exploration side, so the two arms see the same commands.
func TestShellClassifiersRecurseIntoCommandSubstitution(t *testing.T) {
	t.Parallel()
	graph := []struct{ command, verb string }{
		{`result=$(cd /repo && entire graph impact --symbol Foo --format json); echo "$result" | jq .`, "impact"},
		{`OUT=$(git status; entire graph search --query foo)`, "search"},
		{"echo `cd /r && entire graph neighbors --symbol X`", "neighbors"},
		{`x=$(entire graph impact --symbol Foo)`, "impact"},
		{`x="$(entire graph search --query foo)"`, "search"},
		{`echo $(echo $(cd /r && entire graph neighbors --symbol X))`, "neighbors"},
	}
	for _, tc := range graph {
		verb, ok := graphVerbFromCommand(tc.command)
		if !ok || verb != tc.verb {
			t.Errorf("graphVerbFromCommand(%q) = %q, %v; want %q, true", tc.command, verb, ok, tc.verb)
		}
	}
	explore := []string{
		`files=$(cd internal && grep -rln Foo .)`,
		`for f in $(cd internal && grep -rl Foo .); do echo $f; done`,
		"echo `cd /r && grep -rn Foo .`",
		`x=$(grep -rln Foo .)`,
		`x="$(cd internal && rg Foo)"`,
		`echo $(echo $(cd /r && grep -rn Foo .))`,
	}
	for _, command := range explore {
		if !isExploringShellCommand(command) {
			t.Errorf("isExploringShellCommand(%q) = false; the substituted search must count", command)
		}
	}

	// The displacement timeline's shell branch must see the same substituted commands its graph
	// branch does, or a re-query hidden in a substitution would not cancel a graph locate's credit.
	for _, command := range []string{
		`files=$(cd internal && grep -rln Foo .)`,
		"echo `cd /r && rg Foo`",
	} {
		if entry := shellTimelineEntry(command); entry.kind != timelineRequery {
			t.Errorf("shellTimelineEntry(%q).kind = %v; want re-query", command, entry.kind)
		}
	}
	if entry := shellTimelineEntry(`body=$(cat internal/cli/stats.go)`); entry.kind != timelineRead || !slices.Contains(entry.paths, "internal/cli/stats.go") {
		t.Errorf("a substituted cat must be a read of its operand, got %+v", entry)
	}
	if entry := timelineFromToolUse(contentBlock{Name: "Bash", Input: map[string]any{"command": `x=$(entire graph search --query foo)`}}); entry.kind != timelineLocate {
		t.Errorf("a substituted graph search must be a locate on the timeline, got %+v", entry)
	}

	// The original finding: the inner `|` must not cut the outer pipeline (the outer statement is a
	// write), and the inner pipeline is headed by `git log`, which locates nothing.
	finding := `cat $(git log --oneline | head -5) > summary.txt`
	if isExploringShellCommand(finding) {
		t.Errorf("%q must stay non-exploration", finding)
	}
	if heads := pipelineHeadStages(finding); len(heads) != 1 || !isWritingShellStage(heads[0]) {
		t.Errorf("%q: outer heads %q; want one write stage", finding, heads)
	}
	if verb, ok := graphVerbFromCommand(finding); ok {
		t.Errorf("%q is not a graph call, got %q", finding, verb)
	}

	// Not commands: single-quoted text, arithmetic, an escaped substitution.
	inert := []string{
		`echo '$(grep -rn Foo .)'`,
		`echo '$(entire graph impact --symbol Foo)'`,
		`x=$(( $y + 1 ))`,
		`n=$((grep + head))`,         // arithmetic on variables named grep and head
		`n=$((entire graph search))`, // bash rejects this as arithmetic; it never runs a command
		`echo \$(grep -rn Foo .)`,
		"echo \\`entire graph search --query foo\\`",
	}
	for _, command := range inert {
		if isExploringShellCommand(command) {
			t.Errorf("isExploringShellCommand(%q) = true; nothing here runs a search", command)
		}
		if verb, ok := graphVerbFromCommand(command); ok {
			t.Errorf("graphVerbFromCommand(%q) = %q; nothing here runs the graph", command, verb)
		}
	}
}

// TestShellSubstitutionRecursionIsBoundedAndTotal: deep nesting stops at maxSubstitutionDepth, and
// unbalanced or pathological input terminates without panicking on both sides.
func TestShellSubstitutionRecursionIsBoundedAndTotal(t *testing.T) {
	t.Parallel()
	nest := func(levels int, inner string) string {
		// `$( $( inner))`: the space keeps `$( $(` from reading as `$((` arithmetic.
		return strings.Repeat("$( ", levels) + inner + strings.Repeat(")", levels)
	}
	// Within the bound the innermost call is still found, on both sides.
	if !isExploringShellCommand(nest(maxSubstitutionDepth, "grep -rn Foo .")) {
		t.Errorf("a search %d substitutions deep must count", maxSubstitutionDepth)
	}
	if verb, ok := graphVerbFromCommand(nest(maxSubstitutionDepth, "entire graph search --query foo")); !ok || verb != "search" {
		t.Errorf("a graph call %d substitutions deep must count, got %q, %v", maxSubstitutionDepth, verb, ok)
	}
	// Past the bound it is not, on either side — symmetrically.
	if isExploringShellCommand(nest(maxSubstitutionDepth+1, "grep -rn Foo .")) {
		t.Errorf("recursion must stop at depth %d", maxSubstitutionDepth)
	}
	if _, ok := graphVerbFromCommand(nest(maxSubstitutionDepth+1, "entire graph search --query foo")); ok {
		t.Errorf("recursion must stop at depth %d", maxSubstitutionDepth)
	}
	pathological := []string{
		strings.Repeat("$(", 100000),
		strings.Repeat("$(echo ", 5000) + "grep x" + strings.Repeat(")", 5000),
		strings.Repeat("`", 10001),
		`x=$(cd internal && grep -rn Foo .`,
		`x=$(echo "unterminated)`,
		"x=`grep -rn Foo .",
		`$((((((`,
		`"$(`,
		`'$(`,
		`\`,
	}
	for _, command := range pathological {
		_ = isExploringShellCommand(command)
		_, _ = graphVerbFromCommand(command)
		for _, body := range shellSubstitutionBodies(command) {
			if !strings.Contains(command, body) {
				t.Fatalf("body %q is not a substring of %q", body, command)
			}
		}
	}
	// An unterminated substitution still classifies through the unmasked top-level reading.
	if !isExploringShellCommand(`x=$(cd internal && grep -rn Foo .`) {
		t.Errorf("an unterminated substitution must fall back to the top-level reading")
	}
}

func FuzzShellSubstitutionClassifiers(f *testing.F) {
	for _, seed := range []string{
		`result=$(cd /repo && entire graph impact --symbol Foo --format json); echo "$result" | jq .`,
		`OUT=$(git status; entire graph search --query foo)`,
		"echo `cd /r && entire graph neighbors --symbol X`",
		`files=$(cd internal && grep -rln Foo .)`,
		`for f in $(cd internal && grep -rl Foo .); do echo $f; done`,
		"echo `cd /r && grep -rn Foo .`",
		`cat $(git log --oneline | head -5) > summary.txt`,
		`x="$(echo "$(date)" '$(no)')" ; y=$((1+2))`,
		"git commit -m \"$(cat <<'EOF'\nbody $(grep x)\nEOF\n)\"",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, command string) {
		start := time.Now()
		_ = isExploringShellCommand(command)
		_ = shellTimelineEntry(command)
		_, _ = graphVerbFromCommand(command)
		// Invalid UTF-8 becomes U+FFFD in the []rune view every splitter here uses, so the
		// substring property only holds for valid text; panics and termination are checked always.
		for _, body := range shellSubstitutionBodies(command) {
			if utf8.ValidString(command) && !strings.Contains(stripHeredocBodies(command), body) {
				t.Fatalf("body %q is not a substring of %q", body, command)
			}
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("classifying %d bytes took %s", len(command), elapsed)
		}
	})
}
