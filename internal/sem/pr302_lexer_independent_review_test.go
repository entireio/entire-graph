package sem

import "testing"

// All tests in this file use the production entry point available in both
// base841 and cdb. Expected positions are authored source-line indices, not
// positions reconstructed by the lexer under test. No parser/provider or other
// external component is required.

// A Python raw string retains the backslash before an escaped quote, but that
// quote does not terminate the string. Taking it as a Rust-style raw terminator
// turns literal "def run" text into a false declaration.
func TestReview302LexerPythonRawQuotes(t *testing.T) {
	tests := []struct {
		name   string
		lines  []string
		want   int
		wantOK bool
	}{
		{
			name:   "genuine_declaration_control",
			lines:  []string{"def run(): pass"},
			want:   0,
			wantOK: true,
		},
		{
			name:   "ordinary_quoted_string_control",
			lines:  []string{`sample = "\"; def run(): pass"`},
			wantOK: false,
		},
		{
			name:   "ordinary_raw_string_control",
			lines:  []string{`sample = r"def run(): pass"`},
			wantOK: false,
		},
		{
			name:   "raw_escaped_quote_has_no_declaration",
			lines:  []string{`sample = r"\"; def run(): pass"`},
			wantOK: false,
		},
		{
			name: "raw_escaped_quote_does_not_preempt_real_declaration",
			lines: []string{
				`sample = r"\"; def run(): pass"`,
				"def run(): pass",
			},
			want:   1,
			wantOK: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DeclarationLineIndex(tt.lines, 0, len(tt.lines)-1, "run")
			if ok != tt.wantOK || ok && got != tt.want {
				t.Errorf("declaration index/ok = %d/%v; want %d/%v; source lines: %q", got, ok, tt.want, tt.wantOK, tt.lines)
			}
		})
	}
}

// Punctuation inside a Python comment is not part of the surrounding decorator
// argument list. If it changes annotation depth, the real declaration is masked
// and a valid source anchor is lost.
func TestReview302LexerDecoratorCommentDepth(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  int
	}{
		{
			name: "ordinary_decorator_comment_control",
			lines: []string{
				"@policy(",
				"    enabled=True, # explanatory comment",
				")",
				"def run():",
				"    pass",
			},
			want: 3,
		},
		{
			name: "parenthesis_comment_outside_decorator_control",
			lines: []string{
				"@policy()",
				"# (",
				"def run():",
				"    pass",
			},
			want: 2,
		},
		{
			name: "parenthesis_in_comment_does_not_extend_decorator",
			lines: []string{
				"@policy(",
				"    enabled=True, # (",
				")",
				"def run():",
				"    pass",
			},
			want: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DeclarationLineIndex(tt.lines, 0, len(tt.lines)-1, "run")
			if !ok || got != tt.want {
				t.Errorf("declaration index/ok = %d/%v; want %d/true; source lines: %q", got, ok, tt.want, tt.lines)
			}
		})
	}
}

// Rust block comments nest. The inner terminator must not expose the remainder
// of the outer comment as code, even when it looks exactly like a declaration.
func TestReview302LexerRustNestedComments(t *testing.T) {
	tests := []struct {
		name   string
		lines  []string
		want   int
		wantOK bool
	}{
		{
			name:   "genuine_declaration_control",
			lines:  []string{"fn run() {}"},
			want:   0,
			wantOK: true,
		},
		{
			name:   "nonnested_comment_control",
			lines:  []string{"/* fn run() {} */"},
			wantOK: false,
		},
		{
			name: "nonnested_comment_then_real_declaration_control",
			lines: []string{
				"/* fn run() {} */",
				"fn run() {}",
			},
			want:   1,
			wantOK: true,
		},
		{
			name: "nested_comment_has_no_declaration",
			lines: []string{
				"/* outer",
				"/* inner */ fn run() {}",
				"*/",
			},
			wantOK: false,
		},
		{
			name: "nested_comment_does_not_preempt_real_declaration",
			lines: []string{
				"/* outer",
				"/* inner */ fn run() {}",
				"*/",
				"fn run() {}",
			},
			want:   3,
			wantOK: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DeclarationLineIndex(tt.lines, 0, len(tt.lines)-1, "run")
			if ok != tt.wantOK || ok && got != tt.want {
				t.Errorf("declaration index/ok = %d/%v; want %d/%v; source lines: %q", got, ok, tt.want, tt.wantOK, tt.lines)
			}
		})
	}
}
