package sem

import (
	"reflect"
	"testing"
)

// An element symbol comes only from a literal id attribute. Attributes whose
// name merely ends in "id" (data-id, :task-id, aria-style ids) and Vue
// bindings (:id, v-bind:id) used to mint elements named after their values.
func TestHTMLEntitiesOnlyLiteralIDAttributes(t *testing.T) {
	content := `<template>
  <div id="board-root" :task-id="selectedTaskId" data-id="card">
    <input :id="inputId" v-bind:id="rowDomId" data-testid="ticket-row">
    <span
id="first-on-line" :data-column-id="column.id"></span>
  </div>
</template>
`
	var got []string
	for _, entity := range htmlEntities("BoardColumn.vue", content, nil) {
		if entity.Kind == "element" {
			got = append(got, entity.Name)
		}
	}
	want := []string{"board-root", "first-on-line"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("element symbols = %v, want %v", got, want)
	}
	// Dropping the phantom elements changes parser output for unchanged trees,
	// and the record and search caches key on the tree, so only the revision
	// bump stops revision-8 records from serving the phantoms back.
	if IdentityRevision == "8" {
		t.Fatal("the literal-id correction must invalidate revision 8")
	}
}
