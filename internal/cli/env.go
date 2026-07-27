package cli

import "os"

const (
	envCLIVersion    = "ENTIRE_CLI_VERSION"
	envRepoRoot      = "ENTIRE_REPO_ROOT"
	envPluginDataDir = "ENTIRE_PLUGIN_DATA_DIR"
	// envReferenceBlocks turns the search reference blocks back on for a whole session, so an
	// interactive user does not have to add flags to every call. Its value is a comma-separated list
	// of `container-map`, `signature-types`, `type-card`, or `all`. See searchReferenceBlocks.
	envReferenceBlocks = "ENTIRE_GRAPH_REFERENCE_BLOCKS"
)

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
