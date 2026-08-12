package cli

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type guideClaim struct {
	command string
	flag    string
	value   string
	line    int
}

var (
	guideCommandRE   = regexp.MustCompile(`\bentire graph ([a-z][a-z0-9-]*)`)
	guideFlagRE      = regexp.MustCompile(`--[a-z][a-z0-9-]*`)
	guideClaimFlagRE = regexp.MustCompile(`(?:^|[\s\[])(--[a-z][a-z0-9-]*)`)
	guideInlineRE    = regexp.MustCompile("`([^`]+)`")
	guideHeadingRE   = regexp.MustCompile(`^### .*?([a-z][a-z0-9-]*) —`)
	guideSectionRE   = regexp.MustCompile(`^#{1,3}\s`)
	guideNegativeRE  = regexp.MustCompile(
		"(?i)there is (?:\\*\\*)?no ((?:`--[a-z][a-z0-9-]*`(?:/)*)+) filter",
	)
	guideDefaultRE = regexp.MustCompile("`(--[a-z][a-z0-9-]*)`[^\\n]*\\(default: ([^)]+)\\)")
	// Markdown's other literal-code form: a 4-space (or tab) indented block. AGENTS.md uses
	// fences, the shipped agentGuide const uses indented blocks, and both are invocations.
	// Indentation alone is not enough to make a line one — see parseGuideClaims.
	guideIndentedCodeRE = regexp.MustCompile(`^(?: {4,}|\t)\S`)
)

func TestAgentGuideMatchesFlagRegistry(t *testing.T) {
	guidePath := filepath.Join("..", "..", "AGENTS.md")
	contents, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("read %s: %v", guidePath, err)
	}
	checkGuideClaims(t, "AGENTS.md", string(contents))
}

// TestAgentGuideConstMatchesFlagRegistry holds the shipped artifact to the same contract as
// AGENTS.md: `agent-guide` and `init-agents` hand consuming projects the embedded agentGuide
// const, not the repo-root file, so a parser change that only AGENTS.md tracked would still
// reach every install.
func TestAgentGuideConstMatchesFlagRegistry(t *testing.T) {
	checkGuideClaims(t, "internal/cli/agents.go agentGuide", agentGuide)
}

// checkGuideClaims asserts that every command, flag, negative and default claim a guide makes
// is backed by the real argument parsers and the help.go flag registry. origin labels the
// source in failure messages so a drift report points at the text that has to change.
func checkGuideClaims(t *testing.T, origin, source string) {
	t.Helper()

	commands, flags, negatives, defaults := parseGuideClaims(source)
	dispatched := make(map[string]bool, len(dispatchCommands))
	for _, command := range dispatchCommands {
		dispatched[command] = true
	}

	for _, claim := range commands {
		_, documented := findCommandDoc(claim.command)
		if !documented || !dispatched[claim.command] {
			t.Errorf("%s:%d names command %q, but commandDocs/Run do not both register it", origin, claim.line, claim.command)
		}
	}
	for _, claim := range flags {
		accepted, reachable := parserAcceptsFlag(claim.command, claim.flag)
		if !reachable {
			t.Logf("SKIP %s:%d parser check for %s %s: command parser is not reachable from this test", origin, claim.line, claim.command, claim.flag)
		} else if !accepted {
			t.Errorf("%s:%d guide/parser drift: %s uses %s, but its real argument parser does not accept it; fix %s or the parser", origin, claim.line, claim.command, claim.flag, origin)
		}
		if !commandHasFlag(claim.command, claim.flag) {
			if reachable && accepted {
				t.Errorf("%s:%d parser/help drift: %s parser accepts %s, but internal/cli/help.go does not document it; fix help.go", origin, claim.line, claim.command, claim.flag)
			} else {
				t.Errorf("%s:%d guide/help drift: %s uses %s, but internal/cli/help.go does not document it; fix %s or help.go", origin, claim.line, claim.command, claim.flag, origin)
			}
		}
	}
	for _, claim := range negatives {
		accepted, reachable := parserAcceptsFlag(claim.command, claim.flag)
		if !reachable {
			t.Logf("SKIP %s:%d negative parser check for %s %s: command parser is not reachable from this test", origin, claim.line, claim.command, claim.flag)
		} else if accepted {
			t.Errorf("%s:%d guide/parser drift: guide says %s has no %s filter, but its real argument parser accepts it; fix %s", origin, claim.line, claim.command, claim.flag, origin)
		}
		if accepted && !commandHasFlag(claim.command, claim.flag) {
			t.Errorf("%s:%d parser/help drift: %s parser accepts %s, but internal/cli/help.go does not document it; fix help.go", origin, claim.line, claim.command, claim.flag)
		} else if commandHasFlag(claim.command, claim.flag) {
			t.Errorf("%s:%d guide/help drift: guide says %s has no %s filter, but internal/cli/help.go documents it; fix %s", origin, claim.line, claim.command, claim.flag, origin)
		}
	}
	for _, claim := range defaults {
		docDefault, ok := commandFlagDefault(claim.command, claim.flag)
		if !ok || docDefault != claim.value {
			t.Errorf("%s:%d documents %s %s default %q, but the flag registry says %q", origin, claim.line, claim.command, claim.flag, claim.value, docDefault)
		}
		parserDefault, ok := parserFlagDefault(t, claim.command, claim.flag)
		if !ok {
			t.Errorf("%s:%d documents a default for %s %s, but the contract test cannot read that parser default", origin, claim.line, claim.command, claim.flag)
		} else if parserDefault != claim.value {
			t.Errorf("%s:%d documents %s %s default %q, but the parser uses %q", origin, claim.line, claim.command, claim.flag, claim.value, parserDefault)
		}
	}
}

func TestParseGuideNegativeClaims(t *testing.T) {
	guide := "### edges — relations\nThere is context, but there is no `--to`/`--from` filter.\n"
	_, _, negatives, _ := parseGuideClaims(guide)
	if len(negatives) != 2 || negatives[0].flag != "--to" || negatives[1].flag != "--from" {
		t.Fatalf("negative claims = %#v, want --to and --from", negatives)
	}
}

func TestParseGuideDefaultClaims(t *testing.T) {
	guide := "### impact — radius\n- `--depth` 1|2 (default: 2)\n"
	_, _, _, defaults := parseGuideClaims(guide)
	if len(defaults) != 1 || defaults[0].command != "impact" || defaults[0].flag != "--depth" || defaults[0].value != "2" {
		t.Fatalf("default claims = %#v, want impact --depth default 2", defaults)
	}
}

func TestParseGuideProseFlagClaims(t *testing.T) {
	guide := strings.Join([]string{
		"### neighbors — relations",
		"- `--internal-only` drops external endpoints.",
		"- `entire graph impact --depth 1` names another command explicitly.",
		"- `ordinary prose` is not a flag claim.",
		"- `not--a-flag` is not a flag token.",
		"## Operating doctrine",
		"Use `--outside-section` only as an example.",
	}, "\n")

	_, flags, _, _ := parseGuideClaims(guide)
	if len(flags) != 2 {
		t.Fatalf("flag claims = %#v, want exactly two", flags)
	}
	if flags[0].command != "neighbors" || flags[0].flag != "--internal-only" {
		t.Errorf("section claim = %#v, want neighbors --internal-only", flags[0])
	}
	if flags[1].command != "impact" || flags[1].flag != "--depth" {
		t.Errorf("explicit claim = %#v, want impact --depth", flags[1])
	}
}

func TestParseGuideIndentedCodeClaims(t *testing.T) {
	guide := strings.Join([]string{
		"### search — locate",
		"Run it like this:",
		"    entire graph search --repo . --profile full",
		"Prose mentioning --repo outside a code block is not a claim.",
	}, "\n")

	_, flags, _, _ := parseGuideClaims(guide)
	if len(flags) != 2 {
		t.Fatalf("flag claims = %#v, want exactly two", flags)
	}
	if flags[0].command != "search" || flags[0].flag != "--repo" {
		t.Errorf("first claim = %#v, want search --repo", flags[0])
	}
	if flags[1].command != "search" || flags[1].flag != "--profile" {
		t.Errorf("second claim = %#v, want search --profile", flags[1])
	}
}

// TestParseGuideIndentedProseIsNotAnInvocation pins the other half of the indented-block rule.
// A wrapped list item is indented exactly like a code block, so an indented line that does not
// name a command is prose — claiming its flags against the enclosing section would report drift
// in a command the guide never associated with that flag.
func TestParseGuideIndentedProseIsNotAnInvocation(t *testing.T) {
	guide := strings.Join([]string{
		"### neighbors — relations",
		"- a bullet whose second line wraps and happens to mention",
		"    --index-all-files, which belongs to search, not neighbors",
	}, "\n")

	if _, flags, _, _ := parseGuideClaims(guide); len(flags) != 0 {
		t.Errorf("flag claims = %#v, want none from indented prose", flags)
	}
}

func TestParserAcceptsFlag(t *testing.T) {
	tests := []struct {
		command   string
		flag      string
		accepted  bool
		reachable bool
	}{
		{command: "edges", flag: "--to", accepted: true, reachable: true},
		{command: "edges", flag: "--not-a-real-flag", accepted: false, reachable: true},
		{command: "impact", flag: "--depth", accepted: true, reachable: true},
		{command: "impact", flag: "--not-a-real-flag", accepted: false, reachable: true},
		{command: "commit", flag: "--json", accepted: false, reachable: false},
	}
	for _, test := range tests {
		t.Run(test.command+"/"+test.flag, func(t *testing.T) {
			accepted, reachable := parserAcceptsFlag(test.command, test.flag)
			if accepted != test.accepted || reachable != test.reachable {
				t.Fatalf("parserAcceptsFlag(%q, %q) = (%t, %t), want (%t, %t)", test.command, test.flag, accepted, reachable, test.accepted, test.reachable)
			}
		})
	}
}

func parseGuideClaims(guide string) (commands, flags, negatives, defaults []guideClaim) {
	inFence := false
	sectionCommand := ""
	for index, line := range strings.Split(guide, "\n") {
		lineNumber := index + 1
		if guideSectionRE.MatchString(line) {
			sectionCommand = ""
			if match := guideHeadingRE.FindStringSubmatch(line); match != nil {
				sectionCommand = match[1]
			}
		}

		for _, match := range guideCommandRE.FindAllStringSubmatch(line, -1) {
			commands = append(commands, guideClaim{command: match[1], line: lineNumber})
		}

		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		// An indented block counts as an invocation only when it names the command outright.
		// Unlike a fence, indentation is also how Markdown continues a wrapped list item, so
		// without that second test a bare --flag in continuation prose would be claimed against
		// whatever heading preceded it, and reported as drift in a command that never had it.
		if inFence || (guideIndentedCodeRE.MatchString(line) && guideCommandRE.MatchString(line)) {
			flags = append(flags, invocationFlagClaims(line, sectionCommand, lineNumber)...)
		} else {
			for _, match := range guideInlineRE.FindAllStringSubmatch(line, -1) {
				flags = append(flags, invocationFlagClaims(match[1], sectionCommand, lineNumber)...)
			}
		}

		if match := guideNegativeRE.FindStringSubmatch(line); match != nil && sectionCommand != "" {
			for _, flag := range guideFlagRE.FindAllString(match[1], -1) {
				negatives = append(negatives, guideClaim{command: sectionCommand, flag: flag, line: lineNumber})
			}
		}
		if match := guideDefaultRE.FindStringSubmatch(line); match != nil && sectionCommand != "" {
			defaults = append(defaults, guideClaim{command: sectionCommand, flag: match[1], value: match[2], line: lineNumber})
		}
	}
	return commands, flags, negatives, defaults
}

func invocationFlagClaims(invocation, sectionCommand string, line int) []guideClaim {
	match := guideCommandRE.FindStringSubmatch(invocation)
	command := sectionCommand
	if match != nil {
		command = match[1]
	}
	if command == "" {
		return nil
	}
	var claims []guideClaim
	for _, flagMatch := range guideClaimFlagRE.FindAllStringSubmatch(invocation, -1) {
		claims = append(claims, guideClaim{command: command, flag: flagMatch[1], line: line})
	}
	return claims
}

func commandHasFlag(command, flag string) bool {
	_, ok := findCommandFlag(command, flag)
	return ok
}

func commandFlagDefault(command, flag string) (string, bool) {
	candidate, ok := findCommandFlag(command, flag)
	return candidate.def, ok && candidate.def != ""
}

func findCommandFlag(command, flag string) (flagDoc, bool) {
	doc, ok := findCommandDoc(command)
	if !ok {
		return flagDoc{}, false
	}
	for _, candidate := range doc.flags {
		if candidate.name == flag {
			return candidate, true
		}
	}
	return flagDoc{}, false
}

func parserAcceptsFlag(command, flag string) (accepted, reachable bool) {
	switch command {
	case "symbols", "edges", "snapshot":
		_, rest, err := parseProviderFlags([]string{flag})
		return err != nil || len(rest) == 0, true
	case "search":
		_, rest, err := parseSearchFlags([]string{flag})
		return err != nil || len(rest) == 0, true
	case "index":
		_, rest, err := parseIndexFlags([]string{flag})
		return err != nil || len(rest) == 0, true
	case "stats":
		_, rest, err := parseStatsFlags([]string{flag})
		return err != nil || len(rest) == 0, true
	case "impact":
		_, err := parseImpactFlags([]string{"--symbol", "contract-test", flag})
		return parserRecognizedStrictFlag(err), true
	case "neighbors":
		_, err := parseNeighborFlags([]string{"--symbol", "contract-test", flag})
		return parserRecognizedStrictFlag(err), true
	case "def":
		_, err := parseDefFlags([]string{"contract-test", flag})
		return parserRecognizedStrictFlag(err), true
	case "capabilities":
		// capabilities has no flag parser: runCapabilities IS its argument validator, and it
		// is side-effect-free apart from the JSON it encodes, so calling it with the discard
		// writer asks the real acceptor rather than a test-side copy of its rule.
		return runCapabilities(Options{Stdout: io.Discard}, []string{flag}) == nil, true
	default:
		return false, false
	}
}

func parserRecognizedStrictFlag(err error) bool {
	return err == nil || !strings.Contains(err.Error(), "unexpected argument")
}

func parserFlagDefault(t *testing.T, command, flag string) (string, bool) {
	t.Helper()
	switch command {
	case "impact":
		defaults, err := parseImpactFlags([]string{"--symbol", "contract-test"})
		if err != nil {
			t.Fatalf("parse impact defaults: %v", err)
		}
		if flag == "--depth" {
			return strconv.Itoa(defaults.Depth), true
		}
	}
	return "", false
}
