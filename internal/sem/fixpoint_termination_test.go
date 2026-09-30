package sem

import (
	"fmt"
	"path"
	"strings"
	"testing"
	"time"
)

// terminatesWithin runs fn under a deadline so a loop or recursion that does not
// terminate fails the test instead of hanging the package.
func terminatesWithin(t *testing.T, limit time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("did not terminate within %s", limit)
	}
}

func TestCPlusPlusOwnerPatternTerminatesOnEmptySegment(t *testing.T) {
	// `inline namespace v1 { struct ::Foo {...}; }` encodes its owner as
	// `\x00v1::::Foo`. When v1 is absent from the definition the scan reaches the
	// empty segment, which strings.Index finds at every offset: offset never
	// advanced and the loop spun forever.
	var got bool
	terminatesWithin(t, 5*time.Second, func() {
		got = signatureNamesQualifiedMethodPattern("Foo::f", "\x00v1::::Foo", "f")
	})
	if got {
		t.Fatal("an unqualified definition must not match an owner that begins at the global scope")
	}
	// `inline namespace ::v1` puts the empty segment first.
	terminatesWithin(t, 5*time.Second, func() {
		got = signatureNamesQualifiedMethodPattern("int ::v1::Foo::f()", "\x00::v1::Foo", "f")
	})
	if !got {
		t.Fatal("a global-qualified definition after its return type must match")
	}
	// Every offset must be visited, not just some: this `::` starts at an odd one.
	terminatesWithin(t, 5*time.Second, func() {
		got = signatureNamesQualifiedMethodPattern("long ::v1::Foo::f()", "\x00::v1::Foo", "f")
	})
	if !got {
		t.Fatal("a global-qualified definition at an odd offset must match")
	}
}

func TestCPlusPlusOwnerPatternKeepsFirstPositionGlobalMatch(t *testing.T) {
	// The one input the old scan answered for an empty segment: a match at the
	// first position. It must keep its answer.
	if !signatureNamesQualifiedMethodPattern("::v1::Foo::f", "\x00::v1::Foo", "f") {
		t.Fatal("global-qualified definition at offset 0 no longer matches")
	}
	if !signatureNamesQualifiedMethodPattern("::Foo::f", "\x00v1::::Foo", "f") {
		t.Fatal("inline namespace omitted before a global qualifier no longer matches")
	}
}

func TestCPlusPlusGlobalQualifiedClassSnapshotTerminates(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "a.cpp", "inline namespace v1 {\nstruct ::Foo {\n  int f();\n};\n}\n"+
		"int Foo::f() { return 1; }\nvoid use(Foo x) { x.f(); }\n")
	terminatesWithin(t, 20*time.Second, func() {
		if _, err := BuildProviderSnapshot(t.Context(), repo, "test-version"); err != nil {
			t.Error(err)
		}
	})
}

// goAliasFanoutSource declares a chain of n type aliases, each referring to the
// next `fanout` times through `shape`, ending in leaf, plus an interface and a
// concrete type whose method signatures both spell A0, and a call through the
// interface so the implementation hop compares the two signatures.
func goAliasFanoutSource(n int, shape, leaf string) string {
	var b strings.Builder
	b.WriteString("package p\n\n")
	for i := 0; i < n; i++ {
		next := fmt.Sprintf("A%d", i+1)
		fmt.Fprintf(&b, "type A%d = %s\n", i, strings.ReplaceAll(shape, "X", next))
	}
	fmt.Fprintf(&b, "type A%d = %s\n\n", n, leaf)
	b.WriteString("type Store interface {\n\tPut(x A0)\n}\n\ntype S struct{}\n\nfunc (S) Put(x A0) {}\n\n")
	b.WriteString("func use(s Store) {\n\ts.Put(nil)\n}\n")
	return b.String()
}

func TestGoSignatureEvidenceTerminatesOnAliasFanout(t *testing.T) {
	// Each alias names the next twice, so the evidence walk expanded 2^N nodes
	// and the depth cap of 64 only stopped N at 32: four billion visits for one
	// signature. An unresolvable leaf keeps every key empty, so this measures
	// the walk and not the string it would build.
	repo := t.TempDir()
	writeFile(t, repo, "p.go", goAliasFanoutSource(40, "map[X]X", "interface{ M() }"))
	terminatesWithin(t, 20*time.Second, func() {
		if _, err := BuildProviderSnapshot(t.Context(), repo, "test-version"); err != nil {
			t.Error(err)
		}
	})
}

func TestGoSignatureEvidenceKeepsKeyWithinBudget(t *testing.T) {
	// 3^8 alias references is well inside the step budget: the signatures still
	// compare equal and the call still reaches the implementation.
	repo := t.TempDir()
	writeFile(t, repo, "p.go", goAliasFanoutSource(8, "func(X, X, X)", "int"))
	var snapshot ProviderSnapshot
	terminatesWithin(t, 20*time.Second, func() {
		var err error
		if snapshot, err = BuildProviderSnapshot(t.Context(), repo, "test-version"); err != nil {
			t.Error(err)
		}
	})
	for _, edge := range runCallsFrom(snapshot, "use") {
		if strings.HasSuffix(edge.ToID, ":method:S.Put") {
			return
		}
	}
	t.Fatalf("use no longer reaches S.Put through Store: %#v", runCallsFrom(snapshot, "use"))
}

func TestSearchVerifyWorkspaceGlobTerminatesOnManyDoubleStars(t *testing.T) {
	// Every `**` retried every split of the path beneath it: 24 of them against a
	// 16-deep path is ~2.5e10 attempts. The pattern is a `workspaces` entry, so the
	// repository chooses it.
	pattern := strings.Repeat("**/", 24) + "zz"
	segments := strings.Split(strings.Repeat("a/", 15)+"b", "/")
	var got bool
	terminatesWithin(t, 5*time.Second, func() {
		got = searchVerifyNodeWorkspaceMatchesExpanded(pattern, segments)
	})
	if got {
		t.Fatal("no segment is zz, so the glob must not match")
	}
	terminatesWithin(t, 5*time.Second, func() {
		got = searchVerifyNodeWorkspaceMatchesExpanded(pattern, append(segments, "zz"))
	})
	if !got {
		t.Fatal("a path ending in zz must match")
	}
}

// searchVerifyWorkspaceGlobReference is the matcher as it was before failed
// states were remembered: the definition the memoized one must agree with.
func searchVerifyWorkspaceGlobReference(pattern string, segments []string) bool {
	if searchVerifyNodeWorkspacePatternIsUnreadable(pattern) {
		return true
	}
	patternSegments := strings.Split(pattern, "/")
	for len(patternSegments) > 0 {
		if patternSegments[0] == "**" {
			if len(patternSegments) == 1 {
				return true
			}
			for skip := 0; skip <= len(segments); skip++ {
				if searchVerifyWorkspaceGlobReference(strings.Join(patternSegments[1:], "/"), segments[skip:]) {
					return true
				}
			}
			return false
		}
		if len(segments) == 0 {
			return false
		}
		matched, err := path.Match(patternSegments[0], segments[0])
		if err != nil {
			return true
		}
		if !matched {
			return false
		}
		patternSegments, segments = patternSegments[1:], segments[1:]
	}
	return len(segments) == 0
}

func TestSearchVerifyWorkspaceGlobAgreesWithReference(t *testing.T) {
	// Every pattern of up to four segments over these atoms, against every path of
	// up to four segments over {a, b}: `[` is a malformed class (the permissive
	// error answer) and `?(x` the unreadable extglob.
	atoms := []string{"**", "*", "a", "b", "a*", "[", "?(x"}
	var patterns []string
	var grow func(prefix []string)
	grow = func(prefix []string) {
		if len(prefix) > 0 {
			patterns = append(patterns, strings.Join(prefix, "/"))
		}
		if len(prefix) == 4 {
			return
		}
		for _, atom := range atoms {
			grow(append(append([]string(nil), prefix...), atom))
		}
	}
	grow(nil)
	var paths [][]string
	var walk func(prefix []string)
	walk = func(prefix []string) {
		paths = append(paths, prefix)
		if len(prefix) == 4 {
			return
		}
		for _, segment := range []string{"a", "b"} {
			walk(append(append([]string(nil), prefix...), segment))
		}
	}
	walk([]string{})
	checked := 0
	for _, pattern := range patterns {
		for _, segments := range paths {
			want := searchVerifyWorkspaceGlobReference(pattern, segments)
			if got := searchVerifyNodeWorkspaceMatchesExpanded(pattern, segments); got != want {
				t.Fatalf("pattern %q against %q: got %v, reference %v", pattern, segments, got, want)
			}
			checked++
		}
	}
	if checked < 50000 {
		t.Fatalf("differential check covered only %d cases", checked)
	}
}
