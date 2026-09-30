package sem

import (
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
