package sem

import "testing"

// Exercises the existing language-unknown exported entry point, so the same
// file is portable to cdb and the dialect-aware repair. This is not a claim
// about DeclarationLineIndexFor with an explicit Rust language or .rs path.
func TestReview302FallbackRustRawMultiline(t *testing.T) {
	for _, tc := range []struct {
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
			name:  "single_line_raw_control",
			lines: []string{`const S: &str = r"fn run() {}";`},
		},
		{
			name: "hashed_multiline_raw_control",
			lines: []string{
				`const S: &str = r#"`,
				`fn run() {}`,
				`"#;`,
			},
		},
		{
			name: "unhashed_multiline_raw_has_no_declaration",
			lines: []string{
				`const S: &str = r"`,
				`fn run() {}`,
				`";`,
			},
		},
		{
			name: "unhashed_multiline_raw_does_not_preempt_real_declaration",
			lines: []string{
				`const S: &str = r"`,
				`fn run() {}`,
				`";`,
				`fn run() {}`,
			},
			want:   3,
			wantOK: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DeclarationLineIndex(tc.lines, 0, len(tc.lines)-1, "run")
			if ok != tc.wantOK || ok && got != tc.want {
				t.Errorf("declaration index/ok = %d/%v; want %d/%v; source lines: %q", got, ok, tc.want, tc.wantOK, tc.lines)
			}
		})
	}
}
