package sem

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Vue single-file components (.vue) are parsed through their script blocks.
// The <script> and <script setup> bodies are ordinary JavaScript or TypeScript,
// so the file is parsed under that dialect's grammar, exactly like a .js/.ts
// file, but against a view in which every byte outside those bodies (the
// template, styles and custom blocks) is blanked to a space. Newlines are kept,
// so every line, column and byte offset in the view is the same as in the file
// and no position needs remapping. The language label stays "Vue", mirroring
// how Flow JavaScript keeps its "JavaScript" label under the TSX grammar.
//
// The whole-file component, document and HTML-id element symbols that
// componentEntities has always produced are kept unchanged, so their
// compound-v1 IDs do not move; the script declarations are added beside them
// and nest under the component.

// vueScriptView is a .vue file reduced to its script bodies.
type vueScriptView struct {
	// content has the source's exact length; only script bodies survive.
	content string
	// dialect is "js", "jsx", "ts" or "tsx".
	dialect string
}

// vueScriptViewOf returns the script view of a single-file component. It
// reports false when the file has no non-blank script body, or when a script
// declares a lang the provider does not parse (coffee, livescript), in which
// case the caller keeps the inventory-only extraction.
func vueScriptViewOf(content string) (vueScriptView, bool) {
	blocks := vueBlocks(content, "script")
	dialect := ""
	hasBody := false
	for _, block := range blocks {
		lang := "js"
		if match := vueLangAttrRe.FindStringSubmatch(block.attrs); match != nil {
			lang = strings.ToLower(match[1])
		}
		switch lang {
		case "js", "javascript", "jsx", "ts", "typescript", "tsx":
		default:
			return vueScriptView{}, false
		}
		dialect = vueMergeDialect(dialect, lang)
		if strings.TrimSpace(content[block.bodyStart:block.bodyEnd]) != "" {
			hasBody = true
		}
	}
	if !hasBody {
		return vueScriptView{}, false
	}
	masked := []byte(content)
	keep := make([]bool, len(masked))
	for _, block := range blocks {
		for i := block.bodyStart; i < block.bodyEnd; i++ {
			keep[i] = true
		}
	}
	for i, ch := range masked {
		if !keep[i] && ch != '\n' && ch != '\r' {
			masked[i] = ' '
		}
	}
	return vueScriptView{content: string(masked), dialect: dialect}, true
}

var vueLangAttrRe = regexp.MustCompile(`(?i)(?:^|\s)lang\s*=\s*["']?([A-Za-z]+)`)

// vueMergeDialect combines the dialects of a file's script blocks. A component
// may pair <script lang="ts"> with <script setup lang="ts">; mixed blocks parse
// under the grammar that accepts both, TSX being the superset.
func vueMergeDialect(current, lang string) string {
	switch lang {
	case "javascript":
		lang = "js"
	case "typescript":
		lang = "ts"
	}
	if current == "" || current == lang {
		return lang
	}
	typed := current == "ts" || current == "tsx" || lang == "ts" || lang == "tsx"
	jsx := current == "tsx" || current == "jsx" || lang == "tsx" || lang == "jsx"
	switch {
	case typed && jsx:
		return "tsx"
	case typed:
		return "ts"
	case jsx:
		return "jsx"
	}
	return "js"
}

var vueSrcAttrRe = regexp.MustCompile(`(?i)(?:^|\s)src\s*=\s*["']([^"']+)["']`)

// vueScriptSources returns the src of each top-level <script src="..."> block:
// a component whose script lives in another file.
func vueScriptSources(content string) []string {
	var sources []string
	for _, block := range vueBlocks(content, "script") {
		if match := vueSrcAttrRe.FindStringSubmatch(block.attrs); match != nil {
			sources = append(sources, match[1])
		}
	}
	return sources
}

// languageSpec returns the grammar spec for the view's dialect and whether its
// TypeScript must be masked as TSX.
func (view vueScriptView) languageSpec() (languageSpec, bool) {
	switch view.dialect {
	case "ts":
		return treeSitterLanguages[".ts"], false
	case "tsx":
		return treeSitterLanguages[".tsx"], true
	case "jsx":
		return treeSitterLanguages[".jsx"], false
	}
	return treeSitterLanguages[".js"], false
}

type vueBlock struct {
	attrs              string
	bodyStart, bodyEnd int
}

// VueTemplateBody returns the body of the top-level template. Its byte range
// excludes neighboring scripts even when the blocks share a single line.
func VueTemplateBody(content string) string {
	blocks := vueBlocks(content, "template")
	if len(blocks) == 0 {
		return ""
	}
	return content[blocks[0].bodyStart:blocks[0].bodyEnd]
}

// vueBlocks walks the outer blocks of a Vue SFC, skipping comments and
// preserving the attributes of self-closing blocks such as <script src="…"/>.
func vueBlocks(content, wanted string) []vueBlock {
	var blocks []vueBlock
	lower := asciiLower(content)
	for i := 0; i < len(content); {
		lt := strings.IndexByte(content[i:], '<')
		if lt < 0 {
			break
		}
		i += lt
		if strings.HasPrefix(content[i:], "<!--") {
			i = vueCommentEnd(content, i)
			continue
		}
		name, attrs, tagEnd, selfClosing, ok := vueOpenTag(content, i)
		if !ok {
			i++
			continue
		}
		bodyEnd, next := tagEnd, tagEnd
		if !selfClosing {
			bodyEnd, next = vueBlockEnd(content, lower, name, tagEnd)
		}
		if name == wanted {
			blocks = append(blocks, vueBlock{attrs: attrs, bodyStart: tagEnd, bodyEnd: bodyEnd})
		}
		i = next
	}
	return blocks
}

func vueCommentEnd(content string, start int) int {
	if end := strings.Index(content[start+4:], "-->"); end >= 0 {
		return start + 4 + end + 3
	}
	return len(content)
}

// vueOpenTag parses the opening tag at content[at] ('<'). It returns the
// lowercased tag name, the raw attribute text, the index just past '>', and
// whether the tag closes itself. Quoted attribute values may contain '>'.
func vueOpenTag(content string, at int) (name, attrs string, end int, selfClosing, ok bool) {
	i := at + 1
	start := i
	for i < len(content) && (isASCIILetter(content[i]) || (i > start && (isASCIIDigit(content[i]) || content[i] == '-'))) {
		i++
	}
	if i == start {
		return "", "", 0, false, false
	}
	name = strings.ToLower(content[start:i])
	attrStart := i
	var quote byte
	for ; i < len(content); i++ {
		ch := content[i]
		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
			}
		case ch == '"' || ch == '\'':
			quote = ch
		case ch == '>':
			attrs = content[attrStart:i]
			return name, attrs, i + 1, strings.HasSuffix(strings.TrimSpace(attrs), "/"), true
		}
	}
	return "", "", 0, false, false
}

// vueBlockEnd returns the start and end of the closing tag for a top-level
// block named name whose opening tag ends at from. Same-name tags nest (a
// <template> holds <template v-slot> children); raw-text blocks like <style>
// have no nested tags of their own name.
func vueBlockEnd(content, lower, name string, from int) (int, int) {
	depth := 1
	open, closing := "<"+name, "</"+name
	for i := from; i < len(content); {
		next := strings.IndexByte(content[i:], '<')
		if next < 0 {
			return len(content), len(content)
		}
		i += next
		switch {
		case name == "template" && strings.HasPrefix(content[i:], "<!--"):
			i = vueCommentEnd(content, i)
		case strings.HasPrefix(lower[i:], closing) && vueTagNameEnds(lower, i+len(closing)):
			depth--
			gt := strings.IndexByte(content[i:], '>')
			if gt < 0 {
				return len(content), len(content)
			}
			if depth == 0 {
				return i, i + gt + 1
			}
			i += gt + 1
		case name != "script" && name != "style" && strings.HasPrefix(lower[i:], open) && vueTagNameEnds(lower, i+len(open)):
			_, _, end, selfClosing, ok := vueOpenTag(content, i)
			if !ok {
				i++
				continue
			}
			if !selfClosing {
				depth++
			}
			i = end
		default:
			i++
		}
	}
	return len(content), len(content)
}

// asciiLower lowercases ASCII letters only, so the result keeps the input's
// byte length and every offset into it is an offset into the input. Tag names
// are ASCII, and strings.ToLower is not length-preserving: `İ`, the Kelvin
// sign and invalid UTF-8 bytes change size, which shifted block boundaries
// and could slice past the end of the copy.
func asciiLower(s string) string {
	lower := []byte(s)
	for i, ch := range lower {
		if ch >= 'A' && ch <= 'Z' {
			lower[i] = ch + ('a' - 'A')
		}
	}
	return string(lower)
}

func vueTagNameEnds(lower string, at int) bool {
	if at >= len(lower) {
		return true
	}
	ch := lower[at]
	return ch == '>' || ch == '/' || ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}

func isASCIILetter(ch byte) bool { return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') }
func isASCIIDigit(ch byte) bool  { return ch >= '0' && ch <= '9' }

// parseVueComponent parses a single-file component's script view under its
// dialect and merges the declarations with the file-level entities.
//
// Entity order sets containers (lexicalEntityParents expects an encloser before
// what it encloses, and the component and document records share one range):
// the document and its HTML-id elements come first, so elements keep the
// document as their container; then the component, so every top-level script
// declaration nests under it.
func parseVueComponent(ctx context.Context, path, content string, view vueScriptView) ([]Entity, string, ParseStatus) {
	spec, tsx := view.languageSpec()
	scripts, _, status := parseWithSpec(ctx, path, view.content, spec, tsx, false)
	if scripts == nil && status.Code == "E_PARSE_TIMEOUT" {
		return nil, "Vue", status
	}
	fileEntities := componentEntities(path, content, "Vue", ctxStop(ctx))
	if len(fileEntities) > 0 {
		// The component's compile-time macros declare what it takes: props,
		// emits, v-model and slots. Recording their type arguments as the
		// component's parameter types lets the signature-type pass resolve them
		// like the props parameter of a function component.
		if types := vueMacroTypeText(view.content); types != "" {
			fileEntities[0].paramTypeText = types
			fileEntities[0].signatureTypesKnown = true
		}
	}
	entities := make([]Entity, 0, len(fileEntities)+len(scripts))
	if len(fileEntities) > 1 {
		entities = append(entities, fileEntities[1:]...)
	}
	if len(fileEntities) > 0 {
		entities = append(entities, fileEntities[0])
	}
	entities = append(entities, scripts...)
	return entities, "Vue", status
}

// isVuePath reports whether path is a single-file component.
func isVuePath(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".vue")
}

// vueScriptReader wraps the relation phase's content reader so every reread of
// a .vue file returns its script view: the scanners see the script at its real
// offsets and never the template. A component without a parseable script reads
// as blank lines, so nothing in its markup can turn into a relation.
//
// Components are reread many times in one pass (imports, mixins, the worker,
// readable-import checks), so each view is computed once per path.
func vueScriptReader(read contentReader) contentReader {
	if read == nil {
		return nil
	}
	var mu sync.Mutex
	views := map[string]string{}
	return func(path string) (string, bool) {
		if !isVuePath(path) {
			return read(path)
		}
		mu.Lock()
		view, cached := views[path]
		mu.Unlock()
		if cached {
			return view, true
		}
		content, ok := read(path)
		if !ok {
			return content, false
		}
		if script, scriptOK := vueScriptViewOf(content); scriptOK {
			view = script.content
		} else {
			view = vueBlankView(content)
		}
		mu.Lock()
		views[path] = view
		mu.Unlock()
		return view, true
	}
}

func vueBlankView(content string) string {
	blank := []byte(content)
	for i, ch := range blank {
		if ch != '\n' && ch != '\r' {
			blank[i] = ' '
		}
	}
	return string(blank)
}

// scriptAnalysisLanguage is the language whose relation rules apply to a
// symbol or file. A Vue component's script is analysed as TypeScript, the
// superset its JavaScript and TypeScript dialects share; every other language
// is itself.
func scriptAnalysisLanguage(language string) string {
	if language == "Vue" {
		return "TypeScript"
	}
	return language
}

// vueFileLevelKind reports the symbols every .vue file carries for the file as
// a whole. They span the file or a template line, so they are neither callers
// nor declarations the script-level scanners should mask out.
func vueFileLevelKind(kind string) bool {
	return kind == "component" || kind == "document" || kind == "element"
}

// vueScriptSymbols drops a component's file-level symbols from the set the
// relation scanners walk.
func vueScriptSymbols(symbols []SymbolRecord) []SymbolRecord {
	out := make([]SymbolRecord, 0, len(symbols))
	for _, symbol := range symbols {
		if !vueFileLevelKind(symbol.Kind) {
			out = append(out, symbol)
		}
	}
	return out
}

// jsImporterExtension reports whether a file with this extension imports
// JavaScript modules: JS/TS sources and Vue components, whose script view is
// one. It is deliberately not jsLikeExtension, which also gates framework
// passes (Express, NestJS) that components never take part in.
func jsImporterExtension(ext string) bool {
	return jsLikeExtension(ext) || strings.EqualFold(ext, ".vue")
}

// vueMacroTypeText returns the type arguments of a component's
// defineProps/defineEmits/defineModel/defineSlots macros, joined, or "" when it
// declares none. withDefaults(defineProps<T>(), ...) is covered by its inner
// defineProps.
func vueMacroTypeText(script string) string {
	var parts []string
	for _, match := range vueMacroRe.FindAllStringIndex(script, -1) {
		open := match[1] - 1 // the '<'
		depth := 0
		for i := open; i < len(script); i++ {
			switch script[i] {
			case '<':
				depth++
			case '>':
				if i > 0 && script[i-1] == '=' {
					continue // an arrow (=>) inside a function type
				}
				depth--
				if depth == 0 {
					parts = append(parts, script[open+1:i])
					i = len(script)
				}
			}
		}
	}
	return strings.Join(parts, ", ")
}

var vueMacroRe = regexp.MustCompile(`\bdefine(?:Props|Emits|Model|Slots)\s*<`)

// vueComponentRecord returns a component's own symbol from its file's records.
func vueComponentRecord(records []SymbolRecord) (SymbolRecord, bool) {
	for _, record := range records {
		if record.Kind == "component" {
			return record, true
		}
	}
	return SymbolRecord{}, false
}

// vueComponentParent is a component's mixin, or the component it extends.
type vueComponentParent struct {
	targetID   string
	relation   string // "INHERITS" for a mixin, "EXTENDS" for extends
	name       string // the identifier written in the option
	resolution string
}

var (
	vueMixinsOptionRe  = regexp.MustCompile(`(?:^|[\s,{])mixins\s*:\s*\[([^\]]*)\]`)
	vueExtendsOptionRe = regexp.MustCompile(`(?:^|[\s,{])extends\s*:\s*([A-Za-z_$][\w$]*)`)
	vueIdentifierRe    = regexp.MustCompile(`[A-Za-z_$][\w$]*`)
)

// vueComponentParents resolves each component's `extends:` and `mixins: [...]`
// options to the symbols they name, keyed by the component's symbol ID, extends
// first and mixins in declaration order. An identifier resolves to a same-file
// declaration or, through the component's imports, to the named export of the
// imported module; a component imported from a .vue file is that file's
// component.
//
// A component whose script is external (<script src="./x.js">) declares its
// options in that file, so they are read, and their imports resolved, there.
func vueComponentParents(files []FileRecord, recordsByFile map[string][]SymbolRecord, readContent, readRaw contentReader, knownFiles map[string]bool, manifestImports manifestImportResolver, stop func() bool) map[string][]vueComponentParent {
	parents := map[string][]vueComponentParent{}
	for _, file := range files {
		if stop != nil && stop() {
			return parents
		}
		if file.Language != "Vue" {
			continue
		}
		component, ok := vueComponentRecord(recordsByFile[file.Path])
		if !ok {
			continue
		}
		content, ok := readContent(file.Path)
		if !ok {
			continue
		}
		optionsPath := file.Path
		if strings.TrimSpace(content) == "" && readRaw != nil {
			if raw, rawOK := readRaw(file.Path); rawOK {
				for _, src := range vueScriptSources(raw) {
					if target, found := resolveLocalImport(file.Path, src, knownFiles); found {
						if external, readOK := readContent(target); readOK {
							optionsPath, content = target, external
						}
						break
					}
				}
			}
		}
		var imports map[string][]string
		resolve := func(name, relation string) (vueComponentParent, bool) {
			for _, record := range recordsByFile[optionsPath] {
				if record.Name == name && !vueFileLevelKind(record.Kind) {
					return vueComponentParent{targetID: record.ID, relation: relation, name: name, resolution: "exact"}, true
				}
			}
			if imports == nil {
				imports = importedJavaScriptNames(content)
			}
			for _, spec := range imports[name] {
				resolution := "import_resolved"
				target, ok := resolveLocalImport(optionsPath, spec, knownFiles)
				if !ok {
					resolved, manifestOK := manifestImports.resolve(optionsPath, spec)
					target = resolved.Path
					if !manifestOK {
						// An alias the resolver cannot read (a jsconfig.json or
						// bundler alias) still names the file by its path suffix;
						// accept it only when exactly one file matches.
						if target, ok = vueAliasSuffixTarget(spec, knownFiles); !ok {
							continue
						}
						resolution = "pattern"
					}
				}
				if isVuePath(target) {
					if parent, ok := vueComponentRecord(recordsByFile[target]); ok {
						return vueComponentParent{targetID: parent.ID, relation: relation, name: name, resolution: resolution}, true
					}
					continue
				}
				for _, record := range recordsByFile[target] {
					if record.Name == name && record.ContainerID == "" {
						return vueComponentParent{targetID: record.ID, relation: relation, name: name, resolution: resolution}, true
					}
				}
			}
			return vueComponentParent{}, false
		}
		var list []vueComponentParent
		if match := vueExtendsOptionRe.FindStringSubmatch(content); match != nil {
			if parent, ok := resolve(match[1], "EXTENDS"); ok {
				list = append(list, parent)
			}
		}
		if match := vueMixinsOptionRe.FindStringSubmatch(content); match != nil {
			for _, name := range vueIdentifierRe.FindAllString(match[1], -1) {
				if parent, ok := resolve(name, "INHERITS"); ok {
					list = append(list, parent)
				}
			}
		}
		if len(list) > 0 {
			parents[component.ID] = list
		}
	}
	return parents
}

// vueMergeParentMethods gives each component the methods of its mixins and
// extends base, as Vue merges them: the component's own methods win, a later
// mixin wins over an earlier one, and every mixin wins over the extends base.
// A parent is merged before its children, so a component extending a component
// that uses a mixin reaches the mixin's methods too. Merged entries keep their
// own symbol IDs, so `this.nextPage()` resolves to the mixin's declaration and
// override derivation (which skips identical IDs) sees no false override.
func vueMergeParentMethods(parents map[string][]vueComponentParent, methodsByContainer map[string]map[string]SymbolRecord) {
	merged := map[string]bool{}
	var merge func(id string, visiting map[string]bool)
	merge = func(id string, visiting map[string]bool) {
		if merged[id] || visiting[id] {
			return
		}
		visiting[id] = true
		list := parents[id]
		for _, parent := range list {
			merge(parent.targetID, visiting)
		}
		own := methodsByContainer[id]
		for i := len(list) - 1; i >= 0; i-- {
			for name, method := range methodsByContainer[list[i].targetID] {
				if own == nil {
					own = map[string]SymbolRecord{}
				}
				if _, exists := own[name]; !exists {
					own[name] = method
				}
			}
		}
		if own != nil {
			methodsByContainer[id] = own
		}
		merged[id] = true
	}
	for _, id := range sortedKeysOf(parents) {
		merge(id, map[string]bool{})
	}
}

// vueAliasSuffixTarget resolves an aliased module spec (`@/mixins/paging`,
// `~/components/X.vue`) by the path that follows the alias: the one known file
// ending in it, with the usual script extensions or index files tried for an
// extensionless spec. More than one match is no match.
//
// Only the conventional root aliases count (`@/`, `~/`, webpack's `~@/`): a
// scoped package such as `@vueuse/core` also starts with `@` and must never be
// matched against local files by suffix.
func vueAliasSuffixTarget(spec string, knownFiles map[string]bool) (string, bool) {
	suffix := ""
	for _, alias := range []string{"~@/", "@/", "~/"} {
		if strings.HasPrefix(spec, alias) {
			suffix = spec[len(alias):]
			break
		}
	}
	if suffix == "" {
		return "", false
	}
	suffixes := []string{suffix}
	if filepath.Ext(suffix) == "" {
		suffixes = nil
		for _, ext := range []string{".js", ".ts", ".jsx", ".tsx", ".vue"} {
			suffixes = append(suffixes, suffix+ext, suffix+"/index"+ext)
		}
	}
	match := ""
	for path := range knownFiles {
		for _, candidate := range suffixes {
			if path == candidate || strings.HasSuffix(path, "/"+candidate) {
				if match != "" && match != path {
					return "", false
				}
				match = path
			}
		}
	}
	return match, match != ""
}

var (
	// Component events and event buses: `this.$emit('reply-sent')`,
	// `EventBus.$emit('ticket:assigned')`, a mitt bus's `bus.emit('x')`, and the
	// `emit('task-moved')` function defineEmits returns. Vuex `dispatch` is a
	// store call, not an event, so unlike emitCallRe it is not an emit verb.
	vueEmitMemberRe = regexp.MustCompile(`\.\s*\$?(?:emit|publish|broadcast)\s*\(\s*["']([^"']+)["']`)
	vueEmitBareRe   = regexp.MustCompile(`(?:^|[^\w$.])\$?emit\s*\(\s*["']([^"']+)["']`)
	vueListenRe     = regexp.MustCompile(`\.\s*\$?(?:on|once|subscribe|addEventListener|addListener)\s*\(\s*["']([^"']+)["']`)
)

// vueChannelEvents is channelEvents for a component's script: Vue's `$`-prefixed
// instance events count, and Vuex dispatch does not.
func vueChannelEvents(content string) []channelEvent {
	var out []channelEvent
	seen := map[string]bool{}
	add := func(relation, name string) {
		key := relation + " " + name
		if name == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, channelEvent{Relation: relation, Name: name})
	}
	for _, re := range []*regexp.Regexp{vueEmitMemberRe, vueEmitBareRe} {
		for _, m := range re.FindAllStringSubmatch(content, -1) {
			add("EMITS", m[1])
		}
	}
	for _, m := range vueListenRe.FindAllStringSubmatch(content, -1) {
		add("LISTENS_ON", m[1])
	}
	return out
}

// Vuex addresses store members by string: `dispatch('tickets/fetchTickets')`,
// `commit('SET_TICKETS')`, `getters['auth/isAuthenticated']`, and the
// mapActions/mapMutations/mapGetters helpers that bind them to component
// methods. vuexStore maps those strings to the action, mutation and getter
// declarations of the store's modules.
type vuexStore struct {
	// members maps kind ("action", "mutation", "getter") to a store key
	// ("tickets/fetchTickets" for a namespaced module, the bare name for the
	// root or a non-namespaced module) to the declaring method.
	members map[string]map[string]SymbolRecord
	// namespaceOf is each namespaced module file's namespace.
	namespaceOf map[string]string
}

var (
	vuexStoreCtorRe     = regexp.MustCompile(`\bVuex\s*\.\s*Store\s*\(`)
	vuexCreateStoreRe   = regexp.MustCompile(`\bcreateStore\s*\(`)
	vuexImportRe        = regexp.MustCompile(`(?:\bfrom\s*|\brequire\s*\(\s*)['"]vuex['"]`)
	vuexModulesRe       = regexp.MustCompile(`\bmodules\s*:\s*\{([^}]*)\}`)
	vuexModuleEntryRe   = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*(?::\s*([A-Za-z_$][\w$]*))?`)
	vuexNamespacedRe    = regexp.MustCompile(`\bnamespaced\s*:\s*true\b`)
	vuexDispatchRe      = regexp.MustCompile(`\bdispatch\s*\(\s*['"]([^'"]+)['"]`)
	vuexCommitRe        = regexp.MustCompile(`\bcommit\s*\(\s*['"]([^'"]+)['"]`)
	vuexGetterIndexRe   = regexp.MustCompile(`\bgetters\s*\[\s*['"]([^'"]+)['"]\s*\]`)
	vuexGetterMemberRe  = regexp.MustCompile(`\bgetters\s*\.\s*([A-Za-z_$][\w$]*)`)
	vuexMapHelperRe     = regexp.MustCompile(`\bmap(Actions|Mutations|Getters)\s*\(\s*(?:['"]([^'"]+)['"]\s*,\s*)?(\[[^\]]*\]|\{[^}]*\})`)
	vuexQuotedRe        = regexp.MustCompile(`['"]([^'"]+)['"]`)
	vuexObjectEntryRe   = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*:\s*['"]([^'"]+)['"]`)
	vuexThisMemberRe    = regexp.MustCompile(`\bthis\s*\.\s*([A-Za-z_$][\w$]*)`)
	vuexMemberContainer = map[string]string{"actions": "action", "mutations": "mutation", "getters": "getter"}
	vuexMemberObjectRe  = regexp.MustCompile(`(?:\b(?:const|let|var)\s+(actions|mutations|getters)\s*=\s*\{|(?:^|[\s,{])(actions|mutations|getters)\s*:\s*\{)`)
)

// buildVuexStore indexes the repository's Vuex store, or returns nil when there
// is none. Only a repository with Vue components is searched, and a root must be
// Vuex's own (`new Vuex.Store(`, or `createStore(` in a file importing vuex), so
// repositories without Vuex, Redux ones included, pay nothing.
func buildVuexStore(files []FileRecord, recordsByFile map[string][]SymbolRecord, readContent contentReader, knownFiles map[string]bool, manifestImports manifestImportResolver, stop func() bool) *vuexStore {
	hasComponents := false
	for _, file := range files {
		if file.Language == "Vue" {
			hasComponents = true
			break
		}
	}
	if !hasComponents {
		return nil
	}
	store := &vuexStore{members: map[string]map[string]SymbolRecord{}, namespaceOf: map[string]string{}}
	found := false
	for _, file := range files {
		if stop != nil && stop() {
			return nil
		}
		if !jsLikeExtension(filepath.Ext(file.Path)) {
			continue
		}
		content, ok := readContent(file.Path)
		if !ok || !vuexStoreCtorRe.MatchString(content) && !(vuexCreateStoreRe.MatchString(content) && vuexImportRe.MatchString(content)) {
			continue
		}
		found = true
		store.index(file.Path, "", content, recordsByFile[file.Path])
		match := vuexModulesRe.FindStringSubmatch(content)
		if match == nil {
			continue
		}
		imports := importedJavaScriptNames(content)
		for _, entry := range vuexModuleEntryRe.FindAllStringSubmatch(match[1], -1) {
			namespace, local := entry[1], entry[1]
			if entry[2] != "" {
				local = entry[2]
			}
			for _, spec := range imports[local] {
				target, ok := resolveLocalImport(file.Path, spec, knownFiles)
				if !ok {
					resolved, manifestOK := manifestImports.resolve(file.Path, spec)
					target, ok = resolved.Path, manifestOK
				}
				if !ok {
					target, ok = vueAliasSuffixTarget(spec, knownFiles)
				}
				if !ok {
					continue
				}
				moduleContent, readOK := readContent(target)
				if !readOK {
					continue
				}
				if vuexNamespacedRe.MatchString(moduleContent) {
					store.namespaceOf[target] = namespace
					store.index(target, namespace, moduleContent, recordsByFile[target])
				} else {
					store.index(target, "", moduleContent, recordsByFile[target])
				}
				break
			}
		}
	}
	if !found {
		return nil
	}
	return store
}

// index records a module file's actions, mutations and getters: the methods
// declared inside its `actions`, `mutations` and `getters` objects, whether
// those are module constants (`const actions = { ... }`) or written inline in
// the exported options. The objects themselves are usually unexported
// constants with no symbol, so members are placed by the object's source range.
func (store *vuexStore) index(path, namespace, content string, records []SymbolRecord) {
	type span struct {
		kind       string
		start, end int
	}
	stripped := stripCodeLiteralsAndComments(content)
	var spans []span
	for _, match := range vuexMemberObjectRe.FindAllStringSubmatchIndex(stripped, -1) {
		var name string
		if match[2] >= 0 {
			name = stripped[match[2]:match[3]]
		} else {
			name = stripped[match[4]:match[5]]
		}
		open := match[1] - 1
		depth, closeAt := 0, -1
		for i := open; i < len(stripped) && closeAt < 0; i++ {
			switch stripped[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					closeAt = i
				}
			}
		}
		if closeAt < 0 {
			continue
		}
		spans = append(spans, span{
			kind:  vuexMemberContainer[name],
			start: open,
			end:   closeAt,
		})
	}
	lineStarts := jsLineStarts(content)
	for _, record := range records {
		if record.Kind != "method" && record.Kind != "function" {
			continue
		}
		start := record.sourceStartByte
		if record.sourceEndByte <= start {
			// Older or synthetic records carry only the declaration line.
			if record.StartLine < 1 || record.StartLine > len(lineStarts) {
				continue
			}
			start = lineStarts[record.StartLine-1]
		}
		for _, object := range spans {
			if start <= object.start || start >= object.end {
				continue
			}
			key := record.Name
			if namespace != "" {
				key = namespace + "/" + record.Name
			}
			if store.members[object.kind] == nil {
				store.members[object.kind] = map[string]SymbolRecord{}
			}
			if _, exists := store.members[object.kind][key]; !exists {
				store.members[object.kind][key] = record
			}
			break
		}
	}
}

// resolve finds the member a store string names from a file. A name without a
// namespace, written inside a namespaced module, means that module's own
// member, as Vuex resolves it; anywhere else it means a root or
// non-namespaced member.
func (store *vuexStore) resolve(kind, name, fromPath string) (SymbolRecord, bool) {
	members := store.members[kind]
	if !strings.Contains(name, "/") {
		if namespace, ok := store.namespaceOf[fromPath]; ok {
			if member, found := members[namespace+"/"+name]; found {
				return member, true
			}
		}
	}
	member, ok := members[name]
	return member, ok
}

// vuexBinding is a component method or computed property that a map helper
// binds to a store member.
type vuexBinding struct {
	kind string
	key  string
}

// vuexMapBindings reads a component's mapActions/mapMutations/mapGetters
// helpers: the local name each binds to the store key it reaches.
func vuexMapBindings(content string) map[string]vuexBinding {
	bindings := map[string]vuexBinding{}
	for _, match := range vuexMapHelperRe.FindAllStringSubmatch(content, -1) {
		kind := map[string]string{"Actions": "action", "Mutations": "mutation", "Getters": "getter"}[match[1]]
		namespace, list := match[2], match[3]
		qualify := func(name string) string {
			if namespace != "" {
				return namespace + "/" + name
			}
			return name
		}
		if strings.HasPrefix(list, "{") {
			for _, entry := range vuexObjectEntryRe.FindAllStringSubmatch(list, -1) {
				bindings[entry[1]] = vuexBinding{kind: kind, key: qualify(entry[2])}
			}
			continue
		}
		for _, entry := range vuexQuotedRe.FindAllStringSubmatch(list, -1) {
			key := qualify(entry[1])
			local := key[strings.LastIndexByte(key, '/')+1:]
			bindings[local] = vuexBinding{kind: kind, key: key}
		}
	}
	return bindings
}

// vuexCall is one store member a code block reaches through a store string.
type vuexCall struct {
	target SymbolRecord
	detail string
}

// vuexRootOption inspects the third argument only. Payloads may themselves
// contain commas, nested calls, or a root property.
func vuexRootOption(call string) bool {
	open := strings.IndexByte(call, '(')
	closeAt := findMatchingStaticDelimiter(call, open, '(', ')')
	if closeAt < 0 {
		return false
	}
	args := splitTopLevelStaticComma(strings.TrimSuffix(strings.TrimSpace(call[open+1:closeAt]), ","))
	if len(args) != 3 {
		return false
	}
	options := strings.TrimSpace(args[2])
	if !strings.HasPrefix(options, "{") || !strings.HasSuffix(options, "}") {
		return false
	}
	for _, field := range splitTopLevelStaticComma(strings.TrimSuffix(strings.TrimSpace(options[1:len(options)-1]), ",")) {
		key, value, ok := strings.Cut(field, ":")
		if ok && strings.Trim(strings.TrimSpace(key), "\"'") == "root" {
			return strings.TrimSpace(value) == "true"
		}
	}
	return false
}

// callsIn returns the store members a code block reaches: by dispatch, commit
// or getter string, or through a map-helper binding used as `this.name`.
func (store *vuexStore) callsIn(fromPath, block string, bindings map[string]vuexBinding) []vuexCall {
	var calls []vuexCall
	seen := map[string]bool{}
	add := func(kind, key, scopePath string) {
		member, ok := store.resolve(kind, key, scopePath)
		if !ok || seen[member.ID] {
			return
		}
		seen[member.ID] = true
		calls = append(calls, vuexCall{target: member, detail: key})
	}
	for _, operation := range []struct {
		kind string
		call *regexp.Regexp
	}{{"action", vuexDispatchRe}, {"mutation", vuexCommitRe}} {
		for _, match := range operation.call.FindAllStringSubmatchIndex(block, -1) {
			scopePath := fromPath
			if store.namespaceOf[fromPath] != "" && vuexRootOption(block[match[0]:]) {
				scopePath = "" // explicit root lookup bypasses the module namespace
			}
			add(operation.kind, block[match[2]:match[3]], scopePath)
		}
	}
	for _, re := range []*regexp.Regexp{vuexGetterIndexRe, vuexGetterMemberRe} {
		for _, match := range re.FindAllStringSubmatch(block, -1) {
			add("getter", match[1], fromPath)
		}
	}
	if len(bindings) > 0 {
		for _, match := range vuexThisMemberRe.FindAllStringSubmatch(block, -1) {
			if binding, ok := bindings[match[1]]; ok {
				add(binding.kind, binding.key, fromPath)
			}
		}
	}
	return calls
}
