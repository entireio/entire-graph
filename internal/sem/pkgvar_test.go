package sem

import "testing"

func TestCollectPackageVarTypes(t *testing.T) {
	content := `package zerolog

import "example.com/m/internal/json"

var (
	_   encoder = (*json.Encoder)(nil)
	enc         = json.Encoder{}
)

var solo = &json.Encoder{}

func useEnc() {
	local := json.Encoder{} // inside a func body: must NOT be collected as a package var
	_ = local
}
`
	got := collectPackageVarTypes(content)
	if qt, ok := got["enc"]; !ok || qt.alias != "json" || qt.typeName != "Encoder" {
		t.Fatalf("enc: got %+v ok=%v", got["enc"], ok)
	}
	if qt, ok := got["solo"]; !ok || qt.alias != "json" || qt.typeName != "Encoder" {
		t.Fatalf("solo: got %+v ok=%v", got["solo"], ok)
	}
	if _, ok := got["local"]; ok {
		t.Fatalf("function-body var must not be collected as a package var")
	}
}

func TestResolveQualifiedType(t *testing.T) {
	from := SymbolRecord{Language: "Go"}
	jsonEnc := SymbolRecord{ID: "m:Go:internal/json/enc.go:type:Encoder", Language: "Go", Name: "Encoder", Kind: "type", FilePath: "internal/json/enc.go"}
	cborEnc := SymbolRecord{ID: "m:Go:internal/cbor/enc.go:type:Encoder", Language: "Go", Name: "Encoder", Kind: "type", FilePath: "internal/cbor/enc.go"}
	idx := map[string][]SymbolRecord{"Encoder": {jsonEnc, cborEnc}}

	// json.Encoder must resolve to the Encoder in the json/ directory, not cbor's.
	got, ok := resolveQualifiedType(from, pkgQualType{alias: "json", typeName: "Encoder"}, nil, idx, newGoModuleIndex([]goModuleRoot{{Path: "example.com/m"}}))
	if !ok || got.ID != jsonEnc.ID {
		t.Fatalf("expected json Encoder, got %+v ok=%v", got, ok)
	}

	// An alias matching no package directory resolves to nothing (not a wrong guess).
	if _, ok := resolveQualifiedType(from, pkgQualType{alias: "msgpack", typeName: "Encoder"}, nil, idx, newGoModuleIndex([]goModuleRoot{{Path: "example.com/m"}})); ok {
		t.Fatalf("unknown alias must not resolve")
	}

	// A directory match from an incompatible language is not a Go package type.
	pythonEnc := SymbolRecord{ID: "m:Python:internal/json/enc.py:class:Encoder", Language: "Python", Name: "Encoder", Kind: "class", FilePath: "internal/json/enc.py"}
	if _, ok := resolveQualifiedType(from, pkgQualType{alias: "json", typeName: "Encoder"}, nil, map[string][]SymbolRecord{"Encoder": {pythonEnc}}, newGoModuleIndex([]goModuleRoot{{Path: "example.com/m"}})); ok {
		t.Fatalf("Go qualified type resolved to a foreign declaration")
	}
}

// TestQualifiedTypeDirMatchesRequiresInModuleImportPath pins both directions of
// the import-to-directory test. A resolved import path was accepted when the
// declaration's directory was any path SUFFIX of it, so
// `foo "external.example/realpkg"` — which ends in `/realpkg` — satisfied a
// lookup that the repository's own `realpkg/` directory answered, and a
// third-party package bound a local symbol. The rule is now equality against
// the module-relative path: the import must be this module's, and the part
// below the module path must be exactly the declaration's directory.
func TestQualifiedTypeDirMatchesRequiresInModuleImportPath(t *testing.T) {
	const goModule = "example.com/m"
	cases := []struct {
		name       string
		filePath   string
		alias      string
		modules    []goModuleRoot
		importPath []string
		want       bool
	}{
		{
			name:       "in-module import binds its own directory",
			filePath:   "realpkg/thing.go",
			alias:      "realpkg",
			modules:    []goModuleRoot{{Path: goModule}},
			importPath: []string{"example.com/m/realpkg"},
			want:       true,
		},
		{
			name:       "aliased in-module import still binds the imported directory",
			filePath:   "realpkg/thing.go",
			alias:      "foo",
			modules:    []goModuleRoot{{Path: goModule}},
			importPath: []string{"example.com/m/realpkg"},
			want:       true,
		},
		{
			name:       "external import whose last segment matches must not bind",
			filePath:   "realpkg/thing.go",
			alias:      "realpkg",
			modules:    []goModuleRoot{{Path: goModule}},
			importPath: []string{"external.example/realpkg"},
			want:       false,
		},
		{
			name:       "another module ending in the same directory must not bind",
			filePath:   "realpkg/thing.go",
			alias:      "realpkg",
			modules:    []goModuleRoot{{Path: goModule}},
			importPath: []string{"github.com/other/realpkg"},
			want:       false,
		},
		{
			name:       "in-module import of a different package must not bind",
			filePath:   "realpkg/thing.go",
			alias:      "realpkg",
			modules:    []goModuleRoot{{Path: goModule}},
			importPath: []string{"example.com/m/decoy"},
			want:       false,
		},
		{
			name:       "a nested directory is not the imported package",
			filePath:   "outer/realpkg/thing.go",
			alias:      "realpkg",
			modules:    []goModuleRoot{{Path: goModule}},
			importPath: []string{"example.com/m/realpkg"},
			want:       false,
		},
		{
			name:       "a nested module re-roots the directories beneath it",
			filePath:   "tools/lib/lib.go",
			alias:      "lib",
			modules:    []goModuleRoot{{Path: goModule}, {Dir: "tools", Path: "example.com/tool"}},
			importPath: []string{"example.com/tool/lib"},
			want:       true,
		},
		{
			name:       "the root module no longer claims a nested module's directory",
			filePath:   "tools/lib/lib.go",
			alias:      "lib",
			modules:    []goModuleRoot{{Path: goModule}, {Dir: "tools", Path: "example.com/tool"}},
			importPath: []string{"example.com/m/tools/lib"},
			want:       false,
		},
		{
			name:       "a nested module's own root directory is its module path",
			filePath:   "tools/main.go",
			alias:      "tool",
			modules:    []goModuleRoot{{Path: goModule}, {Dir: "tools", Path: "example.com/tool"}},
			importPath: []string{"example.com/tool"},
			want:       true,
		},
		{
			name:       "with no module declared at all a resolved import binds nothing",
			filePath:   "realpkg/thing.go",
			alias:      "realpkg",
			importPath: []string{"example.com/m/realpkg"},
			want:       false,
		},
		{
			name:     "with no resolved import the alias-equals-basename convention still stands",
			filePath: "internal/json/enc.go",
			alias:    "json",
			modules:  []goModuleRoot{{Path: goModule}},
			want:     true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := qualifiedTypeDirMatches(tc.filePath, tc.alias, newGoModuleIndex(tc.modules), tc.importPath); got != tc.want {
				t.Fatalf("qualifiedTypeDirMatches(%q, %q, %v, %v) = %v, want %v",
					tc.filePath, tc.alias, tc.modules, tc.importPath, got, tc.want)
			}
		})
	}
}
