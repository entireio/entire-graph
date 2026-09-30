package sem

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Independent, source-reviewed-only PR307 fixture for a90e732a. The actual
// goEvidenceTypeKey helper receives authored ASTs and a scope with no index or
// reader. No provider, product command, repository input, or model is used.
// The larger case is characterization, NOT a claimed universal output-size
// contract, infinite-loop reproduction, or assertion of a private budget value.
const review307GroupedCaseEnv = "EG_REVIEW307_GROUPED_CASE"
const review307GroupedResultPrefix = "EG_REVIEW307_GROUPED_RESULT="

type review307GroupedResult struct {
	Depth                  int  `json:"depth"`
	GroupedInputBytes      int  `json:"grouped_input_bytes"`
	SeparateInputBytes     int  `json:"separate_input_bytes"`
	GroupedKnown           bool `json:"grouped_known"`
	SeparateKnown          bool `json:"separate_known"`
	GroupedReturnedBytes   int  `json:"grouped_returned_bytes"`
	SeparateReturnedBytes  int  `json:"separate_returned_bytes"`
	EqualWhenBothKnown     bool `json:"equal_when_both_known"`
}

func review307GroupedSource(t *testing.T, depth int, grouped bool) string {
	t.Helper()
	// Only these two fixed depths are permitted, not an environment-controlled
	// stress size. At depth 10 the existing key grammar expands to 472,395 bytes
	// including the outer func(A0), well below one MiB per complete key. This is
	// a source-derived size prediction, not an executed observation by the author.
	if depth != 2 && depth != 10 {
		t.Fatalf("refusing non-fixture alias depth %d", depth)
	}
	var source strings.Builder
	source.WriteString("package fixture\n")
	for i := 0; i < depth; i++ {
		if grouped {
			fmt.Fprintf(&source, "type A%d = func(a, b, c A%d)\n", i, i+1)
		} else {
			fmt.Fprintf(&source, "type A%d = func(A%d, A%d, A%d)\n", i, i+1, i+1, i+1)
		}
	}
	fmt.Fprintf(&source, "type A%d = int\n", depth)
	// Literal compact-input guard. It protects this authored fixture's scope;
	// it is not a product limit or an assertion about arbitrary repository input.
	if source.Len() > 512 {
		t.Fatalf("authored source exceeded the 512-byte fixture guard: %d", source.Len())
	}
	return source.String()
}

func review307GroupedKey(t *testing.T, source string) (string, bool) {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	scope := &goTypeScope{
		packageID:    "fixture",
		declarations: map[string][]goTypeDeclaration{},
	}
	for _, declaration := range parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.TYPE {
			t.Fatal("fixture must contain only type declarations")
		}
		for _, declaration := range general.Specs {
			specification, ok := declaration.(*ast.TypeSpec)
			if !ok || !specification.Assign.IsValid() {
				t.Fatal("fixture must contain only type aliases")
			}
			scope.declarations[specification.Name.Name] = []goTypeDeclaration{{spec: specification, scope: scope}}
		}
	}
	expression, err := parser.ParseExpr("func(A0)")
	if err != nil {
		t.Fatal(err)
	}
	return goEvidenceTypeKey(expression, scope, &goEvidenceWalk{seen: map[string]bool{}}, 0)
}

func TestReview307GroupedTypeChild(t *testing.T) {
	name := os.Getenv(review307GroupedCaseEnv)
	if name == "" {
		t.Skip("test-owned subprocess entry")
	}
	depth := 0
	switch name {
	case "small-equivalence":
		depth = 2
	case "bounded-expansion":
		depth = 10
	default:
		t.Fatalf("unknown authored grouped-type fixture %q", name)
	}
	groupedSource := review307GroupedSource(t, depth, true)
	separateSource := review307GroupedSource(t, depth, false)
	groupedKey, groupedKnown := review307GroupedKey(t, groupedSource)
	separateKey, separateKnown := review307GroupedKey(t, separateSource)
	result := review307GroupedResult{
		Depth:                 depth,
		GroupedInputBytes:     len(groupedSource),
		SeparateInputBytes:    len(separateSource),
		GroupedKnown:          groupedKnown,
		SeparateKnown:         separateKnown,
		GroupedReturnedBytes:  len(groupedKey),
		SeparateReturnedBytes: len(separateKey),
		EqualWhenBothKnown:    groupedKnown && separateKnown && groupedKey == separateKey,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	// Report lengths and known/unknown status, not the expanded strings.
	fmt.Printf("%s%s\n", review307GroupedResultPrefix, encoded)
}

func review307GroupedWithin(t *testing.T, name string) review307GroupedResult {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable,
		"-test.run=^TestReview307GroupedTypeChild$", "-test.count=1", "-test.timeout=4s")
	command.Env = []string{review307GroupedCaseEnv + "=" + name, "GOMAXPROCS=1", "GOGC=50"}
	// Preserve loader/temp essentials, including Windows DLL search through PATH.
	// Do not inherit credentials, product/session configuration, or other helper
	// modes, and never print environment values. The executable is absolute.
	for _, key := range []string{"SystemRoot", "WINDIR", "SystemDrive", "PATH", "PATHEXT", "TEMP", "TMP", "TMPDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			command.Env = append(command.Env, key+"="+value)
		}
	}
	command.WaitDelay = 250 * time.Millisecond
	// CombinedOutput waits for/reaps this child on success, failure, or deadline.
	// Its selected body parses strings only and does not create descendants.
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("grouped-type fixture exceeded the external 5s deadline; owned child killed and waited: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("grouped-type child failed: %v\n%s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		encoded, ok := strings.CutPrefix(line, review307GroupedResultPrefix)
		if !ok {
			continue
		}
		var result review307GroupedResult
		if err := json.Unmarshal([]byte(encoded), &result); err != nil {
			t.Fatalf("invalid grouped-type child result: %v", err)
		}
		return result
	}
	t.Fatalf("grouped-type child emitted no result: %s", output)
	return review307GroupedResult{}
}

func TestReview307GroupedTypeKeysAgreeForSmallEquivalentForms(t *testing.T) {
	// Parameter names and grouping do not change Go function type identity.
	// Catch losing ordinary evidence or giving these equivalent types different
	// keys. The two-level case is deliberately small, not a resource threshold.
	result := review307GroupedWithin(t, "small-equivalence")
	if !result.GroupedKnown || !result.SeparateKnown || !result.EqualWhenBothKnown {
		t.Fatalf("small equivalent function types did not retain matching evidence: %+v", result)
	}
}

func TestReview307GroupedTypeExpansionCharacterization(t *testing.T) {
	result := review307GroupedWithin(t, "bounded-expansion")
	if result.GroupedKnown && result.SeparateKnown && !result.EqualWhenBothKnown {
		t.Fatalf("equivalent known function types have different keys: %+v", result)
	}
	t.Logf("CHARACTERIZATION ONLY: %+v", result)
	t.Log("No output-size cap or required rejection is asserted. Unknown is permitted; returned bytes with known=false are not a complete evidence key. A pass alone does not prove resource containment.")
}
