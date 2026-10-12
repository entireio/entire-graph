package cli

import "testing"

func TestExplorationWithStderrAppend(t *testing.T) {
	for _, command := range []string{"cat file 2>> log", "cat file 3>> log", "cat file 2>>log 1>&2"} {
		if !isExploringShellCommand(command) {
			t.Errorf("read omitted: %s", command)
		}
	}
	for _, command := range []string{"cat file >> log", "cat file 1>>log"} {
		if isExploringShellCommand(command) {
			t.Errorf("write counted as read: %s", command)
		}
	}
}
