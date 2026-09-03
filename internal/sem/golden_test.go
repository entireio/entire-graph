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

// TestJuliaBareCallStaysInsideItsOwnLanguageAndScope pins the two edges the Julia method
// arm must not create.
//
// Admitting method targets is what lets a bare `name(args...)` -- Julia's only call form
// -- reach a module-scoped definition. Two things ride along with it unless refused. The
// global fallback it draws on is language-agnostic, so an unresolved call bound to a
// globally unique JAVA method of the same name, which Julia cannot invoke. And a
// `module M ... end` block spans every nested definition, so scanning the module's own
// text saw the call names written inside its FUNCTIONS and credited the module with
// making them -- the same call appearing twice, once from the function that made it and
// once from a symbol that did not.
//
// The intra-module case is asserted alongside because both fixes withdraw candidates, and
// a withdrawal that also drops the real edge has traded one wrong answer for another.
func TestJuliaBareCallStaysInsideItsOwnLanguageAndScope(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()

	writeFileForTest := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFileForTest("m.jl", "module M\nfunction go()\n    runTask()\nend\nend\n")
	writeFileForTest("T.java", "public class T { void runTask() { } }\n")
	writeFileForTest("n.jl", "module N\nfunction outer()\n    inner()\nend\nfunction inner()\n    return 1\nend\nend\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, symbol := range snapshot.Symbols {
		symbolsByID[symbol.ID] = symbol
	}

	intraModule := 0
	for _, relation := range snapshot.Relations {
		if relation.Type != "CALLS" {
			continue
		}
		from, to := symbolsByID[relation.FromID], symbolsByID[relation.ToID]
		if from.Language == "Julia" && to.Language != "Julia" {
			t.Errorf("Julia %s resolved a bare call to %s/%s, which it cannot invoke", from.Name, to.Language, to.Name)
		}
		if from.Language == "Julia" && from.Kind == "module" {
			t.Errorf("module %s credited with calling %s; the call belongs to the function that wrote it", from.Name, to.Name)
		}
		if from.Name == "outer" && to.Name == "inner" {
			intraModule++
		}
	}
	if intraModule != 1 {
		t.Fatalf("the legitimate intra-module call outer -> inner was withdrawn too (found %d)", intraModule)
	}
}

// TestJuliaModuleOwnsOnlyItsOwnBodyAndOnlyItsOwnScope pins the two boundaries a Julia
// bare call sits inside.
//
// SCOPE: a module is a hard namespace boundary in every file. From outside M its
// definitions are reachable as `M.name(...)` and no other way, so a bare call in one
// module must not bind a same-named method of another just because their files differ.
//
// BODY: a `module M ... end` block spans every definition under it, so scanning it whole
// credited the module with the calls its FUNCTIONS make. Refusing the module outright
// fixed that and took a real edge with it -- `setup()` written at module scope has no
// other symbol to belong to. The module owns what its own body runs, and nothing else.
func TestJuliaModuleOwnsOnlyItsOwnBodyAndOnlyItsOwnScope(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	for name, content := range map[string]string{
		"a.jl": "module A\nfunction alpha()\n    return 1\nend\nend\n",
		"b.jl": "module B\nfunction caller()\n    alpha()\nend\nend\n",
		"m.jl": "module M\nfunction setup()\n    return 1\nend\nfunction helper()\n    return 2\nend\nfunction go()\n    helper()\nend\nsetup()\nend\n",
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

	edges := map[string]bool{}
	for _, relation := range snapshot.Relations {
		if relation.Type != "CALLS" {
			continue
		}
		from, to := symbolsByID[relation.FromID], symbolsByID[relation.ToID]
		edges[from.QualifiedName+" -> "+to.QualifiedName] = true
	}

	for _, want := range []string{
		"M.go -> M.helper", // a nested call belongs to the function that wrote it
		"M -> M.setup",     // a module-body call belongs to the module
	} {
		if !edges[want] {
			t.Errorf("missing edge %q; got %v", want, edges)
		}
	}
	for _, unwanted := range []string{
		"B.caller -> A.alpha", // crosses a module boundary, so it needs A.alpha(...)
		"M -> M.helper",       // written inside go(), not in the module body
	} {
		if edges[unwanted] {
			t.Errorf("edge %q must not be emitted; got %v", unwanted, edges)
		}
	}
}

// TestJuliaOneLineModuleKeepsItsOwnCall pins the module-body scan against a module whose
// child shares its line.
//
// `module M; setup() = 1; setup(); end` puts the module, its child and a real module-level
// call on ONE line. Blanking the child's lines therefore deleted the call the narrowing
// exists to keep, and a SymbolRecord carries only line numbers, so there is no byte range
// to mask instead. The child's BODY is blanked and its definition HEAD is masked by name,
// which says the same thing without deleting whatever else shares the line.
func TestJuliaOneLineModuleKeepsItsOwnCall(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	for name, content := range map[string]string{
		"one.jl":  "module M; setup() = 1; setup(); end\n",
		"many.jl": "module N\nfunction helper()\n    return 1\nend\nfunction go()\n    helper()\nend\nhelper()\nend\n",
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
	edges := map[string]bool{}
	for _, relation := range snapshot.Relations {
		if relation.Type == "CALLS" {
			edges[symbolsByID[relation.FromID].QualifiedName+" -> "+symbolsByID[relation.ToID].QualifiedName] = true
		}
	}
	for _, want := range []string{
		"M -> M.setup",  // the one-line module still owns its own call
		"N -> N.helper", // a module-level call in a multi-line module
		"N.go -> N.helper",
	} {
		if !edges[want] {
			t.Errorf("missing edge %q; got %v", want, edges)
		}
	}
	if edges["N -> N.go"] {
		t.Errorf("a definition head was read as a call: %v", edges)
	}
}

// TestJuliaShortFormAndNestedModuleScope pins two shapes the module-body narrowing has to
// get right.
//
// SHORT FORM: `f() = helper()` is one line, so no body line is blanked for it. Masking
// only the definition HEAD left `helper()` in the module's block and credited the module
// with a call its child makes. The mask now covers the right-hand side too.
//
// NESTED MODULE: a child of `Outer.Inner` records its container qualified, so comparing
// the module's SHORT name rejected every module-body call inside a nested module before
// the same-container check could accept it.
func TestJuliaShortFormAndNestedModuleScope(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	for name, content := range map[string]string{
		"sf.jl":   "module S\nhelper() = 1\nf() = helper()\nend\n",
		"nest.jl": "module Outer\nmodule Inner\nfunction setup()\n    return 1\nend\nsetup()\nend\nend\n",
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
	edges := map[string]bool{}
	for _, relation := range snapshot.Relations {
		if relation.Type == "CALLS" {
			edges[symbolsByID[relation.FromID].QualifiedName+" -> "+symbolsByID[relation.ToID].QualifiedName] = true
		}
	}
	if !edges["S.f -> S.helper"] {
		t.Errorf("the short-form child's own call was lost: %v", edges)
	}
	if edges["S -> S.helper"] {
		t.Errorf("the module was credited with a call its child makes: %v", edges)
	}
	if !edges["Inner -> Inner.setup"] {
		t.Errorf("a nested module's own body call was rejected: %v", edges)
	}
}
