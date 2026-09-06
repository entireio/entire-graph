package sem

// OCaml call-site extraction. OCaml applies functions by juxtaposition —
// `Mod.fn arg1 arg2`, `fn arg`, `x |> fn` — so the generic `name(` scanner
// sees almost no OCaml call sites at all. A dedicated scanner can use what an
// OCaml call site does carry: a qualified application `Mod.fn args` names the
// target compilation unit explicitly (module `Mod` lives in `mod.ml` by
// language convention, or is a nested `module Mod = struct` symbol), and a
// bare `fn args` application can be matched against the enclosing file's own
// `let` bindings. OCaml also has lexical noise the generic stripper does not
// know: `(* ... *)` comments nest, `{|...|}` quoted strings skip escapes, and
// `'c'` character literals share the quote character with type variables
// (`'a`) and primed identifiers (`x'`). Everything here is gated to
// Language == "OCaml" so no other language's extraction shifts.

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ocamlCallSite is one application expression found in an OCaml block: a
// qualified application `Path.name args` (Path is the dotted module qualifier,
// e.g. "Ordered_set_lang.Unexpanded"), or a bare application `name args` when
// Path is empty.
type ocamlCallSite struct {
	Path      string
	Name      string
	Reference bool
}

var (
	// Uppercase-rooted dotted module path ending in a lowercase value name:
	// `Mod.fn`, `Mod.Sub.fn`. A terminal Uppercase component is a constructor
	// or module reference, not a value, and is deliberately not matched.
	ocamlQualifiedRe = regexp.MustCompile(`([A-Z][A-Za-z0-9_']*(?:\.[A-Z][A-Za-z0-9_']*)*)\.([a-z_][A-Za-z0-9_']*)`)
	ocamlIdentRe     = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_']*`)
	ocamlOpenRe      = regexp.MustCompile(`(?m)^[ \t]*open!?[ \t]+([A-Z][A-Za-z0-9_']*(?:\.[A-Z][A-Za-z0-9_']*)*)\b`)
)

// ocamlKeyword reports OCaml reserved words. Identifiers in the scan are never
// keywords in call position, but an argument-position check must not mistake a
// following keyword (`Mod.value in ...`, `f when ...`) for an argument.
func ocamlKeyword(word string) bool {
	switch word {
	case "and", "as", "assert", "asr", "begin", "class", "constraint", "do", "done",
		"downto", "else", "end", "exception", "external", "false", "for", "fun",
		"function", "functor", "if", "in", "include", "inherit", "initializer",
		"land", "lazy", "let", "lor", "lsl", "lsr", "lxor", "match", "method",
		"mod", "module", "mutable", "new", "nonrec", "object", "of", "open", "or",
		"private", "rec", "sig", "struct", "then", "to", "true", "try", "type",
		"val", "virtual", "when", "while", "with":
		return true
	}
	return false
}

func isOCamlIdentByte(b byte) bool {
	return b == '_' || b == '\'' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// stripOCamlCodeText masks the OCaml syntaxes whose payloads must never
// register as call sites: `(* ... *)` comments (which nest), `"..."` string
// literals (backslash escapes, may span lines), `{|...|}` / `{id|...|id}`
// quoted string literals (no escapes), and `'c'` character literals. A `'`
// that trails an identifier (`x'`) or introduces a type variable (`'a`) is
// left alone. Newlines are preserved so offsets keep line context.
func stripOCamlCodeText(content string) string {
	bytes := []byte(content)
	for i := 0; i < len(bytes); i++ {
		switch bytes[i] {
		case '(':
			if i+1 >= len(bytes) || bytes[i+1] != '*' {
				continue
			}
			depth := 1
			j := i + 2
			for j < len(bytes) && depth > 0 {
				if j+1 < len(bytes) && bytes[j] == '(' && bytes[j+1] == '*' {
					depth++
					j += 2
					continue
				}
				if j+1 < len(bytes) && bytes[j] == '*' && bytes[j+1] == ')' {
					depth--
					j += 2
					continue
				}
				j++
			}
			maskBytes(bytes, i, j)
			i = j - 1
		case '"':
			j := i + 1
			for j < len(bytes) {
				if bytes[j] == '\\' {
					j += 2
					continue
				}
				if bytes[j] == '"' {
					break
				}
				j++
			}
			if j >= len(bytes) {
				j = len(bytes) - 1
			}
			maskBytes(bytes, i, j+1)
			i = j
		case '{':
			// Quoted string literal `{|...|}` or `{id|...|id}`; a plain `{`
			// (record syntax) is left alone.
			k := i + 1
			for k < len(bytes) && (bytes[k] == '_' || (bytes[k] >= 'a' && bytes[k] <= 'z')) {
				k++
			}
			if k >= len(bytes) || bytes[k] != '|' {
				continue
			}
			closer := "|" + string(bytes[i+1:k]) + "}"
			rest := string(bytes[k+1:])
			end := strings.Index(rest, closer)
			if end < 0 {
				maskBytes(bytes, i, len(bytes))
				i = len(bytes) - 1
				continue
			}
			stop := k + 1 + end + len(closer)
			maskBytes(bytes, i, stop)
			i = stop - 1
		case '\'':
			if i > 0 && isOCamlIdentByte(bytes[i-1]) {
				continue // primed identifier `x'` — not a literal opener
			}
			if i+2 < len(bytes) && bytes[i+1] == '\\' {
				// Escaped character literal: '\n', '\'', '\\', '\123', '\xFF'.
				j := i + 2
				for j < len(bytes) && j-i <= 6 && bytes[j] != '\'' && bytes[j] != '\n' {
					j++
				}
				if j < len(bytes) && bytes[j] == '\'' {
					maskBytes(bytes, i, j+1)
					i = j
				}
			} else if i+2 < len(bytes) && bytes[i+1] != '\'' && bytes[i+2] == '\'' {
				maskBytes(bytes, i, i+3)
				i += 2
			}
			// Otherwise a type variable (`'a`) — leave it.
		}
	}
	return string(bytes)
}

// ocamlArgumentFollows reports whether the text after offset `end_` (skipping
// whitespace, including line breaks — formatters routinely wrap arguments onto
// the next line) starts an application argument: an identifier or constructor
// (but not a keyword), a literal, `(`, `[`, `{`, a label `~x`/`?x`, or a
// polymorphic variant. `fn @@ arg` also applies fn, with the argument behind
// the operator. A following `=`, `;`, `)`, `->`, keyword, or other operator
// means the name was a definition, a record field, or a plain value reference,
// not an application.
func ocamlArgumentFollows(s string, end int) bool {
	i := end
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	if i >= len(s) {
		return false
	}
	if i+1 < len(s) && s[i] == '@' && s[i+1] == '@' {
		return true
	}
	switch c := s[i]; {
	case c == '(' || c == '[' || c == '{' || c == '~' || c == '?' || c == '"' || c == '`' || c == '\'':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		word := ocamlIdentRe.FindString(s[i:])
		return !ocamlKeyword(word)
	}
	return false
}

// ocamlHeadPosition reports whether the name starting at `start` can be the
// head of an application, judged by the preceding non-whitespace token. In
// `eval set ~standard` the argument `set` is followed by another argument and
// would look applied; what gives it away is what precedes it — an identifier,
// a literal, or a closing bracket means the name is itself a trailing argument
// (or a labeled value after `:`), not a function being applied. Operators,
// opening brackets, separators, keywords (`if f x`, `x in f y`), and
// start-of-block all leave the name in head position.
func ocamlHeadPosition(s string, start int) bool {
	i := start - 1
	for i >= 0 && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i--
	}
	if i < 0 {
		return true
	}
	switch c := s[i]; {
	case c == ')' || c == ']' || c == '}':
		return false // trailing argument after a bracketed argument
	case c == ':':
		return false // labeled-argument value `~f:name` (and type annotations)
	case c >= '0' && c <= '9':
		return false // trailing argument after a numeric literal
	case isOCamlIdentByte(c):
		ws := i
		for ws > 0 && isOCamlIdentByte(s[ws-1]) {
			ws--
		}
		return ocamlKeyword(s[ws : i+1])
	}
	return true
}

// ocamlPipelineApplied reports whether the name starting at `start` is applied
// through the pipeline operator: in `x |> fn` the function follows the
// operator, so no argument-position evidence exists to its right. The
// right-application operator is the mirror image — in `fn @@ x` the function
// is on the *left* — so a preceding `@@` marks an argument, not a call, and is
// deliberately not accepted here (the left side is handled by
// ocamlArgumentFollows).
func ocamlPipelineApplied(s string, start int) bool {
	i := start - 1
	for i >= 0 && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i--
	}
	if i < 1 {
		return false
	}
	return s[i-1:i+1] == "|>"
}

// ocamlCallSites scans an OCaml block for application expressions,
// deduplicated and in deterministic order. Qualified applications
// (`Mod.Sub.fn args`) are matched anywhere in expression position; bare
// applications are only reported for names in `callableNames` (visible let
// bindings plus names exported by opened modules) and never from a binding head
// (`let f x y = ...` binds f, x, and y — it applies nothing).
func ocamlCallSites(block string, callableNames, referenceNames map[string]bool) []ocamlCallSite {
	stripped := stripOCamlCodeText(block)
	seen := map[ocamlCallSite]bool{}
	qualifiedAt := map[int]bool{} // offsets covered by qualified paths
	for _, m := range ocamlQualifiedRe.FindAllStringSubmatchIndex(stripped, -1) {
		for at := m[0]; at < m[1]; at++ {
			qualifiedAt[at] = true
		}
		if m[0] > 0 && (isOCamlIdentByte(stripped[m[0]-1]) || stripped[m[0]-1] == '.') {
			continue // `record.Mod...` field-access chain or mid-identifier
		}
		if !ocamlHeadPosition(stripped, m[0]) && !ocamlPipelineApplied(stripped, m[0]) {
			continue // trailing argument (`map f Mod.cmp xs`), not the head
		}
		if !ocamlArgumentFollows(stripped, m[1]) && !ocamlPipelineApplied(stripped, m[0]) {
			continue // value/field reference, record field binding, type mention
		}
		seen[ocamlCallSite{Path: stripped[m[2]:m[3]], Name: stripped[m[4]:m[5]]}] = true
	}
	if len(callableNames) > 0 {
		for _, site := range ocamlBareCallSites(stripped, callableNames, qualifiedAt) {
			seen[site] = true
		}
		if len(referenceNames) > 0 {
			for _, site := range ocamlCallableReferenceSites(stripped, referenceNames, qualifiedAt) {
				seen[site] = true
			}
		}
	}
	sites := make([]ocamlCallSite, 0, len(seen))
	for site := range seen {
		sites = append(sites, site)
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].Path != sites[j].Path {
			return sites[i].Path < sites[j].Path
		}
		return sites[i].Name < sites[j].Name
	})
	return sites
}

// ocamlBareCallSites walks the identifiers of a stripped block in order,
// tracking whether the scan is inside a binding head — between `let`/`and`/
// `fun`/`function`/`method`/`external`/`val` and the `=` or `->` that starts
// the body — where every identifier is a binder or parameter, not a call.
// Outside a head, a lowercase identifier that names a known callable binding
// and stands in application position is reported as a bare call site.
func ocamlBareCallSites(stripped string, callableNames map[string]bool, qualifiedAt map[int]bool) []ocamlCallSite {
	var sites []ocamlCallSite
	inHead := false
	prevEnd := 0
	for _, m := range ocamlIdentRe.FindAllStringIndex(stripped, -1) {
		start, end := m[0], m[1]
		gap := stripped[prevEnd:start]
		prevEnd = end
		if inHead && (strings.Contains(gap, "=") || strings.Contains(gap, "->")) {
			inHead = false
		}
		word := stripped[start:end]
		if ocamlKeyword(word) {
			switch word {
			case "let", "and", "fun", "function", "method", "external", "val":
				inHead = true
			}
			continue
		}
		if inHead {
			continue // binder or parameter name
		}
		if qualifiedAt[start] {
			continue // part of a Mod.name path handled by the qualified pass
		}
		if !callableNames[word] {
			continue
		}
		if start > 0 {
			switch stripped[start-1] {
			case '.', '~', '?', '`', '#', '\'', '%':
				continue // field access, label, variant, method call, quoted
			}
		}
		if !ocamlHeadPosition(stripped, start) && !ocamlPipelineApplied(stripped, start) {
			continue // trailing argument (`eval set ~standard`), not the head
		}
		if !ocamlArgumentFollows(stripped, end) && !ocamlPipelineApplied(stripped, start) {
			continue
		}
		sites = append(sites, ocamlCallSite{Name: word})
	}
	return sites
}

// ocamlCallableReferenceSites reports known callable values used as expression
// arguments rather than application heads, e.g.
// `make_int_padding_precision ... convert_int iconv`. Those higher-order
// references are semantic outbound dependencies even though OCaml does not write
// them as `convert_int(...)`.
func ocamlCallableReferenceSites(stripped string, callableNames map[string]bool, qualifiedAt map[int]bool) []ocamlCallSite {
	var sites []ocamlCallSite
	inHead := false
	prevEnd := 0
	for _, m := range ocamlIdentRe.FindAllStringIndex(stripped, -1) {
		start, end := m[0], m[1]
		gap := stripped[prevEnd:start]
		prevEnd = end
		if inHead && (strings.Contains(gap, "=") || strings.Contains(gap, "->")) {
			inHead = false
		}
		word := stripped[start:end]
		if ocamlKeyword(word) {
			switch word {
			case "let", "and", "fun", "function", "method", "external", "val":
				inHead = true
			}
			continue
		}
		if inHead || !callableNames[word] || qualifiedAt[start] {
			continue
		}
		if start > 0 {
			switch stripped[start-1] {
			case '.', '~', '?', '`', '#', '\'', '%', ':':
				continue
			}
		}
		// Plain head applications are handled by ocamlBareCallSites. This pass is
		// for callable values that occur as arguments to another expression.
		if ocamlHeadPosition(stripped, start) && ocamlArgumentFollows(stripped, end) {
			continue
		}
		if !ocamlReferenceArgumentPosition(stripped, start, end) {
			continue
		}
		sites = append(sites, ocamlCallSite{Name: word, Reference: true})
	}
	return sites
}

func ocamlReferenceArgumentPosition(s string, start, end int) bool {
	prev := start - 1
	for prev >= 0 && (s[prev] == ' ' || s[prev] == '\t' || s[prev] == '\n' || s[prev] == '\r') {
		prev--
	}
	if prev < 0 {
		return false
	}
	prevIsExpr := s[prev] == ')' || s[prev] == ']' || s[prev] == '}' || isOCamlIdentByte(s[prev]) || (s[prev] >= '0' && s[prev] <= '9')
	if !prevIsExpr {
		return false
	}
	next := end
	for next < len(s) && (s[next] == ' ' || s[next] == '\t' || s[next] == '\n' || s[next] == '\r') {
		next++
	}
	if next >= len(s) {
		return true
	}
	switch s[next] {
	case ';', ')', ']', '}', ',', '|':
		return true
	}
	if s[next] == '-' && next+1 < len(s) && s[next+1] == '>' {
		return false
	}
	return ocamlArgumentFollows(s, end)
}

// ocamlFileDefinesModule reports whether an OCaml source file defines module
// `module`: by filename convention (unit `mod.ml` defines module `Mod` — the
// basename with its first letter capitalized) or by an extracted nested
// `module Mod = struct` symbol.
func ocamlFileDefinesModule(path, module string, symbolsByShortName map[string][]SymbolRecord) bool {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	if ext == ".ml" || ext == ".mli" {
		unit := strings.TrimSuffix(base, ext)
		if unit != "" && strings.ToUpper(unit[:1])+unit[1:] == module {
			return true
		}
	}
	for _, s := range symbolsByShortName[module] {
		if s.Language == "OCaml" && s.Kind == "module" && s.FilePath == path {
			return true
		}
	}
	return false
}

// ocamlCallableKind reports the symbol kinds a call site can land on: plain
// `let` bindings (kind "function") and nested-module members (kind "method").
// Types, modules, and other kinds are never call targets — a lowercase name in
// a type expression (`Mod.t option`) must not fabricate a call.
func ocamlCallableKind(kind string) bool {
	return kind == "function" || kind == "method"
}

func ocamlOpenModules(content string) []string {
	stripped := stripOCamlCodeText(content)
	seen := map[string]bool{}
	var modules []string
	for _, m := range ocamlOpenRe.FindAllStringSubmatch(stripped, -1) {
		if len(m) < 2 || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		modules = append(modules, m[1])
	}
	return modules
}

func ocamlVisibleSameFileCallables(from SymbolRecord, sameFile []SymbolRecord) []SymbolRecord {
	var visible []SymbolRecord
	for _, s := range sameFile {
		if s.ID == from.ID || !ocamlCallableKind(s.Kind) {
			continue
		}
		// OCaml top-level values are sequential, while `let rec ... and ...`
		// siblings are contained in the parsed recursive block. A later binding
		// outside the caller's block is not in scope and commonly has short
		// parameter-like names (`k`, `f`) that would otherwise false-match.
		if s.StartLine > from.EndLine {
			continue
		}
		visible = append(visible, s)
	}
	return visible
}

func ocamlOpenedCallableNames(openedModules []string, symbolsByShortName map[string][]SymbolRecord) map[string]bool {
	names := map[string]bool{}
	if len(openedModules) == 0 {
		return names
	}
	for name, symbols := range symbolsByShortName {
		for _, s := range symbols {
			if s.Language != "OCaml" || !ocamlCallableKind(s.Kind) {
				continue
			}
			if ocamlAnyOpenedModuleDefinesFile(s.FilePath, openedModules, symbolsByShortName) {
				names[name] = true
				break
			}
		}
	}
	return names
}

func ocamlOpenedCallableReferenceNames(openedModules []string, symbolsByShortName map[string][]SymbolRecord) map[string]bool {
	names := map[string]bool{}
	if len(openedModules) == 0 {
		return names
	}
	for name, symbols := range symbolsByShortName {
		for _, s := range symbols {
			if s.Language != "OCaml" || !ocamlCallableReferenceTarget(s) {
				continue
			}
			if ocamlAnyOpenedModuleDefinesFile(s.FilePath, openedModules, symbolsByShortName) {
				names[name] = true
				break
			}
		}
	}
	return names
}

func ocamlCallableReferenceTarget(symbol SymbolRecord) bool {
	if !ocamlCallableKind(symbol.Kind) {
		return false
	}
	signature := strings.TrimSpace(symbol.Signature)
	if signature == "" {
		return true
	}
	if strings.Contains(signature, " = fun ") || strings.Contains(signature, " -> ") {
		return true
	}
	for _, prefix := range []string{"let rec ", "let ", "and "} {
		if !strings.HasPrefix(signature, prefix) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(signature, prefix))
		if !strings.HasPrefix(rest, symbol.Name) {
			continue
		}
		after := strings.TrimSpace(rest[len(symbol.Name):])
		if after == "" || strings.HasPrefix(after, "=") {
			return false
		}
		if strings.HasPrefix(after, ":") {
			return strings.Contains(after, "->") || strings.Contains(after, "= fun")
		}
		return true
	}
	return false
}

func ocamlAnyOpenedModuleDefinesFile(path string, openedModules []string, symbolsByShortName map[string][]SymbolRecord) bool {
	for _, modulePath := range openedModules {
		root, _, _ := strings.Cut(modulePath, ".")
		if ocamlFileDefinesModule(path, root, symbolsByShortName) {
			return true
		}
	}
	return false
}

func ocamlResolveOpenedTargets(name string, from SymbolRecord, openedModules []string, symbolsByShortName map[string][]SymbolRecord) []resolvedCallTarget {
	for i := len(openedModules) - 1; i >= 0; i-- {
		root, _, _ := strings.Cut(openedModules[i], ".")
		var targets []resolvedCallTarget
		for _, to := range symbolsByShortName[name] {
			if to.ID == from.ID || to.Language != "OCaml" || !ocamlCallableKind(to.Kind) {
				continue
			}
			if !localReachable(from, to) || !ocamlFileDefinesModule(to.FilePath, root, symbolsByShortName) {
				continue
			}
			targets = append(targets, resolvedCallTarget{
				SymbolRecord: to,
				Confidence:   0.84,
				Reason:       "bare application resolved through open module",
				Resolution:   "import_resolved",
				Scope:        "module",
			})
		}
		targets = ocamlPreferImplementationTargets(targets)
		if len(targets) > 0 {
			return targets
		}
	}
	return nil
}

// ocamlCallRelations resolves the application sites in an OCaml symbol's block.
// Bare applications resolve first to visible same-file `let` bindings, then
// through top-level opened modules; qualified applications resolve to callables
// defined in a file that defines the root module of the qualifier. When both
// `mod.ml` and `mod.mli` match, the implementation wins: the `.mli` is an
// interface restating the same value. Recursive calls to the enclosing symbol
// itself are skipped.
func ocamlCallRelations(from SymbolRecord, block string, sameFile []SymbolRecord, symbolsByShortName map[string][]SymbolRecord, openedModules []string, openedCallableNames, openedReferenceNames map[string]bool) []RelationRecord {
	localNames := map[string]bool{}
	referenceNames := map[string]bool{}
	visibleSameFile := ocamlVisibleSameFileCallables(from, sameFile)
	for _, s := range visibleSameFile {
		localNames[s.Name] = true
		if ocamlCallableReferenceTarget(s) {
			referenceNames[s.Name] = true
		}
	}
	callableNames := map[string]bool{}
	for name := range localNames {
		callableNames[name] = true
	}
	for name := range openedCallableNames {
		callableNames[name] = true
	}
	for name := range openedReferenceNames {
		referenceNames[name] = true
	}
	var relations []RelationRecord
	for _, site := range ocamlCallSites(block, callableNames, referenceNames) {
		var targets []resolvedCallTarget
		if site.Path == "" {
			for _, to := range visibleSameFile {
				if to.ID == from.ID || to.Kind != "function" || to.Name != site.Name {
					continue
				}
				targets = append(targets, resolvedCallTarget{
					SymbolRecord: to,
					Confidence:   0.92,
					Reason:       "bare application resolved to same-file let binding",
					Resolution:   "exact",
					Scope:        "file",
				})
			}
			if len(targets) == 0 {
				targets = ocamlResolveOpenedTargets(site.Name, from, openedModules, symbolsByShortName)
			}
		} else {
			root, _, _ := strings.Cut(site.Path, ".")
			for _, to := range symbolsByShortName[site.Name] {
				if to.ID == from.ID || to.Language != "OCaml" || !ocamlCallableKind(to.Kind) {
					continue
				}
				if !ocamlFileDefinesModule(to.FilePath, root, symbolsByShortName) {
					continue
				}
				targets = append(targets, resolvedCallTarget{
					SymbolRecord: to,
					Confidence:   0.9,
					Reason:       "qualified application resolved to the module named by the qualifier",
					Resolution:   "exact",
					Scope:        "module",
				})
			}
			targets = ocamlPreferImplementationTargets(targets)
		}
		detail := site.Name
		if site.Path != "" {
			detail = site.Path + "." + site.Name
		}
		for _, to := range targets {
			reason := to.Reason
			if site.Reference {
				reason = "callable value reference resolved to OCaml symbol"
			}
			relations = append(relations, RelationRecord{
				RecordType:    "relation",
				FromID:        from.ID,
				ToID:          to.ID,
				Type:          "CALLS",
				Confidence:    to.Confidence,
				Reason:        reason,
				RelationScope: to.Scope,
				Resolution:    to.Resolution,
				TargetKind:    "symbol",
				Evidence: []Evidence{{
					Kind:      "call_site",
					FilePath:  from.FilePath,
					StartLine: from.StartLine,
					EndLine:   from.EndLine,
					Detail:    detail,
				}},
				WarningCodes: []string{},
			})
		}
	}
	return relations
}

// ocamlPreferImplementationTargets drops a `.mli` target when the same unit's
// `.ml` also matched: both declare the same value, and the implementation is
// where the body lives. A `.mli`-only match (implementation symbol not
// extracted) is kept rather than dropped.
func ocamlPreferImplementationTargets(targets []resolvedCallTarget) []resolvedCallTarget {
	implUnits := map[string]bool{}
	for _, t := range targets {
		if strings.HasSuffix(t.FilePath, ".ml") {
			implUnits[strings.TrimSuffix(t.FilePath, ".ml")] = true
		}
	}
	if len(implUnits) == 0 {
		return targets
	}
	kept := targets[:0]
	for _, t := range targets {
		if strings.HasSuffix(t.FilePath, ".mli") && implUnits[strings.TrimSuffix(t.FilePath, ".mli")] {
			continue
		}
		kept = append(kept, t)
	}
	return kept
}

// ocamlCallScanFile reports whether OCaml call extraction applies to a file:
// implementations only. A `.mli` interface contains no expressions — its val
// signatures are type mentions whose lowercase names (`Mod.t option`) would
// only fabricate call edges.
func ocamlCallScanFile(path string) bool {
	return strings.HasSuffix(path, ".ml")
}
