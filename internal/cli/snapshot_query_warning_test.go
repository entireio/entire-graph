package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// failingWriter refuses every write, standing in for a closed pipe or a full
// disk on the stream the mandatory warning is owed to.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("stderr is gone") }

// writeCompactSnapshotDeclaring builds a minimal, valid compact snapshot whose
// header declares the given schema version.
func writeCompactSnapshotDeclaring(t *testing.T, version string) string {
	t.Helper()
	var buf bytes.Buffer
	encoder := sem.NewCompactSnapshotEncoder(&buf)
	if err := encoder.Encode(sem.SnapshotHeader{SchemaVersion: version, RepoKey: "local/x", Tree: "t"}); err != nil {
		t.Fatalf("encode header: %v", err)
	}
	if err := encoder.Encode(sem.SnapshotSummary{}); err != nil {
		t.Fatalf("encode summary: %v", err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.compact")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	return path
}

// newerSchemaMinorThanThisBuild returns a version one minor above this build's,
// which ADR 0001 clause 3 says must LOAD and must WARN.
func newerSchemaMinorThanThisBuild(t *testing.T) string {
	t.Helper()
	parts := strings.SplitN(sem.SchemaVersion, ".", 2)
	if len(parts) != 2 {
		t.Fatalf("SchemaVersion %q is not major.minor", sem.SchemaVersion)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		t.Fatalf("SchemaVersion minor %q: %v", parts[1], err)
	}
	return parts[0] + "." + strconv.Itoa(minor+1)
}

// TestSnapshotQueryFailsWhenTheMandatoryWarningCannotBeWritten covers ADR 0001
// clause 3's teeth. The newer-minor warning tells the consumer that additive
// facts were skipped; if it cannot be delivered, serving the records anyway
// leaves the consumer believing it received everything — which is the exact
// condition the clause exists to prevent, and it is silent by construction,
// because nothing downstream can tell it was owed a warning.
func TestSnapshotQueryFailsWhenTheMandatoryWarningCannotBeWritten(t *testing.T) {
	t.Parallel()
	path := writeCompactSnapshotDeclaring(t, newerSchemaMinorThanThisBuild(t))

	var stdout bytes.Buffer
	err := runSnapshotQuery(Options{Stdout: &stdout, Stderr: failingWriter{}}, []string{"--input", path, "--symbol", "nothing"})
	if err == nil {
		t.Fatal("the query served records after failing to deliver the mandatory compatibility warning")
	}
	if !strings.Contains(err.Error(), "schema compatibility warning") {
		t.Fatalf("the error must name what could not be delivered, got %v", err)
	}
}

// The control: with a working stderr the same snapshot is served, and the
// warning appears there rather than on stdout, which is the record stream.
func TestSnapshotQueryWarnsOnStderrAndStillServes(t *testing.T) {
	t.Parallel()
	newer := newerSchemaMinorThanThisBuild(t)
	path := writeCompactSnapshotDeclaring(t, newer)

	var stdout, stderr bytes.Buffer
	if err := runSnapshotQuery(Options{Stdout: &stdout, Stderr: &stderr}, []string{"--input", path, "--symbol", "nothing"}); err != nil {
		t.Fatalf("a newer minor of a readable major must still be served: %v", err)
	}
	if !strings.Contains(stderr.String(), "W_NEWER_SCHEMA_MINOR") || !strings.Contains(stderr.String(), newer) {
		t.Fatalf("stderr must carry the warning naming the version: %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "W_NEWER_SCHEMA_MINOR") {
		t.Fatalf("the warning must not pollute the record stream on stdout: %q", stdout.String())
	}
}
