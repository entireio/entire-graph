package sem

import (
	"bytes"
	"reflect"
	"testing"
)

func TestLoadCompactSnapshotQueriesSymbolsAndRelations(t *testing.T) {
	records := compactFixtureRecords()
	data, _ := encodeCompactFixture(t, records)
	index, err := LoadCompactSnapshot(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if index.CanonicalSemanticHash != hashRecords(t, records) {
		t.Fatalf("hash was not retained")
	}
	if got := index.Query(CompactSnapshotQuery{Symbol: "symbol-id"}).Symbols; len(got) != 1 || got[0].ID != "symbol-id" {
		t.Fatalf("exact id lookup = %#v", got)
	}
	if got := index.Query(CompactSnapshotQuery{Symbol: "same"}).Symbols; len(got) != 2 || got[0].ID != "symbol-id" || got[1].ID != "symbol-id-2" {
		t.Fatalf("name lookup = %#v", got)
	}
	if got := index.Query(CompactSnapshotQuery{Symbol: "pkg.same"}).Symbols; len(got) != 1 || got[0].ID != "symbol-id" {
		t.Fatalf("qualified-name lookup = %#v", got)
	}
	if got := index.Query(CompactSnapshotQuery{Symbol: "sym"}).Symbols; len(got) != 0 {
		t.Fatalf("partial lookup = %#v", got)
	}
	got := index.Query(CompactSnapshotQuery{FromID: "symbol-id", Relation: "CALLS"}).Relations
	if len(got) != 1 || got[0].Type != "CALLS" {
		t.Fatalf("relation lookup = %#v", got)
	}
	got = index.Query(CompactSnapshotQuery{FromID: "symbol-id"}).Relations
	if len(got) != 2 || got[0].Type != "CALLS" || got[1].Type != "IMPORTS" {
		t.Fatalf("relation sort = %#v", got)
	}
}

func TestCompactSnapshotQueryResultsAreDeterministic(t *testing.T) {
	data, _ := encodeCompactFixture(t, compactFixtureRecords())
	index, err := LoadCompactSnapshot(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	query := CompactSnapshotQuery{Symbol: "same", FromID: "symbol-id"}
	first, second := index.Query(query), index.Query(query)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("query order is unstable: %#v != %#v", first, second)
	}
}

func TestCompactSnapshotQueryDoesNotDuplicateOneMatchingSymbol(t *testing.T) {
	records := compactFixtureRecords()
	records[3] = SymbolRecord{RecordType: "symbol", ID: "same", StableIDVersion: "v1", Kind: "function", Name: "same", QualifiedName: "pkg.same", FilePath: "main.go", Language: "Go"}
	data, _ := encodeCompactFixture(t, records)
	index, err := LoadCompactSnapshot(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := index.Query(CompactSnapshotQuery{Symbol: "same"}).Symbols; len(got) != 2 {
		t.Fatalf("matching symbol was duplicated: %#v", got)
	}
}
