package bench

import (
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
