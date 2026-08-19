package sem

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Round four. Two more code paths that ran to completion with nothing able to
// stop them: the parsers that never reach tree-sitter (so the walk's predicate
// never covered them) and the whole-repository producers inside forEachRelation
// (which materialize their entire result before the caller's per-record guard
// can run).
//
// Every test here is DETERMINISTIC: the stop predicate is flipped by the test,
// not by a clock, and the assertions are on work performed, not on elapsed time.
// A wall-clock ceiling is not a property a shared CI runner can be asked to
// meet; the timed end-to-end measurements live in the PR description.

// tf142r4GroovyBomb is Groovy whose declarations nest, so every enclosing class
// entity spans the rest of the file and the body-hash pass joins those lines
// once per entity: superlinear in file length, in plain Go, previously with no
// way to interrupt it.
func tf142r4GroovyBomb(n int) string {
	var sb strings.Builder
	for i := range n {
		fmt.Fprintf(&sb, "class C%d {\n  def m%d() { return %d }\n", i, i, i)
	}
	for range n {
		sb.WriteString("}\n")
	}
	return sb.String()
}

// TestTF142R4GrammarlessParsersObserveBudget reproduces the finding at
// parser.go:203. ParseWithStatusCtx built its stop predicate BELOW three return
// paths that never reach tree-sitter -- the dedicated Groovy parser, YAML's own
// entity extraction, and every grammar-less fallback -- so a caller whose budget
// had already expired still paid for the whole file parse, and
// processProviderFile then discarded the result anyway.
func TestTF142R4GrammarlessParsersObserveBudget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		path    string
		content string
	}{
		{"groovy", "Bomb.groovy", tf142r4GroovyBomb(200)},
		{"yaml", ".github/workflows/ci.yaml", "name: ci\njobs:\n  build:\n    runs-on: ubuntu-latest\n"},
		{"fallback-markdown", "README.md", "# Title\n\ntext\n\n## Section\n\nmore\n"},
		{"fallback-inventory", "notes.rst", "Notes\n=====\n\nbody\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// An unbudgeted parse must be unchanged: this is the control.
			live, _, _ := TreeSitterParser{}.ParseWithStatus(tc.path, tc.content)
			if len(live) == 0 {
				t.Fatalf("fixture parses to nothing without a budget; the probe never armed")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // the caller's budget has already expired
			got, _, status := TreeSitterParser{}.ParseWithStatusCtx(ctx, tc.path, tc.content)
			if len(got) != 0 {
				t.Fatalf("an expired budget must abandon the file, got %d entities (the parser never consulted ctx)", len(got))
			}
			if status.Code != "E_PARSE_TIMEOUT" {
				t.Fatalf("an abandoned parse must be reported as E_PARSE_TIMEOUT, got %q (%s)", status.Code, status.Detail)
			}
		})
	}
}

// TestTF142R4GroovyScanStopsMidFile pins the second half of the same finding:
// an entry guard alone cannot bound a parser whose own runtime dwarfs the
// budget, so the Groovy scanner takes the predicate itself.
func TestTF142R4GroovyScanStopsMidFile(t *testing.T) {
	t.Parallel()
	src := tf142r4GroovyBomb(200)
	full, _, _ := TreeSitterParser{}.ParseWithStatus("Bomb.groovy", src)
	if len(full) < 100 {
		t.Fatalf("fixture should yield hundreds of entities, got %d", len(full))
	}
	probes := 0
	stopped, _ := groovyEntities(src, func() bool { probes++; return probes > 1 })
	if probes == 0 {
		t.Fatal("the Groovy scanner never consulted the stop predicate")
	}
	if len(stopped) >= len(full) {
		t.Fatalf("a stopped scan must not complete the file: %d entities stopped vs %d unstopped", len(stopped), len(full))
	}
}

// TestTF142R4SimilarityProducerStopsMidPass reproduces the finding at
// provider.go:1252. forEachRelation's per-record guard could not run until a
// whole-repository producer had materialized its entire result; similarityRelations
// is the superlinear one (symbolBlockFromLines re-scans an enclosing symbol's
// whole body once per nested symbol), so a budget expiring inside it bought the
// whole pass.
//
// The assertion is on files read, not on elapsed time, so it holds on any runner.
func TestTF142R4SimilarityProducerStopsMidPass(t *testing.T) {
	t.Parallel()
	const files = 40
	body := "function a(x) {\n  const y = x + 1;\n  const z = y * 2;\n  return z + y + x;\n}\n"
	records := map[string][]SymbolRecord{}
	for i := range files {
		path := fmt.Sprintf("pkg%02d/a.js", i)
		records[path] = []SymbolRecord{{
			ID: fmt.Sprintf("sym%02d", i), Kind: "function", Name: "a",
			FilePath: path, StartLine: 1, EndLine: 5,
		}}
	}
	reads := 0
	read := func(string) (string, bool) { reads++; return body, true }

	// Control: with no budget the producer reads every file, exactly as before.
	unbudgeted := similarityRelations(records, read, nil)
	if reads != files {
		t.Fatalf("unbudgeted producer must read every file: %d of %d", reads, files)
	}
	if len(unbudgeted) == 0 {
		t.Fatal("fixture produced no SIMILAR_TO relations; the probe never armed")
	}

	// The deadline fires after the first file: the producer must stop there
	// rather than finish the pass and hand a complete slice to a guard that
	// will discard it.
	reads = 0
	stopAfter := 1
	stopped := similarityRelations(records, read, func() bool { return reads >= stopAfter })
	if reads > stopAfter+1 {
		t.Fatalf("the producer read %d files after the deadline fired at file %d: it materializes the whole pass before any guard runs", reads, stopAfter)
	}
	if len(stopped) != 0 {
		t.Fatalf("a producer stopped mid-pass must not emit a partial relation set, got %d", len(stopped))
	}
}
