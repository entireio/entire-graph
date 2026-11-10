package sem

import (
	"encoding/json"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
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

// ScipProjectVersionMaxLen bounds the declared version's LENGTH, which is a
// separate concern from bounding the manifest read.
//
// The provider's reader caps the file, not the value pulled out of it, so a
// manifest well under that cap can still declare a version of hundreds of
// kilobytes -- and the version is copied into every emitted symbol, so it
// amplifies. Measured: a 200 KB version across 200 symbols produced an 80 MB
// index, roughly 400x. A real version is a handful of bytes; anything past this
// is not one, and is treated as undeclared rather than truncated, because a
// truncated version would silently name a different package.
const ScipProjectVersionMaxLen = 256

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
		version := strings.TrimSpace(manifest.parse(content))
		if version == "" || len(version) > ScipProjectVersionMaxLen {
			continue
		}
		return version
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
	manifest, ok := parseTOMLDocument(content)
	if !ok {
		return ""
	}
	rootPackage, ok := tomlTable(manifest, "package")
	if !ok {
		// A virtual workspace has no root package. This exporter emits one SCIP
		// package for the repository and cannot safely choose among member
		// versions until package identity is member-aware.
		return ""
	}
	if version, ok := rootPackage["version"].(string); ok {
		return version
	}
	inherited, ok := rootPackage["version"].(map[string]any)
	if !ok || len(inherited) != 1 || inherited["workspace"] != true {
		return ""
	}
	workspacePackage, ok := tomlTable(manifest, "workspace", "package")
	if !ok {
		return ""
	}
	version, _ := workspacePackage["version"].(string)
	return version
}

func parsePyProjectVersion(content string) string {
	manifest, ok := parseTOMLDocument(content)
	if !ok {
		return ""
	}
	// PEP 621 first, then Poetry's pre-PEP 621 table, which is still
	// widespread. A non-string declaration is dynamic or unsupported.
	for _, path := range [][]string{{"project"}, {"tool", "poetry"}} {
		table, ok := tomlTable(manifest, path...)
		if !ok {
			continue
		}
		if version, ok := table["version"].(string); ok {
			return version
		}
	}
	return ""
}

func parseTOMLDocument(content string) (map[string]any, bool) {
	var document map[string]any
	if err := toml.Unmarshal([]byte(content), &document); err != nil {
		return nil, false
	}
	return document, true
}

func tomlTable(document map[string]any, path ...string) (map[string]any, bool) {
	current := document
	for _, part := range path {
		next, ok := current[part].(map[string]any)
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}
