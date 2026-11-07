package rust

//#include "tree_sitter/parser.h"
//TSLanguage *tree_sitter_rust();
import "C"

import (
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"
)

// GetLanguage returns the vendored tree-sitter-rust grammar (origin in
// ../grammars.json), replacing the one bundled with go-tree-sitter.
func GetLanguage() *sitter.Language {
	return sitter.NewLanguage(unsafe.Pointer(C.tree_sitter_rust()))
}
