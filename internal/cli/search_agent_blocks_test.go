package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestParseSearchFlagsLeavesTheReferenceBlocksOff(t *testing.T) {
	t.Parallel()
	flags, rest, err := parseSearchFlags([]string{"--query", "x"})
	if err != nil || len(rest) != 0 {
		t.Fatalf("parse = %v / %v", err, rest)
	}
	if flags.ContainerMap || flags.SignatureTypes || flags.TypeCard {
		t.Fatalf("reference blocks default on: %+v", flags)
	}
}

func TestSearchReferenceBlockSelection(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		args    []string
		value   string
		wantMap bool
		wantSig bool
		wantCrd bool
		wantErr bool
	}{
		{name: "individual flags", args: []string{"--container-map"}, wantMap: true},
		{name: "signature types", args: []string{"--signature-types"}, wantSig: true},
		{name: "declaration card", args: []string{"--type-card"}, wantCrd: true},
		{
			name: "all three at once", args: []string{"--reference-blocks", "all"},
			wantMap: true, wantSig: true, wantCrd: true,
		},
		{
			name: "a comma separated list", args: []string{"--reference-blocks", "container-map,type-card"},
			wantMap: true, wantCrd: true,
		},
		{name: "spaces and case are tolerated", args: []string{"--reference-blocks", " Container-Map , "}, wantMap: true},
		{name: "an unknown name is an error, not a silent no-op", args: []string{"--reference-blocks", "nope"}, wantErr: true},
		{name: "the environment turns them on", value: "all", wantMap: true, wantSig: true, wantCrd: true},
		{name: "an unknown environment value is an error", value: "nope", wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			flags, _, err := parseSearchFlags(append([]string{"--query", "x"}, testCase.args...))
			if err == nil && testCase.value != "" {
				err = applySearchReferenceBlocks(&flags, testCase.value)
			}
			if testCase.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if flags.ContainerMap != testCase.wantMap || flags.SignatureTypes != testCase.wantSig ||
				flags.TypeCard != testCase.wantCrd {
				t.Fatalf("blocks = map %v / sig %v / card %v, want %v / %v / %v",
					flags.ContainerMap, flags.SignatureTypes, flags.TypeCard,
					testCase.wantMap, testCase.wantSig, testCase.wantCrd)
			}
		})
	}
}

// searchAgentBlockResponse is a payload carrying all three agent-asked blocks.
func searchAgentBlockResponse() sem.SearchResponse {
	return sem.SearchResponse{
		Query:   "cacheKey for the arithmetic Ops enum is wrong",
		Profile: "full",
		Results: []sem.SearchResult{
			{
				Rank: 1, Score: 20, FilePath: "Ops.java", StartLine: 12, EndLine: 18, FocusLine: 12,
				SnippetStartLine: 12, SnippetEndLine: 18, SymbolName: "cacheKey",
				Signals: []string{"body"}, Snippet: "  String cacheKey(Ops op) {\n  }\n",
			},
			{
				Rank: 2, FilePath: "OpsTest.java", StartLine: 5, EndLine: 9, FocusLine: 6,
				SnippetStartLine: 5, SnippetEndLine: 9, SymbolName: "cacheKeyIsStable",
				Section: sem.SearchSectionCoveringTest, Signals: []string{"covering-test"},
				Snippet: "  void cacheKeyIsStable() {\n  }\n",
			},
		},
		ClosedSet: &sem.SearchClosedSet{
			Type: "Ops", Kind: "enum", Variants: 3,
			Warning: "adding a variant without adding an arm compiles and then throws at runtime",
			Sites: []sem.SearchClosedSetSite{
				{FilePath: "Ops.java", Line: 13, Symbol: "Ops.cacheKey", Arms: 3, Exhaustive: true,
					Default: "throws", Checked: "runtime"},
			},
		},
		VerifyCommand: &sem.SearchVerifyCommand{
			Command: "mvn -q -Dtest=OpsTest -DfailIfNoTests=false test",
			Targets: "OpsTest.java", DerivedFrom: "pom.xml module + covering test class",
		},
		LiteralCluster: &sem.SearchLiteralCluster{
			Literal: "quotient", HitsTotal: 4, FilesTotal: 3,
			Hits: []sem.SearchLiteralHit{
				{FilePath: "Ops.java", Line: 6, Symbol: "Ops", Role: sem.SearchLiteralRoleEdit},
				{FilePath: "AvgAgg.java", Line: 88, Symbol: "AvgAgg.render", Role: sem.SearchLiteralRoleConsumer},
			},
		},
		Warnings:        []sem.ProviderWarning{},
		PartialFailures: []sem.PartialFailure{},
	}
}

func TestWriteTextSearchPinsTheAgentAskedBlockOrder(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := writeTextSearch(&out, searchAgentBlockResponse()); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	// The order is the doctrine: be warned, edit, know the goal, know how to prove it, then sweep.
	wanted := []string{"CLOSED SET Ops", "1. Ops.java:", searchTextTestHeader, "VERIFY:", "SAME-CONCEPT LITERAL"}
	previous := -1
	for _, marker := range wanted {
		index := strings.Index(rendered, marker)
		if index < 0 {
			t.Fatalf("missing %q in:\n%s", marker, rendered)
		}
		if index < previous {
			t.Fatalf("%q is out of order in:\n%s", marker, rendered)
		}
		previous = index
	}
}

func TestWriteTextSearchOmitsTheAgentAskedBlocksItDoesNotHave(t *testing.T) {
	t.Parallel()
	response := searchAgentBlockResponse()
	response.ClosedSet, response.VerifyCommand, response.LiteralCluster = nil, nil, nil
	var out bytes.Buffer
	if err := writeTextSearch(&out, response); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"CLOSED SET", "VERIFY", "SAME-CONCEPT"} {
		if strings.Contains(out.String(), marker) {
			t.Fatalf("%q rendered for an absent block:\n%s", marker, out.String())
		}
	}
}

func TestAgentSearchPutsTheWarningInThePrefixAndTheRestInTheSuffix(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := writeAgentSearch(&out, searchAgentBlockResponse(), 4096); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	warning := strings.Index(rendered, "CLOSED SET Ops")
	location := strings.Index(rendered, "1. Ops.java")
	verify := strings.Index(rendered, "VERIFY:")
	if warning < 0 || location < 0 || verify < 0 {
		t.Fatalf("missing a block:\n%s", rendered)
	}
	if !(warning < location && location < verify) {
		t.Fatalf("agent-format order is wrong:\n%s", rendered)
	}
}

func TestFitAgentSearchSuffixesSkipsOnlyWhatDoesNotFit(t *testing.T) {
	t.Parallel()
	payload := []byte("1. a.go:1\n")
	// The middle block is too large for the remaining cap; the small one after it must still ride
	// along, because the blocks are independent.
	suffixes := [][]byte{[]byte("VERIFY: x\n"), bytes.Repeat([]byte("L"), 200), []byte("D: y\n")}
	got := string(fitAgentSearchSuffixes(payload, suffixes, len(payload)+16))
	if !strings.Contains(got, "VERIFY: x") || !strings.Contains(got, "D: y") {
		t.Fatalf("both small blocks should fit: %q", got)
	}
	if strings.Contains(got, "LLL") {
		t.Fatalf("the oversized block must be skipped: %q", got)
	}
	if len(got) > len(payload)+16 {
		t.Fatalf("the cap was exceeded: %d", len(got))
	}
}

func TestNdjsonSearchEmitsTheAgentAskedBlocks(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := writeNdjsonSearch(&out, searchAgentBlockResponse()); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	for _, marker := range []string{
		`"record_type":"search_closed_set"`, `"verify_command"`, `"literal_cluster"`,
	} {
		if !strings.Contains(rendered, marker) {
			t.Fatalf("ndjson missing %q:\n%s", marker, rendered)
		}
	}
}
