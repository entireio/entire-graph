package sem

import (
	"regexp"
	"testing"
)

// identifierBoundaryPattern is the regex forEachIdentifierToken replaced. The
// scan has to agree with it on every input, including the ones where a
// byte-wise reading could differ from a rune-wise one.
var identifierBoundaryPattern = regexp.MustCompile(`[A-Za-z0-9_$]+`)

func collectIdentifierTokens(s string) []string {
	var out []string
	forEachIdentifierToken(s, func(token string) { out = append(out, token) })
	return out
}

func TestForEachIdentifierTokenMatchesTheRegex(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"",
		"a",
		"$",
		"_",
		"9",
		"   ",
		"foo bar",
		"foo.bar.baz",
		"$this->prop->method()",
		"a_b$c9 D-E",
		"func (r *Reader) ReadFile(path string) (string, bool, error)",
		"trailing",
		"trailing ",
		" leading",
		"::separated::",
		"emoji 🚀 between",
		"accented café name",
		"日本語 identifier",
		"mixed🚀$token",
		"\x00null\x00bytes\x00",
		"tabs\tand\nnewlines\r\n",
		"UPPER lower 123 _under $dollar",
		"----",
		"a--b",
	}
	for _, in := range inputs {
		want := identifierBoundaryPattern.FindAllString(in, -1)
		got := collectIdentifierTokens(in)
		if len(want) != len(got) {
			t.Fatalf("input %q: got %d tokens %q, want %d %q", in, len(got), got, len(want), want)
		}
		for i := range want {
			if want[i] != got[i] {
				t.Fatalf("input %q: token %d is %q, want %q", in, i, got[i], want[i])
			}
		}
	}
}

// A source file rather than a hand-written string, so the two agree on real
// content and not only on the shapes this test thought to list.
func TestForEachIdentifierTokenMatchesTheRegexOnSource(t *testing.T) {
	t.Parallel()
	source := `package sem

import "strings"

// A comment with $dollars, 123 numbers and _underscores.
func Example(path string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	for _, part := range strings.Split(path, "/") {
		out[part] = struct{}{}
	}
	return out, nil
}
`
	want := identifierBoundaryPattern.FindAllString(source, -1)
	got := collectIdentifierTokens(source)
	if len(want) != len(got) {
		t.Fatalf("got %d tokens, want %d", len(got), len(want))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("token %d is %q, want %q", i, got[i], want[i])
		}
	}
}

func TestForEachIdentifierTokenAllocatesNothing(t *testing.T) {
	// No t.Parallel: testing.AllocsPerRun panics when the test is parallel.
	source := "func Example(path string) (map[string]struct{}, error) { return nil, nil }"
	count := 0
	allocs := testing.AllocsPerRun(100, func() {
		count = 0
		forEachIdentifierToken(source, func(string) { count++ })
	})
	if count == 0 {
		t.Fatal("scanned no tokens")
	}
	if allocs != 0 {
		t.Fatalf("scan allocated %.0f times per run, want 0", allocs)
	}
}
