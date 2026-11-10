package sem

import (
	"fmt"
	"strings"
	"testing"
)

// goRouteInlineLineFixture is a Go file of n one-line functions, each registering one inline
// handler, joined by eol, with or without a trailing newline. Function k (1-based) is on line k+1.
func goRouteInlineLineFixture(n int, eol string, trailing bool) (string, []SymbolRecord) {
	var b strings.Builder
	b.WriteString("package p")
	symbols := make([]SymbolRecord, 0, n)
	for k := 1; k <= n; k++ {
		b.WriteString(eol)
		fmt.Fprintf(&b, `func reg%d() { http.HandleFunc("/r%d", func(w http.ResponseWriter, r *http.Request) {}) }`, k, k)
		name := fmt.Sprintf("reg%d", k)
		symbols = append(symbols, SymbolRecord{ID: "sym:" + name, Kind: "function", Name: name, FilePath: "routes.go", StartLine: k + 1, EndLine: k + 1})
	}
	if trailing {
		b.WriteString(eol)
	}
	return b.String(), symbols
}

func goRouteInlineRelationsFor(content string, symbols []SymbolRecord) []expressRouteRelation {
	files := []FileRecord{{Path: "routes.go", Language: "Go"}}
	read := func(path string) (string, bool) { return content, path == "routes.go" }
	_, inline, _, _ := goHTTPRouteRelations(files, map[string][]SymbolRecord{"routes.go": symbols}, read, newFileStringConstants())
	return inline
}

// The binary-search line lookup gives, at every byte offset, the line the old per-registration
// newline count gave: on CRLF text, on text without a trailing newline, and on empty text.
func TestGoRouteInlineLineLookupMatchesNewlineCount(t *testing.T) {
	t.Parallel()
	crlf, _ := goRouteInlineLineFixture(5, "\r\n", true)
	bare, _ := goRouteInlineLineFixture(5, "\n", false)
	for name, content := range map[string]string{"crlf": crlf, "no trailing newline": bare, "crlf no trailing newline": strings.TrimSuffix(crlf, "\r\n"), "empty": "", "only newlines": "\n\r\n\n"} {
		newlines := newlineOffsets(content)
		for offset := 0; offset <= len(content); offset++ {
			if got, want := lineAtOffset(newlines, offset), 1+strings.Count(content[:offset], "\n"); got != want {
				t.Fatalf("%s: offset %d: line %d, want %d", name, offset, got, want)
			}
		}
	}
}

// End to end through goHTTPRouteRelations: every inline registration is attributed to the one-line
// function it sits in, whose line the old newline count named, on CRLF text and on text whose last
// registration has no trailing newline.
func TestGoHTTPRouteRelationsInlineLinesCRLFAndNoTrailingNewline(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		eol      string
		trailing bool
	}{
		{"crlf", "\r\n", true},
		{"crlf no trailing newline", "\r\n", false},
		{"lf no trailing newline", "\n", false},
	} {
		content, symbols := goRouteInlineLineFixture(200, c.eol, c.trailing)
		inline := goRouteInlineRelationsFor(content, symbols)
		if len(inline) != len(symbols) {
			t.Fatalf("%s: %d inline relations, want %d", c.name, len(inline), len(symbols))
		}
		for _, relation := range inline {
			var k int
			if _, err := fmt.Sscanf(relation.Route, "/r%d", &k); err != nil {
				t.Fatalf("%s: unexpected route %q", c.name, relation.Route)
			}
			at := strings.Index(content, fmt.Sprintf(`http.HandleFunc("/r%d"`, k))
			oldLine := 1 + strings.Count(content[:at], "\n")
			if want := fmt.Sprintf("reg%d", k); relation.Handler.Name != want || relation.Handler.StartLine != oldLine {
				t.Fatalf("%s: route %s attributed to %s (line %d), want %s (line %d)", c.name, relation.Route, relation.Handler.Name, relation.Handler.StartLine, want, oldLine)
			}
		}
	}
}

// BenchmarkGoHTTPRouteRelationsManyInline is one function registering many inline handlers: the
// line lookup used to recount newlines from the file start per registration, quadratic in the file.
// Timing (Apple M-series, GOMAXPROCS=2, n=20000, 1.5 MB, -benchtime 3x): 1.58 s/op with the
// per-registration strings.Count, 0.41-0.47 s/op with newlineOffsets + sort.SearchInts (the rest is
// the route parse itself).
func BenchmarkGoHTTPRouteRelationsManyInline(b *testing.B) {
	const n = 20000
	var src strings.Builder
	src.WriteString("package p\n\nfunc register() {\n")
	for k := 0; k < n; k++ {
		fmt.Fprintf(&src, "\thttp.HandleFunc(\"/r%d\", func(w http.ResponseWriter, r *http.Request) {})\n", k)
	}
	src.WriteString("}\n")
	content := src.String()
	symbols := []SymbolRecord{{ID: "sym:register", Kind: "function", Name: "register", FilePath: "routes.go", StartLine: 3, EndLine: n + 4}}
	b.SetBytes(int64(len(content)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if inline := goRouteInlineRelationsFor(content, symbols); len(inline) != n {
			b.Fatalf("%d inline relations, want %d", len(inline), n)
		}
	}
}
