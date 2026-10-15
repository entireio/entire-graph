package sem

import (
	"strings"
	"testing"
)

func TestDeclarationBracketMaskRespectsLanguage(t *testing.T) {
	for _, tc := range []struct {
		language, path, opening string
		masked                  bool
	}{
		{"Go", "a.go", "[", false}, {"Python", "a.py", "[", false}, {"JavaScript", "a.js", "[", false},
		{"C++", "a.cpp", "[", false}, {"Objective-C", "a.m", "[", false},
		{"C#", "a.cs", "[", true}, {"F#", "a.fs", "[<", true}, {"C++", "a.cpp", "[[", true},
		{"", "", "[", true},
	} {
		t.Run(tc.language+tc.opening, func(t *testing.T) {
			closing := strings.Repeat("]", strings.Count(tc.opening, "["))
			if tc.opening == "[<" {
				closing = ">]"
			}
			lines := []string{tc.opening, " NamedToken", closing, "func AfterMask() {}"}
			code := DeclarationCodeLinesFor(tc.language, tc.path, lines, 0, len(lines)-1)
			if visible := strings.Contains(strings.Join(code, "\n"), "NamedToken"); visible == tc.masked {
				t.Fatalf("masked=%v, code=%q", tc.masked, code)
			}
			if !strings.Contains(code[3], "AfterMask") {
				t.Fatalf("mask leaked past attribute: %q", code)
			}
		})
	}
}
