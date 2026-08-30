package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/entireio/entire-graph/internal/sem"
)

type indexFlags struct {
	Repo         string
	Profile      string
	CacheDir     string
	Format       string
	IgnoreFiles  []string
	IncludeFiles []string
	// Report is where to write the human-readable GRAPH_REPORT.md rendering of the
	// snapshot this run built or loaded. Empty writes no report. It rides on index
	// rather than on a command of its own because index is already the command that
	// materializes a whole committed-tree snapshot for a human to act on.
	Report string
	// Force rebuilds the committed-tree snapshot from scratch and overwrites the
	// cache entry even when a valid one already exists for this tree.
	Force bool
}

type indexResponse struct {
	FormatVersion   int                    `json:"format_version"`
	Provider        string                 `json:"provider"`
	ProviderVersion string                 `json:"provider_version"`
	RepoRoot        string                 `json:"repo_root"`
	Commit          string                 `json:"commit"`
	Tree            string                 `json:"tree"`
	Profile         string                 `json:"profile"`
	IndexCacheHit   bool                   `json:"index_cache_hit"`
	IndexLatencyMS  int64                  `json:"index_latency_ms"`
	Counts          sem.ProviderStats      `json:"counts"`
	Warnings        []sem.ProviderWarning  `json:"warnings"`
	PartialFailures []sem.PartialFailure   `json:"partial_failures"`
	Completeness    sem.CompletenessReport `json:"completeness"`
}

func runIndex(ctx context.Context, opts Options, args []string) error {
	flags, rest, err := parseIndexFlags(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return unexpectedArgumentsError("index", opts.Version, rest)
	}
	// Format resolution: an explicit --format wins; otherwise pick by audience —
	// a human at a terminal gets the readable summary, a pipe/CI gets the
	// schema-versioned JSON (the machine contract, unchanged).
	var outputText bool
	switch flags.Format {
	case "json":
		outputText = false
	case "text":
		outputText = true
	case "", "auto":
		outputText = isTerminal(opts.Stdout)
	default:
		return fmt.Errorf("index --format must be json, text, or auto, got %q", flags.Format)
	}
	profile, err := parseProfile(flags.Profile)
	if err != nil {
		return err
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	cacheDir := resolveCacheDir(flags.CacheDir, opts.Env.PluginDataDir)
	if cacheDir == "" {
		return errors.New("index requires --cache-dir or ENTIRE_PLUGIN_DATA_DIR: this platform has no resolvable per-user cache directory")
	}
	snapOptions := sem.ProviderSnapshotOptions{
		NoNetwork:    true,
		Profile:      profile,
		IgnoreFiles:  flags.IgnoreFiles,
		IncludeFiles: flags.IncludeFiles,
		ForceRebuild: flags.Force,
	}
	// Draw a live progress bar on stderr when it's a terminal. It only fires on a
	// cache miss (the build emits the events); a cache hit returns instantly with
	// nothing drawn. stdout is left untouched so piped JSON stays clean.
	var bar *progressBar
	if isTerminal(opts.Stderr) {
		bar = newProgressBar(opts.Stderr, "indexing")
		snapOptions.Progress = func(e sem.ProgressEvent) { bar.update(e) }
	}
	started := time.Now()
	snapshot, cacheHit, err := sem.PreindexProviderSnapshot(ctx, repo, opts.Version, snapOptions, cacheDir)
	if bar != nil {
		bar.clear()
	}
	if err != nil {
		return err
	}
	warnings := snapshot.Header.Warnings
	if warnings == nil {
		warnings = []sem.ProviderWarning{}
	}
	partialFailures := snapshot.Header.PartialFailures
	if partialFailures == nil {
		partialFailures = []sem.PartialFailure{}
	}
	response := indexResponse{
		FormatVersion:   1,
		Provider:        sem.ProviderName,
		ProviderVersion: opts.Version,
		RepoRoot:        snapshot.Header.RepoRoot,
		Commit:          snapshot.Header.Commit,
		Tree:            snapshot.Header.Tree,
		Profile:         snapshot.Header.Profile,
		IndexCacheHit:   cacheHit,
		IndexLatencyMS:  time.Since(started).Milliseconds(),
		Counts:          snapshot.Header.Stats,
		Warnings:        warnings,
		PartialFailures: partialFailures,
		Completeness:    snapshot.Header.Completeness,
	}
	if flags.Report != "" {
		// writeOutputFile, not os.WriteFile: --report names a path anywhere on the
		// machine, but the in-repo spelling this verb prints puts the destination
		// under the control of the scanned repository, which may have committed a
		// symlink there. ctx is passed because the boundary is the checkout's git
		// top level, which --repo does not have to name. See outputpath.go.
		if err := writeOutputFile(ctx, repo, flags.Report, []byte(renderGraphReport(snapshot)), 0o644, false); err != nil {
			return fmt.Errorf("write graph report %q: %w", flags.Report, err)
		}
	}
	if outputText {
		return writeIndexText(opts.Stdout, response, flags.Report)
	}
	encoder := json.NewEncoder(opts.Stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(response)
}

// writeIndexText renders the human-facing summary shown when index runs at a
// terminal. It reads the same indexResponse the JSON contract carries, so the
// two never diverge.
func writeIndexText(w io.Writer, r indexResponse, reportPath string) error {
	commit := r.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	fmt.Fprintf(w, "Indexed %s", r.RepoRoot)
	if commit != "" {
		fmt.Fprintf(w, " @ %s", commit)
	}
	fmt.Fprintf(w, " (%s profile)\n", r.Profile)

	if r.IndexCacheHit {
		fmt.Fprintf(w, "  cache hit — reused in %s\n", time.Duration(r.IndexLatencyMS)*time.Millisecond)
	} else {
		fmt.Fprintf(w, "  built in %s\n", time.Duration(r.IndexLatencyMS)*time.Millisecond)
	}

	c := r.Counts
	fmt.Fprintf(w, "  %s of %s files parsed · %s symbols · %s relations\n",
		humanInt(int64(c.ParsedFiles)), humanInt(int64(c.Files)),
		humanInt(int64(c.Symbols)), humanInt(int64(c.Relations)))

	level := c.CompletenessLevel
	if level == "" {
		level = "unknown"
	}
	fmt.Fprintf(w, "  completeness: %s\n", level)

	if n := len(r.Warnings); n > 0 {
		fmt.Fprintf(w, "  ⚠️  %d warning(s) — run with --format json for detail\n", n)
	}
	if reportPath != "" {
		fmt.Fprintf(w, "  report written to %s\n", reportPath)
	}
	return nil
}

func parseIndexFlags(args []string) (indexFlags, []string, error) {
	flags := indexFlags{Profile: "full"}
	var rest []string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--repo":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.Repo, index = value, next
		case "--profile":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.Profile, index = value, next
		case "--cache-dir":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.CacheDir, index = value, next
		case "--report":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.Report, index = value, next
		case "--ignore-file":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.IgnoreFiles, index = append(flags.IgnoreFiles, value), next
		case "--include-file":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.IncludeFiles, index = append(flags.IncludeFiles, value), next
		case "--format":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.Format, index = value, next
		case "--force":
			flags.Force = true
		case "--head", "--no-network":
			// Indexing is always local-only and always targets committed HEAD.
		case "--worktree":
			return flags, nil, errors.New("index is HEAD-only; --worktree is not supported")
		default:
			rest = append(rest, args[index])
		}
	}
	return flags, rest, nil
}
