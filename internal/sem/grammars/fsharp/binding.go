package fsharp

//#include "tree_sitter/parser.h"
//TSLanguage *tree_sitter_fsharp();
import "C"

import (
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"
)

// GetLanguage returns the vendored ionide/tree-sitter-fsharp grammar (ABI 14),
// promoting F# from inventory-only to the semantic tier.
func GetLanguage() *sitter.Language {
	return sitter.NewLanguage(unsafe.Pointer(C.tree_sitter_fsharp()))
}
