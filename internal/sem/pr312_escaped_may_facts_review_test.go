package sem

import (
	"strings"
	"testing"
)

func TestReview312AddressEscapeKeepsMiddlewareMayEvidence(t *testing.T) {
	for _, rewrite := range []bool{true, false} {
		source := review312OuterSetterCallbackSource(false)
		initial := "middleware.Recoverer"
		if rewrite {
			initial = `middleware.StripPrefix("/api")`
		}
		source = review312Replace(t, source, "mw := middleware.Recoverer", "mw := "+initial+"\n\t_ = &mw", 1)
		source = review312Replace(t, source, "\tset()", "\t_ = set", 1)
		review312AssertSeededCallbackRoutes(t, source, !rewrite)
	}
}
func TestReview312AddressEscapeKeepsClosureMayEvidence(t *testing.T) {
	for _, rewrite := range []bool{true, false} {
		source := review312OuterSetterCallbackSource(rewrite)
		source = review312Replace(t, source, "r.Group(func(sub chi.Router) {\n\t\t_ = sub\n\t\tset()\n\t})", "_ = &set\n\tset()", 1)
		review312AssertSeededCallbackRoutes(t, source, !rewrite)
	}
}

func review312Replace(t *testing.T, source, old, replacement string, n int) string {
	t.Helper()
	if n != 1 || strings.Count(source, old) != 1 {
		t.Fatalf("fixture replacement must match exactly once: %q", old)
	}
	return strings.Replace(source, old, replacement, n)
}
