package pgsql

// #cgo CFLAGS: -std=c11 -fPIC -I./src
// #include "src/parser.c"
// #include "src/scanner.c"
import "C"

import (
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"
)

// GetLanguage returns the PostgreSQL-capable tree-sitter SQL grammar.
func GetLanguage() *sitter.Language {
	return sitter.NewLanguage(unsafe.Pointer(C.tree_sitter_sql()))
}

func Version() uint32 {
	return uint32(C.tree_sitter_sql().version)
}
