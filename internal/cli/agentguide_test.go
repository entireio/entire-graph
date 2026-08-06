package cli

import (
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
	guideCommandRE  = regexp.MustCompile(`\bentire graph ([a-z][a-z0-9-]*)`)
	guideFlagRE     = regexp.MustCompile(`--[a-z][a-z0-9-]*`)
	guideInlineRE   = regexp.MustCompile("`([^`]+)`")
	guideHeadingRE  = regexp.MustCompile(`^### .*?([a-z][a-z0-9-]*) —`)
	guideNegativeRE = regexp.MustCompile(
		"there is (?:\\*\\*)?no ((?:`--[a-z][a-z0-9-]*`(?:/)*)+) filter",
	)
	guideDefaultRE = regexp.MustCompile("`(--[a-z][a-z0-9-]*)`[^\\n]*\\(default: ([^)]+)\\)")
)

func TestAgentGuideMatchesFlagRegistry(t *testing.T) {
	guidePath := filepath.Join("..", "..", "AGENTS.md")
	contents, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("read %s: %v", guidePath, err)
	}

	commands, flags, negatives, defaults := parseGuideClaims(string(contents))
	dispatched := make(map[string]bool, len(dispatchCommands))
	for _, command := range dispatchCommands {
		dispatched[command] = true
	}

	for _, claim := range commands {
		_, documented := findCommandDoc(claim.command)
		if !documented || !dispatched[claim.command] {
			t.Errorf("AGENTS.md:%d names command %q, but commandDocs/Run do not both register it", claim.line, claim.command)
		}
	}
	for _, claim := range flags {
		accepted, reachable := parserAcceptsFlag(claim.command, claim.flag)
		if !reachable {
			t.Logf("SKIP AGENTS.md:%d parser check for %s %s: command parser is not reachable from this test", claim.line, claim.command, claim.flag)
		} else if !accepted {
			t.Errorf("AGENTS.md:%d guide/parser drift: %s uses %s, but its real argument parser does not accept it; fix AGENTS.md or the parser", claim.line, claim.command, claim.flag)
		}
		if !commandHasFlag(claim.command, claim.flag) {
			if reachable && accepted {
				t.Errorf("AGENTS.md:%d parser/help drift: %s parser accepts %s, but internal/cli/help.go does not document it; fix help.go", claim.line, claim.command, claim.flag)
			} else {
				t.Errorf("AGENTS.md:%d guide/help drift: %s uses %s, but internal/cli/help.go does not document it; fix AGENTS.md or help.go", claim.line, claim.command, claim.flag)
			}
		}
	}
	for _, claim := range negatives {
		accepted, reachable := parserAcceptsFlag(claim.command, claim.flag)
		if !reachable {
			t.Logf("SKIP AGENTS.md:%d negative parser check for %s %s: command parser is not reachable from this test", claim.line, claim.command, claim.flag)
		} else if accepted {
			t.Errorf("AGENTS.md:%d guide/parser drift: guide says %s has no %s filter, but its real argument parser accepts it; fix AGENTS.md", claim.line, claim.command, claim.flag)
		}
		if accepted && !commandHasFlag(claim.command, claim.flag) {
			t.Errorf("AGENTS.md:%d parser/help drift: %s parser accepts %s, but internal/cli/help.go does not document it; fix help.go", claim.line, claim.command, claim.flag)
		} else if commandHasFlag(claim.command, claim.flag) {
			t.Errorf("AGENTS.md:%d guide/help drift: guide says %s has no %s filter, but internal/cli/help.go documents it; fix AGENTS.md", claim.line, claim.command, claim.flag)
		}
	}
	for _, claim := range defaults {
		docDefault, ok := commandFlagDefault(claim.command, claim.flag)
		if !ok || docDefault != claim.value {
			t.Errorf("AGENTS.md:%d documents %s %s default %q, but the flag registry says %q", claim.line, claim.command, claim.flag, claim.value, docDefault)
		}
		parserDefault, ok := parserFlagDefault(t, claim.command, claim.flag)
		if !ok {
			t.Errorf("AGENTS.md:%d documents a default for %s %s, but the contract test cannot read that parser default", claim.line, claim.command, claim.flag)
		} else if parserDefault != claim.value {
			t.Errorf("AGENTS.md:%d documents %s %s default %q, but the parser uses %q", claim.line, claim.command, claim.flag, claim.value, parserDefault)
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
		if match := guideHeadingRE.FindStringSubmatch(line); match != nil {
			sectionCommand = match[1]
		}

		for _, match := range guideCommandRE.FindAllStringSubmatch(line, -1) {
			commands = append(commands, guideClaim{command: match[1], line: lineNumber})
		}

		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			flags = append(flags, invocationFlagClaims(line, lineNumber)...)
		} else {
			for _, match := range guideInlineRE.FindAllStringSubmatch(line, -1) {
				flags = append(flags, invocationFlagClaims(match[1], lineNumber)...)
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

func invocationFlagClaims(invocation string, line int) []guideClaim {
	match := guideCommandRE.FindStringSubmatch(invocation)
	if match == nil {
		return nil
	}
	var claims []guideClaim
	for _, flag := range guideFlagRE.FindAllString(invocation, -1) {
		claims = append(claims, guideClaim{command: match[1], flag: flag, line: line})
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
