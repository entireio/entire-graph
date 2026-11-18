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
	blocks := vueScriptBlocks(content)
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

type vueScriptBlock struct {
	attrs              string
	bodyStart, bodyEnd int
}

// vueScriptBlocks returns the top-level <script> blocks of a single-file
// component. Only top-level blocks count: template, style and custom blocks are
// skipped whole (a <template> may nest further <template> tags), and HTML
// comments between blocks are ignored.
func vueScriptBlocks(content string) []vueScriptBlock {
	var blocks []vueScriptBlock
	lower := asciiLower(content)
	for i := 0; i < len(content); {
		lt := strings.IndexByte(content[i:], '<')
		if lt < 0 {
			break
		}
		i += lt
		if strings.HasPrefix(content[i:], "<!--") {
			end := strings.Index(content[i+4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		name, attrs, tagEnd, selfClosing, ok := vueOpenTag(content, i)
		if !ok {
			i++
			continue
		}
		if selfClosing {
			i = tagEnd
			continue
		}
		if name == "script" {
			closeAt := strings.Index(lower[tagEnd:], "</script")
			if closeAt < 0 {
				blocks = append(blocks, vueScriptBlock{attrs: attrs, bodyStart: tagEnd, bodyEnd: len(content)})
				break
			}
			blocks = append(blocks, vueScriptBlock{attrs: attrs, bodyStart: tagEnd, bodyEnd: tagEnd + closeAt})
			i = tagEnd + closeAt + len("</script")
			continue
		}
		i = vueSkipBlock(content, lower, name, tagEnd)
	}
	return blocks
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

// vueSkipBlock returns the index just past the close of a non-script top-level
// block named name whose opening tag ends at from. Same-name tags nest (a
// <template> holds <template v-slot> children); raw-text blocks like <style>
// have no nested tags of their own name.
func vueSkipBlock(content, lower, name string, from int) int {
	depth := 1
	open, closing := "<"+name, "</"+name
	for i := from; i < len(content); {
		next := strings.IndexByte(content[i:], '<')
		if next < 0 {
			return len(content)
		}
		i += next
		switch {
		case strings.HasPrefix(lower[i:], closing) && vueTagNameEnds(lower, i+len(closing)):
			depth--
			gt := strings.IndexByte(content[i:], '>')
			if gt < 0 {
				return len(content)
			}
			i += gt + 1
			if depth == 0 {
				return i
			}
		case strings.HasPrefix(lower[i:], open) && vueTagNameEnds(lower, i+len(open)):
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
	return len(content)
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
