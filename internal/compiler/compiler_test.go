package compiler

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestUTF16Positions(t *testing.T) {
	source := "a𐐀b\r\n世\rx\ny"
	fixtures := map[int]Position{0: {0, 0}, 1: {0, 1}, 5: {0, 3}, 6: {0, 4}, 8: {1, 0}, 11: {1, 1}, 12: {2, 0}, 13: {2, 1}, 14: {3, 0}, 15: {3, 1}}
	for offset, want := range fixtures {
		got, err := PositionAt(source, offset)
		if err != nil || got != want {
			t.Fatalf("byte %d: %v %v", offset, got, err)
		}
		round, err := OffsetAt(source, want)
		if err != nil || round != offset {
			t.Fatalf("round trip %d: %d %v", offset, round, err)
		}
	}
	for _, offset := range []int{-1, 2, 7, 16} {
		if _, err := PositionAt(source, offset); err == nil {
			t.Fatalf("accepted split/invalid byte %d", offset)
		}
	}
	for _, position := range []Position{{0, 2}, {0, 5}, {4, 0}, {-1, 0}} {
		if _, err := OffsetAt(source, position); err == nil {
			t.Fatalf("accepted position %v", position)
		}
	}
}

func TestProtocolFrames(t *testing.T) {
	var stream bytes.Buffer
	for _, body := range []json.RawMessage{json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":"世"}`), json.RawMessage(`{"jsonrpc":"2.0","id":2,"result":null}`)} {
		if err := WriteMessage(&stream, body); err != nil {
			t.Fatal(err)
		}
	}
	reader := bufio.NewReader(&stream)
	for range 2 {
		if _, err := ReadMessage(reader); err != nil {
			t.Fatal(err)
		}
	}
	for _, frame := range []string{"\r\n{}", "Content-Length: 2\n\n{}", "Content-Length: 2\r\nContent-Length: 2\r\n\r\n{}", "Content-Length: -1\r\n\r\n", fmt.Sprintf("Content-Length: %d\r\n\r\n", MaxMessageBytes+1), "Content-Length: 2\r\nContent-Type: application/vscode-jsonrpc; charset=latin1\r\n\r\n{}", "Content-Length: 2\r\n\r\n{", "Content-Length: 2\r\n\r\nxx", strings.Repeat("A", maxHeaderBytes+1)} {
		if _, err := ReadMessage(bufio.NewReader(strings.NewReader(frame))); err == nil {
			t.Fatal("invalid frame accepted")
		}
	}
}

func TestCompilerContextInvalidationAndEvidence(t *testing.T) {
	sources := map[string]string{"caller.go": "call()", "target.go": "func call() {}"}
	build := BuildContext{OperationID: ContentDigest("operation"), Inputs: []Input{{Path: "caller.go", Digest: ContentDigest(sources["caller.go"]), Present: true}, {Path: "target.go", Digest: ContentDigest(sources["target.go"]), Present: true}}, Configuration: []string{"GOOS=linux", "GOARCH=amd64", "cgo=0"}, Packages: []string{"fixture"}, AdapterVersion: "fixture-adapter-v1", ServerVersion: "fake-server-v1", ToolchainVersion: "fixture-toolchain-v1"}
	id, err := build.ID()
	if err != nil {
		t.Fatal(err)
	}
	evidence := Evidence{ContextID: id, BackendVersion: build.ServerVersion, Category: DirectDeclaration, QueryKind: "textDocument/definition", Caller: Site{"caller.go", ContentDigest(sources["caller.go"]), 0, 4}, Target: Site{"target.go", ContentDigest(sources["target.go"]), 5, 9}, TargetSymbolID: "fixture:call"}
	if err := ValidateEvidence(evidence, id, build.ServerVersion, sources); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*BuildContext){func(b *BuildContext) { b.Configuration = append(b.Configuration, "tag=other") }, func(b *BuildContext) { b.ServerVersion = "new" }, func(b *BuildContext) { b.Inputs = append(b.Inputs, Input{Path: "go.work", Present: false}) }, func(b *BuildContext) { b.Packages = []string{"another"} }, func(b *BuildContext) { b.OperationID = ContentDigest("changed dependency") }} {
		changed := build
		mutate(&changed)
		other, err := changed.ID()
		if err != nil || other == id {
			t.Fatal("context edit failed to invalidate")
		}
		if ValidateEvidence(evidence, other, build.ServerVersion, sources) == nil {
			t.Fatal("stale evidence accepted")
		}
	}
	candidate := evidence
	candidate.Category = ImplementationCandidate
	if ValidateEvidence(candidate, id, build.ServerVersion, sources) == nil {
		t.Fatal("definition relabeled as implementation")
	}
	candidate.QueryKind = "textDocument/implementation"
	if err := ValidateEvidence(candidate, id, build.ServerVersion, sources); err != nil {
		t.Fatal(err)
	}
	sources["target.go"] = "func nope() {}"
	if ValidateEvidence(evidence, id, build.ServerVersion, sources) == nil {
		t.Fatal("changed source accepted")
	}
}

func TestCompilerContextLengthPrefixAndOrdering(t *testing.T) {
	build := BuildContext{OperationID: ContentDigest("fixture"), AdapterVersion: "a", ServerVersion: "b", ToolchainVersion: "c", Configuration: []string{"ab", "c"}, Packages: []string{"b", "a"}}
	first, _ := build.ID()
	build.Configuration = []string{"a", "bc"}
	second, _ := build.ID()
	if first == second {
		t.Fatal("ambiguous field encoding")
	}
	build.Configuration = []string{"ab", "c"}
	build.Packages = []string{"a", "b", "a"}
	third, _ := build.ID()
	if first != third {
		t.Fatal("set order or duplicates changed identity")
	}
}
