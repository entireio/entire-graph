package sem

import "testing"

// TestCPlusPlusInClassAllocationOperatorsAreExtracted covers the allocation and
// deallocation members a class overrides to control its own storage. They are
// spelled with a WORD, not punctuation, so maskCPlusPlusOperatorCall (which only
// rewrites `operator<punctuation>(`) never touches them: the parsed source still
// carries the real `operator new`. The in-class declaration walk discarded every
// `operator_name` regardless, so these bodyless members produced no symbol at
// all — nothing to search for, no CONTAINS edge, and no target for a call.
func TestCPlusPlusInClassAllocationOperatorsAreExtracted(t *testing.T) {
	t.Parallel()
	got := memberIndex(t, "arena.hpp", `#pragma once
#include <cstddef>

class Arena {
public:
    void* operator new(size_t size);
    void operator delete(void* p);
    void* operator new[](size_t size);
    void operator delete[](void* p);
    void*  operator   co_await(int n);
    int Add(int amount) const;
};
`)
	for _, want := range []string{
		"method:Arena.Add",
		"method:Arena.operator new",
		"method:Arena.operator delete",
		"method:Arena.operator new[]",
		"method:Arena.operator delete[]",
		// Runs of whitespace inside the name collapse, so one operator has one
		// name whatever the author's spacing.
		"method:Arena.operator co_await",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q: %#v", want, mapKeys(got))
		}
	}
	// The parameter must never become the member's name, and no masked
	// stand-in may leak.
	for _, unwanted := range []string{"method:Arena.size", "method:Arena.p", "method:Arena.op", "method:Arena.n", "method:Arena.operator   co_await"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("bogus member name %q: %#v", unwanted, mapKeys(got))
		}
	}
}

// TestSignatureNamesQualifiedMethodBalancesNestedTemplateArguments pins the
// template-argument skip. `A<std::vector<T>>::foo` closes the OUTER argument
// list at the second `>`, so cutting at the first one leaves `>::foo` and the
// definition is rejected — the out-of-line body of every nested-template class
// stayed disconnected from its callers.
func TestSignatureNamesQualifiedMethodBalancesNestedTemplateArguments(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		signature string
		container string
		method    string
		want      bool
	}{
		{"nested template argument", "int A<std::vector<T>>::foo(int x)", "A", "foo", true},
		{"nested argument with a space between the closers", "int A<std::vector<T> >::foo(int x)", "A", "foo", true},
		{"two nested arguments", "int A<map<K, V>, list<T>>::foo(int x)", "A", "foo", true},
		{"three levels deep", "int A<B<C<D>>>::foo(int x)", "A", "foo", true},
		{"single level still matches", "int A<T>::foo(int x)", "A", "foo", true},
		{"a longer class name is still not this class", "int BA<B<C>>::foo(int x)", "A", "foo", false},
		{"a longer method name is still not this method", "int A<B<C>>::foobar(int x)", "A", "foo", false},
		{"an unbalanced argument list matches nothing", "int A<B<C>::foo(int x)", "A", "foo", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := signatureNamesQualifiedMethod(testCase.signature, testCase.container, testCase.method); got != testCase.want {
				t.Fatalf("signatureNamesQualifiedMethod(%q, %q, %q) = %v, want %v",
					testCase.signature, testCase.container, testCase.method, got, testCase.want)
			}
		})
	}
}

// TestSkipBalancedAnglesFindsTheMatchingCloser pins the helper's contract: it
// consumes the list that OPENS at text[0] and stops at the bracket matching it,
// reporting whether that bracket was ever reached. Text that opens no list
// consumes nothing.
func TestSkipBalancedAnglesFindsTheMatchingCloser(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		in     string
		rest   string
		closed bool
	}{
		{"<T>::foo(int x)", "::foo(int x)", true},
		{"<std::vector<T>>::foo(int x)", "::foo(int x)", true},
		{"<std::vector<T> >::foo(int x)", "::foo(int x)", true},
		{"<B<C<D>>>::foo(int x)", "::foo(int x)", true},
		{"<map<K, V>, list<T>>::foo(int x)", "::foo(int x)", true},
		{"<B<C>::foo(int x)", "<B<C>::foo(int x)", false},
		{"<T", "<T", false},
		// No list opens here, so nothing is consumed and nothing is closed —
		// a stray closer must never be read as the end of a list that never began.
		{">>x", ">>x", false},
		{"::foo(int x)", "::foo(int x)", false},
		{"", "", false},
	} {
		rest, closed := skipBalancedAngles(testCase.in)
		if rest != testCase.rest || closed != testCase.closed {
			t.Errorf("skipBalancedAngles(%q) = (%q, %v), want (%q, %v)",
				testCase.in, rest, closed, testCase.rest, testCase.closed)
		}
	}
}
