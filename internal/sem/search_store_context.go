package sem

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The store context query is the surface a downstream consumer (Entire Brain's
// brief) calls instead of maintaining its own copy of the graph: task text in,
// ranked task-relevant symbols plus their relation fan-out and test
// suggestions out, served from the sqlite sidecar with indexed reads. It is
// deliberately NOT the hybrid search pipeline — no corpus content pass, no
// snippet rendering — because the brief joins these facts with its own history
// and git signals; it needs the graph's rows, not prose.

// StoreContextSymbol is one task-relevant symbol. Signature and the line range
// come from the symbol's stored record; Score is the match strength the
// ranking used, kept so a consumer can weigh symbols against its own signals.
type StoreContextSymbol struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	QualifiedName string  `json:"qualified_name"`
	Kind          string  `json:"kind"`
	FilePath      string  `json:"file_path"`
	StartLine     int     `json:"start_line"`
	EndLine       int     `json:"end_line"`
	Signature     string  `json:"signature,omitempty"`
	Language      string  `json:"language,omitempty"`
	Score         float64 `json:"score"`
}

// StoreContextRelation is one edge touching a returned symbol.
type StoreContextRelation struct {
	FromID     string  `json:"from_id"`
	ToID       string  `json:"to_id"`
	Type       string  `json:"type"`
	Confidence float64 `json:"confidence"`
}

type SearchStoreContext struct {
	Commit    string                 `json:"commit"`
	Tree      string                 `json:"tree"`
	Symbols   []StoreContextSymbol   `json:"symbols"`
	Relations []StoreContextRelation `json:"relations"`
	// RelationsTruncated reports that the relation fan-out was cut at the
	// caller's cap: the kept rows are the highest-confidence ones, and an
	// absent relation is not evidence of no relation.
	RelationsTruncated bool                 `json:"relations_truncated,omitempty"`
	Tests              []StoreContextSymbol `json:"tests"`
}

// defaultStoreContextRelationsLimit bounds the relation fan-out of one context
// response. Observed fan-out for 12 symbols on this repository is 120-170
// rows; the cap exists for hub symbols, whose fan-out is unbounded.
const defaultStoreContextRelationsLimit = 200

// storeContextStopwords are prose function words a task sentence carries that
// say nothing about code ("add a kind filter flag to the recall command" —
// only kind/filter/flag/recall/command discriminate). Three-letter tokens stay
// allowed in general ("api", "fts", "sql" are valuable), so the generic ones
// must be named: without this, "the" scores every path containing "other".
var storeContextStopwords = map[string]struct{}{
	"the": {}, "and": {}, "for": {}, "not": {}, "with": {}, "from": {}, "into": {},
	"that": {}, "this": {}, "are": {}, "was": {}, "has": {}, "have": {}, "when": {},
	"where": {}, "what": {}, "how": {}, "why": {}, "can": {}, "all": {}, "its": {},
	"should": {}, "must": {}, "does": {}, "use": {}, "add": {}, "make": {}, "then": {},
}

// storeContextTokens reduces task text to lowercase match tokens: alphanumeric
// and underscore runs, three or more characters, stopwords dropped,
// deduplicated, capped so a pasted paragraph cannot turn the scan quadratic.
// Two-character tokens are dropped because they match half the corpus ("go",
// "db", "id") and drown the discriminative terms.
func storeContextTokens(task string) []string {
	lower := strings.ToLower(task)
	fields := strings.FieldsFunc(lower, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_')
	})
	seen := make(map[string]struct{}, len(fields))
	var tokens []string
	for _, field := range fields {
		if len(field) < 3 {
			continue
		}
		if _, ok := storeContextStopwords[field]; ok {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		tokens = append(tokens, field)
		if len(tokens) == 16 {
			break
		}
	}
	return tokens
}

// QuerySearchStoreContext answers a task-context request from the store at
// path. The symbol scan is a single pass over the typed name columns with
// Go-side scoring (52k short rows scan in tens of milliseconds); the relation
// fan-out is indexed. wantEntryDigest carries the same coherence contract as
// loadSearchStore.
func QuerySearchStoreContext(ctx context.Context, path, wantEntryDigest, task string, limit, testsLimit, relationsLimit int) (SearchStoreContext, error) {
	if limit <= 0 {
		limit = 12
	}
	if testsLimit < 0 {
		testsLimit = 0
	}
	if relationsLimit <= 0 {
		relationsLimit = defaultStoreContextRelationsLimit
	}
	tokens := storeContextTokens(task)
	if len(tokens) == 0 {
		return SearchStoreContext{}, fmt.Errorf("task %q yields no searchable tokens", task)
	}
	db, err := sql.Open("sqlite", "file:"+sqliteURLPath(path)+"?immutable=1&mode=ro")
	if err != nil {
		return SearchStoreContext{}, err
	}
	defer db.Close()

	var result SearchStoreContext
	var storeVersion string
	if err := readSearchStoreMetaKey(ctx, db, "store_version", &storeVersion); err != nil {
		return SearchStoreContext{}, err
	}
	if storeVersion != searchStoreVersion {
		// A store at another version is a stale artifact, not a caller error:
		// consumers degrade exactly as they would with no store at all.
		return SearchStoreContext{}, fmt.Errorf("%w (store version %q, want %q)", ErrSearchStoreMissing, storeVersion, searchStoreVersion)
	}
	if wantEntryDigest != "" {
		var entryDigest string
		if err := readSearchStoreMetaKey(ctx, db, "entry_digest", &entryDigest); err != nil {
			return SearchStoreContext{}, err
		}
		if entryDigest != wantEntryDigest {
			return SearchStoreContext{}, fmt.Errorf("%w (store does not match the current snapshot entry)", ErrSearchStoreMissing)
		}
	}
	var meta searchStoreMeta
	if err := readSearchStoreMetaKey(ctx, db, "envelope", &meta); err != nil {
		return SearchStoreContext{}, err
	}
	result.Commit, result.Tree = meta.Commit, meta.Tree

	type scored struct {
		symbol StoreContextSymbol
		isTest bool
	}
	var matched []scored
	if err := scanSearchStoreRows(ctx, db, `SELECT id, name, qualified_name, kind, file_path, record FROM symbols ORDER BY rowid`, func(scan rowScanner) error {
		var id, name, qualified, kind, filePath, record string
		if err := scan(&id, &name, &qualified, &kind, &filePath, &record); err != nil {
			return err
		}
		lowerName := strings.ToLower(name)
		lowerQualified := strings.ToLower(qualified)
		lowerPath := strings.ToLower(filePath)
		// Token weight scales with token length: in "render the widget name",
		// "render" and "widget" are the content words and "name" matches half
		// the corpus — a flat per-slot score let one generic exact-name hit
		// outrank a symbol matching every content word (caught by the fixture
		// pin: a `Name` field beat `RenderWidget`).
		score := 0.0
		for _, token := range tokens {
			weight := float64(len(token))
			switch {
			case lowerName == token:
				score += 4 * weight
			case strings.Contains(lowerName, token):
				score += 3 * weight
			case strings.Contains(lowerQualified, token):
				score += 2 * weight
			case strings.Contains(lowerPath, token):
				score += weight
			}
		}
		if score == 0 {
			return nil
		}
		var full SymbolRecord
		if err := json.Unmarshal([]byte(record), &full); err != nil {
			return err
		}
		matched = append(matched, scored{
			symbol: StoreContextSymbol{
				ID:            id,
				Name:          name,
				QualifiedName: qualified,
				Kind:          kind,
				FilePath:      filePath,
				StartLine:     full.StartLine,
				EndLine:       full.EndLine,
				Signature:     full.Signature,
				Language:      full.Language,
				Score:         score,
			},
			isTest: storeContextTestPath(lowerPath),
		})
		return nil
	}); err != nil {
		return SearchStoreContext{}, err
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].symbol.Score != matched[j].symbol.Score {
			return matched[i].symbol.Score > matched[j].symbol.Score
		}
		return matched[i].symbol.ID < matched[j].symbol.ID
	})
	for _, entry := range matched {
		if entry.isTest {
			if len(result.Tests) < testsLimit {
				result.Tests = append(result.Tests, entry.symbol)
			}
			continue
		}
		if len(result.Symbols) < limit {
			result.Symbols = append(result.Symbols, entry.symbol)
		}
		if len(result.Symbols) == limit && len(result.Tests) == testsLimit {
			break
		}
	}

	ids := make([]string, 0, len(result.Symbols))
	for _, symbol := range result.Symbols {
		ids = append(ids, symbol.ID)
	}
	relations, relationsTruncated, err := searchStoreRelationsForIDs(ctx, db, ids, relationsLimit)
	if err != nil {
		return SearchStoreContext{}, err
	}
	result.Relations = relations
	result.RelationsTruncated = relationsTruncated

	// A test that exercises a returned symbol belongs in the suggestions even
	// when the task text never names it: TESTS edges and test-file callers of
	// the top symbols fill remaining slots.
	if testsLimit > len(result.Tests) {
		testIDs := map[string]struct{}{}
		for _, relation := range relations {
			if relation.Type == "TESTS" {
				testIDs[relation.FromID] = struct{}{}
			}
		}
		if len(testIDs) > 0 {
			already := map[string]struct{}{}
			for _, test := range result.Tests {
				already[test.ID] = struct{}{}
			}
			extra, err := searchStoreSymbolsByIDs(ctx, db, testIDs, already, testsLimit-len(result.Tests))
			if err != nil {
				return SearchStoreContext{}, err
			}
			result.Tests = append(result.Tests, extra...)
		}
	}
	return result, nil
}

// ErrSearchStoreMissing reports that the committed tree has no store sidecar.
// A consumer treats this as "graph context unavailable" and degrades, exactly
// as it would with the plugin absent; the remedy is one indexed build with the
// store enabled.
var ErrSearchStoreMissing = fmt.Errorf(
	"search store not built for this commit: run `entire graph index --head` with %s=1", searchStoreEnv)

// LoadSearchStoreContext resolves the committed tree's complete search entry
// and answers the context query from its sqlite sidecar. It never builds: a
// missing sidecar (or one that does not match the current entry) returns
// ErrSearchStoreMissing so the caller can degrade instead of paying a parse.
func LoadSearchStoreContext(ctx context.Context, repo, providerVersion, cacheDir, task string, options ProviderSnapshotOptions, limit, testsLimit, relationsLimit int) (SearchStoreContext, error) {
	descriptor, err := DescribeSearchStore(ctx, repo, providerVersion, cacheDir, options)
	if err != nil {
		return SearchStoreContext{}, err
	}
	path, digest := descriptor.Path, descriptor.EntryDigest
	result, err := QuerySearchStoreContext(ctx, path, digest, task, limit, testsLimit, relationsLimit)
	if err != nil {
		return SearchStoreContext{}, err
	}
	return result, nil
}

func storeContextTestPath(lowerPath string) bool {
	base := lowerPath
	if index := strings.LastIndexByte(base, '/'); index >= 0 {
		base = base[index+1:]
	}
	return strings.Contains(base, "_test.") || strings.HasPrefix(base, "test_") ||
		strings.Contains(base, ".spec.") || strings.Contains(base, ".test.")
}

// searchStoreRelationsForIDs returns the relations touching one of ids,
// chunked to stay under sqlite's bind-variable ceiling. This is the read the
// relations_from/relations_to indexes exist for: O(fan-out), not O(total
// relations).
//
// The fan-out is capped at limit, highest confidence first (rowid breaks
// ties, so the order is deterministic for a given store), and the bool
// reports whether anything was cut: a hub symbol — a widely called helper, a
// ubiquitous type — can touch thousands of relations, and an uncapped list
// would make the response unbounded. Each chunk's SQL carries the same order
// and a limit+1 bound, which keeps memory bounded per chunk and is lossless:
// a relation in the global top-limit ranks within the top limit+1 of every
// chunk that matches it.
func searchStoreRelationsForIDs(ctx context.Context, db *sql.DB, ids []string, limit int) ([]StoreContextRelation, bool, error) {
	if len(ids) == 0 || limit <= 0 {
		return nil, false, nil
	}
	type rankedRelation struct {
		relation StoreContextRelation
		rowid    int64
	}
	const chunkSize = 400
	seen := map[int64]struct{}{}
	var out []rankedRelation
	for start := 0; start < len(ids); start += chunkSize {
		chunk := ids[start:min(start+chunkSize, len(ids))]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		binds := make([]any, 0, len(chunk)*2+1)
		for _, id := range chunk {
			binds = append(binds, id)
		}
		for _, id := range chunk {
			binds = append(binds, id)
		}
		binds = append(binds, limit+1)
		query := `SELECT from_id, to_id, type, confidence, rowid FROM relations WHERE from_id IN (` +
			placeholders + `) OR to_id IN (` + placeholders + `) ORDER BY confidence DESC, rowid LIMIT ?`
		rows, err := db.QueryContext(ctx, query, binds...)
		if err != nil {
			return nil, false, err
		}
		for rows.Next() {
			var ranked rankedRelation
			if err := rows.Scan(&ranked.relation.FromID, &ranked.relation.ToID,
				&ranked.relation.Type, &ranked.relation.Confidence, &ranked.rowid); err != nil {
				rows.Close()
				return nil, false, err
			}
			if _, ok := seen[ranked.rowid]; ok {
				continue
			}
			seen[ranked.rowid] = struct{}{}
			out = append(out, ranked)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, false, err
		}
		rows.Close()
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].relation.Confidence != out[j].relation.Confidence {
			return out[i].relation.Confidence > out[j].relation.Confidence
		}
		return out[i].rowid < out[j].rowid
	})
	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	relations := make([]StoreContextRelation, 0, len(out))
	for _, ranked := range out {
		relations = append(relations, ranked.relation)
	}
	return relations, truncated, nil
}

func searchStoreSymbolsByIDs(ctx context.Context, db *sql.DB, ids map[string]struct{}, exclude map[string]struct{}, limit int) ([]StoreContextSymbol, error) {
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		if _, ok := exclude[id]; ok {
			continue
		}
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	var out []StoreContextSymbol
	for _, id := range ordered {
		if len(out) == limit {
			break
		}
		var name, qualified, kind, filePath, record string
		err := db.QueryRowContext(ctx, `SELECT name, qualified_name, kind, file_path, record FROM symbols WHERE id = ?`, id).
			Scan(&name, &qualified, &kind, &filePath, &record)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		var full SymbolRecord
		if err := json.Unmarshal([]byte(record), &full); err != nil {
			return nil, err
		}
		out = append(out, StoreContextSymbol{
			ID: id, Name: name, QualifiedName: qualified, Kind: kind, FilePath: filePath,
			StartLine: full.StartLine, EndLine: full.EndLine, Signature: full.Signature, Language: full.Language,
		})
	}
	return out, nil
}

// SearchStoreDescriptor identifies an immutable Graph-owned store. Consumers
// may open it mode=ro, use only the versioned views, and must re-resolve it per
// operation. Paths are local capabilities, not portable manifest entries.
type SearchStoreDescriptor struct {
	ContractVersion int             `json:"contract_version"`
	Path            string          `json:"path"`
	EntryDigest     string          `json:"entry_digest"`
	Commit          string          `json:"commit"`
	Tree            string          `json:"tree"`
	Header          json.RawMessage `json:"header"`
	Files           int             `json:"files"`
	Symbols         int             `json:"symbols"`
	Relations       int             `json:"relations"`
	Externals       int             `json:"externals"`
}

// DescribeSearchStore never builds or scans a snapshot. It verifies the same
// cache identity as context before handing out a read-only store capability.
func DescribeSearchStore(ctx context.Context, repo, providerVersion, cacheDir string, options ProviderSnapshotOptions) (SearchStoreDescriptor, error) {
	if cacheDir == "" {
		return SearchStoreDescriptor{}, fmt.Errorf("store context requires a cache directory")
	}
	options.Worktree = false
	options.OnlyFiles = nil
	if options.Profile == "" {
		options.Profile = ProfileFull
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return SearchStoreDescriptor{}, err
	}
	options, err = CaptureProviderCachePolicy(absRepo, options)
	if err != nil {
		return SearchStoreDescriptor{}, err
	}
	_, tree, err := resolveCommittedHEAD(ctx, absRepo)
	if err != nil {
		return SearchStoreDescriptor{}, fmt.Errorf("resolve committed HEAD for store context: %w", err)
	}
	repositoryKey := repoKey(ctx, absRepo)
	key, err := searchSnapshotKey(absRepo, repositoryKey, providerVersion, tree, options, ctx)
	if err != nil {
		return SearchStoreDescriptor{}, err
	}
	entry, err := newCacheEntry(cacheDir, "search", searchSnapshotCacheVersion, key)
	if err != nil {
		return SearchStoreDescriptor{}, err
	}
	store := searchStoreEntryFor(entry)
	path := filepath.Join(store.root, store.relative)
	if _, err := os.Stat(path); err != nil {
		return SearchStoreDescriptor{}, ErrSearchStoreMissing
	}
	digest, err := entry.digest()
	if err != nil {
		return SearchStoreDescriptor{}, ErrSearchStoreMissing
	}

	db, err := sql.Open("sqlite", "file:"+sqliteURLPath(path)+"?immutable=1&mode=ro")
	if err != nil {
		return SearchStoreDescriptor{}, err
	}
	defer db.Close()
	var version, storedDigest string
	if err := readSearchStoreMetaKey(ctx, db, "store_version", &version); err != nil {
		return SearchStoreDescriptor{}, err
	}
	if err := readSearchStoreMetaKey(ctx, db, "entry_digest", &storedDigest); err != nil {
		return SearchStoreDescriptor{}, err
	}
	if version != searchStoreVersion || storedDigest != digest.String() {
		return SearchStoreDescriptor{}, ErrSearchStoreMissing
	}
	var meta searchStoreMeta
	if err := readSearchStoreMetaKey(ctx, db, "envelope", &meta); err != nil {
		return SearchStoreDescriptor{}, err
	}
	result := SearchStoreDescriptor{ContractVersion: 1, Path: path, EntryDigest: storedDigest, Commit: meta.Commit, Tree: meta.Tree}
	if err := readSearchStoreMetaKey(ctx, db, "header", &result.Header); err != nil {
		return SearchStoreDescriptor{}, err
	}
	if err := db.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM files), (SELECT count(*) FROM symbols), (SELECT count(*) FROM relations), (SELECT count(*) FROM externals)").Scan(&result.Files, &result.Symbols, &result.Relations, &result.Externals); err != nil {
		return SearchStoreDescriptor{}, err
	}
	return result, nil
}
