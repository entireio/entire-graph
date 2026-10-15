package cli

import "testing"

func TestStatsSedStdoutRedirectIsAWrite(t *testing.T) {
	for _, command := range []string{
		`sed 's/foo/bar/' file.go > out.txt`,
		`sed -n '1,80p' file.go >> out.txt`,
		`sed -n '1,80p' file.go 1> out.txt`,
		`sed -n '1,80p' file.go 01> out.txt`,
	} {
		if isExploringShellCommand(command) {
			t.Errorf("file write counted as exploration: %s", command)
		}
	}
	for _, command := range []string{
		`sed -n '1,80p' file.go`,
		`sed -n '1,80p' file.go 2> err.txt`,
		`sed 's/>/x/' file.go`,
		`sed -n '1,80p' 'odd>name.go'`,
	} {
		if !isExploringShellCommand(command) {
			t.Errorf("source read omitted from exploration: %s", command)
		}
	}
}
