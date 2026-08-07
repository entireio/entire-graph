package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/entireio/entire-graph/internal/sem"
)

type measureWorkerRequest struct {
	Name             string      `json:"name"`
	Language         string      `json:"language"`
	Dir              string      `json:"dir"`
	ProviderVersion  string      `json:"provider_version"`
	Profile          sem.Profile `json:"profile"`
	MaxRSSBytes      uint64      `json:"max_rss_bytes"`
	ExactOutputBytes bool        `json:"exact_output_bytes"`
}

type measureWorkerResponse struct {
	Metrics           RepoMetrics         `json:"metrics"`
	Events            []sem.ProgressEvent `json:"-"`
	Error             string              `json:"error,omitempty"`
	WorkerMaxRSSBytes uint64              `json:"worker_max_rss_bytes"`
}

type measureWorkerMessage struct {
	Progress *sem.ProgressEvent     `json:"progress,omitempty"`
	Result   *measureWorkerResponse `json:"result,omitempty"`
}

// MeasureRepoIsolated measures one repository in a fresh worker process. The
// worker captures cold RSS before compact preflight and exits afterward, so
// preflight's process peak cannot leak into the next repository row.
func MeasureRepoIsolated(ctx context.Context, name, language, dir, providerVersion string, profile sem.Profile, opts MeasureOptions, command []string) (RepoMetrics, error) {
	response, err := measureRepoIsolatedWithCommand(ctx, name, language, dir, providerVersion, profile, opts, command, nil)
	if err != nil {
		return failedRepoMetrics(name, language, profile, err), err
	}
	response.Metrics.Name = name
	response.Metrics.Language = language
	if profile == "" {
		profile = sem.ProfileFull
	}
	response.Metrics.Profile = string(profile)
	if response.Error != "" {
		response.Metrics.Error = response.Error
		return response.Metrics, errors.New(response.Error)
	}
	return response.Metrics, nil
}

func failedRepoMetrics(name, language string, profile sem.Profile, err error) RepoMetrics {
	if profile == "" {
		profile = sem.ProfileFull
	}
	message := "isolated measurement failed"
	if err != nil {
		message = err.Error()
	}
	return RepoMetrics{Name: name, Language: language, Profile: string(profile), Error: message}
}

func measureRepoIsolatedWithCommand(ctx context.Context, name, language, dir, providerVersion string, profile sem.Profile, opts MeasureOptions, command, environment []string) (measureWorkerResponse, error) {
	if len(command) == 0 || command[0] == "" {
		return measureWorkerResponse{}, errors.New("isolated measurement requires a worker command")
	}
	request := measureWorkerRequest{
		Name:             name,
		Language:         language,
		Dir:              dir,
		ProviderVersion:  providerVersion,
		Profile:          profile,
		MaxRSSBytes:      opts.MaxRSSBytes,
		ExactOutputBytes: opts.ExactOutputBytes,
	}
	var input bytes.Buffer
	if err := json.NewEncoder(&input).Encode(request); err != nil {
		return measureWorkerResponse{}, err
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin = &input
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return measureWorkerResponse{}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if environment != nil {
		cmd.Env = environment
	} else {
		cmd.Env = os.Environ()
	}
	if err := cmd.Start(); err != nil {
		return measureWorkerResponse{}, fmt.Errorf("start isolated measurement worker: %w", err)
	}
	decoder := json.NewDecoder(stdout)
	var response measureWorkerResponse
	seenResult := false
	for {
		var message measureWorkerMessage
		if err := decoder.Decode(&message); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return measureWorkerResponse{}, fmt.Errorf("decode isolated measurement response: %w", err)
		}
		if message.Progress != nil {
			response.Events = append(response.Events, *message.Progress)
			if opts.Progress != nil {
				opts.Progress(*message.Progress)
			}
		}
		if message.Result != nil {
			resultEvents := response.Events
			response = *message.Result
			response.Events = resultEvents
			seenResult = true
		}
	}
	if err := cmd.Wait(); err != nil {
		return measureWorkerResponse{}, fmt.Errorf("isolated measurement worker: %w: %s", err, stderr.String())
	}
	if !seenResult {
		return measureWorkerResponse{}, errors.New("isolated measurement worker returned no result")
	}
	return response, nil
}

// RunMeasureWorker serves one JSON request from in and writes one JSON
// response to out. It is public only so the graph-bench command can re-exec
// itself as the isolated worker; callers should normally use
// MeasureRepoIsolated.
func RunMeasureWorker(ctx context.Context, in io.Reader, out io.Writer) int {
	return runMeasureWorker(ctx, in, out, runCompactPreflight)
}

func runMeasureWorker(ctx context.Context, in io.Reader, out io.Writer, preflightRunner compactPreflightRunner) int {
	var request measureWorkerRequest
	decoder := json.NewDecoder(in)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		response := measureWorkerResponse{Error: fmt.Sprintf("decode worker request: %v", err)}
		_ = json.NewEncoder(out).Encode(measureWorkerMessage{Result: &response})
		return 2
	}
	encoder := json.NewEncoder(out)
	var streamErr error
	metrics, err := measureRepoWithPreflight(ctx, request.Name, request.Language, request.Dir, request.ProviderVersion, request.Profile, MeasureOptions{
		MaxRSSBytes:      request.MaxRSSBytes,
		ExactOutputBytes: request.ExactOutputBytes,
		Progress: func(event sem.ProgressEvent) {
			if streamErr != nil {
				return
			}
			streamErr = encoder.Encode(measureWorkerMessage{Progress: &event})
		},
	}, preflightRunner)
	response := measureWorkerResponse{
		Metrics:           metrics,
		WorkerMaxRSSBytes: maxRSSBytesCurrent(),
	}
	if err != nil {
		response.Error = err.Error()
	}
	if streamErr != nil {
		return 2
	}
	if encodeErr := encoder.Encode(measureWorkerMessage{Result: &response}); encodeErr != nil {
		return 2
	}
	return 0
}
