package sem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goDeclarationsLost compares go/parser's declarations with the names the Go
// entity walk assigns. If the two disagree on a file that parses CLEANLY, the
// comparison would report a loss that is only a naming difference, and every
// grammar-gap file using that construct would be held as E_PARSE_ERROR. This
// package's own sources are a broad sample of real Go (generic receivers,
// grouped type declarations, repeated init, blank functions), so a clean parse
// of each must lose nothing.
func TestGoDeclarationsLostAgreesWithCleanParses(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, valid := goReferenceParse(src)
		if !valid {
			continue
		}
		entities, _, status := TreeSitterParser{}.ParseWithStatus(path, string(src))
		if status.ParseError || status.Partial {
			continue
		}
		checked++
		if lost := goDeclarationsLost(file, entities); len(lost) > 0 {
			t.Errorf("%s parses cleanly but reports lost declarations: %s", path, strings.Join(lost, ", "))
		}
	}
	if checked < 50 {
		t.Fatalf("only %d files were checked; the sample is too small to mean anything", checked)
	}
}
