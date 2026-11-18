package sem

import (
	"strings"
	"testing"
)

const vueTaskCardSample = `<template>
  <!-- <script>commentedOut()</script> -->
  <article id="card-root" :task-id="task.id">
    <template v-if="task.done"><span>done</span></template>
    <button @click="toggleDone">toggle</button>
  </article>
</template>

<script lang="ts">
export interface TaskCardProps { title: string }
</script>

<script setup lang="ts">
import { formatDue } from '@/utils/format'

function toggleDone(): void {
  function inner(): string {
    return formatDue()
  }
  inner()
}
</script>

<style scoped>
.card > span { color: red; }
</style>
`

// The view must keep every byte offset and line of the component: script
// bodies verbatim, everything else blank, newlines in place.
func TestVueScriptViewKeepsOffsetsAndOnlyScriptBodies(t *testing.T) {
	view, ok := vueScriptViewOf(vueTaskCardSample)
	if !ok {
		t.Fatal("expected a script view")
	}
	if view.dialect != "ts" {
		t.Fatalf("dialect = %q, want ts", view.dialect)
	}
	if len(view.content) != len(vueTaskCardSample) {
		t.Fatalf("view length %d, source length %d", len(view.content), len(vueTaskCardSample))
	}
	for i := range vueTaskCardSample {
		if (vueTaskCardSample[i] == '\n') != (view.content[i] == '\n') {
			t.Fatalf("newline moved at byte %d", i)
		}
	}
	for _, kept := range []string{"export interface TaskCardProps", "function toggleDone(): void {", "import { formatDue } from '@/utils/format'"} {
		at := strings.Index(vueTaskCardSample, kept)
		if view.content[at:at+len(kept)] != kept {
			t.Errorf("script text %q was not kept in place", kept)
		}
	}
	for _, blanked := range []string{"commentedOut", "card-root", "toggle</button>", "color: red", "<script", "</script>", "<template"} {
		if strings.Contains(view.content, blanked) {
			t.Errorf("non-script text %q survived in the view", blanked)
		}
	}
}

func TestVueScriptViewDialects(t *testing.T) {
	for _, testCase := range []struct {
		name, source, dialect string
		ok                    bool
	}{
		{"default js", "<script>\nexport default {}\n</script>\n", "js", true},
		{"ts", "<script lang=\"ts\">\nconst a = 1\n</script>\n", "ts", true},
		{"tsx", "<script setup lang='tsx'>\nconst a = 1\n</script>\n", "tsx", true},
		{"jsx", "<script lang=\"jsx\">\nconst a = 1\n</script>\n", "jsx", true},
		{"ts with plain js block", "<script>\nexport default { name: 'A' }\n</script>\n<script setup lang=\"ts\">\nconst a = 1\n</script>\n", "ts", true},
		{"jsx with ts block", "<script lang=\"jsx\">\nconst a = 1\n</script>\n<script setup lang=\"ts\">\nconst b = 2\n</script>\n", "tsx", true},
		{"unsupported lang", "<script lang=\"coffee\">\na = 1\n</script>\n", "", false},
		{"template only", "<template>\n  <div id=\"x\"></div>\n</template>\n", "", false},
		{"blank script", "<template><div/></template>\n<script setup>\n\n</script>\n", "", false},
		{"external script only", "<template><div/></template>\n<script src=\"./TicketTimer.js\"></script>\n", "", false},
		{"script inside template is not top level", "<template>\n  <div><script>evil()</script></div>\n</template>\n", "", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			view, ok := vueScriptViewOf(testCase.source)
			if ok != testCase.ok || view.dialect != testCase.dialect {
				t.Fatalf("got (%q, %v), want (%q, %v)", view.dialect, ok, testCase.dialect, testCase.ok)
			}
		})
	}
}

// Script declarations become symbols nested under the component, while the
// file-level component, document and element symbols keep their IDs and
// containers.
func TestVueComponentParsesScriptDeclarations(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("src/components/TaskCard.vue", vueTaskCardSample)
	if language != "Vue" {
		t.Fatalf("language = %q, want Vue", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %+v", status)
	}
	symbols := entitySymbols("local/app", "src/components/TaskCard.vue", language, entities)
	byID := map[string]SymbolRecord{}
	for _, symbol := range symbols {
		byID[symbol.ID] = symbol
	}
	const prefix = "local/app:Vue:src/components/TaskCard.vue:"
	component, document := prefix+"component:TaskCard", prefix+"document:TaskCard"
	want := map[string]string{ // symbol ID -> container ID
		component:                          "",
		document:                           "",
		prefix + "element:card-root":       document,
		prefix + "interface:TaskCardProps": component,
		prefix + "function:toggleDone":     component,
	}
	for id, container := range want {
		symbol, ok := byID[id]
		if !ok {
			t.Errorf("missing symbol %s; have %s", id, symbolIDs(symbols))
			continue
		}
		if symbol.ContainerID != container {
			t.Errorf("%s container = %q, want %q", id, symbol.ContainerID, container)
		}
	}
	if symbol := byID[prefix+"function:toggleDone"]; symbol.StartLine != 16 {
		t.Errorf("toggleDone starts on line %d, want 16", symbol.StartLine)
	}
	for _, symbol := range symbols {
		if symbol.Name == "inner" && symbol.ContainerID != prefix+"function:toggleDone" {
			t.Errorf("nested inner container = %q, want toggleDone", symbol.ContainerID)
		}
		if symbol.Name == "task" || symbol.Name == "id" {
			t.Errorf("phantom symbol from a template binding: %s", symbol.ID)
		}
	}
	// Parsing the scripts changes parser output for unchanged .vue trees.
	if IdentityRevision == "8" {
		t.Fatal("Vue script parsing must invalidate revision 8")
	}
}

// Without a parseable script the component keeps exactly the inventory
// extraction it always had.
func TestVueComponentWithoutScriptKeepsFileEntities(t *testing.T) {
	for _, source := range []string{
		"<template>\n  <div id=\"only\"></div>\n</template>\n",
		"<template>\n  <div id=\"only\"></div>\n</template>\n<script lang=\"coffee\">\nx = 1\n</script>\n",
	} {
		entities, language, status := TreeSitterParser{}.ParseWithStatus("Only.vue", source)
		if language != "Vue" || status.ParseError {
			t.Fatalf("language %q status %+v", language, status)
		}
		got := map[string]bool{}
		for _, entity := range entities {
			got[entity.Kind+":"+entity.Name] = true
		}
		if len(entities) != 3 || !got["component:Only"] || !got["document:Only"] || !got["element:only"] {
			t.Fatalf("entities = %+v", entities)
		}
	}
}

// A script syntax error is reported against the component's own line numbers.
func TestVueScriptParseErrorUsesFileLines(t *testing.T) {
	source := "<template>\n  <div></div>\n</template>\n\n<script setup lang=\"ts\">\nconst ok = 1\nfunction broken( {\n</script>\n"
	_, language, status := TreeSitterParser{}.ParseWithStatus("Broken.vue", source)
	if language != "Vue" || !status.ParseError || status.Code != "E_PARSE_ERROR" {
		t.Fatalf("language %q status %+v", language, status)
	}
	if !strings.Contains(status.Detail, "line 7") && !strings.Contains(status.Detail, "line 8") {
		t.Fatalf("detail %q does not point at the script's file line", status.Detail)
	}
}

// Characters whose lowercase form has a different byte length must not move
// script boundaries: the block scan indexes a lowercase copy with offsets into
// the source.
func TestVueScriptViewWithLengthChangingCharacters(t *testing.T) {
	for _, prefix := range []string{"İstanbul İzmir ", "KKK ", "\xff\xfe broken "} {
		source := "<template>\n  <p>" + strings.Repeat(prefix, 40) + "</p>\n</template>\n\n<script setup lang=\"ts\">\nfunction keep(): number {\n  return 1\n}\n</script>\n<style>\n.x { color: red; }\n</style>\n"
		view, ok := vueScriptViewOf(source)
		if !ok || len(view.content) != len(source) {
			t.Fatalf("prefix %q: ok=%v, view length %d, source length %d", prefix, ok, len(view.content), len(source))
		}
		body := "\nfunction keep(): number {\n  return 1\n}\n"
		at := strings.Index(source, body)
		if view.content[at:at+len(body)] != body || strings.Contains(view.content, "script") {
			t.Fatalf("prefix %q: script body not kept exactly:\n%q", prefix, view.content)
		}
	}
	// An unterminated template full of shrinking characters must not slice past
	// the end of the lowercase copy.
	if _, ok := vueScriptViewOf("<template>\n" + strings.Repeat("İK", 200)); ok {
		t.Fatal("a component with no script must report no view")
	}
	if IdentityRevision == "8" {
		t.Fatal("the byte-length correction changes parser output and must invalidate revision 8")
	}
}
