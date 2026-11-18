package sem

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The Vue corpus is two realistic Vue applications with a hand-written ground
// truth (testdata/vuecorpus/<app>/expected.json). It measures how much of a
// correct Vue graph the provider emits, tag by tag, and ratchets the result in
// testdata/vuecorpus/score.json. See testdata/vuecorpus/README.md.
var vueCorpusApps = []string{"helpdesk", "taskboard"}

const vueCorpusRoot = "testdata/vuecorpus"

type vueCorpusEndpoint struct {
	File     string `json:"file,omitempty"`
	Name     string `json:"name,omitempty"`
	Kind     string `json:"kind,omitempty"`
	External string `json:"external,omitempty"`
}

func (e vueCorpusEndpoint) key() string {
	switch {
	case e.External != "":
		return "ext:" + e.External
	case e.Name == "":
		return e.File
	case e.Kind != "":
		return e.File + "#" + e.Name + ":" + e.Kind
	default:
		return e.File + "#" + e.Name
	}
}

// vueCorpusTypes accepts a relation type written as one string or a list.
type vueCorpusTypes []string

func (t *vueCorpusTypes) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*t = vueCorpusTypes{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("relation type must be a string or a list of strings: %w", err)
	}
	*t = many
	return nil
}

type vueCorpusSymbol struct {
	File string `json:"file"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
	Line int    `json:"line"`
	Tag  string `json:"tag"`
}

type vueCorpusAbsent struct {
	File string `json:"file"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
	Tag  string `json:"tag"`
	Why  string `json:"why"`
}

type vueCorpusRelation struct {
	Type vueCorpusTypes    `json:"type"`
	From vueCorpusEndpoint `json:"from"`
	To   vueCorpusEndpoint `json:"to"`
	Tag  string            `json:"tag"`
	Note string            `json:"note,omitempty"`
}

// vueCorpusAbsentRelation is an edge a correct graph must not emit. Without a
// To endpoint it forbids every edge of those types from the From endpoint.
type vueCorpusAbsentRelation struct {
	Type vueCorpusTypes     `json:"type"`
	From vueCorpusEndpoint  `json:"from"`
	To   *vueCorpusEndpoint `json:"to,omitempty"`
	Tag  string             `json:"tag"`
	Why  string             `json:"why"`
}

func (r vueCorpusAbsentRelation) key() string {
	to := "*"
	if r.To != nil {
		to = r.To.key()
	}
	return strings.Join(r.Type, "|") + " " + r.From.key() + " -> " + to
}

type vueCorpusManifest struct {
	App             string                    `json:"app"`
	Description     string                    `json:"description,omitempty"`
	Symbols         []vueCorpusSymbol         `json:"symbols"`
	AbsentSymbols   []vueCorpusAbsent         `json:"absent_symbols"`
	Relations       []vueCorpusRelation       `json:"relations"`
	AbsentRelations []vueCorpusAbsentRelation `json:"absent_relations"`
}

var vueCorpusTags = map[string]bool{
	"baseline": true, "script-symbols": true, "script-imports": true, "script-calls": true,
	"component-owner": true, "false-elements": true, "template-edges": true, "options-api": true,
	"store-members": true, "path-alias": true, "lazy-imports": true, "global-registration": true,
	"ts-types": true, "mixins": true, "event-bus": true, "tests": true, "script-src": true,
	"js-resolution": true, "false-relations": true,
}

func loadVueCorpusManifest(t *testing.T, app string) vueCorpusManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(vueCorpusRoot, app, "expected.json"))
	if err != nil {
		t.Fatalf("read %s manifest: %v", app, err)
	}
	var manifest vueCorpusManifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatalf("parse %s manifest: %v", app, err)
	}
	return manifest
}

func vueCorpusSymbolKey(file, name, kind string) string {
	if kind != "" {
		return "symbol " + file + "#" + name + ":" + kind
	}
	return "symbol " + file + "#" + name
}

func (r vueCorpusRelation) key() string {
	return strings.Join(r.Type, "|") + " " + r.From.key() + " -> " + r.To.key()
}

// TestVueCorpusManifestMatchesSource keeps the ground truth honest: every fact
// names a real file, every symbol is declared on its stated line, every trap in
// absent_symbols exists in source, and every named relation endpoint is itself
// an expected symbol.
func TestVueCorpusManifestMatchesSource(t *testing.T) {
	supported := map[string]bool{}
	for _, relation := range Capabilities().SupportedRelationTypes {
		supported[relation] = true
	}
	for _, app := range vueCorpusApps {
		t.Run(app, func(t *testing.T) {
			manifest := loadVueCorpusManifest(t, app)
			if manifest.App != app {
				t.Errorf("manifest app = %q, want %q", manifest.App, app)
			}
			sources := map[string][]string{}
			readLines := func(file string) ([]string, bool) {
				if lines, ok := sources[file]; ok {
					return lines, true
				}
				data, err := os.ReadFile(filepath.Join(vueCorpusRoot, app, filepath.FromSlash(file)))
				if err != nil {
					t.Errorf("%s: %v", file, err)
					return nil, false
				}
				lines := strings.Split(string(normalizeFixtureData(data)), "\n")
				sources[file] = lines
				return lines, true
			}
			declared := map[string]bool{}
			seen := map[string]bool{}
			for _, symbol := range manifest.Symbols {
				key := vueCorpusSymbolKey(symbol.File, symbol.Name, symbol.Kind)
				if seen[key] {
					t.Errorf("duplicate symbol %s", key)
				}
				seen[key] = true
				if !vueCorpusTags[symbol.Tag] {
					t.Errorf("%s: unknown tag %q", key, symbol.Tag)
				}
				declared[symbol.File+"#"+symbol.Name] = true
				declared[symbol.File+"#"+symbol.Name+":"+symbol.Kind] = true
				lines, ok := readLines(symbol.File)
				if !ok {
					continue
				}
				if symbol.Line < 1 || symbol.Line > len(lines) {
					t.Errorf("%s: line %d is outside the file (%d lines)", key, symbol.Line, len(lines))
					continue
				}
				// A .vue file's component and document symbols span the whole
				// file and take its base name, which no source line declares.
				base := strings.TrimSuffix(filepath.Base(symbol.File), ".vue")
				if strings.HasSuffix(symbol.File, ".vue") && symbol.Name == base && (symbol.Kind == "component" || symbol.Kind == "document") {
					if symbol.Line != 1 {
						t.Errorf("%s: a file-level %s symbol starts on line 1, not %d", key, symbol.Kind, symbol.Line)
					}
					continue
				}
				pattern := regexp.MustCompile(`(^|[^A-Za-z0-9_$])` + regexp.QuoteMeta(symbol.Name) + `([^A-Za-z0-9_$]|$)`)
				if !pattern.MatchString(lines[symbol.Line-1]) {
					t.Errorf("%s: line %d does not declare the name: %q", key, symbol.Line, lines[symbol.Line-1])
				}
			}
			for _, absent := range manifest.AbsentSymbols {
				if !vueCorpusTags[absent.Tag] || absent.Why == "" {
					t.Errorf("absent %s#%s needs a known tag and a why", absent.File, absent.Name)
				}
				// Without a kind, a trap would also match a real script symbol
				// that shares the attribute value's name.
				if absent.Tag == "false-elements" && absent.Kind != "element" {
					t.Errorf("absent %s#%s: false-elements traps must set kind \"element\"", absent.File, absent.Name)
				}
				lines, ok := readLines(absent.File)
				if ok && !strings.Contains(strings.Join(lines, "\n"), absent.Name) {
					t.Errorf("absent %s#%s: the name never appears in the file, so the trap is not real", absent.File, absent.Name)
				}
			}
			checkEndpoint := func(key string, endpoint vueCorpusEndpoint) {
				if (endpoint.External == "") == (endpoint.File == "") {
					t.Errorf("%s: endpoint needs exactly one of file or external", key)
					return
				}
				if endpoint.External != "" {
					return
				}
				if _, ok := readLines(endpoint.File); !ok {
					return
				}
				if endpoint.Name != "" && !declared[endpoint.File+"#"+endpoint.Name] {
					t.Errorf("%s: endpoint %s is not listed in symbols", key, endpoint.key())
				}
				if endpoint.Name != "" && endpoint.Kind != "" && !declared[endpoint.File+"#"+endpoint.Name+":"+endpoint.Kind] {
					t.Errorf("%s: endpoint %s has no symbols entry with that kind", key, endpoint.key())
				}
			}
			checkTypes := func(key string, types vueCorpusTypes) {
				if len(types) == 0 {
					t.Errorf("%s: missing type", key)
				}
				for _, relationType := range types {
					if !supported[relationType] {
						t.Errorf("%s: %q is not a supported relation type", key, relationType)
					}
				}
			}
			for _, relation := range manifest.Relations {
				key := relation.key()
				if seen[key] {
					t.Errorf("duplicate relation %s", key)
				}
				seen[key] = true
				if !vueCorpusTags[relation.Tag] {
					t.Errorf("%s: unknown tag %q", key, relation.Tag)
				}
				checkTypes(key, relation.Type)
				checkEndpoint(key, relation.From)
				checkEndpoint(key, relation.To)
			}
			for _, absent := range manifest.AbsentRelations {
				key := "absent " + absent.key()
				if seen[key] {
					t.Errorf("duplicate %s", key)
				}
				seen[key] = true
				if !vueCorpusTags[absent.Tag] || absent.Why == "" {
					t.Errorf("%s needs a known tag and a why", key)
				}
				checkTypes(key, absent.Type)
				checkEndpoint(key, absent.From)
				if absent.To != nil {
					checkEndpoint(key, *absent.To)
				}
			}
		})
	}
}

type vueCorpusTagScore struct {
	Found int `json:"found"`
	Total int `json:"total"`
}

type vueCorpusAppScore struct {
	Found      int                          `json:"found"`
	Total      int                          `json:"total"`
	Tags       map[string]vueCorpusTagScore `json:"tags"`
	FoundFacts []string                     `json:"found_facts"`
	Missing    map[string][]string          `json:"-"`
}

// vueCorpusFact is one manifest entry in checkable form. Key matches the keys
// recorded in score.json.
type vueCorpusFact struct {
	Tag    string
	Key    string
	Absent bool
	Types  []string           // nil for a symbol fact
	From   vueCorpusEndpoint  // the symbol itself for a symbol fact
	To     *vueCorpusEndpoint // nil for a symbol fact or a "to anything" absent relation
	Line   int                // declaring line of a symbol fact
}

func vueCorpusFactsOf(manifest vueCorpusManifest) []vueCorpusFact {
	var facts []vueCorpusFact
	for _, symbol := range manifest.Symbols {
		facts = append(facts, vueCorpusFact{
			Tag:  symbol.Tag,
			Key:  vueCorpusSymbolKey(symbol.File, symbol.Name, symbol.Kind),
			From: vueCorpusEndpoint{File: symbol.File, Name: symbol.Name, Kind: symbol.Kind},
			Line: symbol.Line,
		})
	}
	for _, absent := range manifest.AbsentSymbols {
		facts = append(facts, vueCorpusFact{
			Tag:    absent.Tag,
			Key:    "absent " + absent.File + "#" + absent.Name,
			Absent: true,
			From:   vueCorpusEndpoint{File: absent.File, Name: absent.Name, Kind: absent.Kind},
		})
	}
	for _, relation := range manifest.Relations {
		to := relation.To
		facts = append(facts, vueCorpusFact{
			Tag: relation.Tag, Key: "relation " + relation.key(), Types: relation.Type, From: relation.From, To: &to,
		})
	}
	for _, absent := range manifest.AbsentRelations {
		facts = append(facts, vueCorpusFact{
			Tag: absent.Tag, Key: "absent " + absent.key(), Absent: true, Types: absent.Type, From: absent.From, To: absent.To,
		})
	}
	return facts
}

// vueCorpusGraph indexes one provider snapshot for fact matching.
type vueCorpusGraph struct {
	symbolsByFile map[string][]SymbolRecord
	fileIDs       map[string]string
	externalIDs   map[string][]string
	relations     []RelationRecord
}

func buildVueCorpusGraph(t *testing.T, src string) vueCorpusGraph {
	t.Helper()
	dir := filepath.Join(t.TempDir(), filepath.Base(src))
	copyFixtureTree(t, src, dir)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), dir, "0.0.0-test", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	graph := vueCorpusGraph{
		symbolsByFile: map[string][]SymbolRecord{},
		fileIDs:       map[string]string{},
		externalIDs:   map[string][]string{},
		relations:     snapshot.Relations,
	}
	for _, symbol := range snapshot.Symbols {
		graph.symbolsByFile[symbol.FilePath] = append(graph.symbolsByFile[symbol.FilePath], symbol)
	}
	for _, file := range snapshot.Files {
		graph.fileIDs[file.Path] = file.ID
	}
	for _, external := range snapshot.Externals {
		graph.externalIDs[external.Value] = append(graph.externalIDs[external.Value], external.ID)
	}
	return graph
}

// endpointIDs resolves an endpoint to snapshot IDs. A positive line also
// requires the symbol's range to contain it.
func (g vueCorpusGraph) endpointIDs(endpoint vueCorpusEndpoint, line int) map[string]bool {
	ids := map[string]bool{}
	switch {
	case endpoint.External != "":
		for _, id := range g.externalIDs[endpoint.External] {
			ids[id] = true
		}
	case endpoint.Name == "":
		if id, ok := g.fileIDs[endpoint.File]; ok {
			ids[id] = true
		}
	default:
		for _, symbol := range g.symbolsByFile[endpoint.File] {
			if symbol.Name != endpoint.Name || (endpoint.Kind != "" && symbol.Kind != endpoint.Kind) {
				continue
			}
			if line > 0 && (line < symbol.StartLine || line > max(symbol.EndLine, symbol.StartLine)) {
				continue
			}
			ids[symbol.ID] = true
		}
	}
	return ids
}

// holds reports whether the fact is true of this graph.
func (g vueCorpusGraph) holds(fact vueCorpusFact) bool {
	if fact.Types == nil {
		line := fact.Line
		if fact.Absent {
			line = 0
		}
		present := len(g.endpointIDs(fact.From, line)) > 0
		return present != fact.Absent
	}
	types := map[string]bool{}
	for _, relationType := range fact.Types {
		types[relationType] = true
	}
	from := g.endpointIDs(fact.From, 0)
	var to map[string]bool
	if fact.To != nil {
		to = g.endpointIDs(*fact.To, 0)
	}
	emitted := false
	for _, candidate := range g.relations {
		if types[candidate.Type] && from[candidate.FromID] && (to == nil || to[candidate.ToID]) {
			emitted = true
			break
		}
	}
	return emitted != fact.Absent
}

func scoreVueCorpusApp(t *testing.T, app string) vueCorpusAppScore {
	t.Helper()
	manifest := loadVueCorpusManifest(t, app)
	graph := buildVueCorpusGraph(t, filepath.Join(vueCorpusRoot, app))
	score := vueCorpusAppScore{Tags: map[string]vueCorpusTagScore{}, Missing: map[string][]string{}}
	for _, fact := range vueCorpusFactsOf(manifest) {
		tagScore := score.Tags[fact.Tag]
		tagScore.Total++
		score.Total++
		if graph.holds(fact) {
			tagScore.Found++
			score.Found++
			score.FoundFacts = append(score.FoundFacts, fact.Key)
		} else {
			score.Missing[fact.Tag] = append(score.Missing[fact.Tag], fact.Key)
		}
		score.Tags[fact.Tag] = tagScore
	}
	sort.Strings(score.FoundFacts)
	return score
}

// TestVueCorpusScore ratchets corpus coverage. It fails when a previously found
// fact disappears, and also when new facts appear, so each improvement is
// recorded in score.json by rerunning with -update.
func TestVueCorpusScore(t *testing.T) {
	scorePath := filepath.Join(vueCorpusRoot, "score.json")
	got := map[string]vueCorpusAppScore{}
	for _, app := range vueCorpusApps {
		got[app] = scoreVueCorpusApp(t, app)
	}

	var report strings.Builder
	for _, app := range vueCorpusApps {
		score := got[app]
		fmt.Fprintf(&report, "%s: %d/%d facts\n", app, score.Found, score.Total)
		tags := make([]string, 0, len(score.Tags))
		for tag := range score.Tags {
			tags = append(tags, tag)
		}
		sort.Strings(tags)
		for _, tag := range tags {
			fmt.Fprintf(&report, "  %-20s %3d/%-3d\n", tag, score.Tags[tag].Found, score.Tags[tag].Total)
		}
	}
	t.Log("\n" + report.String())
	for _, app := range vueCorpusApps {
		// baseline facts are ones the provider emits today; losing one is a
		// regression, and listing one it never emitted is a manifest error.
		if missing := got[app].Missing["baseline"]; len(missing) > 0 {
			t.Errorf("%s: %d baseline facts are not emitted:\n  %s", app, len(missing), strings.Join(missing, "\n  "))
		}
		if os.Getenv("VUE_CORPUS_VERBOSE") != "" {
			tags := make([]string, 0, len(got[app].Missing))
			for tag := range got[app].Missing {
				tags = append(tags, tag)
			}
			sort.Strings(tags)
			for _, tag := range tags {
				t.Logf("%s missing [%s]:\n  %s", app, tag, strings.Join(got[app].Missing[tag], "\n  "))
			}
		}
	}

	if *updateGolden {
		data, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(scorePath, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(scorePath)
	if err != nil {
		t.Fatalf("read %s (run with -update to create): %v", scorePath, err)
	}
	var want map[string]vueCorpusAppScore
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("parse %s: %v", scorePath, err)
	}
	for _, app := range vueCorpusApps {
		before := map[string]bool{}
		for _, fact := range want[app].FoundFacts {
			before[fact] = true
		}
		now := map[string]bool{}
		for _, fact := range got[app].FoundFacts {
			now[fact] = true
		}
		var lost, gained []string
		for fact := range before {
			if !now[fact] {
				lost = append(lost, fact)
			}
		}
		for fact := range now {
			if !before[fact] {
				gained = append(gained, fact)
			}
		}
		sort.Strings(lost)
		sort.Strings(gained)
		if len(lost) > 0 {
			t.Errorf("%s regressed: %d previously found facts are missing:\n  %s", app, len(lost), strings.Join(lost, "\n  "))
		}
		if len(gained) > 0 {
			t.Errorf("%s improved by %d facts; record it with:\n\tgo test ./internal/sem -run TestVueCorpusScore -update\n  %s", app, len(gained), strings.Join(gained, "\n  "))
		}
		if want[app].Total != got[app].Total {
			t.Errorf("%s manifest changed (%d facts, score.json has %d); rerun with -update", app, got[app].Total, want[app].Total)
		}
	}
}
