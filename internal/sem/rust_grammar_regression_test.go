package sem

import "testing"

// The bundled go-tree-sitter Rust grammar reported a parse error on each of
// the "newer syntax" inputs; the vendored tree-sitter-rust 0.24.2 accepts
// them. The "older surface" inputs parsed on the bundled grammar and must keep
// parsing. A grammar refresh must preserve both.
func TestRustGrammarRegressionSyntax(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		want   map[string]string // entity name -> kind
		line   map[string]int    // entity name -> start line
	}{
		{
			name: "newer syntax: raw borrows",
			source: `pub struct Packet {
    header: u8,
}

impl Packet {
    pub fn header_ptr(&self) -> *const u8 {
        &raw const self.header
    }

    pub fn header_mut_ptr(&mut self) -> *mut u8 {
        &raw mut self.header
    }
}

pub fn after_raw_borrows() {}
`,
			want: map[string]string{
				"Packet":                "struct",
				"Packet.header_ptr":     "method",
				"Packet.header_mut_ptr": "method",
				"after_raw_borrows":     "function",
			},
		},
		{
			name: "newer syntax: gen block",
			source: `pub fn evens() -> impl Iterator<Item = u32> {
    gen {
        yield 2;
        yield 4;
    }
}

pub fn after_gen() {}
`,
			want: map[string]string{"evens": "function", "after_gen": "function"},
		},
		{
			name: "newer syntax: async closure",
			source: `pub async fn run() -> u32 {
    let task = async || 1;
    task().await
}

pub fn after_async_closure() {}
`,
			want: map[string]string{"run": "function", "after_async_closure": "function"},
		},
		{
			name: "newer syntax: half-open range pattern",
			source: `pub fn bucket(value: u32) -> u32 {
    match value {
        ..=9 => 1,
        _ => 2,
    }
}
`,
			want: map[string]string{"bucket": "function"},
		},
		{
			name: "newer syntax: shebang line before an inner attribute",
			source: `#!/usr/bin/env run-cargo-script
#![allow(dead_code)]

fn main() {}
`,
			want: map[string]string{"main": "function"},
			line: map[string]int{"main": 4},
		},
		{
			// rust-lang/rust tests/ui/parser/shebang/multiline-attrib.rs: a
			// leading "#!" is not a shebang when the next token is "[".
			// 0.24.2 lexes it as one; maskRustUnsupportedSyntax repairs that.
			name: "older surface: inner attribute split after #!",
			source: `#!
[allow(unused_variables)]

fn main() {
    let x = 5;
}
`,
			want: map[string]string{"main": "function"},
			line: map[string]int{"main": 4},
		},
		{
			name:   "older surface: inner attribute split by a blank line",
			source: "#!\r\n\r\n  [allow(unused_variables)]\r\nfn main() {}\r\n",
			want:   map[string]string{"main": "function"},
			line:   map[string]int{"main": 4},
		},
		{
			name: "older surface: inner attribute on one line",
			source: `#! [allow(dead_code)]
#![allow(unused_variables)]
fn main() {}
`,
			want: map[string]string{"main": "function"},
			line: map[string]int{"main": 3},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entities, language, status := TreeSitterParser{}.ParseWithStatus("regression.rs", tc.source)
			if language != "Rust" || status.ParseError || status.Code != "" {
				t.Fatalf("language=%q status=%+v", language, status)
			}
			found := map[string]Entity{}
			for _, entity := range entities {
				found[entity.Name] = entity
			}
			for name, kind := range tc.want {
				entity, ok := found[name]
				if !ok || entity.Kind != kind {
					t.Errorf("want %s %s; got %+v in %v", kind, name, entity, entityNames(entities))
				}
				if line, ok := tc.line[name]; ok && entity.StartLine != line {
					t.Errorf("%s starts on line %d, want %d", name, entity.StartLine, line)
				}
			}
		})
	}
}

// A real shebang must stay a shebang: only whitespace may separate "#!" from
// the "[" that makes it an inner attribute.
func TestRustShebangMaskLeavesShebangsAlone(t *testing.T) {
	for _, source := range []string{
		"#!/usr/bin/env run-cargo-script\n[allow(dead_code)]\nfn main() {}\n",
		"#!/usr/bin/env cargo\n\nfn main() {}\n",
		"#![allow(dead_code)]\nfn main() {}\n",
		"#!",
		"#!\n",
		"fn main() {}\n#!\n[allow(dead_code)]\n",
	} {
		if got := maskRustUnsupportedSyntax(t.Context(), source); got != source {
			t.Errorf("mask rewrote %q to %q", source, got)
		}
	}
	source := "#!\n\t[allow(dead_code)]\nfn main() {}\n"
	want := "\n\t#![allow(dead_code)]\nfn main() {}\n"
	got := maskRustUnsupportedSyntax(t.Context(), source)
	if got != want {
		t.Errorf("mask = %q, want %q", got, want)
	}
}
