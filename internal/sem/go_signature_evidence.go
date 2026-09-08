package sem

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type goTypeComparison uint8

const (
	goTypeUnknown goTypeComparison = iota
	goTypeDifferent
	goTypeMatch
)

type goTypeDeclaration struct {
	spec  *ast.TypeSpec
	scope *goTypeScope
}
type goTypeScope struct {
	dotImports   []string
	imports      map[string]string
	declarations map[string][]goTypeDeclaration
	packageID    string
	index        *goFileImports
}

// Package declarations are loaded only when signature matching needs them.
// Each source file is read once. No compiler importer or network fetch is used.
type goSignatureCacheKey struct{ file, signature string }
type goSignatureKey struct {
	text  string
	known bool
}

type goFileImports struct {
	signatures  map[goSignatureCacheKey]goSignatureKey
	mu          sync.Mutex
	readContent contentReader
	modules     goModuleIndex
	filesByDir  map[string][]string
	byFile      map[string]*goTypeScope
	loaded      map[string]bool
	packageDirs map[string]string
}

func newGoFileImports(read contentReader, files []FileRecord, modules goModuleIndex) *goFileImports {
	x := &goFileImports{signatures: map[goSignatureCacheKey]goSignatureKey{}, readContent: read, modules: modules, filesByDir: map[string][]string{}, byFile: map[string]*goTypeScope{}, loaded: map[string]bool{}, packageDirs: map[string]string{}}
	for _, f := range files {
		if !strings.EqualFold(filepath.Ext(f.Path), ".go") {
			continue
		}
		dir := normalizeRepoDir(filepath.Dir(f.Path))
		x.filesByDir[dir] = append(x.filesByDir[dir], f.Path)
		if importPath, ok := modules.importPathFor(dir); ok {
			x.packageDirs[importPath] = dir
		}
	}
	return x
}
func (x *goFileImports) load(dir string) {
	if x.loaded[dir] {
		return
	}
	x.loaded[dir] = true
	packages := map[string]map[string][]goTypeDeclaration{}
	for _, file := range x.filesByDir[dir] {
		content, ok := x.readContent(file)
		if !ok {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, content, parser.SkipObjectResolution)
		if err != nil {
			continue
		} // no identity evidence from malformed source
		declarations := packages[parsed.Name.Name]
		if declarations == nil {
			declarations = map[string][]goTypeDeclaration{}
			packages[parsed.Name.Name] = declarations
		}
		id := "repo:" + dir + ":" + parsed.Name.Name
		if modulePath, ok := x.modules.importPathFor(dir); ok {
			id = modulePath
			if strings.HasSuffix(parsed.Name.Name, "_test") {
				id += "#" + parsed.Name.Name
			}
		}
		scope := &goTypeScope{imports: map[string]string{}, declarations: declarations, packageID: id, index: x}
		x.byFile[file] = scope
		for _, imp := range parsed.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			name := path.Base(importPath)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			if name == "_" {
				continue
			}
			if name == "." {
				scope.dotImports = append(scope.dotImports, importPath)
				continue
			}
			if _, exists := scope.imports[name]; exists {
				scope.imports[name] = ""
			} else {
				scope.imports[name] = importPath
			}
		}
		for _, decl := range parsed.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				if typ, ok := spec.(*ast.TypeSpec); ok {
					declarations[typ.Name.Name] = append(declarations[typ.Name.Name], goTypeDeclaration{typ, scope})
				}
			}
		}
	}
}

// packageType resolves only repository declarations. An external package is
// unknown; looking it up must never invoke a compiler importer or fetch source.
func (x *goFileImports) packageType(importPath, name string) (*goTypeScope, bool) {
	dir, known := x.packageDirs[importPath]
	if !known {
		return nil, false
	}
	x.load(dir)
	if !ast.IsExported(name) {
		return nil, true
	}
	var target *goTypeScope
	for _, file := range x.filesByDir[dir] {
		other := x.byFile[file]
		if other == nil || strings.Contains(other.packageID, "#") || len(other.declarations[name]) == 0 {
			continue
		}
		if target != nil && target.packageID != other.packageID {
			return nil, true
		}
		target = other
	}
	return target, true
}

func (x *goFileImports) forFile(file string) *goTypeScope {
	if x == nil || x.readContent == nil {
		return nil
	}
	x.load(normalizeRepoDir(filepath.Dir(file)))
	return x.byFile[file]
}
func (x *goFileImports) signaturesMatch(want, got SymbolRecord) bool {
	if x == nil {
		return false
	}
	// Relation workers share this index. Hold the lock through lazy package and
	// alias resolution so no worker observes a partially populated declaration set.
	x.mu.Lock()
	defer x.mu.Unlock()
	left, right := x.signatureKey(want), x.signatureKey(got)
	return left.known && right.known && left.text == right.text
}
func (x *goFileImports) signatureKey(symbol SymbolRecord) goSignatureKey {
	cacheKey := goSignatureCacheKey{symbol.FilePath, symbol.Signature}
	if result, ok := x.signatures[cacheKey]; ok {
		return result
	}
	var result goSignatureKey
	if scope := x.forFile(symbol.FilePath); scope != nil {
		result.text, result.known = goSignatureEvidenceKey(symbol.Signature, scope)
	}
	x.signatures[cacheKey] = result
	return result
}

func compareGoSignatures(want, got string, a, b *goTypeScope) goTypeComparison {
	left, lok := goSignatureEvidenceKey(want, a)
	right, rok := goSignatureEvidenceKey(got, b)
	if !lok || !rok {
		return goTypeUnknown
	}
	if left != right {
		return goTypeDifferent
	}
	return goTypeMatch
}
func goSignatureEvidenceKey(signature string, scope *goTypeScope) (string, bool) {
	normalized, ok := goNormalizedMethodSignature(signature)
	if !ok {
		return "", false
	}
	expr, err := parser.ParseExpr("func" + normalized)
	if err != nil {
		return "", false
	}
	return goEvidenceTypeKey(expr, scope, map[string]bool{}, 0)
}
func goEvidenceNamedType(name string, scope *goTypeScope, seen map[string]bool, depth int) (string, bool) {
	if depth > 64 {
		return "", false
	}
	if scope != nil {
		if decls := scope.declarations[name]; len(decls) > 0 {
			if len(decls) != 1 {
				return "", false
			} // build variants are not interchangeable evidence
			decl := decls[0]
			if decl.spec.TypeParams != nil {
				return "", false
			}
			id := scope.packageID + "." + name
			if !decl.spec.Assign.IsValid() {
				return "named(" + id + ")", true
			}
			if seen[id] {
				return "", false
			}
			seen[id] = true
			defer delete(seen, id)
			return goEvidenceTypeKey(decl.spec.Type, decl.scope, seen, depth+1)
		}
	}
	if scope != nil && len(scope.dotImports) > 0 {
		if scope.index == nil {
			return "", false
		}
		var found *goTypeScope
		for _, importPath := range scope.dotImports {
			target, known := scope.index.packageType(importPath, name)
			if !known {
				return "", false
			}
			if target != nil {
				if found != nil {
					return "", false
				}
				found = target
			}
		}
		if found != nil {
			return goEvidenceNamedType(name, found, seen, depth+1)
		}
	}

	switch name {
	case "byte":
		return "uint8", true
	case "rune":
		return "int32", true
	case "any":
		return "interface{}", true
	case "bool", "string", "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "float32", "float64", "complex64", "complex128", "error":
		return name, true
	}
	return "", false
}
func goEvidenceTypeKey(expr ast.Expr, scope *goTypeScope, seen map[string]bool, depth int) (string, bool) {
	if depth > 64 {
		return "", false
	}
	key := func(e ast.Expr) (string, bool) { return goEvidenceTypeKey(e, scope, seen, depth+1) }
	switch e := expr.(type) {
	case *ast.Ident:
		return goEvidenceNamedType(e.Name, scope, seen, depth)
	case *ast.SelectorExpr:
		qualifier, ok := e.X.(*ast.Ident)
		if !ok || scope == nil {
			return "", false
		}
		importPath := scope.imports[qualifier.Name]
		if importPath == "" {
			return "", false
		}
		if scope.index != nil {
			if target, known := scope.index.packageType(importPath, e.Sel.Name); known {
				if target == nil {
					return "", false
				}
				return goEvidenceNamedType(e.Sel.Name, target, seen, depth+1)
			}
		}
		return "named(" + importPath + "." + e.Sel.Name + ")", true
	case *ast.ParenExpr:
		return key(e.X)
	case *ast.StarExpr:
		k, ok := key(e.X)
		return "*" + k, ok
	case *ast.Ellipsis:
		k, ok := key(e.Elt)
		return "..." + k, ok
	case *ast.ArrayType:
		k, ok := key(e.Elt)
		if !ok {
			return "", false
		}
		if e.Len == nil {
			return "[]" + k, true
		}
		n, ok := e.Len.(*ast.BasicLit)
		if !ok || n.Kind != token.INT {
			return "", false
		}
		length, err := strconv.ParseUint(n.Value, 0, 64)
		if err != nil {
			return "", false
		}
		return "[" + strconv.FormatUint(length, 10) + "]" + k, true
	case *ast.MapType:
		a, ok := key(e.Key)
		b, bok := key(e.Value)
		return "map[" + a + "]" + b, ok && bok
	case *ast.ChanType:
		k, ok := key(e.Value)
		return "chan" + strconv.Itoa(int(e.Dir)) + "(" + k + ")", ok
	case *ast.InterfaceType:
		if e.Methods == nil || len(e.Methods.List) == 0 {
			return "interface{}", true
		}
		return "", false
	case *ast.FuncType:
		if e.TypeParams != nil {
			return "", false
		}
		fields := func(list *ast.FieldList) (string, bool) {
			var keys []string
			if list != nil {
				for _, field := range list.List {
					k, ok := key(field.Type)
					if !ok {
						return "", false
					}
					count := len(field.Names)
					if count == 0 {
						count = 1
					}
					for i := 0; i < count; i++ {
						keys = append(keys, k)
					}
				}
			}
			return strings.Join(keys, ","), true
		}
		params, ok := fields(e.Params)
		results, rok := fields(e.Results)
		return "func(" + params + ")(" + results + ")", ok && rok
	}
	return "", false
}
