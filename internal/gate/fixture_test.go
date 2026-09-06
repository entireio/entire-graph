package gate

// The tests in this package run against synthetic records rather than a real
// repository. That is the point of the collect/pure split: no git, no
// tree-sitter, no CGO, and a suite that finishes in milliseconds so it is
// actually run between edits.
//
// The fixture is a small call chain with one test file attached:
//
//	Protected -> Require -> VerifyToken <- TestVerifyToken
//	Load                                (no dependents, no test)
//
// It is enough to exercise hop depth, coverage resolution, name/path
// disambiguation and the verdict rules without anyone having to hold a larger
// graph in their head while reading a failure.

const (
	idVerify    = "repo:Go:pkg/auth.go:function:VerifyToken"
	idRequire   = "repo:Go:pkg/mw.go:function:Require"
	idProtected = "repo:Go:pkg/router.go:function:Protected"
	idTest      = "repo:Go:pkg/auth_test.go:function:TestVerifyToken"
	idLoad      = "repo:Go:pkg/config.go:function:Load"
	// idOtherLoad is a second Load in a different file, so Resolve has an
	// ambiguous name to disambiguate.
	idOtherLoad = "repo:Go:pkg/db/config.go:function:Load"
)

func fixtureSymbols() []Symbol {
	return []Symbol{
		{ID: idVerify, Name: "VerifyToken", Path: "pkg/auth.go", Line: 88, Kind: "function"},
		{ID: idRequire, Name: "Require", Path: "pkg/mw.go", Line: 41, Kind: "function"},
		{ID: idProtected, Name: "Protected", Path: "pkg/router.go", Line: 98, Kind: "function"},
		{ID: idTest, Name: "TestVerifyToken", Path: "pkg/auth_test.go", Line: 12, Kind: "function"},
		{ID: idLoad, Name: "Load", Path: "pkg/config.go", Line: 31, Kind: "function"},
		{ID: idOtherLoad, Name: "Load", Path: "pkg/db/config.go", Line: 7, Kind: "function"},
	}
}

func fixtureRelations() []Relation {
	return []Relation{
		{FromID: idRequire, ToID: idVerify, Type: "CALLS"},
		{FromID: idProtected, ToID: idRequire, Type: "CALLS"},
		{FromID: idTest, ToID: idVerify, Type: "CALLS"},
		// Structural edges must not count as dependencies: a file containing a
		// symbol is not a caller of it.
		{FromID: "repo:file:pkg/auth.go", ToID: idVerify, Type: "CONTAINS"},
		{FromID: "repo:file:pkg/auth.go", ToID: idVerify, Type: "DEFINES"},
	}
}

func fixtureIndex() *Index {
	return NewIndex(fixtureSymbols(), fixtureRelations())
}

func entity(name, path string, line int, change ChangeType) ChangedEntity {
	return ChangedEntity{
		Anchor:     Anchor{Name: name, Path: path, Line: line},
		Kind:       "function",
		ChangeType: change,
	}
}

func names(symbols []Symbol) []string {
	out := make([]string, 0, len(symbols))
	for _, s := range symbols {
		out = append(out, s.Name)
	}
	return out
}
