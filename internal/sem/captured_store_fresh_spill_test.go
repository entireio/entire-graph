package sem

import "testing"

func TestCapturedStoreFreshSpillPreservesBytesAndLaterIntegrity(t *testing.T) {
	live, reads := "original", 0
	store := newCapturedStore(t.Context(), func(string) (string, bool) {
		reads++
		return live, true
	}, 0)
	defer store.close()
	first, ok, err := store.acquire("fixture")
	if err != nil || !ok || first.content != live {
		t.Fatalf("first capture: %+v, %v, %v", first, ok, err)
	}
	entry := store.entries["fixture"]
	if store.memory != 0 || entry.source.content != "" || entry.backing == "" {
		t.Fatal("fresh consumer caused spilled bytes to remain in the store")
	}
	live = "modified"
	second, ok, err := store.acquire("fixture")
	if err != nil || !ok || second != first || reads != 1 {
		t.Fatalf("later consumer did not use captured backing: %+v, %v, %v, reads=%d", second, ok, err, reads)
	}
	if err := store.root.WriteFile(entry.backing, []byte(live), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.acquire("fixture"); err == nil || ok {
		t.Fatal("later consumer accepted equal-length backing corruption")
	}
}
