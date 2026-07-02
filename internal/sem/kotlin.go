package sem

// Kotlin-specific call-site extraction. The generic scanners miss the dominant
// Kotlin call idioms (evidence: on square/okhttp the focus method
// RealWebSocket.failWebSocket resolved 5/5 inbound but 0/4 outbound edges):
//
//   - Class properties (`private var taskQueue = taskRunner.newQueue()`,
//     `internal val listener: WebSocketListener` primary-constructor
//     properties) have no var-type source, so `taskQueue.execute(...)` and
//     `listener.onFailure(...)` never resolve. The property's type is declared
//     at the class level: explicit `val name: Type` annotations, constructor
//     val/var parameters, or a factory initializer whose declared return type
//     is unambiguous in the workspace.
//   - Declared-type locals (`val writerToClose: WebSocketWriter?`) are missed
//     by localVarTypes, which only understands constructor assignments.
//   - Safe calls (`socket?.closeQuietly()`) and trailing-lambda calls
//     (`taskQueue.execute { ... }`, bare `runTask { ... }`) never match
//     receiverCallRe / callLikeIdentifiers, which require a literal `.` and a
//     literal `(` after the method name.
//   - Top-level extension functions (`fun Closeable.closeQuietly()`) are not
//     members of any container, so member lookup on the receiver's type can
//     never find them.
//
// Everything in this file is gated to Language == "Kotlin" by its callers so
// the other languages keep their existing behavior. The extractors follow the
// same conservative straight-line rules as ruby.go/php.go: single-assignment
// tracking, capitalized type names only, conflicting bindings dropped,
// ambiguous lookups resolved to nothing rather than guessed.

import (
	"regexp"
	"strings"
)

var (
	// receiver.method( / receiver?.method( / receiver!!.method( call sites, also
	// accepting a trailing-lambda `{` where the argument list would start
	// (`taskQueue.execute { ... }` has no parentheses at all).
	kotlinReceiverCallRe = regexp.MustCompile(`\b([A-Za-z_]\w*)\s*(?:\?\.|!!\.|\.)\s*([A-Za-z_]\w*)\s*([({])`)
	// Bare trailing-lambda call `name { ... }` (no receiver, no parentheses).
	kotlinBareLambdaCallRe = regexp.MustCompile(`\b([A-Za-z_]\w*)[ \t]*\{`)
	// val/var declarations with an explicit type annotation.
	kotlinTypedDeclRe = regexp.MustCompile(`\b(?:val|var)\s+([A-Za-z_]\w*)\s*:\s*([A-Za-z_][\w.]*)`)
	// Class-body property declarations start with at least one modifier keyword,
	// which locals essentially never carry; this keeps method-body locals out of
	// the class-level property map.
	kotlinModifiedPropTypedRe = regexp.MustCompile(`(?m)^[ \t]*(?:(?:public|protected|private|internal|lateinit|open|final|override|const)\s+)+(?:val|var)\s+([A-Za-z_]\w*)\s*:\s*([A-Za-z_][\w.]*)`)
	// Modifier-prefixed property with an initializer instead of a type
	// annotation: `private var taskQueue = taskRunner.newQueue()` or
	// `private val lock = ReentrantLock()`.
	kotlinModifiedPropInitRe = regexp.MustCompile(`(?m)^[ \t]*(?:(?:public|protected|private|internal|open|final|override)\s+)+(?:val|var)\s+([A-Za-z_]\w*)\s*=\s*([^\n]+)`)
	// Primary-constructor header: `class Name(` with optional modifiers and an
	// optional `internal constructor(` between the name and the parameter list.
	kotlinClassHeaderRe = regexp.MustCompile(`\b(?:class|interface)\s+([A-Za-z_]\w*)[^({\n]*\(`)
	// One primary-constructor parameter: optional annotations, optional
	// visibility, optional val/var (which promotes it to a property), then
	// `name: Type`.
	kotlinCtorParamRe = regexp.MustCompile(`^(?:@\w+(?:\([^)]*\))?\s*)*(?:(?:public|protected|private|internal|override)\s+)*(val\s+|var\s+)?([A-Za-z_]\w*)\s*:\s*([A-Za-z_][\w.]*)`)
	// Constructor-call initializer: `= Klass(...)`.
	kotlinCtorInitRe = regexp.MustCompile(`^([A-Z]\w*)\s*\(`)
	// Factory-call initializer: `= receiver.factory(...)`.
	kotlinFactoryInitRe = regexp.MustCompile(`^([a-z_]\w*)\s*\.\s*([A-Za-z_]\w*)\s*\(`)
	// Extension-function signature: `fun Closeable.closeQuietly(`, including
	// generic receivers (`fun <T> List<T>.foo(`) and nullable ones.
	kotlinExtensionFunRe = regexp.MustCompile(`\bfun\s+(?:<[^>]*>\s+)?([A-Za-z_][\w.]*(?:<[^<>]*>)?\??)\s*\.\s*([A-Za-z_]\w*)\s*\(`)
)

// kotlinLambdaKeywords are bare words before `{` that are never call sites:
// control flow and declaration keywords whose blocks are plain syntax, plus the
// ubiquitous stdlib lambda-takers whose targets would be noise (a repo symbol
// shadowing `let`/`run` cannot be told apart from the stdlib call).
var kotlinLambdaKeywords = map[string]bool{
	"else": true, "try": true, "finally": true, "do": true, "when": true,
	"init": true, "companion": true, "object": true, "class": true,
	"interface": true, "enum": true, "fun": true, "val": true, "var": true,
	"get": true, "set": true, "where": true, "constructor": true,
	"suspend": true, "in": true, "is": true, "by": true, "catch": true,
	"run": true, "let": true, "also": true, "apply": true, "with": true,
	"use": true, "repeat": true, "lazy": true, "forEach": true, "map": true,
	"filter": true, "takeIf": true, "takeUnless": true, "synchronized": true,
	"withLock": true, "runCatching": true,
}

// stripKotlinCodeText extends the generic literal/comment stripper with raw
// (triple-quoted) strings, whose bodies span lines: the generic stripper resets
// its string state at each newline, so a multi-line `"""..."""` body would
// otherwise leak prose (and any `name(` inside it) into the call scanners.
// Length and line structure are preserved. Single-line strings, `${...}`
// templates inside them, and `//`/`/* */` comments are handled by the generic
// stripper.
func stripKotlinCodeText(content string) string {
	return stripCodeLiteralsAndComments(maskKotlinRawStrings(content))
}

// maskKotlinRawStrings blanks `"""..."""` raw-string literals, including the
// delimiters, preserving newlines.
func maskKotlinRawStrings(content string) string {
	bytes := []byte(content)
	for i := 0; i+2 < len(bytes); i++ {
		if bytes[i] != '"' || bytes[i+1] != '"' || bytes[i+2] != '"' {
			continue
		}
		j := i + 3
		for j+2 < len(bytes) && !(bytes[j] == '"' && bytes[j+1] == '"' && bytes[j+2] == '"') {
			j++
		}
		if j+2 < len(bytes) {
			j += 3
		} else {
			j = len(bytes)
		}
		maskBytes(bytes, i, j)
		i = j - 1
	}
	return string(bytes)
}

// kotlinReceiverCalls extracts `receiver.method(...)` call sites, accepting the
// safe-call (`?.`) and non-null-asserted (`!!.`) operators and trailing-lambda
// invocations (`receiver.method { ... }`), none of which match the generic
// receiverCallRe.
func kotlinReceiverCalls(block string) []receiverCall {
	stripped := stripKotlinCodeText(block)
	var out []receiverCall
	seen := map[string]bool{}
	for _, m := range kotlinReceiverCallRe.FindAllStringSubmatchIndex(stripped, -1) {
		receiver := stripped[m[2]:m[3]]
		method := stripped[m[4]:m[5]]
		key := receiver + "." + method
		if receiver == "" || method == "" || seen[key] {
			continue
		}
		args := ""
		if open := m[6]; stripped[open] == '(' {
			if close := matchingParen(stripped, open); close > open {
				args = stripped[open+1 : close]
			}
		}
		seen[key] = true
		out = append(out, receiverCall{Receiver: receiver, Method: method, Args: args})
	}
	return out
}

// kotlinBareLambdaCallIdentifiers returns bare `name { ... }` trailing-lambda
// call sites, which carry no parentheses and are invisible to the generic
// `name(` scanner. Keywords, stdlib scope functions, declaration names
// (`class Foo {`), supertype lists (`object : Callback {`), and dotted
// selectors (handled by the receiver path) are excluded; resolution precision
// comes from the caller only emitting an edge when the name matches a
// workspace symbol.
func kotlinBareLambdaCallIdentifiers(content string) map[string]struct{} {
	stripped := stripKotlinCodeText(content)
	out := map[string]struct{}{}
	for _, m := range kotlinBareLambdaCallRe.FindAllStringSubmatchIndex(stripped, -1) {
		name := stripped[m[2]:m[3]]
		if kotlinLambdaKeywords[name] || kotlinBareLambdaContextExcluded(stripped, m[2]) {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

// kotlinBareLambdaContextExcluded reports whether the word starting at start
// cannot be a bare trailing-lambda call site: dotted/safe-call selectors
// (receiver path), annotations, supertype and type-annotation positions, and
// words directly preceded by a declaration keyword (`class Foo {`).
func kotlinBareLambdaContextExcluded(stripped string, start int) bool {
	for i := start - 1; i >= 0; i-- {
		switch stripped[i] {
		case ' ', '\t':
			continue
		case '.', '?', ':', ',', '@', '>', '<':
			return true
		}
		break
	}
	for _, keyword := range []string{"class", "interface", "object", "enum", "fun", "val", "var", "companion", "is", "as"} {
		if rubyWordBefore(stripped, start, keyword) {
			return true
		}
	}
	return false
}

// kotlinTypeName normalizes a matched type reference to the terminal,
// capitalized type segment (`okio.Socket` -> Socket); nullability markers and
// generics never make it into the match. Empty when the segment is not a
// plausible class name.
func kotlinTypeName(ref string) string {
	name := lastTypeSegment(ref)
	if !isCapitalized(name) {
		return ""
	}
	return name
}

// kotlinLocalVarTypes infers variable -> type from declared-type local
// declarations (`val writerToClose: WebSocketWriter?`). Constructor
// assignments (`val w = WebSocketWriter(...)`) are already handled by the
// generic localVarTypes. A name declared with two different types is dropped.
func kotlinLocalVarTypes(block string) map[string]string {
	stripped := stripKotlinCodeText(block)
	out := map[string]string{}
	conflicted := map[string]bool{}
	for _, m := range kotlinTypedDeclRe.FindAllStringSubmatch(stripped, -1) {
		name, typeName := m[1], kotlinTypeName(m[2])
		if typeName == "" {
			continue
		}
		if existing, ok := out[name]; ok && existing != typeName {
			conflicted[name] = true
			continue
		}
		out[name] = typeName
	}
	for name := range conflicted {
		delete(out, name)
	}
	return out
}

// kotlinPropertyTypes infers property -> type for a Kotlin file from its cheap
// declared sources: primary-constructor val/var parameters, modifier-prefixed
// class-body declarations with explicit types, and modifier-prefixed
// initializers (`= Klass(...)` constructors, `= receiver.factory()` calls
// whose factory's declared return type is workspace-unique). Kotlin files may
// hold several classes; a property name bound to two different types is
// dropped rather than guessed.
func kotlinPropertyTypes(content string, returnTypesBySymbolNameAndFile map[string]map[string][]string) map[string]string {
	stripped := stripKotlinCodeText(content)
	out := map[string]string{}
	conflicted := map[string]bool{}
	record := func(name, typeName string) {
		if name == "" || typeName == "" {
			return
		}
		if existing, ok := out[name]; ok && existing != typeName {
			conflicted[name] = true
			return
		}
		out[name] = typeName
	}
	// Primary-constructor parameters. Plain (non-val/var) parameters are not
	// properties, but they are the only names a property initializer can
	// reference besides other properties, so they participate as factory
	// receivers below.
	ctorParamTypes := map[string]string{}
	for _, loc := range kotlinClassHeaderRe.FindAllStringSubmatchIndex(stripped, -1) {
		open := loc[1] - 1
		close := matchingParen(stripped, open)
		if close < 0 {
			continue
		}
		for _, param := range splitTopLevelCommas(stripped[open+1 : close]) {
			m := kotlinCtorParamRe.FindStringSubmatch(strings.TrimSpace(param))
			if m == nil {
				continue
			}
			propertyMarker, name, typeName := m[1], m[2], kotlinTypeName(m[3])
			if typeName == "" {
				continue
			}
			ctorParamTypes[name] = typeName
			if propertyMarker != "" {
				record(name, typeName)
			}
		}
	}
	// Modifier-prefixed declarations with explicit type annotations.
	for _, m := range kotlinModifiedPropTypedRe.FindAllStringSubmatch(stripped, -1) {
		if typeName := kotlinTypeName(m[2]); typeName != "" {
			record(m[1], typeName)
		}
	}
	// Modifier-prefixed initializers: `= Klass(...)` types the property
	// directly; `= receiver.factory(...)` types it by the factory's declared
	// return type when the receiver is a known constructor parameter or
	// property and the return type is unambiguous across the workspace.
	for _, m := range kotlinModifiedPropInitRe.FindAllStringSubmatch(stripped, -1) {
		name, init := m[1], strings.TrimSpace(m[2])
		if cm := kotlinCtorInitRe.FindStringSubmatch(init); cm != nil {
			record(name, cm[1])
			continue
		}
		fm := kotlinFactoryInitRe.FindStringSubmatch(init)
		if fm == nil {
			continue
		}
		receiver, factory := fm[1], fm[2]
		if _, ok := ctorParamTypes[receiver]; !ok {
			if _, ok := out[receiver]; !ok {
				continue
			}
		}
		if typeName := kotlinUniqueReturnType(factory, returnTypesBySymbolNameAndFile); typeName != "" {
			record(name, typeName)
		}
	}
	for name := range conflicted {
		delete(out, name)
	}
	return out
}

// kotlinUniqueReturnType returns the single declared return type of the named
// callable when every declaration in the workspace agrees on exactly one type
// name (`fun newQueue(): TaskQueue` -> TaskQueue). Ambiguity (overloads with
// different returns, generic returns naming several types) yields "".
func kotlinUniqueReturnType(name string, returnTypesBySymbolNameAndFile map[string]map[string][]string) string {
	unique := ""
	for _, types := range returnTypesBySymbolNameAndFile[name] {
		for _, typeName := range types {
			if unique == "" {
				unique = typeName
			} else if unique != typeName {
				return ""
			}
		}
	}
	return unique
}

// kotlinExtensionReceiver returns the declared extension-receiver type of a
// Kotlin function signature for the given function name
// (`fun Closeable.closeQuietly()` -> Closeable), or "" for ordinary functions.
func kotlinExtensionReceiver(signature, name string) string {
	for _, m := range kotlinExtensionFunRe.FindAllStringSubmatch(signature, -1) {
		if m[2] != name {
			continue
		}
		return kotlinTypeName(strings.TrimSuffix(stripGenerics(m[1]), "?"))
	}
	return ""
}

// kotlinSupertypeNames parses the supertype list from a Kotlin class/interface
// declaration signature (`class WebSocketWriter( ... ) : Closeable` ->
// [Closeable]). Delegation (`by`), superclass constructor arguments, and
// generics are stripped; only capitalized terminal segments are kept.
func kotlinSupertypeNames(signature string) []string {
	sig := stripGenerics(signature)
	rest := sig
	if open := strings.Index(sig, "("); open >= 0 {
		if close := matchingParen(sig, open); close > open {
			rest = sig[close+1:]
		}
	}
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return nil
	}
	identRe := regexp.MustCompile(`^[A-Za-z_][\w.]*`)
	var out []string
	for _, part := range splitTopLevelCommas(rest[colon+1:]) {
		name := kotlinTypeName(identRe.FindString(strings.TrimSpace(part)))
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// kotlinReceiverTypeNames returns the receiver type plus its (workspace-known)
// supertype names, walking declaration signatures transitively with a small
// depth bound. External supertypes that never resolve to a workspace symbol
// still contribute their name when spelled in a signature, which is exactly
// what extension receivers like java.io.Closeable need.
func kotlinReceiverTypeNames(typeName string, from SymbolRecord, symbolsByShortName map[string][]SymbolRecord) map[string]bool {
	names := map[string]bool{}
	var visit func(name string, depth int)
	visit = func(name string, depth int) {
		if name == "" || names[name] || depth > 4 {
			return
		}
		names[name] = true
		sym, ok := firstTypeLikeNamedPreferFile(symbolsByShortName[name], name, from.FilePath)
		if !ok || sym.Language != "Kotlin" {
			return
		}
		for _, super := range kotlinSupertypeNames(sym.Signature) {
			visit(super, depth+1)
		}
	}
	visit(typeName, 0)
	return names
}

// kotlinExtensionFunctionTarget resolves a `receiver.method(...)` call whose
// member lookup failed to a top-level Kotlin extension function. With a typed
// receiver the extension's declared receiver type must match the receiver's
// type or one of its supertypes, uniquely; with an unknown receiver type only
// a workspace-unique extension name resolves (the same unique-name stance as
// the generic fallbacks). The boolean reports whether the match was
// type-directed.
func kotlinExtensionFunctionTarget(call receiverCall, receiverType string, from SymbolRecord, symbolsByShortName map[string][]SymbolRecord) (SymbolRecord, bool, bool) {
	var extensions []SymbolRecord
	for _, cand := range symbolsByShortName[call.Method] {
		if cand.ID == from.ID || cand.Kind != "function" || cand.Language != "Kotlin" {
			continue
		}
		if kotlinExtensionReceiver(cand.Signature, call.Method) == "" {
			continue
		}
		extensions = append(extensions, cand)
	}
	if len(extensions) == 0 {
		return SymbolRecord{}, false, false
	}
	if receiverType != "" {
		receiverNames := kotlinReceiverTypeNames(receiverType, from, symbolsByShortName)
		var matches []SymbolRecord
		for _, ext := range extensions {
			if receiverNames[kotlinExtensionReceiver(ext.Signature, call.Method)] {
				matches = append(matches, ext)
			}
		}
		if len(matches) == 1 {
			return matches[0], true, true
		}
		return SymbolRecord{}, false, false
	}
	if len(extensions) == 1 {
		return extensions[0], false, true
	}
	return SymbolRecord{}, false, false
}
