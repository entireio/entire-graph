package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// explainStreamProbe is stdin for a build that is still running. Its SECOND read refuses to hand
// over the rest of the output until the first part has already been echoed, which is the difference
// between a filter and a buffer: a command that reads its input to the end before writing anything
// holds the whole build in memory, and a build that goes wrong is exactly the one that prints too
// much to hold.
type explainStreamProbe struct {
	echoed *bytes.Buffer
	head   string
	tail   string
	reads  int
}

func (probe *explainStreamProbe) Read(into []byte) (int, error) {
	probe.reads++
	switch probe.reads {
	case 1:
		return copy(into, probe.head), nil
	case 2:
		if probe.echoed.Len() == 0 {
			return 0, errors.New("explain read the whole build into memory before echoing any of it")
		}
		return copy(into, probe.tail), nil
	default:
		return 0, io.EOF
	}
}

// A build that names no symbol still has to come back out of the filter, in full, as it arrives.
func TestExplainEchoesTheBuildAsItArrivesRatherThanBufferingIt(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	probe := &explainStreamProbe{echoed: &out, head: "compiling package one\n", tail: "compiling package two\n"}
	err := Run(t.Context(), Options{
		Version: "test", Env: EntireEnv{}, Stdout: &out, Stderr: io.Discard, Stdin: probe,
	}, []string{"explain"})
	if err != nil {
		t.Fatalf("explain did not stream its input: %v", err)
	}
	if got := out.String(); got != probe.head+probe.tail {
		t.Fatalf("echoed %q, want %q", got, probe.head+probe.tail)
	}
}

// A build whose last line has no newline still has to be separated from the block that follows it.
func TestExplainTerminatesAnUnterminatedEcho(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := Run(t.Context(), Options{
		Version: "test", Env: EntireEnv{}, Stdout: &out, Stderr: io.Discard,
		Stdin: strings.NewReader("no trailing newline"),
	}, []string{"explain"})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "no trailing newline\n" {
		t.Fatalf("echoed %q, want a terminated line", got)
	}
}

// Truncation has to be VISIBLE. The collector used to stop the moment it had the limit, so Scanned
// was a restatement of the limit and Omitted was never set: a consumer could not tell a build that
// named exactly two symbols from one that named ten.
func TestExplainReportsHowManyNamesTheBuildOmitted(t *testing.T) {
	t.Parallel()
	build := strings.Join([]string{
		"./a.go:1:1: undefined: alpha",
		"./b.go:2:1: undefined: beta",
		"./c.go:3:1: undefined: gamma",
		"./d.go:4:1: undefined: delta",
		"./e.go:5:1: undefined: epsilon",
	}, "\n")
	candidates, scanned, err := explainCandidates(strings.NewReader(build), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("collected %d candidates, want 2", len(candidates))
	}
	if scanned != 5 {
		t.Fatalf("scanned = %d, want 5: the scan has to keep counting past the limit", scanned)
	}
	response := buildExplainResponse(sem.ProviderSnapshot{}, candidates, scanned)
	if response.Scanned != 5 {
		t.Fatalf("Scanned = %d, want 5", response.Scanned)
	}
	if response.Omitted != 3 {
		t.Fatalf("Omitted = %d, want 3", response.Omitted)
	}
}

// A name a hundred packages define is not resolved by shape. The error line says which file it is
// about, and using it is the difference between an answer and a coin toss.
func TestExplainResolvesAnAmbiguousNameWithTheErrorsOwnFile(t *testing.T) {
	t.Parallel()
	snapshot := sem.ProviderSnapshot{Symbols: []sem.SymbolRecord{
		// The decoy is WIDER and also carries a signature, so shape alone picks it.
		{ID: "store", Name: "Config", Kind: "struct", Signature: "type Config struct",
			FilePath: "internal/store/config.go", StartLine: 1, EndLine: 400},
		{ID: "api", Name: "Config", Kind: "struct", Signature: "type Config struct",
			FilePath: "internal/api/config.go", StartLine: 10, EndLine: 20},
	}}
	candidates, scanned, err := explainCandidates(
		strings.NewReader("./internal/api/handler.go:12:9: undefined: Config\n"), 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].File != "internal/api/handler.go" {
		t.Fatalf("candidates = %+v, want one carrying the error's file", candidates)
	}
	response := buildExplainResponse(snapshot, candidates, scanned)
	if len(response.Symbols) != 1 {
		t.Fatalf("symbols = %+v", response.Symbols)
	}
	if got := response.Symbols[0].FilePath; got != "internal/api/config.go" {
		t.Fatalf("resolved to %q, want internal/api/config.go: the error named that package", got)
	}
	if got := response.Symbols[0].Candidates; got != 2 {
		t.Fatalf("Candidates = %d, want 2: an ambiguous pick has to say it was a pick", got)
	}
}

// The other half of the same context: when the build says WHICH TYPE the member belongs to, the
// method on that type is the answer and every same-named method elsewhere is noise.
func TestExplainResolvesAnAmbiguousMemberWithTheErrorsOwnType(t *testing.T) {
	t.Parallel()
	snapshot := sem.ProviderSnapshot{Symbols: []sem.SymbolRecord{
		{ID: "writer", Name: "Writer", Kind: "struct", FilePath: "w.go", StartLine: 1, EndLine: 90},
		{ID: "parser", Name: "Parser", Kind: "struct", FilePath: "p.go", StartLine: 1, EndLine: 20},
		{ID: "w.reset", Name: "reset", Kind: "method", Signature: "func (w *Writer) reset()",
			FilePath: "w.go", StartLine: 10, EndLine: 80, ContainerID: "writer"},
		{ID: "p.reset", Name: "reset", Kind: "method", Signature: "func (p *Parser) reset()",
			FilePath: "p.go", StartLine: 5, EndLine: 9, ContainerID: "parser"},
	}}
	candidates, scanned, err := explainCandidates(
		strings.NewReader("./x.go:9:5: p.reset undefined (type *Parser has no field or method reset)\n"), 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Owner != "Parser" {
		t.Fatalf("candidates = %+v, want one carrying owner Parser", candidates)
	}
	response := buildExplainResponse(snapshot, candidates, scanned)
	if got := response.Symbols[0].FilePath; got != "p.go" {
		t.Fatalf("resolved to %q, want p.go: the error blamed *Parser", got)
	}
}

// The compiler's path is relative to wherever it ran; the graph's is relative to the repository
// root. Matching them means matching the shared tail, and only on a component boundary.
func TestExplainPathRankMatchesOnComponentBoundaries(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		symbol, err string
		want        int
	}{
		{"identical", "internal/cli/explain.go", "internal/cli/explain.go", 0},
		{"compiler ran inside the package", "internal/cli/explain.go", "explain.go", 0},
		{"same directory", "internal/cli/explain.go", "internal/cli/root.go", 1},
		{"unrelated", "internal/cli/explain.go", "internal/sem/search.go", 2},
		{"a tail that is not a component", "internal/cli/myexplain.go", "explain.go", 2},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := explainPathRank(testCase.symbol, testCase.err); got != testCase.want {
				t.Fatalf("explainPathRank(%q, %q) = %d, want %d", testCase.symbol, testCase.err, got, testCase.want)
			}
		})
	}
}

// The dedupe set exists to bound what an UNTRUSTED build can make this process remember. It was
// also, silently, deciding how many symbols the CALLER was allowed to ask for: collection stops
// dead once the set is full, so `--max-symbols 1500` resolved 1024 and said nothing about it.
// The two limits answer different questions and only one of them is attacker-controlled.
func TestExplainHonoursAMaxSymbolsAboveTheScannedNameBudget(t *testing.T) {
	t.Parallel()
	const limit = explainMaxScannedNames + 476 // 1500: above the budget, and not a multiple of it
	var build strings.Builder
	for i := 0; i < limit+500; i++ {
		fmt.Fprintf(&build, "./x.go:%d:1: undefined: Sym%d\n", i+1, i)
	}
	candidates, scanned, err := explainCandidates(strings.NewReader(build.String()), limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != limit {
		t.Fatalf("candidates = %d, want %d: --max-symbols above %d must not be silently clamped to it",
			len(candidates), limit, explainMaxScannedNames)
	}
	// Every one of them distinct: the set still has to dedupe across the whole raised range.
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if seen[candidate.Name] {
			t.Fatalf("duplicate candidate %q: the raised cap must not disable dedupe", candidate.Name)
		}
		seen[candidate.Name] = true
	}
	if scanned < limit {
		t.Fatalf("scanned = %d, want >= %d", scanned, limit)
	}
}

// The other half: with a limit at or below the budget, the budget still binds, because THAT is the
// case where the build rather than the caller is choosing how much is remembered.
func TestExplainStillBoundsWhatAnUntrustedBuildCanMakeItRemember(t *testing.T) {
	t.Parallel()
	var build strings.Builder
	for i := 0; i < explainMaxScannedNames+4000; i++ {
		fmt.Fprintf(&build, "./x.go:%d:1: undefined: Sym%d\n", i+1, i)
	}
	_, scanned, err := explainCandidates(strings.NewReader(build.String()), 8)
	if err != nil {
		t.Fatal(err)
	}
	if scanned != explainMaxScannedNames {
		t.Fatalf("scanned = %d, want it to saturate at %d", scanned, explainMaxScannedNames)
	}
}
