package sem

import (
	"fmt"
	"strings"
	"testing"
)

// A local closure with no rewrite writes must not make every branch scan
// every visible binding. With many package variables that scan was
// O(vars x branches): a large generated file ran out of its step budget and
// dropped every route in it, /health included.
func TestGoRouteClosureRewriteTargetsDoNotScanEveryBinding(t *testing.T) {
	var src strings.Builder
	src.WriteString("package p\n\nimport \"net/http\"\n\n")
	for i := 0; i < 50000; i++ {
		fmt.Fprintf(&src, "var v%d = %d\n", i, i)
	}
	src.WriteString("func main() {\n\tmux := http.NewServeMux()\n\tf := func() {}\n\tf()\n")
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&src, "\tif v%d > 0 { v%d++ }\n", i, i+1)
	}
	src.WriteString("\tmux.HandleFunc(\"/health\", health)\n\thttp.ListenAndServe(\":1\", mux)\n}\n")
	src.WriteString("func health(w http.ResponseWriter, r *http.Request) {}\n")
	content := src.String()
	receivers := resolveGoRouteReceivers(content, nil)
	if receivers.exhausted {
		t.Fatalf("resolution exhausted its step budget on a %d-byte file", len(content))
	}
	assertGoRouteSet(t, goRouteRegistrations(content, nil), "/health -> health")
}
