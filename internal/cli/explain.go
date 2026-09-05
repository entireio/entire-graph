package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
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
//	( o=$(go test ./internal/configs -run '^TestX$' 2>&1); r=$?; printf '%s\n' "$o" | entire graph explain --repo .; exit $r )
//
// The status capture is not decoration. `<test> 2>&1 | entire graph explain` replaces the test's exit
// status with the PIPE's, which is this command's — so a failing test reports success and the agent
// stops. `set -o pipefail` cannot fix it either: the line runs in whatever shell the caller has, and
// on Debian-family systems /bin/sh is dash, where `set -o pipefail` is fatal rather than merely
// absent. The POSIX capture above keeps the test's own `$?` and exits with it.
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

	// explainMaxScannedNames bounds the distinct names the counter behind Scanned/Omitted remembers.
	// The scan now runs to end-of-input rather than stopping at MaxSymbols — it has to, because the
	// echo is that same read — so the dedupe set is no longer bounded by MaxSymbols and a hostile
	// build could otherwise grow it without limit. Scanned saturates here rather than growing: a
	// build naming more than a thousand distinct missing symbols is already "everything is broken",
	// and an exact count of it buys nothing worth unbounded memory.
	//
	// It is a FLOOR, not a ceiling: a caller asking for more symbols than this raises it (see
	// explainCandidates). The bound exists to keep the BUILD from choosing how much is remembered,
	// and the caller's own --max-symbols is not the build.
	explainMaxScannedNames = 1024
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

// explainLocationPatterns pull the FILE an error line is about. Every compiler prints it, and it is
// the only thing that tells `Config` in one package apart from `Config` in another — without it the
// resolver picks a same-named declaration by shape alone and can send the reader to a file the build
// never mentioned. Each pattern captures one path.
var explainLocationPatterns = []*regexp.Regexp{
	// Rust: "  --> src/lib.rs:3:5"
	regexp.MustCompile(`-->\s+([A-Za-z0-9_./\\+-]+\.[A-Za-z0-9_+]+):\d+`),
	// TypeScript: "src/a.ts(4,7): error TS2339: ..."
	regexp.MustCompile(`([A-Za-z0-9_./\\+-]+\.[A-Za-z0-9_+]+)\((\d+),\d+\):`),
	// Go, Java, C/C++, Python tracebacks: "./x.go:12:2:", "Foo.java:8:", "file \"a/b.py\", line 3"
	regexp.MustCompile(`(?:^|[\s"(\[])(?:\./)?([A-Za-z0-9_./\\+-]+\.[A-Za-z0-9_+]+):\d+`),
}

// explainOwnerPatterns pull the TYPE an error attributes a member to. When the build says the method
// belongs to `*Parser`, a declaration whose container is `Parser` is not a better guess than the
// others — it is the answer, and every other same-named method is noise.
var explainOwnerPatterns = []*regexp.Regexp{
	// Go: "p.cfg undefined (type *Parser has no field or method cfg)"
	regexp.MustCompile(`type \**([A-Za-z_][A-Za-z0-9_.]*) has no field or method`),
	// TypeScript: "Property 'brokenLinks' does not exist on type 'Cfg'."
	regexp.MustCompile(`does not exist on type '\**([A-Za-z_][A-Za-z0-9_]*)'`),
	// Python: "'Sheet' object has no attribute 'calcFormula'"
	regexp.MustCompile(`'([A-Za-z_][A-Za-z0-9_]*)' object has no attribute`),
	// PHP: "Call to undefined method RuleSet::getRuleNames()"
	regexp.MustCompile(`Call to undefined method\s+(?:[A-Za-z_][A-Za-z0-9_\\]*\\)?([A-Za-z_][A-Za-z0-9_]*)::`),
	// Java: "location: class Foo"
	regexp.MustCompile(`location:\s+(?:class|interface|enum)\s+(?:[A-Za-z_][A-Za-z0-9_.]*\.)?([A-Za-z_][A-Za-z0-9_]*)`),
}

// explainCandidate is one name the build named, together with what the same error line said about
// WHERE it lives. The context is carried per candidate rather than per run because one build reports
// several failures from several files, and attributing the last file seen to every name would be a
// worse guess than having no file at all.
type explainCandidate struct {
	Name  string
	File  string
	Owner string
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
	// Candidates is how many declarations carry this name when more than one does. The resolver picks
	// the best of them from the error's own file and type context, but a name like `Config` or `String`
	// can be defined in a dozen packages and the pick is still a pick. Saying so is the difference
	// between an answer and an assertion: a reader who sees "3 definitions" checks, and one who sees a
	// bare file:line does not.
	Candidates int `json:"candidates,omitempty"`
}

// ExplainResponse is the whole answer.
type ExplainResponse struct {
	Symbols []ExplainSymbol `json:"symbols"`
	// Scanned is how many candidate names the error text yielded, so a truncated answer is visible.
	Scanned int `json:"scanned"`
	Omitted int `json:"omitted,omitempty"`
	// Commit / Tree / WorktreeSnapshot are the provenance of the declarations above: WHICH tree these
	// line numbers and signatures describe. The text form says it in its header; without these a JSON
	// consumer had no way to tell a --head answer from a working-tree one, and on a clean tree the two
	// were byte-identical. Same field names and semantics as def, neighbors, and impact.
	Commit           string `json:"commit,omitempty"`
	Tree             string `json:"tree,omitempty"`
	WorktreeSnapshot bool   `json:"worktree_snapshot,omitempty"`
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
		// Spelled with the status capture, not a bare pipe: a bare pipe hands the caller this
		// command's exit status in place of the test's, so a failing test reads as a pass. Anyone who
		// copies the example gets the form that keeps the status.
		return errors.New("explain reads a failing build's output on stdin: pipe it, e.g. " +
			`( o=$(<verify command> 2>&1); r=$?; printf '%s\n' "$o" | entire graph explain --repo .; exit $r )`)
	}
	// PASS THE BUILD OUTPUT THROUGH, AS IT ARRIVES. This is what makes the command safe to bake into
	// the VERIFY line as `<test command> 2>&1 | entire graph explain`: a filter adds to what the agent
	// sees, a sink replaces it, and an agent that loses its own test output has been made worse off.
	//
	// It is also the only form that gets used at all. Measured over 27 sessions whose prompt carried an
	// explicit rule to pipe the failure into this command, with VERIFY present in 14 of their payloads:
	// ZERO agents typed the pipe, while 79 VERIFY-style commands were run. Instructing an agent to
	// compose two commands does not work; emitting one command that is already composed does.
	//
	// STREAMED, not buffered. This used to io.ReadAll the pipe and then convert the result to a string,
	// so a verbose or runaway build was held twice over in memory before a single byte was echoed —
	// unbounded input, on the exact command whose job is to survive a build that went wrong. The tee
	// below echoes each read as it happens and the scan keeps only one line plus a bounded name set,
	// so peak memory no longer depends on how much the build printed.
	echo := &explainEchoWriter{out: opts.Stdout}
	var source io.Reader = opts.Stdin
	if flags.Echo {
		source = io.TeeReader(opts.Stdin, echo)
	}
	candidates, scanned, scanErr := explainCandidates(source, flags.MaxSymbols)
	// Drain whatever the scan did not reach before deciding anything. A line longer than the scanner's
	// token limit stops the scan, and with the echo riding on the same reader that would silently
	// truncate the agent's own output — the one failure this command must never cause.
	if _, err := io.Copy(io.Discard, source); err != nil {
		return fmt.Errorf("reading build output from stdin: %w", err)
	}
	if err := echo.finish(); err != nil {
		return err
	}
	if scanErr != nil && !errors.Is(scanErr, bufio.ErrTooLong) {
		return fmt.Errorf("reading build output from stdin: %w", scanErr)
	}
	if len(candidates) == 0 {
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
	response := buildExplainResponse(snapshot, candidates, scanned)
	// One decision, both renderings. The text header and the JSON fields are computed from the same
	// expression so they can never disagree about which tree was read — the disagreement this command's
	// provenance work exists to remove.
	useHead := !flags.Worktree && snapshot.Header.Commit != ""
	response.Commit, response.Tree = snapshot.Header.Commit, snapshot.Header.Tree
	response.WorktreeSnapshot = !useHead
	switch flags.Format {
	case "json":
		encoder := json.NewEncoder(termsafe.NewJSONWriter(opts.Stdout))
		encoder.SetEscapeHTML(false)
		return encoder.Encode(response)
	case "text", "agent":
		_, err := opts.Stdout.Write(RenderExplainWithProvenance(response, flags.MaxContextBytes, useHead))
		return err
	default:
		return fmt.Errorf("explain --format must be json, text, or agent, got %q", flags.Format)
	}
}

// explainEchoWriter passes the piped build output through and remembers whether it ended in a
// newline, so the declaration block below it starts on a line of its own. It exists because the echo
// is now a STREAM: nothing holds the last byte any more, so the writer has to be the thing that
// noticed it.
type explainEchoWriter struct {
	out      io.Writer
	lastByte byte
	wrote    bool
}

func (w *explainEchoWriter) Write(chunk []byte) (int, error) {
	if len(chunk) == 0 {
		return 0, nil
	}
	written, err := w.out.Write(chunk)
	if written > 0 {
		w.wrote = true
		w.lastByte = chunk[written-1]
	}
	return written, err
}

// finish terminates the echoed output if the build did not.
func (w *explainEchoWriter) finish() error {
	if !w.wrote || w.lastByte == '\n' {
		return nil
	}
	_, err := io.WriteString(w.out, "\n")
	return err
}

// explainCandidateNames extracts the symbols an error names, in first-seen order — a build reports
// the root cause before the cascade, so order is information and must not be sorted away.
func explainCandidateNames(text string, limit int) []string {
	candidates, _, _ := explainCandidates(strings.NewReader(text), limit)
	if len(candidates) == 0 {
		return nil
	}
	names := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		names = append(names, candidate.Name)
	}
	return names
}

// explainCandidates scans a build's output as it arrives, reporting the names it can resolve and how
// many distinct names the build named in total.
//
// It reads to END OF INPUT even after it has collected `limit` of them, and that is deliberate twice
// over. The caller reads this same stream through an io.TeeReader that echoes it, so stopping the
// scan early would stop the ECHO early and the agent would lose the tail of its own test output.
// And the count it returns is what makes truncation visible: the collector used to return the moment
// it hit the limit, so `Scanned` was a restatement of the limit and `Omitted` was never set at all —
// a machine consumer could not tell exactly eight names from eight out of ninety.
//
// Nothing is retained beyond one line and a bounded name set, so the memory cost does not depend on
// how much the build printed.
func explainCandidates(input io.Reader, limit int) ([]explainCandidate, int, error) {
	if limit <= 0 {
		limit = explainMaxSymbols
	}
	// The name budget bounds what an UNTRUSTED build can make this process remember; it is not a
	// second opinion on what the CALLER may ask for. Those are different questions and only one of
	// them has an attacker on the other end. Collection stops when the set stops growing, so a
	// budget below `limit` silently answered a smaller question than the one asked: --max-symbols
	// 1500 resolved 1024 and reported nothing. Raising the budget to meet the limit costs nothing
	// that was not already spent — `candidates` is sized by `limit` regardless, and each entry
	// there is strictly heavier than a set key — while the build still cannot push either past a
	// bound the caller chose.
	budget := explainMaxScannedNames
	if limit > budget {
		budget = limit
	}
	seen := map[string]bool{}
	var candidates []explainCandidate
	scanned := 0
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		var file, owner string
		context := false
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
				if len(seen) >= budget {
					// The set is what dedupes, so once it stops growing, collecting stops with it —
					// otherwise a name already reported could be reported a second time. Scanned
					// saturates here rather than lying about a build this broken.
					continue
				}
				seen[name] = true
				scanned++
				if len(candidates) >= limit {
					continue
				}
				if !context {
					// Read once per line, not once per match: every name on one error line shares
					// that line's file and type context.
					file, owner, context = explainFirstMatch(explainLocationPatterns, line), explainFirstMatch(explainOwnerPatterns, line), true
				}
				candidates = append(candidates, explainCandidate{Name: name, File: file, Owner: owner})
			}
		}
	}
	return candidates, scanned, scanner.Err()
}

// explainFirstMatch returns the first capture any of the patterns finds on the line, or "".
func explainFirstMatch(patterns []*regexp.Regexp, line string) string {
	for _, pattern := range patterns {
		if match := pattern.FindStringSubmatch(line); len(match) >= 2 && match[1] != "" {
			return match[1]
		}
	}
	return ""
}

func buildExplainResponse(snapshot sem.ProviderSnapshot, candidates []explainCandidate, scanned int) ExplainResponse {
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
	if scanned < len(candidates) {
		scanned = len(candidates)
	}
	response := ExplainResponse{Scanned: scanned, Omitted: scanned - len(candidates)}
	for _, candidate := range candidates {
		group := byName[candidate.Name]
		if len(group) == 0 {
			response.Symbols = append(response.Symbols, ExplainSymbol{Query: candidate.Name})
			continue
		}
		anchor, best := group[0], explainRank(group[0], candidate, byID)
		for _, symbol := range group[1:] {
			if rank := explainRank(symbol, candidate, byID); rank.Less(best) {
				anchor, best = symbol, rank
			}
		}
		entry := ExplainSymbol{
			Query: candidate.Name, Name: anchor.Name, Kind: anchor.Kind, Signature: anchor.Signature,
			FilePath: anchor.FilePath, StartLine: anchor.StartLine, EndLine: anchor.EndLine,
			Resolved: true,
		}
		if len(group) > 1 {
			entry.Candidates = len(group)
		}
		if owner, ok := byID[anchor.ContainerID]; ok {
			entry.Owner = owner.Name
		}
		response.Symbols = append(response.Symbols, entry)
	}
	return response
}

// explainRankKey orders the same-named declarations a build error could be talking about. Lowest
// wins, and the fields are in the order a reader would resolve them by hand: the type the error
// named, then the file it named, and only then the shape of the declaration.
//
// Shape alone is what this used to sort by, and shape alone cannot tell two `Config` types in two
// packages apart — it returned whichever happened to be widest and discarded the rest, so an error
// about `internal/api.Config` could point at `internal/store/config.go` with no sign anything had
// been chosen. The error line already carries the answer; this reads it.
type explainRankKey struct {
	owner     int // 0 when the declaration's container is the type the error blamed
	file      int // 0 same file as the error, 1 same directory, 2 elsewhere
	signature int // 0 when the declaration carries a signature (a definition, not a forward decl)
	span      int // negative width, so the widest declaration sorts first among equals
}

// Less reports whether key sorts ahead of other.
func (key explainRankKey) Less(other explainRankKey) bool {
	switch {
	case key.owner != other.owner:
		return key.owner < other.owner
	case key.file != other.file:
		return key.file < other.file
	case key.signature != other.signature:
		return key.signature < other.signature
	default:
		return key.span < other.span
	}
}

// explainRank scores one candidate declaration against the context its error line carried. A context
// the build did not supply scores every declaration alike, so an absent file or type never reorders
// anything — it just leaves the decision to the field below it.
func explainRank(symbol sem.SymbolRecord, candidate explainCandidate, byID map[string]sem.SymbolRecord) explainRankKey {
	key := explainRankKey{signature: 1, span: -(symbol.EndLine - symbol.StartLine)}
	if symbol.Signature != "" {
		key.signature = 0
	}
	if candidate.Owner != "" {
		key.owner = 1
		if container, ok := byID[symbol.ContainerID]; ok && container.Name == candidate.Owner {
			key.owner = 0
		}
	}
	if candidate.File != "" {
		key.file = explainPathRank(symbol.FilePath, candidate.File)
	}
	return key
}

// explainPathRank scores a declaration's file against the file the error named: same file, same
// directory, or neither.
func explainPathRank(symbolPath, errorPath string) int {
	symbolPath = path.Clean(filepath.ToSlash(symbolPath))
	errorPath = path.Clean(filepath.ToSlash(errorPath))
	if explainSamePath(symbolPath, errorPath) {
		return 0
	}
	if symbolDir, errorDir := path.Dir(symbolPath), path.Dir(errorPath); errorDir != "." && explainSamePath(symbolDir, errorDir) {
		return 1
	}
	return 2
}

// explainSamePath reports whether two paths name the same thing as far as either can tell.
//
// Neither is a prefix of the other: a compiler prints a path relative to whatever directory it ran
// in and the graph records one relative to the repository root, so `./explain.go` and
// `internal/cli/explain.go` are the same file written from two places. Only the shared TAIL can be
// compared, and only on a component boundary — otherwise `cli.go` would match `internal/mycli.go`.
func explainSamePath(a, b string) bool {
	if a == b {
		return true
	}
	if len(a) < len(b) {
		a, b = b, a
	}
	return b != "" && b != "." && strings.HasSuffix(a, "/"+b)
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
		if symbol.Candidates > 1 {
			// The pick is a pick. A reader who is sent to the wrong `Config` has no way to know it
			// unless the block says the name was ambiguous.
			line += fmt.Sprintf(" [%d definitions]", symbol.Candidates)
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
