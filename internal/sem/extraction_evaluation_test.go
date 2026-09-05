package sem

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Opt-in reproducible product-local evaluation. This generated corpus has no
// external implementation or labels. It does not replace GraphMark's corpus.
func TestExtractionEvaluation(t *testing.T) {
	output := os.Getenv("ENTIRE_GRAPH_EXTRACTION_EVALUATION")
	if output == "" {
		t.Skip("set ENTIRE_GRAPH_EXTRACTION_EVALUATION to write paired observations")
	}
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, size := range []int{12, 120, 600} {
		repo := t.TempDir()
		for i := 0; i < size; i++ {
			body := fmt.Sprintf("package fixture\nfunc F%d() int { return %d }\n", i, i)
			if i > 0 {
				body += fmt.Sprintf("func C%d() int { return F%d() }\n", i, i-1)
			}
			if err := os.WriteFile(filepath.Join(repo, fmt.Sprintf("f%04d.go", i)), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
		for _, profile := range []Profile{ProfileSyntaxOnly, ProfileFast, ProfileFull} {
			for _, scenario := range []string{"cold", "unchanged", "one-edit"} {
				for trial := 0; trial < 30; trial++ {
					cache := t.TempDir()
					options := ProviderSnapshotOptions{Worktree: true, Profile: profile, ExtractionCacheDir: cache, ExtractionReuse: true}
					if scenario != "cold" {
						if _, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "evaluation-v1", options); err != nil {
							t.Fatal(err)
						}
					}
					if scenario == "one-edit" {
						body := fmt.Sprintf("package fixture\nfunc F0() int { return %d }\n", trial+1000)
						if err := os.WriteFile(filepath.Join(repo, "f0000.go"), []byte(body), 0600); err != nil {
							t.Fatal(err)
						}
					}
					type observation struct {
						Size      int              `json:"size"`
						Profile   Profile          `json:"profile"`
						Scenario  string           `json:"scenario"`
						Trial     int              `json:"trial"`
						Reuse     bool             `json:"reuse"`
						ElapsedNS int64            `json:"elapsed_ns"`
						Stats     *ExtractionStats `json:"extraction,omitempty"`
						Equal     bool             `json:"equal"`
					}
					var snapshots [2]ProviderSnapshot
					var observations [2]observation
					for step := 0; step < 2; step++ {
						arm := (step + trial) % 2
						options.ExtractionReuse = arm == 1
						started := time.Now()
						snapshot, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "evaluation-v1", options)
						elapsed := time.Since(started)
						if err != nil {
							t.Fatal(err)
						}
						observations[step] = observation{size, profile, scenario, trial, arm == 1, elapsed.Nanoseconds(), snapshot.Header.Stats.Extraction, false}
						if arm == 1 {
							assertCaptureProvenance(t, snapshot)
						}
						snapshot.Header.OperationInputs = nil // separately validated opt-in provenance; semantic digests remain
						snapshot.Header.Stats.Extraction = nil
						snapshots[arm] = snapshot
					}
					equal := reflect.DeepEqual(snapshots[0], snapshots[1])
					for _, observation := range observations {
						observation.Equal = equal
						if err := encoder.Encode(observation); err != nil {
							t.Fatal(err)
						}
					}
					if !equal {
						t.Fatalf("semantic drift size=%d profile=%s scenario=%s trial=%d", size, profile, scenario, trial)
					}
				}
			}
		}
	}
}
