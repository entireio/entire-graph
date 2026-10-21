package swift

//#include "tree_sitter/parser.h"
//TSLanguage *tree_sitter_swift();
import "C"

import (
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"
)

// GetLanguage returns the vendored tree-sitter-swift grammar (v0.7.4,
// ABI 14), replacing the 0.5.0 grammar bundled with go-tree-sitter so
// Swift 5.9-6 syntax (freestanding macros such as `#expect`) parses
// without ERROR nodes.
func GetLanguage() *sitter.Language {
	return sitter.NewLanguage(unsafe.Pointer(C.tree_sitter_swift()))
}
