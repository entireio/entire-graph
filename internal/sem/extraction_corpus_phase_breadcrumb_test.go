package sem

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

const extractionCorpusPhaseBreadcrumbChildEnv = "ENTIRE_GRAPH_PHASE_BREADCRUMB_CHILD"

func TestExtractionCorpusPhaseBreadcrumbOptIn(t *testing.T) {
	t.Setenv(extractionCorpusPhaseBreadcrumbsEnv, "1")
	if !extractionCorpusPhaseBreadcrumbsEnabled() {
		t.Fatal("1 should enable phase breadcrumbs")
	}
	t.Setenv(extractionCorpusPhaseBreadcrumbsEnv, "0")
	if extractionCorpusPhaseBreadcrumbsEnabled() {
		t.Fatal("0 should disable phase breadcrumbs")
	}
}

func TestExtractionCorpusPhaseBreadcrumbsDisabled(t *testing.T) {
	var output bytes.Buffer
	writer := newExtractionCorpusPhaseBreadcrumbWriter(false, &output)
	writer.recordEssential("request_started", 0, nil)
	writer.recordProgress(time.Millisecond, ProgressEvent{Phase: BuildPhaseParse, FilesDone: 2, FilesTotal: 3})
	writer.flushProgress(2 * time.Millisecond)
	writer.close()
	if output.Len() != 0 {
		t.Fatalf("disabled breadcrumbs wrote %d bytes", output.Len())
	}
}

func TestExtractionCorpusPhaseBreadcrumbsRetainWriteError(t *testing.T) {
	writer := newExtractionCorpusPhaseBreadcrumbWriter(true, phaseBreadcrumbErrorWriter{})
	writer.recordEssential("request_started", 0, nil)
	if writer.err() == nil {
		t.Fatal("stderr write failure was not retained")
	}
}

type phaseBreadcrumbErrorWriter struct{}

func (phaseBreadcrumbErrorWriter) Write([]byte) (int, error) {
	return 0, errors.New("diagnostic stream closed")
}

func TestExtractionCorpusPhaseBreadcrumbsBoundedAndRetainTail(t *testing.T) {
	var output bytes.Buffer
	writer := newExtractionCorpusPhaseBreadcrumbWriter(true, &output)
	writer.recordEssential("request_started", 0, nil)
	for i := 0; i < 10000; i++ {
		writer.recordProgress(time.Duration(i+1)*time.Microsecond, ProgressEvent{
			Phase:        BuildPhaseParse,
			PhaseElapsed: time.Duration(i+1) * time.Microsecond,
			FilesDone:    i,
			FilesTotal:   10000,
			Symbols:      i * 2,
			Relations:    i * 3,
		})
	}
	writer.recordProgress(11*time.Second, ProgressEvent{Phase: BuildPhaseRelations, FilesDone: 10000, FilesTotal: 10000, Symbols: 20000, Relations: 30000})
	writer.flushProgress(12 * time.Second)
	writer.recordEssential("api_returned", 13*time.Second, nil)
	writer.recordEssential("serialization_started", 13*time.Second, nil)
	writer.recordEssential("serialization_ended", 14*time.Second, nil)
	writer.recordEssential("full_artifact_write_started", 14*time.Second, nil)
	writer.recordEssential("full_artifact_write_ended", 15*time.Second, nil)
	writer.close()

	if output.Len() > extractionCorpusPhaseBreadcrumbMaxBytes {
		t.Fatalf("phase breadcrumbs are %d bytes, limit is %d", output.Len(), extractionCorpusPhaseBreadcrumbMaxBytes)
	}
	var events []extractionCorpusPhaseBreadcrumb
	scanner := bufio.NewScanner(bytes.NewReader(output.Bytes()))
	for scanner.Scan() {
		var breadcrumb extractionCorpusPhaseBreadcrumb
		if err := json.Unmarshal(scanner.Bytes(), &breadcrumb); err != nil {
			t.Fatal(err)
		}
		events = append(events, breadcrumb)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(events) > extractionCorpusPhaseBreadcrumbMaxEvents {
		t.Fatalf("phase breadcrumbs have %d records, limit is %d", len(events), extractionCorpusPhaseBreadcrumbMaxEvents)
	}
	var names []string
	for _, event := range events {
		names = append(names, event.Event)
	}
	for _, want := range []string{
		"snapshot_phase_start", "snapshot_phase_end", "api_returned",
		"serialization_started", "serialization_ended",
		"full_artifact_write_started", "full_artifact_write_ended",
	} {
		if !containsPhaseBreadcrumbEvent(names, want) {
			t.Fatalf("tail marker %q missing from %v", want, names)
		}
	}
	var parseEnd extractionCorpusPhaseBreadcrumb
	for _, event := range events {
		if event.Event == "snapshot_phase_end" && event.Phase == string(BuildPhaseParse) {
			parseEnd = event
		}
	}
	if parseEnd.Events != 10000 {
		t.Fatalf("parse event count = %d, want 10000", parseEnd.Events)
	}
}

func TestExtractionCorpusPhaseBreadcrumbSubprocess(t *testing.T) {
	if os.Getenv(extractionCorpusPhaseBreadcrumbChildEnv) == "1" {
		writer := newExtractionCorpusPhaseBreadcrumbWriter(true, os.Stderr)
		writer.recordEssential("request_started", 0, nil)
		for i := 0; i < 10000; i++ {
			writer.recordProgress(time.Duration(i+1)*time.Microsecond, ProgressEvent{Phase: BuildPhaseParse, FilesDone: i, FilesTotal: 10000})
		}
		fmt.Fprintln(os.Stderr, "ordinary-child-output")
		writer.recordProgress(time.Second, ProgressEvent{Phase: BuildPhaseRelations, FilesDone: 10000, FilesTotal: 10000})
		writer.flushProgress(2 * time.Second)
		writer.recordEssential("api_returned", 3*time.Second, nil)
		writer.recordEssential("serialization_started", 3*time.Second, nil)
		writer.recordEssential("serialization_ended", 4*time.Second, nil)
		// The parent kills this process to model a timeout. All records above
		// use the inherited stderr descriptor and have already been written.
		time.Sleep(30 * time.Second)
		return
	}

	path := t.TempDir() + "/captured-stderr.log"
	captured, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^TestExtractionCorpusPhaseBreadcrumbSubprocess$")
	cmd.Env = append(os.Environ(), extractionCorpusPhaseBreadcrumbChildEnv+"=1")
	cmd.Stderr = captured
	if err := cmd.Start(); err != nil {
		captured.Close()
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	if err := cmd.Process.Kill(); err != nil {
		captured.Close()
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		captured.Close()
		t.Fatal("child unexpectedly completed instead of being killed")
	}
	if err := captured.Close(); err != nil {
		t.Fatal(err)
	}

	var names []string
	ordinaryOutput := false
	scanner := bufio.NewScanner(bytes.NewReader(mustReadFile(t, path)))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "ordinary-child-output" {
			ordinaryOutput = true
			continue
		}
		var breadcrumb extractionCorpusPhaseBreadcrumb
		if err := json.Unmarshal([]byte(line), &breadcrumb); err != nil {
			t.Fatalf("captured stderr line %q: %v", line, err)
		}
		names = append(names, breadcrumb.Event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !ordinaryOutput {
		t.Fatal("ordinary stderr output was not retained beside breadcrumbs")
	}
	for _, want := range []string{"snapshot_phase_start", "snapshot_phase_end", "api_returned", "serialization_started", "serialization_ended"} {
		if !containsPhaseBreadcrumbEvent(names, want) {
			t.Fatalf("captured stderr missing %q: %v", want, names)
		}
	}
	if len(names) > extractionCorpusPhaseBreadcrumbMaxEvents {
		t.Fatalf("captured breadcrumbs have %d records, limit is %d", len(names), extractionCorpusPhaseBreadcrumbMaxEvents)
	}
}

func containsPhaseBreadcrumbEvent(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
