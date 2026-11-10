package sem

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// resortedHistoryReference is the refresh a cache hit performed before the
// splice: drop every FILE_CHANGES_WITH record, append the history and re-sort
// everything by the concatenated key. The splice must return exactly this.
func resortedHistoryReference(relations, history []RelationRecord) []RelationRecord {
	out := []RelationRecord{}
	for _, relation := range relations {
		if relation.Type != "FILE_CHANGES_WITH" {
			out = append(out, relation)
		}
	}
	out = append(out, history...)
	sort.Slice(out, func(i, j int) bool {
		return out[i].Type+out[i].FromID+out[i].ToID < out[j].Type+out[j].FromID+out[j].ToID
	})
	return out
}

func spliceTestRelation(kind, from, to, reason string) RelationRecord {
	return RelationRecord{RecordType: "relation", Type: kind, FromID: from, ToID: to, Reason: reason, WarningCodes: []string{}}
}

// spliceTestSnapshotRelations is a snapshot-ordered relation list around an
// optional FILE_CHANGES_WITH block.
func spliceTestSnapshotRelations(block ...RelationRecord) []RelationRecord {
	relations := append([]RelationRecord{
		spliceTestRelation("CONTAINS", "k:file:a/Foo", "k:go:a/Foo:function:Run", ""),
		spliceTestRelation("DEFINES", "k:file:a/Foo", "k:go:a/Foo:function:Run", ""),
		spliceTestRelation("DEFINES", "k:file:a/FooBar", "k:go:a/FooBar:function:Run", ""),
		spliceTestRelation("IMPORTS", "k:file:a/FooBar", "external:module:strings", ""),
		spliceTestRelation("SIMILAR_TO", "k:go:a/Foo:function:Run", "k:go:a/FooBar:function:Run", ""),
	}, block...)
	return resortedHistoryReference(relations, block)
}

func TestSpliceFileChangesWithRelationsMatchesFullResort(t *testing.T) {
	// The prefix pair orders differently by concatenation than by tuple:
	// "…a/Foo" + "k:file:z" puts 'k' where "…a/FooBar" + … puts 'B', so FooBar
	// sorts first, while a (FromID, ToID) comparison would put Foo first.
	foo := spliceTestRelation("FILE_CHANGES_WITH", "k:file:a/Foo", "k:file:z", "files changed together in 2 recent commits")
	fooBar := spliceTestRelation("FILE_CHANGES_WITH", "k:file:a/FooBar", "k:file:b", "files changed together in 2 recent commits")
	stale := spliceTestRelation("FILE_CHANGES_WITH", "k:file:a/Foo", "k:file:old", "files changed together in 2 recent commits")
	recounted := foo
	recounted.Reason = "files changed together in 3 recent commits"
	cases := []struct {
		name      string
		relations []RelationRecord
		history   []RelationRecord
		block     []RelationRecord // the expected FILE_CHANGES_WITH records, in order
	}{
		{"history replaced", spliceTestSnapshotRelations(stale), []RelationRecord{foo, fooBar}, []RelationRecord{fooBar, foo}},
		{"same keys, new counts", spliceTestSnapshotRelations(fooBar, foo), []RelationRecord{recounted, fooBar}, []RelationRecord{fooBar, recounted}},
		{"no block yet", spliceTestSnapshotRelations(), []RelationRecord{foo, fooBar}, []RelationRecord{fooBar, foo}},
		{"history emptied", spliceTestSnapshotRelations(fooBar, foo), nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := spliceFileChangesWithRelations(tc.relations, tc.history)
			if want := resortedHistoryReference(tc.relations, tc.history); !reflect.DeepEqual(got, want) {
				t.Fatalf("splice differs from the full re-sort:\n got %#v\nwant %#v", got, want)
			}
			var block []RelationRecord
			for _, relation := range got {
				if relation.Type == "FILE_CHANGES_WITH" {
					block = append(block, relation)
				}
			}
			if !reflect.DeepEqual(block, tc.block) {
				t.Fatalf("history block = %#v, want %#v", block, tc.block)
			}
		})
	}
}

func TestSpliceFileChangesWithRelationsFallsBackOutsideTheBlockShape(t *testing.T) {
	history := []RelationRecord{
		spliceTestRelation("FILE_CHANGES_WITH", "k:file:a/FooBar", "k:file:b", "new"),
		spliceTestRelation("FILE_CHANGES_WITH", "k:file:a/Foo", "k:file:z", "new"),
	}
	// A type that is a prefix of FILE_CHANGES_WITH can sort inside the block:
	// "FILE" + "_CHANGES_WITHk:file:a/FooC" lands between the two records above,
	// while its type alone sorts before every one of them.
	prefixType := append(spliceTestSnapshotRelations(), spliceTestRelation("FILE", "_CHANGES_WITHk:file:a/FooC", "k:file:q", ""))
	prefixType = resortedHistoryReference(prefixType, nil)
	// A block that is not contiguous cannot be replaced in place.
	split := spliceTestSnapshotRelations()
	split = append([]RelationRecord{history[1]}, split...)
	split = append(split, history[0])
	for name, relations := range map[string][]RelationRecord{"prefix type": prefixType, "split block": split} {
		t.Run(name, func(t *testing.T) {
			fresh := []RelationRecord{history[1], history[0]}
			fresh[0].Reason, fresh[1].Reason = "refreshed", "refreshed"
			if got, want := spliceFileChangesWithRelations(relations, fresh), resortedHistoryReference(relations, fresh); !reflect.DeepEqual(got, want) {
				t.Fatalf("splice differs from the full re-sort:\n got %#v\nwant %#v", got, want)
			}
		})
	}
}

func TestSpliceFileChangesWithRelationsSkipsUnchangedHistory(t *testing.T) {
	foo := spliceTestRelation("FILE_CHANGES_WITH", "k:file:a/Foo", "k:file:z", "files changed together in 2 recent commits")
	fooBar := spliceTestRelation("FILE_CHANGES_WITH", "k:file:a/FooBar", "k:file:b", "files changed together in 2 recent commits")
	relations := spliceTestSnapshotRelations(foo, fooBar)
	// git log reports pairs by count, not in snapshot order.
	history := []RelationRecord{foo, fooBar}
	got := spliceFileChangesWithRelations(relations, history)
	if !reflect.DeepEqual(got, resortedHistoryReference(relations, history)) {
		t.Fatal("unchanged history changed the relations")
	}
	if len(got) != len(relations) || &got[0] != &relations[0] {
		t.Fatal("unchanged history rebuilt the relation slice instead of reusing it")
	}
}

// TestSearchCacheHistorySpliceMatchesColdBuild serves the same tree twice from
// one entry, first with the history it was built with and then after history
// moved, and requires each hit to equal a cold build relation for relation.
func TestSearchCacheHistorySpliceMatchesColdBuild(t *testing.T) {
	repo := t.TempDir()
	sharedCacheFixture(t, repo)
	touchTogether := func(paths ...string) {
		t.Helper()
		originals := make(map[string][]byte)
		for _, path := range paths {
			content, err := os.ReadFile(filepath.Join(repo, path))
			if err != nil {
				t.Fatal(err)
			}
			originals[path] = content
			write(t, repo, path, string(content)+"\n// history change\n")
		}
		git(t, repo, "add", ".")
		git(t, repo, "commit", "-q", "-m", "history change")
		for _, path := range paths {
			write(t, repo, path, string(originals[path]))
		}
		git(t, repo, "add", ".")
		git(t, repo, "commit", "-q", "-m", "restore tree")
	}
	touchTogether("ledger/audit.go", "web/format.ts")
	cache := t.TempDir()
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	if _, hit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", options, cache); err != nil || hit {
		t.Fatalf("warm: %v %v", hit, err)
	}
	historyCount := func(s ProviderSnapshot) int {
		count := 0
		for _, relation := range s.Relations {
			if relation.Type == "FILE_CHANGES_WITH" {
				count++
			}
		}
		return count
	}
	previous := -1
	for _, step := range []string{"unchanged history", "moved history"} {
		if step == "moved history" {
			touchTogether("ledger/audit.go", "ledger/ledger.go", "web/client.ts")
		}
		served, hit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", options, cache)
		if err != nil || !hit {
			t.Fatalf("%s: hit=%v err=%v", step, hit, err)
		}
		cold, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", options)
		if err != nil {
			t.Fatal(err)
		}
		if historyCount(cold) == 0 || historyCount(cold) == previous {
			t.Fatalf("%s: fixture history did not move (%d records)", step, historyCount(cold))
		}
		previous = historyCount(cold)
		if !reflect.DeepEqual(served.Relations, cold.Relations) {
			t.Fatalf("%s: served relations differ from a cold build", step)
		}
		if served.Header.Stats.Relations != cold.Header.Stats.Relations || !reflect.DeepEqual(served.Header.Completeness.Relations, cold.Header.Completeness.Relations) {
			t.Fatalf("%s: served counts differ from a cold build", step)
		}
	}
}
