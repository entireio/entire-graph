package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/entireio/entire-graph/internal/sem"
)

// `graph context` is the delegation surface for downstream consumers (Entire
// Brain's brief): task text in, the store's ranked task-relevant symbols,
// their relation fan-out, and test suggestions out, as JSON. It reads ONLY the
// committed tree's sqlite store sidecar — it never parses and never builds, so
// its latency is bounded by indexed reads, and a missing store is a clean
// machine-readable degradation signal rather than a multi-second surprise.
type contextFlags struct {
	IgnoreFiles    storeStringList
	IncludeFiles   storeStringList
	Repo           string
	Task           string
	Limit          int
	TestsLimit     int
	RelationsLimit int
	Profile        string
	CacheDir       string
}

func parseContextFlags(args []string) (contextFlags, error) {
	flags := contextFlags{}
	set := flag.NewFlagSet("context", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.Var(&flags.IgnoreFiles, "ignore-file", "extra ignore file")
	set.Var(&flags.IncludeFiles, "include-file", "extra include file")
	set.StringVar(&flags.Repo, "repo", "", "repository path (default: current repo)")
	set.StringVar(&flags.Task, "task", "", "task text to find context for (required)")
	set.IntVar(&flags.Limit, "limit", 12, "maximum symbols returned")
	set.IntVar(&flags.TestsLimit, "tests-limit", 6, "maximum test suggestions returned")
	set.IntVar(&flags.RelationsLimit, "relations-limit", 200, "maximum relations returned (highest confidence kept; relations_truncated signals a cut)")
	set.StringVar(&flags.Profile, "profile", "full", "indexing profile of the store to read")
	set.StringVar(&flags.CacheDir, "cache-dir", "", "cache directory holding the store (default: plugin data dir, else user cache dir)")
	if err := set.Parse(args); err != nil {
		return contextFlags{}, err
	}
	if flags.Task == "" {
		return contextFlags{}, errors.New("context requires --task")
	}
	if set.NArg() > 0 {
		return contextFlags{}, fmt.Errorf("context takes no positional arguments, got %q", set.Args())
	}
	return flags, nil
}

type contextResponse struct {
	SchemaVersion string                     `json:"schema_version"`
	Commit        string                     `json:"commit"`
	Tree          string                     `json:"tree"`
	Symbols       []sem.StoreContextSymbol   `json:"symbols"`
	Relations     []sem.StoreContextRelation `json:"relations"`
	// RelationsTruncated reports the fan-out hit --relations-limit: the kept
	// rows are the highest-confidence ones, and an absent relation is not
	// evidence of no relation.
	RelationsTruncated bool                     `json:"relations_truncated,omitempty"`
	Tests              []sem.StoreContextSymbol `json:"tests"`
	LatencyMS          int64                    `json:"latency_ms"`
	// StoreUnavailable is the machine-readable degradation signal: true means
	// the committed tree has no (or a stale) store sidecar and every list is
	// empty. Consumers degrade exactly as they would with the plugin absent.
	StoreUnavailable bool   `json:"store_unavailable,omitempty"`
	Detail           string `json:"detail,omitempty"`
}

func runContext(ctx context.Context, opts Options, args []string) error {
	flags, err := parseContextFlags(args)
	if err != nil {
		return err
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	profile, err := parseProfile(flags.Profile)
	if err != nil {
		return err
	}
	cacheDir := resolveCacheDir(flags.CacheDir, opts.Env.PluginDataDir)
	started := time.Now()
	result, err := sem.LoadSearchStoreContext(ctx, repo, opts.Version, cacheDir, flags.Task, sem.ProviderSnapshotOptions{
		NoNetwork:    true,
		IgnoreFiles:  flags.IgnoreFiles,
		IncludeFiles: flags.IncludeFiles,
		Profile:      profile,
	}, flags.Limit, flags.TestsLimit, flags.RelationsLimit)
	response := contextResponse{
		SchemaVersion:      sem.SchemaVersion,
		Commit:             result.Commit,
		Tree:               result.Tree,
		Symbols:            result.Symbols,
		Relations:          result.Relations,
		RelationsTruncated: result.RelationsTruncated,
		Tests:              result.Tests,
		LatencyMS:          time.Since(started).Milliseconds(),
	}
	if err != nil {
		if !errors.Is(err, sem.ErrSearchStoreMissing) {
			return err
		}
		response.StoreUnavailable = true
		response.Detail = err.Error()
	}
	encoder := json.NewEncoder(opts.Stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(response)
}
