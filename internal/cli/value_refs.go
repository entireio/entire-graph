package cli

import (
	"fmt"
	"unicode"

	"github.com/entireio/entire-graph/internal/sem"
)

// VALUE REFERENCES ARE NOT MODELLED
// =================================
//
// The graph records CALLS, not references. A Go function used as a value — assigned to a variable
// (`var openForRanking = openHistoryFTS`), passed as an argument, or stored in a struct field or map
// literal — produces no edge at all: package-level var/const declarations are not symbols, and there
// is no REFERENCES relation. So `neighbors --direction in` on such a function lists only the code
// that calls it by name, and a production dependent that reaches it through the value is silently
// absent. Measured on entire-brain: `openHistoryFTS` reported four test callers and no production
// caller, while `history_fts.go` routes production through `openHistoryFTSForRanking`.
//
// Modelling the references needs var symbols plus a new relation type, which is a schema and cache
// change. Until then the gap is disclosed where the answer is read: a warning in the JSON `warnings`
// array, and one line beside the callers in agent and text output, carrying a grep the reader can
// run to find what the graph cannot see.
//
// It is emitted only where it can matter: a single resolved Go function or method, and only for an
// answer that reports callers (neighbors over CALLS with --direction in or both, and impact).

// valueReferenceDisclosureCode is the warning code. It is query-scoped, not a snapshot diagnostic,
// so the completeness banners do not count it among "diagnostics in this snapshot".
const valueReferenceDisclosureCode = "W_VALUE_REFERENCES_UNMODELLED"

// valueReferenceDisclosure returns the warning for focus, and false when the gap cannot affect it.
func valueReferenceDisclosure(focus neighborEndpoint) (sem.ProviderWarning, bool) {
	if !valueReferenceDisclosureApplies(focus) {
		return sem.ProviderWarning{}, false
	}
	return sem.ProviderWarning{
		Code:     valueReferenceDisclosureCode,
		Severity: "info",
		EffectOnCompleteness: "references to " + focus.Name + " as a value (assigned to a variable, passed as an argument, " +
			"stored in a struct field or map) are not modelled, so code that reaches it only through such a value is " +
			"missing from its callers and impact",
		Detail: "self-check from the repository root: " + valueReferenceGrep(focus.Name) +
			"; any non-comment hit that is not a call is a value reference the graph cannot see",
	}, true
}

// valueReferenceDisclosureApplies reports whether focus is a Go function or method whose name can be
// placed in the self-check verbatim. A name that is not a plain identifier never reaches a shell
// command the reader may run.
func valueReferenceDisclosureApplies(focus neighborEndpoint) bool {
	if focus.External || focus.Language != "Go" || (focus.Kind != "function" && focus.Kind != "method") {
		return false
	}
	return goIdentifier(focus.Name)
}

// goIdentifier reports whether name is a Go identifier: a letter or underscore, then letters, digits
// or underscores. That excludes every quote, space and shell metacharacter.
func goIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for index, r := range name {
		if r == '_' || unicode.IsLetter(r) || (index > 0 && unicode.IsDigit(r)) {
			continue
		}
		return false
	}
	return true
}

// valueReferenceGrep is the self-check. The trailing class excludes `(` so calls do not match, and the
// `$` alternative keeps a reference at the end of a line, `var x = name`, which is the common case.
func valueReferenceGrep(name string) string {
	return fmt.Sprintf("grep -rnE '(^|[^A-Za-z0-9_])%s([^(A-Za-z0-9_]|$)' --include='*.go' .", name)
}

// valueReferenceNote is the one-line agent and text form, or "" when warnings carry no disclosure for
// name. It is rendered from the warning, so text and JSON cannot disagree about whether it applies.
func valueReferenceNote(warnings []sem.ProviderWarning, name string) string {
	if !hasWarningCode(warnings, valueReferenceDisclosureCode) {
		return ""
	}
	return "Value refs unmodelled (var/arg/struct/map): " + valueReferenceGrep(name) + " ; non-call hits are missed callers"
}

// withValueReferenceDisclosure returns warnings plus the disclosure for focus when it applies. The
// input is never mutated: it is the snapshot's own slice.
func withValueReferenceDisclosure(warnings []sem.ProviderWarning, focus neighborEndpoint) []sem.ProviderWarning {
	disclosure, ok := valueReferenceDisclosure(focus)
	if !ok {
		return warnings
	}
	return append(append(make([]sem.ProviderWarning, 0, len(warnings)+1), warnings...), disclosure)
}

// snapshotWarnings returns warnings without the query-scoped disclosure, for the completeness
// banners, which report diagnostics "in this snapshot".
func snapshotWarnings(warnings []sem.ProviderWarning) []sem.ProviderWarning {
	for index, warning := range warnings {
		if warning.Code != valueReferenceDisclosureCode {
			continue
		}
		kept := append(make([]sem.ProviderWarning, 0, len(warnings)-1), warnings[:index]...)
		for _, rest := range warnings[index+1:] {
			if rest.Code != valueReferenceDisclosureCode {
				kept = append(kept, rest)
			}
		}
		return kept
	}
	return warnings
}

// hasWarningCode reports whether warnings carry code.
func hasWarningCode(warnings []sem.ProviderWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}
