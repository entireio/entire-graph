package sem

import (
	"go/scanner"
	"go/token"
	"path/filepath"
	"strings"
)

// Registration tables name source identifiers. Go strings cannot define a Go
// callable; its lexer can therefore eliminate comment/string-only matches.
// Other languages retain the conservative scan: strings can contain embedded
// source or callable names, so a generic string stripper would lose candidates.
func mentionsRegistrationHandlerInFile(path, content string, aliases map[string][]string) bool {
	if !mentionsRegistrationHandler(content, aliases) {
		return false
	}
	if !strings.EqualFold(filepath.Ext(path), ".go") {
		return true
	}
	var lex scanner.Scanner
	failed := false
	lex.Init(token.NewFileSet().AddFile(path, -1, len(content)), []byte(content), func(token.Position, string) { failed = true }, 0)
	for {
		_, kind, literal := lex.Scan()
		if kind == token.IDENT && len(aliases[literal]) > 0 {
			return true
		}
		if kind == token.EOF {
			return failed
		} // malformed source keeps the parser fallback
	}
}
