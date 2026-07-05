package sem

import (
	"regexp"
	"strings"
)

var (
	perlReceiverChainRe   = regexp.MustCompile(`(?:\$?[A-Za-z_]\w*|[A-Za-z_]\w*(?:::[A-Za-z_]\w*)*)[ \t]*(?:->[ \t]*[A-Za-z_]\w*[ \t]*(?:\([^()\n]*\))?[ \t]*)+`)
	perlReceiverSegmentRe = regexp.MustCompile(`->[ \t]*([A-Za-z_]\w*)`)
)

// perlReceiverCalls extracts terminal `$obj->method` call sites. Perl commonly
// omits parentheses for method calls (`$self->stash`, `$base->protocol`), so the
// generic receiver scanner only sees a subset of real call sites.
func perlReceiverCalls(block string) []receiverCall {
	stripped := stripCodeLiteralsAndComments(block)
	var out []receiverCall
	seen := map[string]bool{}
	for _, loc := range perlReceiverChainRe.FindAllStringIndex(stripped, -1) {
		chain := stripped[loc[0]:loc[1]]
		segments := perlReceiverSegmentRe.FindAllStringSubmatch(chain, -1)
		if len(segments) == 0 {
			continue
		}
		method := segments[len(segments)-1][1]
		if method == "" || method == "new" {
			continue
		}
		receiver := strings.TrimSpace(strings.SplitN(chain, "->", 2)[0])
		receiver = strings.TrimPrefix(receiver, "$")
		if receiver == "" {
			continue
		}
		key := receiver + "." + method
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, receiverCall{Receiver: receiver, Method: method})
	}
	return out
}
