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

const minimumGPSContextBytes = 512

func runSpec(ctx context.Context, opts Options, args []string) error {
	if len(args) == 0 {
		return errors.New("spec requires init, list, show, validate, or relationships")
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
	switch args[0] {
	case "validate":
		if flags.head {
			set, err := gpsIntent(ctx, repo, true)
			if err != nil {
				return err
			}
			return gpsEncode(opts, flags.format, map[string]any{"schema_version": gpsSchemaVersion, "valid": true, "intent_digest": set.Digest, "specifications": len(set.Specs), "diagnostics": []intent.Diagnostic{}})
		}
		diagnostics, err := intent.Validate(repo)
		if err != nil {
			return err
		}
		return gpsEncode(opts, flags.format, map[string]any{"schema_version": gpsSchemaVersion, "valid": len(diagnostics) == 0, "diagnostics": diagnostics})
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
	case "relationships":
		if flags.id == "" {
			return errors.New("spec relationships requires --id")
		}
		for _, spec := range set.Specs {
			if spec.ID == flags.id {
				return gpsEncode(opts, flags.format, map[string]any{"schema_version": gpsSchemaVersion, "id": spec.ID, "relationships": spec.Relationships})
			}
		}
		return fmt.Errorf("specification %q not found", flags.id)
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
	view, err := gpsCaptureView(ctx, repo, flags.head)
	if err != nil {
		return err
	}
	set, err := gpsIntentRevision(ctx, repo, view.revision, flags.head)
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
		return intent.SaveBinding(repo, intent.Binding{ID: flags.id, SymbolID: s.ID, Selector: intent.Selector{QualifiedName: s.QualifiedName, Kind: s.Kind, File: s.FilePath}, Baseline: intent.Baseline{Version: 1, SignatureHash: intent.Hash(s.Signature), ContainerID: s.ContainerID, BodyHash: s.BodyHash, FileBlob: snapshotFileBlob(snapshot, s.FilePath)}}, flags.update)
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
	if flags.maxBytes < minimumGPSContextBytes {
		return fmt.Errorf("context --max-context-bytes must be at least %d", minimumGPSContextBytes)
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.repo)
	if err != nil {
		return err
	}
	view, err := gpsCaptureView(ctx, repo, flags.head)
	if err != nil {
		return err
	}
	set, err := gpsIntentRevision(ctx, repo, view.revision, flags.head)
	if err != nil {
		return err
	}
	requirements := matchingRequirements(set, flags.query)
	response := map[string]any{"schema_version": gpsSchemaVersion, "request": flags.query, "intent_digest": set.Digest, "repository_view": view.repositoryView(), "status": "complete", "requirements": requirements, "symbols": []any{}, "code": []any{}, "dependencies": []any{}, "tests": []any{}, "inferred_tests": []any{}, "gaps": []string{}, "budget": map[string]any{"maximum_bytes": flags.maxBytes, "rendered_bytes": 0, "omitted": []string{}}}
	if len(set.Specs) == 0 {
		response["status"] = "complete_with_gaps"
		response["gaps"] = []string{"NO_SPECS"}
	}
	snapshot, err := gpsSnapshotRevision(ctx, opts, repo, view.revision, flags.head)
	if err != nil {
		return err
	}
	search, err := sem.SearchRepository(ctx, repo, opts.Version, flags.query, sem.SearchOptions{Worktree: !flags.head, Profile: sem.ProfileFull, TopK: 5, MaxContextBytes: flags.maxBytes, DisableCache: true})
	if err != nil {
		return err
	}
	code := make([]any, 0, len(search.Results))
	for _, result := range search.Results {
		// A bounded snippet leaves quota for inferred tests instead of letting one
		// search result consume the entire source-evidence section.
		code = append(code, map[string]any{"reason": "ranked_code_search", "rank": result.Rank, "score": result.Score, "citation": fmt.Sprintf("%s:%d", result.FilePath, result.FocusLine), "symbol_id": result.SymbolID, "snippet": trimUTF8(result.Snippet, 160), "snippet_start_line": result.SnippetStartLine, "snippet_end_line": result.SnippetEndLine})
	}
	response["code"] = code
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
	var inferredTests []any
	gaps := response["gaps"].([]string)
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
	for _, symbol := range snapshot.Symbols {
		if !strings.HasPrefix(symbol.Name, "Test") || !queryMatchesText(flags.query, symbol.Name) {
			continue
		}
		inferredTests = append(inferredTests, map[string]any{"reason": "name_match_candidate", "fulfills_mapping": false, "citation": fmt.Sprintf("%s:%d", symbol.FilePath, symbol.StartLine), "symbol_id": symbol.ID, "name": symbol.QualifiedName})
	}
	sort.Strings(gaps)
	response["symbols"] = symbols
	response["dependencies"] = dependencies
	response["tests"] = tests
	response["inferred_tests"] = inferredTests
	response["gaps"] = gaps
	if len(gaps) > 0 {
		response["status"] = "complete_with_gaps"
	}
	fitGPSContextBudget(response, flags.maxBytes)
	if view.inputChanged(ctx, repo) {
		response["status"] = "incomplete"
		response["gaps"] = append(response["gaps"].([]string), "INPUT_CHANGED")
		setGPSRenderedBytes(response)
	}
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
	view, err := gpsCaptureView(ctx, repo, flags.head)
	if err != nil {
		return err
	}
	set, err := gpsIntentRevision(ctx, repo, view.revision, flags.head)
	if err != nil {
		return err
	}
	if len(set.Specs) == 0 {
		response := map[string]any{"schema_version": gpsSchemaVersion, "repository_view": view.repositoryView(), "change_delta": "not_requested", "disposition": "NOT_CONFIGURED", "findings": []any{}}
		if view.inputChanged(ctx, repo) {
			response["disposition"] = "INCOMPLETE"
			response["findings"] = []map[string]any{{"id": "GPS-INPUT-CHANGED", "severity": "incomplete", "subject": "repository", "message": "HEAD changed while GPS was reading committed inputs"}}
		}
		return gpsEncode(opts, flags.format, response)
	}
	snapshot, err := gpsSnapshotRevision(ctx, opts, repo, view.revision, flags.head)
	if err != nil {
		return err
	}
	findings := make([]map[string]any, 0)
	if len(snapshot.Header.PartialFailures) != 0 || snapshot.Header.Stats.CompletenessLevel != "ok" {
		findings = append(findings, map[string]any{"id": "GPS-COMPLETENESS-INCOMPLETE", "severity": "incomplete", "subject": "graph", "message": "selected graph has partial or incomplete analysis"})
	}
	if flags.base != "" {
		baseRevision, err := gitutil.RevParse(ctx, repo, flags.base)
		if err != nil {
			return fmt.Errorf("resolve base revision: %w", err)
		}
		headRevision := view.revision
		baseSet, err := intent.LoadRevision(ctx, repo, baseRevision)
		if err != nil {
			return fmt.Errorf("load base intent: %w", err)
		}
		changed, err := gitutil.ChangedFiles(ctx, repo, baseRevision, headRevision, nil)
		if err != nil {
			return fmt.Errorf("compare base revision: %w", err)
		}
		if baseSet.Digest != set.Digest {
			findings = append(findings, map[string]any{"id": "GPS-DELTA-INTENT", "severity": "warning", "subject": "intent", "message": "selected intent differs from base revision"})
		}
		currentTests := map[string]bool{}
		for _, spec := range set.Specs {
			for _, test := range spec.Tests {
				currentTests[test.ID] = true
			}
		}
		for _, spec := range baseSet.Specs {
			for _, test := range spec.Tests {
				if !currentTests[test.ID] {
					findings = append(findings, map[string]any{"id": "GPS-DELTA-MAPPING-REMOVED", "severity": "warning", "subject": test.ID, "message": "declared test mapping was removed since base revision"})
				}
			}
		}
		intentChanged := false
		for _, file := range changed {
			if strings.HasPrefix(file.Path, intent.Root+"/") || strings.HasPrefix(file.OldPath, intent.Root+"/") {
				intentChanged = true
			}
		}
		baseSnapshot, err := gpsSnapshotRevision(ctx, opts, repo, baseRevision, true)
		if err != nil {
			return fmt.Errorf("load base graph: %w", err)
		}
		findings = append(findings, gpsSymbolDeltaFindings(baseSet, set, baseSnapshot, snapshot)...)
		codeChanged := gpsSnapshotsHaveCodeDelta(baseSnapshot, snapshot)
		if intentChanged && !codeChanged {
			findings = append(findings, map[string]any{"id": "GPS-DELTA-SPEC-ONLY", "severity": "warning", "subject": "intent", "message": "specification changed without implementation changes"})
		}
		if codeChanged && !intentChanged {
			findings = append(findings, map[string]any{"id": "GPS-DELTA-CODE-ONLY", "severity": "warning", "subject": "code", "message": "implementation changed without specification changes"})
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
	changeDelta := "not_requested"
	if flags.base != "" {
		changeDelta = "compared"
	}
	if view.inputChanged(ctx, repo) {
		findings = append(findings, map[string]any{"id": "GPS-INPUT-CHANGED", "severity": "incomplete", "subject": "repository", "message": "HEAD changed while GPS was reading committed inputs"})
		disposition = "INCOMPLETE"
	}
	response := map[string]any{"schema_version": gpsSchemaVersion, "intent_digest": set.Digest, "repository_view": view.repositoryView(), "change_delta": changeDelta, "disposition": disposition, "findings": findings}
	if flags.evidence != "" {
		response["execution_evidence"] = gpsExecutionEvidence(flags.evidence, set)
	}
	return gpsEncode(opts, flags.format, response)
}

func runGPSWhy(ctx context.Context, opts Options, args []string) error {
	_, flags, err := gpsFlags(args)
	if err != nil {
		return err
	}
	if flags.symbol == "" {
		return errors.New("why requires --symbol")
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.repo)
	if err != nil {
		return err
	}
	view, err := gpsCaptureView(ctx, repo, flags.head)
	if err != nil {
		return err
	}
	set, err := gpsIntentRevision(ctx, repo, view.revision, flags.head)
	if err != nil {
		return err
	}
	snapshot, err := gpsSnapshotRevision(ctx, opts, repo, view.revision, flags.head)
	if err != nil {
		return err
	}
	matches := matchingSymbols(snapshot.Symbols, flags.symbol, flags.file)
	if len(matches) != 1 {
		if len(matches) == 0 {
			return fmt.Errorf("symbol %q not found", flags.symbol)
		}
		return fmt.Errorf("symbol %q is ambiguous; pass --file", flags.symbol)
	}
	symbol := matches[0]
	bindings := map[string]bool{}
	for _, binding := range set.Bindings {
		if binding.SymbolID == symbol.ID {
			bindings[binding.ID] = true
		}
	}
	var requirements []map[string]string
	var tests []map[string]string
	var decisions []intent.Decision
	for _, spec := range set.Specs {
		for _, anchor := range spec.Anchors {
			if bindings[anchor.ID] {
				requirements = append(requirements, map[string]string{"specification": spec.ID, "id": anchor.Requirement, "anchor": anchor.ID, "authority": "developer_confirmed"})
			}
		}
		for _, test := range spec.Tests {
			for _, acceptance := range spec.Acceptance {
				if test.Acceptance != acceptance.ID {
					continue
				}
				for _, requirement := range requirements {
					if requirement["specification"] == spec.ID && requirement["id"] == acceptance.Requirement {
						tests = append(tests, map[string]string{"id": test.ID, "acceptance": test.Acceptance, "selector": test.Selector.Name, "authority": "declared"})
					}
				}
			}
		}
	}
	for _, decision := range set.Decisions {
		for _, anchor := range decision.Anchors {
			if bindings[anchor] {
				decisions = append(decisions, decision)
				break
			}
		}
	}
	status := "complete"
	gaps := []string{}
	if len(requirements) == 0 {
		status, gaps = "complete_with_gaps", []string{"NO_INTENT_LINK"}
	}
	response := map[string]any{"schema_version": gpsSchemaVersion, "status": status, "symbol": symbol, "requirements": requirements, "tests": tests, "decisions": decisions, "gaps": gaps, "repository_view": view.repositoryView()}
	if flags.history {
		history, err := gitutil.HistoryForPath(ctx, repo, view.revision, symbol.FilePath, flags.historyLimit)
		if err != nil {
			response["history"] = map[string]any{"status": "HISTORY_UNAVAILABLE"}
			response["status"] = "complete_with_gaps"
			response["gaps"] = append(gaps, "HISTORY_UNAVAILABLE")
		} else {
			response["history"] = map[string]any{"status": "AVAILABLE", "entries": history}
		}
	}
	if view.inputChanged(ctx, repo) {
		response["status"] = "incomplete"
		response["gaps"] = append(response["gaps"].([]string), "INPUT_CHANGED")
	}
	return gpsEncode(opts, flags.format, response)
}

func runGPSReview(ctx context.Context, opts Options, args []string) error {
	_, flags, err := gpsFlags(args)
	if err != nil {
		return err
	}
	if flags.base == "" {
		return errors.New("review requires --base")
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.repo)
	if err != nil {
		return err
	}
	view, err := gpsCaptureView(ctx, repo, true)
	if err != nil {
		return err
	}
	base, err := gitutil.RevParse(ctx, repo, flags.base)
	if err != nil {
		return fmt.Errorf("resolve base revision: %w", err)
	}
	set, err := intent.LoadRevision(ctx, repo, view.revision)
	if err != nil {
		return err
	}
	baseSet, err := intent.LoadRevision(ctx, repo, base)
	if err != nil {
		return err
	}
	changed, err := gitutil.ChangedFiles(ctx, repo, base, view.revision, nil)
	if err != nil {
		return err
	}
	baseSnapshot, err := gpsSnapshotRevision(ctx, opts, repo, base, true)
	if err != nil {
		return err
	}
	headSnapshot, err := gpsSnapshotRevision(ctx, opts, repo, view.revision, true)
	if err != nil {
		return err
	}
	deltas := gpsSymbolDeltaFindings(baseSet, set, baseSnapshot, headSnapshot)
	anchors := map[string]string{}
	for _, delta := range deltas {
		if delta["id"] != "GPS-DELTA-ANCHOR" {
			continue
		}
		id, _ := delta["subject"].(string)
		for _, binding := range set.Bindings {
			if binding.ID == id {
				anchors[id] = binding.Selector.File
			}
		}
	}
	var requirements []map[string]string
	var tests []intent.TestRef
	for _, spec := range set.Specs {
		for _, anchor := range spec.Anchors {
			if _, ok := anchors[anchor.ID]; ok {
				requirements = append(requirements, map[string]string{"specification": spec.ID, "id": anchor.Requirement, "anchor": anchor.ID, "file": anchors[anchor.ID]})
				for _, acceptance := range spec.Acceptance {
					if acceptance.Requirement == anchor.Requirement {
						for _, test := range spec.Tests {
							if test.Acceptance == acceptance.ID {
								tests = append(tests, test)
							}
						}
					}
				}
			}
		}
	}
	response := map[string]any{"schema_version": gpsSchemaVersion, "base": base, "repository_view": view.repositoryView(), "changed_files": changed, "symbol_deltas": deltas, "requirements": requirements, "tests": tests, "disposition": func() string {
		if len(requirements) > 0 {
			return "REVIEW_REQUIRED"
		}
		return "PASS"
	}()}
	if view.inputChanged(ctx, repo) {
		response["disposition"] = "INCOMPLETE"
		response["findings"] = []map[string]any{{"id": "GPS-INPUT-CHANGED", "severity": "incomplete", "subject": "repository", "message": "HEAD changed while GPS was reading committed inputs"}}
	}
	if flags.evidence != "" {
		response["execution_evidence"] = gpsExecutionEvidence(flags.evidence, set)
	}
	return gpsEncode(opts, flags.format, response)
}

type gpsOptions struct {
	repo, format, id, symbol, file, query, base, evidence string
	update                                                bool
	head                                                  bool
	history                                               bool
	historyLimit                                          int
	maxBytes                                              int
}

func gpsFlags(args []string) (string, gpsOptions, error) {
	flags := gpsOptions{format: "json", maxBytes: 12000, historyLimit: 16}
	for i := 0; i < len(args); i++ {
		value := func() (string, error) {
			i++
			if i >= len(args) {
				return "", fmt.Errorf("%s requires a value", args[i-1])
			}
			return args[i], nil
		}
		switch args[i] {
		case "--repo", "--format", "--id", "--symbol", "--file", "--query", "--base", "--evidence", "--max-context-bytes", "--history-limit":
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
			case "--evidence":
				flags.evidence = v
			case "--history-limit":
				if _, err := fmt.Sscan(v, &flags.historyLimit); err != nil || flags.historyLimit < 1 || flags.historyLimit > 32 {
					return "", flags, errors.New("--history-limit requires an integer from 1 through 32")
				}
			default:
				if _, err := fmt.Sscan(v, &flags.maxBytes); err != nil || flags.maxBytes < 1 {
					return "", flags, errors.New("--max-context-bytes requires a positive integer")
				}
			}
		case "--head":
			flags.head = true
		case "--update":
			flags.update = true
		case "--history":
			flags.history = true
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

func gpsExecutionEvidence(path string, set intent.Set) map[string]any {
	baseline, err := readVerifyBaseline(path)
	if err != nil {
		return map[string]any{"path": path, "status": "UNAVAILABLE"}
	}
	if baseline.IntentDigest != "" && baseline.IntentDigest != set.Digest {
		return map[string]any{"path": path, "scope": baseline.Scope, "status": "STALE"}
	}
	for _, result := range baseline.Results {
		if result != verifyStatusPass {
			return map[string]any{"path": path, "scope": baseline.Scope, "status": "FAILED"}
		}
	}
	return map[string]any{"path": path, "scope": baseline.Scope, "status": "CURRENT"}
}
func gpsIntent(ctx context.Context, repo string, head bool) (intent.Set, error) {
	if head {
		return intent.LoadRevision(ctx, repo, "HEAD")
	}
	return intent.Load(repo)
}

type gpsView struct {
	head     bool
	revision string
	tree     string
}

func gpsCaptureView(ctx context.Context, repo string, head bool) (gpsView, error) {
	if !head {
		return gpsView{}, nil
	}
	commit, tree, err := gitutil.HeadCommitAndTree(ctx, repo)
	if err != nil {
		return gpsView{}, fmt.Errorf("capture committed GPS inputs: %w", err)
	}
	return gpsView{head: true, revision: commit, tree: tree}, nil
}

func (view gpsView) repositoryView() map[string]string {
	if !view.head {
		return map[string]string{"kind": "working_tree"}
	}
	return map[string]string{"kind": "committed", "commit": view.revision, "tree": view.tree}
}

func (view gpsView) inputChanged(ctx context.Context, repo string) bool {
	if !view.head {
		return false
	}
	commit, tree, err := gitutil.HeadCommitAndTree(ctx, repo)
	return err != nil || commit != view.revision || tree != view.tree
}

func gpsIntentRevision(ctx context.Context, repo, revision string, head bool) (intent.Set, error) {
	if head {
		return intent.LoadRevision(ctx, repo, revision)
	}
	return intent.Load(repo)
}

func gpsRepositoryView(ctx context.Context, repo string, head bool) map[string]string {
	if !head {
		return map[string]string{"kind": "working_tree"}
	}
	commit, tree, err := gitutil.HeadCommitAndTree(ctx, repo)
	if err != nil {
		return map[string]string{"kind": "committed", "status": "unavailable"}
	}
	return map[string]string{"kind": "committed", "commit": commit, "tree": tree}
}

func gpsSnapshot(ctx context.Context, opts Options, repo string, head bool) (sem.ProviderSnapshot, error) {
	snapshot, _, err := sem.LoadOrBuildProviderSnapshot(ctx, repo, opts.Version, sem.ProviderSnapshotOptions{NoNetwork: true, Worktree: !head, Profile: sem.ProfileFull}, "", true)
	return snapshot, err
}

func gpsSnapshotRevision(ctx context.Context, opts Options, repo, revision string, head bool) (sem.ProviderSnapshot, error) {
	if !head {
		return gpsSnapshot(ctx, opts, repo, false)
	}
	return sem.BuildProviderSnapshotWithOptions(ctx, repo, opts.Version, sem.ProviderSnapshotOptions{NoNetwork: true, Revision: revision, Profile: sem.ProfileFull})
}

func gpsSnapshotsHaveCodeDelta(base, head sem.ProviderSnapshot) bool {
	baseSymbols, headSymbols := gpsCodeSymbolsByID(base.Symbols), gpsCodeSymbolsByID(head.Symbols)
	if len(baseSymbols) != len(headSymbols) {
		return true
	}
	for id, before := range baseSymbols {
		after, ok := headSymbols[id]
		if !ok || !gpsSameSymbol(before, after) {
			return true
		}
	}
	return false
}

func gpsCodeSymbolsByID(symbols []sem.SymbolRecord) map[string]sem.SymbolRecord {
	out := map[string]sem.SymbolRecord{}
	for _, symbol := range symbols {
		if !strings.HasPrefix(symbol.FilePath, intent.Root+"/") {
			out[symbol.ID] = symbol
		}
	}
	return out
}

func gpsSymbolDeltaFindings(baseSet, headSet intent.Set, base, head sem.ProviderSnapshot) []map[string]any {
	findings := []map[string]any{}
	baseBindings, headBindings := gpsBindingsByID(baseSet.Bindings), gpsBindingsByID(headSet.Bindings)
	for _, id := range gpsSortedBindingIDs(baseBindings, headBindings) {
		before, beforeOK := baseBindings[id]
		after, afterOK := headBindings[id]
		if !beforeOK || !afterOK || !gpsSameBoundSymbol(before, after, base, head) {
			findings = append(findings, map[string]any{"id": "GPS-DELTA-ANCHOR", "severity": "warning", "subject": id, "message": "anchored implementation changed or was deleted since base revision"})
		}
	}
	baseTests, headTests := gpsTestsByID(baseSet), gpsTestsByID(headSet)
	for _, id := range gpsSortedTestIDs(baseTests, headTests) {
		before, beforeOK := baseTests[id]
		after, afterOK := headTests[id]
		if beforeOK && afterOK && !gpsSameSelectedSymbols(matchingSymbols(base.Symbols, before.Selector.Name, ""), matchingSymbols(head.Symbols, after.Selector.Name, "")) {
			findings = append(findings, map[string]any{"id": "GPS-DELTA-TEST", "severity": "warning", "subject": id, "message": "declared test implementation changed or was deleted since base revision"})
		}
	}
	return findings
}

func gpsBindingsByID(bindings []intent.Binding) map[string]intent.Binding {
	out := make(map[string]intent.Binding, len(bindings))
	for _, binding := range bindings {
		out[binding.ID] = binding
	}
	return out
}

func gpsSortedBindingIDs(left, right map[string]intent.Binding) []string {
	ids := map[string]bool{}
	for id := range left {
		ids[id] = true
	}
	for id := range right {
		ids[id] = true
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func gpsTestsByID(set intent.Set) map[string]intent.TestRef {
	out := map[string]intent.TestRef{}
	for _, spec := range set.Specs {
		for _, test := range spec.Tests {
			out[test.ID] = test
		}
	}
	return out
}

func gpsSortedTestIDs(left, right map[string]intent.TestRef) []string {
	ids := map[string]bool{}
	for id := range left {
		ids[id] = true
	}
	for id := range right {
		ids[id] = true
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func gpsSymbolsByID(symbols []sem.SymbolRecord) map[string]sem.SymbolRecord {
	out := make(map[string]sem.SymbolRecord, len(symbols))
	for _, symbol := range symbols {
		out[symbol.ID] = symbol
	}
	return out
}

func gpsSameBoundSymbol(before, after intent.Binding, base, head sem.ProviderSnapshot) bool {
	baseSymbol, baseOK := gpsSymbolsByID(base.Symbols)[before.SymbolID]
	headSymbol, headOK := gpsSymbolsByID(head.Symbols)[after.SymbolID]
	return baseOK && headOK && gpsSameSymbol(baseSymbol, headSymbol)
}

func gpsSameSelectedSymbols(before, after []sem.SymbolRecord) bool {
	if len(before) != len(after) {
		return false
	}
	for i := range before {
		if !gpsSameSymbol(before[i], after[i]) {
			return false
		}
	}
	return true
}

func gpsSameSymbol(before, after sem.SymbolRecord) bool {
	return before.ID == after.ID && before.FilePath == after.FilePath && before.Signature == after.Signature && before.BodyHash == after.BodyHash
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
	// A missing stable symbol ID may be a rename or move. Locate candidates by
	// qualified name across paths, but never write a rebind without review.
	candidates := matchingSymbols(snapshot.Symbols, binding.Selector.QualifiedName, "")
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

func queryMatchesText(query, text string) bool {
	for _, word := range strings.Fields(strings.ToLower(query)) {
		if strings.Contains(strings.ToLower(text), word) {
			return true
		}
	}
	return false
}

func acceptanceMatchesRequirement(spec intent.Spec, acceptanceID string, requirements map[string]bool) bool {
	for _, acceptance := range spec.Acceptance {
		if acceptance.ID == acceptanceID {
			return requirements[acceptance.Requirement]
		}
	}
	return false
}

var gpsContextSectionQuotas = []struct {
	name    string
	percent int
}{
	{"requirements", 25},
	{"symbols", 10},
	{"code", 35},
	{"dependencies", 5},
	{"tests", 10},
	{"inferred_tests", 15},
}

// fitGPSContextBudget assigns the available evidence space in priority order.
// Unused capacity carries only to later sections, so the same inputs always
// retain the same requirements, snippets, and test candidates.
func fitGPSContextBudget(response map[string]any, maximum int) {
	budget := response["budget"].(map[string]any)
	quotas := map[string]int{}
	for _, quota := range gpsContextSectionQuotas {
		quotas[quota.name] = quota.percent
	}
	budget["section_quotas"] = quotas
	if setGPSRenderedBytes(response) <= maximum {
		return
	}

	candidates := map[string][]any{}
	for _, quota := range gpsContextSectionQuotas {
		candidates[quota.name] = gpsAnySlice(response[quota.name])
		response[quota.name] = []any{}
	}
	originalGaps := append([]string(nil), response["gaps"].([]string)...)
	response["status"] = "complete_with_gaps"
	response["gaps"] = append(originalGaps, "CONTEXT_OMITTED_FOR_BUDGET")
	allNames := make([]string, 0, len(gpsContextSectionQuotas))
	for _, quota := range gpsContextSectionQuotas {
		allNames = append(allNames, quota.name)
	}
	budget["omitted"] = allNames
	base := setGPSRenderedBytes(response)
	if base > maximum {
		fitGPSMinimalManifest(response, maximum)
		return
	}

	available := maximum - base
	carry := 0
	selected := map[string]int{}
	for _, quota := range gpsContextSectionQuotas {
		allowance := available*quota.percent/100 + carry
		used := 0
		for _, item := range candidates[quota.name] {
			section := response[quota.name].([]any)
			before := setGPSRenderedBytes(response)
			response[quota.name] = append(section, item)
			now := setGPSRenderedBytes(response)
			delta := now - before
			if used+delta > allowance || now > maximum {
				response[quota.name] = section
				setGPSRenderedBytes(response)
				continue
			}
			used += delta
			selected[quota.name]++
		}
		carry = allowance - used
	}
	omitted := make([]string, 0, len(gpsContextSectionQuotas))
	for _, quota := range gpsContextSectionQuotas {
		if selected[quota.name] != len(candidates[quota.name]) {
			omitted = append(omitted, quota.name)
		}
	}
	budget["omitted"] = omitted
	if len(omitted) == 0 {
		response["gaps"] = originalGaps
		if len(originalGaps) == 0 {
			response["status"] = "complete"
		}
	}
	if setGPSRenderedBytes(response) > maximum {
		fitGPSMinimalManifest(response, maximum)
	}
}

func gpsAnySlice(value any) []any {
	switch values := value.(type) {
	case []any:
		return values
	case []map[string]string:
		out := make([]any, len(values))
		for i := range values {
			out[i] = values[i]
		}
		return out
	default:
		return nil
	}
}

func fitGPSMinimalManifest(response map[string]any, maximum int) {
	budget := response["budget"].(map[string]any)
	// The quota declaration is useful only when there is room for evidence. Drop
	// it in the emergency manifest so the advertised minimum remains achievable.
	delete(budget, "section_quotas")
	for _, quota := range gpsContextSectionQuotas {
		response[quota.name] = []any{}
	}
	response["status"] = "BUDGET_TOO_SMALL"
	response["gaps"] = []string{"BUDGET_TOO_SMALL"}
	omitted := make([]string, 0, len(gpsContextSectionQuotas))
	for _, quota := range gpsContextSectionQuotas {
		omitted = append(omitted, quota.name)
	}
	budget["omitted"] = omitted
	for len(response["request"].(string)) > 0 && setGPSRenderedBytes(response) > maximum {
		response["request"] = trimUTF8(response["request"].(string), len(response["request"].(string))-1)
	}
	setGPSRenderedBytes(response)
}

func trimUTF8(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	for maximum > 0 && (value[maximum]&0xc0) == 0x80 {
		maximum--
	}
	return value[:maximum]
}

// setGPSRenderedBytes reaches the stable serialized size after adding the size
// field itself. JSON is the context command's integration contract.
func setGPSRenderedBytes(response map[string]any) int {
	budget := response["budget"].(map[string]any)
	for range 8 {
		n := renderedGPSJSONBytes(response)
		if previous, ok := budget["rendered_bytes"].(int); ok && previous == n {
			return n
		}
		budget["rendered_bytes"] = n
	}
	return renderedGPSJSONBytes(response)
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
