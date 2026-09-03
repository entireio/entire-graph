package sem

import "testing"

// pythonLocalBindingNames decides whether a bare call inside a Python body may
// be bound to an imported or file-level callable at all, so both directions of
// its answer are load-bearing: a missed binding lets a shadowed name become a
// confident (and with the import tiers, cross-language) false edge, and a
// fabricated one silences a real call.
func TestPythonLocalBindingNames(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		source  string
		bound   []string
		unbound []string
	}{
		{
			name: "parameters, including annotated, defaulted and variadic ones",
			source: `def run(compute, scale: int = 2, *rest, **options):
    return compute(scale)`,
			bound:   []string{"compute", "scale", "rest", "options"},
			unbound: []string{"int"},
		},
		{
			name: "an annotation's own brackets are not further parameters",
			source: `def run(items: Dict[str, int] = {}, compute=None):
    return compute(items)`,
			bound:   []string{"items", "compute"},
			unbound: []string{"Dict", "str", "int"},
		},
		{
			name: "assignment, tuple, annotated and augmented targets",
			source: `def run(source):
    compute = source
    left, right = source
    total: int = 0
    total += 1
    return compute(left, right, total)`,
			bound: []string{"compute", "left", "right", "total"},
		},
		{
			name: "loop, context, exception, walrus and nested-definition targets",
			source: `def run(source):
    for compute in source:
        pass
    with source as handle:
        pass
    try:
        pass
    except ValueError as failure:
        pass
    if (parsed := source) is not None:
        pass
    def helper():
        pass
    return compute, handle, failure, parsed, helper`,
			bound: []string{"compute", "handle", "failure", "parsed", "helper"},
		},
		{
			name: "a lambda parameter binds too",
			source: `def run(source):
    return sorted(source, key=lambda compute, index: compute(index))`,
			bound:   []string{"compute", "index"},
			unbound: []string{"sorted", "key"},
		},
		{
			// A lambda's parameters are the lambda's scope. Carrying them into
			// the enclosing body made an unrelated later call of the same name
			// look shadowed, which deletes its edge outright.
			name: "a lambda parameter does not escape the lambda",
			source: `def run(source):
    handler = lambda compute: compute(1)
    return handler(compute(source))`,
			bound:   []string{"handler", "source"},
			unbound: []string{"compute"},
		},
		{
			// Python 3 scopes a comprehension's variables to the comprehension,
			// list and generator alike.
			name: "a comprehension variable does not escape the comprehension",
			source: `def run(values):
    labels = [compute for compute in values]
    total = sum(scale for scale in values)
    return compute(scale), labels, total`,
			bound:   []string{"labels", "total", "values"},
			unbound: []string{"compute", "scale"},
		},
		{
			// A confined comprehension variable is still a binding: nothing
			// outside the brackets can mean anything else by that name.
			name: "a comprehension variable used only inside it binds",
			source: `def run(values):
    return [value(1) for value in values]`,
			bound: []string{"value", "values"},
		},
		{
			// PEP 572: an assignment expression inside a comprehension binds in
			// the scope that CONTAINS the comprehension, unlike the loop
			// variable beside it. Treating it as the comprehension's own left
			// the later call resolving to a same-named import instead.
			name: "a comprehension's walrus target binds in the scope around it",
			source: `def run(values):
    labels = [total for value in values if (total := value)]
    return total(1)`,
			bound: []string{"labels", "total", "values"},
		},
		{
			// The walrus in a lambda is the lambda's own, so a use of that name
			// in the body around it is not the lambda's binding.
			name: "a lambda's walrus target does not shadow the body around it",
			source: `def run(values):
    cb = lambda v: (compute := v)
    return compute(cb(values))`,
			bound:   []string{"cb", "values"},
			unbound: []string{"compute"},
		},
		{
			// Both nested scopes are masked out of the enclosing body's own
			// text, so testing only that text reported the lambda's parameter
			// as body-wide and deleted the comprehension's call.
			name: "a nested scope's binding does not shadow a sibling nested scope",
			source: `def run(values):
    ranked = sorted(values, key=lambda compute: compute)
    return [compute(v) for v in values]`,
			bound:   []string{"ranked", "values"},
			unbound: []string{"compute"},
		},
		{
			// Brackets that never close are a parse failure, not a clean
			// result: the comprehension is no longer recognised, and reading
			// its `for` as a statement fabricated a body-wide binding that
			// deletes the call after it. Fewer bindings is the safe answer.
			name: "brackets that never close fabricate no binding",
			source: `def run(values):
    labels = [compute for compute in values
    return compute(1)`,
			bound:   []string{"labels", "values"},
			unbound: []string{"compute"},
		},
		{
			// The same rule for well-formed source: a keyword argument binds
			// nothing, and putting it on its own line inside the call's
			// parentheses does not turn it into an assignment statement.
			name: "a keyword argument on its own line is not an assignment",
			source: `def run(values):
    schedule(
        compute=1,
    )
    return compute(1)`,
			bound:   []string{"values"},
			unbound: []string{"compute", "schedule"},
		},
		{
			// The negative fence for the two above: a statement whose brackets
			// do close still binds everything it binds, on one line or several.
			name: "a bracketed expression that closes leaves the statements around it readable",
			source: `def run(values):
    total = sum(
        values,
    )
    for compute in (
        values
    ):
        pass
    with open(total) as handle:
        pass
    return compute(handle)`,
			bound: []string{"total", "compute", "handle", "values"},
		},
		{
			name: "a nested def's parameters and locals stay in the nested def",
			source: `def run(source):
    def helper(compute):
        cache = source
        return compute(cache)
    return helper, cache, compute(source)`,
			bound:   []string{"helper", "source"},
			unbound: []string{"compute", "cache"},
		},
		{
			// `global`/`nonlocal` declare the opposite of a local binding: the
			// name is the module's or the enclosing function's, which is the
			// symbol a call by that name is meant to reach.
			name: "global and nonlocal are not local bindings",
			source: `def run(source):
    global compute
    nonlocal scale
    compute = source
    scale = source
    return compute(scale)`,
			bound:   []string{"source"},
			unbound: []string{"compute", "scale"},
		},
		{
			name: "an attribute, an element and a keyword argument bind nothing",
			source: `def run(source):
    self.compute = source
    source[0] = 1
    return dispatch(compute=source)`,
			unbound: []string{"compute", "dispatch"},
		},
		{
			name: "comparisons are not assignments",
			source: `def run(source):
    if compute == source or scale != source or size <= source:
        return compute(source)
    return None`,
			bound:   []string{"source"},
			unbound: []string{"compute", "scale", "size"},
		},
		{
			name: "a name that only appears inside a literal or a comment is not bound",
			source: `def run(source):
    # compute = source
    label = "scale = 1"
    return label`,
			bound:   []string{"source", "label"},
			unbound: []string{"compute", "scale"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bindings := pythonLocalBindingNames(tc.source)
			for _, name := range tc.bound {
				if _, ok := bindings[name]; !ok {
					t.Fatalf("%q must be a local binding; got %v", name, sortedKeysOf(bindings))
				}
			}
			for _, name := range tc.unbound {
				if _, ok := bindings[name]; ok {
					t.Fatalf("%q must not be a local binding; got %v", name, sortedKeysOf(bindings))
				}
			}
		})
	}
}

// pythonLocalOnlyImportScopes answers which imported names are NOT visible
// file-wide. Getting that wrong in the other direction hides a real import, so
// every shape that binds at module scope must stay out of the map entirely.
func TestPythonLocalOnlyImportScopes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		source   string
		confined []string
		filewide []string
	}{
		{
			name: "an import inside a function is confined to it",
			source: `def run():
    from frobnicate import compute
    return compute(1)
`,
			confined: []string{"compute"},
		},
		{
			name: "a module-level import is never confined",
			source: `from frobnicate import compute


def run():
    return compute(1)
`,
			filewide: []string{"compute"},
		},
		{
			name: "a name imported at module scope as well stays file-wide",
			source: `from frobnicate import compute


def run():
    from other import compute
    return compute(1)
`,
			filewide: []string{"compute"},
		},
		{
			// `global name` in the scope that imports it says the import binds
			// the MODULE's name (`def _load(): global np; import numpy as np`),
			// which every other function in the file can then see. Confining it
			// hides a real import, and a hidden import deletes the call edge it
			// was the evidence for.
			name: "an import declared global in its scope binds at module scope",
			source: `def _load():
    global compute
    from frobnicate import compute


def run():
    return compute(1)
`,
			filewide: []string{"compute"},
		},
		{
			// `nonlocal` is the bounded half of the same rule: the binding is
			// the ENCLOSING function's, so it stays confined -- to that
			// function, not to the nested def that imported it.
			name: "an import declared nonlocal stays confined to the enclosing function",
			source: `def outer():
    def _load():
        nonlocal compute
        from frobnicate import compute

    def use():
        return compute(1)
`,
			confined: []string{"compute"},
		},
		{
			name: "an indented import no definition encloses binds at module scope",
			source: `if TYPE_CHECKING:
    from frobnicate import compute


def run():
    return compute(1)
`,
			filewide: []string{"compute"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scopes := pythonLocalOnlyImportScopes(tc.source)
			for _, name := range tc.confined {
				if len(scopes[name]) == 0 {
					t.Fatalf("%q must be confined to its own scope; got %v", name, sortedKeysOf(scopes))
				}
			}
			for _, name := range tc.filewide {
				if len(scopes[name]) != 0 {
					t.Fatalf("%q is visible file-wide and must not be confined; got %v", name, sortedKeysOf(scopes))
				}
			}
		})
	}
}
