package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// checkedInRunner is the part of a .entire/runners/*.json file these tests
// reason about. The platform reads far more; these are the fields that decide
// whether the runner ever runs and whether its output reaches a reviewer.
type checkedInRunner struct {
	File   string
	ID     string `json:"id"`
	Enable bool   `json:"enabled"`
	Select struct {
		TriggerTypes []string `json:"trigger_types"`
	} `json:"select"`
	Prompt struct {
		Template string `json:"template"`
	} `json:"prompt"`
	Output struct {
		TrailField   string `json:"trail_field"`
		TrailMonitor *struct {
			Key       string `json:"key"`
			Label     string `json:"label"`
			ValueType string `json:"value_type"`
			Polarity  string `json:"polarity"`
		} `json:"trail_monitor"`
	} `json:"output"`
	TrailsReview struct {
		Enabled bool `json:"enabled"`
	} `json:"trails_review"`
}

// loadEnabledTrailRunners reads every enabled runner checked in under
// .entire/runners. It returns the runners plus the count of files seen, so a
// caller can tell "all disabled" from "no files at all" -- two very different
// reasons for an empty set, and both of them make an assertion vacuous.
//
// The tests below assert over whatever is found rather than over a list of
// names on purpose: the failure mode they exist for is the runner added later
// with a block copied from the wrong sibling, which a name list never sees.
func loadEnabledTrailRunners(t *testing.T) (runners []checkedInRunner, filesFound int) {
	t.Helper()

	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	dir := filepath.Join(strings.TrimSpace(string(root)), ".entire", "runners")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no checked-in runners at %s: %v", dir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		filesFound++
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		var runner checkedInRunner
		if err := json.Unmarshal(raw, &runner); err != nil {
			t.Fatalf("%s is not valid JSON, so nothing reads it: %v", entry.Name(), err)
		}
		runner.File = entry.Name()
		if runner.Enable {
			runners = append(runners, runner)
		}
	}
	return runners, filesFound
}

// skipIfNothingToCheck turns an empty set into a stated skip rather than a
// silent pass. It is a skip and not a failure because the file-runner
// mechanism may legitimately go away -- PR #388 removes all seven of these
// files -- and because disabling every runner is a configuration choice, not a
// defect these tests are entitled to veto.
func skipIfNothingToCheck(t *testing.T, runners []checkedInRunner, filesFound int) {
	t.Helper()
	switch {
	case filesFound == 0:
		t.Skip("non-vacuity: no runner JSON is checked in, so this proves nothing; if the file runners were removed, delete this test with them")
	case len(runners) == 0:
		t.Skipf("non-vacuity: all %d checked-in runners are disabled, so this proves nothing", filesFound)
	}
}

// TestEveryEnabledTrailRunnerFiresOnAPush guards the one thing a checked-in
// runner cannot tell you by reading it: whether it ever runs.
//
// A runner's select.trigger_types names the events that start it. A trail
// gathers its evidence when a branch is pushed, so a runner that lists only
// "api" waits for a caller that nothing in the normal flow provides: it is
// enabled, it is valid, it is wired to an output adapter, and it produces
// nothing on any branch anyone pushes. There is no error and no empty result
// to notice -- the monitor is simply absent from the trail, which reads the
// same as a monitor that had nothing to say.
//
// That is not hypothetical. trail-semantic-diff shipped gated to ["api"]
// alone while the other six runners all listed "push", so the semantic diff
// was the one monitor that never appeared, in the repository whose product is
// the semantic diff.
func TestEveryEnabledTrailRunnerFiresOnAPush(t *testing.T) {
	t.Parallel()

	// Runners that are api-only on purpose, each with the reason it is. The
	// point is that the choice is stated, not inferred from a file nobody reads.
	apiOnlyByDesign := map[string]string{}

	runners, filesFound := loadEnabledTrailRunners(t)
	for _, runner := range runners {
		if reason, ok := apiOnlyByDesign[runner.ID]; ok {
			if slices.Contains(runner.Select.TriggerTypes, "push") {
				t.Errorf("%s lists \"push\" but is recorded as api-only by design (%q): drop it from apiOnlyByDesign",
					runner.File, reason)
			}
			continue
		}
		if !slices.Contains(runner.Select.TriggerTypes, "push") {
			t.Errorf("%s is enabled with trigger_types %v, which never includes a push, so this runner produces nothing on any pushed branch and its absence from a trail is indistinguishable from it having nothing to say; add \"push\", or record it in apiOnlyByDesign with the reason",
				runner.File, runner.Select.TriggerTypes)
		}
	}
	skipIfNothingToCheck(t, runners, filesFound)
}

// TestEveryPushTriggeredRunnerCarriesFirstClassTrailOutput guards the second
// gate on push firing, which is the one that is invisible from this repository.
//
// Listing "push" is necessary and not sufficient. The platform will only start
// a pushed trail-prompt runner when its output also lands somewhere a trail
// renders. In entire.io, api/src/lib/agents/push-router.ts:
//
//	function hasFirstClassTrailOutput(config) {
//	  return !!config.output?.trailMonitor || config.output?.trailField === "body"
//	}
//
// and isPushTriggeredTrailPromptConfig requires it, while review runners take
// the separate trails_review.enabled route. So there are exactly three ways to
// clear the gate, and a runner that takes none of them silently does nothing on
// a push no matter what its trigger_types say.
//
// This cost a release. trail-semantic-diff was fixed once by adding "push" to
// trigger_types alone, which looked like the whole repair and changed nothing:
// it carried no trail_monitor, no trail_field and no review flag, so it still
// never fired.
//
// This test encodes another repository's predicate. If push-router.ts changes
// its rules, this test is stale rather than wrong -- re-read the function above
// before weakening anything here.
func TestEveryPushTriggeredRunnerCarriesFirstClassTrailOutput(t *testing.T) {
	t.Parallel()

	runners, filesFound := loadEnabledTrailRunners(t)
	pushTriggered := 0
	for _, runner := range runners {
		if !slices.Contains(runner.Select.TriggerTypes, "push") {
			continue
		}
		pushTriggered++
		switch {
		case runner.TrailsReview.Enabled:
		case runner.Output.TrailMonitor != nil:
		case runner.Output.TrailField == "body":
		default:
			t.Errorf("%s lists \"push\" but has no output a pushed trail accepts: it sets no output.trail_monitor, no output.trail_field \"body\" and no trails_review.enabled, so push-router's hasFirstClassTrailOutput rejects it and listing \"push\" buys nothing. Give it a trail_monitor, write it to the trail body, or stop claiming it runs on a push",
				runner.File)
		}
	}

	skipIfNothingToCheck(t, runners, filesFound)
	if pushTriggered == 0 {
		t.Skipf("non-vacuity: none of the %d enabled runners lists \"push\", so the output gate was never reached", len(runners))
	}
}

// TestEveryTrailMonitorRunnerPromptAsksForTheValueItDeclares guards the join
// between a runner's config and its prompt, which nothing else checks and which
// fails in the most expensive possible way.
//
// A trail_monitor block declares a value_type. The platform then parses the
// agent's final JSON for a value of exactly that shape and for a rationale
// string, in entire.io api/src/lib/sandbox-runs/automation-results.ts:
//
//	percent -> record.value | record.score, 0-100
//	size    -> record.value | record.size, "small" | "medium" | "large"
//	boolean -> record.value | record.boolean
//	rationale -> record.rationale | record.summary
//
// If the prompt never asks the agent for those fields, parseTrailMonitorOutput
// fails -- and applyTrailOutput runs BEFORE the result is stored, so
// completePromptAutomationResultFromText calls failPromptAutomationResult and
// returns nil. The narrative is discarded along with the value. A prompt that
// forgets its own monitor field does not degrade; it destroys the whole run.
//
// So this asserts the prompt names a field the platform will read for the value
// and one it will read for the rationale, and for a size monitor that it names
// all three legal values. It is a shallow textual check on purpose: it cannot
// prove the agent complies, only that the prompt ever asked.
func TestEveryTrailMonitorRunnerPromptAsksForTheValueItDeclares(t *testing.T) {
	t.Parallel()

	// Keys automation-results.ts will read, per declared value_type.
	valueKeys := map[string][]string{
		"percent": {"value", "score"},
		"size":    {"value", "size"},
		"boolean": {"value", "boolean"},
	}
	rationaleKeys := []string{"rationale", "summary"}

	runners, filesFound := loadEnabledTrailRunners(t)
	monitors := 0
	for _, runner := range runners {
		monitor := runner.Output.TrailMonitor
		if monitor == nil {
			continue
		}
		monitors++
		prompt := runner.Prompt.Template

		accepted, known := valueKeys[monitor.ValueType]
		if !known {
			t.Errorf("%s declares value_type %q, which automation-results.ts does not parse; it accepts percent, size or boolean",
				runner.File, monitor.ValueType)
			continue
		}
		if !namesOneOf(prompt, accepted) {
			t.Errorf("%s declares a %s monitor but its prompt never asks the agent for %v, so parseTrailMonitorOutput finds no value, the run is failed and the narrative is discarded with it",
				runner.File, monitor.ValueType, quoted(accepted))
		}
		if !namesOneOf(prompt, rationaleKeys) {
			t.Errorf("%s declares a monitor but its prompt never asks the agent for %v, and a monitor with no rationale is rejected outright",
				runner.File, quoted(rationaleKeys))
		}
		if monitor.ValueType == "size" {
			for _, allowed := range []string{"small", "medium", "large"} {
				if !strings.Contains(prompt, allowed) {
					t.Errorf("%s declares a size monitor but its prompt never mentions %q; a size monitor accepts only small, medium and large, and anything else fails the run",
						runner.File, allowed)
				}
			}
		}
	}

	skipIfNothingToCheck(t, runners, filesFound)
	if monitors == 0 {
		t.Skipf("non-vacuity: none of the %d enabled runners declares a trail_monitor, so nothing here was checked", len(runners))
	}
}

// namesOneOf reports whether the prompt mentions any of these keys as a JSON
// field. It looks for the quoted key, which is how every runner prompt states
// its output schema, so a key named only in prose does not count.
func namesOneOf(prompt string, keys []string) bool {
	for _, key := range keys {
		if strings.Contains(prompt, `"`+key+`"`) {
			return true
		}
	}
	return false
}

func quoted(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, `"`+key+`"`)
	}
	return out
}
