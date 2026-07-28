package cli

import (
	"os"
	"path/filepath"
)

const (
	envCLIVersion    = "ENTIRE_CLI_VERSION"
	envRepoRoot      = "ENTIRE_REPO_ROOT"
	envPluginDataDir = "ENTIRE_PLUGIN_DATA_DIR"
	// envReferenceBlocks turns the search reference blocks back on for a whole session, so an
	// interactive user does not have to add flags to every call. Its value is a comma-separated list
	// of `container-map`, `signature-types`, `type-card`, or `all`. See searchReferenceBlocks.
	envReferenceBlocks = "ENTIRE_GRAPH_REFERENCE_BLOCKS"
)

// cacheDirName is this provider's directory inside the platform's per-user cache
// root. It is namespaced by product, not by repository: the cache is keyed on the
// git tree hash, so one directory holds every repository's entries without them
// colliding.
const cacheDirName = "entire-graph"

// resolveCacheDir decides where the tree-hash record cache lives. An explicit
// --cache-dir wins, then the data directory Entire hands a plugin
// (ENTIRE_PLUGIN_DATA_DIR), and otherwise the platform's per-user cache
// directory — `os.UserCacheDir` is the convention: XDG_CACHE_HOME (or
// ~/.cache) on Unix, ~/Library/Caches on macOS, %LocalAppData% on Windows.
//
// That last step is the point. Committed-tree caching was implemented but
// inert for anyone who had not exported ENTIRE_PLUGIN_DATA_DIR by hand, so the
// documented "same tree hits" path never hit and every `--head` query re-parsed
// the repository from scratch. Only WHERE the cache lands is decided here.
// Nothing else moves: the working tree is still never cached, the git tree hash
// is still the only thing that decides a hit, and --no-cache still disables the
// cache outright — so results are identical with or without a cache directory.
// A platform with no resolvable cache root falls back to the previous
// behaviour of not caching at all.
func resolveCacheDir(flagValue, pluginDataDir string) string {
	if flagValue != "" {
		return flagValue
	}
	if pluginDataDir != "" {
		return pluginDataDir
	}
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		return ""
	}
	return filepath.Join(base, cacheDirName)
}

// EntireEnv captures environment variables supplied by Entire when it dispatches
// an external plugin command.
type EntireEnv struct {
	CLIVersion    string
	RepoRoot      string
	PluginDataDir string
	// ReferenceBlocks is the session-wide default for the off-by-default search reference blocks.
	ReferenceBlocks string
}

func EnvFromOS() EntireEnv {
	return EntireEnv{
		CLIVersion:      os.Getenv(envCLIVersion),
		RepoRoot:        os.Getenv(envRepoRoot),
		PluginDataDir:   os.Getenv(envPluginDataDir),
		ReferenceBlocks: os.Getenv(envReferenceBlocks),
	}
}

func valueOrUnset(value string) string {
	if value == "" {
		return "<unset>"
	}
	return value
}
