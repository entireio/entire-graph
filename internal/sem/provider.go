package sem

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/suhaanthayyil/entire-sem/internal/gitutil"
)

const (
	SchemaVersion         = "1.0"
	ProviderName          = "entire-sem"
	StableSymbolIDVersion = "compound-v1"
)

var relationTypes = []string{
	"DEFINES",
	"CONTAINS",
	"IMPORTS",
	"CALLS",
	"IMPLEMENTS",
	"EXTENDS",
	"OVERRIDES",
	"ACCESSES",
	"HANDLES_ROUTE",
	"HANDLES_TOOL",
}

type ProviderRecord struct {
	RecordType string `json:"record_type,omitempty"`
}

type SnapshotHeader struct {
	SchemaVersion   string            `json:"schema_version"`
	Provider        string            `json:"provider"`
	ProviderVersion string            `json:"provider_version"`
	RepoRoot        string            `json:"repo_root"`
	RepoKey         string            `json:"repo_key"`
	Commit          string            `json:"commit"`
	Tree            string            `json:"tree"`
	Languages       []string          `json:"languages"`
	Capabilities    []string          `json:"capabilities"`
	Warnings        []ProviderWarning `json:"warnings"`
	PartialFailures []PartialFailure  `json:"partial_failures"`
	Stats           ProviderStats     `json:"stats"`
}

type ProviderStats struct {
	Files             int    `json:"files"`
	ParsedFiles       int    `json:"parsed_files"`
	Symbols           int    `json:"symbols"`
	Relations         int    `json:"relations"`
	PartialFailures   int    `json:"partial_failures"`
	CompletenessLevel string `json:"completeness_level"`
}

type ProviderWarning struct {
	Code                 string `json:"code"`
	Severity             string `json:"severity"`
	FilePath             string `json:"file_path,omitempty"`
	EffectOnCompleteness string `json:"effect_on_semantic_completeness"`
	Detail               string `json:"detail,omitempty"`
}

type PartialFailure struct {
	Code                 string `json:"code"`
	Severity             string `json:"severity"`
	FilePath             string `json:"file_path,omitempty"`
	EffectOnCompleteness string `json:"effect_on_semantic_completeness"`
	Detail               string `json:"detail,omitempty"`
}

type FileRecord struct {
	RecordType string `json:"record_type"`
	Path       string `json:"path"`
	Blob       string `json:"blob"`
	Language   string `json:"language,omitempty"`
	Bytes      int    `json:"bytes"`
}

type SymbolRecord struct {
	RecordType      string `json:"record_type"`
	ID              string `json:"id"`
	StableIDVersion string `json:"stable_id_version"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	QualifiedName   string `json:"qualified_name"`
	FilePath        string `json:"file_path"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	Signature       string `json:"signature"`
	BodyHash        string `json:"body_hash"`
	Language        string `json:"language"`
	ContainerID     string `json:"container_id,omitempty"`
}

type RelationRecord struct {
	RecordType   string   `json:"record_type"`
	FromID       string   `json:"from_id"`
	ToID         string   `json:"to_id"`
	Type         string   `json:"type"`
	Confidence   float64  `json:"confidence"`
	Reason       string   `json:"reason"`
	WarningCodes []string `json:"warning_codes"`
}

type CapabilityReport struct {
	SchemaVersion                   string            `json:"schema_version"`
	Provider                        string            `json:"provider"`
	SupportedFileExtensions         []string          `json:"supported_file_extensions"`
	SupportedLanguages              []string          `json:"supported_languages"`
	ParserVersions                  map[string]string `json:"parser_versions"`
	SupportedRelationTypes          []string          `json:"supported_relation_types"`
	UnsupportedButDetectedLanguages []string          `json:"unsupported_but_detected_language_hints"`
	OptionalLocalOnlyFeatures       map[string]bool   `json:"optional_local_only_features"`
	FeaturesRequiringNetworkAccess  map[string]bool   `json:"features_requiring_network_access"`
}

type ProviderSnapshot struct {
	Header    SnapshotHeader
	Files     []FileRecord
	Symbols   []SymbolRecord
	Relations []RelationRecord
}

func Capabilities() CapabilityReport {
	extensions := make([]string, 0, len(treeSitterLanguages))
	languageSet := map[string]struct{}{}
	for extension, spec := range treeSitterLanguages {
		extensions = append(extensions, extension)
		languageSet[spec.language] = struct{}{}
	}
	sort.Strings(extensions)
	languages := make([]string, 0, len(languageSet))
	for language := range languageSet {
		languages = append(languages, language)
	}
	sort.Strings(languages)

	return CapabilityReport{
		SchemaVersion:                   SchemaVersion,
		Provider:                        ProviderName,
		SupportedFileExtensions:         extensions,
		SupportedLanguages:              languages,
		UnsupportedButDetectedLanguages: []string{},
		ParserVersions: map[string]string{
			"go-tree-sitter": "github.com/smacker/go-tree-sitter",
		},
		SupportedRelationTypes: append([]string(nil), relationTypes...),
		OptionalLocalOnlyFeatures: map[string]bool{
			"stable_symbol_ids": true,
			"semantic_diff":     true,
			"ndjson_snapshot":   true,
		},
		FeaturesRequiringNetworkAccess: map[string]bool{
			"grammar_download":  false,
			"hosted_models":     false,
			"remote_embeddings": false,
			"telemetry_upload":  false,
			"remote_code_fetch": false,
			"network_discovery": false,
		},
	}
}

func BuildProviderSnapshot(ctx context.Context, repo, providerVersion string) (ProviderSnapshot, error) {
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return ProviderSnapshot{}, err
	}
	repoKey := repoKey(absRepo)
	commit, _ := gitutil.RevParse(ctx, absRepo, "HEAD")
	tree, _ := gitutil.RevParse(ctx, absRepo, "HEAD^{tree}")

	paths, err := workingTreeFiles(absRepo)
	if err != nil {
		return ProviderSnapshot{}, err
	}

	parser := TreeSitterParser{}
	languageSet := map[string]struct{}{}
	var files []FileRecord
	var symbols []SymbolRecord
	var failures []PartialFailure
	recordsByFile := map[string][]SymbolRecord{}

	for _, path := range paths {
		if !Supported(path) {
			continue
		}
		content, err := os.ReadFile(filepath.Join(absRepo, filepath.FromSlash(path)))
		if err != nil {
			failures = append(failures, PartialFailure{
				Code:                 "E_FILE_READ",
				Severity:             "error",
				FilePath:             path,
				EffectOnCompleteness: "file omitted from semantic snapshot",
				Detail:               err.Error(),
			})
			continue
		}
		entities, language := parser.Parse(path, string(content))
		if language == "" {
			failures = append(failures, PartialFailure{
				Code:                 "E_UNSUPPORTED_LANGUAGE",
				Severity:             "warning",
				FilePath:             path,
				EffectOnCompleteness: "file omitted because no parser is available",
			})
			continue
		}
		languageSet[language] = struct{}{}
		files = append(files, FileRecord{
			RecordType: "file",
			Path:       path,
			Blob:       contentHash(content),
			Language:   language,
			Bytes:      len(content),
		})
		fileSymbols := entitySymbols(repoKey, path, language, entities)
		symbols = append(symbols, fileSymbols...)
		recordsByFile[path] = fileSymbols
	}

	relations := buildRelations(repoKey, absRepo, files, recordsByFile)
	languages := sortedKeys(languageSet)
	if failures == nil {
		failures = []PartialFailure{}
	}
	header := SnapshotHeader{
		SchemaVersion:   SchemaVersion,
		Provider:        ProviderName,
		ProviderVersion: providerVersion,
		RepoRoot:        absRepo,
		RepoKey:         repoKey,
		Commit:          commit,
		Tree:            tree,
		Languages:       languages,
		Capabilities:    []string{"ndjson", "stable-symbol-id-v1", "local-only", "partial-failures"},
		Warnings:        []ProviderWarning{},
		PartialFailures: failures,
		Stats: ProviderStats{
			Files:             len(files),
			ParsedFiles:       len(recordsByFile),
			Symbols:           len(symbols),
			Relations:         len(relations),
			PartialFailures:   len(failures),
			CompletenessLevel: completenessLevel(len(failures), len(files)),
		},
	}
	return ProviderSnapshot{Header: header, Files: files, Symbols: symbols, Relations: relations}, nil
}

func WriteSnapshotNDJSON(out io.Writer, snapshot ProviderSnapshot) error {
	if err := writeJSONLine(out, snapshot.Header); err != nil {
		return err
	}
	for _, record := range snapshot.Files {
		if err := writeJSONLine(out, record); err != nil {
			return err
		}
	}
	for _, record := range snapshot.Symbols {
		if err := writeJSONLine(out, record); err != nil {
			return err
		}
	}
	for _, record := range snapshot.Relations {
		if err := writeJSONLine(out, record); err != nil {
			return err
		}
	}
	return nil
}

func WriteSymbolsNDJSON(out io.Writer, snapshot ProviderSnapshot) error {
	for _, record := range snapshot.Symbols {
		if err := writeJSONLine(out, record); err != nil {
			return err
		}
	}
	return nil
}

func WriteRelationsNDJSON(out io.Writer, snapshot ProviderSnapshot) error {
	for _, record := range snapshot.Relations {
		if err := writeJSONLine(out, record); err != nil {
			return err
		}
	}
	return nil
}

func entitySymbols(repoKey, path, language string, entities []Entity) []SymbolRecord {
	byName := map[string]string{}
	var symbols []SymbolRecord
	for _, entity := range entities {
		qualified := entity.Name
		id := symbolID(repoKey, language, path, entity.Kind, qualified)
		containerID := ""
		if containerName := containerName(qualified); containerName != "" {
			if parentID, ok := byName[containerName]; ok {
				containerID = parentID
			}
		}
		symbol := SymbolRecord{
			RecordType:      "symbol",
			ID:              id,
			StableIDVersion: StableSymbolIDVersion,
			Kind:            entity.Kind,
			Name:            shortEntityName(entity.Name),
			QualifiedName:   qualified,
			FilePath:        path,
			StartLine:       entity.StartLine,
			EndLine:         entity.EndLine,
			Signature:       entity.Signature,
			BodyHash:        entity.BodyHash,
			Language:        language,
			ContainerID:     containerID,
		}
		symbols = append(symbols, symbol)
		byName[qualified] = id
	}
	return symbols
}

func buildRelations(repoKey, repo string, files []FileRecord, recordsByFile map[string][]SymbolRecord) []RelationRecord {
	var relations []RelationRecord
	symbolsByShortName := map[string][]SymbolRecord{}
	for _, records := range recordsByFile {
		for _, symbol := range records {
			relations = append(relations, RelationRecord{
				RecordType:   "relation",
				FromID:       fileID(repoKey, symbol.FilePath),
				ToID:         symbol.ID,
				Type:         "DEFINES",
				Confidence:   1,
				Reason:       "symbol parsed from file",
				WarningCodes: []string{},
			})
			if symbol.ContainerID != "" {
				relations = append(relations, RelationRecord{
					RecordType:   "relation",
					FromID:       symbol.ContainerID,
					ToID:         symbol.ID,
					Type:         "CONTAINS",
					Confidence:   1,
					Reason:       "symbol qualified name is nested in container",
					WarningCodes: []string{},
				})
			}
			symbolsByShortName[symbol.Name] = append(symbolsByShortName[symbol.Name], symbol)
		}
	}

	for _, file := range files {
		contentBytes, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(file.Path)))
		if err != nil {
			continue
		}
		content := string(contentBytes)
		fromID := fileID(repoKey, file.Path)
		for _, imported := range importsFor(file.Path, content) {
			relations = append(relations, RelationRecord{
				RecordType:   "relation",
				FromID:       fromID,
				ToID:         externalID("import", imported),
				Type:         "IMPORTS",
				Confidence:   0.8,
				Reason:       "import declaration matched by language-specific scanner",
				WarningCodes: []string{},
			})
		}
		for _, from := range recordsByFile[file.Path] {
			block := symbolBlock(content, from)
			for name, candidates := range symbolsByShortName {
				if name == from.Name || !containsIdentifier(block, name) {
					continue
				}
				for _, to := range candidates {
					if to.ID == from.ID {
						continue
					}
					relations = append(relations, RelationRecord{
						RecordType:   "relation",
						FromID:       from.ID,
						ToID:         to.ID,
						Type:         "CALLS",
						Confidence:   0.62,
						Reason:       "identifier reference inside symbol body matches known symbol name",
						WarningCodes: []string{},
					})
				}
			}
			for _, route := range routeLiterals(block) {
				relations = append(relations, RelationRecord{
					RecordType:   "relation",
					FromID:       from.ID,
					ToID:         externalID("route", route),
					Type:         "HANDLES_ROUTE",
					Confidence:   0.7,
					Reason:       "route-like string literal found inside handler symbol",
					WarningCodes: []string{},
				})
			}
			if looksLikeToolHandler(from, block) {
				relations = append(relations, RelationRecord{
					RecordType:   "relation",
					FromID:       from.ID,
					ToID:         externalID("tool", from.QualifiedName),
					Type:         "HANDLES_TOOL",
					Confidence:   0.58,
					Reason:       "symbol name or body contains tool handler vocabulary",
					WarningCodes: []string{},
				})
			}
		}
	}

	sort.Slice(relations, func(i, j int) bool {
		left := relations[i].Type + relations[i].FromID + relations[i].ToID
		right := relations[j].Type + relations[j].FromID + relations[j].ToID
		return left < right
	})
	return dedupeRelations(relations)
}

func workingTreeFiles(repo string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(repo, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			switch name {
			case ".git", "node_modules", "vendor", ".next", "dist", "build":
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

func importsFor(path, content string) []string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*import\s+(?:\w+\s+)?["]([^"]+)["]`), regexp.MustCompile(`(?m)^\s*(?:\w+\s+)?["]([^"]+)["]`))
	case ".py":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*(?:from\s+([A-Za-z0-9_\.]+)\s+import|import\s+([A-Za-z0-9_\.]+))`))
	case ".js", ".jsx", ".ts", ".tsx":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*import\s+.*?\s+from\s+['"]([^'"]+)['"]|^\s*import\s+['"]([^'"]+)['"]`))
	case ".rs":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*use\s+([^;]+);`))
	default:
		return nil
	}
}

func scanImports(content string, expressions ...*regexp.Regexp) []string {
	seen := map[string]struct{}{}
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		for _, expression := range expressions {
			matches := expression.FindStringSubmatch(line)
			if len(matches) == 0 {
				continue
			}
			for _, match := range matches[1:] {
				if match == "" {
					continue
				}
				seen[strings.TrimSpace(match)] = struct{}{}
			}
		}
	}
	return sortedKeys(seen)
}

func routeLiterals(content string) []string {
	re := regexp.MustCompile(`["'](/[A-Za-z0-9_\-/{}/:.]*)["']`)
	seen := map[string]struct{}{}
	for _, match := range re.FindAllStringSubmatch(content, -1) {
		if len(match) > 1 {
			seen[match[1]] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func looksLikeToolHandler(symbol SymbolRecord, block string) bool {
	value := strings.ToLower(symbol.QualifiedName + "\n" + block)
	return strings.Contains(value, "tool") && (strings.Contains(value, "handler") || strings.Contains(value, "execute") || strings.Contains(value, "schema"))
}

func symbolBlock(content string, symbol SymbolRecord) string {
	lines := strings.Split(content, "\n")
	start := symbol.StartLine - 1
	if start < 0 {
		start = 0
	}
	end := symbol.EndLine
	if end > len(lines) {
		end = len(lines)
	}
	if end <= start {
		return ""
	}
	return strings.Join(lines[start:end], "\n")
}

func completenessLevel(failures, files int) string {
	switch {
	case failures == 0:
		return "ok"
	case files == 0 || failures*4 > files:
		return "unsafe"
	default:
		return "degraded"
	}
}

func dedupeRelations(relations []RelationRecord) []RelationRecord {
	seen := map[string]struct{}{}
	out := make([]RelationRecord, 0, len(relations))
	for _, relation := range relations {
		key := relation.FromID + "\x00" + relation.ToID + "\x00" + relation.Type
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, relation)
	}
	return out
}

func writeJSONLine(out io.Writer, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(encoded))
	return err
}

func symbolID(repoKey, language, path, kind, qualifiedName string) string {
	return strings.Join([]string{repoKey, language, path, kind, qualifiedName}, ":")
}

func fileID(repoKey, path string) string {
	return repoKey + ":file:" + path
}

func externalID(kind, value string) string {
	return "external:" + kind + ":" + value
}

func repoKey(repo string) string {
	return "local/" + filepath.Base(repo)
}

func containerName(qualifiedName string) string {
	index := strings.LastIndex(qualifiedName, ".")
	if index < 0 {
		return ""
	}
	return qualifiedName[:index]
}

func contentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
