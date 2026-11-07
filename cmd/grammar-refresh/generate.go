package main

import (
	"fmt"
	"regexp"
)

var defaultGenerateArgs = []string{"--abi", "14"}

var cliVersionPattern = regexp.MustCompile(`\b\d+\.\d+\.\d+\b`)

// wantsGeneration reports whether this run regenerates the parser.
func wantsGeneration(existing Grammar, o options) bool {
	return o.Generate || o.TreeSitterCLI != "" || (existing.LocalBuild != nil && existing.LocalBuild.TreeSitterCLI != "")
}

// cliVersion picks the tree-sitter-cli to run: an explicit override, else
// the recorded version when regenerating the recorded revision (so the
// output is reproducible), else latest.
func cliVersion(existing Grammar, revision string, o options) string {
	if o.TreeSitterCLI != "" {
		return o.TreeSitterCLI
	}
	if existing.LocalBuild != nil && existing.LocalBuild.TreeSitterCLI != "" && existing.Revision == revision {
		return existing.LocalBuild.TreeSitterCLI
	}
	return "latest"
}

func generateArgs(build *LocalBuild) []string {
	if build != nil && len(build.GenerateArgs) > 0 {
		return build.GenerateArgs
	}
	return defaultGenerateArgs
}

// resolveCLI turns a version spec such as "latest" into the exact version
// npx runs, so generation and the manifest use the same one.
func resolveCLI(dir, spec string) (string, error) {
	out, err := npx(dir, "tree-sitter-cli@"+spec, "--version")
	if err != nil {
		return "", fmt.Errorf("tree-sitter-cli@%s --version: %w", spec, err)
	}
	version := cliVersionPattern.FindString(out)
	if version == "" {
		return "", fmt.Errorf("tree-sitter-cli@%s --version printed no version: %q", spec, out)
	}
	return version, nil
}

func generate(grammarRoot, version string, args []string) error {
	if _, err := npx(grammarRoot, append([]string{"tree-sitter-cli@" + version, "generate"}, args...)...); err != nil {
		return fmt.Errorf("tree-sitter-cli@%s generate: %w", version, err)
	}
	return nil
}
