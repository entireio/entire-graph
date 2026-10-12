package cli

import "testing"

func TestExplorationRedirectDescriptorTokens(t *testing.T) {
	for _, command := range []string{"cat file 2>> log", "cat file 3>> log", "cat file 11>log", "cat file 21>log", "cat file 11>>log", "cat file 12>log", "cat file 2>>log 1>&2"} {
		if !isExploringShellCommand(command) {
			t.Errorf("read omitted: %s", command)
		}
	}
	for _, command := range []string{"cat file >> log", "cat file 1>>log", "cat file 01>log", "cat data2>log", "cat 'data'2>log", "cat file '11'>log"} {
		if isExploringShellCommand(command) {
			t.Errorf("stdout write counted as read: %s", command)
		}
	}
}
