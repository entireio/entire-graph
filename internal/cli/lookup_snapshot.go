package cli

import (
	"context"

	"github.com/entireio/entire-graph/internal/sem"
)

// lookupFocus is what one lookup verb reads from the graph: the subgraph to
// load (docs/perf/focus-read-sets.md lists each verb's) and the verb's own
// exact resolution, which decides whether that subgraph can answer.
type lookupFocus struct {
	focus sem.SnapshotFocus
	// resolvesExactly reports whether the verb's exact resolution finds every
	// name it was asked for in snapshot. Nil means the verb never falls back
	// to a fuzzy match, so any focused snapshot answers it.
	resolvesExactly func(snapshot sem.ProviderSnapshot) bool
}

// loadLookupSnapshot loads the snapshot a lookup verb (neighbors, def, impact,
// explain) answers from, with LoadOrBuildProviderSnapshot's results. It asks
// the sidecar for the focused subgraph the verb reads and, when the sidecar
// cannot serve it, gets the full snapshot from the same call.
//
// Exact resolution matches the same symbols on a focused snapshot as on the
// full one: the focus's starting set holds every symbol an exact answer can
// name. The fuzzy ladder an exact miss falls to ranks over every symbol, so
// when resolvesExactly finds nothing in the focused snapshot it is dropped and
// the full snapshot is loaded, and the verb then runs exactly as it always has.
//
// A focused snapshot is read-only and local to the verb that asked for it. Its
// slices are partial and it carries no unmerged data-flow edges, so it must
// never reach a cache write, a preloaded full snapshot, or the selective
// derivation `query` builds from one. The verb reads it, answers, and drops it.
func loadLookupSnapshot(ctx context.Context, repo, version string, options sem.ProviderSnapshotOptions,
	cacheDir string, disableCache bool, lookup lookupFocus) (sem.ProviderSnapshot, bool, error) {
	// One hash of the gz entry serves the focused attempt and the fallback.
	ctx = sem.WithLookupDigestMemo(ctx)
	snapshot, focused, cacheHit, err := sem.LoadFocusedProviderSnapshot(ctx, repo, version, options, cacheDir, disableCache, lookup.focus)
	if err != nil || !focused || lookup.resolvesExactly == nil || lookup.resolvesExactly(snapshot) {
		return snapshot, cacheHit, err
	}
	return sem.LoadOrBuildProviderSnapshot(ctx, repo, version, options, cacheDir, disableCache)
}

// symbolResolvesExactly is the exact half of neighbors' and impact's name
// resolution (resolveFocusSymbolsOrFuzzy without the ladder).
func symbolResolvesExactly(snapshot sem.ProviderSnapshot, symbol, file string, line int, kind string) bool {
	ref := parseSymbolRef(symbol, file, line, kind, snapshot.Header.RepoRoot, snapshotFilePaths(snapshot))
	return len(resolveFocusSymbols(snapshot.Symbols, ref)) > 0
}

// impactCallTypes and impactTouchTypes are the relation families impact
// reads: the call family (impactCallRelation), and the type family
// (impactTypeRelation) with DATA_FLOWS.
var (
	impactCallTypes  = []string{"CALLS", "CONSTRUCTS", "ASYNC_CALLS"}
	impactTouchTypes = []string{"USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE", "DATA_FLOWS"}
)

// neighborLookupFocus reads one hop of the requested relation, in the
// requested directions. --depth 2 pairs the focus's own incoming and outgoing
// edges, so it needs no second hop.
func neighborLookupFocus(flags neighborFlags) lookupFocus {
	types := []string{flags.Relation}
	if flags.Relation == "CALLS" {
		types = append(types, "CONSTRUCTS")
	}
	return lookupFocus{
		focus: sem.SnapshotFocus{
			Names: []string{flags.Symbol},
			Hops:  []sem.FocusHop{{Types: types, In: flags.Direction != "out", Out: flags.Direction != "in"}},
		},
		resolvesExactly: func(snapshot sem.ProviderSnapshot) bool {
			return symbolResolvesExactly(snapshot, flags.Symbol, flags.File, flags.Line, flags.Kind)
		},
	}
}

// impactLookupFocus reads the call, type and data-flow edges touching the
// focus, then one more hop of incoming calls per extra level of --depth, plus
// the focus's container, siblings and members. Only callers are walked further:
// the callee and type hops are terminal, so a focus that calls a function with
// ten thousand callers, or a type ten thousand functions use, does not load
// their callers too.
func impactLookupFocus(flags impactFlags) lookupFocus {
	hops := []sem.FocusHop{
		{Types: impactTouchTypes, In: true, Out: true, Terminal: true},
		{Types: impactCallTypes, Out: true, Terminal: true},
		{Types: impactCallTypes, In: true},
	}
	for depth := 2; depth <= flags.Depth; depth++ {
		hops = append(hops, sem.FocusHop{Types: impactCallTypes, In: true})
	}
	return lookupFocus{
		focus: sem.SnapshotFocus{Names: []string{flags.Symbol}, Hops: hops, Kinship: true},
		resolvesExactly: func(snapshot sem.ProviderSnapshot) bool {
			return symbolResolvesExactly(snapshot, flags.Symbol, flags.File, flags.Line, flags.Kind)
		},
	}
}

// defLookupFocus reads every spelling of every name, their members,
// supertypes and subtypes, the members' OVERRIDES, and the supertypes'
// members. One snapshot answers all the names, so it is kept only when every
// one of them resolves exactly.
func defLookupFocus(flags defFlags, names []string) lookupFocus {
	var forms []string
	for _, name := range names {
		forms = append(forms, defQualifiedNameForms(name)...)
	}
	return lookupFocus{
		focus: sem.SnapshotFocus{
			Names: forms,
			Hops: []sem.FocusHop{
				{Types: []string{"CONTAINS", "EXTENDS", "INHERITS", "IMPLEMENTS"}, In: true, Out: true},
				{Types: []string{"OVERRIDES"}, Out: true},
			},
			Kinship: true,
		},
		resolvesExactly: func(snapshot sem.ProviderSnapshot) bool {
			index := &defIndex{symbols: snapshot.Symbols, filePaths: snapshotFilePaths(snapshot), repoRoot: snapshot.Header.RepoRoot}
			for _, name := range names {
				query := flags
				query.Symbol = name
				if len(index.resolveExact(query)) == 0 {
					return false
				}
			}
			return true
		},
	}
}

// explainLookupFocus reads the candidates' same-named symbols and their
// containers. Explain matches names exactly and has no fuzzy ladder, so a
// focused snapshot always answers it.
func explainLookupFocus(candidates []explainCandidate) lookupFocus {
	names := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		names = append(names, candidate.Name)
	}
	return lookupFocus{focus: sem.SnapshotFocus{Names: names, Kinship: true}}
}
