package sem

import "testing"

// Losing lexical state at a newline must not turn annotation arguments or a
// raw-string body into a declaration. Expectations are literal source positions,
// not positions derived from the production scanner.
func TestReview302DeclarationLineLexicalContext(t *testing.T) {
	tests := []struct {
		name   string
		lines  []string
		want   int
		wantOK bool
	}{
		{
			name:   "plain_declaration_control",
			lines:  []string{"public void run() {}"},
			want:   0,
			wantOK: true,
		},
		{
			name:   "single_line_annotation_control",
			lines:  []string{"@Policy(run = true)", "public void run() {}"},
			want:   1,
			wantOK: true,
		},
		{
			name: "multiline_annotation_argument_is_not_declaration",
			lines: []string{
				"@Policy(",
				"    run = true,",
				"    audit = false",
				")",
				"public void run() {}",
			},
			want:   4,
			wantOK: true,
		},
		{
			name: "multiline_raw_string_has_no_declaration",
			lines: []string{
				"src := `",
				"func run() {}",
				"`",
			},
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DeclarationLineIndex(tt.lines, 0, len(tt.lines)-1, "run")
			if ok != tt.wantOK || ok && got != tt.want {
				t.Fatalf("declaration index/ok = %d/%v; want %d/%v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
