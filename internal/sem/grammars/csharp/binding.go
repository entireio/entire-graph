package csharp

//#include "tree_sitter/parser.h"
//TSLanguage *tree_sitter_c_sharp();
import "C"

import (
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"
)

// GetLanguage returns the vendored tree-sitter-c-sharp grammar
// (v0.23.5, regenerated at ABI 14), replacing the C# 10-era grammar
// bundled with go-tree-sitter so C# 12/13 syntax (collection
// expressions, params collections, primary constructors) parses
// without ERROR nodes.
func GetLanguage() *sitter.Language {
	return sitter.NewLanguage(unsafe.Pointer(C.tree_sitter_c_sharp()))
}
