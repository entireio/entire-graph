package sem

// symbolBody is one symbol's source text paired with the literal- and
// comment-stripped copy the relation scanners actually read.
//
// Sixteen scanners derived that copy independently from the same text, so
// resolving one Go function's relations stripped its body sixteen times and the
// relation phase allocated 529 MiB doing it. Deriving it once and passing it
// down removes the repeats without changing what any scanner sees.
//
// The value and its immutable facts live for one symbol's relation pass. The
// workers share no mutable body state, and matching offsets always refer to
// this exact stripped text.
type symbolBody struct {
	text     string
	stripped string
	facts    *relationFacts
}

func newSymbolBody(text string) symbolBody {
	return symbolBody{text: text, stripped: stripCodeLiteralsAndComments(text)}
}

// newRelationSymbolBody shares immutable matches within one callable symbol's
// full relation pass. Language-specific interpretation remains in the scanners;
// facts never escape this body or enter a persistent cache.
func newRelationSymbolBody(text, language string, spec profileSpec) symbolBody {
	body := newSymbolBody(text)
	if spec.shareRelationFacts && callExtractionLanguage(language) {
		body.facts = collectRelationFacts(body.stripped)
	}
	return body
}

// masked returns the body for text a language mask derived from this one,
// reusing the receiver when the mask changed nothing. Most languages mask
// nothing, so most symbols are stripped once even where a mask could apply.
func (b symbolBody) masked(text string) symbolBody {
	if text == b.text {
		return b
	}
	return newSymbolBody(text)
}
