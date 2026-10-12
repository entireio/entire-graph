package cli

import (
	"strings"
	"testing"
)

func TestStatsMultipleHeredocsRemainData(t *testing.T) {
	command := "cat <<A >a && cat <<B >b\nnoop\nA\nentire graph search --query fake\ngrep fake .\nB"
	if got := stripHeredocBodies(command); strings.Contains(got, "fake") {
		t.Fatalf("second body leaked: %q", got)
	}
	if _, ok := graphVerbFromCommand(command); ok {
		t.Fatal("data counted as graph invocation")
	}
	if isExploringShellCommand(command) {
		t.Fatal("data counted as exploration")
	}
	if !isExploringShellCommand(command + "\ngrep real .") {
		t.Fatal("later real command lost")
	}
	tabbed := "cat <<-EOF >a\n\tdata\n\tEOF\ngrep real ."
	if strings.Contains(stripHeredocBodies(tabbed), "data") {
		t.Fatal("tab delimiter not recognized")
	}
	spaced := "cat <<EOF >a\ndata\n EOF\ngrep real ."
	if !strings.Contains(stripHeredocBodies(spaced), "data") {
		t.Fatal("non-terminating space accepted")
	}
}
