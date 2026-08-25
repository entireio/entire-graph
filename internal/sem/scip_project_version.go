package sem

import (
	"encoding/json"
	"strings"
)

// ManifestReader reads one repo-root manifest by name and reports whether it
// exists. The caller supplies it so the version is read from the same tree the
// snapshot describes: a committed-tree snapshot must not pick up a version a
// dirty working tree happens to carry.
type ManifestReader func(name string) (string, bool)

// ScipProjectVersionUnknown is the SCIP package version used when no manifest
// declares one.
//
// It is "0" rather than an empty component because the field is the version of
// the package, and a value that parses as one keeps the symbol honest: "0" says
// "unversioned" without claiming a release. Note this is the common case for Go
// repositories -- go.mod carries a module path, not a version, since Go takes
// versions from Git tags -- and for tsconfig.json and setup.cfg.
const ScipProjectVersionUnknown = "0"

// scipProjectVersionManifests are the root manifests that can declare the
// project's own version, in precedence order. go.mod, tsconfig.json and
// setup.cfg are deliberately absent: none of them carries the version of the
// project they describe.
var scipProjectVersionManifests = []struct {
	name  string
	parse func(string) string
}{
	{"package.json", parsePackageJSONVersion},
	{"Cargo.toml", parseCargoPackageVersion},
	{"pyproject.toml", parsePyProjectVersion},
}

// ScipProjectVersion returns the project's declared version, or "" when no
// manifest declares one. It never fails: an unreadable or malformed manifest is
// treated as "not declared" rather than as an error, because a version string
// must not be able to fail an export.
//
// The version is deliberately taken from the ROOT manifest only. SCIP scopes a
// package per symbol, so a monorepo whose sub-packages carry different versions
// should strictly emit different packages -- but the symbol's package NAME is
// the repository key here, so per-directory versions would produce the
// incoherent "one package, many versions". Per-package identity is a larger
// contract decision than a version lookup and is left to that decision.
func ScipProjectVersion(read ManifestReader) string {
	if read == nil {
		return ""
	}
	for _, manifest := range scipProjectVersionManifests {
		content, ok := read(manifest.name)
		if !ok {
			continue
		}
		if version := strings.TrimSpace(manifest.parse(content)); version != "" {
			return version
		}
	}
	return ""
}

func parsePackageJSONVersion(content string) string {
	var data struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(content), &data); err != nil {
		return ""
	}
	return strings.TrimSpace(data.Version)
}

func parseCargoPackageVersion(content string) string {
	return tomlTableString(content, "package", "version")
}

func parsePyProjectVersion(content string) string {
	// PEP 621 first, then Poetry's pre-621 table, which is still widespread.
	if version := tomlTableString(content, "project", "version"); version != "" {
		return version
	}
	return tomlTableString(content, "tool.poetry", "version")
}

// tomlTableString reads a quoted string value from a top-level TOML table
// without a TOML dependency, which this repository does not carry.
//
// It is deliberately conservative: it recognizes `key = "value"` inside the
// named table and nothing else. A dynamic version, an inline table, a
// multi-line string, or any shape it does not understand yields "", which the
// caller reads as "not declared" and falls back from. Guessing would be worse
// than falling back, because the value becomes part of every symbol's identity.
func tomlTableString(content, table, key string) string {
	inTable := false
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			// A table header ends the previous table. Array-of-tables headers
			// ("[[bin]]") are tables too and must end it just the same.
			name := strings.TrimSpace(strings.Trim(line, "[]"))
			inTable = name == table
			continue
		}
		if !inTable {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(name) != key {
			continue
		}
		value = strings.TrimSpace(value)
		// Strip a trailing comment only when it sits outside the quoted value.
		if end := strings.LastIndex(value, `"`); end > 0 && strings.HasPrefix(value, `"`) {
			return value[1:end]
		}
		return ""
	}
	return ""
}
