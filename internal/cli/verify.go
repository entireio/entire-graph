package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
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

// verifyBaselineFormatVersion is the on-disk shape this build writes AND the only shape it will
// adjudicate. A baseline is compared field-by-field against ids the current run produced, so a file
// this build cannot claim to understand must be refused rather than half-read.
const verifyBaselineFormatVersion = 1

// verifyBaseline is the on-disk pre-edit record. The format is deliberately boring — a status per id
// plus provenance — because its only consumer is the diff below and its only job is to still be
// readable when the tree it describes is gone.
type verifyBaseline struct {
	FormatVersion int           `json:"format_version"`
	RecordedAt    string        `json:"recorded_at"`
	Repo          string        `json:"repo"`
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
		// runVerifyCommands has already said which, so the message is returned as written rather than
		// re-labelled as a test failure — a setup command that exited nonzero is not a test result.
		return runErr
	}
	results, parser, parsed := parseVerifyOutput(output)

	if flags.RecordBaseline != "" {
		return writeVerifyBaseline(ctx, opts, repo, flags, results, parser, parsed, exitCode)
	}
	baseline, err := readVerifyBaseline(flags.PreEditBaseline)
	if err != nil {
		return err
	}
	if err := validateVerifyBaseline(baseline, flags.PreEditBaseline, repo, flags.Test, parser, parsed); err != nil {
		return err
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
		_, setupExit, err := runVerifyShell(setupCtx, repo, flags.Setup)
		cancel()
		if err != nil {
			return "", 0, fmt.Errorf("verify could not launch the setup command: %w", err)
		}
		// runVerifyShell reports a command that RAN and failed as (exit != 0, nil error), so the exit
		// code is the only evidence setup succeeded. Discarding it lets a failed install or build fall
		// through to the test command, and the results of a tree whose dependencies were never built
		// then get adjudicated as if setup had worked — a verdict about the wrong tree. Refuse instead:
		// a verification tool reporting success on a run that actually failed is the severe outcome.
		if setupExit != 0 {
			return "", setupExit, fmt.Errorf(
				"verify setup command exited %d, so the test command was not run: the tree it would "+
					"have adjudicated was never prepared", setupExit)
		}
	}
	testCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, exitCode, err := runVerifyShell(testCtx, repo, flags.Test)
	if err != nil {
		return "", 0, fmt.Errorf("verify could not run the test command: %w", err)
	}
	return output, exitCode, nil
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
		FormatVersion: verifyBaselineFormatVersion,
		RecordedAt:    time.Now().UTC().Format(time.RFC3339),
		Repo:          verifyRecordedRepo(repo),
		TestCommand:   flags.Test,
		Parser:        parser,
		ExitCode:      exitCode,
		Results:       results,
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

// validateVerifyBaseline refuses a baseline that does not describe THIS run.
//
// The verdict below is a field-by-field diff of two id sets, and nothing in that diff can tell that
// the two sets came from different repositories, different test commands or different runners. A
// baseline recorded elsewhere therefore produces an authoritative-looking PASS or REGRESSION that is
// about nothing at all: every id the other suite reported reads as a disappeared test, and every id
// this one reports reads as brand new. Refusing is the only honest answer, and it is refused loudly
// because the failure mode this verb exists to prevent is a confident verdict on a run that did not
// happen the way the verdict assumes.
func validateVerifyBaseline(
	baseline verifyBaseline, path, repo, testCommand, parser string, parsed bool,
) error {
	if baseline.FormatVersion != verifyBaselineFormatVersion {
		return fmt.Errorf(
			"verify --pre-edit-baseline %s has format_version %d, this build writes %d: re-record it",
			path, baseline.FormatVersion, verifyBaselineFormatVersion)
	}
	if !verifySameRepo(baseline.Repo, repo) {
		return fmt.Errorf(
			"verify --pre-edit-baseline %s was recorded in repository %q but this run is in %q: "+
				"a delta between two repositories is not a delta", path, baseline.Repo, repo)
	}
	if baseline.TestCommand != testCommand {
		return fmt.Errorf(
			"verify --pre-edit-baseline %s recorded test command %q but this run ran %q: "+
				"the two id sets are not comparable", path, baseline.TestCommand, testCommand)
	}
	// Parser is checked only when BOTH sides parsed. An exit-code-only baseline is a legitimate,
	// self-describing degradation that renderVerifyVerdict already handles as coarse mode.
	if parsed && baseline.Parser != "exit-code-only" && baseline.Parser != parser {
		return fmt.Errorf(
			"verify --pre-edit-baseline %s was parsed by %q but this run was parsed by %q: "+
				"ids from two runners do not name the same tests", path, baseline.Parser, parser)
	}
	return nil
}

// verifyRecordedRepo canonicalizes the --repo value for the BASELINE, at record time, which is the
// only moment a relative spelling still means what the caller meant by it.
//
// resolveRepo returns the caller's argument verbatim, so `--repo .` reaches the baseline as ".", and
// a "." on disk is a path relative to whatever directory the NEXT invocation happens to run in. That
// broke the workflow both ways: adjudicating the same repository from anywhere else was refused as
// "a different repository", and — the expensive half — a baseline recorded with `--repo .` in one
// checkout compared EQUAL to `--repo .` in another, because "." is "." wherever each ran, so a
// verdict was rendered about a repository the baseline never described.
//
// Only the recorded side can be fixed here. The current side is resolved against the working
// directory of the invocation that is asking, which is where it belongs.
func verifyRecordedRepo(repo string) string {
	absolute, err := filepath.Abs(repo)
	if err != nil {
		// Abs fails only when the working directory cannot be read. Storing the caller's spelling is
		// then no worse than what was stored before, and refusing to record at all would be worse.
		return repo
	}
	return filepath.Clean(absolute)
}

// verifySameRepo compares two --repo values as locations rather than as strings, so recording with
// `--repo .` and adjudicating with `--repo /abs/path` is not reported as a different repository.
// resolveRepo returns the caller's argument verbatim, which is what makes this necessary; the
// recorded side arrives already canonicalized (verifyRecordedRepo), so the resolution below applies
// to the current invocation's own spelling.
func verifySameRepo(recorded, current string) bool {
	if recorded == current {
		return true
	}
	recordedAbs, recordedErr := filepath.Abs(recorded)
	currentAbs, currentErr := filepath.Abs(current)
	if recordedErr != nil || currentErr != nil {
		return false
	}
	return filepath.Clean(recordedAbs) == filepath.Clean(currentAbs)
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
	// A DISAPPEARED test is the one class the loop above structurally cannot see, because it iterates
	// only ids the CURRENT run reported. An aborted, crashed or truncated run therefore drops every
	// baseline id it never reached, and each of those absences reads as "nothing changed" — which is
	// the single most misleading thing a regression delta can say. Classify them explicitly.
	var notRun []string
	for id := range input.baseline.Results {
		if _, present := input.current[id]; !present {
			notRun = append(notRun, id)
		}
	}
	sort.Strings(newlyPassing)
	sort.Strings(newlyFailing)
	sort.Strings(stillFailing)
	sort.Strings(notRun)

	// An UNEXPLAINED nonzero exit is a run that failed for a reason the per-test output does not
	// contain: a collection error, a configuration error, a build failure, a signal. Once any output
	// parsed, that exit code was the only remaining evidence the run was whole, and ignoring it let a
	// suite that printed one newly passing test and then died be adjudicated "PASS — verification is
	// complete". A nonzero exit is EXPLAINED only when the parsed results themselves carry a failure.
	// The converse hole is just as expensive, and it is the one this rule opened: a single reported
	// failure was then taken to explain ANY nonzero exit, so a run that failed a test AND died —
	// interrupted, segfaulted, OOM-killed, or stopped by a collection or build error — was still
	// adjudicated as merely a test failure. A failure explains an exit code only when that code is one
	// the RUNNER uses to say "a test failed"; see verifyExitCodeMeansTestFailure.
	unexplainedExit := input.exitCode != 0 &&
		!(verifyResultsHaveFailure(input.current) &&
			verifyExitCodeMeansTestFailure(input.parser, input.exitCode))

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
	if len(notRun) > 0 {
		verifyWriteList(&buffer,
			"NOT RUN (in the baseline, absent from this run: aborted, filtered, renamed or deleted)", notRun)
	}

	switch {
	case len(newlyFailing) > 0:
		fmt.Fprintf(&buffer, "VERDICT: REGRESSION in %d test%s: %s\n",
			len(newlyFailing), pluralSuffix(len(newlyFailing)), verifyJoinIDs(newlyFailing))
	case len(notRun) > 0:
		fmt.Fprintf(&buffer,
			"VERDICT: INCOMPLETE — %d baseline test%s did not report in this run, so no claim "+
				"about regressions can be made. Verification is NOT complete.\n",
			len(notRun), pluralSuffix(len(notRun)))
	case unexplainedExit && verifyResultsHaveFailure(input.current):
		fmt.Fprintf(&buffer,
			"VERDICT: INCOMPLETE — the runner %s, which is not how %s reports a test failure, so the "+
				"run ALSO came apart for a reason its per-test output does not name (a crash, a "+
				"signal, a collection or a build error). Verification is NOT complete.\n",
			verifyExitDescription(input.exitCode), input.parser)
	case unexplainedExit:
		fmt.Fprintf(&buffer,
			"VERDICT: INCOMPLETE — the runner %s while every test it reported passed, so it "+
				"failed for a reason its per-test output does not name. Verification is NOT complete.\n",
			verifyExitDescription(input.exitCode))
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

// verifyTestFailureExitCodes is each runner's own code for "a test failed", and nothing else. Every
// other code these runners emit means the run itself came apart — pytest 2 interrupted, 3 internal
// error, 4 usage error, 5 nothing collected; go test 2 for a command-line or build error; cargo's
// harness failing at 101 while cargo itself exits 1 when it could not even build — and none of them
// is explained by a test the run happened to report failing before it died.
//
// An unlisted runner keeps the old rule (any ordinary nonzero is plausible), because refusing a code
// nobody has documented would be a guess in the loud direction about a runner this build does not
// otherwise know. Where a runner IS listed the set is deliberately narrow: a false INCOMPLETE costs
// the caller one investigation, while a false PASS is the failure class this whole verb exists to
// prevent.
var verifyTestFailureExitCodes = map[string][]int{
	"pytest":      {1},
	"cargo test":  {101},
	"go test":     {1},
	"phpunit":     {1},
	"jest/vitest": {1},
	"rspec":       {1},
	"minitest":    {1},
	"surefire":    {1},
	"ctest":       {8},
}

// verifyExitCodeMeansTestFailure reports whether exitCode is one the named runner uses to report a
// test failure — the only kind of nonzero exit a reported failure can explain.
func verifyExitCodeMeansTestFailure(parser string, exitCode int) bool {
	if exitCode < 0 || exitCode >= 128 {
		// No exit status at all (killed before it could exit), or the shell's 128+N for a child that
		// died of a signal. A segfault, an OOM kill and a timeout are not test results whatever the
		// suite printed on its way down.
		return false
	}
	codes, known := verifyTestFailureExitCodes[parser]
	if !known {
		return true
	}
	for _, code := range codes {
		if code == exitCode {
			return true
		}
	}
	return false
}

// verifyExitDescription names what the process did, so a status that is not a code does not get
// printed as one.
func verifyExitDescription(exitCode int) string {
	if exitCode < 0 {
		return "was killed without an exit status"
	}
	return fmt.Sprintf("exited %d", exitCode)
}

// verifyResultsHaveFailure reports whether the parsed results themselves explain a nonzero exit.
func verifyResultsHaveFailure(results verifyResults) bool {
	for _, status := range results {
		if status != verifyStatusPass {
			return true
		}
	}
	return false
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
