package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/entireio/entire-graph/internal/gitutil"
	"github.com/entireio/entire-graph/internal/intent"
	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

const gpsSchemaVersion = "1.0"

func runSpec(ctx context.Context, opts Options, args []string) error {
	if len(args) == 0 {
		return errors.New("spec requires init, list, show, or validate")
	}
	_, flags, err := gpsFlags(args[1:])
	if err != nil {
		return err
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.repo)
	if err != nil {
		return err
	}
	if args[0] == "init" {
		if flags.head {
			return errors.New("spec init cannot write a --head view")
		}
		return intent.Init(repo)
	}
	set, err := gpsIntent(ctx, repo, flags.head)
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		return gpsEncode(opts, flags.format, map[string]any{"schema_version": gpsSchemaVersion, "intent_digest": set.Digest, "specifications": set.Specs})
	case "show":
		if flags.id == "" {
			return errors.New("spec show requires --id")
		}
		for _, spec := range set.Specs {
			if spec.ID == flags.id {
				return gpsEncode(opts, flags.format, spec)
			}
		}
		return fmt.Errorf("specification %q not found", flags.id)
	case "validate":
		return gpsEncode(opts, flags.format, map[string]any{"schema_version": gpsSchemaVersion, "valid": true, "intent_digest": set.Digest, "specifications": len(set.Specs)})
	default:
		return fmt.Errorf("unknown spec command %q", args[0])
	}
}

func runAnchor(ctx context.Context, opts Options, args []string) error {
	if len(args) == 0 {
		return errors.New("anchor requires bind, list, or resolve")
	}
	verb, flags, err := gpsFlags(args[1:])
	_ = verb
	if err != nil {
		return err
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.repo)
	if err != nil {
		return err
	}
	set, err := gpsIntent(ctx, repo, flags.head)
	if err != nil {
		return err
	}
	if args[0] == "list" {
		return gpsEncode(opts, flags.format, map[string]any{"schema_version": gpsSchemaVersion, "intent_digest": set.Digest, "anchors": set.Bindings})
	}
	if args[0] == "bind" {
		if flags.head {
			return errors.New("anchor bind cannot write a --head view")
		}
		if flags.id == "" || flags.symbol == "" {
			return errors.New("anchor bind requires --id and --symbol")
		}
		snapshot, err := gpsSnapshot(ctx, opts, repo, flags.head)
		if err != nil {
			return err
		}
		matches := matchingSymbols(snapshot.Symbols, flags.symbol, flags.file)
		if len(matches) == 0 {
			return fmt.Errorf("symbol %q not found", flags.symbol)
		}
		if len(matches) > 1 {
			return fmt.Errorf("symbol %q is ambiguous; pass --file", flags.symbol)
		}
		s := matches[0]
		return intent.SaveBinding(repo, intent.Binding{ID: flags.id, SymbolID: s.ID, Selector: intent.Selector{QualifiedName: s.QualifiedName, Kind: s.Kind, File: s.FilePath}, Baseline: intent.Baseline{SignatureHash: intent.Hash(s.Signature), ContainerID: s.ContainerID, BodyHash: s.BodyHash, FileBlob: snapshotFileBlob(snapshot, s.FilePath)}}, flags.update)
	}
	if args[0] == "resolve" {
		if flags.id == "" {
			return errors.New("anchor resolve requires --id")
		}
		snapshot, err := gpsSnapshot(ctx, opts, repo, flags.head)
		if err != nil {
			return err
		}
		for _, binding := range set.Bindings {
			if binding.ID == flags.id {
				return gpsEncode(opts, flags.format, resolveBinding(binding, snapshot))
			}
		}
		return fmt.Errorf("anchor %q not found", flags.id)
	}
	return fmt.Errorf("unknown anchor command %q", args[0])
}

func runGPSContext(ctx context.Context, opts Options, args []string) error {
	_, flags, err := gpsFlags(args)
	if err != nil {
		return err
	}
	if flags.query == "" {
		return errors.New("context requires --query")
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.repo)
	if err != nil {
		return err
	}
	set, err := gpsIntent(ctx, repo, flags.head)
	if err != nil {
		return err
	}
	requirements := matchingRequirements(set, flags.query)
	response := map[string]any{"schema_version": gpsSchemaVersion, "request": flags.query, "intent_digest": set.Digest, "status": "complete", "requirements": requirements, "symbols": []any{}, "dependencies": []any{}, "tests": []any{}, "gaps": []string{}, "budget": map[string]any{"maximum_bytes": flags.maxBytes, "rendered_bytes": 0, "omitted": []string{}}}
	if len(set.Specs) == 0 {
		response["status"] = "complete_with_gaps"
		response["gaps"] = []string{"NO_SPECS"}
		return gpsEncode(opts, flags.format, response)
	}
	snapshot, err := gpsSnapshot(ctx, opts, repo, flags.head)
	if err != nil {
		return err
	}
	selected := make(map[string]bool, len(requirements))
	for _, requirement := range requirements {
		selected[requirement["id"]] = true
	}
	bindings := make(map[string]intent.Binding, len(set.Bindings))
	for _, binding := range set.Bindings {
		bindings[binding.ID] = binding
	}
	var symbols []any
	var dependencies []any
	var tests []any
	var gaps []string
	for _, spec := range set.Specs {
		for _, anchor := range spec.Anchors {
			if !selected[anchor.Requirement] {
				continue
			}
			binding, ok := bindings[anchor.ID]
			if !ok {
				gaps = append(gaps, "UNBOUND_ANCHOR:"+anchor.ID)
				continue
			}
			state := resolveBinding(binding, snapshot)
			if state["state"] != "VALID" {
				gaps = append(gaps, state["state"].(string)+":"+anchor.ID)
				continue
			}
			symbol := state["symbol"].(sem.SymbolRecord)
			symbols = append(symbols, map[string]any{"anchor": anchor.ID, "requirement": anchor.Requirement, "reason": "approved_anchor", "citation": fmt.Sprintf("%s:%d", symbol.FilePath, symbol.StartLine), "symbol": symbol})
			for _, relation := range snapshot.Relations {
				if relation.FromID == symbol.ID {
					dependencies = append(dependencies, map[string]any{"reason": "anchor_callee", "type": relation.Type, "symbol_id": relation.ToID})
				}
				if relation.ToID == symbol.ID {
					dependencies = append(dependencies, map[string]any{"reason": "anchor_caller", "type": relation.Type, "symbol_id": relation.FromID})
				}
			}
		}
		for _, test := range spec.Tests {
			if !acceptanceMatchesRequirement(spec, test.Acceptance, selected) {
				continue
			}
			matches := matchingSymbols(snapshot.Symbols, test.Selector.Name, "")
			if len(matches) == 1 {
				tests = append(tests, map[string]any{"id": test.ID, "acceptance": test.Acceptance, "reason": "declared_mapping", "symbol": matches[0]})
			} else {
				gaps = append(gaps, "DECLARED_TEST_UNRESOLVED:"+test.ID)
			}
		}
	}
	sort.Strings(gaps)
	response["symbols"] = symbols
	response["dependencies"] = dependencies
	response["tests"] = tests
	response["gaps"] = gaps
	if len(gaps) > 0 {
		response["status"] = "complete_with_gaps"
	}
	fitGPSContextBudget(response, flags.maxBytes)
	return gpsEncode(opts, flags.format, response)
}

func runGPSCheck(ctx context.Context, opts Options, args []string) error {
	_, flags, err := gpsFlags(args)
	if err != nil {
		return err
	}
	if flags.base != "" && !flags.head {
		return errors.New("check --base requires --head so code and intent use one committed view")
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.repo)
	if err != nil {
		return err
	}
	set, err := gpsIntent(ctx, repo, flags.head)
	if err != nil {
		return err
	}
	if len(set.Specs) == 0 {
		return gpsEncode(opts, flags.format, map[string]any{"schema_version": gpsSchemaVersion, "disposition": "NOT_CONFIGURED", "findings": []any{}})
	}
	snapshot, err := gpsSnapshot(ctx, opts, repo, flags.head)
	if err != nil {
		return err
	}
	findings := make([]map[string]any, 0)
	if len(snapshot.Header.PartialFailures) != 0 || snapshot.Header.Stats.CompletenessLevel != "ok" {
		findings = append(findings, map[string]any{"id": "GPS-COMPLETENESS-INCOMPLETE", "severity": "incomplete", "subject": "graph", "message": "selected graph has partial or incomplete analysis"})
	}
	if flags.base != "" {
		baseSet, err := intent.LoadRevision(ctx, repo, flags.base)
		if err != nil {
			return fmt.Errorf("load base intent: %w", err)
		}
		changed, err := gitutil.ChangedFiles(ctx, repo, flags.base, "HEAD", nil)
		if err != nil {
			return fmt.Errorf("compare base revision: %w", err)
		}
		if baseSet.Digest != set.Digest {
			findings = append(findings, map[string]any{"id": "GPS-DELTA-INTENT", "severity": "warning", "subject": "intent", "message": "selected intent differs from base revision"})
		}
		for _, file := range changed {
			for _, binding := range set.Bindings {
				if binding.Selector.File == file.Path || binding.Selector.File == file.OldPath {
					findings = append(findings, map[string]any{"id": "GPS-DELTA-ANCHOR", "severity": "warning", "subject": binding.ID, "message": "anchored implementation changed since base revision"})
				}
			}
		}
	}
	for _, spec := range set.Specs {
		for _, anchor := range spec.Anchors {
			found := false
			for _, binding := range set.Bindings {
				if binding.ID != anchor.ID {
					continue
				}
				found = true
				state := resolveBinding(binding, snapshot)
				if state["state"] != "VALID" {
					findings = append(findings, map[string]any{"id": "GPS-ANCHOR-" + state["state"].(string), "severity": "warning", "subject": anchor.ID, "message": state["state"]})
				}
			}
			if !found {
				findings = append(findings, map[string]any{"id": "GPS-ANCHOR-UNBOUND", "severity": "warning", "subject": anchor.ID, "message": "declared anchor has no reviewed binding"})
			}
		}
		for _, acceptance := range spec.Acceptance {
			mapped := false
			for _, test := range spec.Tests {
				if test.Acceptance == acceptance.ID {
					mapped = true
					matches := matchingSymbols(snapshot.Symbols, test.Selector.Name, "")
					if len(matches) != 1 {
						findings = append(findings, map[string]any{"id": "GPS-MAPPING-UNRESOLVED", "severity": "warning", "subject": test.ID, "message": "declared test selector does not resolve to exactly one symbol"})
					}
				}
			}
			if !mapped {
				findings = append(findings, map[string]any{"id": "GPS-MAPPING-MISSING", "severity": "warning", "subject": acceptance.ID, "message": "acceptance criterion has no declared test"})
			}
		}
	}
	disposition := "PASS"
	for _, finding := range findings {
		if finding["severity"] == "incomplete" {
			disposition = "INCOMPLETE"
			break
		}
		if finding["severity"] == "error" {
			disposition = "FAIL"
			break
		}
		disposition = "REVIEW_REQUIRED"
	}
	return gpsEncode(opts, flags.format, map[string]any{"schema_version": gpsSchemaVersion, "intent_digest": set.Digest, "disposition": disposition, "findings": findings})
}

type gpsOptions struct {
	repo, format, id, symbol, file, query, base string
	update                                      bool
	head                                        bool
	maxBytes                                    int
}

func gpsFlags(args []string) (string, gpsOptions, error) {
	flags := gpsOptions{format: "json", maxBytes: 12000}
	for i := 0; i < len(args); i++ {
		value := func() (string, error) {
			i++
			if i >= len(args) {
				return "", fmt.Errorf("%s requires a value", args[i-1])
			}
			return args[i], nil
		}
		switch args[i] {
		case "--repo", "--format", "--id", "--symbol", "--file", "--query", "--base", "--max-context-bytes":
			v, err := value()
			if err != nil {
				return "", flags, err
			}
			switch args[i-1] {
			case "--repo":
				flags.repo = v
			case "--format":
				flags.format = v
			case "--id":
				flags.id = v
			case "--symbol":
				flags.symbol = v
			case "--file":
				flags.file = v
			case "--query":
				flags.query = v
			case "--base":
				flags.base = v
			default:
				if _, err := fmt.Sscan(v, &flags.maxBytes); err != nil || flags.maxBytes < 1 {
					return "", flags, errors.New("--max-context-bytes requires a positive integer")
				}
			}
		case "--head":
			flags.head = true
		case "--update":
			flags.update = true
		case "--json":
			flags.format = "json"
		default:
			return "", flags, fmt.Errorf("unexpected argument %q", args[i])
		}
	}
	if flags.format != "json" && flags.format != "text" {
		return "", flags, errors.New("--format must be json or text")
	}
	return "", flags, nil
}
func gpsIntent(ctx context.Context, repo string, head bool) (intent.Set, error) {
	if head {
		return intent.LoadRevision(ctx, repo, "HEAD")
	}
	return intent.Load(repo)
}

func gpsSnapshot(ctx context.Context, opts Options, repo string, head bool) (sem.ProviderSnapshot, error) {
	snapshot, _, err := sem.LoadOrBuildProviderSnapshot(ctx, repo, opts.Version, sem.ProviderSnapshotOptions{NoNetwork: true, Worktree: !head, Profile: sem.ProfileFull}, "", true)
	return snapshot, err
}
func matchingSymbols(symbols []sem.SymbolRecord, name, file string) []sem.SymbolRecord {
	var out []sem.SymbolRecord
	for _, s := range symbols {
		if (s.QualifiedName == name || s.Name == name || s.ID == name) && (file == "" || s.FilePath == file) {
			out = append(out, s)
		}
	}
	return out
}
func resolveBinding(binding intent.Binding, snapshot sem.ProviderSnapshot) map[string]any {
	for _, s := range snapshot.Symbols {
		if s.ID != binding.SymbolID {
			continue
		}
		state := "VALID"
		if binding.Baseline.SignatureHash != intent.Hash(s.Signature) || binding.Baseline.ContainerID != s.ContainerID {
			state = "STRUCTURAL_DRIFT"
		} else if binding.Selector.File != s.FilePath || binding.Selector.Kind != s.Kind {
			state = "STRUCTURAL_DRIFT"
		} else if binding.Baseline.BodyHash != s.BodyHash {
			state = "CONTENT_DRIFT"
		}
		return map[string]any{"id": binding.ID, "state": state, "symbol": s}
	}
	candidates := matchingSymbols(snapshot.Symbols, binding.Selector.QualifiedName, binding.Selector.File)
	state := "MISSING"
	if len(candidates) > 1 {
		state = "AMBIGUOUS"
	} else if len(candidates) == 1 {
		state = "CANDIDATE_REBIND"
	} else if incompleteAnchorPath(binding.Selector.File, snapshot.Header.PartialFailures) {
		state = "UNVERIFIABLE"
	}
	return map[string]any{"id": binding.ID, "state": state, "candidates": candidates}
}

func snapshotFileBlob(snapshot sem.ProviderSnapshot, path string) string {
	for _, file := range snapshot.Files {
		if file.Path == path {
			return file.Blob
		}
	}
	return ""
}

func incompleteAnchorPath(path string, failures []sem.PartialFailure) bool {
	for _, failure := range failures {
		if failure.FilePath == path || failure.FilePath == "" {
			return true
		}
	}
	return false
}
func matchingRequirements(set intent.Set, query string) []map[string]string {
	words := strings.Fields(strings.ToLower(query))
	var out []map[string]string
	for _, s := range set.Specs {
		for _, r := range s.Requirements {
			text := strings.ToLower(s.Title + " " + s.Intent + " " + r.ID + " " + r.Description)
			for _, word := range words {
				if strings.Contains(text, word) {
					out = append(out, map[string]string{"specification": s.ID, "id": r.ID, "description": r.Description, "reason": "lexical_match"})
					break
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["id"] < out[j]["id"] })
	return out
}

func acceptanceMatchesRequirement(spec intent.Spec, acceptanceID string, requirements map[string]bool) bool {
	for _, acceptance := range spec.Acceptance {
		if acceptance.ID == acceptanceID {
			return requirements[acceptance.Requirement]
		}
	}
	return false
}

// fitGPSContextBudget preserves direct intent first and falls back to an explicit
// minimal manifest when even that evidence cannot fit the caller's budget.
func fitGPSContextBudget(response map[string]any, maximum int) {
	budget := response["budget"].(map[string]any)
	if renderedGPSJSONBytes(response) <= maximum {
		budget["rendered_bytes"] = renderedGPSJSONBytes(response)
		return
	}
	response["symbols"] = []any{}
	response["dependencies"] = []any{}
	response["tests"] = []any{}
	budget["omitted"] = []string{"symbols", "dependencies", "tests"}
	if renderedGPSJSONBytes(response) > maximum {
		response["requirements"] = []map[string]string{}
		response["status"] = "BUDGET_TOO_SMALL"
		response["gaps"] = []string{"BUDGET_TOO_SMALL"}
		budget["omitted"] = []string{"requirements", "symbols", "dependencies", "tests"}
	}
	budget["rendered_bytes"] = renderedGPSJSONBytes(response)
}

func renderedGPSJSONBytes(value any) int {
	var out bytes.Buffer
	encoder := json.NewEncoder(termsafe.NewJSONWriter(&out))
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return 0
	}
	return out.Len()
}

func gpsEncode(opts Options, format string, value any) error {
	if format == "json" {
		encoder := json.NewEncoder(termsafe.NewJSONWriter(opts.Stdout))
		encoder.SetEscapeHTML(false)
		return encoder.Encode(value)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err == nil {
		_, err = fmt.Fprintln(opts.Stdout, string(data))
	}
	return err
}
