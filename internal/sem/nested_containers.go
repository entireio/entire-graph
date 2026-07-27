package sem

// Lexical containment for nested declarations
// ===========================================
//
// A symbol's container is normally read off its QUALIFIED NAME: `Ops.lookup` is contained by
// whatever symbol is named `Ops`. That works for MEMBERS, because the walk qualifies a member
// under the type that declares it. It does not work for the NESTED TYPE ITSELF: a type
// declaration takes its name verbatim from the source, so a private nested enum
//
//	class ArithmeticPostAggregator {          // 49-353
//	  private enum Ops { ... }                // 213-282, qualified name "Ops"
//	}
//
// is emitted with the qualified name `Ops` and therefore NO container at all. Its own members
// resolve correctly (`Ops.lookup` -> `Ops`), so the subtree hangs off nothing: the graph knows
// `Ops` exists and knows what is inside it, but not that it is inside
// `ArithmeticPostAggregator`.
//
// Everything derived from containment then misses it. There is no CONTAINS edge from the
// enclosing class, so the nested type is not one of its members; `sameContainerFlowSymbols`,
// the related-sites co-member route and the container map all enumerate members by container
// id and so cannot see it; and `childNamesByContainer` does not know the enclosing class can
// name it. Measured on apache/druid at 51dfde0284: 1,542 Java symbols in 661 files were
// lexically nested inside another symbol of the same file while carrying no container — 1,161
// classes, 124 interfaces, 97 enums, 160 methods.
//
// The same hole exists wherever a declaration's name is not qualified by its surroundings:
// Java/C#/Kotlin nested and inner types, Python inner classes, Ruby nested classes and
// modules, Go types declared inside a function, Rust items declared inside a function, and the
// method set of a JS/TS object literal (whose lexical parent is the `variable` that holds it).
//
// The fix is to fall back to LEXICAL containment: when a symbol's qualified name names no
// owner, its container is the smallest symbol of the same file that strictly encloses its line
// range. The fallback is deliberately narrow — it fires only when the parser gave the symbol no
// owner name at all, so a member that DOES name an owner the file did not define is still left
// to the cross-file container resolver rather than being silently reparented to whatever
// happens to surround it.

// Reasons reported on a CONTAINS relation. They are not decoration: a consumer auditing the
// graph has to be able to tell a containment the SOURCE spelled out (a member qualified under
// its owner) from one this file inferred from line ranges alone.
const (
	containmentReasonQualified = "symbol qualified name is nested in container"
	containmentReasonLexical   = "symbol is lexically nested in container"
)

// containmentReason says how a symbol's container was established.
func containmentReason(symbol SymbolRecord) string {
	if containerName(symbol.QualifiedName) != "" {
		return containmentReasonQualified
	}
	return containmentReasonLexical
}

// lexicalEntityParents returns, for each entity, the index of the smallest other entity that
// strictly encloses its line range, or -1 when nothing does.
//
// Entities arrive in the walk's preorder, so an enclosing declaration always precedes the ones
// inside it and a stack of still-open declarations is enough; the pass is O(n) amortized rather
// than the O(n^2) a pairwise scan would cost on a file with thousands of symbols. An entity
// emitted out of order simply finds no parent, which is the pre-existing behaviour.
//
// "Strictly" excludes an equal range: two records describing the same span (a declaration head
// and the declaration, say) are the same region of code, not a nesting, and making one the
// other's container would invent a containment level that the source does not have.
func lexicalEntityParents(entities []Entity) []int {
	parents := make([]int, len(entities))
	open := make([]int, 0, 16)
	for index, entity := range entities {
		parents[index] = -1
		if entity.StartLine <= 0 || entity.EndLine < entity.StartLine {
			continue
		}
		for len(open) > 0 && !entityEncloses(entities[open[len(open)-1]], entity) {
			open = open[:len(open)-1]
		}
		// The nearest STRICT encloser may sit under an equal-range record, so the stack is
		// searched from the top down rather than only read at the top.
		for at := len(open) - 1; at >= 0; at-- {
			if entityStrictlyEncloses(entities[open[at]], entity) {
				parents[index] = open[at]
				break
			}
		}
		open = append(open, index)
	}
	return parents
}

// entityEncloses reports whether outer's line range covers inner's, equal ranges included. It
// decides how far the open-declaration stack unwinds, so an equal-range record stays on the
// stack and can still shelter the records nested inside it.
func entityEncloses(outer, inner Entity) bool {
	return outer.StartLine <= inner.StartLine && inner.EndLine <= outer.EndLine
}

// entityStrictlyEncloses reports whether outer covers inner AND is bigger than it.
func entityStrictlyEncloses(outer, inner Entity) bool {
	return entityEncloses(outer, inner) &&
		(outer.StartLine < inner.StartLine || inner.EndLine < outer.EndLine)
}
