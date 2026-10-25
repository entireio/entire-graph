package sem

import (
	"slices"
	"testing"
)

// A function whose BODY says "tool" and "schema" (here, in a vocabulary list
// and a comment) is not a tool handler, and must not get a second, synthetic
// kind=tool definition of its own name: that duplicate made every def,
// neighbors and impact lookup of the name ambiguous. The MCP tools/call
// dispatcher in the same file is one, and keeps its tool boundary and its
// HANDLES_TOOL edge.
func TestToolBoundaryComesFromIdentifiersNotBodyText(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.com/brain\n\ngo 1.22\n")
	writeFile(t, repo, "mcp.go", `package brain

import "strings"

// historyTerms picks domain vocabulary out of text. A tool handler schema
// would execute elsewhere; this only matches words.
func historyTerms(text string) []string {
	candidates := []string{"decision", "schema", "tool", "handler", "execute"}
	var terms []string
	for _, candidate := range candidates {
		if strings.Contains(text, candidate) {
			terms = append(terms, candidate)
		}
	}
	return terms
}

type mcpToolCallParams struct {
	Name string
}

func handleMCPToolCall(params mcpToolCallParams) []string {
	return historyTerms(params.Name)
}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var historyDefinitions []string
	for _, symbol := range snapshot.Symbols {
		if symbol.Name == "historyTerms" {
			historyDefinitions = append(historyDefinitions, symbol.Kind)
		}
	}
	if !slices.Equal(historyDefinitions, []string{"function"}) {
		t.Fatalf("historyTerms definitions = %v, want exactly [function]: its body text fabricated a tool boundary", historyDefinitions)
	}
	tool := symbolByKindAndName(snapshot.Symbols, "tool", "handleMCPToolCall")
	if tool.ID == "" {
		t.Fatalf("the MCP tools/call dispatcher lost its tool boundary: %v", toolSymbolNames(snapshot.Symbols))
	}
	handler := symbolByKindAndName(snapshot.Symbols, "function", "handleMCPToolCall")
	handled := false
	for _, relation := range relationsOfType(snapshot.Relations, "HANDLES_TOOL") {
		if relation.FromID == handler.ID && relation.ToID == tool.ID {
			handled = true
		}
	}
	if !handled {
		t.Fatalf("missing HANDLES_TOOL %s -> %s in %#v", handler.ID, tool.ID, relationsOfType(snapshot.Relations, "HANDLES_TOOL"))
	}
	if got := toolSymbolNames(snapshot.Symbols); !slices.Equal(got, []string{"handleMCPToolCall"}) {
		t.Fatalf("tool boundaries = %v, want only handleMCPToolCall", got)
	}
}

func TestLooksLikeToolHandler(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		symbol SymbolRecord
		want   bool
	}{
		// Genuine handlers.
		{"mcp tools/call dispatcher", SymbolRecord{Kind: "function", Name: "handleMCPToolCall", QualifiedName: "handleMCPToolCall"}, true},
		{"snake_case method", SymbolRecord{Kind: "method", Name: "execute_tool_handler", QualifiedName: "AuthService.execute_tool_handler"}, true},
		{"handler noun", SymbolRecord{Kind: "function", Name: "searchToolHandler", QualifiedName: "searchToolHandler"}, true},
		{"action on a tool class", SymbolRecord{Kind: "method", Name: "execute", QualifiedName: "SearchTool.execute"}, true},
		{"handler class", SymbolRecord{Kind: "class", Name: "ToolExecutor", QualifiedName: "ToolExecutor"}, true},
		{"mcp sdk handler signature", SymbolRecord{Kind: "function", Name: "handleSearch", QualifiedName: "handleSearch",
			Signature: "func handleSearch(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error)"}, true},

		// Not handlers.
		{"vocabulary function", SymbolRecord{Kind: "function", Name: "historyTerms", QualifiedName: "historyTerms"}, false},
		{"predicate about handlers", SymbolRecord{Kind: "function", Name: "looksLikeToolHandler", QualifiedName: "looksLikeToolHandler"}, false},
		{"test of a handler", SymbolRecord{Kind: "function", Name: "TestMCPToolHandlerRejectsUnknownTool", QualifiedName: "TestMCPToolHandlerRejectsUnknownTool"}, false},
		{"field holding a handler", SymbolRecord{Kind: "field", Name: "ToolHandler", QualifiedName: "Server.ToolHandler"}, false},
		{"markdown heading", SymbolRecord{Kind: "section", Name: "Tool handlers", QualifiedName: "Tool handlers"}, false},
		{"method of a handler", SymbolRecord{Kind: "method", Name: "close", QualifiedName: "ToolHandler.close"}, false},
		{"other handler on a tool-named server", SymbolRecord{Kind: "method", Name: "handleHealth", QualifiedName: "ToolServer.handleHealth"}, false},
		{"bare action on a server, not a tool", SymbolRecord{Kind: "method", Name: "handle", QualifiedName: "ToolServer.handle"}, false},
		{"tool method handling something else", SymbolRecord{Kind: "method", Name: "handleHealth", QualifiedName: "SearchTool.handleHealth"}, false},
		{"tool-call data", SymbolRecord{Kind: "type", Name: "ToolCall", QualifiedName: "ToolCall"}, false},
		{"tool declarations", SymbolRecord{Kind: "function", Name: "mcpToolDefinitions", QualifiedName: "mcpToolDefinitions"}, false},
		{"longer identifier is not the sdk type", SymbolRecord{Kind: "function", Name: "decode", QualifiedName: "decode",
			Signature: "func decode(raw json.RawMessage) (CallToolRequestEnvelope, error)"}, false},
	}
	for _, tc := range cases {
		if got := looksLikeToolHandler(tc.symbol); got != tc.want {
			t.Errorf("%s: looksLikeToolHandler(%s %q) = %v, want %v", tc.name, tc.symbol.Kind, tc.symbol.QualifiedName, got, tc.want)
		}
	}
}

func TestIdentifierWords(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{
		"handleMCPToolCall":                {"handle", "mcp", "tool", "call"},
		"AuthService.execute_tool_handler": {"auth", "service", "execute", "tool", "handler"},
		"HTTPServer":                       {"http", "server"},
		"v2Handler":                        {"v2", "handler"},
		"Tool handlers":                    {"tool", "handlers"},
		"":                                 nil,
	}
	for identifier, want := range cases {
		if got := identifierWords(identifier); !slices.Equal(got, want) {
			t.Errorf("identifierWords(%q) = %q, want %q", identifier, got, want)
		}
	}
}

func toolSymbolNames(symbols []SymbolRecord) []string {
	var names []string
	for _, symbol := range symbols {
		if symbol.Kind == "tool" {
			names = append(names, symbol.Name)
		}
	}
	slices.Sort(names)
	return names
}
