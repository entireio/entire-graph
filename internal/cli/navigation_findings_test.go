package cli

import (
	"bytes"
	"encoding/json"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/entireio/entire-graph/internal/sem"
)

// A `--file` value that names a directory OUTSIDE the repository must not be
// folded onto an in-repo path just because it matches the root when case is
// ignored. On a case-sensitive filesystem /work/Foo and /work/foo are two
// directories, and treating the second as the first let the filter select a
// definition the caller's path never pointed at.
//
// The two spellings below name nothing on disk, so no filesystem can confirm
// them as one directory, on any platform.
func TestNormalizeSymbolRefFileRefusesUnconfirmedCaseFoldedRoot(t *testing.T) {
	t.Parallel()
	parent := filepath.ToSlash(t.TempDir())
	root := parent + "/Foo"
	outside := parent + "/foo/src/a.go"

	if got := normalizeSymbolRefFile(outside, root); got != path.Clean(outside) {
		t.Fatalf("normalizeSymbolRefFile(%q, %q) = %q, want the path left outside the repo (%q)",
			outside, root, got, path.Clean(outside))
	}
	// The same fold must not turn a path outside the repository into "no filter",
	// which selects every definition rather than none.
	if got := normalizeSymbolRefFile(parent+"/foo", root); got == "" {
		t.Fatalf("normalizeSymbolRefFile(%q, %q) dropped the filter entirely", parent+"/foo", root)
	}
	// An exactly-spelled root still strips, which is the behaviour callers rely on.
	if got := normalizeSymbolRefFile(root+"/src/a.go", root); got != "src/a.go" {
		t.Fatalf("exact-case root prefix = %q, want src/a.go", got)
	}
	if got := normalizeSymbolRefFile(root, root); got != "" {
		t.Fatalf("the root itself = %q, want the empty filter", got)
	}
}

func memberlessTypeSnapshot(language, signature string) sem.ProviderSnapshot {
	symbol := func(id, file string) sem.SymbolRecord {
		return sem.SymbolRecord{
			ID: id, Kind: "class", Name: "Config", QualifiedName: "Config",
			FilePath: file, StartLine: 3, EndLine: 4, Language: language, Signature: signature,
		}
	}
	return sem.ProviderSnapshot{
		Header:  sem.SnapshotHeader{RepoRoot: "/repo", Profile: "full"},
		Files:   []sem.FileRecord{{Path: "src/a"}, {Path: "src/b"}},
		Symbols: []sem.SymbolRecord{symbol("a", "src/a"), symbol("b", "src/b")},
	}
}

// Two unrelated same-named types that happen to declare no members are two
// answers, not one. Merging them produced a single declaration carrying a
// PARTIAL part pointing at a file the type was never split across — a confident
// wrong answer in place of a visible ambiguity.
func TestDefKeepsUnrelatedMemberlessTypesApart(t *testing.T) {
	t.Parallel()
	response := buildDefResponse(
		memberlessTypeSnapshot("Java", "public class Config"),
		defFlags{Symbol: "Config", MemberLimit: defaultDefMemberLimit},
	)
	if len(response.Declarations) != 2 {
		t.Fatalf("two empty Java Config classes returned %d declaration(s), want 2: %+v",
			len(response.Declarations), response.Declarations)
	}
	for _, declaration := range response.Declarations {
		for _, part := range declaration.Parts {
			if part.Relation == "PARTIAL" {
				t.Fatalf("Java has no partial types, but %s:%d was reported as a PARTIAL part",
					part.FilePath, part.StartLine)
			}
		}
	}
}

// The other half of the same rule: a declaration that SAYS `partial` is still
// merged even when it owns no members, so the fix above cannot have been bought
// by refusing to merge real partial types.
func TestDefStillMergesDeclaredPartialTypes(t *testing.T) {
	t.Parallel()
	response := buildDefResponse(
		memberlessTypeSnapshot("C#", "public partial class Config"),
		defFlags{Symbol: "Config", MemberLimit: defaultDefMemberLimit},
	)
	if len(response.Declarations) != 1 {
		t.Fatalf("two C# partial Config declarations returned %d declarations, want 1", len(response.Declarations))
	}
	if len(response.Declarations[0].Parts) != 1 || response.Declarations[0].Parts[0].Relation != "PARTIAL" {
		t.Fatalf("merged C# declaration lost its PARTIAL part: %+v", response.Declarations[0].Parts)
	}
}

// `--signature-types` asks for a block, the response carries it, the statistics
// charge for it — and the NDJSON stream had no serializer for it at all, so the
// one format an automated consumer reads silently omitted what it asked for.
func TestNdjsonSearchSummaryCarriesSignatureTypes(t *testing.T) {
	t.Parallel()
	response := sem.SearchResponse{
		FormatVersion: 1,
		Query:         "config",
		SignatureTypes: []sem.SearchSignatureType{{
			Name: "memChunk", FilePath: "tsdb/head.go", StartLine: 2107,
			Fields: []string{"prev *memChunk"}, FieldsTotal: 1,
		}},
	}
	var buf bytes.Buffer
	if err := writeNdjsonSearch(&buf, response); err != nil {
		t.Fatal(err)
	}
	var summary map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("ndjson record %q: %v", line, err)
		}
		if record["record_type"] == "search_summary" {
			summary = record
		}
	}
	if summary == nil {
		t.Fatalf("no search_summary record in:\n%s", buf.String())
	}
	types, ok := summary["signature_types"].([]any)
	if !ok || len(types) != 1 {
		t.Fatalf("summary omitted the requested signature types: %v", summary["signature_types"])
	}
}

// The disambiguation selector is advertised as a command to copy. Every value in
// it is repository-controlled, so a space split the advertised command into the
// wrong arguments and a `;` or `$(...)` in a tracked path turned it into an
// invitation to run something else.
func TestDisambiguationSelectorsQuoteRepositoryControlledValues(t *testing.T) {
	t.Parallel()
	// The shell family is passed in rather than taken from the host, because WHICH selectors exist
	// is a property of the shell the caller pastes into. This subject is the POSIX one.
	selectors := disambiguationSelectorsFor("linux", []neighborEndpoint{
		{Name: "State", FilePath: "src/a b.go", StartLine: 12, Kind: "struct"},
		{Name: "State;id", FilePath: "src/$(id).go", StartLine: 20, Kind: "struct"},
		{Name: "State", FilePath: "src/plain.go", StartLine: 30, Kind: "struct"},
	})
	if !strings.Contains(selectors[0], "--file 'src/a b.go'") {
		t.Fatalf("a path with a space was not quoted: %q", selectors[0])
	}
	if !strings.Contains(selectors[1], "--file 'src/$(id).go'") ||
		!strings.Contains(selectors[1], "--symbol 'State;id'") {
		t.Fatalf("shell metacharacters were not quoted: %q", selectors[1])
	}
	// An ordinary selector is unchanged: quoting is applied where it is needed,
	// so the common case still reads as the command a caller would have typed.
	if !strings.Contains(selectors[2], "--symbol State --file src/plain.go --line 30") {
		t.Fatalf("an ordinary selector was rewritten: %q", selectors[2])
	}
}

// A JavaScript parse failure CAN hide a caller of a TypeScript symbol: a JS
// import resolves against .ts/.tsx candidates, so the relation crosses the
// language label the completeness banner scopes on. Reporting that failure as
// one that "cannot affect this answer" is the claim that stops a reader looking
// further.
func TestCompletenessKeepsRelatedLanguageFailuresInScope(t *testing.T) {
	t.Parallel()
	snapshot := sem.ProviderSnapshot{
		Files: []sem.FileRecord{
			{Path: "web/app.js", Language: "JavaScript"},
			{Path: "infra/main.py", Language: "Python"},
		},
		Header: sem.SnapshotHeader{
			PartialFailures: []sem.PartialFailure{
				{Code: "P_PARSE", FilePath: "web/app.js"},
				{Code: "P_PARSE", FilePath: "infra/main.py"},
			},
		},
	}
	scope := buildCompletenessScope(snapshot, "TypeScript")
	if scope.LanguageFailed != 1 || len(scope.InScopeFailures) != 1 ||
		scope.InScopeFailures[0].FilePath != "web/app.js" {
		t.Fatalf("JavaScript failure was ruled out of a TypeScript answer: %+v", scope)
	}
	// Python cannot reach a TypeScript symbol, and must stay collapsed: the fix
	// widens the scope to the languages that actually join, not to everything.
	if scope.OtherFailures != 1 {
		t.Fatalf("unrelated-language failures = %d, want 1: %+v", scope.OtherFailures, scope)
	}
}

// Both flags document zero as a DEFINED value (no padding; the built-in depth),
// so a configuration that serializes its default explicitly must not fail before
// the search runs.
func TestSearchFlagsAcceptTheirDefinedZeroValues(t *testing.T) {
	t.Parallel()
	for _, flag := range []string{"--enclosure-context-lines", "--body-head-ranks"} {
		flags, _, err := parseSearchFlags([]string{"--query", "x", flag, "0"})
		if err != nil {
			t.Fatalf("%s 0: %v", flag, err)
		}
		value := flags.EnclosureContextLines
		if flag == "--body-head-ranks" {
			value = flags.BodyHeadRanks
		}
		if value != 0 {
			t.Fatalf("%s 0 parsed as %d", flag, value)
		}
		if _, _, err := parseSearchFlags([]string{"--query", "x", flag, "-1"}); err == nil {
			t.Fatalf("%s -1 was accepted", flag)
		}
	}
}

// --max-context-bytes is a hard ceiling on what the agent is handed. Repository
// source is escaped on the way out — one ESC becomes four printed bytes — so
// fitting the raw bytes and escaping afterwards let a snippet of control bytes
// overrun the cap by a multiple.
func TestAgentSearchHonorsByteCapWithControlBytes(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "a.py", "def target():\n    return \""+strings.Repeat("\x1b", 300)+"\"\n")
	for _, budget := range []int{600, 400, 300} {
		var out bytes.Buffer
		err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
			"search", "--repo", repo, "--query", "target", "--format", "agent",
			"--profile", "syntax-only", "--worktree",
			"--max-context-bytes", strconv.Itoa(budget),
		})
		if err != nil {
			t.Fatalf("budget %d: %v", budget, err)
		}
		if out.Len() > budget {
			t.Fatalf("budget %d: emitted %d bytes", budget, out.Len())
		}
		if bytes.IndexByte(out.Bytes(), 0x1b) >= 0 {
			t.Fatalf("budget %d: a raw ESC reached the payload", budget)
		}
	}
}

// index_latency_ms is what a caller reads to decide whether the index cache is
// working. Opening the source reader spawns a `git cat-file` child; charging
// that spawn to the index made the field report work the index never did.
//
// The hook makes the open take a known amount of time, so the assertion is about
// WHICH phase absorbs it and not about how fast the machine is: whatever the
// index costs, the reader's time must land outside it.
func TestIndexLatencyExcludesSourceReaderOpen(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "a.py", "def target():\n    return True\n")
	const openCost = 250 * time.Millisecond
	previous := sourceReaderOpenHook
	sourceReaderOpenHook = func() { time.Sleep(openCost) }
	t.Cleanup(func() { sourceReaderOpenHook = previous })

	for _, verb := range []string{"neighbors", "impact"} {
		var out bytes.Buffer
		err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
			verb, "--repo", repo, "--symbol", "target", "--format", "json", "--profile", "syntax-only",
		})
		if err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
		var response struct {
			IndexLatencyMS int64 `json:"index_latency_ms"`
			TotalLatencyMS int64 `json:"total_latency_ms"`
		}
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatalf("%s: %v\n%s", verb, err, out.String())
		}
		outsideIndex := response.TotalLatencyMS - response.IndexLatencyMS
		if outsideIndex < 200 {
			t.Fatalf("%s: source-reader open was charged to the index (index=%dms total=%dms)",
				verb, response.IndexLatencyMS, response.TotalLatencyMS)
		}
	}
}

// TestDisambiguationSelectorsDeclineUnquotableValuesOnWindows is the regression for POSIX quoting
// being emitted to a shell that does not have it.
//
// `'` is an ORDINARY CHARACTER to cmd.exe. So `--file 'src/a b.go'` reaches the program as the two
// arguments `'src/a` and `b.go'` and resolves nothing, and — the half that is not merely cosmetic —
// `'a&whoami'` still runs `whoami`, because cmd.exe parses `&` before the program ever sees the
// quotes. The quoting is not just ineffective on Windows, it is a false guarantee about exactly the
// characters it was added for. PowerShell reads `'` as a quote but disagrees with cmd.exe about the
// bare spelling, so no single string is right for both.
//
// The answer is to withhold the command, not to invent a second dialect: the definition line still
// carries the file and the line, which is what navigation needs.
func TestDisambiguationSelectorsDeclineUnquotableValuesOnWindows(t *testing.T) {
	t.Parallel()
	definitions := []neighborEndpoint{
		{Name: "State", FilePath: "src/a b.go", StartLine: 12, Kind: "struct"},
		{Name: "State&whoami", FilePath: "src/x.go", StartLine: 20, Kind: "struct"},
		{Name: "O'Brien", FilePath: "src/y.go", StartLine: 25, Kind: "struct"},
		{Name: "State", FilePath: "src/plain.go", StartLine: 30, Kind: "struct"},
	}
	selectors := disambiguationSelectorsFor("windows", definitions)
	for index, selector := range selectors[:3] {
		if selector != "" {
			t.Fatalf("definition %d advertised %q on Windows, where no quoting of %q holds: "+
				"the command either resolves nothing or executes part of the value",
				index, selector, definitions[index].Name+" "+definitions[index].FilePath)
		}
	}
	// A value no shell reads as syntax still gets its selector: the common case is unchanged on
	// every platform, which is what keeps this a decline and not a Windows-wide regression.
	if !strings.Contains(selectors[3], "--symbol State --file src/plain.go --line 30") {
		t.Fatalf("an ordinary Windows selector was withheld: %q", selectors[3])
	}
	// And the same values keep their POSIX selectors, so the decline is scoped to the shell that
	// cannot express them rather than applied to everyone.
	posix := disambiguationSelectorsFor("darwin", definitions)
	for index, selector := range posix {
		if selector == "" {
			t.Fatalf("definition %d lost its POSIX selector: %+v", index, definitions[index])
		}
	}
}

// TestDefKeepsAPartialDeclarationOutOfANonPartialGroup is the regression for the OTHER direction of
// the memberless-merge rule.
//
// The candidate's own `partial` keyword says it is A part; it says nothing about whether the group
// already seated is the type it is a part of. A non-partial `Config` that sorts first therefore
// absorbed partial `Config` declarations from another namespace or assembly, and `def` answered with
// one type carrying PARTIAL parts it was never split across — the confident wrong answer in place of
// a visible ambiguity, reached from the side the first fix did not cover.
func TestDefKeepsAPartialDeclarationOutOfANonPartialGroup(t *testing.T) {
	t.Parallel()
	snapshot := memberlessTypeSnapshot("C#", "public partial class Config")
	// src/a sorts first and is NOT partial: a different Config that merely shares the name.
	snapshot.Symbols[0].Signature = "public class Config"
	response := buildDefResponse(snapshot, defFlags{Symbol: "Config", MemberLimit: defaultDefMemberLimit})
	if len(response.Declarations) != 2 {
		t.Fatalf("a non-partial C# Config absorbed a partial Config from elsewhere: got %d "+
			"declaration(s), want 2: %+v", len(response.Declarations), response.Declarations)
	}
	for _, declaration := range response.Declarations {
		for _, part := range declaration.Parts {
			if part.Relation == "PARTIAL" {
				t.Fatalf("%s:%d was reported as a PARTIAL part of a type it is not part of",
					part.FilePath, part.StartLine)
			}
		}
	}
}
