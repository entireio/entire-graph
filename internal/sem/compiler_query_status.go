package sem

import (
	"fmt"
	"github.com/entireio/entire-graph/internal/compiler"
)

// No selected source is a coverage limitation, never a successful negative
// compiler result. Avoid starting a server solely for an empty retrieval scope.
func compilerNoSelectedSource(options *CompilerOptions) (*CompilerOverlay, error) {
	if options == nil {
		return nil, nil
	}
	if options.Require {
		return nil, fmt.Errorf("required compiler coverage unavailable: no selected source")
	}
	return &CompilerOverlay{Report: compiler.Report{Status: "unavailable", Diagnostics: []compiler.Diagnostic{{Code: "compiler_no_selected_source", Detail: "No source files were selected for this query."}}}}, nil
}
