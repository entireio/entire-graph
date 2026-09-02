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
	if version := tomlTableString(content, "package", "version"); version != "" {
		return version
	}
	// Workspace inheritance. A root Cargo.toml commonly declares the version
	// once in [workspace.package] and has each member write `version.workspace
	// = true` (or `version = { workspace = true }`). Reading only a literal
	// [package] version exported every such crate as "0", so distinct releases
	// shared one SCIP package identity -- the identity confusion this whole
	// field exists to avoid.
	if !tomlTableInheritsFromWorkspace(content, "package", "version") {
		return ""
	}
	return tomlTableString(content, "workspace.package", "version")
}

// tomlTableInheritsFromWorkspace reports whether a table defers a key to the
// workspace, in either spelling TOML allows: a dotted key (`version.workspace =
// true`) or an inline table (`version = { workspace = true }`).
func tomlTableInheritsFromWorkspace(content, table, key string) bool {
	if value := tomlTableRawValue(content, table, key+".workspace"); strings.EqualFold(value, "true") {
		return true
	}
	value := tomlTableRawValue(content, table, key)
	if !strings.HasPrefix(value, "{") {
		return false
	}
	inner := strings.TrimSpace(strings.Trim(value, "{}"))
	for _, field := range strings.Split(inner, ",") {
		name, setting, found := strings.Cut(field, "=")
		if found && strings.TrimSpace(name) == "workspace" && strings.EqualFold(strings.TrimSpace(setting), "true") {
			return true
		}
	}
	return false
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
// tomlTableRawValue returns a key's value inside a table, unquoted and
// untrimmed of its own syntax, or "" when the table or key is absent.
func tomlTableRawValue(content, table, key string) string {
	inTable := false
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inTable = strings.TrimSpace(strings.Trim(line, "[]")) == table
			continue
		}
		if !inTable {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if found && strings.TrimSpace(name) == key {
			// A trailing comment is not part of the value. Without this,
			// `version.workspace = true # inherit` compares "true # inherit"
			// against "true", reports no inheritance, and collapses the crate
			// back to the shared "0" identity this field exists to prevent.
			// stripTOMLComment is quote-aware, so `"1.0#rc1"` keeps its hash.
			return strings.TrimSpace(stripTOMLComment(value))
		}
	}
	return ""
}

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
		// TOML has two string forms and both are ordinary in a manifest:
		// basic ("1.2.3") and literal ('1.2.3'). Handling only the first
		// exported Cargo and Python projects that use the second as version
		// "0", collapsing distinct releases into one package identity.
		for _, quote := range []string{`"`, "'"} {
			if !strings.HasPrefix(value, quote) {
				continue
			}
			// Closing quote, not a later one in a trailing comment.
			if end := strings.Index(value[1:], quote); end >= 0 {
				return value[1 : 1+end]
			}
		}
		return ""
	}
	return ""
}
