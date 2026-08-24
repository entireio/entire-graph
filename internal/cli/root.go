package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/entireio/entire-graph/internal/gitutil"
	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

type Options struct {
	Version string
	Env     EntireEnv
	Stdout  io.Writer
	Stderr  io.Writer
	// Stdin is where `explain` reads a failing build's output from. It is the only command that takes
	// piped input, because it is the only one whose question ("what are these names the build is
	// complaining about") is asked by composing with another command rather than by naming a symbol.
	Stdin io.Reader
}

// SignalError reports that Execute stopped because it received a signal
// asking the process to terminate (SIGINT/SIGTERM), rather than because the
// command itself failed.
//
// signal.NotifyContext turns the signal into an ordinary context.Canceled,
// indistinguishable from any other cancellation once it reaches Run's
// return value — so without this, main's generic "print the error, exit 1"
// path reported an operator's Ctrl-C the same way it reports a real command
// failure, regressing the exit statuses (130 for SIGINT, 143 for SIGTERM) a
// program with no signal handling at all would have had for free, and that
// shells and supervisors already know how to interpret. Callers that care
// can use errors.As to recover the signal and choose the conventional
// 128+signal status instead.
type SignalError struct {
	Signal os.Signal
	Err    error
}

func (e *SignalError) Error() string { return e.Err.Error() }
func (e *SignalError) Unwrap() error { return e.Err }

// Execute is the process entry point. It runs the command under a context that
// is actually cancellable, which the provider path depends on: every phase of
// the indexer polls ctx.Err() (see internal/sem/provider.go), so under the
// previous context.Background() those checks could never fire and an
// interrupted index could only be stopped by killing the process mid-write.
//
// The first SIGINT/SIGTERM cancels the context and restores the default signal
// handler, so a second one still kills the process immediately. Without that
// restore, installing a handler would make a runaway index HARDER to stop than
// before, which is the opposite of the point.
//
// Signal delivery is handled with signal.Notify rather than
// signal.NotifyContext specifically so the received signal itself survives
// past cancellation, for SignalError below — NotifyContext's context carries
// no record of which signal (or that one at all, versus a caller-driven
// cancellation) caused Done to close. The signal race itself lives in
// runUnderSignals, split out so it is testable without going through real
// command dispatch.
func Execute(version string, args []string) error {
	return runUnderSignals(func(ctx context.Context) error {
		return Run(ctx, Options{
			Version: version,
			Env:     EnvFromOS(),
			Stdout:  os.Stdout,
			Stderr:  os.Stderr,
			Stdin:   os.Stdin,
		}, args)
	})
}

// runUnderSignals runs run under a context canceled by the first
// SIGINT/SIGTERM this process receives, and wraps a non-nil result in a
// *SignalError when that signal is what stopped it.
func runUnderSignals(run func(context.Context) error) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// caughtCh, not a plain variable: it is written from this goroutine and
	// read from the one running run after it returns, and a channel handoff
	// is what makes that safe without a separate lock. The send
	// happens-before cancel() in program order, and cancel() closing
	// ctx.Done() happens-before run observes it, so if run returned BECAUSE
	// of this signal, the send below is already complete by the time the
	// non-blocking receive after run runs.
	caughtCh := make(chan os.Signal, 1)
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case sig := <-sigCh:
			caughtCh <- sig
			cancel()
			// Restore the default disposition so a second signal kills the
			// process immediately, matching the guarantee the previous
			// NotifyContext-based handler already made.
			signal.Stop(sigCh)
		case <-stopWatch:
		}
	}()

	err := run(ctx)

	var caught os.Signal
	select {
	case caught = <-caughtCh:
	default:
	}
	if err != nil && caught != nil {
		return &SignalError{Signal: caught, Err: err}
	}
	return err
}

const writeBytesChunkSize = 64 * 1024

// writeBytesWithContext writes b to w in chunks, returning ctx.Err() as soon as
// the context is canceled. A cache hit can be megabytes; a plain Write would
// ignore SIGINT until the whole buffer is flushed.
func writeBytesWithContext(ctx context.Context, w io.Writer, b []byte) error {
	for len(b) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := writeBytesChunkSize
		if n > len(b) {
			n = len(b)
		}
		written, err := w.Write(b[:n])
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		b = b[written:]
	}
	return nil
}

func Run(ctx context.Context, opts Options, args []string) error {
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}

	if len(args) == 0 {
		printHelp(opts.Stdout)
		return nil
	}

	// Per-command help: `entire graph <cmd> --help` prints that command's detail
	// view and exits, without touching each runX. Only fires for commands that
	// have a doc entry — which includes `help` itself, so `help --help` prints
	// help's own detail view rather than the root listing. A bare `--help` has
	// no command word and is handled above.
	if wantsHelp(args[1:]) {
		if _, ok := findCommandDoc(args[0]); ok {
			renderCommandHelp(opts.Stdout, args[0])
			return nil
		}
	}

	switch args[0] {
	case "diff":
		return runDiff(ctx, opts, args[1:])
	case "commit":
		return runCommit(ctx, opts, args[1:])
	case "checkpoint":
		return runCheckpoint(ctx, opts, args[1:])
	case "analyze":
		return runAnalyze(ctx, opts, args[1:])
	case "doctor":
		return runDoctor(ctx, opts, args[1:])
	case "capabilities":
		return runCapabilities(opts, args[1:])
	case "snapshot":
		return runProviderRecords(ctx, opts, args[1:], "snapshot")
	case "snapshot-query":
		return runSnapshotQuery(opts, args[1:])
	case "symbols":
		return runProviderRecords(ctx, opts, args[1:], "symbols")
	case "edges":
		return runProviderRecords(ctx, opts, args[1:], "edges")
	case "search":
		return runSearch(ctx, opts, args[1:])
	case "index":
		return runIndex(ctx, opts, args[1:])
	case "def":
		return runDef(ctx, opts, args[1:])
	case "explain":
		return runExplain(ctx, opts, args[1:])
	case "neighbors":
		return runNeighbors(ctx, opts, args[1:])
	case "impact":
		return runImpact(ctx, opts, args[1:])
	case "verify":
		return runVerify(ctx, opts, args[1:])
	case "stats":
		return runStats(ctx, opts, args[1:])
	case "agent-guide":
		return runAgentGuide(opts, args[1:])
	case "init-agents":
		return runInitAgents(opts, args[1:])
	case "version", "--version", "-v":
		if len(args) > 1 && args[1] == "--json" {
			return json.NewEncoder(opts.Stdout).Encode(map[string]string{
				"provider": sem.ProviderName,
				"version":  opts.Version,
			})
		}
		fmt.Fprintln(opts.Stdout, opts.Version)
		return nil
	case "help", "--help", "-h":
		printHelp(opts.Stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q; run \"entire graph help\" to list commands", args[0])
	}
}

// printHelp renders the grouped root listing. Per-command detail lives in
// renderCommandHelp; both read from the commandDocs registry in help.go.
func printHelp(out io.Writer) {
	renderRootHelp(out)
}

func runDoctor(ctx context.Context, opts Options, args []string) error {
	asJSON := false
	var asserts []string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--json":
			asJSON = true
		case "--assert":
			// Repeatable: a harness usually drives more than one verb, and finding out about the
			// second one only after the first has been fixed costs another whole run.
			if index+1 >= len(args) {
				return errors.New("doctor --assert needs a command line, for example --assert \"search --profile full\"")
			}
			index++
			asserts = append(asserts, args[index])
		default:
			return errors.New("doctor accepts only --json and --assert \"<command line>\"")
		}
	}
	// The assertions run FIRST and stop the report: a caller that asked whether this binary can
	// serve its command line wants that answer, and printing an otherwise-healthy environment
	// report above the failure buries it.
	for _, spec := range asserts {
		if err := checkPreflight(opts.Version, spec); err != nil {
			return err
		}
	}
	if len(asserts) > 0 && !asJSON {
		for _, spec := range asserts {
			fmt.Fprintf(opts.Stdout, "assert_ok=%q\n", spec)
		}
	}
	report := map[string]any{
		"provider":  sem.ProviderName,
		"version":   opts.Version,
		"no_egress": true,
		"environment": map[string]string{
			envCLIVersion:    valueOrUnset(opts.Env.CLIVersion),
			envRepoRoot:      valueOrUnset(opts.Env.RepoRoot),
			envPluginDataDir: valueOrUnset(opts.Env.PluginDataDir),
		},
		"phase_1_local_only": map[string]bool{
			"fetch_remote_code":              false,
			"download_grammars_or_assets":    false,
			"upload_telemetry":               false,
			"call_hosted_model_apis":         false,
			"call_remote_embedding_provider": false,
			"perform_network_discovery":      false,
		},
	}
	if len(asserts) > 0 {
		// Reaching here means every assertion parsed, so the JSON says so explicitly rather than
		// leaving a caller to infer success from the absence of an error.
		report["asserted_command_lines"] = asserts
	}
	if !asJSON {
		fmt.Fprintf(opts.Stdout, "ENTIRE_CLI_VERSION=%s\n", valueOrUnset(opts.Env.CLIVersion))
		fmt.Fprintf(opts.Stdout, "ENTIRE_REPO_ROOT=%s\n", valueOrUnset(opts.Env.RepoRoot))
		fmt.Fprintf(opts.Stdout, "ENTIRE_PLUGIN_DATA_DIR=%s\n", valueOrUnset(opts.Env.PluginDataDir))
		fmt.Fprintln(opts.Stdout, "no_egress=true")
	}

	if opts.Env.PluginDataDir != "" {
		if err := os.MkdirAll(opts.Env.PluginDataDir, 0o700); err != nil {
			return fmt.Errorf("create plugin data dir: %w", err)
		}
		probe, err := os.CreateTemp(opts.Env.PluginDataDir, ".write-test-*")
		if err != nil {
			return fmt.Errorf("write plugin data dir: %w", err)
		}
		probeName := probe.Name()
		if err := probe.Close(); err != nil {
			return fmt.Errorf("close plugin data probe: %w", err)
		}
		if err := os.Remove(probeName); err != nil {
			return fmt.Errorf("remove plugin data probe: %w", err)
		}
		report["plugin_data_dir"] = "writable"
		if !asJSON {
			fmt.Fprintln(opts.Stdout, "plugin_data_dir=writable")
		}
	}

	repo, err := resolveRepo(ctx, opts.Env, "")
	if err != nil {
		report["repo_root"] = ""
		report["repo_error"] = err.Error()
		if asJSON {
			return json.NewEncoder(opts.Stdout).Encode(report)
		}
		fmt.Fprintf(opts.Stdout, "repo_root=%s\n", valueOrUnset(""))
		fmt.Fprintf(opts.Stdout, "repo_error=%s\n", err)
		return nil
	}
	report["repo_root"] = repo
	if asJSON {
		return json.NewEncoder(opts.Stdout).Encode(report)
	}
	fmt.Fprintf(opts.Stdout, "repo_root=%s\n", repo)
	return nil
}

func runCapabilities(opts Options, args []string) error {
	if len(args) != 1 || args[0] != "--json" {
		return errors.New("capabilities requires --json")
	}
	return json.NewEncoder(opts.Stdout).Encode(sem.Capabilities())
}

func runProviderRecords(ctx context.Context, opts Options, args []string, mode string) error {
	flags, rest, err := parseProviderFlags(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return unexpectedArgumentsError(mode, opts.Version, rest)
	}
	if mode != "snapshot" && mode != "symbols" && mode != "edges" {
		return fmt.Errorf("unknown provider record mode %q", mode)
	}
	filterActive := flags.To != "" || flags.From != "" || len(flags.Relation) > 0
	compact := flags.Format == "compact-ndjson"
	if flags.Format != "ndjson" && !compact {
		if mode == "snapshot" {
			return fmt.Errorf("%s requires --format ndjson or compact-ndjson", mode)
		}
		return fmt.Errorf("%s requires --format ndjson", mode)
	}
	if compact && mode != "snapshot" {
		return errors.New("--format compact-ndjson is only valid for snapshot")
	}
	if compact && filterActive {
		return errors.New("--format compact-ndjson requires a complete snapshot; remove --to/--from/--relation")
	}
	if compact && flags.MaxSeconds > 0 {
		// A compact artifact is DEFINED to be a complete snapshot: LoadCompactSnapshot
		// and snapshot-query accept whatever prefix they are handed, with no place to
		// carry E_ANALYSIS_BUDGET_EXCEEDED forward, so a truncated compact file turns
		// every symbol that was never reached into a confident negative answer. The
		// NDJSON stream can say "partial"; this format cannot, so the combination is
		// refused rather than written.
		return errors.New("--format compact-ndjson requires a complete snapshot and cannot be combined with --max-seconds; use --format ndjson (which reports E_ANALYSIS_BUDGET_EXCEEDED when truncated), or --max-seconds 0")
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	profile, err := parseProfile(flags.Profile)
	if err != nil {
		return err
	}
	options := sem.ProviderSnapshotOptions{
		NoNetwork:    flags.NoNetwork,
		Worktree:     flags.Worktree,
		IgnoreFiles:  flags.IgnoreFiles,
		IncludeFiles: flags.IncludeFiles,
		Profile:      profile,
	}
	if flags.MaxSeconds > 0 {
		options.MaxDuration = time.Duration(flags.MaxSeconds) * time.Second
	}
	if flags.Progress {
		options.Progress = func(event sem.ProgressEvent) {
			fmt.Fprintf(opts.Stderr, "graph progress phase=%s files=%d/%d symbols=%d relations=%d heap=%d rss=%d elapsed=%s\n",
				event.Phase,
				event.FilesDone,
				event.FilesTotal,
				event.Symbols,
				event.Relations,
				event.HeapAlloc,
				event.MaxRSSBytes,
				event.Elapsed.Round(time.Millisecond),
			)
		}
	}
	// Stream records straight to stdout so peak memory does not scale with the
	// relation count on large repositories.
	newRecordEncoder := func(out io.Writer) func(any) error {
		if compact {
			return sem.NewCompactSnapshotEncoder(out).Encode
		}
		encoder := json.NewEncoder(out)
		encoder.SetEscapeHTML(false) // match json.Marshal used elsewhere (no < escaping)
		return encoder.Encode
	}
	encodeRecord := newRecordEncoder(opts.Stdout)

	// Targeted edge query: when --to/--from/--relation is set, emit only matching
	// relations (plus header/summary), never files/symbols. Turns "callers of X"
	// into a tiny reply instead of dumping the whole graph for the caller to grep.
	// Streaming-safe: idMatches keys off the stable ID's trailing name segment, so
	// no in-memory symbol table is needed. Only meaningful in edges/snapshot modes.
	// Capture the summary as it streams past so we can loudly warn on a partial
	// parse. Without this the CLI discards the summary and a run that silently
	// parsed only a fraction of the repo (e.g. a mis-scoped subdir) looks clean.
	var summary *sem.SnapshotSummary
	capture := func(record any) {
		if s, ok := record.(sem.SnapshotSummary); ok {
			s := s
			summary = &s
		}
	}

	if filterActive && mode == "symbols" {
		return fmt.Errorf("--to/--from/--relation filter relations; use `edges` (not `symbols`)")
	}
	if filterActive {
		var matched int
		if err := sem.StreamSnapshot(ctx, repo, opts.Version, options, func(record any) error {
			capture(record)
			switch r := record.(type) {
			case sem.RelationRecord:
				if !relationMatches(r, flags) {
					return nil
				}
				matched++
				return encodeRecord(r)
			case sem.FileRecord, sem.ExternalRecord, sem.SymbolRecord:
				return nil // suppressed for a targeted edge query
			default: // header, summary
				return encodeRecord(record)
			}
		}); err != nil {
			return err
		}
		warnIfPartial(opts.Stderr, flags.Worktree, summary)
		if budgetTruncated(summary) {
			fmt.Fprintln(opts.Stderr, "graph: index stopped at the --max-seconds budget; the matched edges are partial")
		}
		fmt.Fprintf(opts.Stderr, "graph: %d edge(s) matched (--to=%q --from=%q --relation=%s)\n",
			matched, flags.To, flags.From, strings.Join(flags.Relation, ","))
		return nil
	}

	// Whole-graph dump (no targeted filter): serve from the tree-hash record
	// cache when possible. The cache is keyed on the HEAD tree, the mode, and the
	// output-affecting options, so a repeat call on an unchanged HEAD skips the
	// expensive re-index. It is deliberately bypassed for --worktree (the working
	// tree may differ from HEAD) and, by returning above, for targeted queries.
	cacheDir := resolveCacheDir(flags.CacheDir, opts.Env.PluginDataDir)
	useCache := !flags.DisableCache && !flags.Worktree && cacheDir != ""
	var tree string
	if useCache {
		if t, err := gitutil.RevParse(ctx, repo, "HEAD^{tree}"); err == nil && t != "" {
			tree = t
		} else {
			useCache = false
		}
	}
	cacheMode := mode
	if compact {
		cacheMode = "snapshot:compact-ndjson-v1"
	}
	if useCache {
		if records, cachedSummary, hit, err := sem.LoadProviderRecords(ctx, repo, opts.Version, tree, cacheMode, cacheDir, options); err == nil && hit {
			if err := writeBytesWithContext(ctx, opts.Stdout, records); err != nil {
				return err
			}
			warnIfPartial(opts.Stderr, flags.Worktree, cachedSummary)
			return nil
		}
	}

	// On a miss, tee the serialized record stream into a buffer so we can persist it after
	// a successful run without a second pass over the graph.
	var recordBuf bytes.Buffer
	if useCache {
		encodeRecord = newRecordEncoder(io.MultiWriter(opts.Stdout, &recordBuf))
	}
	if err := sem.StreamSnapshot(ctx, repo, opts.Version, options, func(record any) error {
		capture(record)
		if !includeRecord(mode, record) {
			return nil
		}
		return encodeRecord(record)
	}); err != nil {
		return err
	}
	warnIfPartial(opts.Stderr, flags.Worktree, summary)
	if budgetTruncated(summary) {
		// A budget-truncated graph must never become the cached answer for this
		// tree: the cache key deliberately does not include the budget, so a
		// stored truncation would be served to every later caller as if it were
		// the complete index.
		fmt.Fprintln(opts.Stderr, "graph: index stopped at the --max-seconds budget; result is partial and was not cached")
		useCache = false
	}
	if useCache {
		// Best effort: a failed cache write never fails the command.
		_ = sem.StoreProviderRecords(ctx, repo, opts.Version, tree, cacheMode, cacheDir, options, recordBuf.Bytes(), summary)
	}
	return nil
}

// budgetTruncated reports whether a snapshot stopped at its wall-clock ceiling
// rather than finishing. Such a result is valid to print but must not be
// persisted or treated as a complete index.
func budgetTruncated(s *sem.SnapshotSummary) bool {
	if s == nil {
		return false
	}
	for _, failure := range s.PartialFailures {
		if failure.Code == sem.AnalysisBudgetExceededCode {
			return true
		}
	}
	return false
}

// warnIfPartial prints a loud stderr banner when the snapshot did not fully cover
// the repository, so a silent partial parse (the #1 sharp edge: running in a
// mis-scoped subdir without --worktree, which indexes only a stray config file
// and reports "ok") becomes impossible to miss. Silent on a clean "ok" run.
func warnIfPartial(w io.Writer, worktree bool, s *sem.SnapshotSummary) {
	if s == nil {
		return
	}
	level := s.Stats.CompletenessLevel
	if level == "" || level == "ok" {
		return
	}
	fmt.Fprintf(w, "\n⚠️  graph is %s: parsed %d/%d files, %d symbols, %d relations (languages: %s).\n",
		strings.ToUpper(level), s.Stats.ParsedFiles, s.Stats.Files, s.Stats.Symbols,
		s.Stats.Relations, strings.Join(s.Languages, ", "))
	switch {
	case s.Stats.Files <= 2 && !worktree:
		fmt.Fprintf(w, "   Only %d file(s) were discovered — you may be indexing a subdirectory or an\n"+
			"   unexpected commit. Run from the repo root, or pass --worktree to index the\n"+
			"   working tree instead of HEAD.\n", s.Stats.Files)
	case s.Stats.ParsedFiles*2 < s.Stats.Files:
		fmt.Fprintf(w, "   Over half the discovered files were not parsed (unsupported language or\n"+
			"   parse errors). Graph queries will miss code in those files.\n")
	default:
		fmt.Fprintf(w, "   The graph is incomplete; treat query results as partial.\n")
	}
}

// includeRecord filters streamed records for the symbols and edges modes, which
// emit a subset of the full snapshot.
func includeRecord(mode string, record any) bool {
	switch record.(type) {
	case sem.FileRecord, sem.ExternalRecord:
		return mode == "snapshot"
	case sem.SymbolRecord:
		return mode == "snapshot" || mode == "symbols"
	case sem.RelationRecord:
		return mode == "snapshot" || mode == "edges"
	default: // header, summary
		return true
	}
}

// defaultMaxSeconds is the wall-clock budget applied to diff/commit/analyze
// when --max-seconds is not given. Large repos with hot changed names can
// otherwise grind for many minutes and produce nothing; with the budget the
// command stops cleanly at the limit and emits the partial result plus
// machine-readable W_ANALYSIS_BUDGET_EXCEEDED warnings. --max-seconds 0
// disables the budget.
const defaultMaxSeconds = 120

// maxSecondsCeiling is the largest --max-seconds value that still fits in a
// time.Duration once multiplied by time.Second (~292 years). strconv.Atoi
// happily accepts every int64, but seconds*time.Second above this wraps to a
// NEGATIVE duration: context.WithDeadline would then fire in the past, and the
// provider commands' "MaxDuration > 0" test would be false, silently disabling
// the very ceiling the operator asked for.
const maxSecondsCeiling = int64(math.MaxInt64) / int64(time.Second)

type commonFlags struct {
	Repo     string
	JSON     bool
	Progress bool
	// MaxSeconds is the overall analysis budget in seconds; -1 means the flag
	// was not given (commands apply their default), 0 means unlimited.
	MaxSeconds int
}

type providerFlags struct {
	Repo         string
	Format       string
	Profile      string
	NoNetwork    bool
	Worktree     bool
	Progress     bool
	IgnoreFiles  []string
	IncludeFiles []string
	// MaxSeconds is the wall-clock ceiling for the index build in seconds.
	// -1 means the flag was not given, 0 means unlimited. Unlike diff/commit
	// these commands do NOT apply defaultMaxSeconds when the flag is absent:
	// a full index of a large repository legitimately runs for minutes, and
	// silently truncating one by default would turn a slow index into a wrong
	// one. The ceiling is opt-in.
	MaxSeconds int
	// Targeted edge filters (edges mode). When any is set the command emits only
	// the matching relation records (plus header/summary) instead of the whole
	// graph, so "callers of X" is a tiny reply rather than a 50MB dump that the
	// caller then greps client-side. --to/--from match a symbol by full stable ID
	// or by trailing name segment (IDs are `...:kind:name`); --relation is one or
	// more edge types (comma-separated, case-insensitive), e.g. CALLS,REFERENCES.
	To       string
	From     string
	Relation []string
	// CacheDir/DisableCache control the tree-hash record cache. Empty CacheDir
	// falls back to ENTIRE_PLUGIN_DATA_DIR; --no-cache disables it entirely.
	CacheDir     string
	DisableCache bool
}

// idMatches reports whether a stable symbol ID matches a user-supplied selector:
// either the exact ID, or a trailing name segment (IDs end `...:kind:name`, so
// `getConfPath` or `function:getConfPath` both select it). Streaming-safe — no
// symbol table needed.
func idMatches(id, sel string) bool {
	return id == sel || strings.HasSuffix(id, ":"+sel)
}

// relationMatches applies the --to/--from/--relation predicate to one edge.
func relationMatches(r sem.RelationRecord, f providerFlags) bool {
	if f.To != "" && !idMatches(r.ToID, f.To) {
		return false
	}
	if f.From != "" && !idMatches(r.FromID, f.From) {
		return false
	}
	if len(f.Relation) > 0 {
		t := strings.ToUpper(r.Type)
		hit := false
		for _, want := range f.Relation {
			if t == want {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// parseProfile validates the --profile value. Empty defaults to full.
func parseProfile(value string) (sem.Profile, error) {
	switch value {
	case "", "full":
		return sem.ProfileFull, nil
	case "fast":
		return sem.ProfileFast, nil
	case "syntax-only":
		return sem.ProfileSyntaxOnly, nil
	default:
		return "", fmt.Errorf("unknown --profile %q (want full, fast, or syntax-only)", value)
	}
}

func parseProviderFlags(args []string) (providerFlags, []string, error) {
	flags := providerFlags{Format: "ndjson", MaxSeconds: -1}
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--repo":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--repo requires a value")
			}
			flags.Repo = args[i]
		case "--format":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--format requires a value")
			}
			flags.Format = args[i]
		case "--profile":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--profile requires a value")
			}
			flags.Profile = args[i]
		case "--max-seconds":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--max-seconds requires a value")
			}
			seconds, err := strconv.Atoi(args[i])
			if err != nil || seconds < 0 {
				return flags, nil, fmt.Errorf("--max-seconds requires a non-negative integer, got %q", args[i])
			}
			if int64(seconds) > maxSecondsCeiling {
				return flags, nil, fmt.Errorf("--max-seconds must be at most %d (larger values overflow time.Duration and disable the ceiling), got %q", maxSecondsCeiling, args[i])
			}
			flags.MaxSeconds = seconds
		case "--no-network":
			flags.NoNetwork = true
		case "--worktree":
			flags.Worktree = true
		case "--progress":
			flags.Progress = true
		case "--ignore-file":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--ignore-file requires a value")
			}
			flags.IgnoreFiles = append(flags.IgnoreFiles, args[i])
		case "--include-file":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--include-file requires a value")
			}
			flags.IncludeFiles = append(flags.IncludeFiles, args[i])
		case "--cache-dir":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--cache-dir requires a value")
			}
			flags.CacheDir = args[i]
		case "--no-cache":
			flags.DisableCache = true
		case "--to":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--to requires a value")
			}
			flags.To = args[i]
		case "--from":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--from requires a value")
			}
			flags.From = args[i]
		case "--relation":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--relation requires a value")
			}
			for _, part := range strings.Split(args[i], ",") {
				if part = strings.ToUpper(strings.TrimSpace(part)); part != "" {
					flags.Relation = append(flags.Relation, part)
				}
			}
		default:
			rest = append(rest, args[i])
		}
	}
	return flags, rest, nil
}

func parseCommonFlags(args []string) (commonFlags, []string, error) {
	flags := commonFlags{MaxSeconds: -1}
	var rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--json":
			flags.JSON = true
		case "--progress":
			flags.Progress = true
		case "--max-seconds":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--max-seconds requires a value")
			}
			seconds, err := strconv.Atoi(args[i])
			if err != nil || seconds < 0 {
				return flags, nil, fmt.Errorf("--max-seconds requires a non-negative integer, got %q", args[i])
			}
			if int64(seconds) > maxSecondsCeiling {
				return flags, nil, fmt.Errorf("--max-seconds must be at most %d (larger values overflow time.Duration and disable the ceiling), got %q", maxSecondsCeiling, args[i])
			}
			flags.MaxSeconds = seconds
		case "--repo":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--repo requires a value")
			}
			flags.Repo = args[i]
		case "--":
			rest = append(rest, args[i+1:]...)
			return flags, rest, nil
		default:
			rest = append(rest, arg)
		}
	}
	return flags, rest, nil
}

func runCommit(ctx context.Context, opts Options, args []string) error {
	flags, rest, err := parseCommonFlags(args)
	if err != nil {
		return err
	}
	rev := "HEAD"
	if len(rest) > 0 {
		rev = rest[0]
	}
	if len(rest) > 1 {
		return errors.New("commit accepts at most one revision")
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	base, err := gitutil.FirstParent(ctx, repo, rev)
	if err != nil {
		return err
	}
	return analyzeAndPrint(ctx, opts, repo, base, rev, nil, flags)
}

func runCheckpoint(ctx context.Context, opts Options, args []string) error {
	flags, rest, err := parseCommonFlags(args)
	if err != nil {
		return err
	}
	if flags.Progress {
		return errors.New("checkpoint does not support --progress")
	}
	if flags.MaxSeconds >= 0 {
		return errors.New("checkpoint does not support --max-seconds")
	}
	if len(rest) != 1 {
		return errors.New("checkpoint requires exactly one checkpoint ID")
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	result, err := sem.AnalyzeCheckpoint(ctx, repo, rest[0])
	if err != nil {
		return err
	}
	return printResult(opts.Stdout, result, flags.JSON, opts.Version)
}

func runAnalyze(ctx context.Context, opts Options, args []string) error {
	return runDiff(ctx, opts, args)
}

// diffFlags is the parsed argument set for `diff` and its `analyze` alias.
type diffFlags struct {
	common commonFlags
	base   string
	head   string
	paths  []string
}

// parseDiffFlags parses `diff`/`analyze` arguments, returning any flag-shaped argument it does
// not recognize separately from the path filters, so the caller can reject it the way every
// other verb does.
//
// Separating the two is the point. The parsing used to live inline in runDiff, whose default
// branch filed EVERY unrecognized token under paths — so `diff --jsonn` exited 0 having filtered
// the diff to a path that does not exist, and printed "No semantic entity changes detected". A
// mistyped flag was answered with a confident empty result, which for a verb whose whole job is
// telling you what changed is the worst possible way to be wrong.
//
// Paths that genuinely look like flags still work: that is what the documented `-- path...`
// separator is for.
func parseDiffFlags(args []string) (diffFlags, []string, error) {
	parsed := diffFlags{base: "HEAD~1", head: "HEAD"}

	// Split on `--` here rather than letting parseCommonFlags consume it: that function flattens
	// everything after the separator into rest, which would leave a literal path named `--base`
	// indistinguishable from the real flag.
	flagArgs, literalPaths := args, []string(nil)
	for i, arg := range args {
		if arg == "--" {
			flagArgs, literalPaths = args[:i], args[i+1:]
			break
		}
	}

	common, rest, err := parseCommonFlags(flagArgs)
	if err != nil {
		return diffFlags{}, nil, err
	}
	parsed.common = common

	var unknown []string
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--base":
			i++
			if i >= len(rest) {
				return diffFlags{}, nil, errors.New("--base requires a value")
			}
			parsed.base = rest[i]
		case "--head":
			i++
			if i >= len(rest) {
				return diffFlags{}, nil, errors.New("--head requires a value")
			}
			parsed.head = rest[i]
		default:
			if strings.HasPrefix(rest[i], "-") && rest[i] != "-" {
				unknown = append(unknown, rest[i])
				continue
			}
			parsed.paths = append(parsed.paths, rest[i])
		}
	}
	parsed.paths = append(parsed.paths, literalPaths...)
	return parsed, unknown, nil
}

func runDiff(ctx context.Context, opts Options, args []string) error {
	parsed, unknown, err := parseDiffFlags(args)
	if err != nil {
		return err
	}
	if len(unknown) != 0 {
		return unexpectedArgumentsError("diff", opts.Version, unknown)
	}

	repo, err := resolveRepo(ctx, opts.Env, parsed.common.Repo)
	if err != nil {
		return err
	}
	return analyzeAndPrint(ctx, opts, repo, parsed.base, parsed.head, parsed.paths, parsed.common)
}

func resolveRepo(ctx context.Context, env EntireEnv, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if env.RepoRoot != "" {
		return env.RepoRoot, nil
	}
	return gitutil.RepoRoot(ctx, ".")
}

func analyzeAndPrint(ctx context.Context, opts Options, repo, base, head string, paths []string, flags commonFlags) error {
	maxSeconds := flags.MaxSeconds
	if maxSeconds < 0 {
		maxSeconds = defaultMaxSeconds
	}
	analyzeOptions := sem.AnalyzeOptions{
		MaxDuration: time.Duration(maxSeconds) * time.Second,
	}
	if flags.Progress {
		analyzeOptions.Progress = func(event sem.AnalyzeProgressEvent) {
			fmt.Fprintln(opts.Stderr, diffProgressLine(event))
		}
	}
	result, err := sem.AnalyzeGitRangeWithOptions(ctx, repo, base, head, paths, analyzeOptions)
	if err != nil {
		return err
	}
	return printResult(opts.Stdout, result, flags.JSON, opts.Version)
}

// diffProgressLine renders one --progress event for stderr.
//
// It is a named function rather than an inline closure because of the escaping:
// event.Path is a raw Git pathname carried through from `git diff -z`, which may
// hold any byte but NUL and '/', and this is the one sink that has to escape by
// value instead of by wrapping its writer — stderr also carries the progress
// bar's own cursor control (see progressbar.go), which a wrap could not tell
// apart from an injected sequence.
func diffProgressLine(event sem.AnalyzeProgressEvent) string {
	line := fmt.Sprintf("graph diff progress phase=%s files=%d/%d elapsed=%s",
		event.Phase,
		event.FilesDone,
		event.FilesTotal,
		event.Elapsed.Round(time.Millisecond),
	)
	if event.Path != "" {
		line += " file=" + termsafe.Line(event.Path)
	}
	return line
}

func printResult(out io.Writer, result sem.Result, asJSON bool, producerVersion string) error {
	// ProducerVersion is set here, the one place a Result is rendered, rather
	// than at each of the two callers — it needs the CLI's build-time version
	// (Options.Version), which the sem package that builds the rest of the
	// Result has no access to.
	result.ProducerVersion = producerVersion
	if asJSON {
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(encoded))
		return nil
	}
	sem.WriteText(out, result)
	return nil
}
