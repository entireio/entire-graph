package bench

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestCompactPreflightReportsRawBytesAndBytesPerProjectedFact(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", `package main

func helper() {}
func main() { helper() }
`)

	got, err := runCompactPreflight(t.Context(), dir, "test-version", sem.ProfileFull)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectedFacts != got.Files+got.Externals+got.Symbols+got.Relations {
		t.Fatalf("projected facts = %d, want files + externals + symbols + relations: %#v", got.ProjectedFacts, got)
	}
	if got.CompactRawBytes <= got.CompactDictionaryBytes || got.CompactDictionaryBytes <= 0 {
		t.Fatalf("compact raw/dictionary bytes = %d/%d, want dictionary included in nonempty raw artifact", got.CompactRawBytes, got.CompactDictionaryBytes)
	}
	if got.NDJSONRawBytes <= 0 || got.NDJSONBytesPerProjectedFact <= 0 || got.CompactBytesPerProjectedFact <= 0 {
		t.Fatalf("missing raw or fact-normalized bytes: %#v", got)
	}
}

func TestCompactPreflightRejectsArtifactTheProductionLoaderCannotLoad(t *testing.T) {
	_, err := verifyCompactPreflight(snapshotProjection{}, []byte("[\"h\",1,{}]\n"), "native-hash", 0, 0)
	if err == nil {
		t.Fatal("expected compact preflight to reject artifact the production loader rejects")
	}
}

func TestCompactPreflightCanonicalHashAndProjectionMatchNative(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", `package main

func helper() {}
func main() { helper() }
`)
	got, err := runCompactPreflight(t.Context(), dir, "test-version", sem.ProfileFull)
	if err != nil {
		t.Fatal(err)
	}
	if got.CanonicalSemanticHash == "" {
		t.Fatal("missing canonical semantic hash")
	}
	if !got.ProjectionMatchesNative {
		t.Fatalf("compact projection did not match native: %#v", got)
	}
}

// newerSchemaMinorVersion returns this build's schema version with the minor
// bumped by one: a readable major, so the loader serves it, but declaring a
// minor whose additive facts this build did not read.
func newerSchemaMinorVersion(t *testing.T) string {
	t.Helper()
	major, minor, found := strings.Cut(sem.SchemaVersion, ".")
	if !found {
		t.Fatalf("schema version %q is not major.minor", sem.SchemaVersion)
	}
	value, err := strconv.Atoi(minor)
	if err != nil {
		t.Fatalf("schema version %q has an unreadable minor: %v", sem.SchemaVersion, err)
	}
	declared := major + "." + strconv.Itoa(value+1)
	// Pin the premise: if this is not actually a newer minor of a readable major,
	// the test below would pass for the wrong reason.
	newerMinor, err := sem.CheckReadableSchemaVersion(declared)
	if err != nil || !newerMinor {
		t.Fatalf("constructed schema %q is not a newer minor of a readable major: newerMinor=%v err=%v",
			declared, newerMinor, err)
	}
	return declared
}

// compactPreflightInputs runs the preflight's own producer over dir and returns
// exactly what verifyCompactPreflight consumes, optionally replacing the schema
// version the header declares. Rewriting the live header struct suffices here —
// unlike the sem fixture, which edits the serialized line — because the case
// that matters for the preflight is a version that IS present and parses: an
// absent or unplaceable one is already refused outright by the loader.
func compactPreflightInputs(t *testing.T, dir, declaredSchema string) (snapshotProjection, []byte, string) {
	t.Helper()
	var compact bytes.Buffer
	encoder := sem.NewCompactSnapshotEncoder(&compact)
	hasher := sem.NewSnapshotSemanticHasher()
	projection := snapshotProjection{}
	err := sem.StreamSnapshot(t.Context(), dir, "test-version", sem.ProviderSnapshotOptions{NoNetwork: true, Profile: sem.ProfileFull}, func(record any) error {
		if header, ok := record.(sem.SnapshotHeader); ok && declaredSchema != "" {
			header.SchemaVersion = declaredSchema
			record = header
		}
		if err := hasher.Add(record); err != nil {
			return err
		}
		if err := projection.add(record); err != nil {
			return err
		}
		return encoder.Encode(record)
	})
	if err != nil {
		t.Fatalf("stream compact preflight snapshot: %v", err)
	}
	return projection, compact.Bytes(), hasher.SumHex()
}

// A newer minor of a readable major LOADS — that is ADR 0001 clause 3 — so the
// loader reports it as a warning instead of an error. The preflight used to drop
// that warning on the floor and certify the artifact anyway, which is wrong for
// this call site specifically: the artifact was produced by this same build, so
// a newer minor means the writer and the reader disagree, and the preflight was
// about to report bytes-per-fact for facts it never read.
func TestCompactPreflightRejectsArtifactLoadedUnderANewerSchemaMinor(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", `package main

func helper() {}
func main() { helper() }
`)

	// Control: the identical pipeline at this build's own schema must pass, so
	// the refusal below is attributable to the declared version and nothing else.
	projection, compact, hash := compactPreflightInputs(t, dir, "")
	if _, err := verifyCompactPreflight(projection, compact, hash, len(compact), 0); err != nil {
		t.Fatalf("preflight refused an artifact declaring this build's own schema %s: %v", sem.SchemaVersion, err)
	}

	declared := newerSchemaMinorVersion(t)
	projection, compact, hash = compactPreflightInputs(t, dir, declared)
	_, err := verifyCompactPreflight(projection, compact, hash, len(compact), 0)
	if err == nil {
		t.Fatalf("preflight certified an artifact declaring schema %s, whose additive facts it never read", declared)
	}
	if !strings.Contains(err.Error(), "W_NEWER_SCHEMA_MINOR") || !strings.Contains(err.Error(), declared) {
		t.Fatalf("error = %q, want it to name W_NEWER_SCHEMA_MINOR and the declared schema %s", err, declared)
	}
}
