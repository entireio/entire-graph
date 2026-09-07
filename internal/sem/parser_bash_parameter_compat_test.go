package sem

import (
	"context"
	"reflect"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

const bashParameterPunctuationFixture = `nested() {
  printf '%s' nested
}

run() {
  local path="${OUT:-${BASE}\dir$(nested)}"
  local aliases="${OLD:+${OLD};}${NEXT}"
  local mapping="${MAP:-{\}}"
  nested
  printf '%s %s %s' "${path}" "${aliases}" "${mapping}"
}
`

const bashParameterGrammarCleanControl = `nested() {
  printf '%s' nested
}

run() {
  local path="${OUT:-${BASE}/dir$(nested)}"
  local aliases="${OLD:+${OLD}_}${NEXT}"
  local mapping="${MAP:-xxx}"
  nested
  printf '%s %s %s' "${path}" "${aliases}" "${mapping}"
}
`

func TestMaskBashParameterReplacementPunctuationIsNarrow(t *testing.T) {
	masked := maskBashParameterReplacementPunctuation(bashParameterPunctuationFixture)
	if masked != bashParameterGrammarCleanControl {
		t.Fatalf("parse view differs from independently authored grammar-clean control:\n%s", masked)
	}
	if len(masked) != len(bashParameterPunctuationFixture) || strings.Count(masked, "\n") != strings.Count(bashParameterPunctuationFixture, "\n") {
		t.Fatalf("parse view changed source coordinates:\nsource=%q\nmasked=%q", bashParameterPunctuationFixture, masked)
	}
	for _, want := range []string{
		`${OUT:-${BASE}/dir$(nested)}`,
		`${OLD:+${OLD}_}${NEXT}`,
		`${MAP:-xxx}`,
	} {
		if !strings.Contains(masked, want) {
			t.Errorf("parse view missing %q: %q", want, masked)
		}
	}
	if !strings.Contains(masked, "$(nested)") {
		t.Fatal("nested command substitution was removed")
	}

	unchanged := []string{
		"# note \"${OUT:-${BASE}\\dir}\"\n",
		"literal='${OUT:-${BASE}\\dir}'\n",
		"literal=${OUT:-${BASE}\\dir}\n",
		"literal=\"${OUT:-${BASE}suffix}\"\n",
		"literal=\"${OUT:-${BASE:-$(nested)}}\"\n",
		"literal=\"${OUT:-${BASE}\\dir`legacy`}\"\n",
	}
	for _, source := range unchanged[:5] {
		if got := maskBashParameterReplacementPunctuation(source); got != source {
			t.Errorf("out-of-scope source changed:\nsource=%q\ngot=%q", source, got)
		}
	}
	withBackticks := unchanged[5]
	got := maskBashParameterReplacementPunctuation(withBackticks)
	if !strings.Contains(got, "`legacy`") || len(got) != len(withBackticks) {
		t.Fatalf("backtick command changed:\nsource=%q\ngot=%q", withBackticks, got)
	}
}

func TestBashParameterReplacementPunctuationRawFailureAndPublicParse(t *testing.T) {
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(treeSitterLanguages[".sh"].grammar)
	raw, err := parser.ParseCtx(context.Background(), nil, []byte(bashParameterPunctuationFixture))
	if err != nil {
		t.Fatal(err)
	}
	if root := raw.RootNode(); root == nil || !root.HasError() {
		t.Fatal("fixture must exercise the bundled grammar gap")
	}
	raw.Close()

	masked := maskBashParameterReplacementPunctuation(bashParameterPunctuationFixture)
	prepared, err := parser.ParseCtx(context.Background(), nil, []byte(masked))
	if err != nil {
		t.Fatal(err)
	}
	if root := prepared.RootNode(); root == nil || root.HasError() {
		t.Fatalf("punctuation-only parse view still fails: %s", parseErrorDetail(root, []byte(masked)))
	} else if !strings.Contains(root.String(), "command_substitution") {
		t.Fatalf("nested command substitution disappeared from parse tree: %s", root.String())
	}
	prepared.Close()

	entities, language, status := TreeSitterParser{}.ParseWithStatus("fixture.sh", bashParameterPunctuationFixture)
	if language != "Bash" || status.ParseError {
		t.Fatalf("public parse = language %q status %+v", language, status)
	}
	var run Entity
	for _, entity := range entities {
		if entity.Name == "run" {
			run = entity
			break
		}
	}
	if run.Kind != "function" || run.StartLine != 5 || run.EndLine != 11 {
		t.Fatalf("run entity range = %#v", run)
	}
	authored := bashParameterPunctuationFixture[run.sourceStartByte:run.sourceEndByte]
	if !strings.Contains(authored, `${OUT:-${BASE}\dir$(nested)}`) || strings.Contains(authored, `${BASE}/dir`) {
		t.Fatalf("entity range does not bind authored source: %q", authored)
	}
	if run.BodyHash != hash(normalize(authored)) {
		t.Fatalf("body hash = %q, want authored body hash", run.BodyHash)
	}
}

func TestBashParameterReplacementPunctuationMalformedStaysPartial(t *testing.T) {
	malformed := "run() {\n  local path=\"${OUT:-${INNER:-${BASE}\\dir}\"\n}\n"
	if got := maskBashParameterReplacementPunctuation(malformed); got != malformed {
		t.Fatalf("incomplete expansion was rewritten: %q", got)
	}
	_, _, status := TreeSitterParser{}.ParseWithStatus("broken.sh", malformed)
	if !status.ParseError || status.Code != "E_PARSE_ERROR" {
		t.Fatalf("malformed source status = %+v, want E_PARSE_ERROR", status)
	}
}

func TestBashParameterReplacementPunctuationSnapshotAndCacheParity(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "fixture.sh", bashParameterPunctuationFixture)
	cacheDir := t.TempDir()
	options := ProviderSnapshotOptions{Worktree: true, ExtractionReuse: true, ExtractionCacheDir: cacheDir}
	build := func(options ProviderSnapshotOptions) ProviderSnapshot {
		t.Helper()
		snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "fixture-version", options)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	cold := build(options)
	if got := cold.Header.Stats.Extraction; got == nil || got.FilesParsed != 1 || got.FilesReused != 0 {
		t.Fatalf("cold extraction stats = %#v", got)
	}
	warm := build(options)
	if got := warm.Header.Stats.Extraction; got == nil || got.FilesParsed != 0 || got.FilesReused != 1 {
		t.Fatalf("warm extraction stats = %#v", got)
	}
	uncachedOptions := options
	uncachedOptions.ExtractionReuse = false
	uncached := build(uncachedOptions)
	if !reflect.DeepEqual(cold.Symbols, warm.Symbols) || !reflect.DeepEqual(warm.Symbols, uncached.Symbols) ||
		!reflect.DeepEqual(cold.Relations, warm.Relations) || !reflect.DeepEqual(warm.Relations, uncached.Relations) {
		t.Fatal("cached and uncached symbols, ranges, body hashes, IDs, or relations differ")
	}
	if !hasRelationByLastSegment(cold.Relations, "CALLS", "run", "nested") {
		t.Fatalf("supported outside call missing from snapshot: %#v", cold.Relations)
	}
	for _, snapshot := range []ProviderSnapshot{cold, warm, uncached} {
		for _, failure := range snapshot.Header.PartialFailures {
			if failure.FilePath == "fixture.sh" {
				t.Fatalf("valid fixture retained partial failure: %#v", failure)
			}
		}
	}

	writeFile(t, repo, "fixture.sh", bashParameterGrammarCleanControl)
	control := build(uncachedOptions)
	// The current shell relation scanner does not emit the quoted local
	// assignment's nested $(nested) call, even from the grammar-clean control.
	// Preserve that existing limitation here while proving this compatibility
	// view keeps every public CALLS relation the control can produce. The raw
	// parse-tree assertion above separately proves the nested command subtree is
	// retained for a future resolver improvement.
	if got, want := bashParameterCallRelations(cold), bashParameterCallRelations(control); !reflect.DeepEqual(got, want) {
		t.Fatalf("compatibility view changed public CALLS set: got %#v want grammar-clean control %#v", got, want)
	}
}

func bashParameterCallRelations(snapshot ProviderSnapshot) []RelationRecord {
	var calls []RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "CALLS" {
			relation.Evidence = nil
			calls = append(calls, relation)
		}
	}
	return calls
}
