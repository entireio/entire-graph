package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

const (
	defaultNeighborLimit        = 20
	defaultNeighborContextBytes = 16 * 1024
)

type neighborFlags struct {
	Repo            string
	Symbol          string
	File            string
	Line            int
	Kind            string
	Format          string
	Profile         string
	Relation        string
	Direction       string
	Depth           int
	Limit           int
	Worktree        bool
	IgnoreFile      []string
	IncludeFile     []string
	CacheDir        string
	DisableCache    bool
	InternalOnly    bool
	ExcludeTests    bool
	MaxContextBytes int
}

type neighborEndpoint struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name,omitempty"`
	Kind          string `json:"kind,omitempty"`
	FilePath      string `json:"file_path,omitempty"`
	StartLine     int    `json:"start_line,omitempty"`
	// EndLine closes the definition span. Reported so one answer can state both
	// the definition line and the span it covers, instead of leaving a reader to
	// reconcile a bare line number here against a line range from `search`.
	EndLine  int    `json:"end_line,omitempty"`
	Language string `json:"language,omitempty"`
	External bool   `json:"external,omitempty"`
}

type neighborEdge struct {
	Direction  string           `json:"direction"`
	Relation   string           `json:"relation"`
	Endpoint   neighborEndpoint `json:"endpoint"`
	Confidence float64          `json:"confidence"`
	Resolution string           `json:"resolution,omitempty"`
	Reason     string           `json:"reason,omitempty"`
	Evidence   []sem.Evidence   `json:"evidence,omitempty"`
	// CallSite is where the call is actually written, resolved from the caller's
	// body. Nil when it could not be resolved, in which case the endpoint's
	// definition line is all this answer knows.
	CallSite *callSite `json:"call_site,omitempty"`
}

type neighborPath struct {
	Caller neighborEndpoint `json:"caller"`
	Focus  neighborEndpoint `json:"focus"`
	Callee neighborEndpoint `json:"callee"`
}

type neighborFocus struct {
	Symbol   neighborEndpoint `json:"symbol"`
	Incoming []neighborEdge   `json:"incoming"`
	Outgoing []neighborEdge   `json:"outgoing"`
	Paths    []neighborPath   `json:"paths,omitempty"`
}

type neighborResponse struct {
	FormatVersion int    `json:"format_version"`
	RepoRoot      string `json:"repo_root"`
	Commit        string `json:"commit,omitempty"`
	Tree          string `json:"tree,omitempty"`
	Profile       string `json:"profile"`
	Relation      string `json:"relation"`
	// Direction echoes which half of the adjacency was asked for. Without it a
	// reader cannot tell an empty section from a section that was never queried.
	Direction     string `json:"direction"`
	Query         string `json:"query"`
	File          string `json:"file,omitempty"`
	Line          int    `json:"line,omitempty"`
	IndexCacheHit bool   `json:"index_cache_hit"`
	// IndexCacheDisabled records that this run had nowhere to store the index it
	// built, so the next relation query pays the same cost again. Relation verbs
	// index the WHOLE repository (search only parses its candidate files), which
	// makes that difference tens of seconds on a large repo — a cost the CLI
	// surface otherwise never states.
	IndexCacheDisabled bool  `json:"index_cache_disabled,omitempty"`
	IndexLatencyMS     int64 `json:"index_latency_ms"`
	QueryLatencyMS     int64 `json:"query_latency_ms"`
	TotalLatencyMS     int64 `json:"total_latency_ms"`
	Truncated          bool  `json:"truncated"`
	FocusMatchesTotal  int   `json:"focus_matches_total"`
	// FuzzyMatch reports that no EXACT match existed and these candidates came from the fuzzy ladder.
	// FuzzyMatchKind names the rung, so a consumer is never misled about what it asked for.
	FuzzyMatch     bool   `json:"fuzzy_match,omitempty"`
	FuzzyMatchKind string `json:"fuzzy_match_kind,omitempty"`
	// MatchBodies carries source for the first few matched definitions, so an ambiguous or fuzzy
	// answer removes the follow-up read instead of prescribing one.
	MatchBodies            []symbolMatchBody      `json:"match_bodies,omitempty"`
	FocusMatchesTruncated  bool                   `json:"focus_matches_truncated"`
	DisambiguationRequired bool                   `json:"disambiguation_required"`
	Matches                []neighborFocus        `json:"matches"`
	Warnings               []sem.ProviderWarning  `json:"warnings,omitempty"`
	PartialFailures        []sem.PartialFailure   `json:"partial_failures"`
	Stats                  sem.ProviderStats      `json:"stats"`
	Completeness           sem.CompletenessReport `json:"completeness"`
	// CompletenessScope is the query-relative reading of the diagnostics above.
	// The full lists stay untouched; this says which of them can matter here.
	CompletenessScope completenessScope `json:"completeness_scope"`

	// endpointTruncated distinguishes a bounded neighbor list from the JSON-only
	// explicit path expansion. Agent output can express the full Cartesian path
	// family compactly, so path expansion alone is not a truncation for agents.
	endpointTruncated bool
}

func runNeighbors(ctx context.Context, opts Options, args []string) error {
	flags, err := parseNeighborFlags(args)
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
	cacheDir := resolveCacheDir(flags.CacheDir, opts.Env.PluginDataDir)
	totalStarted := time.Now()
	indexStarted := totalStarted
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
	readSource, closeSource := openSnapshotLineReaderOrDegrade(ctx, snapshot, flags.Worktree, opts.Stderr)
	if closeSource != nil {
		defer closeSource()
	}
	indexLatency := time.Since(indexStarted)
	queryStarted := time.Now()
	response := buildNeighborResponseFromReader(snapshot, flags, readSource)
	// Call-site resolution reads the caller's source from the same snapshot-bound
	// reader as fuzzy and ambiguous bodies. It runs after the graph query and is
	// bounded by the answer's size.
	annotateNeighborCallSites(&response, readSource)
	queryLatency := time.Since(queryStarted)
	response.IndexCacheHit = cacheHit
	response.IndexCacheDisabled = cacheDir == "" || flags.DisableCache
	response.IndexLatencyMS = indexLatency.Milliseconds()
	response.QueryLatencyMS = queryLatency.Milliseconds()
	response.TotalLatencyMS = time.Since(totalStarted).Milliseconds()
	switch flags.Format {
	case "json":
		encoder := json.NewEncoder(termsafe.NewJSONWriter(opts.Stdout))
		encoder.SetEscapeHTML(false)
		return encoder.Encode(response)
	case "agent":
		return writeAgentNeighborsBounded(opts.Stdout, response, flags.MaxContextBytes)
	case "text":
		return writeAgentNeighbors(opts.Stdout, response)
	default:
		return fmt.Errorf("neighbors --format must be json, text, or agent, got %q", flags.Format)
	}
}

func parseNeighborFlags(args []string) (neighborFlags, error) {
	flags := neighborFlags{
		Format: "json", Profile: "full", Relation: "CALLS", Direction: "both",
		Depth: 1, Limit: defaultNeighborLimit, Worktree: true,
		MaxContextBytes: defaultNeighborContextBytes,
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
		switch arg {
		case "--repo":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.Repo = item
		case "--symbol":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.Symbol = item
		case "--file":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.File = item
		case "--line":
			parsed, next, parseErr := searchPositiveIntFlag(args, index)
			if parseErr != nil {
				return flags, parseErr
			}
			flags.Line, index = parsed, next
		case "--kind":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.Kind = item
		case "--format":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.Format = item
		case "--profile":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.Profile = item
		case "--relation":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.Relation = strings.ToUpper(item)
		case "--direction":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.Direction = item
		case "--depth":
			parsed, next, parseErr := searchPositiveIntFlag(args, index)
			if parseErr != nil {
				return flags, parseErr
			}
			flags.Depth, index = parsed, next
		case "--limit":
			parsed, next, parseErr := searchPositiveIntFlag(args, index)
			if parseErr != nil {
				return flags, parseErr
			}
			flags.Limit, index = parsed, next
		case "--max-context-bytes":
			// 0 disables the cap (unbounded), matching `graph search`.
			parsed, next, parseErr := searchNonNegativeIntFlag(args, index)
			if parseErr != nil {
				return flags, parseErr
			}
			flags.MaxContextBytes, index = parsed, next
		case "--head":
			flags.Worktree = false
		case "--worktree":
			flags.Worktree = true
		case "--ignore-file":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.IgnoreFile = append(flags.IgnoreFile, item)
		case "--include-file":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.IncludeFile = append(flags.IncludeFile, item)
		case "--cache-dir":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.CacheDir = item
		case "--no-cache":
			flags.DisableCache = true
		case "--internal-only":
			flags.InternalOnly = true
		case "--exclude-tests":
			flags.ExcludeTests = true
		default:
			return flags, fmt.Errorf("neighbors received unexpected argument %q", arg)
		}
	}
	if strings.TrimSpace(flags.Symbol) == "" {
		return flags, errors.New("neighbors requires --symbol")
	}
	if flags.Depth != 1 && flags.Depth != 2 {
		return flags, errors.New("neighbors --depth must be 1 or 2")
	}
	if flags.Direction != "both" && flags.Direction != "in" && flags.Direction != "out" {
		return flags, errors.New("neighbors --direction must be both, in, or out")
	}
	if flags.Relation == "" {
		return flags, errors.New("neighbors --relation cannot be empty")
	}
	return flags, nil
}

func buildNeighborResponseFromReader(
	snapshot sem.ProviderSnapshot,
	flags neighborFlags,
	readSource lineReader,
) neighborResponse {
	endpoints := make(map[string]neighborEndpoint, len(snapshot.Symbols)+len(snapshot.Externals))
	for _, file := range snapshot.Files {
		endpoints[file.ID] = endpointForFile(file)
	}
	for _, symbol := range snapshot.Symbols {
		endpoints[symbol.ID] = endpointForSymbol(symbol)
	}
	for _, external := range snapshot.Externals {
		endpoints[external.ID] = endpointForExternal(external)
	}
	ref := parseSymbolRef(flags.Symbol, flags.File, flags.Line, flags.Kind, snapshot.Header.RepoRoot, snapshotFilePaths(snapshot))
	// FIX B: an exact miss degrades to the fuzzy ladder rather than returning nothing. See
	// resolveFocusSymbolsOrFuzzy for the measured cost of the empty answer.
	focuses, matchTier, fuzzyMatch := resolveFocusSymbolsOrFuzzy(
		snapshot.Symbols, ref, symbolFuzzyCandidateLimit,
	)
	// File/line order is right for EXACT matches — several definitions of one name are equally valid
	// answers, so a stable positional order is the honest presentation. It is wrong for a FUZZY answer,
	// where the order IS the answer: resolveFocusSymbolsOrFuzzy already sorted by how well each
	// candidate matched, and re-sorting alphabetically buried the correct
	// `Functions.flattenSingleValue` under a `Single` class that merely shares one token.
	if !fuzzyMatch {
		sort.Slice(focuses, func(left, right int) bool {
			if focuses[left].FilePath != focuses[right].FilePath {
				return focuses[left].FilePath < focuses[right].FilePath
			}
			if focuses[left].StartLine != focuses[right].StartLine {
				return focuses[left].StartLine < focuses[right].StartLine
			}
			return focuses[left].ID < focuses[right].ID
		})
	}
	focusMatchesTotal := len(focuses)
	matchBodies := []symbolMatchBody(nil)
	if fuzzyMatch || focusMatchesTotal > 1 {
		matchBodies = symbolMatchBodiesFromReader(readSource, focuses, symbolAmbiguousBodyLimit)
	}
	focusMatchesTruncated := focusMatchesTotal > flags.Limit
	if focusMatchesTruncated {
		focuses = focuses[:flags.Limit]
	}
	partialFailures := snapshot.Header.PartialFailures
	if partialFailures == nil {
		partialFailures = []sem.PartialFailure{}
	}
	response := neighborResponse{
		FormatVersion:         1,
		FuzzyMatch:            fuzzyMatch,
		FuzzyMatchKind:        fuzzyKindLabel(fuzzyMatch, matchTier),
		RepoRoot:              snapshot.Header.RepoRoot,
		Commit:                snapshot.Header.Commit,
		Tree:                  snapshot.Header.Tree,
		Profile:               snapshot.Header.Profile,
		Relation:              flags.Relation,
		Direction:             flags.Direction,
		Query:                 flags.Symbol,
		File:                  ref.File,
		Line:                  ref.Line,
		Truncated:             focusMatchesTruncated,
		FocusMatchesTotal:     focusMatchesTotal,
		FocusMatchesTruncated: focusMatchesTruncated,
		MatchBodies:           matchBodies,
		Matches:               make([]neighborFocus, 0, len(focuses)),
		Warnings:              snapshot.Header.Warnings,
		PartialFailures:       partialFailures,
		Stats:                 snapshot.Header.Stats,
		Completeness:          snapshot.Header.Completeness,
		CompletenessScope:     buildCompletenessScope(snapshot, focusQueryLanguage(focuses)),
	}
	if focusMatchesTotal > 1 {
		response.DisambiguationRequired = true
		for _, focus := range focuses {
			response.Matches = append(response.Matches, neighborFocus{
				Symbol:   endpointForSymbol(focus),
				Incoming: []neighborEdge{},
				Outgoing: []neighborEdge{},
			})
		}
		return response
	}

	// Index the requested adjacency once. Keep only the best deterministic
	// --limit edges while scanning so a high-degree symbol does not accumulate
	// and sort every incident relation before truncation.
	focusIDs := make(map[string]bool, len(focuses))
	for _, focus := range focuses {
		focusIDs[focus.ID] = true
	}
	incomingByFocus := make(map[string][]neighborEdge, len(focuses))
	outgoingByFocus := make(map[string][]neighborEdge, len(focuses))
	incomingTotals := make(map[string]int, len(focuses))
	outgoingTotals := make(map[string]int, len(focuses))
	for _, relation := range snapshot.Relations {
		if !neighborRelationMatches(flags.Relation, relation.Type) {
			continue
		}
		if flags.Direction != "out" && focusIDs[relation.ToID] {
			if endpoint, ok := endpoints[relation.FromID]; ok && neighborEndpointAllowed(endpoint, flags) {
				incomingTotals[relation.ToID]++
				incomingByFocus[relation.ToID] = appendBoundedNeighborEdge(
					incomingByFocus[relation.ToID], edgeForRelation("in", endpoint, relation), flags.Limit,
				)
			}
		}
		if flags.Direction != "in" && focusIDs[relation.FromID] {
			if endpoint, ok := endpoints[relation.ToID]; ok && neighborEndpointAllowed(endpoint, flags) {
				outgoingTotals[relation.FromID]++
				outgoingByFocus[relation.FromID] = appendBoundedNeighborEdge(
					outgoingByFocus[relation.FromID], edgeForRelation("out", endpoint, relation), flags.Limit,
				)
			}
		}
	}
	for _, focus := range focuses {
		entry := neighborFocus{
			Symbol:   endpointForSymbol(focus),
			Incoming: incomingByFocus[focus.ID],
			Outgoing: outgoingByFocus[focus.ID],
		}
		if entry.Incoming == nil {
			entry.Incoming = []neighborEdge{}
		}
		if entry.Outgoing == nil {
			entry.Outgoing = []neighborEdge{}
		}
		sortNeighborEdges(entry.Incoming)
		sortNeighborEdges(entry.Outgoing)
		if incomingTotals[focus.ID] > len(entry.Incoming) {
			response.Truncated = true
			response.endpointTruncated = true
		}
		if outgoingTotals[focus.ID] > len(entry.Outgoing) {
			response.Truncated = true
			response.endpointTruncated = true
		}
		if flags.Depth == 2 && flags.Direction == "both" {
		paths:
			for _, incoming := range entry.Incoming {
				for _, outgoing := range entry.Outgoing {
					if len(entry.Paths) >= flags.Limit {
						response.Truncated = true
						break paths
					}
					entry.Paths = append(entry.Paths, neighborPath{
						Caller: incoming.Endpoint,
						Focus:  entry.Symbol,
						Callee: outgoing.Endpoint,
					})
				}
			}
		}
		response.Matches = append(response.Matches, entry)
	}
	return response
}

func endpointForSymbol(symbol sem.SymbolRecord) neighborEndpoint {
	return neighborEndpoint{
		ID: symbol.ID, Name: symbol.Name, QualifiedName: symbol.QualifiedName,
		Kind: symbol.Kind, FilePath: symbol.FilePath, StartLine: symbol.StartLine,
		EndLine: symbol.EndLine, Language: symbol.Language,
	}
}

func endpointForFile(file sem.FileRecord) neighborEndpoint {
	return neighborEndpoint{
		ID: file.ID, Name: filepath.Base(file.Path), Kind: "file", FilePath: file.Path,
		Language: file.Language,
	}
}

// focusQueryLanguage is the language a relation answer is about: the language of
// the focus symbol. It scopes the coverage report, because relations in this
// graph do not cross language boundaries, so a failure in another language
// cannot have removed a fact this answer needed.
func focusQueryLanguage(focuses []sem.SymbolRecord) string {
	if len(focuses) != 1 {
		return ""
	}
	return focuses[0].Language
}

// annotateNeighborCallSites resolves, for every incoming call edge, the line the
// call is actually written on plus the conditions in force there. Outgoing edges
// are deliberately left alone: their endpoint's definition line IS the useful
// location, and the call site is inside the focus symbol the reader already has.
func annotateNeighborCallSites(response *neighborResponse, read lineReader) {
	if response == nil || read == nil {
		return
	}
	for matchIndex := range response.Matches {
		// The callee as written at the call site is normally in the evidence
		// detail; when it is absent, the focus symbol's own name is what a
		// caller must have written, not the raw --symbol argument (which may be
		// a `<file>:<line>` selector).
		focusName := response.Matches[matchIndex].Symbol.Name
		if focusName == "" {
			focusName = response.Query
		}
		for edgeIndex := range response.Matches[matchIndex].Incoming {
			edge := &response.Matches[matchIndex].Incoming[edgeIndex]
			if !isCallRelation(edge.Relation) {
				continue
			}
			filePath, start, end, detail, ok := callEvidenceSpan(edge.Evidence)
			if !ok {
				continue
			}
			if filePath == "" {
				filePath = edge.Endpoint.FilePath
			}
			token := callTokenFromDetail(detail, focusName)
			if site, resolved := resolveCallSite(read, filePath, token, start, end); resolved {
				edge.CallSite = &site
			}
		}
	}
}

// isCallRelation reports whether a relation type is a call, so only call edges
// get a call site. The set mirrors neighborRelationMatches' call family.
func isCallRelation(relationType string) bool {
	switch relationType {
	case "CALLS", "CONSTRUCTS", "ASYNC_CALLS":
		return true
	default:
		return false
	}
}

// The --file canonicalizer that used to live here is now normalizeSymbolRefFile in
// symbolref.go: `def`, `neighbors` and `impact` all resolve one definition through the same
// symbolRef, so the path spellings a caller might type have to normalize identically for all
// three rather than only for the verb that grew the fix.

func neighborEndpointAllowed(endpoint neighborEndpoint, flags neighborFlags) bool {
	if flags.InternalOnly && endpoint.External {
		return false
	}
	return !flags.ExcludeTests || !isConventionalTestPath(endpoint.FilePath)
}

func isConventionalTestPath(path string) bool {
	clean := strings.Trim(filepath.ToSlash(filepath.Clean(path)), "/")
	if clean == "" || clean == "." {
		return false
	}
	parts := strings.Split(clean, "/")
	for _, part := range parts[:len(parts)-1] {
		switch strings.ToLower(part) {
		case "test", "tests", "__tests__", "spec", "specs", "testdata":
			return true
		}
	}

	base := parts[len(parts)-1]
	lowerBase := strings.ToLower(base)
	if strings.Contains(lowerBase, ".test.") || strings.Contains(lowerBase, ".spec.") {
		return true
	}
	if strings.HasSuffix(lowerBase, "_test.go") ||
		strings.HasSuffix(lowerBase, "_test.py") ||
		strings.HasPrefix(lowerBase, "test_") && strings.HasSuffix(lowerBase, ".py") ||
		strings.HasSuffix(lowerBase, "_spec.rb") ||
		lowerBase == "test.rs" || lowerBase == "tests.rs" {
		return true
	}

	extension := filepath.Ext(base)
	stem := strings.TrimSuffix(base, extension)
	return strings.HasSuffix(stem, "Test") ||
		strings.HasSuffix(stem, "Tests") ||
		strings.HasSuffix(stem, "TestCase")
}

func neighborRelationMatches(requested, actual string) bool {
	// Constructors are callable dependencies. The provider schema keeps
	// CONSTRUCTS distinct, while the focused call-neighborhood view includes
	// them so "callees" does not silently omit direct constructor invocations.
	return actual == requested || (requested == "CALLS" && actual == "CONSTRUCTS")
}

func endpointForExternal(external sem.ExternalRecord) neighborEndpoint {
	name := external.Value
	if index := strings.LastIndexAny(name, ".:/#"); index >= 0 && index+1 < len(name) {
		name = name[index+1:]
	}
	return neighborEndpoint{
		ID: external.ID, Name: name, QualifiedName: external.Value, Kind: external.Kind,
		FilePath: external.FilePath, StartLine: external.StartLine, External: true,
	}
}

func edgeForRelation(direction string, endpoint neighborEndpoint, relation sem.RelationRecord) neighborEdge {
	return neighborEdge{
		Direction: direction, Relation: relation.Type, Endpoint: endpoint,
		Confidence: relation.Confidence, Resolution: relation.Resolution,
		Reason: relation.Reason, Evidence: relation.Evidence,
	}
}

func sortNeighborEdges(edges []neighborEdge) {
	sort.Slice(edges, func(left, right int) bool { return neighborEdgeLess(edges[left], edges[right]) })
}

func appendBoundedNeighborEdge(edges []neighborEdge, candidate neighborEdge, limit int) []neighborEdge {
	if limit <= 0 {
		return edges
	}
	if len(edges) < limit {
		return append(edges, candidate)
	}
	worst := 0
	for index := 1; index < len(edges); index++ {
		if neighborEdgeLess(edges[worst], edges[index]) {
			worst = index
		}
	}
	if neighborEdgeLess(candidate, edges[worst]) {
		edges[worst] = candidate
	}
	return edges
}

func neighborEdgeLess(left, right neighborEdge) bool {
	if leftTier, rightTier := neighborEndpointTier(left.Endpoint), neighborEndpointTier(right.Endpoint); leftTier != rightTier {
		return leftTier < rightTier
	}
	if leftResolution, rightResolution := neighborResolutionTier(left.Resolution), neighborResolutionTier(right.Resolution); leftResolution != rightResolution {
		return leftResolution < rightResolution
	}
	if left.Confidence != right.Confidence {
		return left.Confidence > right.Confidence
	}
	if left.Endpoint.FilePath != right.Endpoint.FilePath {
		return left.Endpoint.FilePath < right.Endpoint.FilePath
	}
	if left.Endpoint.StartLine != right.Endpoint.StartLine {
		return left.Endpoint.StartLine < right.Endpoint.StartLine
	}
	leftName := left.Endpoint.QualifiedName
	if leftName == "" {
		leftName = left.Endpoint.Name
	}
	rightName := right.Endpoint.QualifiedName
	if rightName == "" {
		rightName = right.Endpoint.Name
	}
	if leftName != rightName {
		return leftName < rightName
	}
	if left.Endpoint.ID != right.Endpoint.ID {
		return left.Endpoint.ID < right.Endpoint.ID
	}
	if left.Relation != right.Relation {
		return left.Relation < right.Relation
	}
	if left.Direction != right.Direction {
		return left.Direction < right.Direction
	}
	if left.Resolution != right.Resolution {
		return left.Resolution < right.Resolution
	}
	if left.Reason != right.Reason {
		return left.Reason < right.Reason
	}
	return false
}

func neighborEndpointTier(endpoint neighborEndpoint) int {
	if endpoint.External {
		return 2
	}
	if isConventionalTestPath(endpoint.FilePath) {
		return 1
	}
	return 0
}

func neighborResolutionTier(resolution string) int {
	switch resolution {
	case "exact":
		return 0
	case "import_resolved":
		return 1
	default:
		return 2
	}
}

func writeAgentNeighbors(out io.Writer, response neighborResponse) error {
	return writeAgentNeighborsFull(out, response)
}

func writeAgentNeighborsBounded(out io.Writer, response neighborResponse, budget int) error {
	var full bytes.Buffer
	if err := writeAgentNeighborsFull(&full, response); err != nil {
		return err
	}
	if budget <= 0 || full.Len() <= budget {
		_, err := out.Write(full.Bytes())
		return err
	}
	// The compact payload is built independently of writeAgentNeighborsFull, so it
	// does not inherit that function's wrapped writer and is escaped on its own.
	payload := termsafe.Bytes(compactAgentNeighbors(response, budget))
	_, err := out.Write(payload)
	return err
}

func writeAgentNeighborsFull(out io.Writer, response neighborResponse) error {
	// Wrapped at the renderer rather than at its two callers, so the call-context
	// windows this prints — raw source lines, via writeCallContext — are covered
	// on the bounded path too, where the caller buffers this output before
	// trimming it. See writeTextSearch.
	out = termsafe.NewWriter(out)
	cacheState := "miss"
	if response.IndexCacheHit {
		cacheState = "hit"
	}
	fmt.Fprintf(out, "Index: cache-%s (%dms) | Query: %dms | Total: %dms\n",
		cacheState, response.IndexLatencyMS, response.QueryLatencyMS, response.TotalLatencyMS,
	)
	writeIndexCostNotice(out, response.IndexCacheHit, response.IndexCacheDisabled)
	writeAgentNeighborCompleteness(out, response)
	if len(response.Matches) == 0 {
		writeNoFocusMatch(out, response.Query, response.File, response.Line)
		return nil
	}
	if response.FuzzyMatch {
		definitions := make([]neighborEndpoint, 0, len(response.Matches))
		for _, match := range response.Matches {
			definitions = append(definitions, match.Symbol)
		}
		writeFuzzyMatchListing(out, response.Query, symbolMatchTierFromLabel(response.FuzzyMatchKind),
			definitions, response.MatchBodies)
		if response.DisambiguationRequired {
			return nil
		}
	} else if response.DisambiguationRequired {
		definitions := make([]neighborEndpoint, 0, len(response.Matches))
		for _, match := range response.Matches {
			definitions = append(definitions, match.Symbol)
		}
		writeDisambiguationListing(out, response.Query, response.FocusMatchesTotal, definitions,
			response.MatchBodies)
		return nil
	}
	if response.FocusMatchesTruncated {
		fmt.Fprintf(out,
			"Focus matches truncated: showing the first %d of %d in file/line order; use --symbol <file>:<line> to select a definition.\n",
			len(response.Matches), response.FocusMatchesTotal,
		)
	}
	budget := newCallContextBudgeter()
	for index, match := range response.Matches {
		if index > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "Focus: %s\n", formatNeighborFocus(match.Symbol))
		// A section that was never queried must not be printed as an empty
		// result: "Callees: none" for a --direction in query is a false
		// statement about the graph, and the reader has no way to tell.
		if neighborDirectionQueried(response.Direction, "in") {
			writeNeighborEdgeList(out, "Callers", match.Incoming, budget)
		} else {
			writeNeighborSectionNotQueried(out, "Callers", "out")
		}
		if neighborDirectionQueried(response.Direction, "out") {
			writeNeighborEdgeList(out, "Callees", match.Outgoing, budget)
		} else {
			writeNeighborSectionNotQueried(out, "Callees", "in")
		}
		if len(match.Paths) > 0 {
			writeNeighborPathFamily(out, match)
		}
	}
	if response.endpointTruncated {
		fmt.Fprintln(out, "Neighbor lists truncated; increase --limit for more callers or callees.")
	}
	return nil
}

func compactAgentNeighbors(response neighborResponse, budget int) []byte {
	if budget <= 0 {
		return nil
	}
	// The coverage marker is scoped: a parse failure in a language this answer
	// cannot contain a relation from is not a reason to distrust the answer.
	scope := completenessScopeOrAll(response.CompletenessScope,
		response.Warnings, response.PartialFailures, response.Stats)
	coverageIssue := len(scope.InScopeWarnings) > 0 || scope.LanguageFailed > 0
	marker := "!output-truncated"
	if coverageIssue {
		marker += "/coverage"
	}
	marker += "\n"
	if len(marker) >= budget {
		return []byte(marker[:budget])
	}

	var output bytes.Buffer
	output.WriteString(marker)
	appendVariant := func(variants ...string) bool {
		for _, variant := range variants {
			if variant == "" {
				continue
			}
			if output.Len()+len(variant) <= budget {
				output.WriteString(variant)
				return true
			}
		}
		return false
	}

	if len(response.Matches) == 0 {
		appendVariant(
			fmt.Sprintf("No symbols matched %q; check the name with `search`.\n", response.Query),
			fmt.Sprintf("No match: %s\n", response.Query),
		)
	} else if response.DisambiguationRequired {
		appendVariant(
			fmt.Sprintf("Ambiguous %q: %d definitions; rerun with the selector shown.\n", response.Query, response.FocusMatchesTotal),
			fmt.Sprintf("Ambiguous: %d; use --symbol <file>:<line>.\n", response.FocusMatchesTotal),
		)
		definitions := make([]neighborEndpoint, 0, len(response.Matches))
		for _, match := range response.Matches {
			definitions = append(definitions, match.Symbol)
		}
		selectors := disambiguationSelectors(definitions)
		for index, definition := range definitions {
			line := "- " + formatNeighborEndpoint(definition)
			if selectors[index] != "" {
				appendVariant(line+"  "+selectors[index]+"\n", line+"\n")
				continue
			}
			appendVariant(line + "\n")
		}
	} else {
		focus := response.Matches[0].Symbol
		// The SHORTER variants have to escape too. The first one inherits it from
		// formatNeighborEndpoint, but these are what get printed under byte
		// pressure — and a guard that holds only while the payload has room is
		// worse than none, because the case it drops is the one nobody re-reads.
		focusPath := termsafe.Line(focus.FilePath)
		appendVariant(
			"Focus: "+formatNeighborEndpoint(focus)+"\n",
			fmt.Sprintf("F %s:%d %s\n", focusPath, focus.StartLine, termsafe.Line(endpointDisplayName(focus))),
			fmt.Sprintf("F %s:%d\n", focusPath, focus.StartLine),
		)
	}

	// Under a hard byte cap, only the diagnostics that could have affected THIS
	// answer are worth a line; the rest is one count.
	appendVariant(compactCompletenessLine(scope, response.Stats))
	if coverageIssue {
		for _, warning := range scope.InScopeWarnings {
			appendVariant(fmt.Sprintf("W %s%s\n", warning.Code, agentDiagnosticPath(warning.FilePath)))
		}
		for _, failure := range scope.InScopeFailures {
			appendVariant(fmt.Sprintf("F %s%s\n", failure.Code, agentDiagnosticPath(failure.FilePath)))
		}
	}
	if response.endpointTruncated {
		appendVariant("!neighbor-list-truncated; raise --limit\n", "!neighbors-truncated\n")
	}
	cacheState := "miss"
	if response.IndexCacheHit {
		cacheState = "hit"
	}
	appendVariant(fmt.Sprintf("I:%s/%d Q:%d T:%d\n", cacheState, response.IndexLatencyMS, response.QueryLatencyMS, response.TotalLatencyMS))

	if len(response.Matches) > 0 && !response.DisambiguationRequired {
		match := response.Matches[0]
		for _, edge := range match.Incoming {
			appendVariant(compactNeighborEdge("<", edge))
		}
		for _, edge := range match.Outgoing {
			appendVariant(compactNeighborEdge(">", edge))
		}
		// Even at the tightest cap, an unqueried direction must not read as an
		// empty one. `<-only` / `>-only` costs 8 bytes and prevents the reader
		// concluding the graph has no callees.
		if !neighborDirectionQueried(response.Direction, "out") {
			appendVariant(">?not-queried\n")
		}
		if !neighborDirectionQueried(response.Direction, "in") {
			appendVariant("<?not-queried\n")
		}
		if len(match.Paths) > 0 {
			pathCount := len(match.Incoming) * len(match.Outgoing)
			appendVariant(fmt.Sprintf("Paths: %dx1x%d=%d (endpoints above)\n", len(match.Incoming), len(match.Outgoing), pathCount))
		}
	}
	return output.Bytes()
}

func compactNeighborEdge(direction string, edge neighborEdge) string {
	annotation := ""
	if edge.Resolution != "" {
		annotation = " [" + edge.Resolution + "]"
	}
	// The call site is the location a reader needs; the definition line is
	// still reported so the caller can be found as a symbol.
	return fmt.Sprintf("%s %s%s\n", direction, formatCallSiteLocation(edge.Endpoint, edge.CallSite), annotation)
}

func writeAgentNeighborCompleteness(out io.Writer, response neighborResponse) {
	writeScopedCompletenessBlock(out,
		completenessScopeOrAll(response.CompletenessScope, response.Warnings, response.PartialFailures, response.Stats),
		response.Warnings, response.PartialFailures, response.Stats)
}

// neighborDirectionQueried reports whether a --direction value asked for the
// given half of the adjacency.
func neighborDirectionQueried(requested, half string) bool {
	if requested == "" {
		// Absent direction means the caller did not scope the query (the flag
		// default is "both"), so both halves were computed.
		return true
	}
	return requested == "both" || requested == half
}

// writeNeighborSectionNotQueried states that a section holds no result because
// it was not asked for. It names the flag that would fill it, so a reader who
// wanted both halves can get them in one more call rather than concluding the
// graph is missing edges.
func writeNeighborSectionNotQueried(out io.Writer, label, other string) {
	fmt.Fprintf(out, "%s: not queried (--direction %s); rerun with --direction both to include them\n", label, other)
}

func writeNeighborPathFamily(out io.Writer, match neighborFocus) {
	pathCount := len(match.Incoming) * len(match.Outgoing)
	fmt.Fprintf(out, "Two-hop path family (%d caller%s × 1 focus × %d callee%s = %d path%s):\n",
		len(match.Incoming), pluralSuffix(len(match.Incoming)), len(match.Outgoing), pluralSuffix(len(match.Outgoing)), pathCount, pluralSuffix(pathCount))
	fmt.Fprintf(out, "- %s -> %s -> %s (locations above)\n",
		neighborEndpointNames(match.Incoming), endpointDisplayName(match.Symbol), neighborEndpointNames(match.Outgoing))
}

func neighborEndpointNames(edges []neighborEdge) string {
	if len(edges) == 1 {
		return endpointDisplayName(edges[0].Endpoint)
	}
	names := make([]string, 0, len(edges))
	for _, edge := range edges {
		names = append(names, endpointDisplayName(edge.Endpoint))
	}
	return "{" + strings.Join(names, "; ") + "}"
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func writeNeighborEdgeList(out io.Writer, label string, edges []neighborEdge, budget *callContextBudgeter) {
	fmt.Fprintf(out, "%s:\n", label)
	if len(edges) == 0 {
		fmt.Fprintln(out, "- none")
		return
	}
	for _, edge := range edges {
		fmt.Fprintf(out, "- %s", formatNeighborEdgeLocation(edge))
		annotations := make([]string, 0, 4)
		if edge.Endpoint.Kind == "file" {
			annotations = append(annotations, "file-level")
		}
		if edge.Relation != "CALLS" {
			annotations = append(annotations, edge.Relation)
		}
		if edge.CallSite != nil && edge.CallSite.AdditionalSites > 0 {
			annotations = append(annotations,
				fmt.Sprintf("+%d more call site%s", edge.CallSite.AdditionalSites, pluralSuffix(edge.CallSite.AdditionalSites)))
		}
		if edge.Resolution != "" {
			annotations = append(annotations, edge.Resolution)
		}
		if len(annotations) > 0 {
			fmt.Fprintf(out, " [%s]", strings.Join(annotations, ", "))
		}
		fmt.Fprintln(out)
		// Never inline a caller's body: a dispatch function can be thousands of
		// lines. A window plus the enclosing conditions is orders of magnitude
		// cheaper and answers the question the body was going to be read for.
		writeCallContext(out, edge.CallSite, budget)
	}
}

// formatNeighborEdgeLocation puts the CALL SITE in the primary location slot when
// one was resolved, and still reports the definition line so the endpoint can be
// looked up as a symbol.
func formatNeighborEdgeLocation(edge neighborEdge) string {
	return formatCallSiteLocation(edge.Endpoint, edge.CallSite)
}

// formatCallSiteLocation renders an endpoint at its call site, keeping the
// definition line alongside — and only when the two differ, so a short function
// that calls on its own definition line does not carry a duplicate number.
func formatCallSiteLocation(endpoint neighborEndpoint, site *callSite) string {
	if site == nil {
		return formatNeighborEndpoint(endpoint)
	}
	// Same one-line contract as formatNeighborEndpoint, which this deliberately
	// does not delegate to (it names the CALL SITE, not the definition) — so the
	// escaping has to be repeated rather than inherited.
	name := termsafe.Line(endpointDisplayName(endpoint))
	path := termsafe.Line(site.FilePath)
	if site.Line == endpoint.StartLine && site.FilePath == endpoint.FilePath {
		return fmt.Sprintf("%s (%s:%d)", name, path, site.Line)
	}
	return fmt.Sprintf("%s (%s:%d, def :%d)", name, path, site.Line, endpoint.StartLine)
}

// formatNeighborEndpoint is the one place an endpoint becomes display text, for
// the edge lists, the disambiguation listing, the fuzzy-match listing and the
// compact payload alike. Its result is always placed on a line of its own by its
// callers, so the name and the path are one-line values and are escaped here —
// covering every caller at once, which is why the escape is not repeated at each
// of them.
func formatNeighborEndpoint(endpoint neighborEndpoint) string {
	name := termsafe.Line(endpointDisplayName(endpoint))
	if endpoint.FilePath == "" {
		return name
	}
	if endpoint.StartLine > 0 {
		return fmt.Sprintf("%s (%s:%d)", name, termsafe.Line(endpoint.FilePath), endpoint.StartLine)
	}
	return fmt.Sprintf("%s (%s)", name, termsafe.Line(endpoint.FilePath))
}

// formatNeighborFocus renders the symbol under query with BOTH numbers a reader
// might see for it elsewhere, each labelled: `def=` is the line the definition
// starts on (what a relation answer reports), `span=` is the range it covers
// (what a ranked search result reports). Two unlabelled numbers for one symbol
// in one session read as a contradiction; labelled, they are one fact.
func formatNeighborFocus(endpoint neighborEndpoint) string {
	base := formatNeighborEndpoint(endpoint)
	if endpoint.StartLine <= 0 || endpoint.EndLine < endpoint.StartLine {
		return base
	}
	return fmt.Sprintf("%s def=%d span=%d-%d", base, endpoint.StartLine, endpoint.StartLine, endpoint.EndLine)
}

func endpointDisplayName(endpoint neighborEndpoint) string {
	if endpoint.QualifiedName != "" {
		return endpoint.QualifiedName
	}
	return endpoint.Name
}
