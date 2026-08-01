package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/entireio/entire-graph/internal/sem"
)

const (
	defaultImpactSectionLimit = 15
	defaultImpactContextBytes = 4 * 1024
)

// impactFlags configures the one-shot blast-radius query. It reuses the
// neighbors snapshot/caching machinery, so the shared flags carry the same
// semantics (--head vs worktree, --profile, --cache-dir, --exclude-tests).
type impactFlags struct {
	Repo            string
	Symbol          string
	File            string
	Line            int
	Kind            string
	Format          string
	Profile         string
	Depth           int
	Limit           int
	Worktree        bool
	IgnoreFile      []string
	IncludeFile     []string
	CacheDir        string
	DisableCache    bool
	ExcludeTests    bool
	MaxContextBytes int
}

// impactEntry is one affected location. Direction is "in" (points at the
// focus) or "out" (the focus points at it); Depth and Via are set for
// transitive callers; Detail carries human context such as the co-change
// evidence sentence.
type impactEntry struct {
	Endpoint  neighborEndpoint `json:"endpoint"`
	Relation  string           `json:"relation,omitempty"`
	Direction string           `json:"direction,omitempty"`
	Depth     int              `json:"depth,omitempty"`
	Via       string           `json:"via,omitempty"`
	Detail    string           `json:"detail,omitempty"`
	// CallSite is where a caller actually writes the call. `impact` is a bounded
	// overview, so it reports the LINE only and never quotes source; use
	// `neighbors --direction in` for the conditions around a call.
	CallSite *callSite `json:"call_site,omitempty"`

	// Evidence span and written call text, kept only to resolve CallSite after
	// the graph query. Not part of the wire format.
	callFile  string
	callStart int
	callEnd   int
	callToken string
}

// impactSection is one bounded facet of the blast radius. Total counts every
// match before the per-section entry cap; Direct/Transitive break down callers
// and In/Out break down bidirectional sections.
type impactSection struct {
	Total      int           `json:"total"`
	Direct     int           `json:"direct,omitempty"`
	Transitive int           `json:"transitive,omitempty"`
	In         int           `json:"in,omitempty"`
	Out        int           `json:"out,omitempty"`
	Entries    []impactEntry `json:"entries"`
}

type impactResponse struct {
	FormatVersion          int                    `json:"format_version"`
	RepoRoot               string                 `json:"repo_root"`
	Commit                 string                 `json:"commit,omitempty"`
	Tree                   string                 `json:"tree,omitempty"`
	Profile                string                 `json:"profile"`
	Query                  string                 `json:"query"`
	File                   string                 `json:"file,omitempty"`
	Line                   int                    `json:"line,omitempty"`
	Depth                  int                    `json:"depth"`
	IndexCacheHit          bool                   `json:"index_cache_hit"`
	IndexCacheDisabled     bool                   `json:"index_cache_disabled,omitempty"`
	IndexLatencyMS         int64                  `json:"index_latency_ms"`
	QueryLatencyMS         int64                  `json:"query_latency_ms"`
	TotalLatencyMS         int64                  `json:"total_latency_ms"`
	FocusMatchesTotal      int                    `json:"focus_matches_total"`
	DisambiguationRequired bool                   `json:"disambiguation_required"`
	Definitions            []neighborEndpoint     `json:"definitions,omitempty"`
	Focus                  *neighborEndpoint      `json:"focus,omitempty"`
	Container              *neighborEndpoint      `json:"container,omitempty"`
	Callers                impactSection          `json:"callers"`
	Callees                impactSection          `json:"callees"`
	TypeConsumers          impactSection          `json:"type_consumers"`
	DataFlows              impactSection          `json:"data_flows"`
	CoChanges              impactSection          `json:"co_changes"`
	Siblings               impactSection          `json:"siblings"`
	Warnings               []sem.ProviderWarning  `json:"warnings,omitempty"`
	PartialFailures        []sem.PartialFailure   `json:"partial_failures"`
	Stats                  sem.ProviderStats      `json:"stats"`
	Completeness           sem.CompletenessReport `json:"completeness"`
	// CompletenessScope is the query-relative reading of the diagnostics above.
	CompletenessScope completenessScope `json:"completeness_scope"`
}

func runImpact(ctx context.Context, opts Options, args []string) error {
	flags, err := parseImpactFlags(args)
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
	indexLatency := time.Since(indexStarted)
	queryStarted := time.Now()
	response := buildImpactResponse(snapshot, flags)
	annotateImpactCallSites(&response, newRepoLineReader(snapshot.Header.RepoRoot))
	queryLatency := time.Since(queryStarted)
	response.IndexCacheHit = cacheHit
	response.IndexCacheDisabled = cacheDir == "" || flags.DisableCache
	response.IndexLatencyMS = indexLatency.Milliseconds()
	response.QueryLatencyMS = queryLatency.Milliseconds()
	response.TotalLatencyMS = time.Since(totalStarted).Milliseconds()
	switch flags.Format {
	case "json":
		encoder := json.NewEncoder(opts.Stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(response)
	case "text":
		return writeImpactBounded(opts.Stdout, response, flags.MaxContextBytes)
	default:
		return fmt.Errorf("impact --format must be text or json, got %q", flags.Format)
	}
}

func parseImpactFlags(args []string) (impactFlags, error) {
	flags := impactFlags{
		Format: "text", Profile: "full", Depth: 2,
		Limit: defaultImpactSectionLimit, Worktree: true,
		MaxContextBytes: defaultImpactContextBytes,
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
			parsed, next, parseErr := searchPositiveIntFlag(args, index)
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
		case "--exclude-tests":
			flags.ExcludeTests = true
		default:
			return flags, fmt.Errorf("impact received unexpected argument %q", arg)
		}
	}
	if strings.TrimSpace(flags.Symbol) == "" {
		return flags, errors.New("impact requires --symbol")
	}
	if flags.Depth != 1 && flags.Depth != 2 {
		return flags, errors.New("impact --depth must be 1 or 2")
	}
	return flags, nil
}

// impactCallRelation is the callable-dependency family: like neighbors, it
// folds CONSTRUCTS into calls (constructor invocations break too), and adds
// ASYNC_CALLS because an async caller breaks the same way a sync one does.
func impactCallRelation(relationType string) bool {
	return relationType == "CALLS" || relationType == "CONSTRUCTS" || relationType == "ASYNC_CALLS"
}

func impactTypeRelation(relationType string) bool {
	return relationType == "USES_TYPE" || relationType == "PARAM_TYPE" || relationType == "RETURNS_TYPE"
}

// impactMentionDetail labels a caller entry that is a documented mention rather than a
// resolved call, so the distinction the graph already records (resolution="name_only" on a
// non-code file) survives into `impact`'s presentation instead of being discarded.
const impactMentionDetail = "doc-mention, name_only"

// impactNonCodeMention reports whether an incoming edge is a NAME-ONLY match attributed to
// a file that holds no program text — prose, serialized data, generated artifacts — or to a
// language parsed for inventory only.
//
// Both halves are required. A resolved edge from a documentation file (a doc-tool script
// living under docs/) is a real call. A name-only edge between two source files is a real,
// if lower-confidence, call. Only the conjunction — "this file cannot execute, and the
// match was lexical" — identifies a mention.
func impactNonCodeMention(relation sem.RelationRecord, endpoint neighborEndpoint, languages map[string]string) bool {
	if relation.Resolution != "name_only" {
		return false
	}
	if endpoint.FilePath != "" && sem.NonProgramTextPath(endpoint.FilePath) {
		return true
	}
	return sem.InventoryOnlyLanguage(languages[endpoint.ID])
}

func buildImpactResponse(snapshot sem.ProviderSnapshot, flags impactFlags) impactResponse {
	endpoints := make(map[string]neighborEndpoint, len(snapshot.Symbols)+len(snapshot.Externals))
	// Relation records carry no language, and file endpoints carry no kind beyond
	// "file", so the language an edge came from has to be looked up by endpoint ID.
	endpointLanguages := make(map[string]string, len(snapshot.Files)+len(snapshot.Symbols))
	for _, file := range snapshot.Files {
		endpoints[file.ID] = endpointForFile(file)
		endpointLanguages[file.ID] = file.Language
	}
	for _, symbol := range snapshot.Symbols {
		endpoints[symbol.ID] = endpointForSymbol(symbol)
		endpointLanguages[symbol.ID] = symbol.Language
	}
	for _, external := range snapshot.Externals {
		endpoints[external.ID] = endpointForExternal(external)
	}

	ref := parseSymbolRef(flags.Symbol, flags.File, flags.Line, flags.Kind, snapshot.Header.RepoRoot, snapshotFilePaths(snapshot))
	focuses := resolveFocusSymbols(snapshot.Symbols, ref)
	sort.Slice(focuses, func(left, right int) bool {
		if focuses[left].FilePath != focuses[right].FilePath {
			return focuses[left].FilePath < focuses[right].FilePath
		}
		if focuses[left].StartLine != focuses[right].StartLine {
			return focuses[left].StartLine < focuses[right].StartLine
		}
		return focuses[left].ID < focuses[right].ID
	})

	partialFailures := snapshot.Header.PartialFailures
	if partialFailures == nil {
		partialFailures = []sem.PartialFailure{}
	}
	response := impactResponse{
		FormatVersion:     1,
		RepoRoot:          snapshot.Header.RepoRoot,
		Commit:            snapshot.Header.Commit,
		Tree:              snapshot.Header.Tree,
		Profile:           snapshot.Header.Profile,
		Query:             flags.Symbol,
		File:              ref.File,
		Line:              ref.Line,
		Depth:             flags.Depth,
		FocusMatchesTotal: len(focuses),
		Warnings:          snapshot.Header.Warnings,
		PartialFailures:   partialFailures,
		Stats:             snapshot.Header.Stats,
		Completeness:      snapshot.Header.Completeness,
		CompletenessScope: buildCompletenessScope(snapshot, focusQueryLanguage(focuses)),
	}
	if len(focuses) == 0 {
		return response
	}
	if len(focuses) > 1 {
		response.DisambiguationRequired = true
		bounded := focuses
		if flags.Limit > 0 && len(bounded) > flags.Limit {
			bounded = bounded[:flags.Limit]
		}
		for _, focus := range bounded {
			response.Definitions = append(response.Definitions, endpointForSymbol(focus))
		}
		return response
	}

	focus := focuses[0]
	focusEndpoint := endpointForSymbol(focus)
	response.Focus = &focusEndpoint
	if focus.ContainerID != "" {
		if container, ok := endpoints[focus.ContainerID]; ok {
			response.Container = &container
		}
	}
	focusFileID := ""
	for _, file := range snapshot.Files {
		if file.Path == focus.FilePath {
			focusFileID = file.ID
			break
		}
	}

	allowed := func(endpoint neighborEndpoint) bool {
		return !flags.ExcludeTests || !isConventionalTestPath(endpoint.FilePath)
	}

	// One pass over the relation stream: collect the full incoming call
	// adjacency (needed for the depth-2 caller walk) and the focus-touching
	// edges for every other section on the fly.
	callsIn := make(map[string][]sem.RelationRecord)
	var callees, typeEntries, flowEntries, cochangeEntries []impactEntry
	seenCallee := map[string]bool{}
	seenTouching := map[string]bool{}
	seenCochange := map[string]bool{}
	appendTouching := func(entries *[]impactEntry, relation sem.RelationRecord) {
		direction, otherID := "", ""
		switch {
		case relation.ToID == focus.ID && relation.FromID != focus.ID:
			direction, otherID = "in", relation.FromID
		case relation.FromID == focus.ID && relation.ToID != focus.ID:
			direction, otherID = "out", relation.ToID
		default:
			return
		}
		key := direction + "\x00" + relation.Type + "\x00" + otherID
		if seenTouching[key] {
			return
		}
		endpoint, ok := endpoints[otherID]
		if !ok || !allowed(endpoint) {
			return
		}
		seenTouching[key] = true
		*entries = append(*entries, impactEntry{
			Endpoint: endpoint, Relation: relation.Type, Direction: direction, Depth: 1,
		})
	}
	for _, relation := range snapshot.Relations {
		switch {
		case impactCallRelation(relation.Type):
			callsIn[relation.ToID] = append(callsIn[relation.ToID], relation)
			if relation.FromID == focus.ID {
				key := relation.Type + "\x00" + relation.ToID
				if seenCallee[key] {
					continue
				}
				if endpoint, ok := endpoints[relation.ToID]; ok && allowed(endpoint) {
					seenCallee[key] = true
					callees = append(callees, impactEntry{
						Endpoint: endpoint, Relation: relation.Type, Direction: "out", Depth: 1,
					})
				}
			}
		case impactTypeRelation(relation.Type):
			appendTouching(&typeEntries, relation)
		case relation.Type == "DATA_FLOWS":
			appendTouching(&flowEntries, relation)
		case relation.Type == "FILE_CHANGES_WITH" && focusFileID != "":
			otherID := ""
			if relation.FromID == focusFileID {
				otherID = relation.ToID
			} else if relation.ToID == focusFileID {
				otherID = relation.FromID
			}
			if otherID == "" || seenCochange[otherID] {
				continue
			}
			if endpoint, ok := endpoints[otherID]; ok && allowed(endpoint) {
				seenCochange[otherID] = true
				cochangeEntries = append(cochangeEntries, impactEntry{
					Endpoint: endpoint, Relation: relation.Type, Detail: relation.Reason,
				})
			}
		}
	}

	// Breadth-first caller walk: depth 1 is direct callers, depth 2 their
	// callers. Each symbol is reported once at its shallowest depth; Via names
	// the first-seen intermediate for transitive entries.
	//
	// A name-only match on a file that holds no program text (a design document, a
	// changelog, a recorded fixture) is a MENTION of the symbol, not a call of it: the
	// document does not break when behavior changes. Such an edge is kept as a
	// direct caller — it IS evidence that the name is written down somewhere, and the
	// user may need to update the prose — but it is labelled, sorted to the end so it can
	// never displace a real caller under --limit, and NEVER traversed: expanding it a
	// second hop manufactures "callers" that do not contain the symbol's name at all.
	type callerNode struct {
		id   string
		name string
	}
	callers := make([]impactEntry, 0)
	seenCaller := map[string]bool{focus.ID: true}
	frontier := []callerNode{{id: focus.ID}}
	for depth := 1; depth <= flags.Depth; depth++ {
		var next []callerNode
		for _, node := range frontier {
			for _, relation := range callsIn[node.id] {
				if seenCaller[relation.FromID] {
					continue
				}
				endpoint, ok := endpoints[relation.FromID]
				if !ok || !allowed(endpoint) {
					continue
				}
				mention := impactNonCodeMention(relation, endpoint, endpointLanguages)
				if mention && depth > 1 {
					// Not marked seen: a later real edge to the same node must still land.
					continue
				}
				seenCaller[relation.FromID] = true
				entry := impactEntry{
					Endpoint: endpoint, Relation: relation.Type, Direction: "in", Depth: depth,
				}
				// Keep the caller-body span and written call text so the call
				// LINE can replace the definition line in the answer.
				if callFile, callStart, callEnd, detail, ok := callEvidenceSpan(relation.Evidence); ok {
					if callFile == "" {
						callFile = endpoint.FilePath
					}
					entry.callFile, entry.callStart, entry.callEnd = callFile, callStart, callEnd
					// The callee as written is normally in the evidence detail; the
					// callee endpoint's own NAME (not its qualified name) is the
					// fallback, because that is what appears at a call site.
					entry.callToken = callTokenFromDetail(detail, endpoints[relation.ToID].Name)
				}
				if depth > 1 {
					entry.Via = node.name
				}
				if mention {
					entry.Detail = impactMentionDetail
					callers = append(callers, entry)
					continue
				}
				callers = append(callers, entry)
				next = append(next, callerNode{id: relation.FromID, name: endpointDisplayName(endpoint)})
			}
		}
		frontier = next
	}

	var siblings []impactEntry
	for _, symbol := range snapshot.Symbols {
		if symbol.ID == focus.ID {
			continue
		}
		switch {
		case focus.ContainerID != "" && symbol.ContainerID == focus.ContainerID:
			siblings = append(siblings, impactEntry{Endpoint: endpointForSymbol(symbol), Detail: "sibling"})
		case symbol.ContainerID == focus.ID:
			siblings = append(siblings, impactEntry{Endpoint: endpointForSymbol(symbol), Detail: "member"})
		}
	}

	response.Callers = impactSectionOf(callers, flags.Limit)
	response.Callees = impactSectionOf(callees, flags.Limit)
	response.TypeConsumers = impactSectionOf(typeEntries, flags.Limit)
	response.DataFlows = impactSectionOf(flowEntries, flags.Limit)
	response.CoChanges = impactSectionOf(cochangeEntries, flags.Limit)
	response.Siblings = impactSectionOf(siblings, flags.Limit)
	return response
}

// annotateImpactCallSites resolves the concrete call line for every caller entry
// that carried call evidence. `impact` stays a bounded overview: it upgrades the
// LOCATION (a caller's definition line points at the wrong place in a long
// function) but never quotes source, which would blow its byte budget and push
// real sections out of the answer.
func annotateImpactCallSites(response *impactResponse, read lineReader) {
	if response == nil || read == nil {
		return
	}
	for index := range response.Callers.Entries {
		entry := &response.Callers.Entries[index]
		if entry.callFile == "" || entry.callToken == "" {
			continue
		}
		site, ok := resolveCallSite(read, entry.callFile, entry.callToken, entry.callStart, entry.callEnd)
		if !ok {
			continue
		}
		// The window and guards are the neighbors verb's job; drop them so an
		// impact JSON payload does not carry source it never renders.
		site.Window, site.WindowStart, site.WindowEnd, site.Guards = nil, 0, 0, nil
		entry.CallSite = &site
	}
}

// formatImpactEntryLocation reports a caller at its call site, with the
// definition line kept alongside; every other section keeps its endpoint
// location, which is already the right one.
func formatImpactEntryLocation(entry impactEntry) string {
	return formatCallSiteLocation(entry.Endpoint, entry.CallSite)
}

// impactSectionOf sorts entries deterministically (depth, then file/line/name),
// records the pre-cap totals and direction breakdowns, and applies the
// per-section entry cap.
func impactSectionOf(entries []impactEntry, limit int) impactSection {
	sort.Slice(entries, func(left, right int) bool {
		// Documented mentions sort behind every resolved entry, ahead of every other
		// key: the per-section cap must spend its slots on entries that can break.
		// Only caller entries ever carry this detail, so no other section is affected.
		leftMention := entries[left].Detail == impactMentionDetail
		rightMention := entries[right].Detail == impactMentionDetail
		if leftMention != rightMention {
			return rightMention
		}
		if entries[left].Depth != entries[right].Depth {
			return entries[left].Depth < entries[right].Depth
		}
		if entries[left].Endpoint.FilePath != entries[right].Endpoint.FilePath {
			return entries[left].Endpoint.FilePath < entries[right].Endpoint.FilePath
		}
		if entries[left].Endpoint.StartLine != entries[right].Endpoint.StartLine {
			return entries[left].Endpoint.StartLine < entries[right].Endpoint.StartLine
		}
		if leftName, rightName := endpointDisplayName(entries[left].Endpoint), endpointDisplayName(entries[right].Endpoint); leftName != rightName {
			return leftName < rightName
		}
		if entries[left].Endpoint.ID != entries[right].Endpoint.ID {
			return entries[left].Endpoint.ID < entries[right].Endpoint.ID
		}
		return entries[left].Relation < entries[right].Relation
	})
	section := impactSection{Total: len(entries)}
	for _, entry := range entries {
		switch {
		case entry.Direction == "in" && entry.Depth > 1:
			section.Transitive++
		case entry.Direction == "in":
			section.In++
			section.Direct++
		case entry.Direction == "out":
			section.Out++
		}
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	if entries == nil {
		entries = []impactEntry{}
	}
	section.Entries = entries
	return section
}

// writeImpactBounded keeps the text explanation inside the byte budget
// (default ~4KB, agent-context-friendly). If the full render is over budget it
// re-renders with progressively tighter per-section caps, then falls back to a
// hard line-boundary truncation with an explicit marker.
func writeImpactBounded(out io.Writer, response impactResponse, budget int) error {
	var full bytes.Buffer
	writeImpactText(&full, response)
	if budget <= 0 || full.Len() <= budget {
		_, err := out.Write(full.Bytes())
		return err
	}
	rendered := full.Bytes()
	for _, sectionCap := range []int{8, 5, 3} {
		var reduced bytes.Buffer
		writeImpactText(&reduced, capImpactSections(response, sectionCap))
		if reduced.Len() <= budget {
			_, err := out.Write(reduced.Bytes())
			return err
		}
		rendered = reduced.Bytes()
	}
	const marker = "!output-truncated; use --format json\n"
	if len(marker) >= budget {
		_, err := out.Write([]byte(marker[:budget]))
		return err
	}
	cut := bytes.LastIndexByte(rendered[:budget-len(marker)], '\n')
	if cut < 0 {
		cut = 0
	} else {
		cut++
	}
	if _, err := out.Write(rendered[:cut]); err != nil {
		return err
	}
	_, err := io.WriteString(out, marker)
	return err
}

func capImpactSections(response impactResponse, limit int) impactResponse {
	trim := func(section impactSection) impactSection {
		if limit > 0 && len(section.Entries) > limit {
			section.Entries = section.Entries[:limit]
		}
		return section
	}
	response.Callers = trim(response.Callers)
	response.Callees = trim(response.Callees)
	response.TypeConsumers = trim(response.TypeConsumers)
	response.DataFlows = trim(response.DataFlows)
	response.CoChanges = trim(response.CoChanges)
	response.Siblings = trim(response.Siblings)
	if limit > 0 && len(response.Definitions) > limit {
		response.Definitions = response.Definitions[:limit]
	}
	return response
}

func writeImpactText(out io.Writer, response impactResponse) {
	cacheState := "miss"
	if response.IndexCacheHit {
		cacheState = "hit"
	}
	fmt.Fprintf(out, "Index: cache-%s (%dms) | Query: %dms | Total: %dms\n",
		cacheState, response.IndexLatencyMS, response.QueryLatencyMS, response.TotalLatencyMS,
	)
	writeIndexCostNotice(out, response.IndexCacheHit, response.IndexCacheDisabled)
	writeScopedCompletenessBlock(out,
		completenessScopeOrAll(response.CompletenessScope, response.Warnings, response.PartialFailures, response.Stats),
		response.Warnings, response.PartialFailures, response.Stats)
	if response.FocusMatchesTotal == 0 {
		writeNoFocusMatch(out, response.Query, response.File, response.Line)
		return
	}
	if response.DisambiguationRequired {
		writeDisambiguationListing(out, response.Query, response.FocusMatchesTotal, response.Definitions)
		return
	}

	focusLine := formatNeighborFocus(*response.Focus)
	if response.Focus.Kind != "" {
		annotation := response.Focus.Kind
		if response.Container != nil {
			annotation += " in " + endpointDisplayName(*response.Container)
		}
		focusLine += " [" + annotation + "]"
	}
	fmt.Fprintf(out, "Impact: %s\n", focusLine)
	fmt.Fprintf(out, "Blast radius: %d caller%s (%d direct, %d transitive), %d callee%s, %d type consumer%s, %d data flow%s, %d co-change file%s, %d sibling%s.\n",
		response.Callers.Total, pluralSuffix(response.Callers.Total),
		response.Callers.Direct, response.Callers.Transitive,
		response.Callees.Total, pluralSuffix(response.Callees.Total),
		response.TypeConsumers.Total, pluralSuffix(response.TypeConsumers.Total),
		response.DataFlows.Total, pluralSuffix(response.DataFlows.Total),
		response.CoChanges.Total, pluralSuffix(response.CoChanges.Total),
		response.Siblings.Total, pluralSuffix(response.Siblings.Total),
	)
	writeImpactSection(out,
		fmt.Sprintf("Callers (%d direct, %d transitive; who breaks if behavior changes)",
			response.Callers.Direct, response.Callers.Transitive),
		response.Callers, false)
	writeImpactSection(out,
		fmt.Sprintf("Callees (%d; what it depends on)", response.Callees.Total),
		response.Callees, false)
	writeImpactSection(out,
		fmt.Sprintf("Type consumers (%d in, %d out; USES_TYPE/PARAM_TYPE/RETURNS_TYPE)",
			response.TypeConsumers.In, response.TypeConsumers.Out),
		response.TypeConsumers, true)
	writeImpactSection(out,
		fmt.Sprintf("Data flows (%d in, %d out)", response.DataFlows.In, response.DataFlows.Out),
		response.DataFlows, true)
	writeImpactSection(out,
		fmt.Sprintf("Co-change coupling (%d; files that historically change with %s)",
			response.CoChanges.Total, response.Focus.FilePath),
		response.CoChanges, false)
	writeImpactSection(out,
		fmt.Sprintf("Same-container siblings (%d)", response.Siblings.Total),
		response.Siblings, false)
}

// writeImpactSection prints one bounded section. arrows toggles the <- / ->
// direction prefix used by the bidirectional sections.
func writeImpactSection(out io.Writer, header string, section impactSection, arrows bool) {
	fmt.Fprintf(out, "%s:\n", header)
	if len(section.Entries) == 0 {
		fmt.Fprintln(out, "- none")
		return
	}
	for _, entry := range section.Entries {
		prefix := ""
		if arrows {
			if entry.Direction == "in" {
				prefix = "<- "
			} else {
				prefix = "-> "
			}
		}
		if entry.Relation == "FILE_CHANGES_WITH" {
			fmt.Fprintf(out, "- %s [%s]\n", entry.Endpoint.FilePath, entry.Detail)
			continue
		}
		fmt.Fprintf(out, "- %s%s", prefix, formatImpactEntryLocation(entry))
		annotations := make([]string, 0, 4)
		if entry.CallSite != nil && entry.CallSite.AdditionalSites > 0 {
			annotations = append(annotations,
				fmt.Sprintf("+%d more call site%s", entry.CallSite.AdditionalSites, pluralSuffix(entry.CallSite.AdditionalSites)))
		}
		// CALLS is the section default and DATA_FLOWS is its whole section's
		// relation, so annotating either would be noise.
		if entry.Relation != "" && entry.Relation != "CALLS" && entry.Relation != "DATA_FLOWS" {
			annotations = append(annotations, entry.Relation)
		}
		if entry.Via != "" {
			annotations = append(annotations, "via "+entry.Via)
		}
		if entry.Detail == "member" {
			annotations = append(annotations, "member")
		}
		if entry.Detail == impactMentionDetail {
			annotations = append(annotations, impactMentionDetail)
		}
		if len(annotations) > 0 {
			fmt.Fprintf(out, " [%s]", strings.Join(annotations, ", "))
		}
		fmt.Fprintln(out)
	}
	if omitted := section.Total - len(section.Entries); omitted > 0 {
		fmt.Fprintf(out, "- ... +%d more (use --format json for the full list)\n", omitted)
	}
}
