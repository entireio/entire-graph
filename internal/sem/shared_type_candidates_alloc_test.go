package sem

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// sameNameTypeDeclarations builds n Go type declarations that all share one
// short name, each in its own package directory. It is the shape generated
// protobuf code has: every .pb.go declares a function-local `type x struct{}`.
func sameNameTypeDeclarations(name string, n int) []SymbolRecord {
	records := make([]SymbolRecord, n)
	for i := range records {
		path := fmt.Sprintf("gen/pkg%04d/file.pb.go", i)
		records[i] = SymbolRecord{ID: fmt.Sprintf("sym-%s-%d", name, i), Name: name, Kind: "struct", Language: "Go", FilePath: path}
	}
	return records
}

// withinDeadline fails the test if fn has not returned before limit. The work
// under test is bounded, so this guards against a super-linear regression
// taking the whole package run down with it rather than against a true hang.
func withinDeadline(t *testing.T, limit time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("did not finish within %s", limit)
	}
	if elapsed := time.Since(start); elapsed > limit {
		t.Fatalf("took %s, limit %s", elapsed, limit)
	}
}

func TestSharedTypeCandidatesDoesNotCopyWhenEveryCandidateShares(t *testing.T) {
	// No t.Parallel: testing.AllocsPerRun panics when the test is parallel.
	candidates := sameNameTypeDeclarations("x", 1000)
	from := SymbolRecord{ID: "from", Name: "Reset", Kind: "method", Language: "Go", FilePath: "gen/pkg0000/file.pb.go"}
	var got []SymbolRecord
	allocs := testing.AllocsPerRun(50, func() {
		got = sharedTypeCandidates(from, candidates)
	})
	if len(got) != len(candidates) {
		t.Fatalf("kept %d candidates, want all %d", len(got), len(candidates))
	}
	if allocs != 0 {
		t.Fatalf("filtering a list every candidate passes allocated %.0f times per call, want 0", allocs)
	}
}

func TestSharedTypeCandidatesResultCannotAppendIntoTheIndex(t *testing.T) {
	t.Parallel()
	backing := sameNameTypeDeclarations("x", 4)
	sentinel := SymbolRecord{ID: "spare-capacity", Name: "x", Kind: "struct", Language: "Go"}
	backing[3] = sentinel
	index := backing[:3] // len 3, cap 4: an append in place would overwrite backing[3]
	from := SymbolRecord{ID: "from", Kind: "method", Language: "Go"}
	got := sharedTypeCandidates(from, index)
	_ = append(got, SymbolRecord{ID: "appended"})
	if backing[3].ID != sentinel.ID {
		t.Fatalf("append to the filtered result wrote %q into the index's spare capacity", backing[3].ID)
	}
}

func TestSharedTypeCandidatesStillDropsForeignLanguages(t *testing.T) {
	t.Parallel()
	candidates := []SymbolRecord{
		{ID: "go-1", Name: "Point", Kind: "struct", Language: "Go"},
		{ID: "erl", Name: "Point", Kind: "record", Language: "Erlang"},
		{ID: "go-2", Name: "Point", Kind: "struct", Language: "Go"},
	}
	from := SymbolRecord{ID: "from", Kind: "function", Language: "Go"}
	got := sharedTypeCandidates(from, candidates)
	if len(got) != 2 || got[0].ID != "go-1" || got[1].ID != "go-2" {
		t.Fatalf("got %v, want go-1 then go-2", got)
	}
	if candidates[1].ID != "erl" || candidates[2].ID != "go-2" {
		t.Fatalf("filtering reordered the input: %v", candidates)
	}
}

// TestResolveTypeReferenceCostDoesNotScaleWithSameNameDeclarations pins the
// quadratic that kept `entire graph index` from finishing on
// google.golang.org/api and made genproto peak at 3 GB: each reference copied
// every same-name declaration twice before deciding it was ambiguous.
func TestResolveTypeReferenceCostDoesNotScaleWithSameNameDeclarations(t *testing.T) {
	const declarations = 2000
	const references = 2000
	byName := map[string][]SymbolRecord{"x": sameNameTypeDeclarations("x", declarations)}
	from := SymbolRecord{ID: "from", Name: "Reset", Kind: "method", Language: "Go", FilePath: "other/file.pb.go"}

	var before, after runtime.MemStats
	resolved := 0
	withinDeadline(t, 20*time.Second, func() {
		runtime.GC()
		runtime.ReadMemStats(&before)
		for range references {
			if _, _, _, _, ok := resolveTypeReference("x", from, nil, byName, nil, nil); ok {
				resolved++
			}
		}
		runtime.ReadMemStats(&after)
	})
	if resolved != 0 {
		t.Fatalf("an ambiguous name with no import or package evidence resolved %d times, want 0", resolved)
	}
	perCall := (after.TotalAlloc - before.TotalAlloc) / references
	// One SymbolRecord copy per declaration would already be far above this;
	// the bound only leaves room for constant per-call bookkeeping.
	if perCall > 4096 {
		t.Fatalf("resolveTypeReference allocated %d bytes per call over %d same-name declarations, want a constant under 4096", perCall, declarations)
	}
}

func TestResolveTypeReferenceStillBindsTheUniqueSamePackageDeclaration(t *testing.T) {
	t.Parallel()
	decls := sameNameTypeDeclarations("x", 50)
	decls = append(decls, SymbolRecord{ID: "x-method", Name: "x", Kind: "method", Language: "Go", FilePath: "gen/pkg0007/other.go"})
	byName := map[string][]SymbolRecord{"x": decls}
	from := SymbolRecord{ID: "from", Name: "Reset", Kind: "method", Language: "Go", FilePath: "gen/pkg0007/reset.go"}
	sym, resolution, _, _, ok := resolveTypeReference("x", from, nil, byName, nil, nil)
	if !ok || sym.ID != "sym-x-7" || resolution != "package" {
		t.Fatalf("got (%q, %q, %v), want sym-x-7 by package", sym.ID, resolution, ok)
	}
	one := map[string][]SymbolRecord{"x": {decls[3], {ID: "x-func", Name: "x", Kind: "function", Language: "Go", FilePath: "a/b.go"}}}
	sym, resolution, _, _, ok = resolveTypeReference("x", from, nil, one, nil, nil)
	if !ok || sym.ID != "sym-x-3" || resolution != "name_only" {
		t.Fatalf("got (%q, %q, %v), want sym-x-3 name_only once the non-type is dropped", sym.ID, resolution, ok)
	}
}
