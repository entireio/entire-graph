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

const goEvidenceBudgetCaseEnv = "EG_GO_EVIDENCE_BUDGET_CASE"
const goEvidenceBudgetResultPrefix = "EG_GO_EVIDENCE_BUDGET_RESULT="

type goEvidenceBudgetResult struct {
	Known         bool `json:"known"`
	ReturnedBytes int  `json:"returned_bytes"`
}

func goEvidenceBudgetGroupedSource(t *testing.T) string {
	t.Helper()
	const depth = 11
	var source strings.Builder
	source.WriteString("package fixture\n")
	for i := 0; i < depth; i++ {
		fmt.Fprintf(&source, "type A%d = func(a, b, c A%d)\n", i, i+1)
	}
	fmt.Fprintf(&source, "type A%d = int\n", depth)
	if source.Len() > 512 {
		t.Fatalf("authored source exceeded the 512-byte fixture guard: %d", source.Len())
	}
	return source.String()
}

func TestGoEvidenceKeyBudgetChild(t *testing.T) {
	if os.Getenv(goEvidenceBudgetCaseEnv) == "" {
		t.Skip("test-owned subprocess entry")
	}
	key, known := review307GroupedKey(t, goEvidenceBudgetGroupedSource(t))
	encoded, err := json.Marshal(goEvidenceBudgetResult{Known: known, ReturnedBytes: len(key)})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", goEvidenceBudgetResultPrefix, encoded)
}

func goEvidenceBudgetWithin(t *testing.T) goEvidenceBudgetResult {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable,
		"-test.run=^TestGoEvidenceKeyBudgetChild$", "-test.count=1", "-test.timeout=4s")
	command.Env = []string{goEvidenceBudgetCaseEnv + "=1", "GOMAXPROCS=1", "GOGC=50"}
	for _, key := range []string{"SystemRoot", "WINDIR", "SystemDrive", "PATH", "PATHEXT", "TEMP", "TMP", "TMPDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			command.Env = append(command.Env, key+"="+value)
		}
	}
	command.WaitDelay = 250 * time.Millisecond
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("n11 grouped-type fixture exceeded the external 5s deadline; owned child killed and waited: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("n11 grouped-type child failed: %v\n%s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		encoded, ok := strings.CutPrefix(line, goEvidenceBudgetResultPrefix)
		if !ok {
			continue
		}
		var result goEvidenceBudgetResult
		if err := json.Unmarshal([]byte(encoded), &result); err != nil {
			t.Fatalf("invalid n11 grouped-type child result: %v", err)
		}
		return result
	}
	t.Fatalf("n11 grouped-type child emitted no result: %s", output)
	return goEvidenceBudgetResult{}
}

func TestGoEvidenceKeyBudgetRejectsFixedGroupedExpansion(t *testing.T) {
	result := goEvidenceBudgetWithin(t)
	if result.Known || result.ReturnedBytes != 0 {
		t.Fatalf("n11 grouped expansion must be unknown with an empty key, got %+v", result)
	}
}

func TestGoEvidenceKeyBuilderBudgetBoundaryAndStickyExhaustion(t *testing.T) {
	exact := &goEvidenceWalk{constructed: goEvidenceKeyByteBudget - 3}
	if got, ok := exact.build("a", "bc"); !ok || got != "abc" {
		t.Fatalf("exactly sufficient budget rejected: got %q, known=%v", got, ok)
	}
	if exact.constructed != goEvidenceKeyByteBudget || exact.exhausted {
		t.Fatalf("exact budget accounting = %d, exhausted=%v", exact.constructed, exact.exhausted)
	}

	short := &goEvidenceWalk{constructed: goEvidenceKeyByteBudget - 2}
	if got, ok := short.build("a", "bc"); ok || got != "" {
		t.Fatalf("insufficient budget returned partial key: got %q, known=%v", got, ok)
	}
	if !short.exhausted || short.constructed != goEvidenceKeyByteBudget-2 {
		t.Fatalf("failed reservation must be sticky without spending: %+v", short)
	}
	if got, ok := short.build(""); ok || got != "" {
		t.Fatalf("exhausted builder recovered: got %q, known=%v", got, ok)
	}
}

func TestGoEvidenceKeyLengthPreflightRejectsScalarOverflow(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	if _, ok := goEvidenceAddRepeatedLength(0, 2, maxInt, goEvidenceKeyByteBudget); ok {
		t.Fatal("scalar count overflow passed preflight")
	}
	if got, ok := goEvidenceAddRepeatedLength(goEvidenceKeyByteBudget-4, 2, 2, goEvidenceKeyByteBudget); !ok || got != goEvidenceKeyByteBudget {
		t.Fatalf("exact repeated length rejected: got %d, ok=%v", got, ok)
	}
	if _, ok := goEvidenceAddRepeatedLength(goEvidenceKeyByteBudget-3, 2, 2, goEvidenceKeyByteBudget); ok {
		t.Fatal("over-budget repeated length passed preflight")
	}
}

func TestGoEvidenceKeyBudgetPreservesCanonicalBuilders(t *testing.T) {
	defined := &ast.TypeSpec{Name: ast.NewIdent("Local"), Type: ast.NewIdent("int")}
	alias := &ast.TypeSpec{Name: ast.NewIdent("Alias"), Assign: token.Pos(1), Type: ast.NewIdent("int")}
	scope := &goTypeScope{
		packageID: "fixture",
		imports:   map[string]string{"pkg": "example.com/p"},
		declarations: map[string][]goTypeDeclaration{
			"Local": {{spec: defined}},
			"Alias": {{spec: alias}},
		},
	}
	cases := []struct {
		expression string
		want       string
	}{
		{"Alias", "int"},
		{"Local", "named(fixture.Local)"},
		{"pkg.T", "named(example.com/p.T)"},
		{"*int", "*int"},
		{"...int", "...int"},
		{"[]int", "[]int"},
		{"[0xc]int", "[12]int"},
		{"map[string]int", "map[string]int"},
		{"chan int", "chan3(int)"},
		{"<-chan int", "chan2(int)"},
		{"chan<- int", "chan1(int)"},
		{"func(a, b int, values ...string) (map[string]int, chan<- bool)", "func(int,int,...string)(map[string]int,chan1(bool))"},
	}
	for _, test := range cases {
		t.Run(test.expression, func(t *testing.T) {
			expression := goEvidenceBudgetExpression(t, test.expression)
			got, known := goEvidenceTypeKey(expression, scope, &goEvidenceWalk{}, 0)
			if !known || got != test.want {
				t.Fatalf("canonical key = %q, known=%v; want %q", got, known, test.want)
			}
		})
	}
}

func TestGoEvidenceKeyBudgetPreservesSmallGroupedEquivalence(t *testing.T) {
	grouped, groupedKnown := review307GroupedKey(t, review307GroupedSource(t, 2, true))
	separate, separateKnown := review307GroupedKey(t, review307GroupedSource(t, 2, false))
	if !groupedKnown || !separateKnown || grouped != separate {
		t.Fatalf("small grouped and separate forms diverged: grouped=(%q,%v), separate=(%q,%v)", grouped, groupedKnown, separate, separateKnown)
	}
}

func TestGoEvidenceKeyUnknownChildrenReturnNoPartialKey(t *testing.T) {
	scope := goEvidenceBudgetAliasScope(t, 11)
	cases := []string{
		"*Missing",
		"...Missing",
		"[]Missing",
		"[2]Missing",
		"map[Missing]int",
		"map[int]Missing",
		"chan Missing",
		"func(Missing) int",
		"func(int) Missing",
		"unknown.T",
	}
	for _, source := range cases {
		t.Run(source, func(t *testing.T) {
			expression := goEvidenceBudgetExpression(t, source)
			got, known := goEvidenceTypeKey(expression, scope, &goEvidenceWalk{}, 0)
			if known || got != "" {
				t.Fatalf("unknown child produced partial key %q, known=%v", got, known)
			}
		})
	}

	for _, source := range []string{"map[Missing]A0", "func(Missing) A0"} {
		expression, err := parser.ParseExpr(source)
		if err != nil {
			t.Fatal(err)
		}
		walk := &goEvidenceWalk{}
		got, known := goEvidenceTypeKey(expression, scope, walk, 0)
		if known || got != "" || walk.exhausted {
			t.Fatalf("%s did not short-circuit its unknown first child: key=%q, known=%v, exhausted=%v", source, got, known, walk.exhausted)
		}
	}
}

func goEvidenceBudgetExpression(t *testing.T, source string) ast.Expr {
	t.Helper()
	if element, ok := strings.CutPrefix(source, "..."); ok {
		expression, err := parser.ParseExpr(element)
		if err != nil {
			t.Fatal(err)
		}
		return &ast.Ellipsis{Elt: expression}
	}
	expression, err := parser.ParseExpr(source)
	if err != nil {
		t.Fatal(err)
	}
	return expression
}

func TestGoEvidenceKeyBudgetIsPerSignature(t *testing.T) {
	scope := goEvidenceBudgetAliasScope(t, 11)
	if got, known := goSignatureEvidenceKey("Run(A0)", scope); known || got != "" {
		t.Fatalf("oversized signature returned key %q, known=%v", got, known)
	}
	if got, known := goSignatureEvidenceKey("Run(int) int", scope); !known || got != "func(int)(int)" {
		t.Fatalf("fresh small signature inherited exhausted budget: key=%q, known=%v", got, known)
	}
}

func goEvidenceBudgetAliasScope(t *testing.T, depth int) *goTypeScope {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), "fixture.go", goEvidenceBudgetGroupedSource(t), parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	scope := &goTypeScope{packageID: "fixture", declarations: map[string][]goTypeDeclaration{}}
	for _, declaration := range parsed.Decls {
		general := declaration.(*ast.GenDecl)
		for _, declaration := range general.Specs {
			specification := declaration.(*ast.TypeSpec)
			scope.declarations[specification.Name.Name] = []goTypeDeclaration{{spec: specification, scope: scope}}
		}
	}
	if depth != 11 {
		t.Fatalf("unsupported authored alias depth %d", depth)
	}
	return scope
}
