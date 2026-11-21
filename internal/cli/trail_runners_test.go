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
//
// The assertion is over every enabled runner found on disk rather than over a
// list of names, because the failure mode is the runner added later with the
// trigger block copied from the wrong sibling. If a future runner genuinely
// should answer only to an explicit API call, add it to apiOnlyByDesign below
// with the reason -- the point is that the choice is stated, not inferred
// from a file nobody reads.
func TestEveryEnabledTrailRunnerFiresOnAPush(t *testing.T) {
	t.Parallel()

	// Runners that are api-only on purpose, each with the reason it is.
	apiOnlyByDesign := map[string]string{}

	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	dir := filepath.Join(strings.TrimSpace(string(root)), ".entire", "runners")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no checked-in runners at %s: %v", dir, err)
	}

	found, checked := 0, 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		found++
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		var runner struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
			Select  struct {
				TriggerTypes []string `json:"trigger_types"`
			} `json:"select"`
		}
		if err := json.Unmarshal(raw, &runner); err != nil {
			t.Fatalf("%s is not valid JSON, so nothing reads it: %v", entry.Name(), err)
		}
		if !runner.Enabled {
			continue
		}
		checked++
		if reason, ok := apiOnlyByDesign[runner.ID]; ok {
			if slices.Contains(runner.Select.TriggerTypes, "push") {
				t.Errorf("%s lists \"push\" but is recorded as api-only by design (%q): drop it from apiOnlyByDesign",
					entry.Name(), reason)
			}
			continue
		}
		if !slices.Contains(runner.Select.TriggerTypes, "push") {
			t.Errorf("%s is enabled with trigger_types %v, which never includes a push, so this runner produces nothing on any pushed branch and its absence from a trail is indistinguishable from it having nothing to say; add \"push\", or record it in apiOnlyByDesign with the reason",
				entry.Name(), runner.Select.TriggerTypes)
		}
	}

	// A skip here prints its reason, so the test cannot stop testing in
	// silence. It is a skip rather than a failure because the file-runner
	// mechanism may legitimately go away -- PR #388 removes all seven of these
	// files -- and because disabling every runner is a configuration choice,
	// not a defect this test is entitled to veto.
	switch {
	case found == 0:
		t.Skipf("non-vacuity: %s holds no runner JSON, so this proves nothing; if the file runners were removed, delete this test with them", dir)
	case checked == 0:
		t.Skipf("non-vacuity: all %d runners in %s are disabled, so this proves nothing", found, dir)
	}
}
