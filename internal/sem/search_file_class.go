package sem

import (
	"path/filepath"
	"strings"
)

// File-class prior
// ================
//
// `search` is a CODE search: the caller's next action is an edit to a source file.
// Some file classes are systematically over-scored by a body-text match yet are
// almost never the edit site:
//
//   - prose documentation restates an issue in the issue's own vocabulary, so it
//     wins BM25 against the code that expresses the same idea in identifiers;
//   - vendored / third-party trees are an upstream copy — editing them is wrong;
//   - generated artifacts are overwritten by the next codegen run;
//   - examples/samples are real code, but secondary to the library they exercise.
//
// The correction is a MULTIPLICATIVE prior on the positive part of a candidate's
// score, not a filter: a non-source hit stays reachable (and still ranks first
// when nothing else matches at all), it just has to be clearly more relevant than
// the best source hit to outrank it.
//
// Defaults are principled, not tuned:
//
//	nonSource = 0.5   "must be twice as relevant as the best source hit to win"
//	secondary = 0.75  "a mild tilt away from example code toward the library"
//
// The prior is switched OFF for a class whenever the query itself asks for that
// class (`searchQuerySupplied`), which is how a genuine documentation task keeps
// full-strength documentation ranking.
const (
	searchNonSourceClassPrior = 0.5
	searchSecondaryClassPrior = 0.75
)

// searchFileClass is the content class a path belongs to, from the point of view
// of "would a coding agent edit this file to fix a defect?".
type searchFileClass string

const (
	searchFileClassSource    searchFileClass = "source"
	searchFileClassDoc       searchFileClass = "doc"
	searchFileClassVendored  searchFileClass = "vendored"
	searchFileClassGenerated searchFileClass = "generated"
	searchFileClassExample   searchFileClass = "example"
)

// Path segments that mark a documentation tree. Matched as whole path SEGMENTS
// (so `versioned_docs/` and `website/` are caught while `my-docs-parser.go` is
// not), which is strictly more precise than substring matching.
var searchDocDirSegments = map[string]bool{
	"doc": true, "docs": true, "documentation": true,
	"man": true, "manual": true, "manuals": true,
	"website": true, "websites": true, "wiki": true, "handbook": true,
	"guide": true, "guides": true, "tutorial": true, "tutorials": true,
	"versioned_docs": true, "versioned-docs": true, "versioned_sidebars": true,
	"changelog": true, "changelogs": true, "javadoc": true, "godoc": true,
}

// Prose / documentation file types. These describe a feature in the same words
// the issue uses, so on a body match they outrank the code that implements it.
// Note: .yml/.yaml are deliberately absent — they are usually config/CI.
var searchDocExtensions = []string{
	".md", ".markdown", ".mdx", ".mdoc", ".rst", ".adoc", ".asciidoc",
	".txt", ".text", ".org", ".pod", ".rdoc", ".textile", ".creole", ".wiki",
}

// Basenames that are repository prose regardless of where they live.
var searchDocBasePrefixes = []string{
	"readme", "changelog", "changes", "history", "news", "contributing",
	"authors", "maintainers", "code_of_conduct", "code-of-conduct",
	"license", "licence", "copying", "notice", "governance", "upgrading",
}

// Path segments that mark a vendored / third-party tree.
var searchVendoredDirSegments = map[string]bool{
	"vendor": true, "vendors": true, "_vendor": true,
	"third_party": true, "third-party": true, "thirdparty": true,
	"dependencies": true, "node_modules": true, "bower_components": true,
	"jspm_packages": true, "site-packages": true, "dist-packages": true,
	"godeps": true, ".venv": true, "venv": true, "virtualenv": true,
	"eggs": true, ".eggs": true, "external": true, "externals": true,
}

// Path segments that mark example / sample code.
var searchExampleDirSegments = map[string]bool{
	"example": true, "examples": true, "sample": true, "samples": true,
	"demo": true, "demos": true, "cookbook": true, "cookbooks": true,
	"recipes": true, "playground": true,
}

// Basenames that are machine-written lock/manifest artifacts.
var searchGeneratedBases = map[string]bool{
	"package-lock.json": true, "npm-shrinkwrap.json": true, "yarn.lock": true,
	"pnpm-lock.yaml": true, "cargo.lock": true, "composer.lock": true,
	"gemfile.lock": true, "poetry.lock": true, "pdm.lock": true,
	"go.sum": true, "flake.lock": true, "packages.lock.json": true,
}

// Query terms that switch the prior off for each class: if the caller asked for
// documentation, documentation ranks at full strength.
var (
	searchDocIntentTerms = []string{
		"doc", "docs", "documentation", "readme", "readmes", "guide", "guides",
		"tutorial", "tutorials", "manual", "manpage", "changelog", "website",
		"handbook", "wiki", "prose", "comment", "comments", "docstring", "docstrings",
	}
	searchVendoredIntentTerms = []string{
		"vendor", "vendored", "vendoring", "dependency", "dependencies",
		"third", "party", "upstream", "node_modules", "lockfile", "lock",
	}
	searchGeneratedIntentTerms = []string{
		"generated", "generator", "generators", "codegen", "codegens",
		"build", "builds", "dist", "bundle", "bundles",
		"amalgamation", "amalgamated", "single", "onefile", "lockfile", "lock",
	}
	searchExampleIntentTerms = []string{
		"example", "examples", "sample", "samples", "demo", "demos",
		"snippet", "snippets", "cookbook", "recipe", "recipes", "playground",
	}
)

// classifySearchFile maps a repository-relative path to its content class.
// Precedence runs from "definitely not editable by this task" downwards so a
// vendored markdown file classifies as vendored rather than doc (the prior is
// the same for both; the label is what changes).
func classifySearchFile(filePath string) searchFileClass {
	lower := strings.ToLower(filepath.ToSlash(filePath))
	segments := searchPathSegments(lower)
	base := filepath.Base(lower)
	dirs := segments
	if len(dirs) > 0 {
		dirs = dirs[:len(dirs)-1]
	}
	for _, segment := range dirs {
		if searchVendoredDirSegments[segment] {
			return searchFileClassVendored
		}
	}
	if searchGeneratedBases[base] || searchGeneratedArtifactPath("/"+lower) {
		return searchFileClassGenerated
	}
	if searchDocumentationClassPath(lower, base, dirs) {
		return searchFileClassDoc
	}
	for _, segment := range dirs {
		if searchExampleDirSegments[segment] {
			return searchFileClassExample
		}
	}
	return searchFileClassSource
}

// searchDocumentationClassPath reports whether a path is prose documentation,
// either by file type (anywhere in the tree) or by living in a documentation
// tree. `lower` is the slash-normalized lowercase path, `base` its basename and
// `dirs` its directory segments.
func searchDocumentationClassPath(lower, base string, dirs []string) bool {
	for _, segment := range dirs {
		if searchDocDirSegments[segment] {
			return true
		}
	}
	for _, prefix := range searchDocBasePrefixes {
		if strings.HasPrefix(base, prefix) {
			return true
		}
	}
	for _, ext := range searchDocExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	// man pages: foo.1 .. foo.9, plus pre-rendered roff variants.
	if strings.HasSuffix(lower, ".prebuilt") || strings.HasSuffix(lower, ".roff") || strings.HasSuffix(lower, ".man") {
		return true
	}
	if n := len(base); n >= 2 && base[n-2] == '.' && base[n-1] >= '1' && base[n-1] <= '9' {
		return true
	}
	return false
}

func searchPathSegments(lower string) []string {
	parts := strings.Split(strings.Trim(lower, "/"), "/")
	out := parts[:0]
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		out = append(out, part)
	}
	return out
}

// searchFileClassPrior returns the multiplicative relevance prior for a path,
// in (0, 1]. 1 means "no correction" (source files, and any class the query
// explicitly asked for).
func searchFileClassPrior(q searchQuery, filePath string) float64 {
	switch classifySearchFile(filePath) {
	case searchFileClassDoc:
		if searchQuerySupplied(q, searchDocIntentTerms...) {
			return 1
		}
		return searchNonSourceClassPrior
	case searchFileClassVendored:
		if searchQuerySupplied(q, searchVendoredIntentTerms...) {
			return 1
		}
		return searchNonSourceClassPrior
	case searchFileClassGenerated:
		if searchQuerySupplied(q, searchGeneratedIntentTerms...) {
			return 1
		}
		return searchNonSourceClassPrior
	case searchFileClassExample:
		if searchQuerySupplied(q, searchExampleIntentTerms...) {
			return 1
		}
		return searchSecondaryClassPrior
	}
	return 1
}

// applySearchFileClassPrior scales the positive part of every candidate score by
// its file-class prior. Negative scores are left alone: they are already the
// result of an explicit demotion and multiplying them would UNDO it.
func applySearchFileClassPrior(candidates []searchCandidate, q searchQuery) {
	priors := make(map[string]float64, len(candidates))
	for index := range candidates {
		candidate := &candidates[index]
		if candidate.score <= 0 {
			continue
		}
		path := candidate.result.FilePath
		prior, cached := priors[path]
		if !cached {
			prior = searchFileClassPrior(q, path)
			priors[path] = prior
		}
		if prior >= 1 {
			continue
		}
		candidate.score *= prior
		candidate.result.Signals = appendUnique(candidate.result.Signals, string(classifySearchFile(path))+"-prior")
	}
}
