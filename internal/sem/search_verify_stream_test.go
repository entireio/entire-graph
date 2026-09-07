package sem

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestSearchVerifyExplainStreamsAndDrains(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell semantics")
	}
	for _, shell := range []string{"sh", "dash", "bash", "zsh"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		t.Run(shell, func(t *testing.T) {
			t.Parallel()
			t.Run("filter receives output before test finishes", func(t *testing.T) {
				marker := shellQuote(filepath.Join(t.TempDir(), "filter-ready"))
				// A buffering wrapper cannot finish the producer until the filter
				// runs, and cannot run the filter until the producer finishes.
				producer := "printf 'ready\\n'; for i in 1 2 3 4 5 6 7 8 9 10; do " +
					"[ -f " + marker + " ] && break; sleep 0.1; done; " +
					"[ -f " + marker + " ] || exit 9; printf 'done\\n'"
				filter := "read -r line; : > " + marker + "; printf '%s\\n' \"$line\"; cat"
				command, _ := composeSearchVerifyExplain(producer, filter)
				out, err := exec.Command(shell, "-c", command).CombinedOutput()
				if err != nil || string(out) != "ready\ndone\n" {
					t.Fatalf("output did not stream: %v, %q", err, out)
				}
			})
			t.Run("binary output and trailing newlines survive", func(t *testing.T) {
				command, _ := composeSearchVerifyExplain(`printf '\000x\n\n'`, "cat")
				out, err := exec.Command(shell, "-c", command).CombinedOutput()
				if err != nil || !bytes.Equal(out, []byte{0, 'x', '\n', '\n'}) {
					t.Fatalf("stream changed: %v, %q", err, out)
				}
			})
			for _, statuses := range [][2]int{{0, 0}, {7, 0}, {0, 3}, {7, 3}} {
				t.Run("early filter "+strconv.Itoa(statuses[0])+"/"+strconv.Itoa(statuses[1]), func(t *testing.T) {
					marker := filepath.Join(t.TempDir(), "test-finished")
					// Far larger than pipe capacity: without a reader that stays
					// alive after the filter exits, dd fails with SIGPIPE.
					producer := "dd if=/dev/zero bs=65536 count=128 2>/dev/null || exit 91; " +
						": > " + shellQuote(marker) + "; exit " + strconv.Itoa(statuses[0])
					command, _ := composeSearchVerifyExplain(producer, "exit "+strconv.Itoa(statuses[1]))
					out, err := exec.Command(shell, "-e", "-c", command).CombinedOutput()
					exitCode := 0
					if err != nil {
						exit, ok := err.(*exec.ExitError)
						if !ok {
							t.Fatal(err)
						}
						exitCode = exit.ExitCode()
					}
					want := statuses[0]
					if want == 0 {
						want = statuses[1]
					}
					if exitCode != want {
						t.Fatalf("exit %d, want %d: %q", exitCode, want, out)
					}
					if _, err := os.Stat(marker); err != nil {
						t.Fatalf("filter prevented test completion: %v", err)
					}
				})
			}
		})
	}
}
