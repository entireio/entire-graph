package sem

// This file is an opt-in, one-request-per-process evaluation harness for P1.
// It lives in a _test.go file: the campaign coordinator supplies the repository,
// mutation, cache directory and manifest. One frozen evaluation executable calls
// production APIs in both arms; it is not the distributed CLI binary. The harness
// never edits a corpus file.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	extractionCorpusManifestEnv     = "ENTIRE_GRAPH_EXTRACTION_CORPUS_MANIFEST"
	extractionCorpusConfigEnv       = "ENTIRE_GRAPH_EXTRACTION_CORPUS_CONFIG"
	extractionCorpusPathEnv         = "ENTIRE_GRAPH_EXTRACTION_CORPUS_PATH"
	extractionCorpusModeEnv         = "ENTIRE_GRAPH_EXTRACTION_CORPUS_MODE"
	extractionCorpusCacheEnv        = "ENTIRE_GRAPH_EXTRACTION_CORPUS_CACHE"
	extractionCorpusCachePathEnv    = "ENTIRE_GRAPH_EXTRACTION_CORPUS_CACHE_PATH"
	extractionCorpusProfileEnv      = "ENTIRE_GRAPH_EXTRACTION_CORPUS_PROFILE"
	extractionCorpusQueryEnv        = "ENTIRE_GRAPH_EXTRACTION_CORPUS_QUERY"
	extractionCorpusOperationEnv    = "ENTIRE_GRAPH_EXTRACTION_CORPUS_OPERATION"
	extractionCorpusOutputEnv       = "ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT"
	extractionCorpusOutputFormatEnv = "ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT_FORMAT"

	// These aliases make the harness convenient to invoke from a coordinator
	// that already uses the shorter P1 names. The long names above are the
	// documented interface and are emitted in errors.
	extractionCorpusP1ManifestEnv = "ENTIRE_GRAPH_P1_MANIFEST"
	extractionCorpusP1PathEnv     = "ENTIRE_GRAPH_P1_CORPUS_PATH"
	extractionCorpusP1ModeEnv     = "ENTIRE_GRAPH_P1_MODE"
	extractionCorpusP1OutputEnv   = "ENTIRE_GRAPH_P1_OUTPUT"
)

type extractionCorpusManifest struct {
	Version         int      `json:"version"`
	Name            string   `json:"name"`
	Repository      string   `json:"repository"`
	Repo            string   `json:"repo"`
	RepoPath        string   `json:"repo_path"`
	RepositoryPath  string   `json:"repository_path"`
	Path            string   `json:"path"`
	CorpusPath      string   `json:"corpus_path"`
	Operation       string   `json:"operation"`
	Verb            string   `json:"verb"`
	Mode            string   `json:"mode"`
	Cache           string   `json:"cache"`
	CacheMode       string   `json:"cache_mode"`
	CachePath       string   `json:"cache_path"`
	CacheDir        string   `json:"cache_dir"`
	Profile         Profile  `json:"profile"`
	Query           string   `json:"query"`
	ProviderVersion string   `json:"provider_version"`
	TopK            int      `json:"top_k"`
	MaxIndexedFiles int      `json:"max_indexed_files"`
	OnlyFiles       []string `json:"only_files"`
	RepositoryID    string   `json:"repository_id"`
	MutationID      string   `json:"mutation_id"`
	SourceDigest    string   `json:"source_digest"`
	Scenario        string   `json:"scenario"`
	Trial           int      `json:"trial"`
	Reuse           *bool    `json:"reuse"`
}

type extractionCorpusEvaluationConfig struct {
	ManifestPath    string
	Manifest        extractionCorpusManifest
	Repository      string
	RepositoryPath  string
	Operation       string
	Mode            string
	CacheMode       string
	CachePath       string
	Profile         Profile
	Query           string
	ProviderVersion string
	TopK            int
	MaxIndexedFiles int
	OnlyFiles       []string
	Scenario        string
	Trial           int
	Reuse           bool
	OutputPath      string
	OutputFormat    string
}

type extractionCorpusPhase struct {
	NS          int64  `json:"ns"`
	Events      int    `json:"events,omitempty"`
	FilesDone   int    `json:"files_done,omitempty"`
	FilesTotal  int    `json:"files_total,omitempty"`
	Symbols     int    `json:"symbols,omitempty"`
	Relations   int    `json:"relations,omitempty"`
	MaxRSSBytes uint64 `json:"max_rss_bytes,omitempty"`
}

type extractionCorpusFailure struct {
	Code                 string `json:"code"`
	Severity             string `json:"severity"`
	FilePath             string `json:"file_path,omitempty"`
	EffectOnCompleteness string `json:"effect_on_semantic_completeness"`
	Detail               string `json:"detail,omitempty"`
}

// extractionCorpusObservation is intentionally an observation envelope rather
// than a second product schema. The full product stats and every explicit
// failure remain available; semanticDigest is the compact cross-arm identity.
type extractionCorpusObservation struct {
	FormatVersion         int                       `json:"format_version"`
	ManifestVersion       int                       `json:"manifest_version,omitempty"`
	ManifestPath          string                    `json:"manifest_path,omitempty"`
	Repository            string                    `json:"repository"`
	RepositoryPath        string                    `json:"repository_path"`
	Operation             string                    `json:"operation"`
	Mode                  string                    `json:"mode"`
	CacheMode             string                    `json:"cache_mode"`
	CachePath             string                    `json:"cache_path,omitempty"`
	Profile               Profile                   `json:"profile"`
	Query                 string                    `json:"query,omitempty"`
	ProviderVersion       string                    `json:"provider_version"`
	MutationID            string                    `json:"mutation_id,omitempty"`
	SourceDigest          string                    `json:"source_digest,omitempty"`
	BinarySHA256          string                    `json:"binary_sha256,omitempty"`
	Scenario              string                    `json:"scenario"`
	Trial                 int                       `json:"trial"`
	Reuse                 bool                      `json:"reuse"`
	Verb                  string                    `json:"verb"`
	Status                string                    `json:"status"`
	Error                 string                    `json:"error,omitempty"`
	StartedAt             string                    `json:"started_at,omitempty"`
	ElapsedNS             int64                     `json:"elapsed_ns"`
	WallNS                int64                     `json:"wall_ns"`
	ProductNS             int64                     `json:"product_ns"`
	SerializationNS       int64                     `json:"serialization_ns"`
	PhaseNS               map[string]int64          `json:"phase_ns,omitempty"`
	SemanticSHA256        string                    `json:"semantic_sha256,omitempty"`
	SemanticDigest        string                    `json:"semantic_digest,omitempty"`
	SemanticBytes         int                       `json:"semantic_bytes,omitempty"`
	PeakRSSBytes          any                       `json:"peak_rss_bytes"`
	CacheBytes            any                       `json:"cache_bytes"`
	Extraction            any                       `json:"extraction"`
	PartialFailuresCount  int                       `json:"partial_failures_count"`
	PartialFailuresSHA256 string                    `json:"partial_failures_sha256,omitempty"`
	PartialFailures       []extractionCorpusFailure `json:"partial_failures,omitempty"`
	WarningsCount         int                       `json:"warnings_count"`
	WarningsSHA256        string                    `json:"warnings_sha256,omitempty"`
	Warnings              []ProviderWarning         `json:"warnings,omitempty"`
	Stats                 any                       `json:"stats,omitempty"`
	Completeness          any                       `json:"completeness,omitempty"`
}

func corpusFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func envFirst(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func readExtractionCorpusManifest(path string) (extractionCorpusManifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return extractionCorpusManifest{}, fmt.Errorf("open corpus manifest %q: %w", path, err)
	}
	defer file.Close()
	var manifest extractionCorpusManifest
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	if err := decoder.Decode(&manifest); err != nil {
		return extractionCorpusManifest{}, fmt.Errorf("decode corpus manifest %q: %w", path, err)
	}
	if manifest.Version == 0 {
		manifest.Version = 1
	}
	if manifest.Version != 1 {
		return extractionCorpusManifest{}, fmt.Errorf("unsupported corpus manifest version %d", manifest.Version)
	}
	return manifest, nil
}

func resolveExtractionCorpusRepo(manifestPath, corpusOverride string, manifest extractionCorpusManifest) (string, error) {
	pathValue := corpusFirstNonEmpty(manifest.RepoPath, manifest.RepositoryPath, manifest.Path)
	corpusPath := corpusFirstNonEmpty(corpusOverride, manifest.CorpusPath)
	if pathValue == "" {
		// A manifest whose repository is itself a path is accepted, while a
		// human-readable repository label still resolves through corpus_path.
		if manifest.Repository != "" && filepath.IsAbs(manifest.Repository) {
			pathValue = manifest.Repository
		} else if manifest.Repo != "" && filepath.IsAbs(manifest.Repo) {
			pathValue = manifest.Repo
		}
	}
	if pathValue == "" && corpusPath != "" {
		pathValue = corpusFirstNonEmpty(manifest.Repository, manifest.Repo)
		if pathValue == "" {
			pathValue = "."
		}
	}
	if pathValue == "" {
		return "", errors.New("corpus manifest has no repo_path/path or corpus_path; set ENTIRE_GRAPH_EXTRACTION_CORPUS_PATH")
	}
	if !filepath.IsAbs(pathValue) {
		base := corpusPath
		if base == "" {
			base = filepath.Dir(manifestPath)
		}
		pathValue = filepath.Join(base, pathValue)
	}
	pathValue, err := filepath.Abs(filepath.Clean(pathValue))
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	info, err := os.Stat(pathValue)
	if err != nil {
		return "", fmt.Errorf("stat repository path %q: %w", pathValue, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository path %q is not a directory", pathValue)
	}
	return pathValue, nil
}

func loadExtractionCorpusEvaluationConfig() (extractionCorpusEvaluationConfig, error) {
	manifestPath := envFirst(extractionCorpusManifestEnv, extractionCorpusConfigEnv, extractionCorpusP1ManifestEnv)
	if manifestPath == "" {
		return extractionCorpusEvaluationConfig{}, errors.New("set ENTIRE_GRAPH_EXTRACTION_CORPUS_MANIFEST to enable the P1 harness")
	}
	manifestPath, err := filepath.Abs(manifestPath)
	if err != nil {
		return extractionCorpusEvaluationConfig{}, fmt.Errorf("resolve corpus manifest: %w", err)
	}
	manifest, err := readExtractionCorpusManifest(manifestPath)
	if err != nil {
		return extractionCorpusEvaluationConfig{}, err
	}
	repoPath, err := resolveExtractionCorpusRepo(manifestPath, envFirst(extractionCorpusPathEnv, extractionCorpusP1PathEnv), manifest)
	if err != nil {
		return extractionCorpusEvaluationConfig{}, err
	}
	operation := strings.ToLower(corpusFirstNonEmpty(envFirst(extractionCorpusOperationEnv), manifest.Operation, manifest.Verb, "snapshot"))
	if operation == "searchrepository" || operation == "search_repository" {
		operation = "search"
	}
	if operation != "snapshot" && operation != "search" {
		return extractionCorpusEvaluationConfig{}, fmt.Errorf("operation must be snapshot or search, got %q", operation)
	}
	mode := strings.ToLower(corpusFirstNonEmpty(envFirst(extractionCorpusModeEnv, extractionCorpusP1ModeEnv), manifest.Mode, "measure"))
	if mode != "warm" && mode != "measure" {
		return extractionCorpusEvaluationConfig{}, fmt.Errorf("mode must be warm or measure, got %q", mode)
	}
	cacheMode := strings.ToLower(corpusFirstNonEmpty(envFirst(extractionCorpusCacheEnv), manifest.CacheMode, manifest.Cache, "on"))
	if cacheMode == "true" {
		cacheMode = "on"
	}
	if cacheMode == "false" {
		cacheMode = "off"
	}
	if cacheMode != "on" && cacheMode != "off" {
		return extractionCorpusEvaluationConfig{}, fmt.Errorf("cache must be on or off, got %q", cacheMode)
	}
	cachePath := corpusFirstNonEmpty(envFirst(extractionCorpusCachePathEnv), manifest.CachePath, manifest.CacheDir)
	if cacheMode == "on" && cachePath == "" {
		return extractionCorpusEvaluationConfig{}, errors.New("cache=on requires ENTIRE_GRAPH_EXTRACTION_CORPUS_CACHE_PATH or manifest cache_path")
	}
	if cachePath != "" {
		if !filepath.IsAbs(cachePath) {
			cachePath = filepath.Join(filepath.Dir(manifestPath), cachePath)
		}
		cachePath, err = filepath.Abs(filepath.Clean(cachePath))
		if err != nil {
			return extractionCorpusEvaluationConfig{}, fmt.Errorf("resolve cache path: %w", err)
		}
	}
	profile := Profile(corpusFirstNonEmpty(envFirst(extractionCorpusProfileEnv), string(manifest.Profile), string(ProfileFull)))
	if profile != ProfileSyntaxOnly && profile != ProfileFast && profile != ProfileFull {
		return extractionCorpusEvaluationConfig{}, fmt.Errorf("profile must be syntax-only, fast, or full, got %q", profile)
	}
	query := corpusFirstNonEmpty(envFirst(extractionCorpusQueryEnv), manifest.Query)
	if operation == "search" && query == "" {
		return extractionCorpusEvaluationConfig{}, errors.New("search operation requires query in manifest or ENTIRE_GRAPH_EXTRACTION_CORPUS_QUERY")
	}
	providerVersion := corpusFirstNonEmpty(manifest.ProviderVersion, "p1-corpus-v1")
	topK := manifest.TopK
	if topK <= 0 {
		topK = 8
	}
	return extractionCorpusEvaluationConfig{
		ManifestPath: manifestPath, Manifest: manifest, Repository: corpusFirstNonEmpty(manifest.Repository, manifest.Repo, manifest.Name, filepath.Base(repoPath)),
		RepositoryPath: repoPath, Operation: operation, Mode: mode, CacheMode: cacheMode, CachePath: cachePath,
		Profile: profile, Query: query, ProviderVersion: providerVersion, TopK: topK,
		MaxIndexedFiles: manifest.MaxIndexedFiles, OnlyFiles: append([]string(nil), manifest.OnlyFiles...),
		Scenario: corpusFirstNonEmpty(manifest.Scenario, "unspecified"), Trial: manifest.Trial,
		Reuse:        cacheMode == "on",
		OutputPath:   envFirst(extractionCorpusOutputEnv, extractionCorpusP1OutputEnv),
		OutputFormat: strings.ToLower(corpusFirstNonEmpty(os.Getenv(extractionCorpusOutputFormatEnv), "ndjson")),
	}, nil
}

func extractionCorpusFailureList(failures []PartialFailure) []extractionCorpusFailure {
	if len(failures) == 0 {
		return nil
	}
	result := make([]extractionCorpusFailure, len(failures))
	for i, failure := range failures {
		result[i] = extractionCorpusFailure{Code: failure.Code, Severity: failure.Severity, FilePath: failure.FilePath, EffectOnCompleteness: failure.EffectOnCompleteness, Detail: failure.Detail}
	}
	return result
}

const extractionCorpusDiagnosticExamples = 32

func boundedExtractionCorpusFailures(failures []PartialFailure) []extractionCorpusFailure {
	items := extractionCorpusFailureList(failures)
	if len(items) > extractionCorpusDiagnosticExamples {
		items = items[:extractionCorpusDiagnosticExamples]
	}
	return items
}

func boundedExtractionCorpusWarnings(warnings []ProviderWarning) []ProviderWarning {
	if len(warnings) > extractionCorpusDiagnosticExamples {
		warnings = warnings[:extractionCorpusDiagnosticExamples]
	}
	return warnings
}

func extractionCorpusFailureDigest(failures []PartialFailure) string {
	data, err := json.Marshal(failures)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func extractionCorpusWarningDigest(warnings []ProviderWarning) string {
	data, err := json.Marshal(warnings)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func extractionCorpusPhaseProgress(phases map[string]extractionCorpusPhase, event ProgressEvent) {
	phase := string(event.Phase)
	value := phases[phase]
	value.Events++
	value.FilesDone = event.FilesDone
	value.FilesTotal = event.FilesTotal
	value.Symbols = event.Symbols
	value.Relations = event.Relations
	if event.MaxRSSBytes > value.MaxRSSBytes {
		value.MaxRSSBytes = event.MaxRSSBytes
	}
	if event.PhaseElapsed.Nanoseconds() > value.NS {
		value.NS = event.PhaseElapsed.Nanoseconds()
	}
	phases[phase] = value
}

func extractionCorpusPhaseNS(phases map[string]extractionCorpusPhase) map[string]int64 {
	if len(phases) == 0 {
		return nil
	}
	result := make(map[string]int64, len(phases))
	for name, phase := range phases {
		result[name] = phase.NS
	}
	return result
}

func extractionCorpusSnapshotDigest(snapshot ProviderSnapshot) (string, int, error) {
	// OperationInputs and extraction stats are opt-in operational provenance.
	// Every semantic record, warning, failure and completeness field stays in
	// this projection. The caller invokes this after the timed request.
	snapshot.Header.OperationInputs = nil
	snapshot.Header.Stats.Extraction = nil
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", 0, err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), len(data), nil
}

func extractionCorpusSearchDigest(response SearchResponse) (string, int, error) {
	response.OperationInputs = nil
	// SearchStats is operational telemetry: candidate counts, preselection
	// backend/read counts, cache state, and latency can differ between reuse
	// arms while returned results and guidance remain identical. Raw Stats is
	// retained separately in the observation.
	response.Stats = SearchStats{}
	data, err := json.Marshal(response)
	if err != nil {
		return "", 0, err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), len(data), nil
}

func extractionCorpusStatus(requestErr error, failures int) string {
	if requestErr != nil {
		return "error"
	}
	if failures > 0 {
		return "partial"
	}
	return "ok"
}

func extractionCorpusTelemetry(value *ExtractionStats) any {
	if value == nil {
		return map[string]any{"unchanged_reparses": nil}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return map[string]any{"unchanged_reparses": nil}
	}
	var telemetry map[string]any
	if err := json.Unmarshal(data, &telemetry); err != nil {
		return map[string]any{"unchanged_reparses": nil}
	}
	// FilesParsed is the native extraction count. It does not prove that every
	// parsed file was an eligible unchanged input: transient failures, policy
	// changes, and scenario edits can contribute to it. Only an exact zero can
	// establish zero unchanged reparses; a positive count is explicitly
	// evidence-incomplete for that gate rather than a fabricated eligible count.
	if value.FilesParsed == 0 {
		telemetry["unchanged_reparses"] = int64(0)
	} else {
		telemetry["unchanged_reparses"] = nil
	}
	return telemetry
}

func writeExtractionCorpusObservation(path, format string, observation extractionCorpusObservation) error {
	if path == "" {
		return errors.New("set ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT to write the observation")
	}
	if format != "ndjson" && format != "json" {
		return fmt.Errorf("output format must be ndjson or json, got %q", format)
	}
	data, err := json.Marshal(observation)
	if err != nil {
		return fmt.Errorf("marshal observation: %w", err)
	}
	if format == "ndjson" {
		data = append(data, '\n')
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("open observation output %q: %w", path, err)
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write observation output %q: %w", path, err)
	}
	return nil
}

func TestExtractionCorpusMeasurement(t *testing.T) {
	if envFirst(extractionCorpusManifestEnv, extractionCorpusConfigEnv, extractionCorpusP1ManifestEnv) == "" {
		t.Skip("set ENTIRE_GRAPH_EXTRACTION_CORPUS_MANIFEST for the opt-in P1 corpus harness")
	}
	config, err := loadExtractionCorpusEvaluationConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.OutputPath == "" {
		t.Fatal("set ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT for the opt-in P1 corpus harness")
	}
	if config.Mode == "warm" && config.CacheMode != "on" {
		t.Fatal("warm mode requires cache=on")
	}
	if config.CacheMode == "on" {
		if err := os.MkdirAll(config.CachePath, 0700); err != nil {
			t.Fatal(fmt.Errorf("create extraction cache path: %w", err))
		}
	}

	observation := extractionCorpusObservation{
		FormatVersion: 1, ManifestVersion: config.Manifest.Version, ManifestPath: config.ManifestPath,
		Repository: config.Repository, RepositoryPath: config.RepositoryPath, Operation: config.Operation,
		Mode: config.Mode, CacheMode: config.CacheMode, CachePath: config.CachePath, Profile: config.Profile,
		Query: config.Query, ProviderVersion: config.ProviderVersion, MutationID: config.Manifest.MutationID,
		SourceDigest: config.Manifest.SourceDigest, Status: "error",
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Scenario: config.Scenario,
		Trial: config.Trial, Reuse: config.Reuse, Verb: config.Operation,
	}
	phases := make(map[string]extractionCorpusPhase)
	requestStarted := time.Now()
	var requestErr error
	var serializeErr error
	var semanticDigest string
	var semanticBytes int
	var serializationNS int64
	var failures []PartialFailure
	var warnings []ProviderWarning
	var stats any
	var completeness any
	var extraction *ExtractionStats
	var snapshotForDigest *ProviderSnapshot
	var searchForDigest *SearchResponse

	if config.Operation == "snapshot" {
		options := ProviderSnapshotOptions{
			ExtractionReuse: config.Reuse, ExtractionCacheDir: config.CachePath,
			Worktree: true, Profile: config.Profile, OnlyFiles: config.OnlyFiles,
			Compiler: nil, Progress: func(event ProgressEvent) { extractionCorpusPhaseProgress(phases, event) },
		}
		snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), config.RepositoryPath, config.ProviderVersion, options)
		requestErr = err
		failures = snapshot.Header.PartialFailures
		warnings = snapshot.Header.Warnings
		stats = snapshot.Header.Stats
		extraction = snapshot.Header.Stats.Extraction
		completeness = snapshot.Header.Completeness
		serializeStarted := time.Now()
		_, serializeErr = json.Marshal(snapshot)
		serializationNS = time.Since(serializeStarted).Nanoseconds()
		snapshotForDigest = &snapshot
	} else {
		options := SearchOptions{
			Compiler: nil, Ranking: "current", ExtractionReuse: config.Reuse, CacheDir: config.CachePath,
			DisableCache: config.CacheMode == "off", Worktree: true, Profile: config.Profile,
			TopK: config.TopK, MaxIndexedFiles: config.MaxIndexedFiles,
		}
		response, err := SearchRepository(t.Context(), config.RepositoryPath, config.ProviderVersion, config.Query, options)
		requestErr = err
		failures = response.PartialFailures
		warnings = response.Warnings
		stats = response.Stats
		extraction = response.Stats.Extraction
		completeness = response.Completeness
		serializeStarted := time.Now()
		_, serializeErr = json.Marshal(response)
		serializationNS = time.Since(serializeStarted).Nanoseconds()
		searchForDigest = &response
		if searchStats, ok := stats.(SearchStats); ok {
			phases["index"] = extractionCorpusPhase{NS: searchStats.IndexLatencyMS * int64(time.Millisecond), FilesDone: searchStats.FilesIndexed}
			phases["preselect"] = extractionCorpusPhase{NS: searchStats.PreselectLatencyMS * int64(time.Millisecond)}
			phases["query"] = extractionCorpusPhase{NS: searchStats.QueryLatencyMS * int64(time.Millisecond)}
			phases["total"] = extractionCorpusPhase{NS: searchStats.TotalLatencyMS * int64(time.Millisecond)}
		}
	}
	// Stop the request clock before canonical verification and hashing. The
	// latter is harness validation work and must not inflate elapsed_ns.
	elapsedNS := time.Since(requestStarted).Nanoseconds()
	if requestErr == nil && serializeErr == nil {
		if snapshotForDigest != nil {
			semanticDigest, semanticBytes, serializeErr = extractionCorpusSnapshotDigest(*snapshotForDigest)
		} else if searchForDigest != nil {
			semanticDigest, semanticBytes, serializeErr = extractionCorpusSearchDigest(*searchForDigest)
		}
	}
	productNS := elapsedNS - serializationNS
	if productNS < 0 {
		productNS = 0
	}
	observation.ElapsedNS = elapsedNS
	observation.WallNS = observation.ElapsedNS
	phases["product"] = extractionCorpusPhase{NS: productNS}
	phases["serialization"] = extractionCorpusPhase{NS: serializationNS}
	phases["total"] = extractionCorpusPhase{NS: observation.ElapsedNS}
	observation.ProductNS = productNS
	observation.SerializationNS = serializationNS
	observation.PhaseNS = extractionCorpusPhaseNS(phases)
	observation.SemanticSHA256 = semanticDigest
	observation.SemanticDigest = semanticDigest
	observation.SemanticBytes = semanticBytes
	observation.PartialFailures = boundedExtractionCorpusFailures(failures)
	observation.PartialFailuresCount = len(failures)
	observation.PartialFailuresSHA256 = extractionCorpusFailureDigest(failures)
	observation.Warnings = boundedExtractionCorpusWarnings(warnings)
	observation.WarningsCount = len(warnings)
	observation.WarningsSHA256 = extractionCorpusWarningDigest(warnings)
	observation.Extraction = extractionCorpusTelemetry(extraction)
	observation.PeakRSSBytes = nil // filled by the external per-process collector
	observation.CacheBytes = nil   // filled by the runner's cache namespace accounting
	observation.BinarySHA256 = extractionBuildIdentity()
	observation.Stats = stats
	observation.Completeness = completeness
	observation.Status = extractionCorpusStatus(requestErr, len(failures))
	if requestErr != nil {
		observation.Error = requestErr.Error()
	}
	if serializeErr != nil {
		if observation.Error != "" {
			observation.Error += "; "
		}
		observation.Error += "serialization: " + serializeErr.Error()
		observation.Status = "error"
	}
	// Request errors are data for the external coordinator. Do not use t.Fatal
	// after the product call: that would discard the raw failure and its counts.
	if err := writeExtractionCorpusObservation(config.OutputPath, config.OutputFormat, observation); err != nil {
		t.Fatal(err)
	}
}

func TestExtractionCorpusEvaluationCanonicalDigest(t *testing.T) {
	snapshot := ProviderSnapshot{Header: SnapshotHeader{OperationInputs: &OperationInputManifest{ID: "operation"}, Stats: ProviderStats{Extraction: &ExtractionStats{FilesParsed: 3}, Files: 1, PartialFailures: 1}, PartialFailures: []PartialFailure{{Code: "E_TEST", Severity: "error", EffectOnCompleteness: "partial"}}}}
	digest, _, err := extractionCorpusSnapshotDigest(snapshot)
	if err != nil || digest == "" {
		t.Fatalf("snapshot digest: %q, %v", digest, err)
	}
	changed := snapshot
	changed.Header.OperationInputs = &OperationInputManifest{ID: "different"}
	changed.Header.Stats.Extraction = &ExtractionStats{FilesParsed: 99}
	changed.Header.PartialFailures = append([]PartialFailure(nil), snapshot.Header.PartialFailures...)
	changedDigest, _, err := extractionCorpusSnapshotDigest(changed)
	if err != nil || digest != changedDigest {
		t.Fatalf("opt-in fields changed semantic digest: %q != %q (%v)", digest, changedDigest, err)
	}
	changed.Header.PartialFailures[0].Code = "E_DIFFERENT"
	differentDigest, _, err := extractionCorpusSnapshotDigest(changed)
	if err != nil || digest == differentDigest {
		t.Fatalf("semantic failure change did not change digest: %q", digest)
	}
}

func TestExtractionCorpusEvaluationSearchDigestExcludesOperationalStats(t *testing.T) {
	response := SearchResponse{
		Query: "trace request routing", Results: []SearchResult{{Rank: 1, FilePath: "handler.go", StartLine: 4, EndLine: 8, Snippet: "route()"}},
		Warnings: []ProviderWarning{{Code: "W_TEST", Severity: "warning", EffectOnCompleteness: "none"}},
		Stats:    SearchStats{IndexCacheHit: true, PreselectionBackend: "git-grep", TotalLatencyMS: 17, ResultBytes: 42},
	}
	digest, _, err := extractionCorpusSearchDigest(response)
	if err != nil {
		t.Fatal(err)
	}
	changedStats := response
	changedStats.Stats = SearchStats{IndexCacheHit: false, PreselectionBackend: "walk", TotalLatencyMS: 99, ResultBytes: 1000}
	changedDigest, _, err := extractionCorpusSearchDigest(changedStats)
	if err != nil || digest != changedDigest {
		t.Fatalf("operational search stats changed semantic digest: %q != %q (%v)", digest, changedDigest, err)
	}
	changedResult := response
	changedResult.Results = append([]SearchResult(nil), response.Results...)
	changedResult.Results[0].Snippet = "different()"
	differentDigest, _, err := extractionCorpusSearchDigest(changedResult)
	if err != nil || digest == differentDigest {
		t.Fatalf("semantic search result change did not change digest: %q", digest)
	}
}

func TestExtractionCorpusEvaluationManifestConfig(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "config.json")
	manifest := []byte(`{"version":1,"repository":"chi","repo_path":"repo","operation":"search","mode":"measure","cache":"off","profile":"fast","query":"corpusagent","top_k":8}`)
	if err := os.WriteFile(manifestPath, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(extractionCorpusManifestEnv, manifestPath)
	t.Setenv(extractionCorpusConfigEnv, "")
	t.Setenv(extractionCorpusP1ManifestEnv, "")
	config, err := loadExtractionCorpusEvaluationConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Repository != "chi" || config.RepositoryPath != repo || config.Operation != "search" || config.Mode != "measure" || config.CacheMode != "off" || config.Profile != ProfileFast || config.Query != "corpusagent" || config.TopK != 8 {
		t.Fatalf("config = %+v", config)
	}
}

func TestExtractionCorpusEvaluationOutputFormats(t *testing.T) {
	dir := t.TempDir()
	observation := extractionCorpusObservation{FormatVersion: 1, Status: "error", Error: "raw failure", PartialFailures: []extractionCorpusFailure{{Code: "E_RAW", EffectOnCompleteness: "partial"}}}
	for _, format := range []string{"ndjson", "json"} {
		path := filepath.Join(dir, format+".out")
		if err := writeExtractionCorpusObservation(path, format, observation); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) == 0 || !strings.Contains(string(data), `"E_RAW"`) {
			t.Fatalf("%s output lost raw failure: %q", format, data)
		}
	}
}
