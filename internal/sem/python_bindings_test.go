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
