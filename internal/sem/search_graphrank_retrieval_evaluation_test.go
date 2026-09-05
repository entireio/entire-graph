package sem

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Same public retrieval pipeline and candidate scope; only transition weighting
// changes in the private uniform arm. GraphMark owns inputs, labels and scoring.
func TestGraphRankRetrievalAblations(t *testing.T) {
	root, output := os.Getenv("ENTIRE_GRAPH_RANK_CORPUS"), os.Getenv("ENTIRE_GRAPH_RANK_RETRIEVAL_RESULTS")
	if root == "" || output == "" {
		t.Skip("explicit GraphMark corpus and result path required")
	}
	var tasks []struct {
		ID         string            `json:"id"`
		Repository string            `json:"repository"`
		Query      string            `json:"query"`
		Hashes     map[string]string `json:"sha256"`
	}
	data, err := os.ReadFile(filepath.Join(root, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &tasks); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, task := range tasks {
		repo := filepath.Join(root, "fixtures", task.Repository)
		verify := func() {
			t.Helper()
			for path, hash := range task.Hashes {
				bytes, err := os.ReadFile(filepath.Join(repo, path))
				if err != nil || contentHash(bytes) != hash {
					t.Fatalf("fixture changed: %s %v", path, err)
				}
			}
		}
		verify()
		for repetition := 0; repetition < 10; repetition++ {
			// Rotate order across all three arms rather than favoring a fixed position.
			arms := []string{"current", "uniform", "weighted"}
			for step := 0; step < len(arms); step++ {
				arm := arms[(step+repetition)%len(arms)]
				options := SearchOptions{Worktree: true, Profile: ProfileFull, TopK: 8, MaxContextBytes: 4096, DisableCache: true}
				if arm != "current" {
					options.Ranking = "experimental-graph"
					options.rankingEvaluationUniform = arm == "uniform"
				}
				started := time.Now()
				response, err := SearchRepository(t.Context(), repo, "development-ablation", task.Query, options)
				elapsed := time.Since(started)
				errorCode := ""
				if err != nil {
					errorCode = err.Error()
				}
				record := struct {
					Task       string         `json:"task"`
					Arm        string         `json:"arm"`
					Repetition int            `json:"repetition"`
					Seconds    float64        `json:"seconds"`
					Error      string         `json:"error,omitempty"`
					Response   SearchResponse `json:"response"`
				}{task.ID, arm, repetition, elapsed.Seconds(), errorCode, response}
				if err := encoder.Encode(record); err != nil {
					t.Fatal(err)
				}
			}
		}
		verify()
	}
}
