package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

// `def` — a structural declaration lookup
// ======================================
//
// The question this answers is "what IS this name, and what can I do with it?".
// Before it existed the only single-symbol lookups were relation queries
// (`neighbors`, `impact`), so every consumer that wanted a declaration rolled
// its own by grepping the `symbols` stream for a NAME SUBSTRING — and a
// substring match over names is exactly how one type's members get reported as
// another's: asking for `Edit` matched `edits`, so the answer to "what does
// Edit have?" was two of Edit's fields followed by two members of Fix, with
// every associated function in `impl Edit` missing.
//
// So membership here is never inferred from spelling. A member is a member
// because the graph says so:
//
//   - a CONTAINS edge from the declaration (or a container_id pointing at it),
//     which is where inherent `impl` blocks, Go receiver methods, Swift/Kotlin
//     extensions and C# extension methods all land;
//   - a member of a supertype the declaration EXTENDS/INHERITS/IMPLEMENTS,
//     reported one hop only and labelled with where it came from (PHP traits,
//     Ruby modules, base classes);
//   - never a symbol whose name merely contains the query.
//
// Partial declarations of one type (C# `partial class`) are merged, because they
// are one type. Two unrelated types that share a name are NOT merged: they are
// listed separately, so the caller sees the ambiguity instead of a card that
// silently mixes both.

const (
	defaultDefContextBytes = 4096
	defaultDefMemberLimit  = 15
	defDeclarationLimit    = 4
	defMaxSignatureRunes   = 96
)

type defFlags struct {
	Repo    string
	Symbols []string
	Symbol  string
	// From is the line a clipped body resumes at, so the "--from N" the resume note prints is real.
	From            int
	File            string
	Line            int
	Kind            string
	Format          string
	Profile         string
	Worktree        bool
	MemberLimit     int
	MaxContextBytes int
	CacheDir        string
	DisableCache    bool
	IgnoreFile      []string
	IncludeFile     []string
}

// defMember is one member of a declaration: a field, an associated function, a
// method, or a nested type.
type defMember struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name,omitempty"`
	Kind          string `json:"kind"`
	Signature     string `json:"signature,omitempty"`
	FilePath      string `json:"file_path"`
	StartLine     int    `json:"start_line"`
	// Origin says why this symbol is a member: "declared" (in the declaration
	// itself, including its impl blocks), "extension" (declared on the type from
	// outside it), or "inherited:<Super>".
	Origin string `json:"origin"`
}

// defRelated is a type-level relation of a declaration (a supertype it declares,
// or a type that declares it as a supertype).
type defRelated struct {
	Name      string `json:"name"`
	Kind      string `json:"kind,omitempty"`
	Relation  string `json:"relation"`
	FilePath  string `json:"file_path,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
}

// defDeclaration is one declaration and everything structurally attached to it.
type defDeclaration struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name,omitempty"`
	Kind          string `json:"kind"`
	Language      string `json:"language,omitempty"`
	Signature     string `json:"signature,omitempty"`
	FilePath      string `json:"file_path"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
	// Owner is the type a method/field belongs to, so a method reports itself as
	// `Edit::deletion` rather than as a free-floating `deletion`.
	Owner *defRelated `json:"owner,omitempty"`
	// Parts lists the other declarations merged into this one (partial types).
	Parts          []defRelated `json:"parts,omitempty"`
	Fields         []defMember  `json:"fields,omitempty"`
	Methods        []defMember  `json:"methods,omitempty"`
	NestedTypes    []defMember  `json:"nested_types,omitempty"`
	Supertypes     []defRelated `json:"supertypes,omitempty"`
	Implementors   []defRelated `json:"implementors,omitempty"`
	FieldsTotal    int          `json:"fields_total"`
	MethodsTotal   int          `json:"methods_total"`
	NestedTotal    int          `json:"nested_types_total"`
	ImplementTotal int          `json:"implementors_total"`
}

type defResponse struct {
	FormatVersion int    `json:"format_version"`
	RepoRoot      string `json:"repo_root"`
	Commit        string `json:"commit,omitempty"`
	Tree          string `json:"tree,omitempty"`
	Profile       string `json:"profile,omitempty"`
	Query         string `json:"query"`
	// FuzzyMatchKind names the rung of the fuzzy ladder these declarations came from, empty on an
	// exact match. See resolveFocusSymbolsOrFuzzy: `def` answers a misspelled name rather than
	// returning nothing.
	FuzzyMatchKind   string                 `json:"fuzzy_match_kind,omitempty"`
	Declarations     []defDeclaration       `json:"declarations"`
	DeclarationTotal int                    `json:"declarations_total"`
	Truncated        bool                   `json:"truncated"`
	Warnings         []sem.ProviderWarning  `json:"warnings"`
	PartialFailures  []sem.PartialFailure   `json:"partial_failures"`
	Completeness     sem.CompletenessReport `json:"completeness"`
	IndexCacheHit    bool                   `json:"index_cache_hit"`
	IndexLatencyMS   int64                  `json:"index_latency_ms"`
	QueryLatencyMS   int64                  `json:"query_latency_ms"`
	TotalLatencyMS   int64                  `json:"total_latency_ms"`
}

func runDef(ctx context.Context, opts Options, args []string) error {
	flags, err := parseDefFlags(args)
	if err != nil {
		return err
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	profile, err := parseProfile(flags.Profile)
	if err != nil {
		return err
	}
	cacheDir := flags.CacheDir
	if cacheDir == "" {
		cacheDir = opts.Env.PluginDataDir
	}
	totalStarted := time.Now()
	snapshot, cacheHit, err := sem.LoadOrBuildProviderSnapshot(ctx, repo, opts.Version, sem.ProviderSnapshotOptions{
		NoNetwork:    true,
		Worktree:     flags.Worktree,
		IgnoreFiles:  flags.IgnoreFile,
		IncludeFiles: flags.IncludeFile,
		Profile:      profile,
	}, cacheDir, flags.DisableCache)
	if err != nil {
		return err
	}
	// index_latency_ms is the cost of HAVING the snapshot, and the snapshot is
	// loaded by the call above — so the clock is read here, before anything else
	// runs. Sampling it after the source reader was opened charged the reader's
	// `git cat-file` spawn to the index, which is the one field a caller reads to
	// decide whether the index cache is working. Source enrichment is part of
	// answering the query and is timed as such.
	indexLatency := time.Since(totalStarted)
	queryStarted := time.Now()
	// Only the source-quoting formats read source. Opening before the format
	// switch spawned a git cat-file child that `--format json` never touched.
	var readSource lineReader
	if flags.Format == "text" || flags.Format == "agent" {
		var closeSource func() error
		readSource, closeSource = openSnapshotLineReaderOrDegrade(ctx, snapshot, flags.Worktree, opts.Stderr)
		if closeSource != nil {
			defer closeSource()
		}
	}
	symbols := flags.Symbols
	if len(symbols) == 0 {
		symbols = []string{flags.Symbol}
	}
	// One index build, N answers. The whole point of the multi-query form is that the second and third
	// name cost a map lookup rather than another 10-second snapshot load.
	for position, symbol := range symbols {
		query := flags
		query.Symbol = symbol
		response := buildDefResponse(snapshot, query)
		response.IndexCacheHit = cacheHit
		response.IndexLatencyMS = indexLatency.Milliseconds()
		response.QueryLatencyMS = time.Since(queryStarted).Milliseconds()
		response.TotalLatencyMS = time.Since(totalStarted).Milliseconds()
		switch flags.Format {
		case "json":
			encoder := json.NewEncoder(termsafe.NewJSONWriter(opts.Stdout))
			encoder.SetEscapeHTML(false)
			if err := encoder.Encode(response); err != nil {
				return err
			}
		case "text", "agent":
			if position > 0 {
				// A separator, because several cards in one stream have to be tellable apart.
				fmt.Fprintln(opts.Stdout)
			}
			if len(symbols) > 1 {
				fmt.Fprintf(opts.Stdout, "== %s ==\n", symbol)
			}
			if err := writeDefText(opts.Stdout, response, flags.MaxContextBytes); err != nil {
				return err
			}
			writeDefBodiesFromReader(opts.Stdout, response, readSource, query.From)
		default:
			return fmt.Errorf("def --format must be json, text, or agent, got %q", flags.Format)
		}
	}
	return nil
}

// writeDefBodiesFromReader prints the SOURCE of each declaration the card describes, numbered,
// through the reader the command owns.
//
// The card answers "what can I do with this"; agents call `def` to answer "show me the code". Measured
// on carbon: the agent got a body it could not navigate, cut it with `head -80`, lost the line it
// needed and spent 87 turns grepping instead. The card alone was never the whole answer.
func writeDefBodiesFromReader(out io.Writer, response defResponse, read lineReader, from int) {
	if len(response.Declarations) == 0 || read == nil {
		return
	}
	records := make([]sem.SymbolRecord, 0, len(response.Declarations))
	for _, declaration := range response.Declarations {
		start := declaration.StartLine
		if from > start {
			start = from
		}
		records = append(records, sem.SymbolRecord{
			Name: defDisplayName(declaration), Kind: declaration.Kind,
			FilePath: declaration.FilePath, StartLine: start, EndLine: declaration.EndLine,
		})
	}
	writeSymbolMatchBodies(out, symbolMatchBodiesFromReader(read, records, len(records)))
}

func parseDefFlags(args []string) (defFlags, error) {
	flags := defFlags{
		Format: "text", Profile: "full", Worktree: true,
		MemberLimit: defaultDefMemberLimit, MaxContextBytes: defaultDefContextBytes,
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		value := func() (string, error) {
			index++
			if index >= len(args) {
				return "", fmt.Errorf("%s requires a value", arg)
			}
			return args[index], nil
		}
		var err error
		switch arg {
		case "--repo":
			flags.Repo, err = value()
		case "--symbol":
			flags.Symbol, err = value()
		case "--file":
			flags.File, err = value()
		case "--kind":
			flags.Kind, err = value()
		// --from N resumes a body the 400-line cap clipped. It is the invocation the resume note prints,
		// so the note is actionable rather than merely apologetic.
		case "--from":
			var raw string
			if raw, err = value(); err == nil {
				flags.From, err = strconv.Atoi(raw)
				if err != nil || flags.From < 0 {
					return flags, fmt.Errorf("def --from requires a non-negative integer, got %q", raw)
				}
			}
		case "--format":
			flags.Format, err = value()
		case "--profile":
			flags.Profile, err = value()
		case "--cache-dir":
			flags.CacheDir, err = value()
		case "--ignore-file":
			var item string
			item, err = value()
			flags.IgnoreFile = append(flags.IgnoreFile, item)
		case "--include-file":
			var item string
			item, err = value()
			flags.IncludeFile = append(flags.IncludeFile, item)
		case "--line":
			parsed, next, parseErr := searchPositiveIntFlag(args, index)
			if parseErr != nil {
				return flags, parseErr
			}
			flags.Line, index = parsed, next
		case "--members":
			parsed, next, parseErr := searchPositiveIntFlag(args, index)
			if parseErr != nil {
				return flags, parseErr
			}
			flags.MemberLimit, index = parsed, next
		case "--max-context-bytes":
			parsed, next, parseErr := searchNonNegativeIntFlag(args, index)
			if parseErr != nil {
				return flags, parseErr
			}
			flags.MaxContextBytes, index = parsed, next
		case "--no-cache":
			flags.DisableCache = true
		case "--head":
			flags.Worktree = false
		case "--worktree":
			flags.Worktree = true
		default:
			if strings.HasPrefix(arg, "-") {
				return flags, fmt.Errorf("def received unexpected argument %q", arg)
			}
			// MULTI-QUERY. Agents batch shell calls under the prompt's batching rule — laravel chained
			// three greps into one Bash call — and a tool that answers one name per invocation cannot
			// compete with that. `def A B C` returns each body in sequence.
			flags.Symbols = append(flags.Symbols, arg)
			if flags.Symbol == "" {
				flags.Symbol = arg
			}
		}
		if err != nil {
			return flags, err
		}
	}
	if strings.TrimSpace(flags.Symbol) == "" {
		return flags, errors.New("def requires a name (positionally or via --symbol)")
	}
	return flags, nil
}

// defQualifiedNameForms returns the spellings of a qualified name that should be
// treated as the same address. Callers type the separator their language uses
// (`Edit::deletion` in Rust, `Edit->deletion` in PHP, `Edit#deletion` in Ruby
// docs); the graph stores one canonical dotted form, and a lookup that only
// accepted dots forced the caller to know that.
func defQualifiedNameForms(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	forms := []string{name}
	canonical := name
	for _, separator := range []string{"::", "->", "#"} {
		canonical = strings.ReplaceAll(canonical, separator, ".")
	}
	if canonical != name {
		forms = append(forms, canonical)
	}
	return forms
}

func buildDefResponse(snapshot sem.ProviderSnapshot, flags defFlags) defResponse {
	partialFailures := snapshot.Header.PartialFailures
	if partialFailures == nil {
		partialFailures = []sem.PartialFailure{}
	}
	response := defResponse{
		FormatVersion:   1,
		RepoRoot:        snapshot.Header.RepoRoot,
		Commit:          snapshot.Header.Commit,
		Tree:            snapshot.Header.Tree,
		Profile:         snapshot.Header.Profile,
		Query:           flags.Symbol,
		Declarations:    []defDeclaration{},
		Warnings:        snapshot.Header.Warnings,
		PartialFailures: partialFailures,
		Completeness:    snapshot.Header.Completeness,
	}
	index := newDefIndex(snapshot)
	matches := index.resolve(flags)
	response.FuzzyMatchKind = index.fuzzyKind
	groups := index.groupPartials(matches)
	response.DeclarationTotal = len(groups)
	if len(groups) > defDeclarationLimit {
		groups = groups[:defDeclarationLimit]
		response.Truncated = true
	}
	limit := flags.MemberLimit
	if limit <= 0 {
		limit = defaultDefMemberLimit
	}
	for _, group := range groups {
		response.Declarations = append(response.Declarations, index.declaration(group, limit))
	}
	return response
}

// defIndex is the read-side view of one snapshot: symbols by ID, members by
// container, and the type-relation adjacency the member walk needs.
type defIndex struct {
	symbolsByID    map[string]sem.SymbolRecord
	membersByOwner map[string][]defOwnedMember
	supertypes     map[string][]defTypeEdge
	subtypes       map[string][]defTypeEdge
	// satisfiedTrait maps a member to the trait/interface whose declaration it
	// implements, read from the graph's OVERRIDES edges.
	satisfiedTrait map[string]string
	symbols        []sem.SymbolRecord
	filePaths      []string
	repoRoot       string
	// fuzzyKind names the rung of the fuzzy ladder that produced the matches, empty when the exact
	// lookup succeeded. It is set by resolve and reported so the caller knows it did not get what it
	// literally asked for.
	fuzzyKind string
}

type defOwnedMember struct {
	symbol    sem.SymbolRecord
	extension bool
}

type defTypeEdge struct {
	id       string
	name     string
	relation string
}

func newDefIndex(snapshot sem.ProviderSnapshot) *defIndex {
	index := &defIndex{
		symbolsByID:    make(map[string]sem.SymbolRecord, len(snapshot.Symbols)),
		membersByOwner: map[string][]defOwnedMember{},
		supertypes:     map[string][]defTypeEdge{},
		subtypes:       map[string][]defTypeEdge{},
		satisfiedTrait: map[string]string{},
		symbols:        snapshot.Symbols,
		filePaths:      snapshotFilePaths(snapshot),
		repoRoot:       snapshot.Header.RepoRoot,
	}
	for _, symbol := range snapshot.Symbols {
		index.symbolsByID[symbol.ID] = symbol
	}
	seen := map[string]bool{}
	addMember := func(ownerID string, symbol sem.SymbolRecord, extension bool) {
		if ownerID == "" || ownerID == symbol.ID {
			return
		}
		key := ownerID + "\x00" + symbol.ID
		if seen[key] {
			return
		}
		seen[key] = true
		index.membersByOwner[ownerID] = append(index.membersByOwner[ownerID], defOwnedMember{symbol: symbol, extension: extension})
	}
	// CONTAINS is the authoritative membership edge: it carries the joins that
	// container_id cannot express (a member acquired from another file's partial
	// declaration, an extension member declared outside the type).
	for _, relation := range snapshot.Relations {
		switch relation.Type {
		case "CONTAINS":
			member, ok := index.symbolsByID[relation.ToID]
			if !ok {
				continue
			}
			addMember(relation.FromID, member, relation.RelationScope == "extension")
		case "EXTENDS", "INHERITS", "IMPLEMENTS":
			from, ok := index.symbolsByID[relation.FromID]
			if !ok {
				continue
			}
			to, hasTo := index.symbolsByID[relation.ToID]
			name := to.Name
			if !hasTo {
				name = defExternalTypeName(relation.ToID)
			}
			if name == "" {
				continue
			}
			index.supertypes[from.ID] = append(index.supertypes[from.ID], defTypeEdge{id: relation.ToID, name: name, relation: relation.Type})
			if hasTo {
				index.subtypes[to.ID] = append(index.subtypes[to.ID], defTypeEdge{id: from.ID, name: from.Name, relation: relation.Type})
			}
		case "OVERRIDES":
			target, ok := index.symbolsByID[relation.ToID]
			if !ok {
				continue
			}
			if owner, ok := index.symbolsByID[target.ContainerID]; ok {
				index.satisfiedTrait[relation.FromID] = owner.Name
			}
		}
	}
	// container_id covers profiles that emit thin relation sets, and it is the
	// only signal for a member whose CONTAINS edge was budget-dropped.
	for _, symbol := range snapshot.Symbols {
		if symbol.ContainerID != "" {
			addMember(symbol.ContainerID, symbol, false)
		}
	}
	return index
}

// defExternalTypeName recovers the type name from an external endpoint ID
// (`external:type:Foo`), so a supertype outside the repo is still nameable.
func defExternalTypeName(id string) string {
	if index := strings.LastIndexByte(id, ':'); index >= 0 && index < len(id)-1 {
		return id[index+1:]
	}
	return ""
}

// resolve returns the declarations the query addresses. Matching is exact on the
// symbol name or its qualified name — never a substring — so a query can only
// ever return the thing it named.
func (index *defIndex) resolve(flags defFlags) []sem.SymbolRecord {
	forms := defQualifiedNameForms(flags.Symbol)
	var matches []sem.SymbolRecord
	for _, form := range forms {
		ref := parseSymbolRef(form, flags.File, flags.Line, flags.Kind, index.repoRoot, index.filePaths)
		matches = resolveFocusSymbols(index.symbols, ref)
		if len(matches) > 0 {
			break
		}
	}
	// FIX B: every spelling of the name missed, so degrade to the fuzzy ladder rather than answering
	// "(no symbol named X)". The ref is rebuilt from the caller's ORIGINAL spelling: the qualified-name
	// forms above are exact-match aids and would only narrow the fuzzy search.
	if len(matches) == 0 {
		ref := parseSymbolRef(flags.Symbol, flags.File, flags.Line, flags.Kind, index.repoRoot, index.filePaths)
		if fuzzy, tier, ok := resolveFocusSymbolsOrFuzzy(index.symbols, ref, symbolFuzzyCandidateLimit); ok {
			index.fuzzyKind = tier.label()
			matches = fuzzy
		}
	}
	// A fuzzy answer keeps the relevance order resolveFocusSymbolsOrFuzzy produced; only exact matches
	// are re-sorted positionally. See the same reasoning in buildNeighborResponse.
	if index.fuzzyKind != "" {
		return matches
	}
	sort.Slice(matches, func(left, right int) bool {
		if matches[left].FilePath != matches[right].FilePath {
			return matches[left].FilePath < matches[right].FilePath
		}
		if matches[left].StartLine != matches[right].StartLine {
			return matches[left].StartLine < matches[right].StartLine
		}
		return matches[left].ID < matches[right].ID
	})
	// A type declaration answers "what can I do with this?"; a same-named member
	// answers a narrower question. Show types first so the common query lands on
	// the useful card, without dropping the member declarations.
	sort.SliceStable(matches, func(left, right int) bool {
		return defKindRank(matches[left].Kind) < defKindRank(matches[right].Kind)
	})
	return matches
}

func defKindRank(kind string) int {
	if sem.IsTypeLikeKind(kind) {
		return 0
	}
	return 1
}

// groupPartials merges declarations that are parts of one type and keeps
// everything else separate. Merging is driven by the graph's own membership
// edges, not by the name: two declarations merge only when they are the same
// kind and qualified name in the same language AND they share a member set
// (which is what the provider's partial-type canonicalization produces).
func (index *defIndex) groupPartials(matches []sem.SymbolRecord) [][]sem.SymbolRecord {
	var groups [][]sem.SymbolRecord
	// EVERY group seated under a key, not just the latest. Matches arrive in file
	// order, so an unrelated same-named type can sort BETWEEN two parts of one
	// partial type (`a.cs` partial, `b.cs` namesake, `c.cs` partial). Remembering
	// only the newest group per key made the namesake displace the partial group,
	// and the later part was then tested against the namesake alone, failed, and
	// stayed split: three declarations where the caller should see one merged
	// partial type plus one namesake. Searching the earlier groups too costs
	// nothing (a key holds one group in the common case) and cannot over-merge,
	// because sharesMemberOwner is still the only thing that seats a part.
	byKey := map[string][]int{}
	for _, symbol := range matches {
		if !sem.IsTypeLikeKind(symbol.Kind) {
			groups = append(groups, []sem.SymbolRecord{symbol})
			continue
		}
		key := symbol.Language + "\x00" + symbol.Kind + "\x00" + symbol.QualifiedName
		seated := false
		for _, position := range byKey[key] {
			if !index.sharesMemberOwner(groups[position], symbol) {
				continue
			}
			groups[position] = append(groups[position], symbol)
			seated = true
			break
		}
		if seated {
			continue
		}
		byKey[key] = append(byKey[key], len(groups))
		groups = append(groups, []sem.SymbolRecord{symbol})
	}
	return groups
}

// sharesMemberOwner reports whether a candidate part belongs to a group already
// seated: either the candidate's own declaration says `partial` and it owns no
// members (an empty part of a partial type), or the group's anchor owns members
// declared in the candidate's file. Both are evidence of one type written in
// several declarations, and both are false of two unrelated same-named types.
//
// Owning no members is NOT evidence on its own. Every memberless same-named type
// used to be absorbed as another part, so two empty `Config` classes in two Java
// files — a language with no partial types at all — merged into one declaration
// carrying a PARTIAL part that names a file the type was never split across, and
// the ambiguity the caller needed to see disappeared. Reporting both
// declarations and letting the caller narrow is the answer that is merely
// incomplete; merging them is the answer that is wrong.
func (index *defIndex) sharesMemberOwner(group []sem.SymbolRecord, candidate sem.SymbolRecord) bool {
	if len(index.membersByOwner[candidate.ID]) == 0 {
		// BOTH SIDES must say `partial`. The candidate's own keyword says it is A part; it says
		// nothing about whether the group already seated is the type it is a part OF. C# and F#
		// require every part of a partial type to carry the keyword, so a group whose declarations
		// are all non-partial is a DIFFERENT type that merely shares the name — another namespace,
		// another assembly, another project in the same graph. Seating an empty part on it produced
		// one declaration carrying a PARTIAL part from a type it was never split across, and the
		// ambiguity the caller needed to see disappeared: exactly the failure the memberless rule
		// above was written to stop, reached from the other side.
		return declaresPartialType(candidate) && groupDeclaresPartialType(group)
	}
	for _, part := range group {
		for _, member := range index.membersByOwner[part.ID] {
			if member.symbol.FilePath == candidate.FilePath {
				return true
			}
		}
	}
	return false
}

// groupDeclaresPartialType reports whether a group already seated is one a
// memberless part can join: at least one of its declarations says `partial`.
//
// One is enough because a group grows only by this predicate or by shared
// membership, so a partial declaration anywhere in it is evidence that the group
// is the split type and not a same-named neighbour.
func groupDeclaresPartialType(group []sem.SymbolRecord) bool {
	for _, part := range group {
		if declaresPartialType(part) {
			return true
		}
	}
	return false
}

// declaresPartialType reports whether a type declaration carries the `partial`
// modifier, read from the signature the parser captured.
//
// It mirrors the provider's own partial-type test (see partialTypeCanonicalIDs
// in internal/sem): C# and F# are the languages in this grammar set that let one
// type be written as several declarations, and the keyword is what separates a
// part from a same-named type in another namespace or assembly. The two must
// stay in step — the provider decides which members a part owns, and this
// decides which declarations are parts.
func declaresPartialType(symbol sem.SymbolRecord) bool {
	if symbol.Language != "C#" && symbol.Language != "F#" {
		return false
	}
	for _, field := range strings.Fields(symbol.Signature) {
		if field == "partial" {
			return true
		}
	}
	return false
}

func (index *defIndex) declaration(group []sem.SymbolRecord, limit int) defDeclaration {
	anchor := group[0]
	// The canonical part is the one that owns the members; when a query resolves
	// to a later part, reporting the first is what makes the member set whole.
	for _, part := range group {
		if len(index.membersByOwner[part.ID]) > len(index.membersByOwner[anchor.ID]) {
			anchor = part
		}
	}
	declaration := defDeclaration{
		Name:          anchor.Name,
		QualifiedName: anchor.QualifiedName,
		Kind:          anchor.Kind,
		Language:      anchor.Language,
		Signature:     anchor.Signature,
		FilePath:      anchor.FilePath,
		StartLine:     anchor.StartLine,
		EndLine:       anchor.EndLine,
	}
	for _, part := range group {
		if part.ID == anchor.ID {
			continue
		}
		declaration.Parts = append(declaration.Parts, defRelated{
			Name: part.Name, Kind: part.Kind, Relation: "PARTIAL",
			FilePath: part.FilePath, StartLine: part.StartLine,
		})
	}
	if !sem.IsTypeLikeKind(anchor.Kind) {
		if owner, ok := index.symbolsByID[anchor.ContainerID]; ok {
			declaration.Owner = &defRelated{
				Name: owner.Name, Kind: owner.Kind, Relation: "OWNER",
				FilePath: owner.FilePath, StartLine: owner.StartLine,
			}
		}
		return declaration
	}

	var fields, methods, nested []defMember
	declared := map[string]bool{}
	// A member reachable from two parts of one partial type (its container_id
	// names the part that wrote it, the canonicalized CONTAINS edge names the
	// part that stands for the type) is ONE member.
	listed := map[string]bool{}
	for _, part := range group {
		for _, owned := range index.membersByOwner[part.ID] {
			if listed[owned.symbol.ID] {
				continue
			}
			listed[owned.symbol.ID] = true
			origin := "declared"
			switch {
			case owned.extension:
				origin = "extension"
			case index.satisfiedTrait[owned.symbol.ID] != "":
				// A trait/interface implementation is a member of the type AND of
				// the contract it satisfies. Saying which contract is what makes
				// the difference between "Edit has a range()" and "Edit is Ranged".
				origin = "impl:" + index.satisfiedTrait[owned.symbol.ID]
			}
			declared[owned.symbol.Kind+"\x00"+owned.symbol.Name] = true
			fields, methods, nested = defAppendMember(fields, methods, nested, owned.symbol, origin)
		}
	}
	// One hop of member acquisition, labelled with its source: a PHP trait, a
	// Ruby module, a base class. Not transitive — the point is the surface of
	// this type, and a transitive walk in a deep hierarchy is unbounded.
	// A member the type declares itself hides the one it would have acquired,
	// exactly as the language resolves it, so an override is listed once.
	for _, edge := range index.supertypes[anchor.ID] {
		declaration.Supertypes = append(declaration.Supertypes, index.relatedFor(edge))
		for _, owned := range index.membersByOwner[edge.id] {
			if declared[owned.symbol.Kind+"\x00"+owned.symbol.Name] || listed[owned.symbol.ID] {
				continue
			}
			listed[owned.symbol.ID] = true
			fields, methods, nested = defAppendMember(fields, methods, nested, owned.symbol, "inherited:"+edge.name)
		}
	}
	for _, edge := range index.subtypes[anchor.ID] {
		declaration.Implementors = append(declaration.Implementors, index.relatedFor(edge))
	}
	declaration.Supertypes = defDedupRelated(declaration.Supertypes)
	declaration.Implementors = defDedupRelated(declaration.Implementors)
	declaration.FieldsTotal = len(fields)
	declaration.MethodsTotal = len(methods)
	declaration.NestedTotal = len(nested)
	declaration.ImplementTotal = len(declaration.Implementors)
	declaration.Fields = defTrimMembers(defSortMembers(fields), limit)
	declaration.Methods = defTrimMembers(defSortMembers(methods), limit)
	declaration.NestedTypes = defTrimMembers(defSortMembers(nested), limit)
	if len(declaration.Implementors) > limit {
		declaration.Implementors = declaration.Implementors[:limit]
	}
	return declaration
}

func (index *defIndex) relatedFor(edge defTypeEdge) defRelated {
	related := defRelated{Name: edge.name, Relation: edge.relation}
	if symbol, ok := index.symbolsByID[edge.id]; ok {
		related.Kind = symbol.Kind
		related.FilePath = symbol.FilePath
		related.StartLine = symbol.StartLine
	}
	return related
}

func defAppendMember(fields, methods, nested []defMember, symbol sem.SymbolRecord, origin string) ([]defMember, []defMember, []defMember) {
	member := defMember{
		Name: symbol.Name, QualifiedName: symbol.QualifiedName, Kind: symbol.Kind,
		Signature: symbol.Signature, FilePath: symbol.FilePath, StartLine: symbol.StartLine,
		Origin: origin,
	}
	switch {
	case sem.IsTypeLikeKind(symbol.Kind):
		nested = append(nested, member)
	case symbol.Kind == "field" || symbol.Kind == "property" || symbol.Kind == "constant" ||
		symbol.Kind == "enum_member" || symbol.Kind == "variable":
		fields = append(fields, member)
	default:
		methods = append(methods, member)
	}
	return fields, methods, nested
}

func defSortMembers(members []defMember) []defMember {
	sort.SliceStable(members, func(left, right int) bool {
		leftOwn := strings.HasPrefix(members[left].Origin, "inherited:")
		rightOwn := strings.HasPrefix(members[right].Origin, "inherited:")
		if leftOwn != rightOwn {
			return rightOwn
		}
		if members[left].FilePath != members[right].FilePath {
			return members[left].FilePath < members[right].FilePath
		}
		return members[left].StartLine < members[right].StartLine
	})
	return members
}

func defTrimMembers(members []defMember, limit int) []defMember {
	if limit > 0 && len(members) > limit {
		return members[:limit]
	}
	return members
}

func defDedupRelated(entries []defRelated) []defRelated {
	if len(entries) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]defRelated, 0, len(entries))
	for _, entry := range entries {
		key := entry.Relation + "\x00" + entry.Name + "\x00" + entry.FilePath
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, entry)
	}
	sort.SliceStable(out, func(left, right int) bool {
		if out[left].Relation != out[right].Relation {
			return out[left].Relation < out[right].Relation
		}
		return out[left].Name < out[right].Name
	})
	return out
}

// writeDefText renders the declaration card. The budget is a ceiling: member
// lists shrink before anything structural is dropped, because the identity line
// and the owner are the two things a caller cannot reconstruct themselves.
func writeDefText(out io.Writer, response defResponse, budget int) error {
	// The card prints whole source lines through writeNumberedSource, which reads
	// them off the scanned repository unchanged. Wrapped here so the declaration
	// headers and the bodies are both covered.
	out = termsafe.NewWriter(out)
	body := renderDefText(response, defaultDefMemberLimit)
	if budget > 0 && len(body) > budget {
		for _, limit := range []int{8, 5, 3, 1, 0} {
			body = renderDefText(response, limit)
			if len(body) <= budget {
				break
			}
		}
		if len(body) > budget {
			// A hard byte cut must not split a multi-byte rune: a card ending in
			// half a character is not a smaller card, it is corrupt output.
			cut := budget
			for cut > 0 && !utf8.RuneStart(body[cut]) {
				cut--
			}
			body = body[:cut]
		}
	}
	_, err := out.Write(body)
	return err
}

func renderDefText(response defResponse, limit int) []byte {
	var buffer strings.Builder
	if len(response.Declarations) == 0 {
		writeNoFocusMatch(&buffer, response.Query, "", 0)
		return []byte(buffer.String())
	}
	// A fuzzy answer says so, once, above the cards. Silently returning a different symbol's
	// declaration than the one asked for would be worse than the empty answer this replaced.
	if response.FuzzyMatchKind != "" {
		fmt.Fprintf(&buffer, "No exact match for %q; showing the closest %d by %s match.\n",
			response.Query, len(response.Declarations), response.FuzzyMatchKind)
	}
	for index, declaration := range response.Declarations {
		if index > 0 {
			buffer.WriteByte('\n')
		}
		writeDefDeclarationText(&buffer, declaration, limit)
	}
	if omitted := response.DeclarationTotal - len(response.Declarations); omitted > 0 {
		fmt.Fprintf(&buffer, "(+%d more declaration%s named %s)\n", omitted, pluralSuffix(omitted), response.Query)
	}
	return []byte(buffer.String())
}

func writeDefDeclarationText(buffer *strings.Builder, declaration defDeclaration, limit int) {
	fmt.Fprintf(buffer, "%s:%d  %s %s\n", termsafe.Line(declaration.FilePath), declaration.StartLine,
		termsafe.Line(declaration.Kind), termsafe.Line(defDisplayName(declaration)))
	if declaration.Owner != nil {
		fmt.Fprintf(buffer, "  owner: %s %s (%s:%d)\n", termsafe.Line(declaration.Owner.Kind), termsafe.Line(declaration.Owner.Name),
			termsafe.Line(declaration.Owner.FilePath), declaration.Owner.StartLine)
	}
	if declaration.Signature != "" {
		fmt.Fprintf(buffer, "  signature: %s\n", termsafe.Line(defTruncateRunes(declaration.Signature, defMaxSignatureRunes)))
	}
	for _, part := range declaration.Parts {
		fmt.Fprintf(buffer, "  also declared: %s:%d\n", termsafe.Line(part.FilePath), part.StartLine)
	}
	if limit > 0 {
		writeDefMemberLine(buffer, "fields", declaration.Fields, declaration.FieldsTotal, limit, false)
		writeDefMemberLine(buffer, "impl "+declaration.Name, declaration.Methods, declaration.MethodsTotal, limit, true)
		writeDefMemberLine(buffer, "nested", declaration.NestedTypes, declaration.NestedTotal, limit, false)
	}
	if len(declaration.Supertypes) > 0 {
		fmt.Fprintf(buffer, "  %s\n", defRelatedLine(declaration.Supertypes, "supertypes"))
	}
	if len(declaration.Implementors) > 0 {
		line := defRelatedLine(declaration.Implementors, "implemented by")
		if omitted := declaration.ImplementTotal - len(declaration.Implementors); omitted > 0 {
			line += fmt.Sprintf(" (+%d more)", omitted)
		}
		fmt.Fprintf(buffer, "  %s\n", line)
	}
}

func defDisplayName(declaration defDeclaration) string {
	if declaration.Owner != nil {
		return declaration.Owner.Name + "::" + declaration.Name
	}
	if declaration.QualifiedName != "" && declaration.QualifiedName != declaration.Name {
		return strings.ReplaceAll(declaration.QualifiedName, ".", "::")
	}
	return declaration.Name
}

func writeDefMemberLine(buffer *strings.Builder, label string, members []defMember, total, limit int, callable bool) {
	if len(members) == 0 {
		return
	}
	shown := members
	if len(shown) > limit {
		shown = shown[:limit]
	}
	entries := make([]string, 0, len(shown))
	for _, member := range shown {
		entry := defMemberEntry(member, callable)
		if origin, ok := defOriginSuffix(member.Origin); ok {
			entry += origin
		}
		entries = append(entries, entry)
	}
	// A member list is one line of the card, and every part of it — the label,
	// which carries the owning type's name, and each entry's name, signature tail
	// and origin — comes from the scanned repository. Escaped after assembly
	// because the separators between them are this renderer's own ASCII, so one
	// pass over the finished line covers every field without a new sink appearing
	// each time an entry grows a part.
	line := termsafe.Line(fmt.Sprintf("  %s: %s", label, strings.Join(entries, ", ")))
	if omitted := total - len(shown); omitted > 0 {
		line += fmt.Sprintf(" (+%d more)", omitted)
	}
	fmt.Fprintln(buffer, line)
}

func defOriginSuffix(origin string) (string, bool) {
	switch {
	case origin == "extension":
		return " [ext]", true
	case strings.HasPrefix(origin, "impl:"):
		return " [impl " + strings.TrimPrefix(origin, "impl:") + "]", true
	case strings.HasPrefix(origin, "inherited:"):
		return " [via " + strings.TrimPrefix(origin, "inherited:") + "]", true
	}
	return "", false
}

// defMemberEntry renders one member as name plus the part of its signature that
// carries information — parameters and result for a callable, declared type for
// a field. Modifiers are dropped: they are the same on every member and are the
// bytes that make a member list unaffordable.
func defMemberEntry(member defMember, callable bool) string {
	signature := strings.TrimSpace(member.Signature)
	if signature == "" {
		if callable {
			return member.Name + "(..)"
		}
		return member.Name
	}
	if !callable {
		if declared := defFieldType(member); declared != "" {
			return member.Name + ": " + declared
		}
		return member.Name
	}
	tail, ok := sem.CallableSignatureTail(member.Name, signature)
	if !ok {
		return member.Name + "(..)"
	}
	return member.Name + defTruncateRunes(tail, defMaxSignatureRunes/2)
}

// defFieldType reads a field's declared type out of its signature. The provider
// records fields as `<name> <type>` (Go, Rust, C#, Kotlin) or with the
// language's own annotation punctuation; both reduce to "whatever is not the
// name".
func defFieldType(member defMember) string {
	signature := strings.TrimSpace(member.Signature)
	rest := strings.TrimSpace(strings.TrimPrefix(signature, member.Name))
	if rest == signature {
		return defTruncateRunes(signature, defMaxSignatureRunes/2)
	}
	rest = strings.TrimSpace(strings.TrimLeft(rest, ":="))
	return defTruncateRunes(rest, defMaxSignatureRunes/2)
}

func defRelatedLine(entries []defRelated, label string) string {
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		part := termsafe.Line(entry.Name)
		if entry.FilePath != "" && entry.StartLine > 0 {
			part += fmt.Sprintf(" (%s:%d)", termsafe.Line(entry.FilePath), entry.StartLine)
		}
		if entry.Relation != "" {
			part += " [" + strings.ToLower(entry.Relation) + "]"
		}
		parts = append(parts, part)
	}
	return label + ": " + strings.Join(parts, ", ")
}

func defTruncateRunes(value string, limit int) string {
	runes := []rune(strings.Join(strings.Fields(value), " "))
	if limit <= 0 || len(runes) <= limit {
		return string(runes)
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}
