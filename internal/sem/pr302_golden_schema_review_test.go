package sem

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
)

// These reproduce the existing golden fixtures and SearchOptions exactly. The
// new golden must match before normalization, and clearing only the additive
// SymbolNameLine field must recover the original golden. This catches any other
// changed result field/order that merely updating the expected hashes would hide.
// Worktree is the test-created directory; empty CacheDir disables cache reuse.
func TestReview302GoldenSchemaCodeResultOrder(t *testing.T) {
	repo := t.TempDir()
	for index, name := range []string{"Fast", "Queued", "Retried", "Timed"} {
		write(t, repo, fmt.Sprintf("delivery/%02d_%s.go", index, name), fmt.Sprintf(`package delivery

// %sDelivery retries delivery with exponential backoff.
func %sDelivery() {}
`, name, name))
	}
	response, err := SearchRepository(
		t.Context(), repo, "test", "retry delivery exponential backoff", SearchOptions{
			Worktree: true, Profile: ProfileSyntaxOnly, TopK: 4,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	review302RequireSchemaOnlyGolden(t, response.Results,
		"0952d01a95c4b24d1eed6a9b5b669531f77e6c8f980acbfeb3f134bbf113ff39",
		"4e88510273830fef30a7aeb44845893c24e723fdf53dfd68f879c44a6f1dbf35")
}

func TestReview302GoldenSchemaProseLaneThreshold(t *testing.T) {
	repo := t.TempDir()
	for index := 0; index < 4; index++ {
		write(t, repo, fmt.Sprintf("notes/session-%d.md", index),
			fmt.Sprintf("# Amber orchard ledger note%d\n", index))
	}
	for index := 0; index < 2; index++ {
		write(t, repo, fmt.Sprintf("src/worker%d.go", index), fmt.Sprintf(`package src

// Worker%d handles amber orchard ledger records.
func Worker%d() {}
`, index, index))
	}
	response, err := SearchRepository(
		t.Context(), repo, "test", "amber orchard ledger", SearchOptions{
			Worktree: true, Profile: ProfileSyntaxOnly, TopK: 6,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	review302RequireSchemaOnlyGolden(t, response.Results,
		"b3dec24fba4d36dd28229fd43ed4a3c4fcc62bd5475f6caf99fa46b87e319aae",
		"e9733475f04142cfe8fd78b823651105cd55eac581d38430c1b92424aa4a2a5b")
}

func review302RequireSchemaOnlyGolden(t *testing.T, results []SearchResult, currentGolden, originalGolden string) {
	t.Helper()
	if len(results) == 0 {
		t.Fatal("schema-only comparison requires nonempty results")
	}
	positive := 0
	for _, result := range results {
		if result.SymbolNameLine > 0 {
			positive++
			t.Logf("name coordinate: %s %s:%d", result.SymbolName, result.FilePath, result.SymbolNameLine)
		}
	}
	if positive == 0 {
		t.Fatal("schema-only comparison requires at least one positive SymbolNameLine")
	}
	t.Logf("results=%d, positive name coordinates=%d", len(results), positive)
	review302AssertResultDigest(t, "current schema", results, currentGolden)

	// Only this scalar field changes. Copying the result structs preserves the
	// response itself, and no nested slice/map or other result field is mutated.
	withoutNameLine := append([]SearchResult(nil), results...)
	for i := range withoutNameLine {
		withoutNameLine[i].SymbolNameLine = 0
	}
	review302AssertResultDigest(t, "only SymbolNameLine cleared", withoutNameLine, originalGolden)
}

func review302AssertResultDigest(t *testing.T, label string, results []SearchResult, want string) {
	t.Helper()
	encoded, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%x", sha256.Sum256(encoded))
	t.Logf("%s result JSON SHA-256 = %s", label, got)
	if got != want {
		t.Errorf("%s result JSON SHA-256 = %s, want %s\nJSON: %s", label, got, want, encoded)
	}
}
