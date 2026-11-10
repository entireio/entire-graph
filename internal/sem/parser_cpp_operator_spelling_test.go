package sem

import "testing"

// C++ permits whitespace anywhere between the tokens of an operator's name:
// `operator const char*`, `operator const char *` and `operator  const  char *`
// all name the SAME conversion. The name feeds compound-v1 symbol identity, so
// carrying the author's spacing into it means a whitespace-only edit changes a
// function's ID, and two files that spell one conversion differently produce
// two entities for one function.
func TestCPlusPlusOperatorNameIsSpellingIndependent(t *testing.T) {
	src := `struct A {
  operator const char*() const { return ""; }
  void* operator new(size_t n) { return nullptr; }
  void* operator new[](size_t n) { return nullptr; }
};

struct B {
  operator const  char  *() const { return ""; }
  void* operator  new(size_t n) { return nullptr; }
  void* operator new [](size_t n) { return nullptr; }
};
`
	kinds := parseCPlusPlus(t, src)
	for _, want := range []string{
		"A.operator const char*", "B.operator const char*",
		"A.operator new", "B.operator new",
		"A.operator new[]", "B.operator new[]",
	} {
		if kinds[want] == "" {
			t.Fatalf("operator %q missing: two spellings of one operator must collapse onto one name; got %#v", want, kinds)
		}
	}
	// Every whitespace variant must be gone, not merely joined by the canonical one.
	for _, spelling := range []string{
		"A.operator const char *", "B.operator const char *",
		"A.operator const  char  *", "B.operator const  char  *",
		"B.operator  new", "B.operator new []",
	} {
		if _, ok := kinds[spelling]; ok {
			t.Fatalf("whitespace-bearing spelling %q survived as its own symbol: %#v", spelling, kinds)
		}
	}
}

// The other direction: canonicalisation must not fuse operators that really are
// different. Only whitespace is removed, and only where removing it cannot join
// two identifier tokens into one.
func TestCPlusPlusOperatorNameKeepsDistinctOperatorsApart(t *testing.T) {
	src := `struct C {
  operator const char*() const { return ""; }
  operator char*() const { return nullptr; }
  operator const char**() const { return nullptr; }
  operator unsigned long() const { return 0; }
  operator long long() const { return 0; }
};
`
	kinds := parseCPlusPlus(t, src)
	for _, want := range []string{
		"C.operator const char*",
		"C.operator char*",
		"C.operator const char**",
		"C.operator unsigned long",
		"C.operator long long",
	} {
		if kinds[want] == "" {
			t.Fatalf("distinct conversion %q lost its own name: %#v", want, kinds)
		}
	}
	// `unsigned long` must never collapse to `unsignedlong`: the space between
	// two identifier tokens is part of the type, not decoration.
	for _, fusedName := range []string{"C.operator unsignedlong", "C.operator longlong"} {
		if _, fused := kinds[fusedName]; fused {
			t.Fatalf("two identifier tokens were fused into one (%s): %#v", fusedName, kinds)
		}
	}
}

// TestCanonicalOperatorNameFoldsOnlyOptionalWhitespace pins the helper itself,
// including the cases no C++ fixture in this package reaches. A space is
// dropped only when dropping it cannot fuse two identifier tokens, and bytes
// >= 0x80 count as identifier bytes so an extended (UTF-8) type name keeps the
// space that separates it from `operator`.
func TestCanonicalOperatorNameFoldsOnlyOptionalWhitespace(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"operator const char*", "operator const char*"},
		{"operator const char *", "operator const char*"},
		{"operator  const  char  *", "operator const char*"},
		{"operator const char **", "operator const char**"},
		{"operator new", "operator new"},
		{"operator  new", "operator new"},
		{"operator new[]", "operator new[]"},
		{"operator new []", "operator new[]"},
		{"operator delete []", "operator delete[]"},
		{"operator unsigned long", "operator unsigned long"},
		{"operator long long", "operator long long"},
		{"operator ns :: Thing", "operator ns::Thing"},
		{"operator decltype(Value::v)", "operator decltype(Value::v)"},
		// An extended identifier: the space before it separates two identifier
		// tokens exactly as an ASCII one would, so it must survive.
		{"operator Ünit", "operator Ünit"},
		{"operator  Ünit *", "operator Ünit*"},
	} {
		if got := canonicalOperatorName(tc.in); got != tc.want {
			t.Errorf("canonicalOperatorName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
