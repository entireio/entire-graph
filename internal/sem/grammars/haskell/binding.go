package haskell

//#include "tree_sitter/parser.h"
//TSLanguage *tree_sitter_haskell();
import "C"

import (
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"
)

// GetLanguage returns the vendored tree-sitter-haskell grammar (v0.23.1, ABI
// 14), promoting Haskell from inventory-only to the semantic tier.
func GetLanguage() *sitter.Language {
	return sitter.NewLanguage(unsafe.Pointer(C.tree_sitter_haskell()))
}
