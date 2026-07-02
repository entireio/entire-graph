package r

//#include "tree_sitter/parser.h"
//const TSLanguage *tree_sitter_r();
import "C"

import (
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"
)

// GetLanguage returns the vendored tree-sitter-r grammar (r-lib, v1.3.0,
// ABI 14), promoting R from inventory-only to the semantic tier.
func GetLanguage() *sitter.Language {
	return sitter.NewLanguage(unsafe.Pointer(C.tree_sitter_r()))
}
