package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

// store resolves the read-only compatibility surface without building it.
func runStore(ctx context.Context, opts Options, args []string) error {
	set := flag.NewFlagSet("store", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var repo, profile, cacheDir string
	var ignores, includes storeStringList
	set.StringVar(&repo, "repo", "", "repository path")
	set.StringVar(&profile, "profile", "full", "index profile")
	set.StringVar(&cacheDir, "cache-dir", "", "cache directory")
	set.Var(&ignores, "ignore-file", "extra ignore file")
	set.Var(&includes, "include-file", "extra include file")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return fmt.Errorf("store takes no positional arguments")
	}
	resolved, err := resolveRepo(ctx, opts.Env, repo)
	if err != nil {
		return err
	}
	parsed, err := parseProfile(profile)
	if err != nil {
		return err
	}
	result, err := sem.DescribeSearchStore(ctx, resolved, opts.Version, resolveCacheDir(cacheDir, opts.Env.PluginDataDir), sem.ProviderSnapshotOptions{NoNetwork: true, Profile: parsed, IgnoreFiles: ignores, IncludeFiles: includes})
	if errors.Is(err, sem.ErrSearchStoreMissing) {
		return json.NewEncoder(termsafe.NewJSONWriter(opts.Stdout)).Encode(map[string]any{"contract_version": 1, "store_unavailable": true, "detail": err.Error()})
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(termsafe.NewJSONWriter(opts.Stdout)).Encode(result)
}

type storeStringList []string

func (s *storeStringList) String() string         { return fmt.Sprint([]string(*s)) }
func (s *storeStringList) Set(value string) error { *s = append(*s, value); return nil }
