package sem

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// The search store is the committed-tree search snapshot persisted as sqlite
// instead of one gzipped JSON document. The JSON form must be decoded in full,
// single-threaded, before the first query can run — measured at 3.1s of an 8s
// warm query on a 52k-symbol repository. The store keeps every record as a
// typed row (id, name, path and relation endpoints are real indexed columns)
// plus the record's canonical JSON, so a full load decodes rows in parallel
// and later consumers can read subsets by index without materializing the
// whole snapshot at all.
//
// Fidelity contract: loading a store must reproduce the cachedSearchSnapshot
// that built it exactly (TestSearchStoreRoundTripsSnapshot pins DeepEqual),
// so the store can substitute for the JSON entry with zero behavior change.
//
// searchStoreVersion follows the same rules as the other cache versions: bump
// it whenever the stored shape or the producer's rules change. The store is a
// derived sibling of the search snapshot entry and shares its key.
// v2 adds the relations_from/relations_to indexes with their first reader
// (the store context query's fan-out); v1 entries lack them and are rebuilt.
// v3 adds the versioned read-only projection used by Brain inspection.
// v4 fixes the file content identity projection to use the indexed Git blob.
const searchStoreVersion = "search-store-sqlite-v4"

// searchStoreEnv gates the READ path while the store rolls out: the artifact
// is written beside complete JSON entries, but it is only served when the
// operator opts in, so a rollout problem degrades to the measured JSON path
// rather than to a broken query.
const searchStoreEnv = "ENTIRE_GRAPH_SEARCH_STORE"

func searchStoreEnabled() bool {
	value, ok := os.LookupEnv(searchStoreEnv)
	if !ok {
		return false
	}
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "", "0", "false", "off", "no":
		return false
	}
	return true
}

const searchStoreSchema = `
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE files (
	path TEXT PRIMARY KEY,
	lines INTEGER NOT NULL,
	record TEXT NOT NULL
);
CREATE TABLE externals (id TEXT PRIMARY KEY, record TEXT NOT NULL);
CREATE TABLE symbols (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	qualified_name TEXT NOT NULL,
	file_path TEXT NOT NULL,
	kind TEXT NOT NULL,
	local INTEGER NOT NULL,
	bodyless INTEGER NOT NULL,
	name_line INTEGER NOT NULL,
	byte_start INTEGER,
	byte_end INTEGER,
	param_names TEXT,
	param_names_known INTEGER NOT NULL,
	sig_param_types TEXT,
	sig_return_types TEXT,
	has_signature_types INTEGER NOT NULL,
	record TEXT NOT NULL
);
CREATE TABLE relations (
	from_id TEXT NOT NULL,
	to_id TEXT NOT NULL,
	type TEXT NOT NULL,
	confidence REAL NOT NULL,
	reason TEXT NOT NULL,
	relation_scope TEXT NOT NULL,
	resolution TEXT NOT NULL,
	target_kind TEXT NOT NULL,
	evidence_dropped INTEGER NOT NULL,
	extra TEXT
);
CREATE INDEX relations_from ON relations(from_id);
CREATE INDEX relations_to ON relations(to_id);
CREATE TABLE unmerged_data_flow_edges (from_id TEXT NOT NULL, to_id TEXT NOT NULL);
-- Versioned read-only projection for consumers. These views contain no copy
-- of the graph; indexes on the underlying endpoint/id columns still apply.
CREATE VIEW brain_symbols_v1 AS SELECT id, kind, name, qualified_name, file_path,
 coalesce(json_extract(record,'$.start_line'),0) start_line,
 coalesce(json_extract(record,'$.end_line'),0) end_line,
 coalesce(json_extract(record,'$.signature'),'') signature,
 coalesce(json_extract(record,'$.language'),'') language,
 coalesce(json_extract(record,'$.stable_id_version'),'') stable_id_version FROM symbols;
CREATE VIEW brain_relations_v1 AS SELECT rowid id, from_id, to_id, type, confidence, reason,
 coalesce(json_extract(extra,'$.warning_codes'),'[]') warning_codes FROM relations;
CREATE VIEW brain_files_v1 AS SELECT path,
 coalesce(json_extract(record,'$.blob'),'') blob,
 CASE WHEN coalesce(json_extract(record,'$.blob'),'') = '' THEN ''
 ELSE 'git-blob:' || json_extract(record,'$.blob') END content_hash,
 coalesce(json_extract(record,'$.language'),'') language FROM files;
CREATE VIEW brain_reverse_relations_v1 AS SELECT to_id, from_id, type FROM relations;
CREATE VIEW brain_symbol_fts_v1 AS SELECT id,
 name || ' ' || qualified_name || ' ' || coalesce(json_extract(record,'$.signature'),'') || ' ' || file_path || ' ' || kind || ' ' || coalesce(json_extract(record,'$.language'),'') text FROM symbols;

`

// Secondary indexes land WITH their first reader, never speculatively: the
// relation endpoint indexes serve the store context query's fan-out
// (searchStoreRelationsForIDs). The symbol name/path columns stay unindexed —
// their reader scans with Go-side scoring, and a B-tree cannot serve the
// substring match it does.

// searchStoreRelationExtra carries the two composite RelationRecord fields.
// WarningCodes is deliberately NOT omitempty: production relations always
// carry a non-nil (often empty) slice, and the empty-vs-nil distinction must
// round-trip exactly. The column is NULL when both fields are nil, which is
// the common case for evidence-free relations.
type searchStoreRelationExtra struct {
	Evidence     []Evidence `json:"evidence"`
	WarningCodes []string   `json:"warning_codes"`
}

// searchStoreMeta holds the envelope fields that are not record streams. The
// header and the envelope scalars travel as JSON under single meta keys; they
// are tiny and never read selectively.
type searchStoreMeta struct {
	CacheVersion    string  `json:"cache_version"`
	ProviderVersion string  `json:"provider_version"`
	Commit          string  `json:"commit"`
	Tree            string  `json:"tree"`
	Profile         Profile `json:"profile"`
	MaxParseBytes   int     `json:"max_parse_bytes"`
	Worktree        bool    `json:"worktree,omitempty"`
	DerivedFrom     string  `json:"derived_from,omitempty"`
	RepoScope       string  `json:"repo_scope,omitempty"`
}

// buildSearchStore writes cache as a sqlite store at path, which must not
// exist (an empty file is fine — sqlite adopts it). entryDigest is the digest
// of the gz JSON entry this store mirrors; readers compare it against the
// entry on disk, so a sidecar that survived a later entry rewrite reads as
// absent rather than serving a stale snapshot. The build runs with journaling
// off: the file is a temporary the caller renames into place on success and
// removes on failure, so a crashed build can never be read as an entry.
func buildSearchStore(ctx context.Context, path, entryDigest string, cache *cachedSearchSnapshot) error {
	db, err := sql.Open("sqlite", "file:"+sqliteURLPath(path)+"?_pragma=journal_mode(OFF)&_pragma=synchronous(OFF)")
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, searchStoreSchema); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	putMeta, err := tx.PrepareContext(ctx, `INSERT INTO meta(key, value) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer putMeta.Close()
	meta := searchStoreMeta{
		CacheVersion:    cache.CacheVersion,
		ProviderVersion: cache.ProviderVersion,
		Commit:          cache.Commit,
		Tree:            cache.Tree,
		Profile:         cache.Profile,
		MaxParseBytes:   cache.MaxParseBytes,
		Worktree:        cache.Worktree,
		DerivedFrom:     cache.DerivedFrom,
		RepoScope:       cache.RepoScope,
	}
	for key, value := range map[string]any{
		"store_version": searchStoreVersion,
		"entry_digest":  entryDigest,
		"envelope":      meta,
		"header":        cache.Snapshot.Header,
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if _, err := putMeta.ExecContext(ctx, key, string(encoded)); err != nil {
			return err
		}
	}

	putFile, err := tx.PrepareContext(ctx, `INSERT INTO files(path, lines, record) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer putFile.Close()
	for index := range cache.Snapshot.Files {
		file := &cache.Snapshot.Files[index]
		record, err := json.Marshal(file)
		if err != nil {
			return err
		}
		// FileLines is keyed by the file record's ID, not its path, and only
		// holds nonzero counts (see newCachedSearchSnapshot); the column
		// mirrors that so restoreCachedSearchInternals reproduces records
		// exactly.
		if _, err := putFile.ExecContext(ctx, file.Path, cache.FileLines[file.ID], string(record)); err != nil {
			return err
		}
	}

	putExternal, err := tx.PrepareContext(ctx, `INSERT INTO externals(id, record) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer putExternal.Close()
	for index := range cache.Snapshot.Externals {
		external := &cache.Snapshot.Externals[index]
		record, err := json.Marshal(external)
		if err != nil {
			return err
		}
		if _, err := putExternal.ExecContext(ctx, external.ID, string(record)); err != nil {
			return err
		}
	}

	local := make(map[string]bool, len(cache.LocalSymbolIDs))
	for _, id := range cache.LocalSymbolIDs {
		local[id] = true
	}
	bodyless := make(map[string]bool, len(cache.BodylessSymbolIDs))
	for _, id := range cache.BodylessSymbolIDs {
		bodyless[id] = true
	}
	paramKnown := make(map[string]bool, len(cache.SymbolParameterNamesKnownIDs))
	for _, id := range cache.SymbolParameterNamesKnownIDs {
		paramKnown[id] = true
	}
	putSymbol, err := tx.PrepareContext(ctx, `INSERT INTO symbols(
		id, name, qualified_name, file_path, kind, local, bodyless, name_line,
		byte_start, byte_end, param_names, param_names_known,
		sig_param_types, sig_return_types, has_signature_types, record
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer putSymbol.Close()
	for index := range cache.Snapshot.Symbols {
		symbol := &cache.Snapshot.Symbols[index]
		record, err := json.Marshal(symbol)
		if err != nil {
			return err
		}
		var byteStart, byteEnd any
		if byteRange, ok := cache.SymbolByteRanges[symbol.ID]; ok {
			byteStart, byteEnd = byteRange.Start, byteRange.End
		}
		var paramNames any
		if names, ok := cache.SymbolParameterNames[symbol.ID]; ok {
			encoded, err := json.Marshal(names)
			if err != nil {
				return err
			}
			paramNames = string(encoded)
		}
		var sigParams, sigReturns any
		hasSignatureTypes := false
		if types, ok := cache.SymbolSignatureTypes[symbol.ID]; ok {
			sigParams, sigReturns = types.Params, types.Returns
			hasSignatureTypes = true
		}
		if _, err := putSymbol.ExecContext(ctx,
			symbol.ID, symbol.Name, symbol.QualifiedName, symbol.FilePath, symbol.Kind,
			boolToInt(local[symbol.ID]), boolToInt(bodyless[symbol.ID]), cache.SymbolNameLines[symbol.ID],
			byteStart, byteEnd, paramNames, boolToInt(paramKnown[symbol.ID]),
			sigParams, sigReturns, boolToInt(hasSignatureTypes), string(record),
		); err != nil {
			return err
		}
	}

	putRelation, err := tx.PrepareContext(ctx, `INSERT INTO relations(
		from_id, to_id, type, confidence, reason, relation_scope, resolution,
		target_kind, evidence_dropped, extra
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer putRelation.Close()
	for index := range cache.Snapshot.Relations {
		relation := &cache.Snapshot.Relations[index]
		var extra any
		if relation.Evidence != nil || relation.WarningCodes != nil {
			encoded, err := json.Marshal(searchStoreRelationExtra{
				Evidence:     relation.Evidence,
				WarningCodes: relation.WarningCodes,
			})
			if err != nil {
				return err
			}
			extra = string(encoded)
		}
		if _, err := putRelation.ExecContext(ctx,
			relation.FromID, relation.ToID, relation.Type, relation.Confidence,
			relation.Reason, relation.RelationScope, relation.Resolution,
			relation.TargetKind, relation.EvidenceDropped, extra,
		); err != nil {
			return err
		}
	}

	putUnmerged, err := tx.PrepareContext(ctx, `INSERT INTO unmerged_data_flow_edges(from_id, to_id) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer putUnmerged.Close()
	for key := range cache.Snapshot.unmergedDataFlowEdges {
		fromID, toID, _ := strings.Cut(key, "\x00")
		if _, err := putUnmerged.ExecContext(ctx, fromID, toID); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// sqliteURLPath encodes a filesystem path for a file: DSN. Query separators in
// a legitimate path would otherwise be parsed as DSN parameters.
func sqliteURLPath(path string) string {
	return (&url.URL{Path: filepath.ToSlash(path)}).EscapedPath()
}

// loadSearchStore reads a store written by buildSearchStore back into the
// cachedSearchSnapshot it was built from. wantEntryDigest, when non-empty,
// must match the digest the store was built against. Row JSON decodes on all
// cores: the monolithic JSON entry this replaces decodes single-threaded by
// nature, and the parallel row decode is where the load-time win comes from
// until consumers stop materializing the whole snapshot.
func loadSearchStore(ctx context.Context, path, wantEntryDigest string) (cachedSearchSnapshot, error) {
	db, err := sql.Open("sqlite", "file:"+sqliteURLPath(path)+"?immutable=1&mode=ro")
	if err != nil {
		return cachedSearchSnapshot{}, err
	}
	defer db.Close()

	var cache cachedSearchSnapshot
	var storeVersion string
	if err := readSearchStoreMetaKey(ctx, db, "store_version", &storeVersion); err != nil {
		return cachedSearchSnapshot{}, err
	}
	if storeVersion != searchStoreVersion {
		return cachedSearchSnapshot{}, fmt.Errorf("search store version %q, want %q", storeVersion, searchStoreVersion)
	}
	if wantEntryDigest != "" {
		var entryDigest string
		if err := readSearchStoreMetaKey(ctx, db, "entry_digest", &entryDigest); err != nil {
			return cachedSearchSnapshot{}, err
		}
		if entryDigest != wantEntryDigest {
			return cachedSearchSnapshot{}, errors.New("search store does not match the current snapshot entry")
		}
	}
	var meta searchStoreMeta
	if err := readSearchStoreMetaKey(ctx, db, "envelope", &meta); err != nil {
		return cachedSearchSnapshot{}, err
	}
	cache.CacheVersion = meta.CacheVersion
	cache.ProviderVersion = meta.ProviderVersion
	cache.Commit = meta.Commit
	cache.Tree = meta.Tree
	cache.Profile = meta.Profile
	cache.MaxParseBytes = meta.MaxParseBytes
	cache.Worktree = meta.Worktree
	cache.DerivedFrom = meta.DerivedFrom
	cache.RepoScope = meta.RepoScope
	if err := readSearchStoreMetaKey(ctx, db, "header", &cache.Snapshot.Header); err != nil {
		return cachedSearchSnapshot{}, err
	}

	if err := scanSearchStoreRows(ctx, db, `SELECT lines, record FROM files ORDER BY rowid`, func(scan rowScanner) error {
		var record string
		var lines int
		if err := scan(&lines, &record); err != nil {
			return err
		}
		var file FileRecord
		if err := json.Unmarshal([]byte(record), &file); err != nil {
			return err
		}
		cache.Snapshot.Files = append(cache.Snapshot.Files, file)
		if lines != 0 {
			if cache.FileLines == nil {
				cache.FileLines = make(map[string]int)
			}
			cache.FileLines[file.ID] = lines
		}
		return nil
	}); err != nil {
		return cachedSearchSnapshot{}, err
	}

	if err := scanSearchStoreRows(ctx, db, `SELECT record FROM externals ORDER BY rowid`, func(scan rowScanner) error {
		var record string
		if err := scan(&record); err != nil {
			return err
		}
		var external ExternalRecord
		if err := json.Unmarshal([]byte(record), &external); err != nil {
			return err
		}
		cache.Snapshot.Externals = append(cache.Snapshot.Externals, external)
		return nil
	}); err != nil {
		return cachedSearchSnapshot{}, err
	}

	type symbolRow struct {
		record            string
		local, bodyless   bool
		nameLine          int
		byteStart         sql.NullInt64
		byteEnd           sql.NullInt64
		paramNames        sql.NullString
		paramKnown        bool
		sigParams         sql.NullString
		sigReturns        sql.NullString
		hasSignatureTypes bool
	}
	var symbolRows []symbolRow
	if err := scanSearchStoreRows(ctx, db, `SELECT record, local, bodyless, name_line, byte_start, byte_end,
		param_names, param_names_known, sig_param_types, sig_return_types, has_signature_types
		FROM symbols ORDER BY rowid`, func(scan rowScanner) error {
		var row symbolRow
		if err := scan(&row.record, &row.local, &row.bodyless, &row.nameLine, &row.byteStart, &row.byteEnd,
			&row.paramNames, &row.paramKnown, &row.sigParams, &row.sigReturns, &row.hasSignatureTypes); err != nil {
			return err
		}
		symbolRows = append(symbolRows, row)
		return nil
	}); err != nil {
		return cachedSearchSnapshot{}, err
	}
	// The side maps allocate lazily, exactly like the producer in
	// newCachedSearchSnapshot: the fidelity pin compares DeepEqual against the
	// JSON round-trip, and an eagerly allocated empty map would differ from the
	// nil the producer leaves behind.
	cache.Snapshot.Symbols = make([]SymbolRecord, len(symbolRows))
	if err := decodeSearchStoreParallel(len(symbolRows), func(index int) error {
		return json.Unmarshal([]byte(symbolRows[index].record), &cache.Snapshot.Symbols[index])
	}); err != nil {
		return cachedSearchSnapshot{}, err
	}
	for index := range symbolRows {
		row := &symbolRows[index]
		id := cache.Snapshot.Symbols[index].ID
		if row.local {
			cache.LocalSymbolIDs = append(cache.LocalSymbolIDs, id)
		}
		if row.bodyless {
			cache.BodylessSymbolIDs = append(cache.BodylessSymbolIDs, id)
		}
		if row.nameLine != 0 {
			if cache.SymbolNameLines == nil {
				cache.SymbolNameLines = make(map[string]int)
			}
			cache.SymbolNameLines[id] = row.nameLine
		}
		if row.byteStart.Valid && row.byteEnd.Valid {
			if cache.SymbolByteRanges == nil {
				cache.SymbolByteRanges = make(map[string]cachedSymbolByteRange)
			}
			cache.SymbolByteRanges[id] = cachedSymbolByteRange{Start: int(row.byteStart.Int64), End: int(row.byteEnd.Int64)}
		}
		if row.paramNames.Valid {
			var names []string
			if err := json.Unmarshal([]byte(row.paramNames.String), &names); err != nil {
				return cachedSearchSnapshot{}, err
			}
			if cache.SymbolParameterNames == nil {
				cache.SymbolParameterNames = make(map[string][]string)
			}
			cache.SymbolParameterNames[id] = names
		}
		if row.paramKnown {
			cache.SymbolParameterNamesKnownIDs = append(cache.SymbolParameterNamesKnownIDs, id)
		}
		if row.hasSignatureTypes {
			if cache.SymbolSignatureTypes == nil {
				cache.SymbolSignatureTypes = make(map[string]cachedSignatureTypes)
			}
			cache.SymbolSignatureTypes[id] = cachedSignatureTypes{
				Params:  row.sigParams.String,
				Returns: row.sigReturns.String,
			}
		}
	}

	if err := scanSearchStoreRows(ctx, db, `SELECT from_id, to_id, type, confidence, reason,
		relation_scope, resolution, target_kind, evidence_dropped, extra
		FROM relations ORDER BY rowid`, func(scan rowScanner) error {
		relation := RelationRecord{RecordType: "relation"}
		var extra sql.NullString
		if err := scan(&relation.FromID, &relation.ToID, &relation.Type, &relation.Confidence,
			&relation.Reason, &relation.RelationScope, &relation.Resolution,
			&relation.TargetKind, &relation.EvidenceDropped, &extra); err != nil {
			return err
		}
		if extra.Valid {
			var composite searchStoreRelationExtra
			if err := json.Unmarshal([]byte(extra.String), &composite); err != nil {
				return err
			}
			relation.Evidence = composite.Evidence
			relation.WarningCodes = composite.WarningCodes
		}
		cache.Snapshot.Relations = append(cache.Snapshot.Relations, relation)
		return nil
	}); err != nil {
		return cachedSearchSnapshot{}, err
	}

	if err := scanSearchStoreRows(ctx, db, `SELECT from_id, to_id FROM unmerged_data_flow_edges ORDER BY rowid`, func(scan rowScanner) error {
		var fromID, toID string
		if err := scan(&fromID, &toID); err != nil {
			return err
		}
		cache.UnmergedDataFlowEdges = append(cache.UnmergedDataFlowEdges, [2]string{fromID, toID})
		return nil
	}); err != nil {
		return cachedSearchSnapshot{}, err
	}

	restoreCachedSearchInternals(&cache)
	return cache, nil
}

func readSearchStoreMetaKey(ctx context.Context, db *sql.DB, key string, value any) error {
	var encoded string
	if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("search store is missing meta key %q", key)
		}
		return err
	}
	return json.Unmarshal([]byte(encoded), value)
}

type rowScanner func(dest ...any) error

func scanSearchStoreRows(ctx context.Context, db *sql.DB, query string, each func(rowScanner) error) error {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows.Scan); err != nil {
			return err
		}
	}
	return rows.Err()
}

// decodeSearchStoreParallel runs decode(i) for every index on all cores. The
// per-row JSON decode dominates a full load, and rows are independent.
func decodeSearchStoreParallel(count int, decode func(int) error) error {
	if count == 0 {
		return nil
	}
	workers := min(count, runtime.GOMAXPROCS(0))
	var group sync.WaitGroup
	errs := make([]error, workers)
	chunk := (count + workers - 1) / workers
	for worker := range workers {
		start := worker * chunk
		end := min(start+chunk, count)
		if start >= end {
			break
		}
		group.Go(func() {
			for index := start; index < end; index++ {
				if err := decode(index); err != nil {
					errs[worker] = fmt.Errorf("search store row %d: %w", index, err)
					return
				}
			}
		})
	}
	group.Wait()
	return errors.Join(errs...)
}

// searchStoreEntryFor is the ONE place the sidecar's name is derived from its
// entry: the entry's relative path with .sqlite replacing .json.gz. Writer and
// reader both address the artifact through this, so the two spellings cannot
// drift into a silent permanent miss.
func searchStoreEntryFor(entry cacheEntry) cacheEntry {
	store := entry
	store.relative = strings.TrimSuffix(entry.relative, ".json.gz") + ".sqlite"
	return store
}

// writeSearchStoreSidecar builds the sqlite sidecar for an entry that was just
// written with entryDigest. It goes through the same confined directory walk
// every cache write uses, builds into a temporary, and renames into place. On
// ANY failure it removes both the temporary and any previous sidecar: the
// invariant readers rely on is that a sidecar present on disk was written by
// the same call that wrote the current entry (the digest check backstops the
// crash window between the two renames). A successful publish then trims the
// sidecars of the same repository and scope to the newest few
// (search_store_retention.go); the gz entries are never trimmed.
func writeSearchStoreSidecar(ctx context.Context, entry cacheEntry, entryDigest string, cache *cachedSearchSnapshot) error {
	if !searchStoreEnabled() {
		// The write is gated by the same flag as the read: with no reader, an
		// unconditional sidecar taxed every complete persist with the build
		// plus ~144MB per cache variant. Enabling the flag costs one rebuild
		// (`graph index`, or the next cold build) instead.
		return nil
	}
	directory, err := openCacheEntryDirectory(entry)
	if err != nil {
		return err
	}
	defer directory.Close()
	store := searchStoreEntryFor(entry)
	succeeded := false
	defer func() {
		if !succeeded {
			// A failed build must not leave a previous sidecar behind either:
			// it would mirror an entry that no longer exists.
			_ = directory.Remove(filepath.Base(store.relative))
		}
	}()
	temporary, temporaryName, err := createRootTemp(directory, "store")
	if err != nil {
		return err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = directory.Remove(temporaryName)
		}
	}()
	if err := temporary.Close(); err != nil {
		return err
	}
	directoryPath := filepath.Join(entry.root, filepath.Dir(entry.relative))
	temporaryPath := filepath.Join(directoryPath, temporaryName)
	if err := buildSearchStore(ctx, temporaryPath, entryDigest, cache); err != nil {
		return err
	}
	if beforeSearchStorePublish != nil {
		beforeSearchStorePublish()
	}
	// Publish and trim under one hold of the retention lock, so no eviction
	// pass in another process can list, then remove, a name this rename has
	// just replaced. Without the lock the store is still published; only the
	// eviction is skipped (see search_store_retention.go).
	retention, retain := newSearchStoreRetention(ctx, store, cache)
	var lock *completeBuildLock
	if retain {
		lock = acquireSearchStoreRetentionLock(ctx, directory)
	}
	defer lock.release()
	if err := directory.Rename(temporaryName, filepath.Base(store.relative)); err != nil {
		return err
	}
	removeTemporary = false
	succeeded = true
	if afterSearchStorePublish != nil {
		afterSearchStorePublish()
	}
	if lock != nil {
		// Best effort, like the sidecar itself: the publish has succeeded, and a
		// store that could not be trimmed now is trimmed by a later publish.
		_, _ = evictSearchStoreSidecars(ctx, directory, directoryPath, retention)
	}
	return nil
}

// repairSearchStoreSidecarForEntry builds the sidecar for an ALREADY-persisted
// entry when it is missing or does not match the entry. The explicit prebuild
// needs this: a gz entry persisted by a run without the store flag (a plain
// query, or an index before the operator opted in) would otherwise make
// `entire graph index` with the flag a cache-hit no-op that never creates the
// store its own degradation message tells the operator to build. The sidecar
// is built from the raw persisted bytes, not from the request-bound snapshot
// in memory, so it mirrors the entry exactly as the fidelity contract demands.
func repairSearchStoreSidecarForEntry(ctx context.Context, entry cacheEntry) error {
	if !searchStoreEnabled() {
		return nil
	}
	if _, ok := loadSearchStoreForEntry(ctx, entry); ok {
		return nil
	}
	digest, err := entry.digest()
	if err != nil {
		return err
	}
	raw, err := readSearchSnapshot(entry)
	if err != nil {
		return err
	}
	return writeSearchStoreSidecar(ctx, entry, digest.String(), &raw)
}

// loadSearchStoreForEntry serves the entry's snapshot from the sqlite sidecar
// when the operator opted in and the sidecar matches the entry on disk. The
// bool reports whether the store answered; every failure is a quiet miss so
// the gz entry stays authoritative.
func loadSearchStoreForEntry(ctx context.Context, entry cacheEntry) (cachedSearchSnapshot, bool) {
	if !searchStoreEnabled() {
		return cachedSearchSnapshot{}, false
	}
	store := searchStoreEntryFor(entry)
	path := filepath.Join(store.root, store.relative)
	if _, err := os.Stat(path); err != nil {
		return cachedSearchSnapshot{}, false
	}
	digest, err := entry.digest()
	if err != nil {
		return cachedSearchSnapshot{}, false
	}
	cache, err := loadSearchStore(ctx, path, digest.String())
	if err != nil {
		return cachedSearchSnapshot{}, false
	}
	return cache, true
}
