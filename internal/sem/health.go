package sem

import "path/filepath"

// DegradationThresholdPercent is inclusive. Compare integer counts for status;
// floating point is used only for reporting the percentage.
const DegradationThresholdPercent = 5

// IsIntentionalSkip separates policy skips from meaningful parser failures.
func IsIntentionalSkip(code string) bool { return intentionalSkipFailureCodes[code] }

type HealthCounts struct {
	SourceFiles             int     `json:"source_files"`
	FlaggedFiles            int     `json:"flagged_files"`
	FlaggedPercentage       float64 `json:"flagged_percentage"`
	IntentionalSkippedFiles int     `json:"intentional_skipped_files"`
}

// GraphHealth describes the snapshot's scope (which may be query-selected).
// Diagnostics remain in partial_failures even when the status is ok.
type GraphHealth struct {
	HealthCounts
	ThresholdPercentage int                     `json:"threshold_percentage"`
	Status              string                  `json:"status"`
	Languages           map[string]HealthCounts `json:"languages"`
}

// sourceFileLanguage is the single eligibility policy. Recognized programming,
// template, stylesheet and interface languages, including inventory-only ones,
// count as source. Documentation, configuration and data formats do not. Use
// the parser's resolved language first, including content-routed shell scripts
// and mixed-language components. Unknown files are not assumed to be source.
func sourceFileLanguage(path, language string) (string, bool) {
	if language == "" {
		if spec, ok := languageForPath(path); ok {
			language = spec.language
		} else if unsupportedLanguageHint(path) != "" {
			language = "Unsupported"
		}
	}
	switch language {
	case "", "Markdown", "reStructuredText", "Textile", "TeX", "BibTeX",
		"Mermaid", "VuePress", "Diff", "Patch", "JSON", "JSON5", "YAML",
		"TOML", "XML", "INI", "RON", "Property List", "HCL", "CUE",
		"Kustomize", "Dockerfile", "Make", "CMake", "Cabal", "MSBuild Project",
		"Bicep", "Dhall", "Nix", "Puppet", "Augeas", "Babel Config",
		"EditorConfig", "Dotenv", "Git Ignore", "NPM Config", "Homebrew Bundle",
		"Bundler", "Pip Requirements", "RubyGems", "Just", "Rake":
		return language, false
	default:
		return language, true
	}
}

// calculateGraphHealth includes source files omitted on read/parser failures,
// which have diagnostics but no FileRecord. A path contributes once regardless
// of how many phases or diagnostic categories flagged it. Policy-excluded files
// with neither a record nor a diagnostic are outside the snapshot's scope.
func calculateGraphHealth(files []FileRecord, failures []PartialFailure, stats ProviderStats) GraphHealth {
	health := GraphHealth{
		ThresholdPercentage: DegradationThresholdPercent,
		Languages:           make(map[string]HealthCounts),
	}
	languages := make(map[string]string)
	parsed := make(map[string]bool)
	flagged := make(map[string]bool)
	skipped := make(map[string]bool)
	for _, file := range files {
		if language, eligible := sourceFileLanguage(file.Path, file.Language); eligible {
			path := filepath.ToSlash(filepath.Clean(file.Path))
			languages[path] = language
			parsed[path] = true
		}
	}
	for _, failure := range failures {
		if failure.FilePath == "" {
			continue // repository diagnostics have no source-file numerator
		}
		path := filepath.ToSlash(filepath.Clean(failure.FilePath))
		language, exists := languages[path]
		if !exists {
			var eligible bool
			language, eligible = sourceFileLanguage(path, failure.Language)
			if !eligible {
				continue
			}
			languages[path] = language
		}
		if intentionalSkipFailureCodes[failure.Code] {
			skipped[path] = true
			delete(parsed, path)
		} else {
			flagged[path] = true
		}
	}
	for path, language := range languages {
		counts := health.Languages[language]
		counts.SourceFiles++
		if flagged[path] {
			counts.FlaggedFiles++
		}
		if skipped[path] {
			counts.IntentionalSkippedFiles++
		}
		counts.FlaggedPercentage = flaggedPercentage(counts.FlaggedFiles, counts.SourceFiles)
		health.Languages[language] = counts
	}
	health.SourceFiles = len(languages)
	health.FlaggedFiles = len(flagged)
	health.IntentionalSkippedFiles = len(skipped)
	health.FlaggedPercentage = flaggedPercentage(health.FlaggedFiles, health.SourceFiles)
	health.Status = completenessLevel(health.FlaggedFiles, health.SourceFiles, len(parsed), stats.Symbols)
	// Retain existing recorded-file and empty/unusable-graph safeguards even
	// when there are no eligible source files. Non-source files can never
	// dilute a source failure or soften an unsafe result.
	guard := completenessLevel(0, stats.Files, stats.ParsedFiles, stats.Symbols)
	if guard == "unsafe" || (guard == "degraded" && health.Status == "ok") {
		health.Status = guard
	}
	return health
}

func flaggedPercentage(flagged, total int) float64 {
	if total == 0 {
		return 0 // no eligible source: 0%, subject to the graph safeguards
	}
	return 100 * float64(flagged) / float64(total)
}
