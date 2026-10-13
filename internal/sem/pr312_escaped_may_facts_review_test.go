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
		source = strings.Replace(source, "mw := middleware.Recoverer", "mw := "+initial+"\n\t_ = &mw", 1)
		source = strings.Replace(source, "\tset()", "\t_ = set", 1)
		review312AssertSeededCallbackRoutes(t, source, !rewrite)
	}
}
func TestReview312AddressEscapeKeepsClosureMayEvidence(t *testing.T) {
	for _, rewrite := range []bool{true, false} {
		source := review312OuterSetterCallbackSource(rewrite)
		source = strings.Replace(source, "r.Group(func(sub chi.Router) {\n\t\t_ = sub\n\t\tset()\n\t})", "_ = &set\n\tset()", 1)
		review312AssertSeededCallbackRoutes(t, source, !rewrite)
	}
}
