package sem

import "fmt"

// EvidenceState classifies how much a caller can trust one Graph result (a relationship
// edge from `neighbors`, an `impact` entry or answer, or a `diff`/`analyze`/`commit`
// dependents count) AT FACE VALUE, without changing the Resolution/Confidence/WarningCodes
// data the graph already computes. It exists so every consumer of Graph relationship,
// impact, and semantic-diff results reports the same three-way verdict instead of each
// verb inventing its own reading of the resolution vocabulary.
//
//   - EvidenceConfirmed: the result came from a high-precision, single-target resolution
//     (or, for a purely structural fact such as container membership, from something the
//     graph did not have to infer at all). Treat it like ground truth.
//   - EvidencePartial: the result is real evidence, not a fabrication, but it was produced
//     by inference, fan-out, a documented heuristic relation kind, or a heuristic scan
//     (see HeuristicRelationTypes and the dependents-count scan in dependents.go). It is
//     useful but should not be read as the complete picture.
//   - EvidenceRequiresVerification: the graph could not resolve this with confidence —
//     commonly dynamic dispatch past its fan-out limit, reflection, generated/vendored
//     code, a bare name match, or a language it only inventories rather than parses
//     semantically. A MISSING relationship in this state must never be read as a confirmed
//     absence of a caller/dependency: the answer must be checked against source or tests.
type EvidenceState string

const (
	EvidenceConfirmed            EvidenceState = "confirmed"
	EvidencePartial              EvidenceState = "partial"
	EvidenceRequiresVerification EvidenceState = "requires_verification"
)

// evidenceRank orders states from most to least trustworthy so many per-edge states can be
// folded into one worst-case verdict for a whole answer.
func evidenceRank(state EvidenceState) int {
	switch state {
	case EvidenceConfirmed:
		return 0
	case EvidencePartial:
		return 1
	default:
		return 2
	}
}

// WorstEvidenceState folds a set of per-result states into the single most cautious verdict
// that covers all of them. Called with zero states it returns EvidenceConfirmed: an answer
// with nothing in it carries no per-edge doubt of its own (a verb that wants "empty could
// still be hiding something" — see impact's degenerate handling — escalates separately).
func WorstEvidenceState(states ...EvidenceState) EvidenceState {
	worst := EvidenceConfirmed
	for _, state := range states {
		if evidenceRank(state) > evidenceRank(worst) {
			worst = state
		}
	}
	return worst
}

// RelationEvidenceState classifies one relation edge. It is a pure re-classification of data
// the graph already computes — the Resolution vocabulary provider.go already emits, the
// heuristic relation-type list Capabilities() already declares, and the WEAK_PATTERN warning
// code already attached to individual edges — so it adds no new detection and cannot disagree
// with what `neighbors --format json` already shows in the `resolution`/`confidence` fields.
//
// An unrecognized or absent Resolution defaults to Confirmed rather than manufacturing new
// doubt: the graph already tells us, per edge, exactly which techniques are uncertain (see
// the resolution values switched on below); the absence of that tag is not itself a signal.
func RelationEvidenceState(relation RelationRecord) EvidenceState {
	for _, code := range relation.WarningCodes {
		if code == "WEAK_PATTERN" {
			return EvidenceRequiresVerification
		}
	}
	if heuristicRelationTypeSet[relation.Type] {
		return EvidencePartial
	}
	switch relation.Resolution {
	case "name_only", "pattern":
		// A bare name/text match: exactly the shape a reflection-based dispatch, a
		// dynamically-built call, or a generated binding leaves behind when static
		// resolution cannot follow it through.
		return EvidenceRequiresVerification
	case "type_inferred", "signature", "git_history":
		// Real evidence produced by inference (e.g. the Go-interface dynamic-dispatch
		// fan-out) or by a statistical signal (co-change history) rather than a
		// single-target resolved reference.
		return EvidencePartial
	default:
		return EvidenceConfirmed
	}
}

// LanguageEvidenceState reports the evidence ceiling a language can offer. A language the
// graph only inventories (file/symbol records, no relations — see InventoryOnlyLanguage) has
// no call/reference resolution running at all, so ANY count derived from a focus in that
// language — including zero — is a guess, not an answer.
func LanguageEvidenceState(language string) EvidenceState {
	if InventoryOnlyLanguage(language) {
		return EvidenceRequiresVerification
	}
	return EvidenceConfirmed
}

// EvidenceVerificationNote explains, in one sentence naming the subject, why an answer needs a
// human check and what to do about it. Shared by `impact` and `neighbors` so the wording (and
// the underlying claim: a low or missing count is not proof of absence) cannot drift between
// the two verbs that both roll relation-level evidence into one answer-wide verdict.
func EvidenceVerificationNote(subject string) string {
	if subject == "" {
		subject = "this symbol"
	}
	return fmt.Sprintf(
		"this answer includes relationships the graph could not resolve with confidence "+
			"(dynamic dispatch past its fan-out limit, reflection, generated/vendored code, or a "+
			"language it only inventories can all hide real callers/dependents); a low or zero count "+
			"above is NOT proof of absence -- verify %s by reading its source at the flagged "+
			"locations, tracing it with `entire graph neighbors --symbol %s --relation CALLS "+
			"--direction in`, or running the tests that cover it",
		subject, subject,
	)
}
