package sem

import (
	"encoding/json"
	"github.com/entireio/entire-graph/internal/compiler"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	arms := []string{"current", "current-expansion", "uniform", "weighted"}
	if selected := os.Getenv("ENTIRE_GRAPH_RANK_ARMS"); selected != "" {
		arms = strings.Split(selected, ",")
	}
	repetitions := 10
	if value := os.Getenv("ENTIRE_GRAPH_RANK_REPETITIONS"); value != "" {
		var err error
		repetitions, err = strconv.Atoi(value)
		if err != nil || repetitions < 1 || repetitions > 1000 {
			t.Fatal("repetitions must be 1..1000")
		}
	}
	var backend *CompilerOptions
	if raw := os.Getenv("ENTIRE_GRAPH_RANK_COMPILER_CONFIG"); raw != "" {
		var config compiler.Config
		if err := json.Unmarshal([]byte(raw), &config); err != nil {
			t.Fatal(err)
		}
		backend = &CompilerOptions{Config: config, Require: true}
	}
	seen := map[string]bool{}
	for _, arm := range arms {
		if seen[arm] {
			t.Fatalf("duplicate arm %q", arm)
		}
		seen[arm] = true
		if _, err := graphRankingEvaluationOptions(arm, backend); err != nil {
			t.Fatal(err)
		}
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
		for repetition := 0; repetition < repetitions; repetition++ {
			// Rotate order across all three arms rather than favoring a fixed position.
			for step := 0; step < len(arms); step++ {
				arm := arms[(step+repetition)%len(arms)]
				options, err := graphRankingEvaluationOptions(arm, backend)
				if err != nil {
					t.Fatal(err)
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
