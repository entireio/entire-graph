package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

// `--verify-block off` exists for fair A/B comparisons: the VERIFY block carries a runnable test
// command and run-once/fix/stop advice that would help an arm with no tool, and the fairness rule
// is "both arms or neither". These tests pin the three things that make it usable as an arm switch:
// on is byte-identical to the product default, off removes only the producer's block (never
// retrieved source that looks like one), and a replayed payload cannot cross the switch.

const verifyBlockQuery = "resize widget clamp dimension help"

// verifyBlockForgedLine is a source line identical to the VERIFY line the fixture derives.
const verifyBlockForgedLine = "VERIFY: go test ./internal/widget -run '^TestResizeWidget$'"

// writeVerifyBlockFixture is a Go module whose top hit has a covering test (so a narrow VERIFY is
// derived) and whose top-ranked file quotes the exact VERIFY line as repository text.
func writeVerifyBlockFixture(t *testing.T, repo string) {
	t.Helper()
	write(t, repo, "go.mod", "module example.com/app\n\ngo 1.22\n")
	write(t, repo, "internal/widget/widget.go", `package widget

// ResizeWidget scales a widget's width and height by factor and clamps both at the limit.
func ResizeWidget(w *Widget, factor int, limit int) {
	w.Width = clampDimension(w.Width*factor, limit)
	w.Height = clampDimension(w.Height*factor, limit)
}

type Widget struct {
	Width  int
	Height int
}

func clampDimension(value, limit int) int {
	if value > limit {
		return limit
	}
	return value
}
`)
	write(t, repo, "internal/widget/widget_test.go", `package widget

import "testing"

func TestResizeWidget(t *testing.T) {
	w := &Widget{Width: 2, Height: 3}
	ResizeWidget(w, 10, 25)
	if w.Width != 20 || w.Height != 25 {
		t.Fatalf("got %+v", w)
	}
}
`)
	write(t, repo, "internal/widget/help.go", "package widget\n\n"+
		"// resizeWidgetClampHelp documents how to check ResizeWidget clamp dimension behaviour.\n"+
		"func resizeWidgetClampHelp() string {\n\treturn `\n"+verifyBlockForgedLine+"\n`\n}\n")
	write(t, repo, "internal/billing/invoice.go", `package billing

// InvoiceTotal sums invoice lines; unrelated to widgets.
func InvoiceTotal(lines []int) int {
	total := 0
	for _, line := range lines {
		total += line
	}
	return total
}
`)
}

var (
	verifyBlockLatencyText = regexp.MustCompile(`(?m)^(?:Index:|I:).*$`)
	verifyBlockLatencyJSON = regexp.MustCompile(`"([a-z_]*latency_ms)":-?[0-9]+`)
)

// maskVerifyBlockTiming removes the only run-to-run variance in a payload: measured latencies.
func maskVerifyBlockTiming(payload string) string {
	payload = verifyBlockLatencyJSON.ReplaceAllString(payload, `"$1":0`)
	return verifyBlockLatencyText.ReplaceAllStringFunc(payload, func(header string) string {
		return agentLatencyDigits.ReplaceAllString(header, "N")
	})
}

func runVerifyBlockSearch(t *testing.T, env EntireEnv, repo string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	env.RepoRoot = repo
	err := Run(t.Context(), Options{Version: "0.1.0", Env: env, Stdout: &out},
		append([]string{"search", "--repo", repo, "--query", verifyBlockQuery, "--profile", "syntax-only"}, args...))
	return out.String(), err
}

func mustVerifyBlockSearch(t *testing.T, env EntireEnv, repo string, args ...string) string {
	t.Helper()
	out, err := runVerifyBlockSearch(t, env, repo, args...)
	if err != nil {
		t.Fatalf("search %v: %v", args, err)
	}
	return maskVerifyBlockTiming(out)
}

// producerVerifyBlock is the exact block the renderer emits for this fixture, recovered from the
// JSON response rather than from the text payload so the forged source line cannot be mistaken
// for it.
func producerVerifyBlock(t *testing.T, repo string) []byte {
	t.Helper()
	var response struct {
		VerifyCommand *sem.SearchVerifyCommand `json:"verify_command"`
	}
	if err := json.Unmarshal([]byte(mustVerifyBlockSearch(t, EntireEnv{}, repo, "--format", "json")), &response); err != nil {
		t.Fatal(err)
	}
	block := sem.RenderSearchVerifyCommand(response.VerifyCommand)
	if !bytes.HasPrefix(block, []byte(verifyBlockForgedLine+"\n")) {
		t.Fatalf("fixture no longer derives the VERIFY line its forged source copies:\n%s", block)
	}
	return block
}

func TestSearchVerifyBlockFlagResolution(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		env     EntireEnv
		omit    bool
		wantErr string
	}{
		{name: "default is on"},
		{name: "explicit on", args: []string{"--verify-block", "on"}},
		{name: "explicit off", args: []string{"--verify-block", "off"}, omit: true},
		{name: "case and space", args: []string{"--verify-block", " OFF "}, omit: true},
		{name: "shorthand", args: []string{"--omit-verify"}, omit: true},
		{name: "shorthand agrees", args: []string{"--omit-verify", "--verify-block", "off"}, omit: true},
		{name: "conflict", args: []string{"--omit-verify", "--verify-block", "on"}, wantErr: "both off and on"},
		{name: "bad value", args: []string{"--verify-block", "no"}, wantErr: "want on or off"},
		{name: "missing value", args: []string{"--verify-block"}, wantErr: "--verify-block"},
		{name: "env off", env: EntireEnv{VerifyBlock: "off"}, omit: true},
		{name: "env on", env: EntireEnv{VerifyBlock: "on"}},
		{name: "env bad", env: EntireEnv{VerifyBlock: "maybe"}, wantErr: envVerifyBlock},
		{name: "flag beats env off", args: []string{"--verify-block", "on"}, env: EntireEnv{VerifyBlock: "off"}},
		{name: "flag beats env on", args: []string{"--omit-verify"}, env: EntireEnv{VerifyBlock: "on"}, omit: true},
		{name: "flag beats bad env", args: []string{"--verify-block", "on"}, env: EntireEnv{VerifyBlock: "maybe"}},
		{name: "off refuses prefix", args: []string{"--omit-verify", "--verify-prefix", "X"}, wantErr: "--verify-prefix decorates"},
		{name: "off refuses status", args: []string{"--omit-verify", "--verify-prefix-status", "X"}, wantErr: "--verify-prefix-status decorates"},
		{name: "off refuses explain", args: []string{"--omit-verify", "--verify-explain", "X"}, wantErr: "--verify-explain decorates"},
		{name: "env off refuses explain", args: []string{"--verify-explain", "X"}, env: EntireEnv{VerifyBlock: "off"}, wantErr: envVerifyBlock},
		{name: "on keeps decorators", args: []string{"--verify-block", "on", "--verify-explain", "X"}},
		{name: "off refuses presearch", args: []string{"--omit-verify"}, env: EntireEnv{PresearchPath: "/x"}, wantErr: envPresearch},
		{name: "on allows presearch", env: EntireEnv{PresearchPath: "/x"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			flags, _, err := parseSearchFlags(append([]string{"--query", "q"}, test.args...))
			if err == nil {
				err = resolveSearchVerifyBlock(&flags, test.env)
			}
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if flags.OmitVerify != test.omit {
				t.Fatalf("OmitVerify = %t, want %t", flags.OmitVerify, test.omit)
			}
		})
	}
}

var verifyBlockFormats = []string{"text", "agent", "json", "ndjson"}

// verifyBlockBudgets spans the no-cap case, caps where the agent fitter must drop VERIFY or rows,
// and the default. "" means the flag is not passed.
var verifyBlockBudgets = []string{"", "0", "120", "300", "450", "600", "700", "800", "1000", "1400", "2048", "4096"}

func verifyBlockBudgetArgs(format, budget string) []string {
	args := []string{"--format", format}
	if budget != "" {
		args = append(args, "--max-context-bytes", budget)
	}
	return args
}

// ON IS THE PRODUCT. With the switch unset, set to on by flag, or set to on by environment, every
// format at every budget renders the same bytes. (Byte identity against the pre-flag commit is a
// binary-level receipt: the same fixture and sweep run through both builds.)
func TestSearchVerifyBlockDefaultIsExplicitOn(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeVerifyBlockFixture(t, repo)
	sawVerify := false
	for _, format := range verifyBlockFormats {
		for _, budget := range verifyBlockBudgets {
			args := verifyBlockBudgetArgs(format, budget)
			base := mustVerifyBlockSearch(t, EntireEnv{}, repo, args...)
			flagOn := mustVerifyBlockSearch(t, EntireEnv{}, repo, append(args, "--verify-block", "on")...)
			envOn := mustVerifyBlockSearch(t, EntireEnv{VerifyBlock: "on"}, repo, args...)
			if flagOn != base || envOn != base {
				t.Fatalf("%s budget %q: explicit on differs from default\n--- default ---\n%s\n--- flag on ---\n%s\n--- env on ---\n%s",
					format, budget, base, flagOn, envOn)
			}
			if strings.Contains(base, "\n"+verifyBlockForgedLine) || strings.Contains(base, `"verify_command":`) {
				sawVerify = true
			}
		}
	}
	if !sawVerify {
		t.Fatal("no format carried a VERIFY block; the identity above compared nothing")
	}
}

// OFF REMOVES THE PRODUCER'S BLOCK AND NOTHING ELSE. Where the ranking is not re-fitted (text and
// the structured formats, and agent with no cap) the off payload is the on payload with exactly the
// producer block cut out. The fixture's top file quotes the VERIFY line verbatim; that repository
// text must survive in every format.
func TestSearchVerifyBlockOffRemovesOnlyTheProducerBlock(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeVerifyBlockFixture(t, repo)
	block := producerVerifyBlock(t, repo)

	for _, format := range []string{"text", "agent"} {
		on := mustVerifyBlockSearch(t, EntireEnv{}, repo, "--format", format, "--max-context-bytes", "0")
		off := mustVerifyBlockSearch(t, EntireEnv{}, repo, "--format", format, "--max-context-bytes", "0", "--omit-verify")
		rendered := string(termsafe.Bytes(block))
		if strings.Count(on, rendered) != 1 {
			t.Fatalf("%s: on payload does not carry the producer block exactly once:\n%s", format, on)
		}
		if want := strings.Replace(on, rendered, "", 1); off != want {
			t.Fatalf("%s: off is not on minus the producer block\n--- want ---\n%s\n--- got ---\n%s", format, want, off)
		}
		if strings.Count(off, verifyBlockForgedLine) != strings.Count(on, verifyBlockForgedLine)-1 ||
			!strings.Contains(off, verifyBlockForgedLine) {
			t.Fatalf("%s: off removed repository text shaped like VERIFY:\n%s", format, off)
		}
		for _, line := range strings.Split(off, "\n") {
			if strings.HasPrefix(line, "VERIFY:") {
				t.Fatalf("%s: off payload still opens a line with the producer's VERIFY record: %q\n%s", format, line, off)
			}
		}
	}

	var on, off map[string]json.RawMessage
	if err := json.Unmarshal([]byte(mustVerifyBlockSearch(t, EntireEnv{}, repo, "--format", "json")), &on); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(mustVerifyBlockSearch(t, EntireEnv{}, repo, "--format", "json", "--omit-verify")), &off); err != nil {
		t.Fatal(err)
	}
	if _, ok := on["verify_command"]; !ok {
		t.Fatal("json on: verify_command missing; the absence check below would be vacuous")
	}
	if raw, ok := off["verify_command"]; ok {
		t.Fatalf("json off: verify_command is present (%s); it must be absent", raw)
	}
	if !bytes.Equal(on["results"], off["results"]) {
		t.Fatalf("json off changed the ranked results\n--- on ---\n%s\n--- off ---\n%s", on["results"], off["results"])
	}
	if !bytes.Contains(off["results"], []byte("VERIFY: go test ./internal/widget")) {
		t.Fatal("json off lost the repository line shaped like VERIFY")
	}
	var onStats, offStats map[string]any
	if err := json.Unmarshal(on["stats"], &onStats); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(off["stats"], &offStats); err != nil {
		t.Fatal(err)
	}
	if offStats["verify_omitted"] != true || offStats["verify_command_bytes"] != nil || offStats["verify_tier"] != nil {
		t.Fatalf("json off stats do not report the suppression: %v", offStats)
	}
	if _, ok := onStats["verify_omitted"]; ok {
		t.Fatalf("json on reports verify_omitted: %v", onStats)
	}

	// ndjson: the summary record must not carry a verify command either.
	ndOff := mustVerifyBlockSearch(t, EntireEnv{}, repo, "--format", "ndjson", "--omit-verify")
	ndOn := mustVerifyBlockSearch(t, EntireEnv{}, repo, "--format", "ndjson")
	if !strings.Contains(ndOn, `"verify_command"`) || strings.Contains(ndOff, `"verify_command"`) {
		t.Fatalf("ndjson: verify_command on=%t off=%t", strings.Contains(ndOn, `"verify_command"`), strings.Contains(ndOff, `"verify_command"`))
	}
	if !strings.Contains(ndOff, "VERIFY: go test ./internal/widget") {
		t.Fatal("ndjson off lost the repository line shaped like VERIFY")
	}
}

// Off honours the cap in every format at every budget, and never carries the producer block.
func TestSearchVerifyBlockOffRespectsCapEndToEnd(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeVerifyBlockFixture(t, repo)
	block := string(termsafe.Bytes(producerVerifyBlock(t, repo)))
	tail := block[len(verifyBlockForgedLine)+1:]
	for _, format := range verifyBlockFormats {
		for _, budget := range verifyBlockBudgets {
			off := mustVerifyBlockSearch(t, EntireEnv{}, repo, append(verifyBlockBudgetArgs(format, budget), "--omit-verify")...)
			if strings.Contains(off, tail) || strings.Contains(off, `"verify_command"`) {
				t.Fatalf("%s budget %q: off payload carries the producer block:\n%s", format, budget, off)
			}
			if format == "agent" && budget != "" && budget != "0" {
				var cap int
				fmt.Sscan(budget, &cap)
				if len(off) > cap {
					t.Fatalf("agent budget %d: off payload is %d bytes:\n%s", cap, len(off), off)
				}
			}
		}
	}
}

// verifyBlockResponse is the timing fixture with a VERIFY block: the renderer input `on` produces.
// `off` is the same response with VerifyCommand nil, which is exactly what the producer returns
// under OmitVerifyCommand.
func verifyBlockResponse() sem.SearchResponse {
	response := timingDeterminismResponse()
	response.VerifyCommand = &sem.SearchVerifyCommand{
		Command: "go test ./internal/pkg1 -run '^TestHandler1$'", Targets: "internal/pkg1/file1_test.go",
		DerivedFrom: "go.mod module root", Tier: "narrow",
	}
	return response
}

// agentRankedBytes is what a payload spent on everything but the producer block.
func agentRankedBytes(payload string, block []byte) int {
	if i := strings.Index(payload, string(block)); i >= 0 {
		return len(payload) - len(block)
	}
	return len(payload)
}

// Under a cap the bytes the block would have cost are handed to the same fitter, so off is a
// different selection, not the on payload with lines deleted. Dense sweep: the cap holds, the body
// is independent of latency (the #301 invariant) in both modes, off never spends fewer non-VERIFY
// bytes than on, and somewhere it spends strictly more.
func TestAgentSearchVerifyBlockOffRefitsUnderTheSameCap(t *testing.T) {
	t.Parallel()
	onResponse := verifyBlockResponse()
	block := termsafe.Bytes(sem.RenderSearchVerifyCommand(onResponse.VerifyCommand))
	var roomy bytes.Buffer
	if err := writeAgentSearch(&roomy, onResponse, 1<<20); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(roomy.Bytes(), block) {
		t.Fatalf("fixture on payload lacks its VERIFY block:\n%s", roomy.String())
	}
	timings := []sem.SearchStats{
		{IndexCacheHit: true, IndexLatencyMS: 1, QueryLatencyMS: 1, PreselectLatencyMS: 1, TotalLatencyMS: 1},
		{IndexCacheHit: true, IndexLatencyMS: 99999999, QueryLatencyMS: 99999999, PreselectLatencyMS: 99999999, TotalLatencyMS: 99999999},
	}
	gained := 0
	for budget := 1; budget <= roomy.Len()+8; budget++ {
		var onPayload, offPayload string
		for i, stats := range timings {
			on, off := verifyBlockResponse(), verifyBlockResponse()
			off.VerifyCommand = nil
			on.Stats, off.Stats = stats, stats
			var onOut, offOut bytes.Buffer
			if err := writeAgentSearch(&onOut, on, budget); err != nil {
				t.Fatal(err)
			}
			if err := writeAgentSearch(&offOut, off, budget); err != nil {
				t.Fatal(err)
			}
			if offOut.Len() > budget || onOut.Len() > budget {
				t.Fatalf("budget %d: on %d B, off %d B", budget, onOut.Len(), offOut.Len())
			}
			if bytes.Contains(offOut.Bytes(), []byte("VERIFY:")) {
				t.Fatalf("budget %d: off payload carries VERIFY:\n%s", budget, offOut.String())
			}
			_, onBody := splitAgentHeader(onOut.String())
			_, offBody := splitAgentHeader(offOut.String())
			if i == 0 {
				onPayload, offPayload = onBody, offBody
				continue
			}
			if onBody != onPayload || offBody != offPayload {
				t.Fatalf("budget %d: rendered body depends on latency (on changed %t, off changed %t)",
					budget, onBody != onPayload, offBody != offPayload)
			}
		}
		onRanked, offRanked := agentRankedBytes(onPayload, block), agentRankedBytes(offPayload, block)
		if offRanked < onRanked {
			t.Fatalf("budget %d: off spent %d non-VERIFY bytes, on spent %d\n--- on ---\n%s\n--- off ---\n%s",
				budget, offRanked, onRanked, onPayload, offPayload)
		}
		if offRanked > onRanked {
			gained++
		}
	}
	if gained == 0 {
		t.Fatal("off never spent the freed bytes on the ranking at any budget")
	}
}

// #302 exact-name answers and #303 declaration blocks are unaffected apart from the missing block:
// with room for everything, off is on minus the producer block; under a cap, every declaration on
// shows, off shows too.
func TestAgentSearchVerifyBlockOffKeepsExactNameAndDeclarations(t *testing.T) {
	t.Parallel()
	t.Run("exact-name", func(t *testing.T) {
		t.Parallel()
		on := exactNameFixture("Resolve", 1, 3)
		if on.VerifyCommand == nil {
			t.Fatal("exact-name fixture lost its VERIFY block")
		}
		off := exactNameFixture("Resolve", 1, 3)
		off.VerifyCommand = nil
		block := string(termsafe.Bytes(sem.RenderSearchVerifyCommand(on.VerifyCommand)))
		roomyOn := renderAgentSearchForTest(t, on, 1<<20, true)
		roomyOff := renderAgentSearchForTest(t, off, 1<<20, true)
		if !strings.Contains(roomyOn, block) || roomyOff != strings.Replace(roomyOn, block, "", 1) {
			t.Fatalf("roomy exact-name off is not on minus the block\n--- on ---\n%s\n--- off ---\n%s", roomyOn, roomyOff)
		}
		for budget := 1; budget <= len(roomyOn)+8; budget += exactNameSweepScale() {
			onPayload := renderAgentSearchForTest(t, on, budget, true)
			offPayload := renderAgentSearchForTest(t, off, budget, true)
			if len(offPayload) > budget {
				t.Fatalf("budget %d: off exact-name payload is %d bytes", budget, len(offPayload))
			}
			for _, rank := range []int{1, 3} {
				if exactNameDeclShown(onPayload, on.Results[rank-1]) && !exactNameDeclShown(offPayload, off.Results[rank-1]) {
					t.Fatalf("budget %d: on shows rank %d's declaration and off does not\n--- on ---\n%s\n--- off ---\n%s",
						budget, rank, onPayload, offPayload)
				}
			}
		}
	})
	for _, fixture := range declFixtures {
		fixture := fixture
		t.Run("decl-"+fixture.lang, func(t *testing.T) {
			t.Parallel()
			result, decl := fixture.build(120, 45)
			on := sem.SearchResponse{
				Query: "flush pending writes", Profile: "full", Results: []sem.SearchResult{result},
				VerifyCommand: &sem.SearchVerifyCommand{Command: "make test", Targets: fixture.path, DerivedFrom: "Makefile", Tier: "suite"},
				Warnings:      []sem.ProviderWarning{}, PartialFailures: []sem.PartialFailure{},
			}
			off := on
			off.VerifyCommand = nil
			block := string(termsafe.Bytes(sem.RenderSearchVerifyCommand(on.VerifyCommand)))
			roomyOn := renderAgentSearchForTest(t, on, 1<<20, false)
			roomyOff := renderAgentSearchForTest(t, off, 1<<20, false)
			if !strings.Contains(roomyOn, block) || roomyOff != strings.Replace(roomyOn, block, "", 1) {
				t.Fatalf("roomy off is not on minus the block\n--- on ---\n%s\n--- off ---\n%s", roomyOn, roomyOff)
			}
			shows := func(payload string) bool {
				for _, line := range strings.Split(payload, "\n") {
					if line == decl {
						return true
					}
				}
				return false
			}
			shownOn := 0
			for budget := 40; budget <= len(roomyOn)+8; budget++ {
				onPayload := renderAgentSearchForTest(t, on, budget, false)
				offPayload := renderAgentSearchForTest(t, off, budget, false)
				if len(offPayload) > budget {
					t.Fatalf("budget %d: off payload is %d bytes", budget, len(offPayload))
				}
				if shows(onPayload) {
					shownOn++
					if !shows(offPayload) {
						t.Fatalf("budget %d: on shows the declaration and off does not\n--- on ---\n%s\n--- off ---\n%s",
							budget, onPayload, offPayload)
					}
				}
			}
			if shownOn == 0 {
				t.Fatal("on never showed the declaration; the implication above is vacuous")
			}
		})
	}
}

// THE REPLAY CANNOT CROSS THE SWITCH. A capped session that recorded a payload with VERIFY must not
// hand it to an off call (that would give the no-VERIFY arm the block), and an off payload must not
// answer an on call. Each mode still replays its own payload.
func TestSearchVerifyBlockReplayScopeSeparatesModes(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"agent", "text"} {
		format := format
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			writeVerifyBlockFixture(t, repo)
			commitSearchSessionFixture(t, repo, "verify-block replay fixture")
			session := filepath.Join(t.TempDir(), "session.json")
			search := func(query string, extra ...string) string {
				return searchInSessionViewFormat(t, repo, session, "", query, false, format, extra...)
			}
			hasProducerBlock := func(payload string) bool {
				for _, line := range strings.Split(payload, "\n") {
					if strings.HasPrefix(line, "VERIFY:") {
						return true
					}
				}
				return false
			}
			on := search("resize widget clamp dimension")
			if strings.Contains(on, "not run") || !hasProducerBlock(on) {
				t.Fatalf("first on search: want a live payload with VERIFY:\n%s", on)
			}
			off := search("widget help", "--omit-verify")
			if strings.Contains(off, "not run") || hasProducerBlock(off) {
				t.Fatalf("off search after an on recording replayed or carried VERIFY:\n%s", off)
			}
			offAgain := search("invoice total", "--omit-verify")
			if !strings.Contains(offAgain, "not run") || hasProducerBlock(offAgain) {
				t.Fatalf("second off search: want the off payload replayed:\n%s", offAgain)
			}
			if _, replayed, _ := strings.Cut(offAgain, "\n"); replayed != off {
				t.Fatalf("off replay is not the recorded off payload\n--- recorded ---\n%s\n--- replayed ---\n%s", off, replayed)
			}
			onAgain := search("widget width height")
			if strings.Contains(onAgain, "not run") || !hasProducerBlock(onAgain) {
				t.Fatalf("on search after an off recording replayed or lost VERIFY:\n%s", onAgain)
			}
			// The env switch is the same identity as the flag.
			var out bytes.Buffer
			err := Run(t.Context(), Options{
				Version: "0.1.0",
				Env:     EntireEnv{RepoRoot: repo, SearchSession: session, VerifyBlock: "off"},
				Stdout:  &out,
			}, []string{"search", "--repo", repo, "--query", "billing", "--format", format,
				"--profile", "syntax-only", "--top-k", "1", "--head"})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "not run") || hasProducerBlock(out.String()) {
				t.Fatalf("env off search after an on recording replayed or carried VERIFY:\n%s", out.String())
			}
		})
	}
}

func TestSearchSessionScopeMatchesVerifyMode(t *testing.T) {
	t.Parallel()
	live := searchSessionScope{Repo: "/r", Tree: "t", PolicyFingerprint: "p", Format: "agent"}
	recorded := searchSessionState{ReplaySchema: searchSessionReplaySchema, Repo: "/r", Tree: "t", PolicyFingerprint: "p", Format: "agent"}
	if !recorded.matches(live) {
		t.Fatal("on state does not match an on scope")
	}
	offLive := live
	offLive.OmitVerify = true
	if recorded.matches(offLive) {
		t.Fatal("a payload recorded with VERIFY matches an omit-verify scope")
	}
	offRecorded := recorded
	offRecorded.VerifyOmitted = true
	if offRecorded.matches(offLive) {
		t.Fatal("an off state under the on schema matches; a pre-switch build would replay it to an on call")
	}
	offRecorded.ReplaySchema = searchSessionReplaySchemaVerifyOmitted
	if !offRecorded.matches(offLive) {
		t.Fatal("off state does not match an off scope")
	}
	if offRecorded.matches(live) {
		t.Fatal("a payload recorded without VERIFY matches an on scope")
	}
	// A state file written before the switch existed has no verify_omitted member, and every such
	// payload was rendered with VERIFY on.
	var legacy searchSessionState
	if err := json.Unmarshal([]byte(`{"searches":1,"replay_schema":3,"policy_fingerprint":"p","format":"agent","repo":"/r","tree":"t","payload":"x","payload_paths":[]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if !legacy.matches(live) || legacy.matches(offLive) {
		t.Fatalf("legacy state: matches on=%t off=%t, want true false", legacy.matches(live), legacy.matches(offLive))
	}
	// Record persists the mode.
	session := &searchSession{path: filepath.Join(t.TempDir(), "s.json"), limit: 1}
	session.record("q", []byte("payload"), []string{}, offLive, false, true)
	state, err := session.load()
	if err != nil {
		t.Fatal(err)
	}
	if !state.VerifyOmitted || state.ReplaySchema != searchSessionReplaySchemaVerifyOmitted ||
		!state.matches(offLive) || state.matches(live) {
		t.Fatalf("recorded off state = %#v", state)
	}
}

// The committed-tree snapshot cache holds the parsed graph, not the rendered answer, and VERIFY is
// derived after it per query. So a cache warmed by an off search must serve an on search exactly
// what a cache warmed by an on search serves it, and vice versa: the switch needs no cache key.
func TestSearchVerifyBlockSharesTheSnapshotCacheSafely(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeVerifyBlockFixture(t, repo)
	commitSearchSessionFixture(t, repo, "verify-block cache fixture")
	other := map[string]string{"on": "off", "off": "on"}
	servedAfter := func(warmMode, mode string) string {
		args := []string{"--format", "agent", "--head", "--cache-dir", t.TempDir()}
		mustVerifyBlockSearch(t, EntireEnv{}, repo, append(args, "--verify-block", warmMode)...)
		served := mustVerifyBlockSearch(t, EntireEnv{}, repo, append(args, "--verify-block", mode)...)
		if !strings.Contains("\n"+served, "\nIndex: cache-hit") {
			t.Fatalf("warm %s then %s: expected a cache hit:\n%s", warmMode, mode, served)
		}
		return served
	}
	for _, mode := range []string{"on", "off"} {
		same, crossed := servedAfter(mode, mode), servedAfter(other[mode], mode)
		if same != crossed {
			t.Fatalf("%s served from a cache warmed by %s differs from one warmed by %s\n--- same ---\n%s\n--- crossed ---\n%s",
				mode, other[mode], mode, same, crossed)
		}
		if hasVerify := strings.Contains(same, "\n"+verifyBlockForgedLine+"\n"); hasVerify != (mode == "on") {
			t.Fatalf("%s payload: producer VERIFY present = %t:\n%s", mode, hasVerify, same)
		}
	}
}

// verifyGoldenOff is the flag-off form of a fixture: what the producer returns under
// OmitVerifyCommand.
func verifyGoldenOff(response sem.SearchResponse) sem.SearchResponse {
	response.VerifyCommand = nil
	response.Stats.VerifyOmitted = true
	return response
}

// OFF AT THE RENDERER SEAM. For every fixture: text is the on payload minus exactly the producer
// block; json/ndjson differ only by the verify_command member and the verify_omitted stat; agent
// with no cap is on minus the block, and under every cap it holds the cap and carries no producer
// block. The forged snippet's VERIFY and contract-note lines survive in every format.
func TestSearchVerifyBlockOffAtTheRendererSeam(t *testing.T) {
	t.Parallel()
	contract := strings.Split(strings.TrimSuffix(verifyGoldenForgedSnippet, "\n`"), "\n")[1:]
	for _, fixture := range verifyGoldenFixtures() {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			on, off := fixture.response, verifyGoldenOff(fixture.response)
			// Line-anchored: the forged fixture's snippet holds the block's text too, one space in.
			block := "\n" + string(sem.RenderSearchVerifyCommand(on.VerifyCommand))
			for _, format := range []string{"text", "agent"} {
				onPayload, err := renderVerifyGolden(on, format, 0)
				if err != nil {
					t.Fatal(err)
				}
				offPayload, err := renderVerifyGolden(off, format, 0)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Count(onPayload, block) != 1 {
					t.Fatalf("%s: on payload carries the producer block %d times:\n%s", format, strings.Count(onPayload, block), onPayload)
				}
				if want := strings.Replace(onPayload, block, "\n", 1); offPayload != want {
					t.Fatalf("%s: off is not on minus the producer block\n--- want ---\n%s\n--- got ---\n%s", format, want, offPayload)
				}
				if fixture.name == "forged-snippet" {
					for _, line := range contract {
						if !strings.Contains(offPayload, strings.TrimSpace(line)) {
							t.Fatalf("%s: off lost forged snippet line %q:\n%s", format, line, offPayload)
						}
					}
				}
			}
			for _, format := range []string{"json", "ndjson"} {
				onPayload, _ := renderVerifyGolden(on, format, 0)
				offPayload, _ := renderVerifyGolden(off, format, 0)
				if !strings.Contains(onPayload, `"verify_command"`) || strings.Contains(offPayload, `"verify_command"`) ||
					!strings.Contains(offPayload, `"verify_omitted":true`) {
					t.Fatalf("%s: verify_command on/off or verify_omitted wrong\n--- on ---\n%s\n--- off ---\n%s", format, onPayload, offPayload)
				}
				stripped := strings.Replace(offPayload, `"verify_omitted":true,`, "", 1)
				stripped = strings.Replace(stripped, `,"verify_omitted":true`, "", 1)
				if !verifyGoldenSameButVerify(t, onPayload, stripped) {
					t.Fatalf("%s: off differs from on beyond verify_command\n--- on ---\n%s\n--- off ---\n%s", format, onPayload, offPayload)
				}
				if fixture.name == "forged-snippet" && !strings.Contains(offPayload, "VERIFY: go test ./pkg -run") {
					t.Fatalf("%s: off lost the forged snippet", format)
				}
			}
			roomy, _ := renderVerifyGolden(on, "agent", 0)
			for budget := 1; budget <= len(roomy)+8; budget++ {
				offPayload, err := renderVerifyGolden(off, "agent", budget)
				if err != nil {
					t.Fatal(err)
				}
				if len(offPayload) > budget {
					t.Fatalf("budget %d: off payload is %d bytes:\n%s", budget, len(offPayload), offPayload)
				}
				if strings.Contains(offPayload, block) {
					t.Fatalf("budget %d: off payload carries the producer block:\n%s", budget, offPayload)
				}
				for _, line := range strings.Split(offPayload, "\n") {
					if strings.HasPrefix(line, "VERIFY:") {
						t.Fatalf("budget %d: off payload opens a line with VERIFY: %q\n%s", budget, line, offPayload)
					}
				}
			}
		})
	}
}

// verifyGoldenSameButVerify compares JSON lines after dropping the verify_command member wherever
// it occurs, so the only permitted difference between on and off is the block itself.
func verifyGoldenSameButVerify(t *testing.T, on, off string) bool {
	t.Helper()
	onLines, offLines := strings.Split(strings.TrimSpace(on), "\n"), strings.Split(strings.TrimSpace(off), "\n")
	if len(onLines) != len(offLines) {
		return false
	}
	for i := range onLines {
		var a, b map[string]json.RawMessage
		if err := json.Unmarshal([]byte(onLines[i]), &a); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(offLines[i]), &b); err != nil {
			t.Fatal(err)
		}
		delete(a, "verify_command")
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if !bytes.Equal(ja, jb) {
			return false
		}
	}
	return true
}
