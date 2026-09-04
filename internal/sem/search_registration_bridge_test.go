package sem

import (
	"fmt"
	"testing"
)

// A command verb reaches its implementation only through a registration table
// (commands/<verb>.json -> "function": handler), and the provider indexes the
// verb as a searchable alias of the handler symbol. Above MaxIndexedFiles the
// selective snapshot is built from the preselected files alone, so a query for
// the verb selected the JSON table — the one file that cannot answer it — and
// left the handler unparsed, which is the only state in which the alias can
// never be attached.
func TestSearchSelectsRegistrationHandlerAboveIndexLimit(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "src/commands/substr.json", `{"function":"getrangeCommand","arity":4,"summary":"Returns a substring."}`)
	write(t, repo, "src/t_string.go", "package src\n\nfunc getrangeCommand() {}\n")
	for index := 0; index < 40; index++ {
		write(t, repo, fmt.Sprintf("src/filler%02d.go", index), fmt.Sprintf("package src\n\nfunc Filler%02d() {}\n", index))
	}

	response, err := SearchRepository(t.Context(), repo, "test-version", "substr", SearchOptions{
		Profile:         ProfileSyntaxOnly,
		TopK:            5,
		MaxIndexedFiles: 2,
		Worktree:        true,
		DisableCache:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, result := range response.Results {
		if result.FilePath == "src/t_string.go" && result.SymbolName == "getrangeCommand" {
			found = true
		}
	}
	if !found {
		t.Fatalf("handler getrangeCommand not returned for its command verb; results: %+v", response.Results)
	}
}

// The bridge must not fire on a repository with no registration table, and must
// not add a file that only mentions the handler name as text.
func TestBridgeRegistrationHandlerFilesIsInert(t *testing.T) {
	contents := map[string]string{
		"src/commands/substr.json": `{"function":"getrangeCommand"}`,
		"src/t_string.go":          "package src\n\nfunc getrangeCommand() {}\n",
		"docs/commands.md":         "getrangeCommand implements substr",
		"src/other.go":             "package src\n\nfunc Other() {}\n",
	}
	paths := []string{"docs/commands.md", "src/commands/substr.json", "src/other.go", "src/t_string.go"}
	source := sourceContext{
		paths: paths,
		read: func(path string) (string, bool) {
			content, ok := contents[path]
			return content, ok
		},
	}

	got := bridgeRegistrationHandlerFiles(t.Context(), source, []string{"src/commands/substr.json"})
	want := []string{"src/commands/substr.json", "src/t_string.go"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("bridged = %v, want %v", got, want)
	}

	// No registration table in the selection: nothing to bridge.
	got = bridgeRegistrationHandlerFiles(t.Context(), source, []string{"src/other.go"})
	if fmt.Sprint(got) != fmt.Sprint([]string{"src/other.go"}) {
		t.Fatalf("bridged without a command table = %v", got)
	}

	// Already selected: no duplicate.
	got = bridgeRegistrationHandlerFiles(t.Context(), source, []string{"src/commands/substr.json", "src/t_string.go"})
	if fmt.Sprint(got) != fmt.Sprint([]string{"src/commands/substr.json", "src/t_string.go"}) {
		t.Fatalf("bridged with the handler already selected = %v", got)
	}
}

// The handler must be matched as a whole identifier applied to a parameter
// list, so neither prose nor a longer identifier that merely contains the name
// drags a file into a selective index.
func TestContainsAppliedIdentifier(t *testing.T) {
	for _, testCase := range []struct {
		content string
		name    string
		want    bool
	}{
		{"void getrangeCommand(client *c) {}", "getrangeCommand", true},
		{"func getrangeCommand() {}", "getrangeCommand", true},
		{"  getrangeCommand (c);", "getrangeCommand", true},
		{`{"function":"getrangeCommand"}`, "getrangeCommand", false},
		{"getrangeCommand implements substr", "getrangeCommand", false},
		{"void xgetrangeCommand(c) {}", "getrangeCommand", false},
		{"void getrangeCommandExtra(c) {}", "getrangeCommand", false},
		{"", "getrangeCommand", false},
		{"getrangeCommand(", "", false},
	} {
		if got := containsAppliedIdentifier(testCase.content, testCase.name); got != testCase.want {
			t.Fatalf("containsAppliedIdentifier(%q, %q) = %v, want %v", testCase.content, testCase.name, got, testCase.want)
		}
	}
}
