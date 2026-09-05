package sem

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
)

const operationManifestLimit = 256

// OperationInputManifest describes observed inputs, not an atomic worktree.
// ID covers every observation even when Observations is output-bounded.
type OperationInputManifest struct {
	PolicyCoverage      string                      `json:"policy_coverage"`
	ID                  string                      `json:"id"`
	Version             int                         `json:"version"`
	Coverage            string                      `json:"coverage"`
	SelectedPathsDigest string                      `json:"selected_paths_digest"`
	SelectedPaths       int                         `json:"selected_paths"`
	ObservedInputs      int                         `json:"observed_inputs"`
	UnobservedSelected  int                         `json:"unobserved_selected"`
	UnavailableInputs   int                         `json:"unavailable_inputs"`
	ObservationsOmitted int                         `json:"observations_omitted"`
	Observations        []OperationInputObservation `json:"observations"`
}
type OperationInputObservation struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Digest string `json:"digest,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
}

func operationCaptureIdentity(options ProviderSnapshotOptions, repo, key, commit, tree string, warnings []ProviderWarning) []string {
	if !(options.ExtractionReuse || options.Compiler != nil || options.captureInputs) {
		return nil
	}
	view := "committed"
	if options.Worktree {
		view = "working-tree"
	} else if commit == "" {
		view = "working-tree-fallback"
	}
	fields := []string{"operation-input-v1", repo, key, view, commit, tree, string(resolveProfile(options.Profile).name), strconv.Itoa(resolveMaxParseBytes(options.MaxParseBytes)), strconv.Itoa(resolveMaxSourceFiles(options.MaxFiles))}
	if policy := options.cachePolicy; policy != nil {
		add := func(kind string, items []capturedIgnoreFile) {
			fields = append(fields, kind, strconv.Itoa(len(items)))
			for _, item := range items {
				fields = append(fields, item.path, strconv.FormatBool(item.present), contentHash(item.content))
			}
		}
		add("graphignore", []capturedIgnoreFile{policy.graphIgnore})
		add("ignore", policy.ignoreFiles)
		add("include", policy.includeFiles)
	}
	data, _ := json.Marshal(warnings)
	fields = append(fields, string(data))
	return fields
}

func (source sourceContext) finishCapture(selected []string) (*OperationInputManifest, error) {
	store := source.capture
	if store == nil {
		return nil, nil
	}
	if err := store.ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.failure != nil {
		return nil, errors.New("E_CAPTURE_IO: captured input storage failed; operation incomplete")
	}
	observations := make([]OperationInputObservation, 0, len(store.entries))
	seen := map[string]bool{}
	for path, entry := range store.entries {
		select {
		case <-entry.ready:
		default:
			return nil, errors.New("E_CAPTURE_INCOMPLETE: source acquisition is still active")
		}
		if entry.err != nil {
			return nil, errors.New("E_CAPTURE_IO: captured input storage failed; operation incomplete")
		}
		item := OperationInputObservation{Path: path, Status: "unavailable"}
		if entry.ok {
			item.Status = "captured"
			item.Digest = entry.source.digest
			item.Bytes = entry.size
		} else if entry.policy {
			item.Status = "absent-policy"
		} else if over, ok := source.oversizeAt(path); ok {
			item.Status = "oversized"
			item.Digest = over.Hash
			item.Bytes = over.Bytes
		}
		observations = append(observations, item)
		seen[path] = true
	}
	sort.Slice(observations, func(i, j int) bool { return observations[i].Path < observations[j].Path })
	manifest := &OperationInputManifest{Version: 1, Coverage: "observed-inputs-only; not an atomic revision", PolicyCoverage: "captured provider policy bytes including nested/vendor rules; Git listing/configuration and metadata probes are opaque", SelectedPaths: len(selected), SelectedPathsDigest: extractionIdentity(selected...), ObservedInputs: len(observations)}
	fields := append([]string(nil), source.captureIdentity...)
	fields = append(fields, "effective-matcher", strconv.Itoa(len(source.ignores.rules)))
	for _, rule := range source.ignores.rules {
		expression := ""
		if rule.expression != nil {
			expression = rule.expression.String()
		}
		fields = append(fields, strconv.FormatBool(rule.ignore), strconv.FormatBool(rule.includeFile), strconv.FormatBool(rule.directory), strconv.FormatBool(rule.fileOnly), strconv.FormatBool(rule.basenameOnly), rule.pattern, expression)
	}
	fields = append(fields, "selected", manifest.SelectedPathsDigest, strconv.Itoa(len(selected)))
	for _, path := range selected {
		if !seen[path] {
			manifest.UnobservedSelected++
		}
	}
	for _, item := range observations {
		fields = append(fields, item.Path, item.Status, item.Digest, strconv.FormatInt(item.Bytes, 10))
		if item.Status == "unavailable" {
			manifest.UnavailableInputs++
		}
	}
	manifest.ID = extractionIdentity(fields...)
	manifest.ObservationsOmitted = max(0, len(observations)-operationManifestLimit)
	manifest.Observations = observations[:min(len(observations), operationManifestLimit)]
	return manifest, nil
}

func capturedSelectedPaths(paths, only []string) []string {
	if len(only) == 0 {
		return paths
	}
	allowed := make(map[string]bool, len(only))
	for _, path := range only {
		allowed[path] = true
	}
	selected := make([]string, 0, len(only))
	for _, path := range paths {
		if allowed[path] {
			selected = append(selected, path)
		}
	}
	return selected
}
