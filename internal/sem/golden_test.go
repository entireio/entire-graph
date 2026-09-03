package sem

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestStreamSnapshotOrderIsDeterministic confirms that streaming a fixed input
// twice produces a byte-identical record sequence — file, symbol, and relation
// order are all stable. It stresses the orderings that derive from Go maps
// (a caller invoking several functions; a subclass overriding several methods).
func TestStreamSnapshotOrderIsDeterministic(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "calls.go", `package p

func driver() {
	alpha()
	bravo()
	charlie()
	delta()
}

func alpha()   {}
func bravo()   {}
func charlie() {}
func delta()   {}
`)
	// Several parameters forwarded into the same callee: the relation identity is
	// one DATA_FLOWS edge, but multiple flows compete to supply its evidence.
	writeFile(t, repo, "flows.go", `package p

func forwardAll(alpha string, bravo string, charlie string) string {
	return sink(alpha, bravo, charlie)
}

func sink(a string, b string, c string) string { return a + b + c }
`)
	writeFile(t, repo, "Animals.java", `class Animal {
	String describe() { return "animal"; }
	String sound() { return "?"; }
	String legs() { return "4"; }
}

class Dog extends Animal {
	String describe() { return "dog"; }
	String sound() { return "woof"; }
	String legs() { return "4"; }
}
`)

	capture := func() string {
		var buf bytes.Buffer
		err := StreamSnapshot(t.Context(), repo, "v", ProviderSnapshotOptions{Worktree: true}, func(rec any) error {
			data, marshalErr := json.Marshal(rec)
			if marshalErr != nil {
				return marshalErr
			}
			buf.Write(data)
			buf.WriteByte('\n')
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}

	// Repeat: a single second run can match by luck when only two orderings
	// exist, so sample enough runs to make a map-order leak surface.
	first := capture()
	for i := 0; i < 8; i++ {
		if next := capture(); next != first {
			t.Fatalf("stream output is not deterministic across runs (run %d differs)", i+2)
		}
	}
	// Sanity: the fixture actually produced the relation kinds we want stable.
	if !strings.Contains(first, `"type":"CALLS"`) || !strings.Contains(first, `"type":"OVERRIDES"`) {
		t.Fatalf("fixture did not exercise CALLS/OVERRIDES ordering")
	}
	if !strings.Contains(first, `"type":"DATA_FLOWS"`) {
		t.Fatalf("fixture did not exercise DATA_FLOWS evidence ordering")
	}
}

// TestReturnFlowCallsOrderIsTotal guards the ordering that decides which flow
// supplies a DATA_FLOWS relation's evidence. Several parameters forwarded into
// one callee produce flows that share a name and evidence kind and differ only
// in detail; the relation dedupe keeps the first, so a comparator that leaves
// them tied would let Go's map iteration pick the evidence payload.
func TestReturnFlowCallsOrderIsTotal(t *testing.T) {
	block := "func handler(alpha string, bravo string, charlie string) string {\n\treturn forward(alpha, bravo, charlie)\n}\n"
	params := map[string]bool{"alpha": true, "bravo": true, "charlie": true}

	first := returnFlowCalls(block, params)
	forwards := 0
	for _, flow := range first {
		if flow.EvidenceKind == "argument_forward_flow" && flow.Name == "forward" {
			forwards++
		}
	}
	if forwards < 3 {
		t.Fatalf("fixture produced %d competing argument forward flows, want 3", forwards)
	}

	for i := 0; i < 200; i++ {
		got := returnFlowCalls(block, params)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("returnFlowCalls order varies across calls: run %d got %+v want %+v", i+2, got, first)
		}
	}
}

// TestSnapshotFormatsAreByteDeterministicWhenRelationsDeduplicate exercises
// the streaming dedup boundary with several DATA_FLOWS candidates that share
// one public relation identity. The chosen evidence must be canonical, not the
// first value encountered while ranging a Go map, because both native and
// first-seen-dictionary compact output inherit that choice byte for byte.
func TestSnapshotFormatsAreByteDeterministicWhenRelationsDeduplicate(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.go", `package flow

func sink(alpha, bravo, charlie, delta, echo, foxtrot, golf, hotel int) {}

func caller(alpha, bravo, charlie, delta, echo, foxtrot, golf, hotel int) {
	sink(alpha, bravo, charlie, delta, echo, foxtrot, golf, hotel)
}
`)

	type capture struct {
		bytes        []byte
		hash         string
		summary      SnapshotSummary
		flowEvidence string
	}
	captureFormat := func(t *testing.T, compact bool) capture {
		t.Helper()
		var out bytes.Buffer
		var encode func(any) error
		if compact {
			encode = NewCompactSnapshotEncoder(&out).Encode
		} else {
			encoder := json.NewEncoder(&out)
			encoder.SetEscapeHTML(false)
			encode = encoder.Encode
		}
		hasher := NewSnapshotSemanticHasher()
		var summary SnapshotSummary
		var flowEvidence string
		err := StreamSnapshot(t.Context(), repo, "determinism-test", ProviderSnapshotOptions{Worktree: true, Profile: ProfileFull}, func(record any) error {
			switch typed := record.(type) {
			case SnapshotSummary:
				summary = typed
			case RelationRecord:
				if typed.Type == "DATA_FLOWS" && lastSegment(typed.FromID) == "caller" && lastSegment(typed.ToID) == "sink" && len(typed.Evidence) == 1 {
					flowEvidence = typed.Evidence[0].Detail
				}
			}
			if err := hasher.Add(record); err != nil {
				return err
			}
			return encode(record)
		})
		if err != nil {
			t.Fatal(err)
		}
		return capture{bytes: append([]byte(nil), out.Bytes()...), hash: hasher.SumHex(), summary: summary, flowEvidence: flowEvidence}
	}

	for _, testCase := range []struct {
		name    string
		compact bool
	}{
		{name: "native", compact: false},
		{name: "compact", compact: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			first := captureFormat(t, testCase.compact)
			if first.summary.Stats.Symbols != 2 || first.summary.Stats.Relations == 0 {
				t.Fatalf("fixture stats = %#v, want 2 symbols and at least one relation", first.summary.Stats)
			}
			if first.flowEvidence != "alpha -> sink()" {
				t.Fatalf("canonical DATA_FLOWS evidence = %q, want %q", first.flowEvidence, "alpha -> sink()")
			}
			for run := 2; run <= 32; run++ {
				next := captureFormat(t, testCase.compact)
				if !reflect.DeepEqual(next.summary, first.summary) {
					t.Fatalf("run %d summary changed\n got=%#v\nwant=%#v", run, next.summary, first.summary)
				}
				if next.hash != first.hash {
					t.Fatalf("run %d semantic hash = %s, want %s", run, next.hash, first.hash)
				}
				if !bytes.Equal(next.bytes, first.bytes) {
					t.Fatalf("run %d output differs from run 1", run)
				}
			}
		})
	}
}

// TestStreamSnapshotStreamsIncrementally proves the streaming contract: a lean
// header is emitted first (before parsing finishes), file and symbol records are
// emitted before relation resolution produces any relation, and a trailing
// summary carries the totals the lean header omits. This is what makes the path
// memory-bounded: nothing waits for the whole repo to be parsed.
func TestStreamSnapshotStreamsIncrementally(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "go-fields")
	copyFixtureTree(t, filepath.Join("testdata", "fixtures", "go-fields"), dir)

	var header SnapshotHeader
	var summary SnapshotSummary
	haveHeader, haveSummary := false, false
	firstFile, firstSymbol, firstRelation := -1, -1, -1
	index := 0
	err := StreamSnapshot(t.Context(), dir, "0.0.0-test", ProviderSnapshotOptions{Worktree: true}, func(rec any) error {
		switch r := rec.(type) {
		case SnapshotHeader:
			header, haveHeader = r, true
			if index != 0 {
				t.Fatalf("header must be the first record, was at %d", index)
			}
		case FileRecord:
			if firstFile < 0 {
				firstFile = index
			}
		case SymbolRecord:
			if firstSymbol < 0 {
				firstSymbol = index
			}
		case RelationRecord:
			if firstRelation < 0 {
				firstRelation = index
			}
		case SnapshotSummary:
			summary, haveSummary = r, true
		}
		index++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Lean header: emitted before the repo is fully processed, so its totals are
	// empty and reported in the summary instead.
	if !haveHeader || len(header.Languages) != 0 || header.Stats.Relations != 0 {
		t.Fatalf("expected a lean header (empty languages, zero relation stat): %#v", header)
	}
	// File and symbol records must precede relation resolution.
	if firstFile < 0 || firstSymbol < 0 || firstRelation < 0 {
		t.Fatalf("missing record kinds: file=%d symbol=%d relation=%d", firstFile, firstSymbol, firstRelation)
	}
	if firstFile >= firstRelation || firstSymbol >= firstRelation {
		t.Fatalf("file/symbol records must stream before relations: file=%d symbol=%d relation=%d", firstFile, firstSymbol, firstRelation)
	}
	// Summary carries the totals the lean header omitted.
	if !haveSummary || len(summary.Languages) == 0 || summary.Stats.Relations == 0 || summary.Stats.Symbols == 0 {
		t.Fatalf("summary must carry languages and stats: %#v", summary)
	}
}

// TestStreamSnapshotCompactRoundTrip keeps the compact consumer tied to the
// public provider stream: the artifact must be deterministic, queryable, and
// semantically identical after decoding.
func TestStreamSnapshotCompactRoundTrip(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "main.go", `package sample

func caller() { callee() }
func callee() {}
`)
	var records []any
	err := StreamSnapshot(t.Context(), repo, "compact-test", ProviderSnapshotOptions{Worktree: true}, func(record any) error {
		records = append(records, record)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	encode := func() []byte {
		var out bytes.Buffer
		encoder := NewCompactSnapshotEncoder(&out)
		for _, record := range records {
			if err := encoder.Encode(record); err != nil {
				t.Fatal(err)
			}
		}
		return out.Bytes()
	}
	first, second := encode(), encode()
	if !bytes.Equal(first, second) {
		t.Fatal("compact stream is not deterministic")
	}
	index, err := LoadCompactSnapshot(bytes.NewReader(first))
	if err != nil {
		t.Fatal(err)
	}
	decoded := make([]any, 0, len(records))
	if err := DecodeCompactSnapshot(bytes.NewReader(first), func(record any) error {
		decoded = append(decoded, record)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := publicRecordJSON(t, decoded), publicRecordJSON(t, records); !reflect.DeepEqual(got, want) {
		t.Fatalf("compact round trip changed public records\n got=%s\nwant=%s", got, want)
	}
	if got, want := index.CanonicalSemanticHash, hashRecords(t, records); got != want {
		t.Fatalf("compact semantic hash = %s, want %s", got, want)
	}
	if len(index.Snapshot.Symbols) == 0 {
		t.Fatal("fixture produced no symbols")
	}
	if got := index.Query(CompactSnapshotQuery{Symbol: index.Snapshot.Symbols[0].ID}).Symbols; len(got) != 1 {
		t.Fatalf("exact symbol query returned %#v", got)
	}
	if len(index.Snapshot.Relations) == 0 {
		t.Fatal("fixture produced no relations")
	}
	relation := index.Snapshot.Relations[0]
	if got := index.Query(CompactSnapshotQuery{FromID: relation.FromID, Relation: relation.Type}).Relations; len(got) == 0 {
		t.Fatalf("exact relation query returned %#v", got)
	}
}

// updateGolden regenerates the committed NDJSON baselines instead of asserting
// against them. Run:
//
//	go test ./internal/sem -run TestProviderGoldenSnapshots -update
var updateGolden = flag.Bool("update", false, "regenerate golden NDJSON baseline files")

// goldenFixtures enumerates the fixture repos under testdata/fixtures. Each
// fixture is a self-contained source tree; the harness snapshots it in worktree
// mode and compares the full NDJSON stream against a committed baseline. The
// baselines are the machine-readable record of the current provider contract,
// so any change to symbols, relations, externals, or header stats shows up as a
// golden diff in review.
//
// Adding a fixture is just dropping a directory under testdata/fixtures, listing
// its name here, and running the test with -update to create the baseline.
var goldenFixtures = []string{
	"csharp-basic",
	// julia-r-basic exists so the capability contract test covers Julia and R.
	// Both resolve calls and both were advertised as inventory-grade; with no
	// fixture in either language the guard could not have seen it.
	"julia-r-basic",
	// multilang-relations covers the semantic languages whose relation support
	// the capability matrix under-reported (Zig, C, Kotlin, Ruby): each emits
	// call, type and data-flow edges the matrix did not declare.
	"multilang-relations",
	"csharp-fields",
	"csharp-oo",
	"go-basic",
	"go-async",
	"go-clones",
	"go-fields",
	"go-tests",
	"go-types",
	"java-basic",
	"java-fields",
	"java-oo",
	"php-basic",
	"php-oo",
	"python-basic",
	"python-imports",
	"python-oo",
	"rust-basic",
	"rust-oo",
	"terraform-basic",
	"typescript-basic",
	"typescript-events",
	"typescript-fields",
	"typescript-http",
	"typescript-imports",
	"typescript-oo",
	"services-config",
}

func TestProviderGoldenSnapshots(t *testing.T) {
	for _, name := range goldenFixtures {
		t.Run(name, func(t *testing.T) {
			got := buildFixtureNDJSON(t, name)
			goldenPath := filepath.Join("testdata", "fixtures", name+".ndjson.golden")
			if *updateGolden {
				if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden (run with -update to create): %v", err)
			}
			if got != normalizeSnapshotText(string(want)) {
				t.Fatalf("snapshot for %s does not match golden; run:\n\tgo test ./internal/sem -run TestProviderGoldenSnapshots -update\n\n--- got ---\n%s", name, got)
			}
		})
	}
}

type goldenFixtureCoverage struct {
	FixtureCount  int            `json:"fixture_count"`
	FileLanguages map[string]int `json:"file_languages"`
	SymbolKinds   map[string]int `json:"symbol_kinds"`
	RelationTypes map[string]int `json:"relation_types"`
}

func TestProviderGoldenFixtureQualityCoverageReport(t *testing.T) {
	got := goldenFixtureCoverage{
		FixtureCount:  len(goldenFixtures),
		FileLanguages: map[string]int{},
		SymbolKinds:   map[string]int{},
		RelationTypes: map[string]int{},
	}
	for _, name := range goldenFixtures {
		goldenPath := filepath.Join("testdata", "fixtures", name+".ndjson.golden")
		data, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			var record struct {
				RecordType string `json:"record_type"`
				Language   string `json:"language"`
				Kind       string `json:"kind"`
				Type       string `json:"type"`
			}
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatalf("parse golden %s: %v\n%s", name, err, line)
			}
			switch record.RecordType {
			case "file":
				if record.Language != "" {
					got.FileLanguages[record.Language]++
				}
			case "symbol":
				if record.Kind != "" {
					got.SymbolKinds[record.Kind]++
				}
			case "relation":
				if record.Type != "" {
					got.RelationTypes[record.Type]++
				}
			}
		}
	}
	reportPath := filepath.Join("testdata", "fixtures", "quality_coverage.json")
	var want goldenFixtureCoverage
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read quality coverage report: %v", err)
	}
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("parse quality coverage report: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		t.Fatalf("golden fixture quality coverage drifted; update %s\n%s", reportPath, gotJSON)
	}
}

// buildFixtureNDJSON copies a fixture into an isolated temp directory (outside
// any git tree, so repo_key resolves to a stable local/<name>), snapshots it in
// worktree mode, and returns the normalized NDJSON stream. Worktree mode avoids
// a machine-specific git error in the no-HEAD warning detail, leaving repo_root
// as the only path-dependent field, which is normalized below.
func buildFixtureNDJSON(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("testdata", "fixtures", name)
	dir := filepath.Join(t.TempDir(), name)
	copyFixtureTree(t, src, dir)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), dir, "0.0.0-test", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteSnapshotNDJSON(&buf, snapshot); err != nil {
		t.Fatal(err)
	}
	return normalizeSnapshotRoot(buf.String(), dir)
}

func normalizeSnapshotRoot(snapshot, dir string) string {
	snapshot = normalizeSnapshotText(snapshot)
	snapshot = strings.ReplaceAll(snapshot, dir, "<repo>")
	encoded, err := json.Marshal(dir)
	if err != nil {
		return snapshot
	}
	escaped := strings.Trim(string(encoded), `"`)
	return strings.ReplaceAll(snapshot, escaped, "<repo>")
}

func normalizeSnapshotText(snapshot string) string {
	snapshot = strings.ReplaceAll(snapshot, "\r\n", "\n")
	return strings.ReplaceAll(snapshot, "\r", "\n")
}

func copyFixtureTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		data = normalizeFixtureData(data)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func normalizeFixtureData(data []byte) []byte {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(data, []byte("\r"), []byte("\n"))
}

// TestCapabilityMatrixCoversEmittedRelations is the contract behind
// `capabilities --json`: AGENTS.md tells agents to feature-detect with it
// before trusting a language, so every relation the provider actually emits for
// a language must be declared — either per-language in
// RelationSupportByLanguage, or as one of the globally-heuristic types that are
// deliberately not attributed per language.
//
// The check runs in one direction only. Absence of a relation in a fixture is
// not evidence the language cannot produce it (a fixture simply may not contain
// the construct), so an advertised-but-unseen relation is fine. An emitted-but-
// undeclared relation is not: that is the provider doing something it told
// agents it could not do.
//
// This is the guard for two defects it would have caught: CONSTRUCTS was
// emitted for every call-capable language and declared for none, and Julia/R
// emitted CALLS (pinned by their own tests) while advertising inventory-grade
// support only.
func TestCapabilityMatrixCoversEmittedRelations(t *testing.T) {
	capabilities := Capabilities()
	global := map[string]bool{}
	for _, relation := range capabilities.HeuristicRelationTypes {
		global[relation] = true
	}
	declared := map[string]map[string]bool{}
	for language, relations := range capabilities.RelationSupportByLanguage {
		set := make(map[string]bool, len(relations))
		for _, relation := range relations {
			set[relation] = true
		}
		declared[language] = set
	}

	type undeclared struct{ language, relation, fixture string }
	var violations []undeclared
	seen := map[string]bool{}
	for _, fixture := range goldenFixtures {
		for _, line := range strings.Split(buildFixtureNDJSON(t, fixture), "\n") {
			if !strings.Contains(line, `"record_type":"relation"`) {
				continue
			}
			var record struct {
				FromID string `json:"from_id"`
				Type   string `json:"type"`
			}
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatalf("%s: parse relation: %v", fixture, err)
			}
			// from_id is repoKey:language:path:kind:name for symbols and
			// repoKey:file:path for files; only the former carries a language.
			parts := strings.Split(record.FromID, ":")
			if len(parts) < 3 || parts[1] == "file" {
				continue
			}
			language := parts[1]
			if global[record.Type] || declared[language][record.Type] {
				continue
			}
			key := language + "\x00" + record.Type
			if seen[key] {
				continue
			}
			seen[key] = true
			violations = append(violations, undeclared{language, record.Type, fixture})
		}
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].language != violations[j].language {
			return violations[i].language < violations[j].language
		}
		return violations[i].relation < violations[j].relation
	})
	for _, violation := range violations {
		t.Errorf("%s emits %s (fixture %s) but capabilities declares neither it per-language nor as a global heuristic",
			violation.language, violation.relation, violation.fixture)
	}
}

// TestImportCapabilityMatchesScannerRegistry pins the property that replaced a
// hand-maintained mirror: every extension with an import scanner is reported as
// import-capable, and every extension without one is not. The two used to be
// separate switch statements kept in sync by a comment.
func TestImportCapabilityMatchesScannerRegistry(t *testing.T) {
	for extension := range importScanners {
		if !importCapableExtension(extension) {
			t.Errorf("%s has an import scanner but is not reported import-capable", extension)
		}
		// Empty content must yield no imports rather than panicking: the scanners
		// run over whatever the reader returns, including a file it could not read.
		if got := importsFor("file"+extension, ""); len(got) != 0 {
			t.Errorf("%s scanner returned %v for empty content", extension, got)
		}
	}
	// Extensions that parse but carry no imports must stay non-capable; a
	// nil-returning scanner entry would silently make them capable.
	for _, extension := range []string{".hcl", ".tf", ".tfvars", ".sql", ".yaml", ".md", ".json"} {
		if importCapableExtension(extension) {
			t.Errorf("%s has no import scanner but is reported import-capable", extension)
		}
		if got := importsFor("file"+extension, "import x\nuse y;\n#include <z>\n"); len(got) != 0 {
			t.Errorf("%s returned imports %v despite having no scanner", extension, got)
		}
	}
}

// TestHeuristicRelationTypesMatchDocumentation pins the active semantic
// provider requirements to the executable heuristic list. The two had drifted:
// older prose named HANDLES_GRPC, HANDLES_GRAPHQL, HANDLES_TRPC and CONFIGURES
// as globally heuristic while all four are attributed per language.
func TestHeuristicRelationTypesMatchDocumentation(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "semantic-provider-requirements.md"))
	if err != nil {
		t.Fatalf("read semantic provider requirements: %v", err)
	}
	const marker = "`heuristic_relation_types` ("
	index := bytes.Index(doc, []byte(marker))
	if index < 0 {
		t.Fatalf("semantic provider requirements no longer document heuristic_relation_types")
	}
	rest := doc[index+len(marker):]
	end := bytes.IndexByte(rest, ')')
	if end < 0 {
		t.Fatalf("heuristic_relation_types list is unterminated")
	}
	documented := map[string]bool{}
	for _, field := range strings.Split(string(rest[:end]), ",") {
		if name := strings.Trim(strings.TrimSpace(field), "`"); name != "" {
			documented[name] = true
		}
	}
	actual := map[string]bool{}
	for _, relation := range Capabilities().HeuristicRelationTypes {
		actual[relation] = true
	}
	for relation := range documented {
		if !actual[relation] {
			t.Errorf("docs list %s as globally heuristic but capabilities does not", relation)
		}
	}
	for relation := range actual {
		if !documented[relation] {
			t.Errorf("capabilities reports %s as globally heuristic but the docs omit it", relation)
		}
	}
}

// TestFSharpModuleDoesNotClaimItsMembersCalls pins who owns a pipeline call.
//
// An F# `module` block spans every binding under it, so scanning the module's own text
// sees the pipelines written inside its FUNCTIONS. Those calls belong to the functions,
// which emit them already, and crediting the module produced a second edge from a symbol
// that never made the call: `B.run`'s `v |> helper` appeared as both `run -> helper` and
// `B -> helper`. Impact on `helper` then named a module that does not call it.
//
// The real edge is asserted alongside because the fix withdraws candidates, and a
// withdrawal that also drops the true edge has traded one wrong answer for another.
func TestFSharpModuleDoesNotClaimItsMembersCalls(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	for name, content := range map[string]string{
		"a.fs": "module A\nlet helper x = x + 1\n",
		"b.fs": "module B\nlet run v =\n    v |> helper\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, symbol := range snapshot.Symbols {
		symbolsByID[symbol.ID] = symbol
	}

	pipeline := 0
	for _, relation := range snapshot.Relations {
		if relation.Type != "CALLS" {
			continue
		}
		from, to := symbolsByID[relation.FromID], symbolsByID[relation.ToID]
		if from.Language == "F#" && from.Kind == "module" {
			t.Errorf("module %s credited with calling %s; the call belongs to the binding that wrote it", from.Name, to.Name)
		}
		if from.Name == "run" && to.Name == "helper" {
			pipeline++
		}
	}
	if pipeline != 1 {
		t.Fatalf("the real pipeline edge run -> helper was withdrawn too (found %d)", pipeline)
	}
}

// TestFSharpBlockCommentDoesNotFabricateACall pins that a commented pipeline is not a call.
//
// The generic literal/comment stripper does not know F#'s `(* ... *)` form, so a commented
// pipeline reached the F# scanners and emitted a CALLS edge to a function the code does
// not call. Commented-out code is the shape most likely to contain a call, which is what
// makes the fabrication easy to hit.
//
// The nested case is here because F# block comments nest: a single-pass strip ends at the
// first `*)` and exposes the tail of the outer comment, which is a call again.
func TestFSharpBlockCommentDoesNotFabricateACall(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name   string
		source string
	}{
		{"a commented pipeline", "let run v =\n    (* xs |> helper *)\n    v |> other\n"},
		{"nested block comments", "let run v =\n    (* outer (* inner |> hidden *) tail |> alsoHidden *)\n    v |> other\n"},
		// A STRING is not code: an unmatched `(*` inside one used to open a comment
		// that never closed, blanking every real pipeline after it.
		{"a comment opener inside a string literal", "let marker = \"(*\"\nlet run v =\n    v |> other\n"},
		{"an escaped quote does not end the string", "let m = \"a\\\"(*\"\nlet run v =\n    v |> other\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			targets := fsharpCallTargets(maskFSharpBlockComments(stripCodeLiteralsAndComments(testCase.source)))
			for _, commented := range []string{"helper", "hidden", "alsoHidden"} {
				if _, fabricated := targets[commented]; fabricated {
					t.Errorf("a commented call %q was scanned as a real one: %v", commented, targets)
				}
			}
			if _, ok := targets["other"]; !ok {
				t.Fatalf("the real pipeline call was masked away too: %v", targets)
			}
		})
	}
}

// TestFSharpQuoteThatIsNotAStringDoesNotStrandTheMasker pins that a `"` which is
// not a string delimiter cannot flip the F# comment masker's string state.
//
// maskFSharpBlockComments runs on RAW source -- the generic stripper runs after
// it, inside the scanners -- so it is the pass that meets `// prose with a "` and
// F#'s `'"'` character literal first. It counted both as string openers, and
// because an F# string may legally span newlines nothing closed them again, so
// everything after was read in the wrong state. That went wrong in both
// directions: a `(* v |> helper *)` below such a line was left unmasked and
// became a CALLS edge to a function the code does not call, and a later `"(*"`
// re-synchronised the state the other way, opening a comment that never closed
// and blanking every genuine pipeline to the end of the block.
func TestFSharpQuoteThatIsNotAStringDoesNotStrandTheMasker(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name       string
		source     string
		fabricated []string
	}{
		{"a quote in a line comment", "let run v =\n    // the marker is a \" character\n    (* v |> helper *)\n    v |> other\n", []string{"helper"}},
		{"a quote character literal", "let run v =\n    let quote = '\"'\n    (* v |> helper *)\n    v |> other\n", []string{"helper"}},
		// The other direction: the stranded state closes on the NEXT quote, and
		// the `(*` behind it then opens a comment that runs to the end of the
		// block, silently deleting the real call.
		{"a comment opener re-synchronising after a line comment", "let run v =\n    // a \" here\n    let s = \"(*\"\n    v |> other\n", nil},
		{"a comment opener re-synchronising after a character literal", "let run v =\n    let quote = '\"'\n    let s = \"(*\"\n    v |> other\n", nil},
		// Guards on the two new cases. A generic type parameter and a primed
		// identifier are spelled with the same apostrophe as a character
		// literal and must be left exactly as they were.
		{"a generic type parameter is not a character literal", "let run (v: 'T) =\n    (* v |> helper *)\n    v |> other\n", []string{"helper"}},
		{"a primed identifier is not a character literal", "let run v =\n    let v' = v\n    (* v' |> helper *)\n    v' |> other\n", []string{"helper"}},
		// A line comment is only a line comment outside a string and outside a
		// block comment: inside either it is ordinary text, and treating it as
		// a comment would hide the `(*` or the closing `*)` after it.
		{"a line comment inside a string literal", "let marker = \"// (*\"\nlet run v =\n    v |> other\n", nil},
		{"a line comment inside a block comment", "let run v =\n    (* // v |> helper *)\n    v |> other\n", []string{"helper"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			// The production composition order: the masker sees raw source, and
			// the scanners strip literals and line comments afterwards.
			targets := fsharpCallTargets(maskFSharpBlockComments(testCase.source))
			for _, commented := range testCase.fabricated {
				if _, ok := targets[commented]; ok {
					t.Errorf("a commented call %q was scanned as a real one: %v", commented, targets)
				}
			}
			if _, ok := targets["other"]; !ok {
				t.Fatalf("the real pipeline call was masked away: %v", targets)
			}
		})
	}
}

// TestFSharpTripleQuotedStringDoesNotStrandTheMasker pins that a `"""..."""`
// string is read as ONE literal.
//
// A triple-quoted string is F#'s raw form: it exists precisely to hold
// unescaped quotes, and it ends only at the next `"""`. The masker ended it at
// the first internal quote, so the remainder of the literal was read as code --
// `let doc = """a " (* b"""` opened a block comment that never closed, and every
// genuine pipeline after it in the block was blanked away and its CALLS edge
// silently lost.
func TestFSharpTripleQuotedStringDoesNotStrandTheMasker(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name       string
		source     string
		fabricated []string
	}{
		// The defect: an internal quote ends the literal early and the `(*`
		// behind it swallows the rest of the block.
		{"an unescaped quote inside a triple-quoted string", "let run v =\n    let doc = \"\"\"a \" (* b\"\"\"\n    v |> other\n", nil},
		// A `(*` inside the literal is text, not a comment opener.
		{"a comment opener inside a triple-quoted string", "let doc = \"\"\"(*\"\"\"\nlet run v =\n    v |> other\n", nil},
		// ... and a `*)` inside one must not close a comment that is open.
		{"a triple-quoted string after a real comment", "let run v =\n    (* v |> helper *)\n    let doc = \"\"\"x\"\"\"\n    v |> other\n", []string{"helper"}},
		// A backslash is NOT an escape in a raw string, so it cannot consume
		// the quote that ends it.
		{"a backslash before the closing quotes", "let doc = \"\"\"a\\\"\"\"\nlet run v =\n    v |> other\n", nil},
		// Guards. An empty ordinary string is two quotes, not the start of a
		// raw one, and `@"""x"""` is a VERBATIM string whose `""` are escaped
		// quotes -- reading either as triple-quoted would swallow the code
		// after it.
		{"an empty string is not a triple-quoted opener", "let e = \"\"\nlet run v =\n    v |> other\n", nil},
		{"a verbatim string is not a triple-quoted one", "let m = @\"\"\"a\"\"\"\nlet run v =\n    v |> other\n", nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			// The production composition order: the masker sees raw source.
			targets := fsharpCallTargets(maskFSharpBlockComments(testCase.source))
			for _, commented := range testCase.fabricated {
				if _, ok := targets[commented]; ok {
					t.Errorf("a commented call %q was scanned as a real one: %v", commented, targets)
				}
			}
			if _, ok := targets["other"]; !ok {
				t.Fatalf("the real pipeline call was masked away: %v", targets)
			}
		})
	}
}

// TestFSharpLiteralFormsTheGenericStripperDoesNotModel pins that the F# call
// scanners read F#'s own literal forms, not the generic thirty-language ones.
//
// The scanners masked their input with stripCodeLiteralsAndComments, which
// knows one string form: quote, backslash escapes, ends at the closing quote or
// the line break. F# has three more, and each one broke a different way.
//
// A TRIPLE-QUOTED string is raw: it ends only at the next `"""`, and the
// unescaped quotes it exists to hold are content. Pairing quotes two at a time
// ended it at the first internal one and left the rest of the literal standing
// as code, so `let s = """a " x |> helper """` fabricated a CALLS edge to
// `helper` -- a function the file never calls.
//
// A VERBATIM string spells an escaped quote `""` and takes no backslash escape,
// so a backslash in front of one (`@"a\"" |> helper"`) was consumed as an escape
// that is not there, shifting the pairing by one quote and exposing the tail of
// the literal the same way.
//
// Both of those forms, and F#'s ordinary strings, may SPAN NEWLINES. The generic
// stripper abandons a string at the first line break, so everything a multi-line
// literal covered below its opening line was read as code.
//
// And the apostrophe is not only a character literal in F#: it opens a generic
// type parameter (`'T`) and may end an identifier (`f'`). Treating the first one
// as a literal opener closed it on the next apostrophe on the line and blanked
// the real code between them, so `let run (xs: 'T list) = xs |> other 'a'` lost
// its call to `other` altogether.
func TestFSharpLiteralFormsTheGenericStripperDoesNotModel(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name       string
		source     string
		fabricated []string
		want       []string
	}{
		// The four reproduced defects.
		{
			"an unescaped quote inside a triple-quoted string",
			"let s = \"\"\"a \" x |> helper \"\"\"\nlet run v = v |> other\n",
			[]string{"helper"}, []string{"other"},
		},
		{
			"a triple-quoted string spanning newlines",
			"let sql = \"\"\"\nselect x |> helper\n\"\"\"\nlet run v = v |> other\n",
			[]string{"helper"}, []string{"other"},
		},
		{
			"a verbatim string spanning newlines",
			"let m = @\"a\nb |> helper\"\nlet run v = v |> other\n",
			[]string{"helper"}, []string{"other"},
		},
		{
			"a backslash before an escaped quote in a verbatim string",
			"let m = @\"a\\\"\" |> helper\"\nlet run v = v |> other\n",
			[]string{"helper"}, []string{"other"},
		},
		// The same defect deleting a real call rather than inventing one: `'T`
		// is a type parameter, and the literal it opened swallowed the pipeline
		// up to the next apostrophe.
		{
			"a generic type parameter before a character literal",
			"let run (xs: 'T list) = xs |> other 'a'\n",
			[]string{"helper"}, []string{"other"},
		},
		// The dotted scanner shares the masker and the defect.
		{
			"a dotted call inside a triple-quoted string",
			"let s = \"\"\"a \" A.helper(x) \"\"\"\nlet run v = B.other(v)\n",
			[]string{"helper"}, []string{"other"},
		},
		// Guards. These forms were already read correctly and must stay so: a
		// plain raw string, a verbatim string with a proper `""` escape, a
		// quote character literal, a line comment, and a primed identifier.
		{
			"a pipe inside a plain triple-quoted string",
			"let s = \"\"\"x |> helper\"\"\"\nlet run v = v |> other\n",
			[]string{"helper"}, []string{"other"},
		},
		{
			"a pipe inside a verbatim string with an escaped quote",
			"let m = @\"a\"\" |> helper\"\nlet run v = v |> other\n",
			[]string{"helper"}, []string{"other"},
		},
		{
			"a quote character literal",
			"let c = '\"'\nlet s = \"x |> helper\"\nlet run v = v |> other\n",
			[]string{"helper"}, []string{"other"},
		},
		{
			"a pipe in a line comment",
			"// x |> helper\nlet run v = v |> other\n",
			[]string{"helper"}, []string{"other"},
		},
		{
			"a primed identifier",
			"let run' v = v |> other\n",
			[]string{"helper"}, []string{"other"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			// The production composition order: the masker sees raw source, and
			// the scanners mask literals themselves afterwards.
			targets := fsharpCallTargetNames(fsharpCallTargets(maskFSharpBlockComments(testCase.source)))
			for _, invented := range testCase.fabricated {
				if _, ok := targets[invented]; ok {
					t.Errorf("a call inside a literal, %q, was scanned as a real one: %v", invented, targets)
				}
			}
			for _, real := range testCase.want {
				if _, ok := targets[real]; !ok {
					t.Errorf("the real call %q was blanked away with the literal: %v", real, targets)
				}
			}
		})
	}
}

// TestFSharpLiteralFormsCorruptTheCallGraph is the same defect at the graph
// level: the fabricated edge and the deleted one both reach CALLS.
func TestFSharpLiteralFormsCorruptTheCallGraph(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "src/A.fs", "module A\n"+
		"\n"+
		"let helper (value: int) = value + 1\n"+
		"\n"+
		"let other (value: int) = value * 2\n"+
		"\n"+
		"let raw (v: int) =\n"+
		"    let doc = \"\"\"a \" v |> helper \"\"\"\n"+
		"    v |> other\n"+
		"\n"+
		"let spanning (v: int) =\n"+
		"    let sql = \"\"\"\n"+
		"select v |> helper\n"+
		"\"\"\"\n"+
		"    v |> other\n"+
		"\n"+
		"let verbatim (v: int) =\n"+
		"    let m = @\"a\\\"\" |> helper\"\n"+
		"    v |> other\n"+
		"\n"+
		"let generic (xs: 'T list) =\n"+
		"    xs |> other 'a'\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, caller := range []string{"raw", "spanning", "verbatim"} {
		if hasRelationByLastSegment(snapshot.Relations, "CALLS", caller, "helper") {
			t.Errorf("fabricated CALLS %s->helper from a pipeline inside a string literal: %#v", caller, relationsOfType(snapshot.Relations, "CALLS"))
		}
	}
	// `'T` is a generic type parameter, not a character literal: reading it as
	// one blanked the pipeline between it and the `'a'` that follows.
	for _, caller := range []string{"raw", "spanning", "verbatim", "generic"} {
		if !hasRelationByLastSegment(snapshot.Relations, "CALLS", caller, "other") {
			t.Errorf("missing CALLS %s->other; a literal mask ate a real pipeline: %#v", caller, relationsOfType(snapshot.Relations, "CALLS"))
		}
	}
}

// TestFSharpStrandedMaskerCorruptsTheCallGraph is the same defect at the graph
// level: the fabricated edge and the deleted one both reach CALLS.
func TestFSharpStrandedMaskerCorruptsTheCallGraph(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "src/A.fs", `module A

let helper (value: int) = value + 1

let other (value: int) = value * 2

let run (v: int) =
    // the marker is a " character
    (* v |> helper *)
    v |> other

let dropped (v: int) =
    let quote = '"'
    let s = "(*"
    v |> other

let raw (v: int) =
    let doc = """a " (* b"""
    v |> other
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if hasRelationByLastSegment(snapshot.Relations, "CALLS", "run", "helper") {
		t.Errorf("fabricated CALLS run->helper from a commented pipeline: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "run", "other") {
		t.Errorf("missing CALLS run->other: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "dropped", "other") {
		t.Errorf("missing CALLS dropped->other; the masker blanked a real pipeline: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	// A triple-quoted string is raw: it holds unescaped quotes, and ending it at
	// the first one left `(*` reading as a comment opener that never closed.
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "raw", "other") {
		t.Errorf("missing CALLS raw->other; a quote inside a triple-quoted string ended it early: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

// TestLiteralMaskersPreserveLengthAndLineStructure pins the invariant every
// caller of these two maskers depends on.
//
// stripRustCodegenMacroBodies blanks bytes of the ORIGINAL text at offsets it
// found in the masked copy, and every language mask that chains into these two
// (maskCSharpTextBlocks, maskSwiftMultilineStrings, maskKotlinRawStrings,
// maskGroovyLiteralsAndComments, the Flow mask) reports entity line numbers read
// from the masked text. A masker that added or dropped a single byte, or moved
// one line break, would silently misalign all of them, so the invariant is
// asserted rather than assumed.
func TestLiteralMaskersPreserveLengthAndLineStructure(t *testing.T) {
	t.Parallel()

	sources := []string{
		"let run v =\n    // the marker is a \" character\n    (* v |> helper *)\n    v |> other\n",
		"let quote = '\"'\nlet s = \"(*\"\nlet run v = v |> other\n",
		"let m = @\"a\"\"b(*\"\nlet run v = v |> other\n",
		"let doc = \"\"\"a \" (* b\"\"\"\nlet run v = v |> other\n",
		"let unterminated = \"\"\"abc\n(* v |> helper\n",
		"\"\"\"",
		"\"\"",
		"let e = \"\"\nlet run v = v |> other\n",
		"let unterminated = \"abc\n(* v |> helper\n",
		// The forms the F# literal masker blanks rather than merely skipping:
		// every one of them is a place a byte could go missing.
		"let s = \"\"\"a \" x |> helper \"\"\"\nlet run v = v |> other\n",
		"let sql = \"\"\"\nselect x |> helper\n\"\"\"\nlet run v = v |> other\n",
		"let m = @\"a\nb |> helper\"\nlet run v = v |> other\n",
		"let m = @\"a\\\"\" |> helper\"\nlet run v = v |> other\n",
		"let run (xs: 'T list) = xs |> other 'a'\n",
		"let c = '\"'\nlet s = \"x |> helper\"\nlet run v = v |> other\n",
		"// x |> helper\r\nlet run' v = v |> other\r\n",
		// A lone `\r` inside a block comment: the comment branch used to blank
		// every byte that was not `\n`, which moved the break on a CRLF file.
		"let run v =\r\n    (* v |> helper\r\n       more *)\r\n    v |> other\r\n",
		"@\"",
		"@",
		"let trailing = '",
		"let escape = '\\''\nlet run v = v |> other\n",
		"x = \"a\" // b\nfunction f() { /* c */ return `t`; }\n\r\n",
		"'",
		"\"",
		"(*",
		"",
	}
	for _, source := range sources {
		for name, mask := range map[string]func(string) string{
			"maskFSharpBlockComments":           maskFSharpBlockComments,
			"maskFSharpLiteralsAndLineComments": maskFSharpLiteralsAndLineComments,
			"stripCodeLiteralsAndComments":      stripCodeLiteralsAndComments,
		} {
			got := mask(source)
			if len(got) != len(source) {
				t.Fatalf("%s changed length of %q: %d -> %d", name, source, len(source), len(got))
			}
			for i := 0; i < len(source); i++ {
				if source[i] == '\n' || source[i] == '\r' {
					if got[i] != source[i] {
						t.Fatalf("%s moved the line break at %d of %q", name, i, source)
					}
				}
			}
		}
	}
}
