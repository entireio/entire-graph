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
	Depth                  int                    `json:"depth"`
	IndexCacheHit          bool                   `json:"index_cache_hit"`
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
	cacheDir := flags.CacheDir
	if cacheDir == "" {
		cacheDir = opts.Env.PluginDataDir
	}
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
	queryLatency := time.Since(queryStarted)
	response.IndexCacheHit = cacheHit
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

func buildImpactResponse(snapshot sem.ProviderSnapshot, flags impactFlags) impactResponse {
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

	focuses := make([]sem.SymbolRecord, 0)
	query := strings.TrimSpace(flags.Symbol)
	for _, symbol := range snapshot.Symbols {
		if !strings.EqualFold(symbol.Name, query) && !strings.EqualFold(symbol.QualifiedName, query) {
			continue
		}
		if flags.File != "" && !strings.EqualFold(symbol.FilePath, flags.File) {
			continue
		}
		focuses = append(focuses, symbol)
	}
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
		File:              flags.File,
		Depth:             flags.Depth,
		FocusMatchesTotal: len(focuses),
		Warnings:          snapshot.Header.Warnings,
		PartialFailures:   partialFailures,
		Stats:             snapshot.Header.Stats,
		Completeness:      snapshot.Header.Completeness,
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
				seenCaller[relation.FromID] = true
				entry := impactEntry{
					Endpoint: endpoint, Relation: relation.Type, Direction: "in", Depth: depth,
				}
				if depth > 1 {
					entry.Via = node.name
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

// impactSectionOf sorts entries deterministically (depth, then file/line/name),
// records the pre-cap totals and direction breakdowns, and applies the
// per-section entry cap.
func impactSectionOf(entries []impactEntry, limit int) impactSection {
	sort.Slice(entries, func(left, right int) bool {
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
	writeCompletenessBlock(out, response.Warnings, response.PartialFailures, response.Stats)
	if response.FocusMatchesTotal == 0 {
		fmt.Fprintf(out, "No symbols matched %q. Add --file to disambiguate a known definition.\n", response.Query)
		return
	}
	if response.DisambiguationRequired {
		fmt.Fprintf(out,
			"Ambiguous symbol %q matched %d definitions; rerun with --file and, if needed, a qualified --symbol.\n",
			response.Query, response.FocusMatchesTotal,
		)
		for _, definition := range response.Definitions {
			fmt.Fprintf(out, "- %s\n", formatNeighborEndpoint(definition))
		}
		if omitted := response.FocusMatchesTotal - len(response.Definitions); omitted > 0 {
			fmt.Fprintf(out, "- ... %d more definitions; raise --limit to list them\n", omitted)
		}
		return
	}

	focusLine := formatNeighborEndpoint(*response.Focus)
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
		fmt.Fprintf(out, "- %s%s", prefix, formatNeighborEndpoint(entry.Endpoint))
		annotations := make([]string, 0, 3)
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
		if len(annotations) > 0 {
			fmt.Fprintf(out, " [%s]", strings.Join(annotations, ", "))
		}
		fmt.Fprintln(out)
	}
	if omitted := section.Total - len(section.Entries); omitted > 0 {
		fmt.Fprintf(out, "- ... +%d more (use --format json for the full list)\n", omitted)
	}
}
