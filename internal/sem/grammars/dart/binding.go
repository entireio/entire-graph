package dart

//#include "tree_sitter/parser.h"
//TSLanguage *tree_sitter_dart();
import "C"

import (
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"
)

// GetLanguage returns the vendored tree-sitter-dart grammar (npm, ABI 14),
// promoting Dart from inventory-only to the semantic tier.
func GetLanguage() *sitter.Language {
	return sitter.NewLanguage(unsafe.Pointer(C.tree_sitter_dart()))
}
