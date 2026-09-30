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
	// Combined with the complete-symbol certification, the two top delivery functions each gain a
	// "complete-symbol" signal; the guard below recovers both standalone goldens from these results.
	review302RequireSchemaOnlyGolden(t, response.Results,
		"6cc6dc4cc14e385f545a33b20896e6957c8813fd70f4f43013f8599e06442f59",
		"e9210f8962e96fc00a4141dfb3cd01d16bd3afc5a3e75aadb8c2dcd7a4a33647")
	review302RequireCompleteSignalsOnly(t, response.Results, 4, 4,
		[]string{"FastDelivery delivery/00_Fast.go", "QueuedDelivery delivery/01_Queued.go"},
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
	// Combined with the complete-symbol certification, this fixture's two Go workers each gain a
	// "complete-symbol" signal. The digests were checked against the standalone ones by editing the
	// result JSON this test printed:
	//   drop both SymbolNameLine and the two signals  -> e9733475f04142cf… (the pre-302 golden)
	//   drop only the two signals                     -> b3dec24fba4d36dd… (the standalone 302 golden)
	//   drop only SymbolNameLine                      -> 76f2907c82b6d8a2… (the complete-certification golden)
	// so the difference is exactly those two signals, with no content or ranking change.
	review302RequireSchemaOnlyGolden(t, response.Results,
		"9ab477eecf353bb1e847ea521bc4bacc9bf4039d8c4954af06ef541623d594e9",
		"76f2907c82b6d8a21d2b1936094e8e647dfeb1142132ec846cae5eee8d0b5d2e")
	review302RequireCompleteSignalsOnly(t, response.Results, 6, 2,
		[]string{"Worker0 src/worker0.go", "Worker1 src/worker1.go"},
		"b3dec24fba4d36dd28229fd43ed4a3c4fcc62bd5475f6caf99fa46b87e319aae",
		"e9733475f04142cfe8fd78b823651105cd55eac581d38430c1b92424aa4a2a5b")
}

// review302RequireCompleteSignalsOnly keeps a fixture's standalone goldens as executable
// evidence: the combined results differ from them only by one complete-symbol signal on each of
// the named rows ("SymbolName FilePath"). Removing exactly those signals must reproduce the
// standalone full golden, and also clearing SymbolNameLine must reproduce the golden from before
// that field existed.
func review302RequireCompleteSignalsOnly(t *testing.T, results []SearchResult, wantRows, wantPositive int,
	certifiedRows []string, standaloneGolden, preNameLineGolden string) {
	t.Helper()
	if len(results) != wantRows {
		t.Fatalf("fixture returned %d results, want %d", len(results), wantRows)
	}
	expected := map[string]bool{}
	for _, key := range certifiedRows {
		expected[key] = true
	}
	positive := 0
	certified := map[string]bool{}
	stripped := append([]SearchResult(nil), results...)
	for i, result := range results {
		if result.SymbolNameLine > 0 {
			positive++
		}
		count := 0
		signals := make([]string, 0, len(result.Signals))
		for _, signal := range result.Signals {
			if signal == "complete-symbol" {
				count++
				continue
			}
			signals = append(signals, signal)
		}
		if count == 0 {
			continue
		}
		key := result.SymbolName + " " + result.FilePath
		if count != 1 || !expected[key] {
			t.Fatalf("unexpected complete-symbol signal: %s carries %d", key, count)
		}
		certified[key] = true
		stripped[i].Signals = signals
	}
	if positive != wantPositive {
		t.Fatalf("fixture has %d positive name coordinates, want %d", positive, wantPositive)
	}
	if len(certified) != len(expected) {
		t.Fatalf("complete-symbol signals on %v, want exactly %v", certified, certifiedRows)
	}
	review302AssertResultDigest(t, "complete-symbol signals removed", stripped, standaloneGolden)
	for i := range stripped {
		stripped[i].SymbolNameLine = 0
	}
	review302AssertResultDigest(t, "complete-symbol signals and SymbolNameLine removed", stripped, preNameLineGolden)
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
