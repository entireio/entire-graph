package cli

import (
	"fmt"
	"sort"
	"strings"
)

// The preflight: prove the binary accepts the command line before a run depends on it.
//
// Three ways a search verb can stop answering, all observed or reproducible, all silent:
//
//   - a pre-delivered payload file that exists but is EMPTY — stdout got zero bytes and the process
//     exited 0, so the exit code, the harness and the transcript all recorded a healthy call;
//   - a session state file reused across tasks — every task after the first replayed the first
//     one's payload, naming files that are not in the tree the agent is looking at;
//   - a flag set built for a newer binary — exit 1 and an empty payload on every call.
//
// The first two now fail loudly at the call. The third does too, but it fails on the FIRST AGENT
// TURN of every instance, which is the worst possible time to find out: by then the run is already
// under way, and what a reviewer sees afterwards is a graph arm whose numbers look like the
// baseline. The fix is to find out BEFORE the run, from one cheap call that parses the exact
// command line the run is about to use and refuses if this binary cannot serve it.
//
// It parses; it never executes. That is what makes it safe to run against a production command
// line — no repository is touched, no index is built, nothing is written.
//
// It runs each command's REAL parser rather than a list of known flag names, so it cannot drift
// from what the command actually accepts — that drift is the whole failure being guarded against.
// The consequence is that where a parser also enforces required flags (verify wants a baseline),
// the preflight enforces them too. Assert the command line you actually intend to run, not a
// fragment of it.

// preflightParsers maps a command word to a parse-only check: it returns an error when this binary
// would reject the given arguments. Commands whose parsers return trailing arguments report those
// through unexpectedArgumentsError, so the preflight's message is the same one the real call would
// have produced.
var preflightParsers = map[string]func(version string, args []string) error{
	"query": func(version string, args []string) error {
		_, rest, err := parseSearchFlags(args)
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return unexpectedArgumentsError("query", version, rest)
		}
		return nil
	},
	"index": func(version string, args []string) error {
		_, rest, err := parseIndexFlags(args)
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return unexpectedArgumentsError("index", version, rest)
		}
		return nil
	},
	"stats": func(version string, args []string) error {
		_, rest, err := parseStatsFlags(args)
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return unexpectedArgumentsError("stats", version, rest)
		}
		return nil
	},
	"def":       func(_ string, args []string) error { _, err := parseDefFlags(args); return err },
	"impact":    func(_ string, args []string) error { _, err := parseImpactFlags(args); return err },
	"neighbors": func(_ string, args []string) error { _, err := parseNeighborFlags(args); return err },
	"verify":    func(_ string, args []string) error { _, err := parseVerifyFlags(args); return err },
}

// preflightCommands is the sorted command list, for the error a caller gets when it names something
// this binary cannot check.
func preflightCommands() []string {
	names := make([]string, 0, len(preflightParsers))
	for name := range preflightParsers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func splitPreflightCommandLine(spec string) ([]string, error) {
	var fields []string
	var field strings.Builder
	var quote byte
	started := false
	for index := 0; index < len(spec); index++ {
		character := spec[index]
		if quote == '\'' {
			if character == '\'' {
				quote = 0
			} else {
				field.WriteByte(character)
			}
			started = true
			continue
		}
		if quote == '"' {
			switch character {
			case '"':
				quote = 0
			case '\\':
				if index+1 >= len(spec) {
					return nil, fmt.Errorf("command line ends with an escape")
				}
				index++
				next := spec[index]
				if next == '"' || next == '\\' || next == '$' || next == '`' {
					field.WriteByte(next)
				} else {
					field.WriteByte('\\')
					field.WriteByte(next)
				}
			default:
				field.WriteByte(character)
			}
			started = true
			continue
		}
		switch character {
		case '\\':
			if index+1 >= len(spec) {
				return nil, fmt.Errorf("command line ends with an escape")
			}
			index++
			field.WriteByte(spec[index])
			started = true
		case '\'', '"':
			quote = character
			started = true
		case ' ', '\t', '\r', '\n':
			if started {
				fields = append(fields, field.String())
				field.Reset()
				started = false
			}
		default:
			field.WriteByte(character)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("command line has an unterminated quote")
	}
	if started {
		fields = append(fields, field.String())
	}
	return fields, nil
}

// checkPreflight parses one `<command> [args...]` string and reports whether this binary accepts it.
func checkPreflight(version, spec string) error {
	fields, err := splitPreflightCommandLine(spec)
	if err != nil {
		return fmt.Errorf("--assert %q: %w", spec, err)
	}
	if len(fields) == 0 {
		return fmt.Errorf("--assert needs a command line, for example --assert %q", "query --profile full")
	}
	command, args := fields[0], fields[1:]
	if command == "search" {
		command = "query"
	}
	parse, ok := preflightParsers[command]
	if !ok {
		return fmt.Errorf("--assert cannot check %q: this binary can check %s",
			command, strings.Join(preflightCommands(), ", "))
	}
	if err := parse(version, args); err != nil {
		return fmt.Errorf("--assert %q: %w", spec, err)
	}
	return nil
}
