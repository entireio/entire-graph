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
	walk := &goEvidenceWalk{seen: map[string]bool{}}
	expression, ok := walk.build("func", normalized)
	if !ok {
		return "", false
	}
	expr, err := parser.ParseExpr(expression)
	if err != nil {
		return "", false
	}
	return goEvidenceTypeKey(expr, scope, walk, 0)
}

// goEvidenceStepBudget bounds how many type nodes one signature's evidence key
// may visit. The depth cap alone does not bound the work: an alias reached
// through several fields is expanded once per reference, so
// `type A0 = func(A1, A1, A1)`, `type A1 = func(A2, A2, A2)`, ... visits 3^N
// nodes, and the depth cap only stops N at 32. One such chain in a package with
// a matching interface stalled the snapshot for good. Past the budget the key
// is unknown, exactly what the depth cap already answers; a signature that
// expands within it keeps its key byte for byte.
const goEvidenceStepBudget = 1 << 16

// goEvidenceKeyByteBudget bounds aggregate key bytes constructed while
// resolving one signature. It is deliberately a conservative containment
// limit, not a promise that signatures below it use a particular amount of
// heap or time. Every variable-size key construction reserves its complete
// output before allocating it.
const goEvidenceKeyByteBudget = 1 << 20

// goEvidenceWalk is the state shared by one signature's evidence expansion: the
// alias path being expanded, for cycles, and the resources it has spent.
type goEvidenceWalk struct {
	seen        map[string]bool
	steps       int
	constructed int
	exhausted   bool
}

// step spends one node of the budget and reports whether the walk may go on.
func (w *goEvidenceWalk) step() bool {
	if w == nil || w.exhausted {
		return false
	}
	w.steps++
	if w.steps > goEvidenceStepBudget {
		w.exhausted = true
		return false
	}
	return true
}

// goEvidenceAddRepeatedLength adds unitLength*count without overflowing and
// without admitting a result beyond limit. Counts are scalar so callers can
// reject oversized repetition before constructing slices or strings.
func goEvidenceAddRepeatedLength(total, unitLength, count, limit int) (int, bool) {
	if total < 0 || unitLength < 0 || count < 0 || limit < 0 || total > limit {
		return 0, false
	}
	if unitLength != 0 && count > (limit-total)/unitLength {
		return 0, false
	}
	return total + unitLength*count, true
}

func (w *goEvidenceWalk) remaining() (int, bool) {
	if w == nil || w.exhausted {
		return 0, false
	}
	if w.constructed < 0 || w.constructed > goEvidenceKeyByteBudget {
		w.exhausted = true
		return 0, false
	}
	return goEvidenceKeyByteBudget - w.constructed, true
}

// reserve charges a complete construction before its allocation. A failed
// reservation is sticky and never spends a partial budget.
func (w *goEvidenceWalk) reserve(length int) bool {
	remaining, ok := w.remaining()
	if !ok || length < 0 || length > remaining {
		if w != nil {
			w.exhausted = true
		}
		return false
	}
	w.constructed += length
	return true
}

func (w *goEvidenceWalk) build(parts ...string) (string, bool) {
	remaining, ok := w.remaining()
	if !ok {
		return "", false
	}
	length := 0
	for _, part := range parts {
		length, ok = goEvidenceAddRepeatedLength(length, len(part), 1, remaining)
		if !ok {
			w.exhausted = true
			return "", false
		}
	}
	if !w.reserve(length) {
		return "", false
	}
	var result strings.Builder
	result.Grow(length)
	for _, part := range parts {
		result.WriteString(part)
	}
	return result.String(), true
}

type goEvidenceRepeatedKey struct {
	key   string
	count int
}

// buildFieldList consumes a length already preflighted by the field walker.
// It performs the one reservation for this join, avoiding a second charge for
// the same output.
func (w *goEvidenceWalk) buildFieldList(parts []goEvidenceRepeatedKey, length int) (string, bool) {
	if !w.reserve(length) {
		return "", false
	}
	var result strings.Builder
	result.Grow(length)
	written := false
	for _, part := range parts {
		for i := 0; i < part.count; i++ {
			if written {
				result.WriteByte(',')
			}
			result.WriteString(part.key)
			written = true
		}
	}
	return result.String(), true
}

func (w *goEvidenceWalk) buildArrayKey(length uint64, key string) (string, bool) {
	var number [20]byte
	digits := strconv.AppendUint(number[:0], length, 10)
	remaining, ok := w.remaining()
	if !ok {
		return "", false
	}
	total := 0
	for _, size := range [4]int{1, len(digits), 1, len(key)} {
		total, ok = goEvidenceAddRepeatedLength(total, size, 1, remaining)
		if !ok {
			w.exhausted = true
			return "", false
		}
	}
	if !w.reserve(total) {
		return "", false
	}
	var result strings.Builder
	result.Grow(total)
	result.WriteByte('[')
	result.Write(digits)
	result.WriteByte(']')
	result.WriteString(key)
	return result.String(), true
}

func (w *goEvidenceWalk) buildChannelKey(direction ast.ChanDir, key string) (string, bool) {
	var number [20]byte
	digits := strconv.AppendInt(number[:0], int64(direction), 10)
	remaining, ok := w.remaining()
	if !ok {
		return "", false
	}
	total := 0
	for _, size := range [5]int{4, len(digits), 1, len(key), 1} {
		total, ok = goEvidenceAddRepeatedLength(total, size, 1, remaining)
		if !ok {
			w.exhausted = true
			return "", false
		}
	}
	if !w.reserve(total) {
		return "", false
	}
	var result strings.Builder
	result.Grow(total)
	result.WriteString("chan")
	result.Write(digits)
	result.WriteByte('(')
	result.WriteString(key)
	result.WriteByte(')')
	return result.String(), true
}

func goEvidenceNamedType(name string, scope *goTypeScope, walk *goEvidenceWalk, depth int) (string, bool) {
	if walk == nil {
		return "", false
	}
	if depth > 64 {
		walk.exhausted = true
		return "", false
	}
	if !walk.step() {
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
			id, ok := walk.build(scope.packageID, ".", name)
			if !ok {
				return "", false
			}
			if !decl.spec.Assign.IsValid() {
				return walk.build("named(", id, ")")
			}
			if walk.seen[id] {
				return "", false
			}
			if walk.seen == nil {
				walk.seen = map[string]bool{}
			}
			walk.seen[id] = true
			defer delete(walk.seen, id)
			return goEvidenceTypeKey(decl.spec.Type, decl.scope, walk, depth+1)
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
			return goEvidenceNamedType(name, found, walk, depth+1)
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
func goEvidenceTypeKey(expr ast.Expr, scope *goTypeScope, walk *goEvidenceWalk, depth int) (string, bool) {
	if walk == nil {
		return "", false
	}
	if depth > 64 {
		walk.exhausted = true
		return "", false
	}
	if !walk.step() {
		return "", false
	}
	key := func(e ast.Expr) (string, bool) { return goEvidenceTypeKey(e, scope, walk, depth+1) }
	switch e := expr.(type) {
	case *ast.Ident:
		return goEvidenceNamedType(e.Name, scope, walk, depth)
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
				return goEvidenceNamedType(e.Sel.Name, target, walk, depth+1)
			}
		}
		id, ok := walk.build(importPath, ".", e.Sel.Name)
		if !ok {
			return "", false
		}
		return walk.build("named(", id, ")")
	case *ast.ParenExpr:
		return key(e.X)
	case *ast.StarExpr:
		k, ok := key(e.X)
		if !ok {
			return "", false
		}
		return walk.build("*", k)
	case *ast.Ellipsis:
		k, ok := key(e.Elt)
		if !ok {
			return "", false
		}
		return walk.build("...", k)
	case *ast.ArrayType:
		k, ok := key(e.Elt)
		if !ok {
			return "", false
		}
		if e.Len == nil {
			return walk.build("[]", k)
		}
		n, ok := e.Len.(*ast.BasicLit)
		if !ok || n.Kind != token.INT {
			return "", false
		}
		length, err := strconv.ParseUint(n.Value, 0, 64)
		if err != nil {
			return "", false
		}
		return walk.buildArrayKey(length, k)
	case *ast.MapType:
		a, ok := key(e.Key)
		if !ok {
			return "", false
		}
		b, bok := key(e.Value)
		if !bok {
			return "", false
		}
		return walk.build("map[", a, "]", b)
	case *ast.ChanType:
		k, ok := key(e.Value)
		if !ok {
			return "", false
		}
		return walk.buildChannelKey(e.Dir, k)
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
			var parts []goEvidenceRepeatedKey
			length := 0
			hasValues := false
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
					remaining, ok := walk.remaining()
					if !ok {
						return "", false
					}
					length, ok = goEvidenceAddRepeatedLength(length, len(k), count, remaining)
					if !ok {
						walk.exhausted = true
						return "", false
					}
					commas := count
					if !hasValues {
						commas--
					}
					length, ok = goEvidenceAddRepeatedLength(length, 1, commas, remaining)
					if !ok {
						walk.exhausted = true
						return "", false
					}
					parts = append(parts, goEvidenceRepeatedKey{key: k, count: count})
					hasValues = true
				}
			}
			return walk.buildFieldList(parts, length)
		}
		params, ok := fields(e.Params)
		if !ok {
			return "", false
		}
		results, rok := fields(e.Results)
		if !rok {
			return "", false
		}
		return walk.build("func(", params, ")(", results, ")")
	}
	return "", false
}
