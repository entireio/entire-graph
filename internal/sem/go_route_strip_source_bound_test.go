package sem

import (
	"fmt"
	"testing"
)

// observeStripPrefixValue recurses once, on the value read from the binding
// ultimateStripPrefixSource returns. That binding is either an escaped write
// target, which read reports as rewrite middleware with no source, or a binding
// with no stripPrefixSource, so the recursive call returns before recursing
// again. ultimateStripPrefixSource itself walks at most
// goRouteStripSourceMaxDepth links. These tests pin both bounds against
// self-referential, cyclic and over-deep source chains: the whole observation
// must finish inside a step budget a few steps above the depth cap, which an
// unbounded walk or a second level of recursion would exhaust.
const goRouteStripObserveStepBound = 2*goRouteStripSourceMaxDepth + 4

func newStripSourceBoundResolver() *goRouteResolver {
	return &goRouteResolver{
		budget:                   goRouteStripObserveStepBound,
		escaped:                  map[*goRouteBinding]bool{},
		escapedStripPrefixWrites: map[*goRouteBinding]bool{},
		stripPrefixWatchers:      map[*goRouteBinding]map[*goRouteOrigin]bool{},
	}
}

// observeWithinStripBound runs one observation and reports whether it
// exhausted the step budget instead of returning.
func observeWithinStripBound(r *goRouteResolver, value goRouteBinding, origin *goRouteOrigin, future bool) (exceeded bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if _, ok := recovered.(goRouteBudgetExceeded); !ok {
				panic(recovered)
			}
			exceeded = true
		}
	}()
	r.observeStripPrefixValue(value, origin, future)
	return false
}

func stripSourceChain(length int) []*goRouteBinding {
	chain := make([]*goRouteBinding, length)
	for i := range chain {
		chain[i] = &goRouteBinding{kind: goRouteUnknown}
	}
	for i := 0; i+1 < length; i++ {
		chain[i].stripPrefixSource = chain[i+1]
	}
	return chain
}

func TestGoRouteObserveStripPrefixValueIsBoundedOnCyclicAndDeepSources(t *testing.T) {
	self := &goRouteBinding{kind: goRouteUnknown}
	self.stripPrefixSource = self

	pair := stripSourceChain(2)
	pair[1].stripPrefixSource = pair[0]

	deep := stripSourceChain(10 * goRouteStripSourceMaxDepth)

	shallow := stripSourceChain(3)

	escapedTail := stripSourceChain(3)
	escapedTail[2].stripPrefixSource = escapedTail[0] // a cycle the escaped write cuts

	cases := []struct {
		name       string
		source     *goRouteBinding
		escaped    *goRouteBinding
		wantOpaque bool
	}{
		{name: "self reference", source: self, wantOpaque: true},
		{name: "two cycle", source: pair[0], wantOpaque: true},
		{name: "deeper than the cap", source: deep[0], wantOpaque: true},
		{name: "terminating chain", source: shallow[0], wantOpaque: false},
		{name: "escaped write in a cycle", source: escapedTail[0], escaped: escapedTail[2], wantOpaque: true},
	}
	for _, test := range cases {
		for _, future := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/future=%v", test.name, future), func(t *testing.T) {
				r := newStripSourceBoundResolver()
				if test.escaped != nil {
					r.escapedStripPrefixWrites[test.escaped] = true
				}
				origin := &goRouteOrigin{}
				value := goRouteBinding{kind: goRouteUnknown, stripPrefixSource: test.source}
				if observeWithinStripBound(r, value, origin, future) {
					t.Fatalf("observation exhausted the %d-step bound: the source walk or recursion is unbounded", goRouteStripObserveStepBound)
				}
				if origin.opaque != test.wantOpaque {
					t.Fatalf("origin.opaque = %v, want %v", origin.opaque, test.wantOpaque)
				}
			})
		}
	}
}

// stripSourceReferenceOpaque independently models both observation modes.
// Immediate observation copies the terminal binding's value; future observation
// watches every capture link, so intermediate rewrite middleware also taints it.
// Escaped writes and a chain exceeding the bound are opaque in either mode.
func stripSourceReferenceOpaque(start *goRouteBinding, escapedWrites map[*goRouteBinding]bool, future bool) bool {
	current := start
	for depth := 0; depth < goRouteStripSourceMaxDepth; depth++ {
		if current == nil || escapedWrites[current] {
			return true
		}
		if future && current.stripPrefixMiddleware {
			return true
		}
		if current.stripPrefixSource == nil {
			return current.stripPrefixMiddleware
		}
		current = current.stripPrefixSource
	}
	return true
}

func TestGoRouteStripPrefixIntermediateMiddlewareModes(t *testing.T) {
	for _, future := range []bool{false, true} {
		t.Run(fmt.Sprintf("future=%v", future), func(t *testing.T) {
			chain := stripSourceChain(3)
			chain[1].stripPrefixMiddleware = true
			r := newStripSourceBoundResolver()
			origin := &goRouteOrigin{}
			if observeWithinStripBound(r, goRouteBinding{stripPrefixSource: chain[0]}, origin, future) {
				t.Fatal("bounded chain exhausted")
			}
			if origin.opaque != future {
				t.Fatalf("intermediate middleware: opaque=%v, want %v", origin.opaque, future)
			}
			if want := stripSourceReferenceOpaque(chain[0], r.escapedStripPrefixWrites, future); want != future {
				t.Fatalf("model skipped mode distinction: got %v, want %v", want, future)
			}
		})
	}
}

// FuzzGoRouteObserveStripPrefixValueBounded builds an arbitrary source graph,
// including self-references and cycles, and checks that observation finishes
// inside the fixed step bound and agrees with the reference model.
func FuzzGoRouteObserveStripPrefixValueBounded(f *testing.F) {
	f.Add([]byte{0, 0, 0})                                                     // one binding pointing at itself
	f.Add([]byte{1, 1, 0, 0})                                                  // two-cycle
	f.Add([]byte{15, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 0, 0}) // 16-cycle at the cap
	f.Add([]byte{2, 1, 2, 3, 0})                                               // terminating chain
	f.Add([]byte{2, 1, 2, 0x80, 0, 1})                                         // cycle cut by an escaped write
	f.Add([]byte{2, 1, 0x40 | 3, 0, 0})                                        // chain ending in rewrite middleware
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		count := 1 + int(data[0])%32
		bindings := make([]*goRouteBinding, count)
		for i := range bindings {
			bindings[i] = &goRouteBinding{kind: goRouteUnknown}
		}
		r := newStripSourceBoundResolver()
		for i, binding := range bindings {
			var b byte
			if 1+i < len(data) {
				b = data[1+i]
			}
			if target := int(b&0x3f) % (count + 1); target < count {
				binding.stripPrefixSource = bindings[target]
			}
			if b&0x40 != 0 {
				binding.stripPrefixMiddleware = true
			}
			if b&0x80 != 0 {
				r.escapedStripPrefixWrites[binding] = true
			}
		}
		var start, flags byte
		if 1+count < len(data) {
			start = data[1+count]
		}
		if 2+count < len(data) {
			flags = data[2+count]
		}
		source := bindings[int(start)%count]
		future := flags&1 != 0
		origin := &goRouteOrigin{}
		value := goRouteBinding{kind: goRouteUnknown, stripPrefixSource: source}
		if observeWithinStripBound(r, value, origin, future) {
			t.Fatalf("observation exhausted the %d-step bound", goRouteStripObserveStepBound)
		}
		if want := stripSourceReferenceOpaque(source, r.escapedStripPrefixWrites, future); origin.opaque != want {
			t.Fatalf("origin.opaque = %v, reference model says %v (future=%v)", origin.opaque, want, future)
		}
	})
}
