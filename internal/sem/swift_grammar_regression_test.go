package sem

import (
	"strings"
	"testing"
)

// These inputs parsed on the bundled grammar, or recovered declarations that
// 0.7.4 lost. Keep this coverage alongside the Swift Testing macro tests: a
// grammar upgrade must preserve both the old surface and the newer syntax.
func TestSwiftGrammarRegressionSyntax(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		want   []string
	}{
		{
			name: "conditions inside switch cases",
			source: `func dispatch(_ value: Int) {
    switch value {
    case 0: break
    default:
#if DEBUG
        if #available(macOS 10.15, *) { first() } else { fallback() }
#else
        fallback()
#endif
    }
}
func first() {}
func fallback() {}
`,
			want: []string{"dispatch", "first", "fallback"},
		},
		{
			name: "inline closing directive",
			source: `func inlineEnd() -> Int {
#if DEBUG
    return 0
#else
    return 1#endif
}
`,
			want: []string{"inlineEnd"},
		},
		{
			name: "versioned and internal conditions",
			source: `#if canImport(Foundation, _version: 113.330.1.2) && _hasAtomicBitWidth(_64)
func versioned() {}
#elseif canImport(Foundation.Submodule, _underlyingVersion: 3.3)
func underlying() {}
#elseif _runtime(_ObjC) || _endian(little) || _compiler_version("4.*.0")
func runtimeSpecific() {}
#endif
`,
			want: []string{"versioned", "underlying", "runtimeSpecific"},
		},
		{
			name: "conditional switch cases",
			source: `func select(_ value: Bool) -> Int {
    switch value {
#if DEBUG
    case true: return 1
#else
    case true: return 2
#endif
    case false: return 0
    }
}
`,
			want: []string{"select"},
		},
		{
			name: "newlines before block comments",
			source: `#if DEBUG
/* explanation */ public struct Commented {
    func run() {}
}
#endif
func continued() -> Int {
    let value = 1
        /* continuation */ + 2
    return value
}
struct Documented {
    func first() {}
    // A line comment before a block comment must not swallow either.
    /** Documentation */
    // Explanation after a block comment must remain a comment.
    func dispose() { synchronizedDispose() }
    /** More documentation */
    func synchronizedDispose() {}
}
/*: Playground documentation */
/*: More playground documentation */
// A following line comment
let documentedValue = 1
`,
			want: []string{"Commented", "Commented.run", "continued", "Documented", "Documented.dispose", "Documented.synchronizedDispose"},
		},
		{
			name: "conditional attributes",
			source: `#if DEBUG
@available(*, deprecated)
#elseif os(macOS)
@available(macOS 12, *)
#else
@available(*, unavailable)
#endif
public struct Conditional {
    public func run() {}
}
`,
			want: []string{"Conditional", "Conditional.run"},
		},
		{
			name: "nested conditional attributes",
			source: `struct Nested {
#if hasFeature(NoncopyableGenerics)
#if _pointerBitWidth(_64)
@inline(__always)
#else
@inline(never)
#endif
#endif
    func run() {}
#if hasAttribute(Sendable)
    func other() {}
#else
    func fallback() {}
#endif
}
`,
			want: []string{"Nested", "Nested.run", "Nested.other", "Nested.fallback"},
		},
		{
			name: "multiline associatedtype constraints",
			source: `protocol Section {
    associatedtype Items: Collection where
        Items.Index == Int,
        Items.Element: Hashable
    var items: Items { get }
}
`,
			want: []string{"Section"},
		},
		{
			name: "diagnostics in type bodies",
			source: `struct Diagnostics {
#warning("still implemented")
    func run() {}
#error("unsupported configuration")
    struct Child {}
}
enum DiagnosticEnum {
#warning("warning")
    case ready
}
protocol DiagnosticProtocol {
#warning("warning")
    var value: Int { get }
}
`,
			want: []string{"Diagnostics", "Diagnostics.run", "Child", "DiagnosticEnum", "DiagnosticProtocol"},
		},
		{
			name: "consume switch with later declarations",
			source: `struct Machine: ~Copyable {
    var state: State
    mutating func advance() {
        switch consume self.state {
        case .active(let value):
            self = .active(value)
        case .closed:
            self = .closed
        }
    }
    enum Result { case done }
    func after() {}
}
extension Machine {
    static func active(_ value: Int) -> Self { .init(state: .active(value)) }
    static var closed: Self { .init(state: .closed) }
}
`,
			want: []string{"Machine", "Machine.state", "Machine.advance", "Machine.after", "Result", "Machine.active", "Machine.closed"},
		},
		{
			name: "copy and contextual identifiers",
			source: `func inspect(_ value: State) {
    switch copy value {
    case .ready: break
    default: break
    }
    let consume = value
    switch consume {
    case .ready: break
    default: break
    }
    let copy = value
    switch copy {
    case .ready: break
    default: break
    }
}
`,
			want: []string{"inspect"},
		},
		{
			name: "conditional macros and literal lookalikes",
			source: `import Testing
struct MacroTests {
#if DEBUG
    @Test func checks() {
        let text = "#if DEBUG @available switch consume"
        // #endif
        /* #if DEBUG */
        #expect(text.count
            > 0)
        #expect(true)
    }
#else
    @Test func fallback() { #expect(true) }
#endif
}
`,
			want: []string{"MacroTests", "MacroTests.checks", "MacroTests.fallback"},
		},
		{
			name: "comments before leading operators",
			source: `func lineTernary(a: Bool, d: Int, e: Int) -> Int {
    let value = a
        // explain
        ? d : e
    return value
}
func blockTernary(a: Bool, d: Int, e: Int) -> Int {
    let value = a
        /* explain */
        ? d : e
    return value
}
func comparisons(a: Int, b: Int) -> Bool {
    let less = a
        // explain
        < b
    let greater = a
        /* explain */
        > b
    return less && greater
}
func bitwise(a: Int, b: Int) -> Int {
    let either = a
        // explain
        | b
    let exclusive = a
        // explain
        ^ b
    let both = a
        /* explain */
        & b
    return either + exclusive + both
}
`,
			want: []string{"lineTernary", "blockTernary", "comparisons", "bitwise"},
		},
		{
			name: "closures after comments",
			source: `class Capture {
    func foo() {
        func bar() {
            self.foo()
        }
        // A closure can start a statement, so the comment keeps the separator.
        {[weak self] in _ = self!; bar() }()
    }
}
func braces(a: Bool, b: Int, c: Int) -> Int
// explain
{
    if a
    // explain
    {
        return a
            ? b
            // explain
            : c
    }
    return c
}
`,
			want: []string{"Capture", "Capture.foo", "braces"},
		},
		{
			name: "comments still separate statements",
			source: `func separated() {
    let first = make()
    // explain
    let second = make()
    /* explain */
    consume(first, second)
}
func make() -> Int { 1 }
func consume(_ a: Int, _ b: Int) {}
`,
			want: []string{"separated", "make", "consume"},
		},
		{
			name: "string canImport versions",
			source: `#if canImport(Foundation, _version: "113.330.1.2")
func stringVersion() {}
#elseif canImport(Foundation, _underlyingVersion: "3.3")
func stringUnderlying() {}
#endif
`,
			want: []string{"stringVersion", "stringUnderlying"},
		},
		{
			name: "diagnostics before the first switch case",
			source: `func warned(_ value: Int) {
    switch value {
    #warning("before the first case")
    default: break
    }
    switch value {
    #error("before the first case")
    #if DEBUG
    case 0: break
    #endif
    default: break
    }
}
`,
			want: []string{"warned"},
		},
		{
			name: "compilation flags named like predicates",
			source: `#if hasFeature
func feature() {}
#elseif hasAttribute && !_runtime
func attribute() {}
#elseif _endian || _pointerBitWidth || _hasAtomicBitWidth || _compiler_version
func others() {}
#endif
#if hasFeature(BareSlashRegexLiterals) && hasAttribute(retroactive)
func predicate() {}
#endif
`,
			want: []string{"feature", "attribute", "others", "predicate"},
		},
		{
			name: "macros prefixed by directive names",
			source: `func macros() {
    #elsewhere
    #endifx
    #elseifx(1)
    #iffy(1)
    #elsei
}
#if DEBUG
func debug() {}
#elseif RELEASE
func release() {}
#else
func other() {}
#endif
`,
			want: []string{"macros", "debug", "release", "other"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entities, language, status := TreeSitterParser{}.ParseWithStatus("Regression.swift", tc.source)
			if language != "Swift" || status.ParseError {
				t.Fatalf("language=%q status=%+v", language, status)
			}
			found := map[string]bool{}
			for _, entity := range entities {
				found[entity.Name] = true
			}
			for _, name := range tc.want {
				if !found[name] {
					t.Errorf("missing %s; got %v", name, found)
				}
			}
		})
	}
}

func TestSwiftGrammarRegressionKeepsRelations(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "Machine.swift", `enum State { case ready; case closed }
struct Machine {
    var state: State
#if DEBUG
@available(*, deprecated)
#endif
    mutating func advance() {
        switch consume self.state {
        case .ready: self.state = .closed
        case .closed: break
        }
        self.after()
    }
    func after() { helper() }
}
func helper() {}
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Header.PartialFailures) != 0 {
		t.Fatalf("partial failures: %+v", snapshot.Header.PartialFailures)
	}
	symbols := map[string]SymbolRecord{}
	names := map[string]string{}
	for _, s := range snapshot.Symbols {
		symbols[s.QualifiedName] = s
		names[s.ID] = s.QualifiedName
	}
	for _, name := range []string{"Machine", "Machine.state", "Machine.advance", "Machine.after", "helper"} {
		if _, ok := symbols[name]; !ok {
			t.Fatalf("missing %s in %v", name, symbols)
		}
	}
	advance := symbols["Machine.advance"]
	if advance.StartLine != 5 || advance.EndLine != 13 || !strings.Contains(advance.Signature, "@available") {
		t.Errorf("conditional attribute/source range lost: %+v", advance)
	}
	seen := map[string]bool{}
	for _, r := range snapshot.Relations {
		seen[r.Type+":"+names[r.FromID]+"->"+names[r.ToID]] = true
	}
	for _, key := range []string{
		"CONTAINS:Machine->Machine.advance",
		"CONTAINS:Machine->Machine.after",
		"CALLS:Machine.advance->Machine.after",
		"CALLS:Machine.after->helper",
		"READS_FIELD:Machine.advance->Machine.state",
		"WRITES_FIELD:Machine.advance->Machine.state",
	} {
		if !seen[key] {
			t.Errorf("missing %s; relations=%v", key, seen)
		}
	}
}

func TestSwiftGrammarStillReportsMalformedSource(t *testing.T) {
	for _, source := range []string{
		"struct Broken { func missing( }",
		"struct Broken { func run() { switch consume } }",
		"#if hasFeature(\nstruct Broken {}\n#endif",
		"protocol Broken { associatedtype Items where Items.Index == }",
	} {
		_, _, status := TreeSitterParser{}.ParseWithStatus("Broken.swift", source)
		if !status.ParseError {
			t.Errorf("malformed Swift silently accepted: %q", source)
		}
	}
}

func TestSwiftGrammarConditionalExtensionIsNotAType(t *testing.T) {
	entities, _, status := TreeSitterParser{}.ParseWithStatus("Extension.swift", `struct Thing {}
#if DEBUG
@available(*, deprecated)
#endif
extension Thing {
    func run() {}
}
`)
	if status.ParseError {
		t.Fatal(status)
	}
	types, methods := 0, 0
	for _, e := range entities {
		if e.Name == "Thing" {
			types++
		}
		if e.Name == "Thing.run" {
			methods++
		}
	}
	if types != 1 || methods != 1 {
		t.Fatalf("conditional extension emitted a duplicate type or lost its method: %+v", entities)
	}
}
