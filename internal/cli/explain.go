package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

// explain — turn a FAILING BUILD into the declarations it is complaining about.
//
// Why this exists, measured on a 30-instance paired haiku run (baseline vs graph-assisted, same
// instances, same prompt discipline):
//
//	                  baseline    graph
//	pre-edit explore     9.43      1.34    <- the locate phase is already won, 86% removed
//	post-edit explore    8.60      6.76    <- barely moved
//	post-edit read       3.57      4.07    <- moved the WRONG way
//	verify/fix total    17.57     16.17    <- 50% of the assisted session, -7.9%
//
// The graph has taken essentially everything available in the locate phase; half of what remains is
// the edit->build->fix loop, and the graph did nothing for it. The reason is not that the graph lacks
// the answer — `def` has it — but that the agent never asks: offering `def` in the prompt for exactly
// this case produced 0.04 calls per session across 28 sessions. Telling an agent to choose a tool
// after its build fails does not work; it is already in the shell, so it greps.
//
// So this is not another instruction. It is a command that composes with the build in ONE shell call,
// and the VERIFY block emits it pre-composed, so the agent runs the command it was given and the
// declarations arrive with the failure:
//
//	go test ./internal/configs -run '^TestX$' 2>&1 | entire graph explain --repo .
//
// One turn replaces build -> grep symbol -> read range. It quotes no source it was not asked for and
// it stays read-only: the build is run by the shell, not by this process, so the provider remains a
// pure analyzer.
const (
	// explainMaxSymbols bounds how many distinct names are resolved. A cascading build failure can
	// name hundreds; the first few are the cause and the rest are consequences.
	explainMaxSymbols = 8

	// explainMaxBytes bounds the whole block, on the same reasoning as the search payload: bytes here
	// are replayed into the model on every later turn.
	explainMaxBytes = 2048
)

// explainErrorPatterns are the ways compilers and test runners name a symbol they cannot resolve.
// Each pattern captures ONE identifier. They are deliberately conservative: a pattern that also
// matched ordinary prose would fill the block with words that are not symbols, which is strictly
// worse than an empty block.
var explainErrorPatterns = []*regexp.Regexp{
	// Go: "undefined: foo", "x.foo undefined (type T has no field or method foo)"
	regexp.MustCompile(`undefined:\s+([A-Za-z_][A-Za-z0-9_.]*)`),
	regexp.MustCompile(`has no field or method\s+([A-Za-z_][A-Za-z0-9_]*)`),
	regexp.MustCompile(`declared and not used:\s*([A-Za-z_][A-Za-z0-9_]*)`),
	// Rust: "cannot find function `foo`", "no method named `foo`", "unresolved import `a::b`"
	regexp.MustCompile("cannot find (?:function|value|type|struct|trait|macro)[^`]*`([A-Za-z_][A-Za-z0-9_]*)`"),
	regexp.MustCompile("no method named `([A-Za-z_][A-Za-z0-9_]*)`"),
	regexp.MustCompile("no field `([A-Za-z_][A-Za-z0-9_]*)`"),
	// Java / Kotlin: "cannot find symbol\n  symbol: method foo(int)"
	regexp.MustCompile(`symbol:\s+(?:method|variable|class)\s+([A-Za-z_][A-Za-z0-9_]*)`),
	regexp.MustCompile(`cannot find symbol[^\n]*?([A-Za-z_][A-Za-z0-9_]{2,})\s*\(`),
	// TypeScript / JavaScript: "Property 'foo' does not exist", "Cannot find name 'foo'"
	regexp.MustCompile(`Property '([A-Za-z_][A-Za-z0-9_]*)' does not exist`),
	regexp.MustCompile(`Cannot find name '([A-Za-z_][A-Za-z0-9_]*)'`),
	regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*) is not defined`),
	regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*) is not a function`),
	// Python: "AttributeError: 'T' object has no attribute 'foo'", "NameError: name 'foo' is not defined"
	regexp.MustCompile(`has no attribute '([A-Za-z_][A-Za-z0-9_]*)'`),
	regexp.MustCompile(`name '([A-Za-z_][A-Za-z0-9_]*)' is not defined`),
	// PHP: "Call to undefined method C::foo()"
	regexp.MustCompile(`Call to undefined (?:method|function)\s+(?:[A-Za-z_][A-Za-z0-9_]*::)?([A-Za-z_][A-Za-z0-9_]*)`),
	// Ruby: "undefined method `foo' for"
	regexp.MustCompile("undefined (?:method|local variable or method) `([A-Za-z_][A-Za-z0-9_?!]*)'"),
	// Generic arity/type complaints that still name the callee.
	regexp.MustCompile(`(?:not enough|too many) arguments (?:in call to|to)\s+([A-Za-z_][A-Za-z0-9_.]*)`),
}

// ExplainSymbol is one resolved declaration.
type ExplainSymbol struct {
	Query     string `json:"query"`
	Name      string `json:"name,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Signature string `json:"signature,omitempty"`
	FilePath  string `json:"file_path,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Owner     string `json:"owner,omitempty"`
	// Resolved is false when the error named something the graph does not index. Reported rather than
	// hidden: "the repository does not define this" is itself the answer the agent needs, and a silent
	// omission would read as "no information".
	Resolved bool `json:"resolved"`
}

// ExplainResponse is the whole answer.
type ExplainResponse struct {
	Symbols []ExplainSymbol `json:"symbols"`
	// Scanned is how many candidate names the error text yielded, so a truncated answer is visible.
	Scanned int `json:"scanned"`
	Omitted int `json:"omitted,omitempty"`
}

type explainFlags struct {
	Repo            string
	Profile         string
	CacheDir        string
	Format          string
	Worktree        bool
	DisableCache    bool
	MaxSymbols      int
	MaxContextBytes int
	// Echo passes stdin through before the block, so the command is a FILTER and can be baked into
	// the VERIFY line without hiding the test output it was piped from. --no-echo restores the
	// block-only form for callers that already have the output.
	Echo        bool
	IgnoreFile  []string
	IncludeFile []string
}

func runExplain(ctx context.Context, opts Options, args []string) error {
	flags, err := parseExplainFlags(args)
	if err != nil {
		return err
	}
	if opts.Stdin == nil {
		// An embedder that wired no stdin gets a clear error rather than a nil dereference: this
		// command is meaningless without piped input, so that is a usage mistake worth naming.
		return fmt.Errorf("explain reads a failing build's output on stdin: pipe it, e.g. `<verify command> 2>&1 | entire graph explain --repo .`")
	}
	text, err := io.ReadAll(opts.Stdin)
	if err != nil {
		return fmt.Errorf("reading build output from stdin: %w", err)
	}
	// PASS THE BUILD OUTPUT THROUGH FIRST. This is what makes the command safe to bake into the VERIFY
	// line as `<test command> 2>&1 | entire graph explain`: a filter adds to what the agent sees, a
	// sink replaces it, and an agent that loses its own test output has been made worse off.
	//
	// It is also the only form that gets used at all. Measured over 27 sessions whose prompt carried an
	// explicit rule to pipe the failure into this command, with VERIFY present in 14 of their payloads:
	// ZERO agents typed the pipe, while 79 VERIFY-style commands were run. Instructing an agent to
	// compose two commands does not work; emitting one command that is already composed does.
	if flags.Echo && len(text) > 0 {
		if _, err := opts.Stdout.Write(text); err != nil {
			return err
		}
		if text[len(text)-1] != '\n' {
			if _, err := io.WriteString(opts.Stdout, "\n"); err != nil {
				return err
			}
		}
	}
	names := explainCandidateNames(string(text), flags.MaxSymbols)
	if len(names) == 0 {
		// Silence is the right answer: the build output named nothing this command can resolve, and a
		// header over an empty list would claim otherwise.
		return nil
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	profile, err := parseProfile(flags.Profile)
	if err != nil {
		return err
	}
	cacheDir := flags.CacheDir
	if cacheDir == "" {
		cacheDir = opts.Env.PluginDataDir
	}
	// Worktree is the default on purpose: the agent has just edited the tree, and a declaration read
	// from HEAD would describe code that no longer exists. --head remains available for callers that
	// explicitly want the committed baseline.
	snapshot, _, err := sem.LoadOrBuildProviderSnapshot(ctx, repo, opts.Version, sem.ProviderSnapshotOptions{
		NoNetwork:    true,
		Worktree:     flags.Worktree,
		IgnoreFiles:  flags.IgnoreFile,
		IncludeFiles: flags.IncludeFile,
		Profile:      profile,
	}, cacheDir, flags.DisableCache)
	if err != nil {
		return err
	}
	response := buildExplainResponse(snapshot, names)
	switch flags.Format {
	case "json":
		encoder := json.NewEncoder(opts.Stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(response)
	case "text", "agent":
		useHead := !flags.Worktree && snapshot.Header.Commit != ""
		_, err := opts.Stdout.Write(RenderExplainWithProvenance(response, flags.MaxContextBytes, useHead))
		return err
	default:
		return fmt.Errorf("explain --format must be json, text, or agent, got %q", flags.Format)
	}
}

// explainCandidateNames extracts the symbols an error names, in first-seen order — a build reports
// the root cause before the cascade, so order is information and must not be sorted away.
func explainCandidateNames(text string, limit int) []string {
	if limit <= 0 {
		limit = explainMaxSymbols
	}
	seen := map[string]bool{}
	var names []string
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		for _, pattern := range explainErrorPatterns {
			for _, match := range pattern.FindAllStringSubmatch(line, -1) {
				if len(match) < 2 {
					continue
				}
				name := match[1]
				// A qualified name resolves on its last segment: `pkg.Foo` is indexed as `Foo`.
				if cut := strings.LastIndex(name, "."); cut >= 0 && cut+1 < len(name) {
					name = name[cut+1:]
				}
				if name == "" || seen[name] {
					continue
				}
				seen[name] = true
				names = append(names, name)
				if len(names) >= limit {
					return names
				}
			}
		}
	}
	return names
}

func buildExplainResponse(snapshot sem.ProviderSnapshot, names []string) ExplainResponse {
	byName := map[string][]sem.SymbolRecord{}
	byID := map[string]sem.SymbolRecord{}
	for _, symbol := range snapshot.Symbols {
		byID[symbol.ID] = symbol
	}
	for _, symbol := range snapshot.Symbols {
		if symbol.Name == "" {
			continue
		}
		byName[symbol.Name] = append(byName[symbol.Name], symbol)
	}
	response := ExplainResponse{Scanned: len(names)}
	for _, name := range names {
		group := byName[name]
		if len(group) == 0 {
			response.Symbols = append(response.Symbols, ExplainSymbol{Query: name})
			continue
		}
		// Prefer a definition with a signature and the widest span: that is the declaration, not a
		// forward declaration or a same-named local.
		sort.SliceStable(group, func(a, b int) bool {
			sa, sb := group[a], group[b]
			if (sa.Signature != "") != (sb.Signature != "") {
				return sa.Signature != ""
			}
			return sa.EndLine-sa.StartLine > sb.EndLine-sb.StartLine
		})
		anchor := group[0]
		entry := ExplainSymbol{
			Query: name, Name: anchor.Name, Kind: anchor.Kind, Signature: anchor.Signature,
			FilePath: anchor.FilePath, StartLine: anchor.StartLine, EndLine: anchor.EndLine,
			Resolved: true,
		}
		if owner, ok := byID[anchor.ContainerID]; ok {
			entry.Owner = owner.Name
		}
		response.Symbols = append(response.Symbols, entry)
	}
	return response
}

// RenderExplain prints the block. Unresolved names are listed last and on one line: they are a short
// negative fact, not an entry worth a paragraph.
func RenderExplain(response ExplainResponse, maxBytes int) []byte {
	return RenderExplainWithProvenance(response, maxBytes, false)
}

// RenderExplainWithProvenance renders the same declaration block while making
// its source view explicit. useHead is true only when a committed snapshot was
// actually selected; the no-HEAD fallback therefore keeps the worktree label.
func RenderExplainWithProvenance(response ExplainResponse, maxBytes int, useHead bool) []byte {
	if len(response.Symbols) == 0 {
		return nil
	}
	if maxBytes <= 0 {
		maxBytes = explainMaxBytes
	}
	var buffer strings.Builder
	header := ExplainHeader
	if useHead {
		header = ExplainHeadHeader
	}
	buffer.WriteString(header + "\n")
	var missing []string
	for _, symbol := range response.Symbols {
		if !symbol.Resolved {
			// Echoed from the build output the caller piped in, which is itself
			// produced by a compiler reading repository names.
			missing = append(missing, termsafe.Line(symbol.Query))
			continue
		}
		// One declaration per line, so every field is a one-line value: the path
		// is a raw Git pathname and the signature is a declaration lifted off a
		// line of the scanned tree. Escaped here rather than by wrapping the
		// writer, because the byte budget below is measured on these strings and
		// has to count what is actually printed.
		line := fmt.Sprintf("  %s:%d-%d", termsafe.Line(symbol.FilePath), symbol.StartLine, symbol.EndLine)
		if symbol.Kind != "" {
			line += " " + termsafe.Line(symbol.Kind)
		}
		line += " " + termsafe.Line(symbol.Name)
		if symbol.Owner != "" {
			line += " (in " + termsafe.Line(symbol.Owner) + ")"
		}
		line += "\n"
		if symbol.Signature != "" {
			line += "      " + termsafe.Line(strings.Join(strings.Fields(symbol.Signature), " ")) + "\n"
		}
		if buffer.Len()+len(line) > maxBytes {
			break
		}
		buffer.WriteString(line)
	}
	if len(missing) > 0 {
		line := "  not defined in this repository: " + strings.Join(missing, ", ") + "\n"
		if buffer.Len()+len(line) <= maxBytes {
			buffer.WriteString(line)
		}
	}
	return []byte(buffer.String())
}

// ExplainHeader is exported so the guide and its test cannot drift from what is printed, the same
// discipline the literal-cluster and file-outline block names are under.
const ExplainHeader = "DECLARATIONS THE BUILD ERROR NAMED (from the working tree, so your own edits are included)"

// ExplainHeadHeader labels the explicit committed-tree view selected by --head.
const ExplainHeadHeader = "DECLARATIONS THE BUILD ERROR NAMED (from committed HEAD, so working-tree edits are excluded)"

func parseExplainFlags(args []string) (explainFlags, error) {
	flags := explainFlags{
		Format: "text", Profile: "full", Worktree: true,
		MaxSymbols: explainMaxSymbols, MaxContextBytes: explainMaxBytes, Echo: true,
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		value := func() (string, error) {
			index++
			if index >= len(args) {
				return "", fmt.Errorf("%s requires a value", arg)
			}
			return args[index], nil
		}
		var err error
		switch arg {
		case "--repo":
			flags.Repo, err = value()
		case "--profile":
			flags.Profile, err = value()
		case "--cache-dir":
			flags.CacheDir, err = value()
		case "--format":
			flags.Format, err = value()
		case "--head":
			flags.Worktree = false
		case "--worktree":
			flags.Worktree = true
		case "--no-cache":
			flags.DisableCache = true
		case "--no-echo":
			flags.Echo = false
		case "--max-symbols":
			parsed, next, parseErr := searchNonNegativeIntFlag(args, index)
			if parseErr != nil {
				return flags, parseErr
			}
			flags.MaxSymbols, index = parsed, next
		case "--max-context-bytes":
			parsed, next, parseErr := searchNonNegativeIntFlag(args, index)
			if parseErr != nil {
				return flags, parseErr
			}
			flags.MaxContextBytes, index = parsed, next
		case "--ignore-file":
			var v string
			if v, err = value(); err == nil {
				flags.IgnoreFile = append(flags.IgnoreFile, v)
			}
		case "--include-file":
			var v string
			if v, err = value(); err == nil {
				flags.IncludeFile = append(flags.IncludeFile, v)
			}
		default:
			return flags, fmt.Errorf("unknown explain flag %q", arg)
		}
		if err != nil {
			return flags, err
		}
	}
	return flags, nil
}
