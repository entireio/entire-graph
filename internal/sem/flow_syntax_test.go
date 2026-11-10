package sem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Flow extraction must run only the pattern families a language can express.
// Before routing, every family scanned every body: a Python conditional
// pattern over a Go body produced an edge for code that cannot parse in Go —
// a false DATA_FLOWS edge and wasted scan time. A family is skipped only
// where its shape is a syntax error, so no real edge is lost.
func TestFlowExtractionIsLanguageRouted(t *testing.T) {
	flowKinds := func(block, language string) map[string]bool {
		t.Helper()
		kinds := map[string]bool{}
		for _, flow := range returnFlowCalls(newSymbolBody(block), nil, flowSyntaxFor(language)) {
			kinds[flow.EvidenceKind] = true
		}
		return kinds
	}

	// Python conditional shape: an edge in Python, impossible syntax in Go.
	pythonCond := "def pick():\n    v = primary() if ready else fallback()\n    return v\n"
	if !flowKinds(pythonCond, "Python")["conditional_assigned_return_flow"] {
		t.Fatal("Python conditional assignment lost its flow on Python")
	}
	if kinds := flowKinds(pythonCond, "Go"); kinds["conditional_assigned_return_flow"] {
		t.Fatalf("Python conditional pattern matched a Go body: %v", kinds)
	}

	// C ternary shape: an edge in JavaScript, impossible syntax in Python.
	ternary := "function pick() {\n  return ready ? primary() : fallback();\n}\n"
	if !flowKinds(ternary, "JavaScript")["conditional_return_flow"] {
		t.Fatal("ternary return lost its flow on JavaScript")
	}
	if kinds := flowKinds(ternary, "Python"); kinds["conditional_return_flow"] {
		t.Fatalf("ternary pattern matched a Python body: %v", kinds)
	}

	// `||` in value position is legal Go (boolean calls), so Go keeps it.
	logical := "func pick() bool {\n\treturn primary() || fallback()\n}\n"
	if !flowKinds(logical, "Go")["fallback_return_flow"] {
		t.Fatal("Go `||` return flow was dropped; `||` is legal Go and the flow is real")
	}
	if kinds := flowKinds(logical, "Python"); kinds["fallback_return_flow"] {
		t.Fatalf("`||` pattern matched a Python body: %v", kinds)
	}

	// `or` in value position: Python yes, Go no.
	orFallback := "def pick():\n    return primary() or fallback()\n"
	if !flowKinds(orFallback, "Python")["fallback_return_flow"] {
		t.Fatal("Python `or` return flow was dropped")
	}
	if kinds := flowKinds(orFallback, "Go"); kinds["fallback_return_flow"] {
		t.Fatalf("`or` pattern matched a Go body: %v", kinds)
	}

	// `or` is also a legal `||` spelling where it is an alternative token
	// (C++, C via <iso646.h>) or an infix function (Kotlin, Scala). The line
	// guards hand ` or ` lines to this family exclusively, so these languages
	// must keep it or the edge is lost entirely.
	cppOr := "int pick() {\n  return primary() or fallback();\n}\n"
	for _, language := range []string{"C++", "C", "Kotlin", "Scala"} {
		if !flowKinds(cppOr, language)["fallback_return_flow"] {
			t.Fatalf("`or` return flow dropped on %s, where `or` parses", language)
		}
	}

	// An unknown language keeps every family: routing is an optimization that
	// must never drop a real edge under uncertainty.
	if kinds := flowKinds(pythonCond, "SomeFutureLanguage"); !kinds["conditional_assigned_return_flow"] {
		t.Fatalf("unknown language lost a flow family: %v", kinds)
	}
}

// End-to-end pin for the one place a cross-language shape reaches a body in
// practice: code that does not parse but still yields symbols through
// tree-sitter error recovery (pasted pseudo-code, generated or corrupted
// files). Before routing, a Python conditional inside an INVALID Go function
// produced DATA_FLOWS edges for code that cannot compile; those edges must be
// gone, while the same shapes in their own languages — and Go's legal `||`
// flow — survive untouched.
func TestSnapshotDropsCrossLanguageFlowMatches(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("pick.go", "package demo\n\nfunc primary() int  { return 1 }\nfunc fallback() int { return 2 }\n\nfunc pick() int {\n\tv := primary() if ready else fallback()\n\treturn v\n}\n")
	write("pick.py", "def primary():\n    return 1\n\ndef fallback():\n    return 2\n\ndef pick(ready):\n    v = primary() if ready else fallback()\n    return v\n")
	write("pick.js", "function primary() { return 1; }\nfunction fallback() { return 2; }\nfunction pick(ready) {\n  return ready ? primary() : fallback();\n}\n")
	write("bools.go", "package demo\n\nfunc isReady() bool { return true }\nfunc isArmed() bool { return false }\n\nfunc either() bool {\n\treturn isReady() || isArmed()\n}\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), dir, "0.0.0-test", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	flows := map[string]bool{}
	for _, relation := range snapshot.Relations {
		if relation.Type != "DATA_FLOWS" {
			continue
		}
		fromFile := ""
		if parts := strings.Split(relation.FromID, ":"); len(parts) > 2 {
			fromFile = parts[2]
		}
		flows[fromFile] = true
	}
	if flows["pick.go"] {
		t.Fatal("a Python conditional inside an invalid Go body produced a DATA_FLOWS edge; cross-language flow matches must not survive routing")
	}
	for _, want := range []string{"pick.py", "pick.js", "bools.go"} {
		if !flows[want] {
			t.Fatalf("legitimate %s flow edge was lost; flows seen: %v", want, flows)
		}
	}
}
