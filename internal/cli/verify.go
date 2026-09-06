package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/entireio/entire-graph/internal/gitutil"
	"github.com/entireio/entire-graph/internal/intent"
)

// `entire graph verify` — an ADJUDICATED verdict, not test output
// ==============================================================
//
// MEASURED BASIS. Post-last-edit confirmation is 5.8 messages per session, the single largest asymmetric
// pot at 5.0 messages. Every prior attempt to shrink it FREED budget the agent immediately re-spent on
// more verification: transfer efficiency 0.2-0.35, meaning two thirds of every saving went straight back
// into running tests again. A cheaper way to run tests therefore cannot work. The only thing that closes
// the phase is a verdict with nothing left to re-spend on — which requires three properties that
// ordinary test output does not have:
//
//   - A DELTA, not a state. "3 tests fail" invites a run to find out whether they failed before. The
//     pre-edit baseline turns that into "these 3 failed before your edit too", which is not actionable
//     and is explicitly labelled so.
//   - A VERDICT, not evidence. Output is something to interpret, and interpreting invites re-running.
//     The verb states the conclusion and the conclusion's own completeness.
//   - NO RAW OUTPUT, EVER. Forwarding even an excerpt reopens the loop this verb exists to close, so
//     nothing the runner printed reaches the caller. Ids are forwarded; text is not.
//
// FAIRNESS. This verb is tool CAPABILITY — it runs a command the caller supplies and adjudicates the
// result. It prints no instruction the control arm's harness-side stub cannot also print: the
// "verification is complete" sentence is a statement about the DATA (a zero-regression, ≥1-fix delta is
// by definition complete), not advice about how to behave. Nothing here tells the reader what to do.
const (
	// verifyDefaultMaxBytes caps the whole rendered verdict. It is small on purpose: a verdict that
	// needs scrolling is evidence again.
	verifyDefaultMaxBytes = 2048

	// verifyMaxListedIDs bounds any one id list. Past twenty the list is not actionable and the COUNT is
	// the information, so the remainder is summarised rather than dropped silently.
	verifyMaxListedIDs = 20

	// Timeouts mirror the harness's own ecosystem split: a compiled-language suite pays for a build
	// before it runs a test, an interpreted one does not.
	verifyCompiledTimeout    = 900 * time.Second
	verifyInterpretedTimeout = 300 * time.Second
)

// verifyBaseline is the on-disk pre-edit record. The format is deliberately boring — a status per id
// plus provenance — because its only consumer is the diff below and its only job is to still be
// readable when the tree it describes is gone.
type verifyBaseline struct {
	FormatVersion int           `json:"format_version"`
	RecordedAt    string        `json:"recorded_at"`
	Repo          string        `json:"repo"`
	Commit        string        `json:"commit,omitempty"`
	Tree          string        `json:"tree,omitempty"`
	IntentDigest  string        `json:"intent_digest,omitempty"`
	Scope         string        `json:"scope,omitempty"`
	Platform      string        `json:"platform"`
	TimeoutMillis int64         `json:"timeout_millis"`
	TestCommand   string        `json:"test_command"`
	Parser        string        `json:"parser"`
	ExitCode      int           `json:"exit_code"`
	Results       verifyResults `json:"results"`
}

type verifyFlags struct {
	Repo            string
	Setup           string
	Test            string
	PreEditBaseline string
	RecordBaseline  string
	MaxBytes        int
	Scope           string
}

func parseVerifyFlags(args []string) (verifyFlags, error) {
	flags := verifyFlags{MaxBytes: verifyDefaultMaxBytes}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		value := func() (string, error) {
			index++
			if index >= len(args) {
				return "", fmt.Errorf("%s requires a value", arg)
			}
			return args[index], nil
		}
		var err error
		switch arg {
		case "--repo":
			flags.Repo, err = value()
		case "--setup":
			flags.Setup, err = value()
		case "--test":
			flags.Test, err = value()
		case "--pre-edit-baseline":
			flags.PreEditBaseline, err = value()
		case "--record-baseline":
			flags.RecordBaseline, err = value()
		case "--max-bytes":
			var raw string
			if raw, err = value(); err == nil {
				flags.MaxBytes, err = strconv.Atoi(raw)
				if err != nil || flags.MaxBytes <= 0 {
					return flags, fmt.Errorf("verify --max-bytes requires a positive integer, got %q", raw)
				}
			}
		case "--scope":
			flags.Scope, err = value()
		default:
			return flags, fmt.Errorf("verify received unexpected argument %q", arg)
		}
		if err != nil {
			return flags, err
		}
	}
	if strings.TrimSpace(flags.Test) == "" {
		return flags, fmt.Errorf("verify requires --test <command>")
	}
	if flags.RecordBaseline == "" && flags.PreEditBaseline == "" {
		return flags, fmt.Errorf(
			"verify requires --pre-edit-baseline <path> (or --record-baseline <path> to create one)")
	}
	return flags, nil
}

func runVerify(ctx context.Context, opts Options, args []string) error {
	flags, err := parseVerifyFlags(args)
	if err != nil {
		return err
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	output, exitCode, runErr := runVerifyCommands(ctx, repo, flags)
	if runErr != nil {
		// A command that could not be LAUNCHED is a different failure from a command that ran and
		// reported. Saying which is the difference between "fix your invocation" and "fix your code".
		return fmt.Errorf("verify could not run the test command: %w", runErr)
	}
	results, parser, parsed := parseVerifyOutput(output)

	if flags.RecordBaseline != "" {
		return writeVerifyBaseline(ctx, opts, repo, flags, results, parser, parsed, exitCode)
	}
	baseline, err := readVerifyBaseline(flags.PreEditBaseline)
	if err != nil {
		return err
	}
	if baseline.Repo != repo || baseline.TestCommand != flags.Test || baseline.Scope != flags.Scope {
		return fmt.Errorf("verify baseline is incompatible with the selected repository, command, or scope")
	}
	if parsed && baseline.Parser != "exit-code-only" && baseline.Parser != parser {
		return fmt.Errorf("verify baseline parser %q is incompatible with current parser %q", baseline.Parser, parser)
	}
	_, writeErr := opts.Stdout.Write(renderVerifyVerdict(
		verifyVerdictInput{
			baseline: baseline, current: results, parser: parser, parsed: parsed,
			exitCode: exitCode, maxBytes: flags.MaxBytes,
		}))
	return writeErr
}

// runVerifyCommands runs setup then test, capturing combined output. Setup output is DISCARDED: an
// install log is not a test result, and the parsers must not see it (a dependency named `test_foo` in a
// pip log would otherwise become a test id).
func runVerifyCommands(ctx context.Context, repo string, flags verifyFlags) (string, int, error) {
	timeout := verifyInterpretedTimeout
	if verifyCompiledCommand(flags.Test) {
		timeout = verifyCompiledTimeout
	}
	if flags.Setup != "" {
		setupCtx, cancel := context.WithTimeout(ctx, timeout)
		_, _, err := runVerifyShell(setupCtx, repo, flags.Setup)
		cancel()
		if err != nil {
			return "", 0, fmt.Errorf("setup command failed to launch: %w", err)
		}
	}
	testCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return runVerifyShell(testCtx, repo, flags.Test)
}

// verifyCompiledCommand reports whether a command belongs to an ecosystem that builds before it tests,
// and therefore needs the long timeout. The test is on the RUNNER, because that is the thing that knows.
func verifyCompiledCommand(command string) bool {
	lowered := strings.ToLower(command)
	for _, marker := range []string{
		"cargo", "go test", "mvn", "gradle", "cmake", "ctest", "make", "bazel", "dotnet", "swift ",
	} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// runVerifyShell executes one command in the repository. It returns the exit code separately from the
// error: a test suite exiting non-zero is the normal case and not a failure of this verb.
func runVerifyShell(ctx context.Context, repo, command string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = repo
	// The verb MUST NOT mutate the tree beyond what the command itself does, so nothing is written, no
	// files are staged and no environment is injected beyond the caller's own.
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exit *exec.ExitError
		if errorsAs(err, &exit) {
			return string(out), exit.ExitCode(), nil
		}
		return string(out), 0, err
	}
	return string(out), 0, nil
}

// errorsAs is errors.As without importing the package name into every call site here.
func errorsAs(err error, target **exec.ExitError) bool {
	if exit, ok := err.(*exec.ExitError); ok {
		*target = exit
		return true
	}
	return false
}

func writeVerifyBaseline(
	ctx context.Context, opts Options, repo string, flags verifyFlags,
	results verifyResults, parser string, parsed bool, exitCode int,
) error {
	baseline := verifyBaseline{
		FormatVersion: 1,
		RecordedAt:    time.Now().UTC().Format(time.RFC3339),
		Repo:          repo,
		Scope:         flags.Scope,
		Platform:      runtime.GOOS + "/" + runtime.GOARCH,
		TimeoutMillis: verifyTimeout(flags.Test).Milliseconds(),
		TestCommand:   flags.Test,
		Parser:        parser,
		ExitCode:      exitCode,
		Results:       results,
	}
	if commit, tree, err := gitutil.HeadCommitAndTree(ctx, repo); err == nil {
		baseline.Commit, baseline.Tree = commit, tree
	}
	if set, err := intent.Load(repo); err == nil {
		baseline.IntentDigest = set.Digest
	}
	if !parsed {
		baseline.Parser = "exit-code-only"
		baseline.Results = verifyResults{}
	}
	encoded, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		return err
	}
	// writeOutputFile, not MkdirAll+os.WriteFile: --record-baseline names a path
	// anywhere on the machine (the help advertises /tmp/base.json), but a path
	// inside the scanned repository is repository-controlled and may be a
	// committed symlink. --repo need not be a git repository here at all, which
	// outputpath.go's fallback preserves. See outputpath.go.
	if err := writeOutputFile(ctx, repo, flags.RecordBaseline, append(encoded, '\n'), 0o644, true); err != nil {
		return err
	}
	passed, failed := verifyCountByStatus(baseline.Results)
	if !parsed {
		fmt.Fprintf(opts.Stdout,
			"BASELINE RECORDED: %s (exit %d; output format not recognised, so the baseline is exit-code only)\n",
			flags.RecordBaseline, exitCode)
		return nil
	}
	fmt.Fprintf(opts.Stdout, "BASELINE RECORDED: %s (%s; %d passing, %d failing, exit %d)\n",
		flags.RecordBaseline, baseline.Parser, passed, failed, exitCode)
	return nil
}

func verifyTimeout(command string) time.Duration {
	if verifyCompiledCommand(command) {
		return verifyCompiledTimeout
	}
	return verifyInterpretedTimeout
}

func readVerifyBaseline(path string) (verifyBaseline, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return verifyBaseline{}, fmt.Errorf("verify could not read --pre-edit-baseline %s: %w", path, err)
	}
	var baseline verifyBaseline
	if err := json.Unmarshal(content, &baseline); err != nil {
		return verifyBaseline{}, fmt.Errorf("verify could not parse --pre-edit-baseline %s: %w", path, err)
	}
	if baseline.Results == nil {
		baseline.Results = verifyResults{}
	}
	return baseline, nil
}

func verifyCountByStatus(results verifyResults) (passed, failed int) {
	for _, status := range results {
		if status == verifyStatusPass {
			passed++
			continue
		}
		failed++
	}
	return passed, failed
}

type verifyVerdictInput struct {
	baseline verifyBaseline
	current  verifyResults
	parser   string
	parsed   bool
	exitCode int
	maxBytes int
}

// renderVerifyVerdict is the whole output contract: a delta, a verdict, and nothing else.
//
// The three classes are not symmetric, and that asymmetry is the point:
//
//   - NEWLY PASSING is the fix working. It is what licenses a PASS verdict.
//   - NEWLY FAILING is a regression. Every id is listed (to the cap) because the ids ARE the actionable
//     information — this is the one place the verb can save a caller a search.
//   - STILL FAILING is labelled PRE-EXISTING and explicitly not the caller's problem. Without this class
//     a caller reads a red suite and starts investigating a failure that predates the edit, which is the
//     measured shape of the confirmation pot.
func renderVerifyVerdict(input verifyVerdictInput) []byte {
	var buffer strings.Builder
	if !input.parsed || (input.baseline.Parser == "exit-code-only" && len(input.baseline.Results) == 0) {
		// COARSE MODE, and it says so. Without a parseable format there are no ids, so there is no delta
		// and no honest claim about regressions — only the exit code, which is reported as exactly that.
		if input.exitCode == 0 {
			buffer.WriteString("VERDICT: PASS (exit 0)\n")
		} else {
			fmt.Fprintf(&buffer, "VERDICT: FAIL (exit %d)\n", input.exitCode)
		}
		buffer.WriteString("  the runner's output format was not recognised, so this verdict is " +
			"exit-code only: it reports whether the suite passed, not which tests changed.\n")
		return verifyTruncateOutput(buffer.String(), input.maxBytes)
	}

	var newlyPassing, newlyFailing, stillFailing []string
	for id, status := range input.current {
		before, known := input.baseline.Results[id]
		switch {
		case status == verifyStatusPass && known && before != verifyStatusPass:
			newlyPassing = append(newlyPassing, id)
		case status != verifyStatusPass && (!known || before == verifyStatusPass):
			newlyFailing = append(newlyFailing, id)
		case status != verifyStatusPass:
			stillFailing = append(stillFailing, id)
		}
	}
	sort.Strings(newlyPassing)
	sort.Strings(newlyFailing)
	sort.Strings(stillFailing)

	if len(newlyPassing) > 0 {
		verifyWriteList(&buffer, "NEWLY PASSING", newlyPassing)
	}
	if len(newlyFailing) > 0 {
		verifyWriteList(&buffer, "NEWLY FAILING", newlyFailing)
	}
	if len(stillFailing) > 0 {
		verifyWriteList(&buffer,
			"PRE-EXISTING FAILURES (also failing before your edit; not caused by your change)", stillFailing)
	}

	switch {
	case len(newlyFailing) > 0:
		fmt.Fprintf(&buffer, "VERDICT: REGRESSION in %d test%s: %s\n",
			len(newlyFailing), pluralSuffix(len(newlyFailing)), verifyJoinIDs(newlyFailing))
	case len(newlyPassing) > 0:
		// The second sentence is a statement about the DELTA, not an instruction: a zero-regression,
		// at-least-one-fix delta is by construction a complete verification of the change. See the
		// fairness note at the top of this file.
		buffer.WriteString("VERDICT: PASS — the change fixes the target behavior and introduces no " +
			"regressions. Verification is complete; no further test runs are needed.\n")
	default:
		buffer.WriteString("VERDICT: NO EFFECT — the target tests behave exactly as before your edit.\n")
	}
	return verifyTruncateOutput(buffer.String(), input.maxBytes)
}

// verifyWriteList prints one class, capped, with the remainder counted rather than dropped.
func verifyWriteList(buffer *strings.Builder, label string, ids []string) {
	fmt.Fprintf(buffer, "%s (%d): %s\n", label, len(ids), verifyJoinIDs(ids))
}

func verifyJoinIDs(ids []string) string {
	if len(ids) <= verifyMaxListedIDs {
		return strings.Join(ids, ", ")
	}
	return strings.Join(ids[:verifyMaxListedIDs], ", ") +
		fmt.Sprintf(", … and %d more", len(ids)-verifyMaxListedIDs)
}

// verifyTruncateOutput enforces the byte cap from the END, so the VERDICT line — the last line and the
// only one that must survive — is never the part that is cut. A verdict without its lists is still a
// verdict; lists without a verdict are evidence, which is what this verb refuses to return.
func verifyTruncateOutput(rendered string, maxBytes int) []byte {
	if maxBytes <= 0 || len(rendered) <= maxBytes {
		return []byte(rendered)
	}
	lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	verdict := lines[len(lines)-1] + "\n"
	if len(verdict) > maxBytes {
		// Even the verdict is too wide, which happens only when its own id list is long. The COUNT is the
		// information and the ids are the bonus, so the list yields — word by word from the end, never the
		// "VERDICT: …" clause itself — rather than the cap yielding. A verdict that overruns the caller's
		// byte budget is the same failure as returning output.
		head := verdict
		if colon := strings.LastIndex(verdict, ": "); colon > 0 {
			head = verdict[:colon+2]
		}
		ids := strings.TrimSuffix(strings.TrimPrefix(verdict, head), "\n")
		for _, part := range strings.Split(ids, ", ") {
			if len(head)+len(part)+len(" …\n") > maxBytes {
				break
			}
			head += part + ", "
		}
		return []byte(strings.TrimSuffix(head, ", ") + " …\n")
	}
	kept, budget := []string{}, maxBytes-len(verdict)
	for _, line := range lines[:len(lines)-1] {
		if len(line)+1 > budget {
			continue
		}
		kept = append(kept, line)
		budget -= len(line) + 1
	}
	return []byte(strings.Join(append(kept, verdict), "\n"))
}
