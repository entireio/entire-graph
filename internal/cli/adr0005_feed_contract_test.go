package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
	scippb "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// adr0005Path is the RFD whose freshness contract tells peregrine how to decide
// per-language whether to trust the SCIP feed. It is checked against the code the
// same way AGENTS.md is (see agentguide_test.go): a design document that other
// teams implement against is only useful while it describes the artifact that
// actually ships.
const adr0005Path = "0005-converge-code-search.md"

// adr0005FreshnessItem2 returns the freshness-contract item that describes what
// the omission note carries, isolated from the rest of the RFD so a field name
// mentioned in some unrelated paragraph cannot satisfy the check below.
func adr0005FreshnessItem2(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "adr", adr0005Path))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	const start = "2. Per-language trust is driven by the omission note"
	begin := strings.Index(text, start)
	if begin < 0 {
		t.Fatalf("%s no longer has a freshness-contract item 2; this test is pinned to it", adr0005Path)
	}
	end := strings.Index(text[begin:], "\n3. ")
	if end < 0 {
		t.Fatalf("%s freshness-contract item 2 has no item 3 after it", adr0005Path)
	}
	return text[begin : begin+end]
}

// adr0005FailureRecordFields returns the field names item 2 lists for a failure
// record, in the order it lists them. The list is the sentence that opens "The
// record is" and closes at its provider.go citation, so a field name that item 2
// mentions anywhere else -- inside a negation, say -- is not counted as listed.
func adr0005FailureRecordFields(t *testing.T, item string) []string {
	t.Helper()
	const start = "The record is "
	begin := strings.Index(item, start)
	if begin < 0 {
		t.Fatalf("%s freshness-contract item 2 no longer has a %q sentence; this test is pinned to it",
			adr0005Path, start)
	}
	sentence := item[begin+len(start):]
	end := strings.Index(sentence, "(`internal/sem/provider.go:")
	if end < 0 {
		t.Fatalf("%s freshness-contract item 2 lists the failure record without citing internal/sem/provider.go",
			adr0005Path)
	}
	var fields []string
	for i, part := range strings.Split(sentence[:end], "`") {
		if i%2 == 1 {
			fields = append(fields, part)
		}
	}
	return fields
}

// TestADR0005NamesTheFailureRecordItActuallyGets is the docs/spec consistency
// check for a mismatch that was live twice. First, item 2 said "only records say
// which language and which file" about a record that then had no language, and
// listed it as carrying "path and code" while the shipped record was five
// fields. Then the record gained an optional `language` (schema 1.3) while the
// corrected paragraph went on saying "there is no `language`" -- and the first
// version of this test kept passing, because it only looked for each field name
// in backticks somewhere in the item and the negation supplied one.
//
// The assertions are deliberately driven off the struct rather than off a fixed
// list, and in both directions: the fields the RFD lists must be exactly the
// fields sem.PartialFailure serializes, in order, so adding, removing or
// renaming a field fails this until the paragraph is brought with it. What the
// paragraph says about a language on the record is then checked against whether
// the struct has one and whether it can be absent on the wire.
func TestADR0005NamesTheFailureRecordItActuallyGets(t *testing.T) {
	t.Parallel()
	item := adr0005FreshnessItem2(t)
	recordType := reflect.TypeOf(sem.PartialFailure{})
	var shipped []string
	languageOptional := false
	for i := range recordType.NumField() {
		tagParts := strings.Split(recordType.Field(i).Tag.Get("json"), ",")
		tag := tagParts[0]
		if tag == "" || tag == "-" {
			continue
		}
		shipped = append(shipped, tag)
		if tag == "language" {
			for _, option := range tagParts[1:] {
				if option == "omitempty" {
					languageOptional = true
				}
			}
		}
	}
	if len(shipped) == 0 {
		t.Fatal("sem.PartialFailure has no JSON fields; this test asserted nothing")
	}
	listed := adr0005FailureRecordFields(t, item)
	if !reflect.DeepEqual(listed, shipped) {
		t.Errorf("the RFD's per-language fallback contract lists the failure record as %v and the feed ships %v; "+
			"a consumer implementing from this paragraph reads a record that is not the one it gets",
			listed, shipped)
	}
	// The specific false claims, in whichever direction the code makes them
	// false. A record with no language field cannot be the thing that says which
	// language; a record with one cannot be described as having none; and a
	// field that is dropped when empty must be described as one a consumer may
	// not find, or it will route per-language fallback on a blank.
	_, hasLanguage := recordType.FieldByName("Language")
	denials := []string{"there is no `language`", "carries no language", "has no language field"}
	if hasLanguage {
		for _, denial := range denials {
			if strings.Contains(item, denial) {
				t.Errorf("the RFD says of the failure record %q, and sem.PartialFailure has a language field", denial)
			}
		}
		const optional = "language is optional"
		if languageOptional != strings.Contains(item, optional) {
			t.Errorf("the RFD says %q: %v, and sem.PartialFailure drops an empty language from the wire: %v",
				optional, strings.Contains(item, optional), languageOptional)
		}
	} else {
		for _, claim := range []string{"records say which language", "language is optional"} {
			if strings.Contains(item, claim) {
				t.Errorf("the RFD says of the failure record %q, and sem.PartialFailure has no language field", claim)
			}
		}
	}
}

// TestSCIPFeedResolvesAFailingFilesLanguage is the mechanism the corrected
// paragraph prescribes, run end to end: a consumer holding only the artifact
// pair must be able to get from a failure record to the language it should stop
// trusting. It joins partial_failures[].file_path to the SCIP Document of the
// same relative_path and reads that Document's language.
//
// It is the reason the RFD was corrected towards the implementation rather than
// the other way round: the information the contract needs is already in the
// artifact, so no field and no format bump is required to deliver it.
//
// It also pins the reason the RFD still prescribes the join now that the record
// has an optional language: item 2 says a parse-skipped failure leaves that
// field out. If the provider starts filling it in for such a record, the join is
// no longer the only route and the paragraph has to say so.
func TestSCIPFeedResolvesAFailingFilesLanguage(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "main.go", "package sample\n\nfunc caller() { callee() }\nfunc callee() {}\n")
	// One very long line is what the provider calls minified, so this produces a
	// real E_MINIFIED failure record naming a TypeScript file without needing a
	// multi-megabyte fixture.
	write(t, repo, "bundle.ts", "export const bundled="+strings.Repeat("\"x\"+", 4000)+"\"x\";\n")

	var stdout, stderr bytes.Buffer
	if err := Run(t.Context(), Options{
		Version: "scip-test",
		Env:     EntireEnv{RepoRoot: repo},
		Stdout:  &stdout,
		Stderr:  &stderr,
	}, []string{"snapshot", "--repo", repo, "--worktree", "--format", "scip"}); err != nil {
		t.Fatal(err)
	}
	var note sem.SCIPOmissionNote
	if err := json.Unmarshal(bytes.TrimSpace(stderr.Bytes()), &note); err != nil {
		t.Fatalf("stderr omission note is not JSON: %q: %v", stderr.String(), err)
	}
	if len(note.PartialFailures) == 0 {
		t.Fatalf("fixture produced no failure record, so the join is not exercised: %#v", note)
	}
	var index scippb.Index
	if err := proto.Unmarshal(stdout.Bytes(), &index); err != nil {
		t.Fatalf("scip output is not a valid Index protobuf: %v", err)
	}
	documents := map[string]*scippb.Document{}
	for _, doc := range index.GetDocuments() {
		documents[doc.GetRelativePath()] = doc
	}
	item := adr0005FreshnessItem2(t)
	const leavesItOut = "Every other failure record leaves it out"
	if !strings.Contains(item, leavesItOut) {
		t.Fatalf("%s freshness-contract item 2 no longer says %q; this test is pinned to it", adr0005Path, leavesItOut)
	}
	joined, namedWithoutLanguage := 0, 0
	for _, failure := range note.PartialFailures {
		if failure.FilePath == "" {
			continue
		}
		if strings.Contains(item, "`"+failure.Code+"`") {
			if failure.Language != "" {
				t.Errorf("failure %s on %q carries language %q, and the RFD names %s among the records that "+
					"leave the field out, which is its reason for prescribing the join", failure.Code,
					failure.FilePath, failure.Language, failure.Code)
			} else {
				namedWithoutLanguage++
			}
		}
		doc := documents[failure.FilePath]
		if doc == nil {
			t.Errorf("failure %s names %q and the index has no document for it, so a consumer cannot tell which "+
				"language to stop trusting", failure.Code, failure.FilePath)
			continue
		}
		if doc.GetLanguage() == "" {
			t.Errorf("failure %s names %q whose document carries no language", failure.Code, failure.FilePath)
			continue
		}
		joined++
	}
	if joined == 0 {
		t.Fatalf("no failure record resolved to a language: failures=%#v documents=%v", note.PartialFailures, documents)
	}
	if namedWithoutLanguage == 0 && !t.Failed() {
		t.Fatalf("fixture produced no failure of a code the RFD names as leaving the language out, so that claim "+
			"is not exercised: %#v", note.PartialFailures)
	}
	// And the tiers the RFD tells the ingestion step to partition by must know
	// that language, or the resolved name has nothing to be looked up in.
	if len(note.LanguageTiers) == 0 {
		t.Fatalf("the note carries no language tiers: %#v", note)
	}
	if _, ok := note.LanguageTiers["TypeScript"]; !ok {
		t.Fatalf("language tiers omit the failing file's language: %v", note.LanguageTiers)
	}
}
