package gate

import "fmt"

// Rule is printed with every run so the verdict is never a black box. A reader
// who disagrees with the decision can see the rule that produced it without
// reading this file.
const Rule = `RULE  revert   = a breaking change with >=1 dependent AND no covering test
      continue = a breaking change with dependents, OR any entity left unchecked,
                 OR a companion gap, OR a clone left undrifted
      keep     = none of the above
      unusable = no dimension produced evidence

DEGRADATION  a dimension that did not run cannot produce a finding against you.
             coverage unavailable  -> nothing may reach revert; cap at continue
             risk unavailable      -> cap at continue
             both unavailable      -> unusable (exit 5)`

// Decide reduces the annotated entities and their findings to one verdict.
//
// The degradation branch is the reason this takes Availability. revert reads
// "has dependents AND no covering test". If coverage never ran, then no entity
// has a covering test, and every dependent-bearing change would satisfy the
// rule — a false accusation manufactured by a missing input rather than by
// anything wrong with the change. So an unavailable dimension caps the verdict
// instead of raising it.
func Decide(entities []ChangedEntity, findings []Finding, avail Availability) Verdict {
	if !avail.Risk && !avail.Coverage {
		return Unusable
	}

	if avail.Risk && avail.Coverage {
		for _, e := range entities {
			if e.ChangeType.Breaking() && e.Dependents > 0 && e.Coverage == Unchecked {
				return Revert
			}
		}
	}

	if len(findings) > 0 {
		return Continue
	}
	for _, e := range entities {
		if e.Coverage == Unchecked {
			return Continue
		}
	}
	return Keep
}

// DegradationNote explains a capped verdict in the report, so a reader is told
// that a dimension was missing rather than left to infer it from a suspiciously
// mild result.
func DegradationNote(avail Availability) string {
	switch {
	case !avail.Risk && !avail.Coverage:
		return "neither risk nor coverage produced evidence: verdict is unusable, not a pass"
	case !avail.Coverage:
		return "coverage did not run: no finding could reach revert, so this verdict is capped at continue"
	case !avail.Risk:
		return "risk did not run: dependent counts are absent, so this verdict is capped at continue"
	}
	return ""
}

// Summarise counts entities by coverage state for the report header. Reporting
// unchecked as its own number, beside verified rather than folded into it, is
// the whole point: an empty selection is not evidence of safety.
func Summarise(entities []ChangedEntity) string {
	var verified, unchecked, noResolver int
	for _, e := range entities {
		switch e.Coverage {
		case Verified:
			verified++
		case Unchecked:
			unchecked++
		default:
			noResolver++
		}
	}
	return fmt.Sprintf("%d entities changed · %d verified · %d unchecked · %d no-resolver",
		len(entities), verified, unchecked, noResolver)
}
