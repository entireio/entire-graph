package sem

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"

	"github.com/entireio/entire-graph/internal/compiler"
)

type CompilerOptions struct {
	Config  compiler.Config
	Require bool
}
type CompilerOverlay struct {
	Report compiler.Report        `json:"coverage"`
	Calls  []CompilerCallEvidence `json:"calls,omitempty"`
}
type CompilerCallEvidence struct {
	CallSiteLine    int               `json:"call_site_line,omitempty"`
	SourceSymbolID  string            `json:"source_symbol_id"`
	Evidence        compiler.Evidence `json:"evidence"`
	Reconciliation  string            `json:"reconciliation"`
	StaticTargetIDs []string          `json:"static_target_ids,omitempty"`
}
type compilerToken struct {
	path             string
	start, end, line int
	name             string
	symbol           string
	interfaceMethod  bool
}

func enrichCompilerSnapshot(ctx context.Context, snapshot *ProviderSnapshot, source sourceContext, options CompilerOptions) error {
	files := map[string]string{}
	var missing []compiler.Diagnostic
	for _, name := range source.paths {
		content, ok := source.read(name)
		if !ok {
			missing = append(missing, compiler.Diagnostic{Code: "compiler_capture_missing", Detail: name})
			continue
		}
		files[name] = content
	}
	// Module/workspace files can be non-semantic inventory inputs. Use the same
	// captured reader; never open them through an unrelated live path.
	for _, name := range []string{"go.mod", "go.sum", "go.work", "go.work.sum"} {
		if content, ok := source.read(name); ok {
			files[name] = content
		}
	}
	declarations, calls := compilerTokens(files, snapshot.Symbols)
	queries := make([]compiler.Query, 0, len(calls))
	for _, call := range calls {
		queries = append(queries, compiler.Query{Path: call.path, Offset: call.start, IncludeCandidates: true})
	}
	manifest, err := source.finishCapture(source.paths)
	if err != nil {
		return err
	}
	if manifest != nil {
		options.Config.OperationID = manifest.ID
	}
	report := compiler.Analyze(ctx, options.Config, files, queries)
	report.Diagnostics = append(report.Diagnostics, missing...)
	if len(missing) > 0 && report.Status == "complete" {
		report.Status = "partial"
	}
	overlay := reconcileCompiler(snapshot, files, declarations, calls, report)
	snapshot.Header.Compiler = &overlay
	if options.Require && overlay.Report.Status != "complete" {
		return fmt.Errorf("required compiler coverage is %s", overlay.Report.Status)
	}
	return nil
}
func compilerTokens(files map[string]string, symbols []SymbolRecord) ([]compilerToken, []compilerToken) {
	byFile := map[string][]SymbolRecord{}
	for _, symbol := range symbols {
		byFile[symbol.FilePath] = append(byFile[symbol.FilePath], symbol)
	}
	names := make([]string, 0, len(files))
	for name := range files {
		if strings.HasSuffix(name, ".go") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var declarations, calls []compilerToken
	for _, name := range names {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, files[name], parser.AllErrors)
		if err != nil || file == nil {
			continue
		}
		offset := func(pos token.Pos) int { return fset.PositionFor(pos, false).Offset }
		mapped := func(ident *ast.Ident, iface bool) {
			if ident == nil {
				return
			}
			start, end := offset(ident.Pos()), offset(ident.End())
			id := ""
			for _, symbol := range byFile[name] {
				if symbol.Name == ident.Name && symbol.sourceStartByte <= start && symbol.sourceEndByte >= end {
					if id != "" {
						id = ""
						break
					}
					id = symbol.ID
				}
			}
			if id != "" {
				declarations = append(declarations, compilerToken{name, start, end, fset.Position(ident.Pos()).Line, ident.Name, id, iface})
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.FuncDecl:
				mapped(node.Name, false)
			case *ast.TypeSpec:
				mapped(node.Name, false)
				if iface, ok := node.Type.(*ast.InterfaceType); ok {
					for _, field := range iface.Methods.List {
						for _, name := range field.Names {
							mapped(name, true)
						}
					}
				}
			case *ast.CallExpr:
				expression := node.Fun
				for {
					switch typed := expression.(type) {
					case *ast.IndexExpr:
						expression = typed.X
						continue
					case *ast.IndexListExpr:
						expression = typed.X
						continue
					case *ast.ParenExpr:
						expression = typed.X
						continue
					}
					break
				}
				var ident *ast.Ident
				switch expression := expression.(type) {
				case *ast.Ident:
					ident = expression
				case *ast.SelectorExpr:
					ident = expression.Sel
				}
				if ident == nil {
					return true
				}
				start, end := offset(ident.Pos()), offset(ident.End())
				caller := ""
				width := int(^uint(0) >> 1)
				for _, symbol := range byFile[name] {
					if symbol.Kind != "function" && symbol.Kind != "method" {
						continue
					}
					if symbol.sourceStartByte <= start && symbol.sourceEndByte >= end {
						candidateWidth := symbol.sourceEndByte - symbol.sourceStartByte
						if candidateWidth < width {
							width = candidateWidth
							caller = symbol.ID
						} else if candidateWidth == width {
							caller = ""
						}
					}
				}
				if caller != "" {
					calls = append(calls, compilerToken{name, start, end, fset.Position(ident.Pos()).Line, ident.Name, caller, false})
				}
			}
			return true
		})
	}
	sort.Slice(calls, func(i, j int) bool {
		if calls[i].path != calls[j].path {
			return calls[i].path < calls[j].path
		}
		return calls[i].start < calls[j].start
	})
	return declarations, calls
}
func reconcileCompiler(snapshot *ProviderSnapshot, files map[string]string, declarations, calls []compilerToken, report compiler.Report) CompilerOverlay {
	overlay := CompilerOverlay{Report: report}
	for _, answer := range report.Answers {
		if !answer.Query.Implementation && len(answer.Targets) > 1 {
			overlay.Report.Status = "partial"
			overlay.Report.Diagnostics = append(overlay.Report.Diagnostics, compiler.Diagnostic{Code: "compiler_direct_target_ambiguous"})
			continue
		}
		var call *compilerToken
		for i := range calls {
			if calls[i].path == answer.Query.Path && calls[i].start == answer.Query.Offset {
				call = &calls[i]
				break
			}
		}
		if call == nil {
			continue
		}
		for _, location := range answer.Targets {
			path, start, end, err := compiler.MapLocation(files, location)
			if err != nil {
				continue
			}
			target := ""
			for _, declaration := range declarations {
				if declaration.path == path && declaration.start == start && declaration.end == end {
					if target != "" {
						target = ""
						break
					}
					target = declaration.symbol
				}
			}
			if target == "" {
				overlay.Report.Status = "partial"
				overlay.Report.Diagnostics = append(overlay.Report.Diagnostics, compiler.Diagnostic{Code: "compiler_symbol_unmapped", Detail: path})
				continue
			}
			category := compiler.DirectDeclaration
			status := "direct_evidence"
			if answer.Query.Implementation {
				category = compiler.ImplementationCandidate
				status = "candidate_only"
			}
			evidence := compiler.Evidence{ContextID: report.ContextID, BackendVersion: report.Backend, Category: category, QueryKind: answer.Kind, Caller: compiler.Site{Path: call.path, Digest: compiler.ContentDigest(files[call.path]), StartByte: call.start, EndByte: call.end}, Target: compiler.Site{Path: path, Digest: compiler.ContentDigest(files[path]), StartByte: start, EndByte: end}, TargetSymbolID: target}
			if compiler.ValidateEvidence(evidence, report.ContextID, report.Backend, files) != nil {
				continue
			}
			item := CompilerCallEvidence{CallSiteLine: call.line, SourceSymbolID: call.symbol, Evidence: evidence, Reconciliation: status}
			// Existing evidence is line-addressed. It can identify this token only if
			// this is the sole call expression on that caller/line. Never dispute an
			// aggregate edge because another expression in the function was answered.
			count := 0
			for _, other := range calls {
				if other.path == call.path && other.symbol == call.symbol && other.line == call.line {
					count++
				}
			}
			if !answer.Query.Implementation && count == 1 && len(answer.Targets) == 1 {
				for _, relation := range snapshot.Relations {
					if relation.FromID != call.symbol || (relation.Type != "CALLS" && relation.Type != "ASYNC_CALLS" && relation.Type != "CONSTRUCTS") {
						continue
					}
					for _, site := range relation.Evidence {
						if site.FilePath == call.path && site.StartLine == call.line && (site.EndLine == 0 || site.EndLine == site.StartLine) {
							item.StaticTargetIDs = append(item.StaticTargetIDs, relation.ToID)
							break
						}
					}
				}
				sort.Strings(item.StaticTargetIDs)
				for _, id := range item.StaticTargetIDs {
					if id == target {
						item.Reconciliation = "confirmed"
					}
				}
				if item.Reconciliation != "confirmed" && len(item.StaticTargetIDs) > 0 && report.Status == "complete" {
					item.Reconciliation = "disputed_static_at_site"
				}
			}
			overlay.Calls = append(overlay.Calls, item)
		}
	}
	if overlay.Report.Status != "complete" {
		for i := range overlay.Calls {
			if overlay.Calls[i].Reconciliation == "disputed_static_at_site" {
				overlay.Calls[i].Reconciliation = "direct_evidence"
			}
		}
	}
	return overlay
}

// CompilerEnrichedRelations creates an operation-local view. The native static
// graph is never mutated. Candidate edges have their own relation and zero
// confidence in runtime invocation, so they cannot become confirmed calls.
func CompilerEnrichedRelations(snapshot ProviderSnapshot, includeCandidates bool) []RelationRecord {
	relations := append([]RelationRecord(nil), snapshot.Relations...)
	if snapshot.Header.Compiler == nil {
		return relations
	}
	for _, item := range snapshot.Header.Compiler.Calls {
		// Remove only a positively disputed exact site from this derivative
		// view. Other sites and source facts without exact location survive.
		if snapshot.Header.Compiler.Report.Status == "complete" && item.CallSiteLine > 0 && (item.Reconciliation == "disputed_static_at_site" || item.Reconciliation == "confirmed") {
			kept := make([]RelationRecord, 0, len(relations))
			for _, relation := range relations {
				disputed := false
				for _, target := range item.StaticTargetIDs {
					if target == relation.ToID && target != item.Evidence.TargetSymbolID {
						disputed = true
					}
				}
				if !disputed || relation.FromID != item.SourceSymbolID || (relation.Type != "CALLS" && relation.Type != "ASYNC_CALLS" && relation.Type != "CONSTRUCTS") || len(relation.Evidence) == 0 {
					kept = append(kept, relation)
					continue
				}
				sites := make([]Evidence, 0, len(relation.Evidence))
				for _, site := range relation.Evidence {
					if site.FilePath != item.Evidence.Caller.Path || site.StartLine != item.CallSiteLine || (site.EndLine != 0 && site.EndLine != site.StartLine) {
						sites = append(sites, site)
					}
				}
				if len(sites) > 0 {
					relation.Evidence = sites
					kept = append(kept, relation)
				}
			}
			relations = kept
		}
		evidence := item.Evidence
		candidate := evidence.Category == compiler.ImplementationCandidate
		if candidate && !includeCandidates {
			continue
		}
		kind, confidence, resolution := "CALLS", 1.0, "compiler_direct_declaration"
		if candidate {
			kind, confidence, resolution = "X-entire-graph:COMPILER_IMPLEMENTATION_CANDIDATE", 0, "compiler_candidate"
		}
		relations = append(relations, RelationRecord{RecordType: "relation", FromID: item.SourceSymbolID, ToID: evidence.TargetSymbolID, Type: kind, Confidence: confidence, Resolution: resolution, TargetKind: "symbol", Reason: string(evidence.Category), Evidence: []Evidence{{Kind: string(evidence.Category), FilePath: evidence.Caller.Path, Detail: fmt.Sprintf("bytes=%d:%d digest=%s context=%s backend=%s", evidence.Caller.StartByte, evidence.Caller.EndByte, evidence.Caller.Digest, evidence.ContextID, evidence.BackendVersion)}}})
	}
	return relations
}
