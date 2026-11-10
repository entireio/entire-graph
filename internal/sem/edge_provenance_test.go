package sem

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Every relation already carried the evidence for how it was established.
// What it lacked was the conclusion, as one field a caller can filter on. These
// tests hold that label to the two things that make it worth trusting: it never
// promotes a guess to a fact, and it cannot quietly go stale as the providers
// change underneath it.

func TestEdgeProvenanceNeverPromotesAGuessToAFact(t *testing.T) {
	cases := []struct {
		relation   string
		resolution string
		want       string
		why        string
	}{
		{"CALLS", "exact", ProvenanceExtracted, "a resolved call is a resolved call"},
		{"IMPORTS", "import_resolved", ProvenanceExtracted, "the import graph identified the target"},
		{"CALLS", "type_inferred", ProvenanceInferred, "a definite target reached by inference is not a direct reference"},
		{"CALLS", "name_only", ProvenanceAmbiguous, "a bare name is not a target"},
		{"CALLS", "import_external", ProvenanceAmbiguous, "the definition is outside this repository"},
		{"CALLS", "", ProvenanceAmbiguous, "no stated resolution is not evidence"},
		{"CALLS", "a_value_invented_later", ProvenanceAmbiguous, "an unknown resolution must fail toward ambiguous"},

		// The judgement this label exists for: a heuristic edge is INFERRED
		// even when its resolution reads exact. HANDLES_ROUTE exists because a
		// route string matched; calling it EXTRACTED would promote a pattern
		// match to a resolved reference.
		{"HANDLES_ROUTE", "exact", ProvenanceInferred, "a route match is a pattern, however exact the target"},
		{"HTTP_CALLS", "exact", ProvenanceInferred, "shape-matched HTTP calls are patterns"},
		{"TESTS", "exact", ProvenanceInferred, "test association is inferred"},
		{"SIMILAR_TO", "full", ProvenanceInferred, "similarity is never extraction"},
	}
	for _, tc := range cases {
		if got := EdgeProvenance(tc.relation, tc.resolution); got != tc.want {
			t.Fatalf("%s/%s = %s, want %s — %s", tc.relation, tc.resolution, got, tc.want, tc.why)
		}
	}
}

// Global heuristics must be inferred, but protocol-specific inference must not
// exempt RPC relations from per-language capability extraction guards.
func TestProvenancePreservesLanguageSpecificCapabilities(t *testing.T) {
	for _, relation := range Capabilities().HeuristicRelationTypes {
		if !heuristicEdgeTypes[relation] {
			t.Errorf("global heuristic %s is not inferred", relation)
		}
	}
	for _, relation := range []string{"HANDLES_GRPC", "HANDLES_GRAPHQL", "HANDLES_TRPC"} {
		if !heuristicEdgeTypes[relation] {
			t.Errorf("protocol relation %s is not inferred", relation)
		}
		if contains(Capabilities().HeuristicRelationTypes, relation) {
			t.Errorf("%s must remain governed by per-language capability declarations", relation)
		}
	}
}

// This is a constant-site lint, not a proof of all dynamically computed values.
// It covers keyed literals, post-construction writes, and local resolution
// initializers, including constant aliases and concatenations. Unknown runtime
// values still fail closed to AMBIGUOUS in EdgeProvenance.
func TestLiteralResolutionValuesInProductionAreClassified(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", entry.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for value := range literalResolutionValues(file) {
			found[value] = true
		}
	}
	if len(found) == 0 {
		t.Fatal("found no resolution constants")
	}
	var missing []string
	for value := range found {
		if _, ok := knownResolutions[value]; !ok && value != "" {
			missing = append(missing, value)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("unclassified resolution constants: %v", missing)
	}
}

func literalResolutionValues(file *ast.File) map[string]bool {
	found := map[string]bool{}
	var constantString func(ast.Expr, map[ast.Expr]bool) (string, bool)
	constantString = func(expr ast.Expr, seen map[ast.Expr]bool) (string, bool) {
		if expr == nil || seen[expr] {
			return "", false
		}
		seen[expr] = true
		switch x := expr.(type) {
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				value, err := strconv.Unquote(x.Value)
				return value, err == nil
			}
		case *ast.ParenExpr:
			return constantString(x.X, seen)
		case *ast.BinaryExpr:
			if x.Op == token.ADD {
				left, lok := constantString(x.X, seen)
				right, rok := constantString(x.Y, seen)
				return left + right, lok && rok
			}
		case *ast.Ident:
			if x.Obj != nil {
				switch decl := x.Obj.Decl.(type) {
				case *ast.ValueSpec:
					for i, name := range decl.Names {
						if name.Name == x.Name && i < len(decl.Values) {
							return constantString(decl.Values[i], seen)
						}
					}
				case *ast.AssignStmt:
					for i, lhs := range decl.Lhs {
						if id, ok := lhs.(*ast.Ident); ok && id.Name == x.Name && i < len(decl.Rhs) {
							return constantString(decl.Rhs[i], seen)
						}
					}
				}
			}
		}
		return "", false
	}
	add := func(expr ast.Expr) {
		if value, ok := constantString(expr, map[ast.Expr]bool{}); ok {
			found[value] = true
		}
	}
	isResolution := func(expr ast.Expr) bool {
		switch x := expr.(type) {
		case *ast.Ident:
			return strings.EqualFold(x.Name, "resolution")
		case *ast.SelectorExpr:
			return x.Sel.Name == "Resolution"
		}
		return false
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.KeyValueExpr:
			if isResolution(x.Key) {
				add(x.Value)
			}
		case *ast.AssignStmt:
			for i, lhs := range x.Lhs {
				if isResolution(lhs) && i < len(x.Rhs) {
					add(x.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			for i, name := range x.Names {
				if isResolution(name) && i < len(x.Values) {
					add(x.Values[i])
				}
			}
		}
		return true
	})
	return found
}

func TestResolutionConstantGuardSeesAssignmentsAndAliases(t *testing.T) {
	source := "package fixture\nconst inferred = `type_` + \"inferred\"\nfunc f() {\n resolution := \"exact\"\n r := RelationRecord{Resolution: resolution}\n r.Resolution = inferred\n r.Resolution = `new_value`\n _ = \"Resolution: \\\"comment_only\\\"\"\n}"
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := literalResolutionValues(file)
	if len(found) != 3 || !found["exact"] || !found["type_inferred"] || !found["new_value"] {
		t.Fatalf("resolution constants = %v", found)
	}
}

func TestProvenanceValuesAreTheThreeDocumentedOnes(t *testing.T) {
	// A consumer filters on these strings. Adding a fourth silently would break
	// every filter that enumerates them.
	allowed := map[string]bool{ProvenanceExtracted: true, ProvenanceInferred: true, ProvenanceAmbiguous: true}
	for _, provenance := range knownResolutions {
		if !allowed[provenance] {
			t.Fatalf("knownResolutions maps to %q, which is not one of the three documented labels", provenance)
		}
	}
	for _, resolution := range []string{"exact", "name_only", "", "nonsense"} {
		for _, relation := range []string{"CALLS", "HANDLES_ROUTE", "DEFINES"} {
			if got := EdgeProvenance(relation, resolution); !allowed[got] {
				t.Fatalf("EdgeProvenance(%q,%q) = %q, outside the documented set", relation, resolution, got)
			}
		}
	}
}

// "package" reads like a package-wide guess and is not one. Both producers emit
// it only after narrowing to a single symbol — Go's same-package resolution
// when len(samePkg) == 1, Dart's same-directory resolution on a single match —
// and shallowCallRelationRetained independently groups it with "exact" and
// "import_resolved" as the single-target, high-precision resolutions.
//
// Classifying it as ambiguous mislabelled a large share of Go and Dart CALLS
// edges, which is the common case the label exists to be useful for.
func TestPackageResolutionIsExtractedNotAmbiguous(t *testing.T) {
	if got := EdgeProvenance("CALLS", "package"); got != ProvenanceExtracted {
		t.Fatalf("EdgeProvenance(CALLS, package) = %s, want %s", got, ProvenanceExtracted)
	}
	// The three the provider itself treats as single-target must agree.
	for _, resolution := range []string{"exact", "package", "import_resolved"} {
		if got := EdgeProvenance("CALLS", resolution); got != ProvenanceExtracted {
			t.Fatalf("%q is one of the provider's high-precision resolutions but is labelled %s", resolution, got)
		}
	}
	// And the genuinely unpinned ones must not have drifted with it.
	for _, resolution := range []string{"name_only", "file", "pattern", "signature"} {
		if got := EdgeProvenance("CALLS", resolution); got != ProvenanceAmbiguous {
			t.Fatalf("%q is unpinned but is labelled %s", resolution, got)
		}
	}
}

// The classification here and the provider's own single-target list must not
// drift apart: they are two statements about the same property, and the defect
// this test guards was them disagreeing.
func TestHighPrecisionResolutionsAgreeWithTheProvider(t *testing.T) {
	for _, resolution := range []string{"exact", "package", "import_resolved"} {
		if !shallowCallRelationRetained(resolution) {
			t.Fatalf("%q is no longer retained by shallow call resolution; the provenance table still calls it extracted", resolution)
		}
		if got := EdgeProvenance("CALLS", resolution); got != ProvenanceExtracted {
			t.Fatalf("%q is retained as high-precision by the provider but labelled %s", resolution, got)
		}
	}
}
