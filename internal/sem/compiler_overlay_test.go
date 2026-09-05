package sem

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/compiler"
)

func TestCompilerOverlayExactSiteReconciliation(t *testing.T) {
	body := "package p\nfunc Target() {}\nfunc Wrong() {}\nfunc Caller() {\n Target()\n Wrong()\n}\n"
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "fixture", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"a.go": body}
	decls, calls := compilerTokens(files, snapshot.Symbols)
	if len(calls) != 2 || len(decls) != 3 {
		t.Fatalf("exact token map declarations=%#v calls=%#v", decls, calls)
	}
	var target, wrong compilerToken
	for _, decl := range decls {
		switch decl.name {
		case "Target":
			target = decl
		case "Wrong":
			wrong = decl
		}
	}
	for i := range snapshot.Relations {
		r := &snapshot.Relations[i]
		if r.FromID == calls[0].symbol && r.ToID == target.symbol {
			r.ToID = wrong.symbol
		}
	}
	// Independent contradictory static fact with an exact source line. Existing
	// aggregate evidence without that location must never be guessed into a site.
	snapshot.Relations = append(snapshot.Relations, RelationRecord{RecordType: "relation", FromID: calls[0].symbol, ToID: wrong.symbol, Type: "CALLS", Evidence: []Evidence{{Kind: "fixture-callsite", FilePath: "a.go", StartLine: calls[0].line}}})
	original := append([]RelationRecord(nil), snapshot.Relations...)
	start, _ := compiler.PositionAt(body, target.start)
	end, _ := compiler.PositionAt(body, target.end)
	report := compiler.Report{Status: "complete", Backend: "fixture/fake", ContextID: compiler.ContentDigest("fixture-context"), Answers: []compiler.Answer{{Query: compiler.Query{Path: "a.go", Offset: calls[0].start}, Kind: "textDocument/definition", Targets: []compiler.Location{{URI: "file:///workspace/a.go", Range: compiler.Range{Start: start, End: end}}}}}}
	overlay := reconcileCompiler(&snapshot, files, decls, calls, report)
	if len(overlay.Calls) != 1 || overlay.Calls[0].Reconciliation != "disputed_static_at_site" {
		t.Fatalf("overlay %#v", overlay)
	}
	if !reflect.DeepEqual(snapshot.Relations, original) {
		t.Fatal("aggregate static relations were mutated")
	}
	if overlay.Calls[0].Evidence.Caller.StartByte != strings.LastIndex(body, " Target()")+1 {
		t.Fatal("wrong source site")
	}
	report.Status = "partial"
	overlay = reconcileCompiler(&snapshot, files, decls, calls, report)
	if overlay.Calls[0].Reconciliation == "disputed_static_at_site" {
		t.Fatal("partial compiler evidence disputed static site")
	}
	report.Answers[0].Query.Implementation = true
	report.Answers[0].Kind = "textDocument/implementation"
	overlay = reconcileCompiler(&snapshot, files, decls, calls, report)
	if overlay.Calls[0].Evidence.Category != compiler.ImplementationCandidate || overlay.Calls[0].Reconciliation != "candidate_only" {
		t.Fatal("candidate promoted to call")
	}
}
func TestCompilerOverlayProjectionRefusalAndUnavailable(t *testing.T) {
	header := SnapshotHeader{Compiler: &CompilerOverlay{Report: compiler.Report{Status: "partial"}}}
	for _, encode := range []func(any) error{NewCompactSnapshotEncoder(io.Discard).Encode, NewSCIPSnapshotEncoder(io.Discard, "").Encode} {
		if encode(header) == nil {
			t.Fatal("projection silently lost compiler distinction")
		}
	}
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package p\nfunc A() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	options := ProviderSnapshotOptions{Worktree: true, Compiler: &CompilerOptions{}}
	snapshot, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "fixture", options)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Header.Compiler == nil || snapshot.Header.Compiler.Report.Status != "unavailable" || len(snapshot.Symbols) != 1 {
		t.Fatal("missing explicit static fallback")
	}
	options.Compiler.Require = true
	if _, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "fixture", options); err == nil {
		t.Fatal("require compiler silently fell back")
	}
}

func TestLiveCompilerSemanticMapping(t *testing.T) {
	if os.Getenv("ENTIRE_GRAPH_COMPILER_LIVE") != "1" {
		t.Skip("explicit isolated Linux integration")
	}
	repo := t.TempDir()
	body := "package p\ntype Worker interface { Work() }\ntype One struct{}\nfunc (One) Work() {}\ntype Two struct{}\nfunc (Two) Work() {}\nfunc Caller(w Worker) {\n w.Work()\n}\n"
	for name, content := range map[string]string{"go.mod": "module fixture.local/semantic\n\ngo 1.24\n", "a.go": body} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	options := ProviderSnapshotOptions{Worktree: true, Compiler: &CompilerOptions{Config: compiler.Config{ServerPath: "/opt/graph-tools/gopls", ServerSHA256: "2b4652d6ac42a22942f63735d9c7e44e9dfbc1dade5d4fd09c0d4eb8fa3539b1", ToolchainRoot: "/usr/local/go", BubblewrapPath: "/usr/bin/bwrap"}}}
	snapshot, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "fixture", options)
	if err != nil {
		t.Fatal(err)
	}
	overlay := snapshot.Header.Compiler
	if overlay == nil || overlay.Report.Status != "complete" {
		t.Fatalf("live overlay %#v", overlay)
	}
	direct, candidates := 0, 0
	for _, call := range overlay.Calls {
		switch call.Evidence.Category {
		case compiler.DirectDeclaration:
			direct++
		case compiler.ImplementationCandidate:
			candidates++
		}
	}
	if direct != 1 || candidates != 2 {
		t.Fatalf("live declaration/candidates direct=%d candidates=%d %#v", direct, candidates, overlay)
	}
}

func TestCompilerOverlayAmbiguousDirectAndClosureRemainUnconfirmed(t *testing.T) {
	body := "package p\nfunc A() {}\nfunc B() {}\nfunc Caller() {\n A()\n callback := func() {}; callback()\n}\n"
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "fixture", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"a.go": body}
	declarations, calls := compilerTokens(files, snapshot.Symbols)
	var locations []compiler.Location
	for _, decl := range declarations {
		if decl.name == "A" || decl.name == "B" {
			start, _ := compiler.PositionAt(body, decl.start)
			end, _ := compiler.PositionAt(body, decl.end)
			locations = append(locations, compiler.Location{URI: "file:///workspace/a.go", Range: compiler.Range{Start: start, End: end}})
		}
	}
	report := compiler.Report{Status: "complete", Backend: "fixture/fake", ContextID: compiler.ContentDigest("context"), Answers: []compiler.Answer{{Query: compiler.Query{Path: "a.go", Offset: calls[0].start}, Kind: "textDocument/definition", Targets: locations}}}
	overlay := reconcileCompiler(&snapshot, files, declarations, calls, report)
	if len(overlay.Calls) != 0 || overlay.Report.Status != "partial" {
		t.Fatalf("ambiguous direct promoted %#v", overlay)
	}
	for _, call := range calls {
		if call.name == "callback" {
			start, _ := compiler.PositionAt(body, strings.Index(body, "callback :="))
			end, _ := compiler.PositionAt(body, strings.Index(body, "callback :=")+len("callback"))
			report.Answers = []compiler.Answer{{Query: compiler.Query{Path: "a.go", Offset: call.start}, Kind: "textDocument/definition", Targets: []compiler.Location{{URI: "file:///workspace/a.go", Range: compiler.Range{Start: start, End: end}}}}}
			overlay = reconcileCompiler(&snapshot, files, declarations, calls, report)
			if len(overlay.Calls) != 0 || overlay.Report.Status != "partial" {
				t.Fatalf("closure variable promoted %#v", overlay)
			}
			return
		}
	}
	t.Fatal("fixture callback call not located")
}

func TestCompilerEnrichedViewDisputesOnlyExactEvidence(t *testing.T) {
	relation := RelationRecord{FromID: "caller", ToID: "wrong", Type: "CALLS", Evidence: []Evidence{{FilePath: "a.go", StartLine: 5}, {FilePath: "a.go", StartLine: 9}}}
	snapshot := ProviderSnapshot{Relations: []RelationRecord{relation}, Header: SnapshotHeader{Compiler: &CompilerOverlay{Report: compiler.Report{Status: "complete"}, Calls: []CompilerCallEvidence{{CallSiteLine: 5, SourceSymbolID: "caller", StaticTargetIDs: []string{"wrong"}, Reconciliation: "disputed_static_at_site", Evidence: compiler.Evidence{Category: compiler.DirectDeclaration, TargetSymbolID: "right", Caller: compiler.Site{Path: "a.go"}}}}}}}
	enriched := CompilerEnrichedRelations(snapshot, false)
	if len(enriched) != 2 || enriched[0].ToID != "wrong" || len(enriched[0].Evidence) != 1 || enriched[0].Evidence[0].StartLine != 9 || enriched[1].ToID != "right" {
		t.Fatalf("wrong enriched view %#v", enriched)
	}
	if len(snapshot.Relations[0].Evidence) != 2 {
		t.Fatal("native evidence mutated")
	}
	snapshot.Header.Compiler.Report.Status = "partial"
	enriched = CompilerEnrichedRelations(snapshot, false)
	if len(enriched[0].Evidence) != 2 {
		t.Fatal("partial evidence disputed static")
	}
}

// These sources and expected targets are independently authored from review F1.
// Fake responses use exact real-parser declaration tokens, not name guesses.
func TestCompilerOverlayConversionsAreNotCalls(t *testing.T) {
	body := `package p
 type UserID int
 type Alias = UserID
 type Slice[T any] []T
 func Invoke(x int) int { return x }
 func Convert(x int, xs []int) {
 _ = UserID(x)
 _ = Alias(x)
 _ = Slice[int](xs)
 _ = (UserID)(x)
 _ = Invoke(x)
 }
 `
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "fixture", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"a.go": body}
	declarations, calls := compilerTokens(files, snapshot.Symbols)
	if len(calls) != 5 {
		t.Fatalf("calls=%#v", calls)
	}
	byName := map[string]compilerToken{}
	for _, declaration := range declarations {
		byName[declaration.name] = declaration
	}
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			target, ok := byName[call.name]
			if !ok {
				t.Fatalf("missing declaration %q", call.name)
			}
			start, _ := compiler.PositionAt(body, target.start)
			end, _ := compiler.PositionAt(body, target.end)
			base := snapshot
			base.Relations = []RelationRecord{{FromID: call.symbol, ToID: "unrelated-static-target", Type: "CALLS", Evidence: []Evidence{{FilePath: "a.go", StartLine: call.line}}}}
			for _, implementation := range []bool{false, true} {
				report := compiler.Report{Status: "complete", Backend: "fixture/fake", ContextID: compiler.ContentDigest("conversion-fixture"), Answers: []compiler.Answer{{Query: compiler.Query{Path: "a.go", Offset: call.start, Implementation: implementation}, Kind: "textDocument/definition", Targets: []compiler.Location{{URI: "file:///workspace/a.go", Range: compiler.Range{Start: start, End: end}}}}}}
				if implementation {
					report.Answers[0].Kind = "textDocument/implementation"
				}
				overlay := reconcileCompiler(&base, files, declarations, calls, report)
				base.Header.Compiler = &overlay
				if call.name == "Invoke" {
					if len(overlay.Calls) != 1 {
						t.Fatalf("callable lost: %#v", overlay)
					}
					continue
				}
				if len(overlay.Calls) != 0 || overlay.Report.Status != "complete" {
					t.Fatalf("conversion promoted or coverage degraded: %#v", overlay)
				}
				if got := CompilerEnrichedRelations(base, true); !reflect.DeepEqual(got, base.Relations) {
					t.Fatalf("conversion changed static evidence: %#v", got)
				}
			}
		})
	}
}

func TestLiveCompilerConversionsAreNotCalls(t *testing.T) {
	if os.Getenv("ENTIRE_GRAPH_COMPILER_LIVE") != "1" {
		t.Skip("explicit isolated Linux integration")
	}
	repo := t.TempDir()
	body := `package p
 type UserID int
 type Alias = UserID
 type Slice[T any] []T
 func Invoke(x int) int { return x }
 func Convert(x int, xs []int) {
 _ = UserID(x)
 _ = Alias(x)
 _ = Slice[int](xs)
 _ = (UserID)(x)
 _ = Invoke(x)
 }
 `
	for name, content := range map[string]string{"go.mod": "module fixture.local/conversion\n\ngo 1.24\n", "a.go": body} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	options := ProviderSnapshotOptions{Worktree: true, Compiler: &CompilerOptions{Config: compiler.Config{ServerPath: "/opt/graph-tools/gopls", ServerSHA256: "2b4652d6ac42a22942f63735d9c7e44e9dfbc1dade5d4fd09c0d4eb8fa3539b1", ToolchainRoot: "/usr/local/go", BubblewrapPath: "/usr/bin/bwrap"}}}
	snapshot, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "fixture", options)
	if err != nil {
		t.Fatal(err)
	}
	overlay := snapshot.Header.Compiler
	if overlay == nil || overlay.Report.Status != "complete" {
		t.Fatalf("live coverage %#v", overlay)
	}
	var invoke string
	for _, symbol := range snapshot.Symbols {
		if symbol.Name == "Invoke" {
			invoke = symbol.ID
		}
	}
	if invoke == "" || len(overlay.Calls) != 1 || overlay.Calls[0].Evidence.TargetSymbolID != invoke || overlay.Calls[0].Evidence.Category != compiler.DirectDeclaration {
		t.Fatalf("live conversions emitted call evidence %#v", overlay)
	}
	for _, relation := range CompilerEnrichedRelations(snapshot, true) {
		if strings.HasPrefix(relation.Resolution, "compiler_") && relation.ToID != invoke {
			t.Fatalf("conversion projected %#v", relation)
		}
	}
}
