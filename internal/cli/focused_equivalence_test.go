package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// TestLookupsAnswerIdenticallyFromSidecarAndFullLoad pins that every lookup verb
// prints the same answer whether its graph is read from the sqlite sidecar
// (ENTIRE_GRAPH_SEARCH_STORE=1) or from the gz entry (=0), for any way of
// naming a symbol. It holds no expected literals: the gz entry is the oracle,
// so a focused read that drops a record or a header count the verb uses shows
// up as a difference between two public CLI outputs.
//
// Each fixture is copied into a temp git repository and indexed once with the
// store enabled. Every lookup then runs in-process through Run, with --head and
// the same --cache-dir, first with the store on and then with it off.
//
// The default is a smoke sample; run FULL (ENTIRE_GRAPH_EQUIV_FULL=1) before
// changing focused loading.
//   - default: each guard-only fixture under testdata/focus-guard takes, from
//     every source file, its first top-level type and that type's first member,
//     or the file's first symbol when it declares no type. Each of those is
//     looked up by plain name with def and with one other verb, and by its
//     upper-cased name, so every case-fold collision among them is reached.
//     Every other name form meets every verb (neighbors, def, impact, explain)
//     at least once, spread across the symbols. Direction, depth and format
//     rotate per verb. Each chosen symbol also runs neighbors --depth 2 in
//     both directions, the one variant that prints two-hop paths, and
//     neighbors --relation EXTENDS; the other extra variants (neighbors
//     --exclude-tests, --internal-only and --profile fast, impact
//     --exclude-tests and --profile fast, def and explain --profile fast)
//     rotate across the symbols. Every other fixture runs one fixed-seed
//     symbol by its plain name through one verb, rotating verb, variant and
//     format across fixtures.
//   - FULL: every symbol of every fixture. The plain name runs every verb in
//     every direction, depth and format, plus neighbors --depth 2 in both
//     directions and every extra variant in json; every other form runs every
//     verb once, in rotation.
//   - both: focus-edge-cases also runs its pinned lookups (pinned).
//
// A fixture that runs --profile fast lookups is indexed under both profiles,
// so those lookups read a fast sidecar too.
//
// Explain takes no symbol flag, so a form reaches it as the name in a
// compiler-style "file:line:1: undefined: Name" line, with --file and --line
// moved into that location. It runs with --no-echo, so it prints only what it
// resolved.
//
// Equality holds trivially when both sides fail or print nothing, so each
// fixture must also have at least one plain-name lookup that succeeded with
// non-empty output on both sides ("answered").
//
// Normalization, and nothing else:
//   - json: decoded, then every key ending "_latency_ms" or starting
//     "index_cache_" is deleted at any depth before reflect.DeepEqual. def json
//     runs with --max-context-bytes 0, because its fitter measures the encoded
//     latency fields, so a budgeted answer could differ by latency width alone.
//   - text/agent: the measured latencies in these telemetry lines are masked:
//     "Index: cache-<state> (<n>ms) | Query: <n>ms | Total: <n>ms", the
//     neighbors text/agent header and the impact text footer
//     (relationLatencyHeader), and "I:<state>/<n> Q:<n> T:<n>", the compact
//     neighbors agent form (compactAgentNeighbors). def and explain text print
//     no telemetry.
//
// The returned error is compared too. stderr is not compared: it carries
// progress, and the load trace (ENTIRE_GRAPH_TRACE_LOAD, set through
// EntireEnv.TraceLoad) that says which load served the lookup. The guard
// searches each lookup's own stderr for that line: with the store off every
// lookup must take the full load, and with it on every lookup that names an
// existing symbol in a form exact resolution selects (wantFocused) must be
// served by the focused load. Without that, a focused read that failed quietly
// and fell back to the full load would print identical output and pass.
//
// -short skips the guard.
func TestLookupsAnswerIdenticallyFromSidecarAndFullLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("differential sidecar/full-load guard; runs without -short")
	}
	t.Setenv("ENTIRE_GRAPH_SEARCH_STORE", "1")
	exhaustive := os.Getenv("ENTIRE_GRAPH_EQUIV_FULL") == "1"

	fixtures := focusGuardFixtureDirs(t)
	for _, fixture := range fixtures {
		// The fixtures that run the extra variants (extraVariants).
		fixture.fast = exhaustive || fixture.guardOnly
	}
	root := t.TempDir()
	if err := focusGuardEach(len(fixtures), func(index int) error {
		return fixtures[index].prepare(root)
	}); err != nil {
		t.Fatal(err)
	}

	var lookups []*focusGuardLookup
	ordinal := 0
	for _, fixture := range fixtures {
		lookups = append(lookups, fixture.lookups(exhaustive, ordinal)...)
		if !fixture.guardOnly {
			ordinal++
		}
	}
	t.Logf("%d lookups per pass across %d fixtures", len(lookups), len(fixtures))
	run := func(answer func(*focusGuardLookup) *focusGuardAnswer) {
		_ = focusGuardEach(len(lookups), func(index int) error {
			*answer(lookups[index]) = lookups[index].run()
			return nil
		})
	}
	run(func(lookup *focusGuardLookup) *focusGuardAnswer { return &lookup.sidecar })
	t.Setenv("ENTIRE_GRAPH_SEARCH_STORE", "0")
	run(func(lookup *focusGuardLookup) *focusGuardAnswer { return &lookup.full })
	t.Setenv("ENTIRE_GRAPH_SEARCH_STORE", "1")

	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			// The comparison means something only if the store answered the
			// first pass, so it must still match the entry after both passes.
			if !fixture.storeAvailable() {
				t.Fatal("the sidecar no longer matches the entry, so the store pass may have been served from gz")
			}
			if len(fixture.symbols) == 0 {
				t.Skip("no symbols to look up")
			}
			answered := false
			// extras holds, per extra variant this fixture ran, whether any of
			// its lookups answered on both sides.
			extras := map[string]bool{}
			for _, lookup := range lookups {
				if lookup.fixture != fixture {
					continue
				}
				answered = answered || (lookup.plain && lookup.answered())
				if lookup.extra != "" {
					extras[lookup.extra] = extras[lookup.extra] || lookup.answered()
				}
				if !lookup.equal() {
					t.Errorf("%s: entire graph %s\n%s", fixture.name, strings.Join(lookup.displayArgs(), " "), lookup.diff())
				}
				if lookup.loadedNothing() {
					continue
				}
				if lookup.full.load != "full" {
					t.Errorf("%s: entire graph %s: with the store off the load was %q, want full",
						fixture.name, strings.Join(lookup.displayArgs(), " "), lookup.full.load)
				}
				if lookup.wantFocused && lookup.sidecar.load != "focused" {
					t.Errorf("%s: entire graph %s: the name resolves exactly, yet with the store on the load was %q, want focused",
						fixture.name, strings.Join(lookup.displayArgs(), " "), lookup.sidecar.load)
				}
			}
			// Two sides that fail, or print nothing, in the same way are equal,
			// so equality alone would pass if every lookup broke.
			if !answered {
				t.Errorf("%s: no plain-name lookup succeeded with output on both sides", fixture.name)
			}
			// The same holds for each extra variant on its own: one that
			// failed, or printed nothing, alike on both sides would pass.
			for _, name := range slices.Sorted(maps.Keys(extras)) {
				if !extras[name] {
					t.Errorf("%s: no %s lookup succeeded with output on both sides", fixture.name, name)
				}
			}
		})
	}
}

const (
	focusGuardSeed    = 20261103
	focusGuardVersion = "focus-guard-test"
)

type focusGuardFixture struct {
	name, source, repo, cache string
	// guardOnly fixtures exist for this guard and get every name form by default.
	guardOnly bool
	// history adds co-change commits before the index and an edit/revert pair
	// after it, so lookups run on a cache hit whose history has moved on.
	history bool
	// fast fixtures run lookups with --profile fast, so prepare indexes that
	// cache variant as well and storeAvailable checks its sidecar too.
	fast    bool
	symbols []focusGuardSymbol
}

type focusGuardSymbol struct {
	RecordType    string `json:"record_type"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Kind          string `json:"kind"`
	FilePath      string `json:"file_path"`
	StartLine     int    `json:"start_line"`
	ContainerID   string `json:"container_id"`
}

func focusGuardFixtureDirs(t *testing.T) []*focusGuardFixture {
	t.Helper()
	var fixtures []*focusGuardFixture
	add := func(pattern string, guardOnly bool) {
		dirs, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, dir := range dirs {
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Base(dir)
			if !info.IsDir() {
				continue
			}
			fixtures = append(fixtures, &focusGuardFixture{
				name: name, source: dir, guardOnly: guardOnly, history: name == "focus-edge-cases",
			})
		}
	}
	add(filepath.Join("..", "sem", "testdata", "fixtures", "*"), false)
	add(filepath.Join("testdata", "focus-guard", "*"), true)
	if len(fixtures) == 0 {
		t.Fatal("no fixtures found")
	}
	// The history commits and the pinned lookups are keyed on this fixture's
	// name; without it they would be skipped without a word.
	if !slices.ContainsFunc(fixtures, func(f *focusGuardFixture) bool { return f.history }) {
		t.Fatal("no focus-edge-cases fixture under testdata/focus-guard")
	}
	return fixtures
}

func (f *focusGuardFixture) prepare(root string) error {
	f.repo = filepath.Join(root, f.name)
	f.cache = filepath.Join(root, f.name+".cache")
	if err := focusGuardCopyTree(f.source, f.repo); err != nil {
		return err
	}
	git := func(args ...string) error { return focusGuardGit(f.repo, args...) }
	for _, args := range [][]string{
		{"init", "-q", "--template="},
		{"config", "user.email", "fixture@example.test"},
		{"config", "user.name", "Fixture"},
		{"config", "core.hooksPath", "/dev/null"},
		{"config", "commit.gpgsign", "false"},
		{"add", "-A"},
		{"commit", "-qm", "fixture"},
	} {
		if err := git(args...); err != nil {
			return err
		}
	}
	if f.history {
		// Three commits changing a.go and b.go together meet the co-change
		// threshold of two.
		for round := 1; round <= 3; round++ {
			if err := f.touchAndCommit(fmt.Sprintf("co-change %d", round), "a.go", "b.go"); err != nil {
				return err
			}
		}
	}
	for _, profile := range f.profiles() {
		if _, err := f.cli("", "index", "--repo", f.repo, "--head", "--cache-dir", f.cache, "--profile", profile, "--format", "json"); err != nil {
			return fmt.Errorf("%s: index --profile %s: %w", f.name, profile, err)
		}
	}
	if f.history {
		// Same tree as the index (a cache hit), different history.
		if err := f.touchAndCommit("edit", "a.go", "c.go"); err != nil {
			return err
		}
		if err := git("revert", "--no-edit", "HEAD"); err != nil {
			return err
		}
	}
	if err := git("gc", "-q"); err != nil {
		return err
	}
	if !f.storeAvailable() {
		return fmt.Errorf("%s: index built no sidecar", f.name)
	}
	stream, err := f.cli("", "symbols", "--repo", f.repo, "--profile", "full", "--format", "ndjson")
	if err != nil {
		return fmt.Errorf("%s: symbols: %w", f.name, err)
	}
	decoder := json.NewDecoder(strings.NewReader(stream))
	for {
		var symbol focusGuardSymbol
		if err := decoder.Decode(&symbol); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return fmt.Errorf("%s: symbols: %w", f.name, err)
		}
		if symbol.RecordType == "symbol" {
			f.symbols = append(f.symbols, symbol)
		}
	}
	sort.Slice(f.symbols, func(i, j int) bool { return f.symbols[i].ID < f.symbols[j].ID })
	return nil
}

func (f *focusGuardFixture) touchAndCommit(message string, paths ...string) error {
	for _, path := range paths {
		file, err := os.OpenFile(filepath.Join(f.repo, path), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(file, "// %s\n", message)
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	if err := focusGuardGit(f.repo, append([]string{"add", "--"}, paths...)...); err != nil {
		return err
	}
	return focusGuardGit(f.repo, "commit", "-qm", message)
}

// profiles are the cache variants the fixture's lookups read, each indexed
// by prepare.
func (f *focusGuardFixture) profiles() []string {
	if f.fast {
		return []string{"full", "fast"}
	}
	return []string{"full"}
}

func (f *focusGuardFixture) storeAvailable() bool {
	for _, profile := range f.profiles() {
		out, err := f.cli("", "store", "--repo", f.repo, "--cache-dir", f.cache, "--profile", profile)
		var descriptor struct {
			Unavailable bool `json:"store_unavailable"`
		}
		if err != nil || json.Unmarshal([]byte(out), &descriptor) != nil || descriptor.Unavailable {
			return false
		}
	}
	return true
}

// cli runs one command in-process. ENTIRE_PLUGIN_DATA_DIR is the fixture's
// cache, so a command without --cache-dir cannot touch the user's cache.
func (f *focusGuardFixture) cli(stdin string, args ...string) (string, error) {
	stdout, _, err := f.cliTraced(stdin, args...)
	return stdout, err
}

// cliTraced is cli with the load trace on, also returning the command's own
// stderr: lookups run concurrently, so each captures its own.
func (f *focusGuardFixture) cliTraced(stdin string, args ...string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Version: focusGuardVersion,
		Env:     EntireEnv{PluginDataDir: f.cache, TraceLoad: true},
		Stdout:  &stdout,
		Stderr:  &stderr,
		Stdin:   strings.NewReader(stdin),
	}, args)
	return stdout.String(), stderr.String(), err
}

// focusGuardLoad is the load a lookup's stderr trace names: "focused", "full",
// or "" when it printed no trace line.
func focusGuardLoad(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		switch strings.TrimSuffix(line, "\r") {
		case "entire-graph: index load focused":
			return "focused"
		case "entire-graph: index load full":
			return "full"
		}
	}
	return ""
}

// sample is a fixed-seed choice of n symbols, or all of them when there are
// no more than n.
func (f *focusGuardFixture) sample(n int) []focusGuardSymbol {
	if len(f.symbols) <= n {
		return f.symbols
	}
	picked := append([]focusGuardSymbol(nil), f.symbols...)
	random := rand.New(rand.NewPCG(focusGuardSeed, uint64(len(picked))))
	random.Shuffle(len(picked), func(i, j int) { picked[i], picked[j] = picked[j], picked[i] })
	return picked[:n]
}

// representatives is a deterministic choice of at least one symbol per source
// file: the file's first top-level type (a symbol other symbols name as their
// container) together with its first member, or the file's first symbol when
// it declares no type. Types and members are where a focused subgraph has to
// carry containment, inheritance, override and extension edges.
func (f *focusGuardFixture) representatives() []focusGuardSymbol {
	ordered := append([]focusGuardSymbol(nil), f.symbols...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].FilePath != ordered[j].FilePath {
			return ordered[i].FilePath < ordered[j].FilePath
		}
		return ordered[i].StartLine < ordered[j].StartLine
	})
	containers := map[string]bool{}
	for _, symbol := range ordered {
		containers[symbol.ContainerID] = true
	}
	var picked []focusGuardSymbol
	for start := 0; start < len(ordered); {
		end := start
		for end < len(ordered) && ordered[end].FilePath == ordered[start].FilePath {
			end++
		}
		file := ordered[start:end]
		chosen := []focusGuardSymbol{file[0]}
		for _, symbol := range file {
			if symbol.ContainerID == "" && containers[symbol.ID] {
				chosen = []focusGuardSymbol{symbol}
				for _, member := range ordered {
					if member.ContainerID == symbol.ID {
						chosen = append(chosen, member)
						break
					}
				}
				break
			}
		}
		picked = append(picked, chosen...)
		start = end
	}
	return picked
}

// nameForm is one way of addressing a symbol: the --symbol value (positional
// for def) plus the selector flags that go with it.
type focusGuardNameForm struct {
	symbol string
	flags  []string
	// exact is a name of the symbol with selectors that the symbol itself
	// meets, so every verb resolves it exactly and the store pass must be
	// served by the focused load.
	exact bool
}

// focusGuardNameFormKinds are the ways of naming a symbol, plain name first.
// A kind reports false where it would only repeat the plain name.
var focusGuardNameFormKinds = []func(focusGuardSymbol) (focusGuardNameForm, bool){
	func(s focusGuardSymbol) (focusGuardNameForm, bool) {
		return focusGuardNameForm{symbol: s.Name, exact: true}, true
	},
	// Exact only when the upper-cased name folds back to the name: ToUpper
	// maps the dotless ı to I, which EqualFold does not fold to ı, so exact
	// resolution misses and the full load answers.
	func(s focusGuardSymbol) (focusGuardNameForm, bool) {
		upper := strings.ToUpper(s.Name)
		return focusGuardNameForm{symbol: upper, exact: strings.EqualFold(upper, s.Name)}, upper != s.Name
	},
	func(s focusGuardSymbol) (focusGuardNameForm, bool) {
		return focusGuardNameForm{symbol: s.QualifiedName}, s.QualifiedName != "" && s.QualifiedName != s.Name
	},
	func(s focusGuardSymbol) (focusGuardNameForm, bool) { return focusGuardNameForm{symbol: s.ID}, true },
	func(s focusGuardSymbol) (focusGuardNameForm, bool) {
		return focusGuardNameForm{symbol: s.Name, flags: []string{"--file", s.FilePath}, exact: true}, true
	},
	func(s focusGuardSymbol) (focusGuardNameForm, bool) {
		return focusGuardNameForm{symbol: s.Name, flags: []string{"--file", "wrong.go"}}, true
	},
	func(s focusGuardSymbol) (focusGuardNameForm, bool) {
		return focusGuardNameForm{symbol: s.Name, flags: []string{"--kind", s.Kind}, exact: true}, true
	},
	func(s focusGuardSymbol) (focusGuardNameForm, bool) {
		return focusGuardNameForm{symbol: s.Name, flags: []string{"--kind", "class"}}, s.Kind != "class"
	},
	func(s focusGuardSymbol) (focusGuardNameForm, bool) {
		return focusGuardNameForm{symbol: s.Name, flags: []string{"--line", strconv.Itoa(s.StartLine)}, exact: true}, true
	},
	func(s focusGuardSymbol) (focusGuardNameForm, bool) {
		return focusGuardNameForm{symbol: s.Name, flags: []string{"--line", "1"}}, true
	},
	func(s focusGuardSymbol) (focusGuardNameForm, bool) {
		return focusGuardNameForm{symbol: s.FilePath + ":" + strconv.Itoa(s.StartLine)}, true
	},
	func(s focusGuardSymbol) (focusGuardNameForm, bool) {
		_, size := utf8.DecodeLastRuneInString(s.Name)
		return focusGuardNameForm{symbol: s.Name[:len(s.Name)-size]}, size < len(s.Name)
	},
}

var (
	focusGuardNeighborVariants = [][]string{
		{"--direction", "in", "--depth", "1"},
		{"--direction", "out", "--depth", "1"},
		{"--direction", "in", "--depth", "2"},
		{"--direction", "out", "--depth", "2"},
	}
	// focusGuardNeighborPathsVariant is the only one that prints two-hop paths.
	focusGuardNeighborPathsVariant = []string{"--direction", "both", "--depth", "2"}
	focusGuardImpactVariants       = [][]string{{"--depth", "1"}, {"--depth", "2"}}
	// focusGuardNeighborExtraVariants and focusGuardImpactExtraVariants change
	// what the focus must carry rather than how far it reaches: a relation
	// outside the call family, the two result filters, and the fast profile,
	// whose cache variant prepare indexes too (focusGuardFixture.fast).
	focusGuardNeighborExtraVariants = [][]string{
		{"--relation", "EXTENDS"},
		{"--exclude-tests"},
		{"--internal-only"},
		focusGuardFastProfile,
	}
	focusGuardImpactExtraVariants = [][]string{{"--exclude-tests"}, focusGuardFastProfile}
	focusGuardFastProfile         = []string{"--profile", "fast"}
	focusGuardThreeFormats        = []string{"json", "agent", "text"}
	focusGuardTwoFormats          = []string{"json", "text"}
	focusGuardVerbs               = []string{"neighbors", "def", "impact", "explain"}
)

// focusGuardBuilder accumulates one fixture's lookups. Each verb keeps its own
// counter, and a lookup made without an explicit variant or format takes the
// next one in rotation: twelve neighbors lookups cover all four
// direction/depth variants in all three formats, four impact lookups cover
// both depths in both formats, and three def or explain lookups cover their
// formats.
type focusGuardBuilder struct {
	fixture *focusGuardFixture
	lookups []*focusGuardLookup
	turns   map[string]int
}

func (b *focusGuardBuilder) add(verb, format, stdin string, args ...string) {
	full := []string{verb, "--repo", b.fixture.repo, "--head", "--cache-dir", b.fixture.cache}
	full = append(full, args...)
	if verb == "def" && format == "json" {
		full = append(full, "--max-context-bytes", "0")
	}
	if verb == "explain" {
		// Without the echo, explain prints only what it resolved, so an
		// answer that resolved nothing is empty rather than the input again.
		full = append(full, "--no-echo")
	}
	full = append(full, "--format", format)
	b.lookups = append(b.lookups, &focusGuardLookup{fixture: b.fixture, args: full, stdin: stdin, json: format == "json"})
}

// plain marks the lookups added by add as plain-name lookups of a symbol that
// exists, which the guard requires at least one of to answer, and which the
// focused load must serve.
func (b *focusGuardBuilder) plain(add func()) {
	start := len(b.lookups)
	b.focused(add)
	for _, lookup := range b.lookups[start:] {
		lookup.plain = true
	}
}

// focused marks the lookups added by add as ones the focused load must serve
// when the store is on.
func (b *focusGuardBuilder) focused(add func()) {
	start := len(b.lookups)
	add()
	for _, lookup := range b.lookups[start:] {
		lookup.wantFocused = true
	}
}

// rotated runs verb once for form, in the verb's next variant and format.
func (b *focusGuardBuilder) rotated(verb string, symbol focusGuardSymbol, form focusGuardNameForm) {
	switch {
	case symbol.ID != "" && form.symbol == symbol.Name && len(form.flags) == 0:
		b.plain(func() { b.rotatedForm(verb, symbol, form) })
	case symbol.ID != "" && form.exact:
		b.focused(func() { b.rotatedForm(verb, symbol, form) })
	default:
		b.rotatedForm(verb, symbol, form)
	}
}

func (b *focusGuardBuilder) rotatedForm(verb string, symbol focusGuardSymbol, form focusGuardNameForm) {
	turn := b.turns[verb]
	b.turns[verb] = turn + 1
	switch verb {
	case "neighbors":
		b.neighbors(form, focusGuardNeighborVariants[turn%4], focusGuardThreeFormats[turn%3])
	case "impact":
		b.impact(form, focusGuardImpactVariants[turn%2], focusGuardTwoFormats[(turn/2)%2])
	case "def":
		b.def(append([]string{form.symbol}, form.flags...), focusGuardThreeFormats[turn%3])
	case "explain":
		b.explain(symbol, form, focusGuardThreeFormats[turn%3])
	}
}

func (b *focusGuardBuilder) neighbors(form focusGuardNameForm, variant []string, formats ...string) {
	args := append(append([]string{"--symbol", form.symbol}, form.flags...), variant...)
	for _, format := range formats {
		b.add("neighbors", format, "", args...)
	}
}

func (b *focusGuardBuilder) impact(form focusGuardNameForm, variant []string, formats ...string) {
	args := append(append([]string{"--symbol", form.symbol}, form.flags...), variant...)
	for _, format := range formats {
		b.add("impact", format, "", args...)
	}
}

func (b *focusGuardBuilder) def(args []string, formats ...string) {
	for _, format := range formats {
		b.add("def", format, "", args...)
	}
}

// explain names the form in a compiler-style error line. Explain takes no
// selector flags, so --file and --line move into the line's location, which
// is what explain ranks candidates by.
func (b *focusGuardBuilder) explain(symbol focusGuardSymbol, form focusGuardNameForm, formats ...string) {
	b.explainWith(symbol, form, nil, formats...)
}

// explainWith is explain with extra command flags, such as --profile.
func (b *focusGuardBuilder) explainWith(symbol focusGuardSymbol, form focusGuardNameForm, args []string, formats ...string) {
	file, line := symbol.FilePath, strconv.Itoa(symbol.StartLine)
	for index := 0; index+1 < len(form.flags); index += 2 {
		switch form.flags[index] {
		case "--file":
			file = form.flags[index+1]
		case "--line":
			line = form.flags[index+1]
		}
	}
	stdin := fmt.Sprintf("%s:%s:1: undefined: %s\n", file, line, form.symbol)
	for _, format := range formats {
		b.add("explain", format, stdin, args...)
	}
}

// extraVariants runs the extra variants by plain name, with def and explain
// under the fast profile too. FULL runs each of them on every symbol. The
// default sample runs neighbors --relation EXTENDS on every symbol, since
// inheritance sits on the types it picks, and rotates the rest across the
// symbols, wrapping past the last one so each is reached at least once.
func (b *focusGuardBuilder) extraVariants(symbols []focusGuardSymbol, all bool) {
	neighborRest := focusGuardNeighborExtraVariants[1:]
	rounds := len(symbols)
	if !all {
		rounds = max(len(symbols), len(neighborRest))
	}
	for index := range rounds {
		symbol := symbols[index%len(symbols)]
		plain, _ := focusGuardNameFormKinds[0](symbol)
		fast := strings.Join(focusGuardFastProfile, " ")
		neighbors := func(variant []string, format string) {
			b.extra("neighbors "+strings.Join(variant, " "), func() { b.neighbors(plain, variant, format) })
		}
		impact := func(variant []string, format string) {
			b.extra("impact "+strings.Join(variant, " "), func() { b.impact(plain, variant, format) })
		}
		fastDef := func(format string) {
			b.extra("def "+fast, func() { b.def(append([]string{plain.symbol}, focusGuardFastProfile...), format) })
		}
		fastExplain := func(format string) {
			b.extra("explain "+fast, func() { b.explainWith(symbol, plain, focusGuardFastProfile, format) })
		}
		if all {
			for _, variant := range focusGuardNeighborExtraVariants {
				neighbors(variant, "json")
			}
			for _, variant := range focusGuardImpactExtraVariants {
				impact(variant, "json")
			}
			fastDef("json")
			fastExplain("json")
			continue
		}
		if index < len(symbols) {
			neighbors(focusGuardNeighborExtraVariants[0], "json")
		}
		neighbors(neighborRest[index%len(neighborRest)], focusGuardThreeFormats[index%3])
		impact(focusGuardImpactExtraVariants[index%2], focusGuardTwoFormats[(index/2)%2])
		if index%2 == 0 {
			fastDef(focusGuardThreeFormats[(index/2)%3])
		} else {
			fastExplain(focusGuardThreeFormats[(index/2)%3])
		}
	}
}

// extra tags the lookups added by add with the extra variant they run, which
// the guard requires to have answered at least once per fixture.
func (b *focusGuardBuilder) extra(name string, add func()) {
	start := len(b.lookups)
	add()
	for _, lookup := range b.lookups[start:] {
		lookup.extra = name
	}
}

// lookups is the fixture's share of the guard. ordinal is the fixture's
// position among the non-guard fixtures, which rotates their one lookup.
func (f *focusGuardFixture) lookups(exhaustive bool, ordinal int) []*focusGuardLookup {
	if len(f.symbols) == 0 {
		return nil
	}
	b := &focusGuardBuilder{fixture: f, turns: map[string]int{}}
	switch {
	case exhaustive:
		b.everyForm(f.symbols)
	case f.guardOnly:
		b.everyFormKind(f.representatives())
	default:
		// One symbol by its plain name, one verb; the verb, its variant and
		// its format rotate across fixtures so the set covers all of them.
		symbol := f.sample(1)[0]
		plain, _ := focusGuardNameFormKinds[0](symbol)
		verb := focusGuardVerbs[ordinal%len(focusGuardVerbs)]
		b.turns[verb] = ordinal / len(focusGuardVerbs)
		b.rotated(verb, symbol, plain)
	}
	if f.name == "focus-edge-cases" {
		b.pinned()
	}
	return b.lookups
}

// pinned are the focus-edge-cases lookups of particular symbols, each served
// by the focused load.
//   - Shared is defined in left/shared.go and right/shared.go, and --file picks
//     one: as stored, in another letter case (exact resolution compares paths
//     under EqualFold), and absolute (normalised against the repository root).
//     The focused starting set is narrowed by --file, so this catches a
//     narrowing that disagrees with exact resolution.
//   - KelvinTemp starts with the Kelvin sign (U+212A), which folds to an ASCII
//     k, so kelvintemp finds it only through a full Unicode case fold. This
//     catches a stored name fold that is narrower than strings.EqualFold.
func (b *focusGuardBuilder) pinned() {
	b.focused(func() {
		for _, file := range []string{
			"left/shared.go",
			"LEFT/Shared.GO",
			filepath.Join(b.fixture.repo, "left", "shared.go"),
		} {
			b.impact(focusGuardNameForm{symbol: "Shared", flags: []string{"--file", file}}, nil, focusGuardTwoFormats...)
		}
		b.def([]string{"--symbol", "kelvintemp"}, focusGuardThreeFormats...)
	})
}

// everyFormKind looks up every chosen symbol by its plain name with def, by
// its plain name with one more verb, and by its upper-cased name, so every
// symbol, and every case-fold collision between them, is reached. Every other
// name form then meets every verb once, spread across the symbols. Finally it
// adds the def rewrites, an unknown name and a three-name def.
func (b *focusGuardBuilder) everyFormKind(symbols []focusGuardSymbol) {
	plainKind, upperKind := focusGuardNameFormKinds[0], focusGuardNameFormKinds[1]
	// Wrapping past the last symbol keeps every verb in each rotation even
	// when there are fewer symbols than verbs.
	others := []string{"neighbors", "impact", "explain"}
	for index := range max(len(symbols), len(others)) {
		symbol := symbols[index%len(symbols)]
		plain, _ := plainKind(symbol)
		if index < len(symbols) {
			b.rotated("def", symbol, plain)
		}
		b.rotated(others[index%len(others)], symbol, plain)
	}
	var upperSymbols []focusGuardSymbol
	var upperForms []focusGuardNameForm
	for _, symbol := range symbols {
		if upper, ok := upperKind(symbol); ok {
			upperSymbols, upperForms = append(upperSymbols, symbol), append(upperForms, upper)
		}
	}
	for index := range max(len(upperSymbols), len(focusGuardVerbs)) {
		if len(upperSymbols) == 0 {
			break
		}
		at := index % len(upperSymbols)
		b.rotated(focusGuardVerbs[index%len(focusGuardVerbs)], upperSymbols[at], upperForms[at])
	}
	for kindIndex, kind := range focusGuardNameFormKinds[2:] {
		for verbIndex, verb := range focusGuardVerbs {
			// Start at a different symbol per verb, and fall through to the
			// next symbol when this form would only repeat the plain name.
			for offset := range symbols {
				symbol := symbols[(kindIndex+verbIndex+offset)%len(symbols)]
				if form, ok := kind(symbol); ok {
					b.rotated(verb, symbol, form)
					break
				}
			}
		}
	}
	plain, _ := focusGuardNameFormKinds[0](symbols[0])
	b.neighbors(plain, []string{"--relation", "USES_TYPE"}, "json")
	b.extraVariants(symbols, false)
	// Two-hop paths pair a focus's incoming and outgoing edges, so only
	// --direction both (the default) at --depth 2 prints them.
	for _, symbol := range symbols {
		plain, _ := focusGuardNameFormKinds[0](symbol)
		b.neighbors(plain, focusGuardNeighborPathsVariant, "json")
	}
	for _, symbol := range symbols {
		if strings.Contains(symbol.QualifiedName, ".") {
			for index, separator := range []string{"::", "->", "#"} {
				b.def([]string{strings.ReplaceAll(symbol.QualifiedName, ".", separator)}, focusGuardThreeFormats[index])
			}
			break
		}
	}
	b.unknownAndMultiName(symbols, false)
}

// everyForm is the exhaustive matrix: the plain name runs every verb in every
// variant and format, and every other form runs each verb once in rotation.
func (b *focusGuardBuilder) everyForm(symbols []focusGuardSymbol) {
	for _, symbol := range symbols {
		forms := focusGuardNameForms(symbol)
		plain := forms[0]
		b.plain(func() {
			for _, variant := range focusGuardNeighborVariants {
				b.neighbors(plain, variant, focusGuardThreeFormats...)
			}
			b.neighbors(plain, []string{"--relation", "USES_TYPE"}, "json")
			b.neighbors(plain, focusGuardNeighborPathsVariant, "json")
			for _, variant := range focusGuardImpactVariants {
				b.impact(plain, variant, focusGuardTwoFormats...)
			}
			b.def([]string{plain.symbol}, focusGuardThreeFormats...)
			b.explain(symbol, plain, focusGuardThreeFormats...)
		})
		for _, form := range forms[1:] {
			for _, verb := range focusGuardVerbs {
				b.rotated(verb, symbol, form)
			}
		}
		if strings.Contains(symbol.QualifiedName, ".") {
			for index, separator := range []string{"::", "->", "#"} {
				b.def([]string{strings.ReplaceAll(symbol.QualifiedName, ".", separator)}, focusGuardThreeFormats[index])
			}
		}
	}
	b.extraVariants(symbols, true)
	b.unknownAndMultiName(symbols, true)
}

// unknownAndMultiName adds zzNoSuchSymbol for every verb and a three-name def,
// in every format when all is set and in the next rotated format otherwise.
func (b *focusGuardBuilder) unknownAndMultiName(symbols []focusGuardSymbol, all bool) {
	missing := focusGuardNameForm{symbol: "zzNoSuchSymbol"}
	nobody := focusGuardSymbol{Name: missing.symbol, FilePath: "wrong.go", StartLine: 1}
	if all {
		b.neighbors(missing, focusGuardNeighborVariants[0], focusGuardThreeFormats...)
		b.impact(missing, focusGuardImpactVariants[1], focusGuardTwoFormats...)
		b.def([]string{missing.symbol}, focusGuardThreeFormats...)
		b.explain(nobody, missing, focusGuardThreeFormats...)
	} else {
		for _, verb := range focusGuardVerbs {
			b.rotated(verb, nobody, missing)
		}
	}
	if len(symbols) < 3 {
		return
	}
	names := []string{symbols[0].Name, symbols[1].Name, symbols[2].Name}
	if all {
		b.def(names, focusGuardThreeFormats...)
		return
	}
	b.def(names, focusGuardThreeFormats[b.turns["def"]%3])
}

// focusGuardNameForms is every distinct form of one symbol, plain name first.
func focusGuardNameForms(symbol focusGuardSymbol) []focusGuardNameForm {
	var forms []focusGuardNameForm
	seen := map[string]bool{}
	for _, kind := range focusGuardNameFormKinds {
		form, ok := kind(symbol)
		key := form.symbol + "\x00" + strings.Join(form.flags, "\x00")
		if ok && form.symbol != "" && !seen[key] {
			seen[key] = true
			forms = append(forms, form)
		}
	}
	return forms
}

type focusGuardLookup struct {
	fixture *focusGuardFixture
	args    []string
	stdin   string
	json    bool
	// plain is a lookup of an existing symbol by its plain name.
	plain bool
	// wantFocused is a lookup of an existing symbol in a form its verb
	// resolves exactly, which the focused load must serve when the store is on.
	wantFocused bool
	// extra is the extra variant the lookup runs (extraVariants), or "".
	extra string
	// sidecar is the answer with the store enabled, full the one without.
	sidecar, full focusGuardAnswer
}

type focusGuardAnswer struct {
	stdout string
	err    string
	// load is the load the lookup's trace named (focusGuardLoad).
	load string
}

func (l *focusGuardLookup) run() focusGuardAnswer {
	stdout, stderr, err := l.fixture.cliTraced(l.stdin, l.args...)
	answer := focusGuardAnswer{stdout: stdout, load: focusGuardLoad(stderr)}
	if err != nil {
		answer.err = err.Error()
	}
	return answer
}

// loadedNothing reports an explain that loaded no graph on either side: it
// returns before loading when the build output names nothing its parser takes
// for a symbol (such as an identifier starting with the Kelvin sign), so it
// prints neither a trace nor an answer.
func (l *focusGuardLookup) loadedNothing() bool {
	none := func(answer focusGuardAnswer) bool {
		return answer.load == "" && answer.stdout == "" && answer.err == ""
	}
	return len(l.args) > 0 && l.args[0] == "explain" && none(l.sidecar) && none(l.full)
}

// answered reports that both sides succeeded and printed something.
func (l *focusGuardLookup) answered() bool {
	return l.sidecar.err == "" && l.full.err == "" && l.sidecar.stdout != "" && l.full.stdout != ""
}

func (l *focusGuardLookup) equal() bool {
	if l.sidecar.err != l.full.err {
		return false
	}
	if l.json {
		sidecar, sidecarErr := focusGuardDecodeJSON(l.sidecar.stdout)
		full, fullErr := focusGuardDecodeJSON(l.full.stdout)
		if sidecarErr == nil && fullErr == nil {
			return reflect.DeepEqual(sidecar, full)
		}
	}
	return focusGuardMaskTelemetry(l.sidecar.stdout) == focusGuardMaskTelemetry(l.full.stdout)
}

// displayArgs elides the temp paths, which are the same in both runs.
func (l *focusGuardLookup) displayArgs() []string {
	shown := make([]string, len(l.args))
	for index, arg := range l.args {
		switch arg {
		case l.fixture.repo:
			arg = "<repo>"
		case l.fixture.cache:
			arg = "<cache>"
		}
		shown[index] = strconv.Quote(arg)
	}
	if l.stdin != "" {
		shown = append(shown, "<<< "+strconv.Quote(l.stdin))
	}
	return shown
}

func (l *focusGuardLookup) diff() string {
	render := func(answer focusGuardAnswer) string {
		text := focusGuardMaskTelemetry(answer.stdout)
		if l.json {
			if values, err := focusGuardDecodeJSON(answer.stdout); err == nil {
				indented, _ := json.MarshalIndent(values, "", "  ")
				text = string(indented) + "\n"
			}
		}
		if answer.err != "" {
			text += "error: " + answer.err + "\n"
		}
		return text
	}
	return focusGuardUnifiedDiff("full load (ENTIRE_GRAPH_SEARCH_STORE=0)", "sidecar (ENTIRE_GRAPH_SEARCH_STORE=1)",
		render(l.full), render(l.sidecar))
}

// focusGuardDecodeJSON decodes every document in the stream (def prints one
// per name) and drops the telemetry keys.
func focusGuardDecodeJSON(stream string) ([]any, error) {
	decoder := json.NewDecoder(strings.NewReader(stream))
	var values []any
	for {
		var value any
		if err := decoder.Decode(&value); errors.Is(err, io.EOF) {
			return values, nil
		} else if err != nil {
			return nil, err
		}
		values = append(values, focusGuardDropTelemetry(value))
	}
}

func focusGuardDropTelemetry(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if strings.HasSuffix(key, "_latency_ms") || strings.HasPrefix(key, "index_cache_") {
				delete(typed, key)
				continue
			}
			typed[key] = focusGuardDropTelemetry(child)
		}
	case []any:
		for index, child := range typed {
			typed[index] = focusGuardDropTelemetry(child)
		}
	}
	return value
}

var (
	focusGuardRelationHeader = regexp.MustCompile(`(?m)^Index: cache-(\w+) \(\d+ms\) \| Query: \d+ms \| Total: \d+ms`)
	focusGuardCompactLatency = regexp.MustCompile(`(?m)^I:(\w+)/\d+ Q:\d+ T:\d+$`)
)

func focusGuardMaskTelemetry(text string) string {
	text = focusGuardRelationHeader.ReplaceAllString(text, "Index: cache-$1 (<n>ms) | Query: <n>ms | Total: <n>ms")
	return focusGuardCompactLatency.ReplaceAllString(text, "I:$1/<n> Q:<n> T:<n>")
}

// focusGuardUnifiedDiff is a line diff with three lines of context, enough to
// show which record one side is missing.
func focusGuardUnifiedDiff(fromName, toName, from, to string) string {
	a, b := strings.SplitAfter(from, "\n"), strings.SplitAfter(to, "\n")
	// lcs[i][j] is the common-subsequence length of a[i:] and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type line struct {
		op   byte
		text string
	}
	var lines []line
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			lines = append(lines, line{' ', a[i]})
			i, j = i+1, j+1
		case i < len(a) && (j == len(b) || lcs[i+1][j] >= lcs[i][j+1]):
			lines = append(lines, line{'-', a[i]})
			i++
		default:
			lines = append(lines, line{'+', b[j]})
			j++
		}
	}
	const context = 3
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", fromName, toName)
	printed := -1
	for index, current := range lines {
		near := false
		for k := max(0, index-context); k <= min(len(lines)-1, index+context); k++ {
			if lines[k].op != ' ' {
				near = true
				break
			}
		}
		if !near {
			continue
		}
		if index != printed+1 {
			out.WriteString("@@\n")
		}
		printed = index
		out.WriteByte(current.op)
		out.WriteString(strings.TrimSuffix(current.text, "\n"))
		out.WriteByte('\n')
	}
	return out.String()
}

// focusGuardEach runs work(0..count-1) on every core and returns the first
// error. It does not touch testing.T, so the work may run on any goroutine.
func focusGuardEach(count int, work func(int) error) error {
	next := make(chan int)
	errs := make(chan error, count)
	var wait sync.WaitGroup
	for range min(count, runtime.GOMAXPROCS(0)) {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range next {
				if err := work(index); err != nil {
					errs <- err
				}
			}
		}()
	}
	for index := range count {
		next <- index
	}
	close(next)
	wait.Wait()
	close(errs)
	return <-errs
}

func focusGuardGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return nil
}

func focusGuardCopyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, content, 0o644)
	})
}
