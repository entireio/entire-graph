# Vue Corpus

Two realistic Vue applications with a hand-written ground truth. Each app has an
`expected.json` that lists the symbols and relations a correct graph contains,
and tags every fact with the gap it measures. `vue_corpus_test.go` builds a
snapshot of each app, scores it against `expected.json`, and ratchets the
result in `score.json`, so every Vue improvement shows up as facts gained per
tag and every regression fails the test.

```sh
go test ./internal/sem -run 'TestVueCorpus' -v            # score and compare
go test ./internal/sem -run 'TestVueCorpusScore' -update  # record a new score
VUE_CORPUS_VERBOSE=1 go test ./internal/sem -run 'TestVueCorpusScore' -v  # list every missing fact
```

| App | Dialect |
|---|---|
| `taskboard/` | Vue 3, TypeScript, `<script setup lang="ts">`, Pinia, vue-router, composables, `@/` path alias |
| `helpdesk/` | Options API, JavaScript, Vuex modules, mixins, event bus, global registration, kebab-case tags |

The apps are never built or run. They contain no `node_modules` or lockfiles,
and every relative and aliased import resolves to a file in the app.

## Manifest Format

```json
{
  "app": "taskboard",
  "symbols": [
    {"file": "src/components/TaskCard.vue", "name": "toggleDone", "kind": "function", "line": 31, "tag": "script-symbols"}
  ],
  "absent_symbols": [
    {"file": "src/App.vue", "name": "selectedId", "kind": "element", "tag": "false-elements", "why": ":task-id=\"selectedId\" is a prop binding, not an HTML id"}
  ],
  "relations": [
    {"type": "IMPORTS", "from": {"file": "src/App.vue"}, "to": {"file": "src/stores/tasks.ts"}, "tag": "script-imports"},
    {"type": "CALLS", "from": {"file": "src/components/TaskCard.vue", "name": "toggleDone"}, "to": {"file": "src/stores/tasks.ts", "name": "toggleTask"}, "tag": "store-members"},
    {"type": ["CALLS", "ACCESSES"], "from": {"file": "src/views/BoardView.vue", "name": "BoardView", "kind": "component"}, "to": {"file": "src/components/TaskCard.vue", "name": "TaskCard", "kind": "component"}, "tag": "template-edges"}
  ],
  "absent_relations": [
    {"type": "HANDLES_ROUTE", "from": {"file": "src/api/agents.js", "name": "fetchAgents"}, "tag": "false-relations", "why": "get('/agents') is a client call, not a route handler"}
  ]
}
```

- `symbols[].line` is the line that declares the name. A symbol matches when a
  snapshot symbol in that file has the same name, the same kind if `kind` is
  given, and a range that contains `line`.
- `absent_symbols` lists symbols a correct graph must not emit. The name must
  appear in the file, so the trap is real. HTML-id traps set `"kind": "element"`,
  because many trap values (`inputId`, `selectedId`) are also real script
  symbols that a correct graph does emit.
- `absent_relations` lists edges a correct graph must not emit. Without `to`,
  it forbids every edge of that type from the `from` endpoint.
- A relation endpoint is a file (`file` only), a symbol (`file` and `name`,
  optionally `kind`), or an external package (`external`, such as `"pinia"`).
  `type` is one relation type or a list of acceptable types.
- Every named endpoint also appears in `symbols`, so the manifest checks itself.
- An `emit('x')` call is attributed to the function that contains it. An emit
  written inline in the template (`@click="$emit('x')"`) is attributed to the
  component.
- Each fact carries the single most specific tag: the capability a correct
  provider needs before it can emit that fact.

## Tags

Each tag maps to a gap in the Vue gap analysis (G1 to G8) or to a construct the
analysis did not cover yet.

| Tag | Gap | What it measures |
|---|---|---|
| `baseline` | | Facts the provider emits today: plain `.ts`/`.js` code, `.vue` component symbols, static HTML ids. The test fails if any is missing. |
| `script-symbols` | G2 | Functions, consts, and classes declared in `<script>` or `<script setup>` |
| `script-imports` | G3 | IMPORTS from a `.vue` file to `.ts`, `.js`, or `.vue` files |
| `script-calls` | G3 | CALLS from a script function to an imported function |
| `component-owner` | G4 | CONTAINS from the component to its script symbols, and top-level setup calls attributed to the component |
| `false-elements` | G5 | HTML-id false positives that must not appear |
| `template-edges` | G6 | Template event handlers to script functions, and component tags to the component they render |
| `options-api` | G7 | `methods`, `computed`, `watch`, and lifecycle hooks as symbols |
| `store-members` | G8 | Calls through store instances, destructured members, Pinia actions, and Vuex `dispatch`/`commit` strings |
| `path-alias` | new | `@/` imports resolved through the `tsconfig.json` or bundler alias |
| `lazy-imports` | new | `() => import('./X.vue')` routes and `defineAsyncComponent` |
| `global-registration` | new | `app.component` / `Vue.component` registrations used as tags elsewhere |
| `ts-types` | new | Types from `.ts` files used in `.vue` files (`defineProps<Props>()`, typed refs, typed params) |
| `mixins` | new | Components that use a mixin, and calls to mixin members through `this` |
| `event-bus` | new | EMITS and LISTENS_ON for event-bus channels and component custom events (`emit('x')` with `@x=` in the parent) |
| `tests` | new | TESTS from spec files to the composables, stores, and components they cover |
| `script-src` | new | `<script src="./x.js">` external script blocks |
| `js-resolution` | | Plain JavaScript and TypeScript resolution gaps that Vue apps hit: multi-line and aliased imports, a call to an imported function with the caller's own name, and calls between sibling functions nested in a composable or setup store |
| `false-relations` | | Edges the provider emits today that a correct graph must not (see `absent_relations`) |

## Provider Findings

Building the corpus surfaced these behaviors in today's provider. Each one that
fits a fact is scored; the rest are recorded here so they are not lost.

- `.vue` script blocks are not parsed, so every tag except `baseline` and
  `lazy-imports` starts near zero.
- HTML-id extraction matches any attribute ending in `id=`, so `:task-id`,
  `data-id`, and `:id` bindings become elements. Only the last dotted segment
  survives, so `:data-task-id="task.id"` becomes an element named `id`.
  `data-testid=` is not matched.
- `@/` imports resolve between `.ts` files through `tsconfig.json`, but not
  through `jsconfig.json`, and a `.ts` import of `@/....vue` becomes an external.
  A call to `useAuthStore()` imported through `@/` resolves to
  `external:symbol:@/stores/auth.useAuthStore` instead of the store.
- Object-literal methods in plain `.js` files become `method` symbols (Vuex
  getters, mutations, actions, mixin members, Pinia options actions). Arrow
  function getters do not. `this.x()` resolves inside a named
  `export const mixin = {...}` but not inside an anonymous `export default {...}`.
- Every Vuex getter and mutation gets a false CALLS edge to the module's
  `state()` function, because its `state` parameter is matched by name.
- `store.dispatch('ns/action')` becomes an EMITS edge to a channel, while
  `$emit` and `$on` are never matched as events.
- Client helpers that call `get('/path')` or `post('/path')` on a local HTTP
  wrapper get false HANDLES_ROUTE edges.
- A lazy `import()` that carries a `/* webpackChunkName */` comment is dropped.
- Calls inside vitest or jest `it(...)` blocks are attributed to the spec file,
  and no TESTS edges are emitted.
