package sem

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const extractionCorpusDiagnosticsSmokeChildEnv = "ENTIRE_GRAPH_EXTRACTION_CORPUS_DIAGNOSTICS_SMOKE_CHILD"
const extractionCorpusDiagnosticsPreflightChildEnv = "ENTIRE_GRAPH_EXTRACTION_CORPUS_DIAGNOSTICS_PREFLIGHT_CHILD"

func TestExtractionCorpusDiagnosticsMeasurementSmoke(t *testing.T) {
	if os.Getenv(extractionCorpusDiagnosticsSmokeChildEnv) == "1" {
		TestExtractionCorpusMeasurement(t)
		return
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	initRepo(t, repo)
	writeFile(t, repo, "main.go", "package smoke\n\nfunc DiagnosticSmoke() {}\n")
	git(t, repo, "add", "main.go")
	manifestPath := filepath.Join(dir, "manifest.json")
	diagnosticsPath := filepath.Join(dir, "diagnostics.json")
	manifest := fmt.Sprintf(`{"version":1,"repository":"smoke","repository_id":"repo-smoke","repo_path":%q,"operation":"snapshot","mode":"measure","cache":"off","profile":"syntax-only","only_files":["main.go"],"mutation_id":"mutation-smoke","source_digest":"source-smoke","diagnostics_path":%q}`, repo, diagnosticsPath)
	if err := os.WriteFile(manifestPath, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	observationPath := filepath.Join(dir, "observation.ndjson")
	t.Setenv(extractionCorpusDiagnosticsSmokeChildEnv, "1")
	t.Setenv(extractionCorpusManifestEnv, manifestPath)
	t.Setenv(extractionCorpusConfigEnv, "")
	t.Setenv(extractionCorpusP1ManifestEnv, "")
	t.Setenv(extractionCorpusOutputEnv, observationPath)
	t.Setenv(extractionCorpusP1OutputEnv, "")
	t.Setenv(extractionCorpusOutputFormatEnv, "ndjson")
	t.Setenv(extractionCorpusPathEnv, "")
	t.Setenv(extractionCorpusP1PathEnv, "")
	t.Setenv(extractionCorpusModeEnv, "")
	t.Setenv(extractionCorpusP1ModeEnv, "")
	t.Setenv(extractionCorpusCacheEnv, "")
	t.Setenv(extractionCorpusCachePathEnv, "")
	t.Setenv(extractionCorpusProfileEnv, "")
	t.Setenv(extractionCorpusQueryEnv, "")
	t.Setenv(extractionCorpusOperationEnv, "")
	command := exec.Command(os.Args[0], "-test.run=^TestExtractionCorpusDiagnosticsMeasurementSmoke$", "-test.count=1")
	command.Env = os.Environ()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("measurement smoke: %v\n%s", err, output)
	}

	var observation extractionCorpusObservation
	readExtractionCorpusJSON(t, observationPath, &observation)
	var diagnostics extractionCorpusDiagnostics
	readExtractionCorpusJSON(t, diagnosticsPath, &diagnostics)
	if observation.Repository != "smoke" || diagnostics.RepositoryID != "repo-smoke" || diagnostics.MutationID != "mutation-smoke" || diagnostics.SourceDigest != "source-smoke" || diagnostics.BinarySHA256 == "" {
		t.Fatalf("measurement identities: observation=%+v diagnostics=%+v", observation, diagnostics)
	}
	if diagnostics.PartialFailuresCount != observation.PartialFailuresCount || diagnostics.PartialFailuresSHA256 != observation.PartialFailuresSHA256 || diagnostics.WarningsCount != observation.WarningsCount || diagnostics.WarningsSHA256 != observation.WarningsSHA256 {
		t.Fatalf("measurement diagnostic summary differs: observation=%+v diagnostics=%+v", observation, diagnostics)
	}
}

func TestExtractionCorpusDiagnosticsMeasurementPreflightRejectsAliasedPaths(t *testing.T) {
	if os.Getenv(extractionCorpusDiagnosticsPreflightChildEnv) == "1" {
		TestExtractionCorpusMeasurement(t)
		return
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	initRepo(t, repo)
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0700); err != nil {
		t.Fatal(err)
	}
	aliasDir := filepath.Join(dir, "alias")
	if err := os.Symlink(realDir, aliasDir); err != nil {
		t.Fatal(err)
	}
	diagnosticsPath := filepath.Join(realDir, "result.json")
	cachePath := filepath.Join(dir, "cache-must-not-be-created")
	manifestPath := filepath.Join(dir, "manifest.json")
	manifest := fmt.Sprintf(`{"version":1,"repo_path":%q,"operation":"snapshot","mode":"measure","cache":"on","cache_path":%q,"diagnostics_path":%q}`, repo, cachePath, diagnosticsPath)
	if err := os.WriteFile(manifestPath, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(extractionCorpusDiagnosticsPreflightChildEnv, "1")
	t.Setenv(extractionCorpusManifestEnv, manifestPath)
	t.Setenv(extractionCorpusConfigEnv, "")
	t.Setenv(extractionCorpusP1ManifestEnv, "")
	t.Setenv(extractionCorpusOutputEnv, filepath.Join(aliasDir, "result.json"))
	t.Setenv(extractionCorpusP1OutputEnv, "")
	t.Setenv(extractionCorpusPathEnv, "")
	t.Setenv(extractionCorpusP1PathEnv, "")
	t.Setenv(extractionCorpusModeEnv, "")
	t.Setenv(extractionCorpusP1ModeEnv, "")
	t.Setenv(extractionCorpusCacheEnv, "")
	t.Setenv(extractionCorpusCachePathEnv, "")
	t.Setenv(extractionCorpusProfileEnv, "")
	t.Setenv(extractionCorpusQueryEnv, "")
	t.Setenv(extractionCorpusOperationEnv, "")
	command := exec.Command(os.Args[0], "-test.run=^TestExtractionCorpusDiagnosticsMeasurementPreflightRejectsAliasedPaths$", "-test.count=1")
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "diagnostics path must differ") {
		t.Fatalf("preflight child error = %v\n%s", err, output)
	}
	if _, err := os.Lstat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("preflight created cache path: %v", err)
	}
	if _, err := os.Lstat(diagnosticsPath); !os.IsNotExist(err) {
		t.Fatalf("preflight wrote aliased output: %v", err)
	}
}

func TestExtractionCorpusDiagnosticsRetainCompleteSets(t *testing.T) {
	dir := t.TempDir()
	failures := make([]PartialFailure, extractionCorpusDiagnosticExamples+9)
	warnings := make([]ProviderWarning, extractionCorpusDiagnosticExamples+11)
	for i := range failures {
		failures[i] = PartialFailure{
			Code: fmt.Sprintf("E_%03d", i), Severity: "error", FilePath: fmt.Sprintf("failure-%03d.go", i),
			EffectOnCompleteness: "partial", Detail: fmt.Sprintf("failure detail %03d", i),
		}
	}
	for i := range warnings {
		warnings[i] = ProviderWarning{
			Code: fmt.Sprintf("W_%03d", i), Severity: "warning", FilePath: fmt.Sprintf("warning-%03d.go", i),
			EffectOnCompleteness: "none", Detail: fmt.Sprintf("warning detail %03d", i),
		}
	}
	observation := extractionCorpusObservation{
		FormatVersion: 1, ManifestVersion: 1, ManifestPath: "/manifests/request.json",
		Repository: "corpus", RepositoryPath: "/corpora/repository", Operation: "snapshot", Mode: "measure",
		CacheMode: "off", Profile: ProfileFull, ProviderVersion: "provider-test", MutationID: "mutation-7",
		SourceDigest: "source-digest", BinarySHA256: "binary-digest", Scenario: "cold", Trial: 3,
		Verb: "snapshot", Status: "partial", StartedAt: "2026-09-06T12:00:00Z",
		SemanticSHA256: "semantic-digest", SemanticDigest: "semantic-digest", SemanticBytes: 1234,
		PartialFailuresCount: len(failures), PartialFailuresSHA256: extractionCorpusFailureDigest(failures),
		PartialFailures: boundedExtractionCorpusFailures(failures),
		WarningsCount:   len(warnings), WarningsSHA256: extractionCorpusWarningDigest(warnings),
		Warnings: boundedExtractionCorpusWarnings(warnings),
	}
	config := extractionCorpusEvaluationConfig{
		Manifest: extractionCorpusManifest{RepositoryID: "repository-id"}, TopK: 8, MaxIndexedFiles: 400,
		OnlyFiles: []string{"a.go", "b.go"}, OutputPath: filepath.Join(dir, "observation.ndjson"),
		OutputFormat: "ndjson", DiagnosticsPath: filepath.Join(dir, "diagnostics.json"),
	}
	if err := writeExtractionCorpusArtifacts(config, observation, failures, warnings); err != nil {
		t.Fatal(err)
	}

	var diagnostics extractionCorpusDiagnostics
	readExtractionCorpusJSON(t, config.DiagnosticsPath, &diagnostics)
	if len(diagnostics.PartialFailures) != len(failures) || len(diagnostics.Warnings) != len(warnings) {
		t.Fatalf("diagnostic lengths = (%d, %d), want (%d, %d)", len(diagnostics.PartialFailures), len(diagnostics.Warnings), len(failures), len(warnings))
	}
	if got, want := diagnostics.PartialFailures[len(failures)-1].Code, failures[len(failures)-1].Code; got != want {
		t.Fatalf("last failure = %q, want %q", got, want)
	}
	if got, want := diagnostics.Warnings[len(warnings)-1].Code, warnings[len(warnings)-1].Code; got != want {
		t.Fatalf("last warning = %q, want %q", got, want)
	}
	if got := extractionCorpusFailureDigest(diagnostics.PartialFailures); got != observation.PartialFailuresSHA256 || diagnostics.PartialFailuresSHA256 != got {
		t.Fatalf("failure digests = artifact %q recomputed %q observation %q", diagnostics.PartialFailuresSHA256, got, observation.PartialFailuresSHA256)
	}
	if got := extractionCorpusWarningDigest(diagnostics.Warnings); got != observation.WarningsSHA256 || diagnostics.WarningsSHA256 != got {
		t.Fatalf("warning digests = artifact %q recomputed %q observation %q", diagnostics.WarningsSHA256, got, observation.WarningsSHA256)
	}
	if diagnostics.RepositoryID != "repository-id" || diagnostics.SourceDigest != "source-digest" || diagnostics.BinarySHA256 != "binary-digest" || diagnostics.StartedAt != observation.StartedAt {
		t.Fatalf("diagnostic identity = %+v", diagnostics)
	}

	var saved extractionCorpusObservation
	readExtractionCorpusJSON(t, config.OutputPath, &saved)
	if len(saved.PartialFailures) != extractionCorpusDiagnosticExamples || len(saved.Warnings) != extractionCorpusDiagnosticExamples {
		t.Fatalf("bounded observation lengths = (%d, %d)", len(saved.PartialFailures), len(saved.Warnings))
	}
	if saved.PartialFailuresCount != len(failures) || saved.WarningsCount != len(warnings) {
		t.Fatalf("bounded observation counts = (%d, %d)", saved.PartialFailuresCount, saved.WarningsCount)
	}
}

func TestExtractionCorpusDiagnosticsRefuseExistingDestinationAndSaveError(t *testing.T) {
	tests := []struct {
		name        string
		destination func(*testing.T, string) (string, string)
	}{
		{
			name: "regular file",
			destination: func(t *testing.T, dir string) (string, string) {
				path := filepath.Join(dir, "diagnostics.json")
				return path, path
			},
		},
		{
			name: "final symlink",
			destination: func(t *testing.T, dir string) (string, string) {
				target := filepath.Join(dir, "target.json")
				path := filepath.Join(dir, "diagnostics.json")
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
				return path, target
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			diagnosticsPath, protectedPath := test.destination(t, dir)
			if err := os.WriteFile(protectedPath, []byte("sentinel"), 0600); err != nil {
				t.Fatal(err)
			}
			outputPath := filepath.Join(dir, "observation.ndjson")
			observation := extractionCorpusObservation{FormatVersion: 1, Status: "error", Error: "raw request failure", PartialFailuresCount: 1}
			config := extractionCorpusEvaluationConfig{OutputPath: outputPath, OutputFormat: "ndjson", DiagnosticsPath: diagnosticsPath}
			err := writeExtractionCorpusArtifacts(config, observation, []PartialFailure{{Code: "E_RAW"}}, nil)
			if err == nil {
				t.Fatal("expected existing diagnostics destination to fail")
			}
			data, readErr := os.ReadFile(protectedPath)
			if readErr != nil || string(data) != "sentinel" {
				t.Fatalf("protected destination = %q, %v", data, readErr)
			}
			var saved extractionCorpusObservation
			readExtractionCorpusJSON(t, outputPath, &saved)
			if saved.Status != "error" || !strings.Contains(saved.Error, "raw request failure") || !strings.Contains(saved.Error, "diagnostics:") {
				t.Fatalf("saved observation error = status %q error %q", saved.Status, saved.Error)
			}
		})
	}
}

func TestExtractionCorpusDiagnosticsRefuseAliasedOutputPath(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0700); err != nil {
		t.Fatal(err)
	}
	aliasDir := filepath.Join(dir, "alias")
	if err := os.Symlink(realDir, aliasDir); err != nil {
		t.Fatal(err)
	}
	diagnosticsPath := filepath.Join(realDir, "result.json")
	config := extractionCorpusEvaluationConfig{
		OutputPath: filepath.Join(aliasDir, "result.json"), OutputFormat: "ndjson", DiagnosticsPath: diagnosticsPath,
	}
	err := writeExtractionCorpusArtifacts(config, extractionCorpusObservation{FormatVersion: 1, Status: "ok"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "must differ") {
		t.Fatalf("alias collision error = %v", err)
	}
	if _, err := os.Lstat(diagnosticsPath); !os.IsNotExist(err) {
		t.Fatalf("alias collision wrote destination: %v", err)
	}
}

func TestExtractionCorpusDiagnosticsPathResolutionFailureSavesObservation(t *testing.T) {
	dir := t.TempDir()
	diagnosticsPath := filepath.Join(dir, "dangling-diagnostics")
	if err := os.Symlink(filepath.Join(dir, "missing-target"), diagnosticsPath); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(dir, "observation.ndjson")
	config := extractionCorpusEvaluationConfig{OutputPath: outputPath, OutputFormat: "ndjson", DiagnosticsPath: diagnosticsPath}
	observation := extractionCorpusObservation{FormatVersion: 1, Status: "error", Error: "raw request failure"}
	err := writeExtractionCorpusArtifacts(config, observation, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "resolve diagnostics") {
		t.Fatalf("resolution error = %v", err)
	}
	var saved extractionCorpusObservation
	readExtractionCorpusJSON(t, outputPath, &saved)
	if saved.Status != "error" || !strings.Contains(saved.Error, "raw request failure") || !strings.Contains(saved.Error, "diagnostics:") {
		t.Fatalf("saved observation error = status %q error %q", saved.Status, saved.Error)
	}
}

func TestExtractionCorpusDiagnosticsDisabledPreservesObservationOutput(t *testing.T) {
	dir := t.TempDir()
	observation := extractionCorpusObservation{
		FormatVersion: 1, Status: "partial", PartialFailuresCount: 1,
		PartialFailures: []extractionCorpusFailure{{Code: "E_ONE", EffectOnCompleteness: "partial"}},
	}
	expectedPath := filepath.Join(dir, "expected.ndjson")
	if err := writeExtractionCorpusObservation(expectedPath, "ndjson", observation); err != nil {
		t.Fatal(err)
	}
	actualPath := filepath.Join(dir, "actual.ndjson")
	config := extractionCorpusEvaluationConfig{OutputPath: actualPath, OutputFormat: "ndjson"}
	if err := writeExtractionCorpusArtifacts(config, observation, []PartialFailure{{Code: "E_ONE"}}, nil); err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(actualPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(expected) {
		t.Fatalf("disabled diagnostics changed observation:\nactual: %s\nexpected: %s", actual, expected)
	}
}

func TestExtractionCorpusDiagnosticsManifestPath(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(`{"version":1,"repo_path":"repo","cache":"off","diagnostics_path":"artifacts/diagnostics.json"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(extractionCorpusManifestEnv, manifestPath)
	t.Setenv(extractionCorpusConfigEnv, "")
	t.Setenv(extractionCorpusP1ManifestEnv, "")
	config, err := loadExtractionCorpusEvaluationConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "artifacts", "diagnostics.json")
	if config.DiagnosticsPath != want {
		t.Fatalf("diagnostics path = %q, want %q", config.DiagnosticsPath, want)
	}
}

func readExtractionCorpusJSON(t *testing.T, path string, target any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}
