package sem

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// A lookup verb (neighbors, def, impact, explain) answers one question about a
// few symbols, yet the full load decodes the whole snapshot to ask it: on the
// Linux kernel that is close to a minute and tens of gigabytes per call. The
// focused load reads, from the sqlite sidecar, only the slice of the graph the
// verb will look at. docs/perf/focus-read-sets.md lists what each verb reads;
// the closure below is checked against it.
//
// The contract is the full snapshot restricted: every slice is the full
// snapshot's order restricted to the rows retained (rows are read by rowid,
// and the sidecar is written in snapshot order), and the header, Stats and
// Completeness are the full snapshot's. Anything that goes wrong reading the
// sidecar returns the full snapshot instead.

// SnapshotFocus describes what one lookup verb will read. Built by the verb.
type SnapshotFocus struct {
	Names []string   // every candidate spelling the verb may resolve: as typed, plus def's rewritten forms
	Hops  []FocusHop // hop i expands the frontier through these relation types and directions
	// Kinship also loads containers (by container_id), their container_id
	// children, and the members of supertypes reached through
	// EXTENDS/INHERITS/IMPLEMENTS/OVERRIDES. Precisely:
	//   - the container_id children of the starting set (its members) are
	//     loaded and join the hop-2 frontier, as if hop 1 had reached them
	//     through CONTAINS, so a member known only by container_id still has
	//     its own relations walked;
	//   - the container_id children of the starting set's containers (its
	//     siblings) are loaded, not expanded;
	//   - every symbol a retained EXTENDS/INHERITS/IMPLEMENTS/OVERRIDES
	//     relation points at gets its CONTAINS out-relations and its
	//     container_id children loaded;
	//   - last, the container of every loaded symbol is loaded, not expanded.
	Kinship bool
}

// FocusHop is one step of the walk out from the starting set.
type FocusHop struct {
	Types   []string // nil means every type
	In, Out bool
}

// kinshipSupertypeRelations are the relations whose target's members Kinship
// loads (def.go's inherited members and `impl:` owners).
var kinshipSupertypeRelations = map[string]bool{
	"EXTENDS": true, "INHERITS": true, "IMPLEMENTS": true, "OVERRIDES": true,
}

// LoadFocusedProviderSnapshot returns (snapshot, focused, cacheHit, err).
// focused=false means the full snapshot was returned (no exact sidecar, a
// positional path:line ref, or any error reading the sidecar), with the same
// contract as LoadOrBuildProviderSnapshot.
//
// A focused snapshot holds, from the exact sidecar of the committed tree's
// complete entry:
//   - the header, through the same post-load chain as a full load
//     (bindCachedSearchSnapshot), with Stats and Completeness of the whole;
//   - every file;
//   - the starting set: symbols whose id is one of focus.Names, or whose name
//     or qualified name equals one of them under strings.EqualFold;
//   - for each hop, every relation of the hop's types with the frontier on the
//     requested side, uncapped and with every field; the symbols among their
//     endpoints are the next frontier, files and externals are never expanded;
//   - the Kinship closure, when asked for;
//   - every symbol and external a retained relation names.
//
// A verb that resolves exactly (ID, then EqualFold name or qualified name,
// then --file/--kind/--line filters) gets the same matches from it as from the
// full snapshot, because the starting set is a superset of what exact
// resolution can return and filters only narrow. The fuzzy ladder ranks over
// every symbol, so a verb whose exact resolution comes back empty must reload
// with LoadOrBuildProviderSnapshot before running it.
func LoadFocusedProviderSnapshot(ctx context.Context, repo, providerVersion string,
	options ProviderSnapshotOptions, cacheDir string, disableCache bool,
	focus SnapshotFocus) (ProviderSnapshot, bool, bool, error) {
	if snapshot, ok := loadFocusedSearchSnapshot(ctx, repo, providerVersion, options, cacheDir, disableCache, focus); ok {
		return snapshot, true, true, nil
	}
	snapshot, cacheHit, err := LoadOrBuildProviderSnapshot(ctx, repo, providerVersion, options, cacheDir, disableCache)
	return snapshot, false, cacheHit, err
}

// loadFocusedSearchSnapshot addresses the complete entry exactly as the full
// load does (loadOrBuildSearchSnapshotPersisting) and reads the focus from its
// sidecar. Every failure is a quiet false: the caller then takes the full load,
// which is authoritative.
func loadFocusedSearchSnapshot(ctx context.Context, repo, providerVersion string,
	options ProviderSnapshotOptions, cacheDir string, disableCache bool,
	focus SnapshotFocus) (ProviderSnapshot, bool) {
	if disableCache || cacheDir == "" || !searchStoreEnabled() || len(options.OnlyFiles) > 0 {
		return ProviderSnapshot{}, false
	}
	names, ok := focusLookupNames(focus.Names)
	if !ok {
		return ProviderSnapshot{}, false
	}
	if options.Profile == "" {
		options.Profile = ProfileFull
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil || !worktreeSnapshotCacheable(ctx, absRepo, options) {
		return ProviderSnapshot{}, false
	}
	options, err = ensureProviderCachePolicy(absRepo, options)
	if err != nil {
		return ProviderSnapshot{}, false
	}
	commit, tree, err := resolveCommittedHEAD(ctx, absRepo)
	if err != nil {
		return ProviderSnapshot{}, false
	}
	repositoryKey := repoKey(ctx, absRepo)
	key, err := searchSnapshotKey(absRepo, repositoryKey, providerVersion, tree, options, ctx)
	if err != nil {
		return ProviderSnapshot{}, false
	}
	entry, err := newCacheEntry(cacheDir, "search", searchSnapshotCacheVersion, key)
	if err != nil {
		return ProviderSnapshot{}, false
	}
	store, db, err := openExactSearchStoreForEntry(ctx, entry, tree, func(entry cacheEntry) (cacheDigest, error) {
		return memoizedEntryDigest(ctx, entry)
	})
	if err != nil {
		return ProviderSnapshot{}, false
	}
	defer db.Close()
	// A complete entry is derived from nothing; the full load requires the
	// same of the entry it serves.
	if store.meta.DerivedFrom != "" {
		return ProviderSnapshot{}, false
	}
	cached, stored, err := readFocusedSearchStore(ctx, db, store.meta, names, focus, repositoryKey)
	if err != nil {
		return ProviderSnapshot{}, false
	}
	bound, ok := bindCachedSearchSnapshot(cached, stored, absRepo, commit, repositoryKey, providerVersion, tree, options, ctx)
	if !ok {
		return ProviderSnapshot{}, false
	}
	return bound.Snapshot, true
}

// focusLookupNames is the set of spellings the starting set is matched
// against: each name as given and trimmed (parseSymbolRef trims). It reports
// false for no names, or for any name a verb could read as a positional
// `path:line` ref. That test is parseSymbolRef's without the file-list
// lookup, so it is a superset of the refs it treats as positional; a
// positional ref resolves over every file and line, which no focus bounds.
func focusLookupNames(raw []string) ([]string, bool) {
	seen := map[string]bool{}
	var names []string
	for _, name := range raw {
		for _, spelling := range []string{name, strings.TrimSpace(name)} {
			if spelling == "" || seen[spelling] {
				continue
			}
			if positionalSymbolRefName(strings.TrimSpace(spelling)) {
				return nil, false
			}
			seen[spelling] = true
			names = append(names, spelling)
		}
	}
	return names, len(names) > 0
}

func positionalSymbolRefName(name string) bool {
	cut := strings.LastIndexByte(name, ':')
	if cut <= 0 || cut == len(name)-1 {
		return false
	}
	line, err := strconv.Atoi(name[cut+1:])
	return err == nil && line > 0
}

// foldSymbolName maps every rune to the smallest rune of its unicode.SimpleFold
// orbit, so foldSymbolName(a) == foldSymbolName(b) exactly when
// strings.EqualFold(a, b): EqualFold compares rune by rune, and two runes are
// equal under it exactly when they share an orbit. Invalid UTF-8 decodes to
// U+FFFD on both sides, as it does in EqualFold. sqlite's NOCASE folds ASCII
// only, so the fold is computed here and stored.
func foldSymbolName(name string) string {
	var folded strings.Builder
	folded.Grow(len(name))
	for _, r := range name {
		folded.WriteRune(foldSymbolRune(r))
	}
	return folded.String()
}

func foldSymbolRune(r rune) rune {
	if r < 0x80 {
		// An ASCII letter's orbit is its two cases plus, for k and s, a
		// non-ASCII rune; the uppercase letter is always the smallest.
		if 'a' <= r && r <= 'z' {
			return r - 'a' + 'A'
		}
		return r
	}
	smallest := r
	for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
		if next < smallest {
			smallest = next
		}
	}
	return smallest
}

// focusedStoreRead accumulates the rows one focused load retains, keyed so
// each row is read once however many hops reach it.
type focusedStoreRead struct {
	ctx            context.Context
	db             *sql.DB
	fileIDs        map[string]bool
	symbols        map[string]*focusedSymbolRow
	notSymbols     map[string]bool
	externals      map[string]focusedExternalRow
	notExternals   map[string]bool
	relations      map[int64]RelationRecord
	childrenLoaded map[string]bool
}

type focusedSymbolRow struct {
	rowid       int64
	id          string
	containerID string
	row         searchStoreSymbolRow
}

type focusedExternalRow struct {
	rowid    int64
	external ExternalRecord
}

// focusedStoreChunk keeps every IN list well under sqlite's bind limit.
const focusedStoreChunk = 500

// readFocusedSearchStore reads the focus's closure from an exact sidecar into
// a cachedSearchSnapshot shaped like loadSearchStore's, plus the stored
// relation counts the post-load chain needs.
func readFocusedSearchStore(ctx context.Context, db *sql.DB, meta searchStoreMeta, names []string,
	focus SnapshotFocus, repositoryKey string) (cachedSearchSnapshot, storedRelationCounts, error) {
	var cache cachedSearchSnapshot
	cache.applySearchStoreMeta(meta)
	if err := readSearchStoreMetaKey(ctx, db, "header", &cache.Snapshot.Header); err != nil {
		return cachedSearchSnapshot{}, storedRelationCounts{}, err
	}
	var typeCounts map[string]int
	if err := readSearchStoreMetaKey(ctx, db, "relation_type_counts", &typeCounts); err != nil {
		return cachedSearchSnapshot{}, storedRelationCounts{}, err
	}
	stored := storedRelationCounts{fileChangesWith: typeCounts["FILE_CHANGES_WITH"]}
	for _, count := range typeCounts {
		stored.total += count
	}
	if err := readSearchStoreFiles(ctx, db, &cache); err != nil {
		return cachedSearchSnapshot{}, storedRelationCounts{}, err
	}

	read := &focusedStoreRead{
		ctx: ctx, db: db,
		fileIDs:        make(map[string]bool, len(cache.Snapshot.Files)),
		symbols:        map[string]*focusedSymbolRow{},
		notSymbols:     map[string]bool{},
		externals:      map[string]focusedExternalRow{},
		notExternals:   map[string]bool{},
		relations:      map[int64]RelationRecord{},
		childrenLoaded: map[string]bool{},
	}
	for index := range cache.Snapshot.Files {
		read.fileIDs[cache.Snapshot.Files[index].ID] = true
	}
	if err := read.walk(storedNamespaceNames(names, cache.Snapshot.Header.RepoKey, repositoryKey), focus); err != nil {
		return cachedSearchSnapshot{}, storedRelationCounts{}, err
	}

	symbolRows := make([]*focusedSymbolRow, 0, len(read.symbols))
	for _, row := range read.symbols {
		symbolRows = append(symbolRows, row)
	}
	sort.Slice(symbolRows, func(i, j int) bool { return symbolRows[i].rowid < symbolRows[j].rowid })
	decoded := make([]searchStoreSymbolRow, len(symbolRows))
	for index, row := range symbolRows {
		decoded[index] = row.row
	}
	if err := decodeSearchStoreSymbols(&cache, decoded); err != nil {
		return cachedSearchSnapshot{}, storedRelationCounts{}, err
	}

	rowids := make([]int64, 0, len(read.relations))
	for rowid := range read.relations {
		rowids = append(rowids, rowid)
	}
	sort.Slice(rowids, func(i, j int) bool { return rowids[i] < rowids[j] })
	for _, rowid := range rowids {
		cache.Snapshot.Relations = append(cache.Snapshot.Relations, read.relations[rowid])
	}

	externals := make([]focusedExternalRow, 0, len(read.externals))
	for _, external := range read.externals {
		externals = append(externals, external)
	}
	sort.Slice(externals, func(i, j int) bool { return externals[i].rowid < externals[j].rowid })
	for _, external := range externals {
		cache.Snapshot.Externals = append(cache.Snapshot.Externals, external.external)
	}

	restoreCachedSearchInternals(&cache)
	return cache, stored, nil
}

// storedNamespaceNames adds, for a sidecar written under another local/ key,
// each name's spelling in the sidecar's ID namespace: the post-load chain
// rebinds the IDs it reads to the serving checkout's key
// (rebindCachedSearchSnapshot), so an ID the caller typed is in that key and
// must be translated back before it can match a stored id.
func storedNamespaceNames(names []string, storedKey, repositoryKey string) []string {
	if storedKey == repositoryKey || !strings.HasPrefix(storedKey, "local/") || !strings.HasPrefix(repositoryKey, "local/") {
		return names
	}
	prefix := repositoryKey + ":"
	out := append([]string(nil), names...)
	for _, name := range names {
		if strings.HasPrefix(name, prefix) {
			out = append(out, storedKey+":"+name[len(prefix):])
		}
	}
	return out
}

// walk loads the starting set, the hops and the Kinship closure, then every
// endpoint of every retained relation.
func (read *focusedStoreRead) walk(names []string, focus SnapshotFocus) error {
	folds := make([]string, 0, len(names))
	for _, name := range names {
		folds = append(folds, foldSymbolName(name))
	}
	start, err := read.symbolsByID(names)
	if err != nil {
		return err
	}
	for _, column := range []string{"name_fold", "qname_fold"} {
		matched, err := read.symbolsWhere(column, folds)
		if err != nil {
			return err
		}
		start = append(start, matched...)
	}
	start = distinctIDs(start)

	var members []string
	if focus.Kinship {
		if members, err = read.children(start); err != nil {
			return err
		}
		if _, err := read.children(read.containersOf(start)); err != nil {
			return err
		}
	}

	frontier := start
	for index, hop := range focus.Hops {
		var reached []RelationRecord
		if hop.Out {
			relations, err := read.relationsWhere("from_id", frontier, hop.Types)
			if err != nil {
				return err
			}
			reached = append(reached, relations...)
		}
		if hop.In {
			relations, err := read.relationsWhere("to_id", frontier, hop.Types)
			if err != nil {
				return err
			}
			reached = append(reached, relations...)
		}
		next, err := read.symbolsByID(relationEndpoints(reached))
		if err != nil {
			return err
		}
		if index == 0 {
			next = append(next, members...)
		}
		frontier = distinctIDs(next)
	}

	if focus.Kinship {
		var targets []string
		for _, relation := range read.relations {
			if kinshipSupertypeRelations[relation.Type] {
				targets = append(targets, relation.ToID)
			}
		}
		supertypes, err := read.symbolsByID(distinctIDs(targets))
		if err != nil {
			return err
		}
		if _, err := read.relationsWhere("from_id", supertypes, []string{"CONTAINS"}); err != nil {
			return err
		}
		if _, err := read.children(supertypes); err != nil {
			return err
		}
	}

	endpoints := make([]RelationRecord, 0, len(read.relations))
	for _, relation := range read.relations {
		endpoints = append(endpoints, relation)
	}
	ids := relationEndpoints(endpoints)
	if _, err := read.symbolsByID(ids); err != nil {
		return err
	}
	if err := read.externalsByID(ids); err != nil {
		return err
	}

	if focus.Kinship {
		loaded := make([]string, 0, len(read.symbols))
		for id := range read.symbols {
			loaded = append(loaded, id)
		}
		if _, err := read.symbolsByID(read.containersOf(loaded)); err != nil {
			return err
		}
	}
	return nil
}

// relationEndpoints lists both ends of every relation, deduplicated.
func relationEndpoints(relations []RelationRecord) []string {
	ids := make([]string, 0, 2*len(relations))
	for _, relation := range relations {
		ids = append(ids, relation.FromID, relation.ToID)
	}
	return distinctIDs(ids)
}

func distinctIDs(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := values[:0:0]
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// containersOf lists the container_id of each loaded symbol among ids.
func (read *focusedStoreRead) containersOf(ids []string) []string {
	containers := make([]string, 0, len(ids))
	for _, id := range ids {
		if row, ok := read.symbols[id]; ok && row.containerID != "" {
			containers = append(containers, row.containerID)
		}
	}
	return distinctIDs(containers)
}

const focusedSymbolColumns = `rowid, id, container_id, ` + searchStoreSymbolColumns

// symbolsByID loads the symbols among ids and returns the ids that are
// symbols. Ids already known either way are not queried again.
func (read *focusedStoreRead) symbolsByID(ids []string) ([]string, error) {
	var unknown []string
	var found []string
	for _, id := range ids {
		switch {
		case read.symbols[id] != nil:
			found = append(found, id)
		case read.notSymbols[id] || read.fileIDs[id]:
		default:
			unknown = append(unknown, id)
		}
	}
	unknown = distinctIDs(unknown)
	loaded, err := read.symbolsWhere("id", unknown)
	if err != nil {
		return nil, err
	}
	isSymbol := make(map[string]bool, len(loaded))
	for _, id := range loaded {
		isSymbol[id] = true
	}
	for _, id := range unknown {
		if !isSymbol[id] {
			read.notSymbols[id] = true
		}
	}
	return distinctIDs(append(found, loaded...)), nil
}

// children loads the container_id children of each id, once per id, and
// returns them.
func (read *focusedStoreRead) children(ids []string) ([]string, error) {
	var pending []string
	for _, id := range ids {
		if !read.childrenLoaded[id] {
			read.childrenLoaded[id] = true
			pending = append(pending, id)
		}
	}
	if _, err := read.symbolsWhere("container_id", pending); err != nil {
		return nil, err
	}
	parents := make(map[string]bool, len(ids))
	for _, id := range ids {
		parents[id] = true
	}
	var children []string
	for _, row := range read.symbols {
		if row.containerID != "" && parents[row.containerID] {
			children = append(children, row.id)
		}
	}
	return distinctIDs(children), nil
}

// symbolsWhere loads every symbol whose column is one of values and returns
// their ids.
func (read *focusedStoreRead) symbolsWhere(column string, values []string) ([]string, error) {
	var ids []string
	err := read.scanIn(`SELECT `+focusedSymbolColumns+` FROM symbols WHERE `+column+` IN (%s)`, values, nil, func(scan rowScanner) error {
		var rowid int64
		var id, containerID string
		row, err := scanSearchStoreSymbolRow(func(dest ...any) error {
			return scan(append([]any{&rowid, &id, &containerID}, dest...)...)
		})
		if err != nil {
			return err
		}
		if read.symbols[id] == nil {
			read.symbols[id] = &focusedSymbolRow{rowid: rowid, id: id, containerID: containerID, row: row}
		}
		ids = append(ids, id)
		return nil
	})
	return distinctIDs(ids), err
}

// relationsWhere retains every relation whose column (from_id or to_id) is one
// of ids and, when types is non-nil, whose type is one of them. It returns the
// relations matched by this call, whether or not an earlier call retained
// them too.
func (read *focusedStoreRead) relationsWhere(column string, ids, types []string) ([]RelationRecord, error) {
	if types != nil && len(types) == 0 {
		return nil, nil
	}
	query := `SELECT rowid, ` + searchStoreRelationColumns + ` FROM relations WHERE ` + column + ` IN (%s)`
	var typeBinds []any
	if types != nil {
		query += ` AND type IN (` + strings.TrimSuffix(strings.Repeat("?,", len(types)), ",") + `)`
		for _, kind := range types {
			typeBinds = append(typeBinds, kind)
		}
	}
	var matched []RelationRecord
	err := read.scanIn(query, ids, typeBinds, func(scan rowScanner) error {
		var rowid int64
		relation, err := scanSearchStoreRelation(func(dest ...any) error {
			return scan(append([]any{&rowid}, dest...)...)
		})
		if err != nil {
			return err
		}
		read.relations[rowid] = relation
		matched = append(matched, relation)
		return nil
	})
	return matched, err
}

// externalsByID loads the externals among ids that are not files or symbols.
func (read *focusedStoreRead) externalsByID(ids []string) error {
	var unknown []string
	for _, id := range ids {
		if read.fileIDs[id] || read.symbols[id] != nil || read.notExternals[id] {
			continue
		}
		if _, ok := read.externals[id]; ok {
			continue
		}
		unknown = append(unknown, id)
	}
	found := map[string]bool{}
	err := read.scanIn(`SELECT rowid, id, `+searchStoreExternalColumns+` FROM externals WHERE id IN (%s)`, unknown, nil, func(scan rowScanner) error {
		var rowid int64
		var id string
		external, err := scanSearchStoreExternal(func(dest ...any) error {
			return scan(append([]any{&rowid, &id}, dest...)...)
		})
		if err != nil {
			return err
		}
		read.externals[id] = focusedExternalRow{rowid: rowid, external: external}
		found[id] = true
		return nil
	})
	for _, id := range unknown {
		if !found[id] {
			read.notExternals[id] = true
		}
	}
	return err
}

// scanIn runs query once per chunk of values, with the chunk bound to its
// `IN (%s)` placeholder list and extra binds after it.
func (read *focusedStoreRead) scanIn(query string, values []string, extra []any, each func(rowScanner) error) error {
	for start := 0; start < len(values); start += focusedStoreChunk {
		chunk := values[start:min(start+focusedStoreChunk, len(values))]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		binds := make([]any, 0, len(chunk)+len(extra))
		for _, value := range chunk {
			binds = append(binds, value)
		}
		binds = append(binds, extra...)
		rows, err := read.db.QueryContext(read.ctx, strings.Replace(query, "%s", placeholders, 1), binds...)
		if err != nil {
			return err
		}
		for rows.Next() {
			if err := each(rows.Scan); err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
