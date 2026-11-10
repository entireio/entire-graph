package main

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var defaultGenerateArgs = []string{"--abi", strconv.Itoa(maxABI)}

// cliVersionPattern takes the whole version npx printed, prerelease and build
// metadata included. Matching only the numeric core recorded 0.25.0 for the
// 0.25.0-rc.1 that actually generated the vendored bytes, so the pin that is
// supposed to reproduce them would have run a different build of the
// generator, and nothing would have said so.
var cliVersionPattern = regexp.MustCompile(`\b\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]*[0-9A-Za-z])?(?:\+[0-9A-Za-z.-]*[0-9A-Za-z])?`)

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
		return fmt.Errorf("tree-sitter-cli@%s generate: %w; if this release cannot generate ABI %d, pass -tree-sitter-cli <older version>", version, err, maxABI)
	}
	return nil
}

// patchTargets lists the paths a unified diff names, read from its own `---`
// and `+++` headers and stripped of the usual one-level prefix. git apply is
// what applies the patch; this only needs to know which files it touches.
func patchTargets(patchFile string) ([]string, error) {
	data, err := os.ReadFile(patchFile)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		var prefix string
		switch {
		case strings.HasPrefix(line, "--- "):
			prefix = "a/"
		case strings.HasPrefix(line, "+++ "):
			prefix = "b/"
		default:
			continue
		}
		target := line[4:]
		// diff -u writes a timestamp after a tab; git apply ignores it.
		if tab := strings.IndexByte(target, '\t'); tab >= 0 {
			target = target[:tab]
		}
		target = strings.TrimSpace(target)
		// A creation names /dev/null on the old side, a deletion on the new
		// side. Both sides are read so either way the real path is seen.
		if target == "" || target == "/dev/null" {
			continue
		}
		target = path.Clean(strings.TrimPrefix(target, prefix))
		if !slices.Contains(out, target) {
			out = append(out, target)
		}
	}
	return out, nil
}

// vendoredDirectly reports whether vendor copies the file at this path, which
// is relative to the grammar directory, straight into the grammar folder.
// Everything else a patch touches -- grammar.js above all -- reaches the
// vendored grammar only through a regenerated parser.
func vendoredDirectly(rel string) bool {
	dir, name := path.Split(rel)
	upper := strings.ToUpper(name)
	if strings.HasPrefix(upper, "LICENSE") || strings.HasPrefix(upper, "COPYING") {
		return dir == ""
	}
	switch dir {
	case "src/":
		return name == "parser.c" || name == "scanner.c" || strings.HasSuffix(name, ".h")
	case "src/tree_sitter/":
		return strings.HasSuffix(name, ".h")
	}
	return false
}

// patchPathsNeedingGeneration lists the paths a patch touches whose effect is
// lost unless the parser is regenerated. A patch that only edits vendored
// files is a complete change on its own; one that edits grammar.js is not.
func patchPathsNeedingGeneration(patchFile string) ([]string, error) {
	targets, err := patchTargets(patchFile)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, t := range targets {
		if !vendoredDirectly(t) {
			out = append(out, t)
		}
	}
	return out, nil
}

// recordedLocalBuild is the local_build block a finished generation writes back
// to the manifest: the exact version that ran, and nothing else new.
//
// GenerateArgs deliberately keeps only what the entry already asked for. The
// args actually passed to generate come from generateArgs, which falls back to
// --abi maxABI; recording that fallback would turn a derived default into an
// explicit pin, and the next runtime bump would edit the one constant while
// every grammar went on generating the old ABI -- which vendor keeps accepting
// for as long as it is inside the range, so nothing would fail.
func recordedLocalBuild(build *LocalBuild, version string) *LocalBuild {
	next := LocalBuild{}
	if build != nil {
		next = *build
	}
	next.TreeSitterCLI = version
	return &next
}
