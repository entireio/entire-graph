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

// Fast-profile snapshots precompute imports from RAW content for languages
// whose per-symbol pass they skip. A component must never be one: its raw
// content holds template markup the import scanner would read.
func TestVueImportsNeverComeFromRawContent(t *testing.T) {
	if skipFastProfilePerSymbolScan(resolveProfile(ProfileFast), "Vue") {
		t.Fatal("the fast profile skips Vue's per-symbol pass, so its imports would be scanned from raw template content")
	}
}

func TestVueMacroTypeText(t *testing.T) {
	for _, testCase := range []struct{ name, script, want string }{
		{"props interface", "const props = defineProps<Props>()", "Props"},
		{"with defaults and nested generics", "withDefaults(defineProps<{ task: Task; tags?: Array<Label> }>(), { tags: () => [] })", "{ task: Task; tags?: Array<Label> }"},
		{"emits with function types", "const emit = defineEmits<{ (e: 'move', payload: MovePayload): void; (e: 'run', fn: () => void): void }>()", "{ (e: 'move', payload: MovePayload): void; (e: 'run', fn: () => void): void }"},
		{"several macros", "defineProps<Props>()\nconst model = defineModel<Filter>()", "Props, Filter"},
		{"no macros", "const count = ref<number>(0)", ""},
		{"runtime props declare no types", "defineProps(['task'])", ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := vueMacroTypeText(testCase.script); got != testCase.want {
				t.Fatalf("got %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestVueAliasSuffixTarget(t *testing.T) {
	known := map[string]bool{
		"src/mixins/paging.js":                true,
		"src/components/base/BaseList.vue":    true,
		"src/util/format.ts":                  true,
		"packages/admin/src/util/format.ts":   true,
		"src/components/forms/index.js":       true,
		"src/components/forms/FieldGroup.vue": true,
		"src/core/index.ts":                   true,
	}
	for _, testCase := range []struct{ spec, want string }{
		{"@/mixins/paging", "src/mixins/paging.js"},
		{"~/components/base/BaseList.vue", "src/components/base/BaseList.vue"},
		{"@/components/forms", "src/components/forms/index.js"},
		{"@/util/format", ""},   // two files end in util/format.ts
		{"./mixins/paging", ""}, // not an alias
		{"@/missing/thing", ""},
		{"@vueuse/core", ""}, // a scoped package, not an alias, though src/core/index.ts exists
		{"~@/mixins/paging", "src/mixins/paging.js"},
	} {
		got, ok := vueAliasSuffixTarget(testCase.spec, known)
		if got != testCase.want || ok != (testCase.want != "") {
			t.Errorf("%s: got (%q, %v), want %q", testCase.spec, got, ok, testCase.want)
		}
	}
}

func TestVueChannelEvents(t *testing.T) {
	script := `
this.$emit('reply-sent', reply)
EventBus.$emit('ticket:assigned', id)
emit('task-moved', payload)
bus.emit('refresh')
EventBus.$on('ticket:assigned', this.onAssigned)
window.addEventListener('resize', onResize)
this.$store.dispatch('tickets/fetchTickets')
store.dispatch('auth/restoreSession')
submit('not-an-event')
`
	got := map[string]bool{}
	for _, event := range vueChannelEvents(script) {
		got[event.Relation+" "+event.Name] = true
	}
	for _, want := range []string{"EMITS reply-sent", "EMITS ticket:assigned", "EMITS task-moved", "EMITS refresh", "LISTENS_ON ticket:assigned", "LISTENS_ON resize"} {
		if !got[want] {
			t.Errorf("missing %s; got %v", want, got)
		}
	}
	for _, unwanted := range []string{"EMITS tickets/fetchTickets", "EMITS auth/restoreSession", "EMITS not-an-event"} {
		if got[unwanted] {
			t.Errorf("Vuex dispatch or a plain call read as an event: %s", unwanted)
		}
	}
}

func TestVuexStoreResolvesStoreStrings(t *testing.T) {
	module := "const mutations = {\n  SET_ITEMS(state, items) {\n    state.items = items\n  }\n}\n\nexport default {\n  namespaced: true,\n  actions: {\n    async load({ commit }) {\n      commit('SET_ITEMS', [])\n    }\n  },\n  getters: {\n    count(state) {\n      return state.items.length\n    }\n  }\n}\n"
	records := []SymbolRecord{
		{ID: "m:SET_ITEMS", Name: "SET_ITEMS", Kind: "method", StartLine: 2},
		{ID: "m:load", Name: "load", Kind: "method", StartLine: 10},
		{ID: "m:count", Name: "count", Kind: "method", StartLine: 15},
	}
	store := &vuexStore{members: map[string]map[string]SymbolRecord{}, namespaceOf: map[string]string{"store/items.js": "items"}}
	store.index("store/items.js", "items", module, records)

	for _, testCase := range []struct {
		kind, name, from, want string
	}{
		{"mutation", "SET_ITEMS", "store/items.js", "m:SET_ITEMS"}, // bare name inside its own namespaced module
		{"mutation", "SET_ITEMS", "src/App.vue", ""},               // the same bare name elsewhere names a root member
		{"action", "items/load", "src/App.vue", "m:load"},
		{"getter", "items/count", "src/App.vue", "m:count"},
		{"action", "items/count", "src/App.vue", ""}, // a getter is not an action
	} {
		member, ok := store.resolve(testCase.kind, testCase.name, testCase.from)
		if got := map[bool]string{true: member.ID}[ok]; got != testCase.want {
			t.Errorf("resolve(%s %s from %s) = %q, want %q", testCase.kind, testCase.name, testCase.from, got, testCase.want)
		}
	}

	bindings := vuexMapBindings("...mapActions('items', ['load']),\n...mapGetters({ total: 'items/count' })")
	if bindings["load"] != (vuexBinding{kind: "action", key: "items/load"}) || bindings["total"] != (vuexBinding{kind: "getter", key: "items/count"}) {
		t.Fatalf("bindings = %+v", bindings)
	}
	got := map[string]bool{}
	for _, call := range store.callsIn("src/App.vue", "this.load()\nreturn this.total + this.$store.getters['items/count']", bindings) {
		got[call.target.ID] = true
	}
	if !got["m:load"] || !got["m:count"] || len(got) != 2 {
		t.Fatalf("calls = %v", got)
	}
}

func TestVueScriptSources(t *testing.T) {
	source := "<template><div/></template>\n<script src=\"./TicketTimer.js\"></script>\n<style src='./timer.css'></style>\n"
	if got := vueScriptSources(source); len(got) != 1 || got[0] != "./TicketTimer.js" {
		t.Fatalf("sources = %v", got)
	}
	if got := vueScriptSources("<script setup>\nconst a = 1\n</script>\n"); len(got) != 0 {
		t.Fatalf("inline script reported a src: %v", got)
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

func TestJSLocalImportCandidatesReachExtensionlessComponents(t *testing.T) {
	candidates := jsLocalImportCandidates("src/views/Customers.vue", "../components/BaseList")
	found := false
	for _, candidate := range candidates {
		if candidate == "src/components/BaseList.vue" {
			found = true
		}
		if candidate == "src/components/BaseList.ts" && found {
			t.Fatal(".vue must be tried after the script extensions")
		}
	}
	if !found {
		t.Fatalf("candidates = %v", candidates)
	}
}

// The Vuex index is built only for a repository with components and a Vuex
// root: Redux's createStore must not switch the store scans on.
func TestBuildVuexStoreNeedsComponentsAndAVuexRoot(t *testing.T) {
	module := "const actions = {\n  load() {}\n}\n\nexport default { namespaced: true, actions }\n"
	records := map[string][]SymbolRecord{"src/store/modules/items.js": {{ID: "m:load", Name: "load", Kind: "method", StartLine: 2}}}
	known := map[string]bool{"src/store/modules/items.js": true, "src/store/index.js": true, "src/App.vue": true}
	build := func(root string, files []FileRecord) *vuexStore {
		contents := map[string]string{"src/store/index.js": root, "src/store/modules/items.js": module}
		read := func(path string) (string, bool) {
			content, ok := contents[path]
			return content, ok
		}
		return buildVuexStore(files, records, read, known, manifestImportResolver{}, nil)
	}
	withComponent := []FileRecord{{Path: "src/App.vue", Language: "Vue"}, {Path: "src/store/index.js", Language: "JavaScript"}, {Path: "src/store/modules/items.js", Language: "JavaScript"}}
	withoutComponent := withComponent[1:]
	vuexCreate := "import { createStore } from 'vuex'\nimport items from './modules/items'\nexport default createStore({ modules: { items } })\n"
	for _, testCase := range []struct {
		name  string
		root  string
		files []FileRecord
		want  bool
	}{
		{"vuex createStore", vuexCreate, withComponent, true},
		{"Vuex.Store", "import Vuex from 'vuex'\nimport items from './modules/items'\nexport default new Vuex.Store({ modules: { items } })\n", withComponent, true},
		{"redux createStore", "import { createStore } from 'redux'\nimport items from './modules/items'\nexport default createStore(reducer)\n", withComponent, false},
		{"no components", vuexCreate, withoutComponent, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := build(testCase.root, testCase.files)
			if (store != nil) != testCase.want {
				t.Fatalf("store built = %v, want %v", store != nil, testCase.want)
			}
			if store != nil {
				if _, ok := store.resolve("action", "items/load", "src/App.vue"); !ok {
					t.Fatal("items/load not indexed")
				}
			}
		})
	}
}
