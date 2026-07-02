package sem

// Swift-specific receiver typing. The generic scanners miss the dominant Swift
// call idioms (evidence: on apple/swift-nio the focus method
// ByteBuffer.discardReadBytes resolved 0/3 inbound CALLS edges):
//
//   - Parameters are spelled `label name: inout Type` (`func finishProcessing(
//     remainder buffer: inout ByteBuffer)`), which none of parameterVarTypes'
//     branches understand: the colon branch requires the colon directly after
//     the first identifier and cannot skip `inout`/`borrowing`/`consuming` or
//     an argument label, so `buffer.discardReadBytes()` never resolves.
//   - Locals bound by enum-case patterns (`case .available(var buffer):`)
//     carry their type on the enum declaration's payload
//     (`case available(ByteBuffer)`), not on the binding.
//   - Stored properties (`internal private(set) var _buffer: ByteBuffer?`)
//     have no var-type source, and force-unwrapped/optional-chained receivers
//     (`self._buffer!.discardReadBytes()`, `delegate?.retry(...)`) never match
//     the generic receiverCallRe, which requires a literal `.` directly after
//     the receiver name.
//   - Multiline string literals (`"""..."""`) span lines, so the generic
//     line-scoped stripper leaks their bodies (and any `name(` inside) into
//     the call scanners.
//
// Everything in this file is gated to Language == "Swift" by its callers so
// the other languages keep their existing behavior. The extractors follow the
// same conservative straight-line rules as kotlin.go/php.go: capitalized type
// names only, conflicting bindings dropped, ambiguous lookups resolved to
// nothing rather than guessed.

import (
	"regexp"
	"strings"
)

// swiftModifierPattern is one declaration modifier (with an optional argument,
// covering `private(set)` and `unowned(unsafe)`). Class-body property maps
// require at least one so method-body locals stay out of them, mirroring
// kotlinModifiedPropTypedRe.
const swiftModifierPattern = `(?:(?:public|private|fileprivate|internal|open|package|static|class|final|lazy|weak|unowned|override|required|nonisolated|indirect)(?:\(\w+\))?\s+)`

var (
	// receiver.method( / receiver?.method( / receiver!.method( call sites. The
	// generic receiverCallRe already covers the plain-dot form; this adds the
	// optional-chaining and force-unwrap operators.
	swiftReceiverCallRe = regexp.MustCompile(`\b([A-Za-z_]\w*)\s*[!?]?\.\s*([A-Za-z_]\w*)\s*\(`)
	// let/var declarations with an explicit type annotation.
	swiftTypedDeclRe = regexp.MustCompile(`\b(?:let|var)\s+([A-Za-z_]\w*)\s*:\s*([A-Za-z_][\w.]*)`)
	// Enum-case pattern bindings: `case .available(var buffer)` and the
	// binding-first spelling `case let .available(buffer)`, both also behind
	// `if`/`guard case`. Multi-payload patterns intentionally fail the match.
	swiftCaseBindingRe    = regexp.MustCompile(`\bcase\s+\.([A-Za-z_]\w*)\s*\(\s*(?:let|var)\s+([A-Za-z_]\w*)\s*\)`)
	swiftCaseLetBindingRe = regexp.MustCompile(`\bcase\s+(?:let|var)\s+\.([A-Za-z_]\w*)\s*\(\s*([A-Za-z_]\w*)\s*\)`)
	// Modifier-prefixed class-body stored properties with explicit types
	// (`internal private(set) var _buffer: ByteBuffer?`).
	swiftPropTypedRe = regexp.MustCompile(`(?m)^[ \t]*` + swiftModifierPattern + `+(?:let|var)\s+([A-Za-z_]\w*)\s*:\s*([A-Za-z_][\w.]*)`)
	// Modifier-prefixed stored property with a constructor initializer instead
	// of a type annotation: `private var buffers = CircularBuffer<ByteBuffer>()`.
	swiftPropInitRe = regexp.MustCompile(`(?m)^[ \t]*` + swiftModifierPattern + `+(?:let|var)\s+([A-Za-z_]\w*)\s*=\s*([A-Z]\w*)\s*[<(]`)
	// Enum-case declaration with an associated-value list: `case available(ByteBuffer)`.
	// Switch patterns never match (they spell the case with a leading dot).
	swiftEnumCaseDeclRe = regexp.MustCompile(`(?m)^[ \t]*(?:(?:indirect|public|private|fileprivate|internal|package)\s+)*case\s+([A-Za-z_]\w*)\s*\(([^()\n]*)\)`)
	// One parameter of a Swift function signature: optional argument label,
	// internal name, then `:` and the type, skipping attributes
	// (`@escaping`), ownership modifiers (`inout`, `borrowing`, ...) and
	// existential/opaque markers (`any`, `some`). Function-type parameters
	// (`(inout Decoder, inout ByteBuffer) -> DecodingState`) intentionally
	// fail the match: their receiver typing is not worth guessing.
	swiftParamRe = regexp.MustCompile(`^(?:([A-Za-z_]\w*)\s+)?([A-Za-z_]\w*)\s*:\s*(?:@\w+(?:\([^)]*\))?\s+)*(?:(?:inout|borrowing|consuming|__owned|__shared)\s+)?(?:(?:some|any)\s+)?([A-Za-z_][\w.]*)`)
)

// swiftFileTypes carries the per-file declared-type maps that live outside any
// method's block: stored-property types and enum-case payload types. Collected
// once per Swift file by the relation pass and threaded into
// receiverCallRelations, like phpPropTypes/kotlinPropTypes.
type swiftFileTypes struct {
	props        map[string]string
	enumPayloads map[string]string
}

// maskSwiftMultilineStrings blanks `"""..."""` multiline string literals,
// including the delimiters, preserving newlines. Swift's delimiters and
// line-spanning bodies match Kotlin's raw strings exactly, so the masking is
// shared.
func maskSwiftMultilineStrings(content string) string {
	return maskKotlinRawStrings(content)
}

// stripSwiftCodeText extends the generic literal/comment stripper with Swift's
// multiline string literals, whose `"""` delimiters and line-spanning bodies
// match Kotlin's raw strings exactly; without the masking the generic stripper
// (which resets its string state at each newline) would leak their prose into
// the call scanners. Single-line strings — including `\(...)` interpolations,
// whose leading backslash escapes the paren so the stripper stays inside the
// literal — and comments are handled by the generic stripper.
func stripSwiftCodeText(content string) string {
	return stripCodeLiteralsAndComments(maskSwiftMultilineStrings(content))
}

// swiftTypeName normalizes a matched type reference to the terminal,
// capitalized type segment (`B2MDBuffer.BufferAvailability` ->
// BufferAvailability); optionality markers and generics never make it into
// the match. Empty when the segment is not a plausible type name.
func swiftTypeName(ref string) string {
	name := lastTypeSegment(strings.TrimSuffix(strings.TrimSuffix(stripGenerics(ref), "?"), "!"))
	if !isCapitalized(name) {
		return ""
	}
	return name
}

// swiftReceiverCalls extracts `receiver.method(...)` call sites, accepting the
// optional-chaining (`?.`) and force-unwrap (`!.`) operators, neither of which
// matches the generic receiverCallRe. `self._buffer!.discardReadBytes()`
// yields receiver `_buffer`: the `self` hop cannot match (no `(` after
// `_buffer`), so the scan resumes at the property, which is exactly the name
// the stored-property type map binds.
func swiftReceiverCalls(block string) []receiverCall {
	stripped := stripSwiftCodeText(block)
	var out []receiverCall
	seen := map[string]bool{}
	for _, m := range swiftReceiverCallRe.FindAllStringSubmatchIndex(stripped, -1) {
		receiver := stripped[m[2]:m[3]]
		method := stripped[m[4]:m[5]]
		key := receiver + "." + method
		if receiver == "" || method == "" || seen[key] {
			continue
		}
		args := ""
		open := m[1] - 1
		if close := matchingParen(stripped, open); close > open {
			args = stripped[open+1 : close]
		}
		seen[key] = true
		out = append(out, receiverCall{Receiver: receiver, Method: method, Args: args})
	}
	return out
}

// swiftParameterVarTypes infers parameter -> type from a Swift function
// signature: `func f(buffer: inout ByteBuffer)`, argument labels
// (`remainder buffer:`, `_ buffer:`), attributes, and defaulted parameters.
// The generic parameterVarTypes' colon branch already types the bare
// `name: Type` form; this covers the spellings it cannot.
func swiftParameterVarTypes(signature, name string) map[string]string {
	out := map[string]string{}
	if name == "" {
		return out
	}
	pattern := `\bfunc\s+` + regexp.QuoteMeta(name) + `\s*(?:<[^<>]*>)?\s*\(`
	if name == "init" {
		pattern = `\binit\s*(?:<[^<>]*>)?\s*[?!]?\s*\(`
	}
	loc := regexp.MustCompile(pattern).FindStringIndex(signature)
	if loc == nil {
		return out
	}
	open := loc[1] - 1
	close := matchingParen(signature, open)
	if close < 0 {
		return out
	}
	for _, param := range splitTopLevelCommas(signature[open+1 : close]) {
		param = strings.TrimSpace(strings.SplitN(param, "=", 2)[0])
		m := swiftParamRe.FindStringSubmatch(param)
		if m == nil {
			continue
		}
		typeName := swiftTypeName(m[3])
		if m[2] == "" || m[2] == "_" || typeName == "" {
			continue
		}
		out[m[2]] = typeName
	}
	delete(out, "self")
	return out
}

// swiftLocalVarTypes infers variable -> type from declared-type local
// declarations (`let decoded: ByteBuffer? = nil`) and enum-case pattern
// bindings (`case .available(var buffer):` against the file's
// `case available(ByteBuffer)` declaration). Constructor assignments
// (`var buffer = ByteBuffer()`) are already handled by the generic
// localVarTypes. A name bound to two different types is dropped.
func swiftLocalVarTypes(block string, enumPayloads map[string]string) map[string]string {
	stripped := stripSwiftCodeText(block)
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
	for _, m := range swiftTypedDeclRe.FindAllStringSubmatch(stripped, -1) {
		record(m[1], swiftTypeName(m[2]))
	}
	for _, re := range []*regexp.Regexp{swiftCaseBindingRe, swiftCaseLetBindingRe} {
		for _, m := range re.FindAllStringSubmatch(stripped, -1) {
			record(m[2], enumPayloads[m[1]])
		}
	}
	for name := range conflicted {
		delete(out, name)
	}
	return out
}

// swiftFileTypeInfo collects a Swift file's class-level declared types: stored
// properties (modifier-prefixed explicit annotations and constructor
// initializers) and enum-case associated-value payloads (single payload only,
// labeled or not). Swift files may hold several types; a name bound to two
// different types is dropped rather than guessed.
func swiftFileTypeInfo(content string) swiftFileTypes {
	stripped := stripSwiftCodeText(content)
	info := swiftFileTypes{props: map[string]string{}, enumPayloads: map[string]string{}}
	record := func(into map[string]string, conflicted map[string]bool, name, typeName string) {
		if name == "" || typeName == "" {
			return
		}
		if existing, ok := into[name]; ok && existing != typeName {
			conflicted[name] = true
			return
		}
		into[name] = typeName
	}
	propConflicts := map[string]bool{}
	for _, m := range swiftPropTypedRe.FindAllStringSubmatch(stripped, -1) {
		record(info.props, propConflicts, m[1], swiftTypeName(m[2]))
	}
	for _, m := range swiftPropInitRe.FindAllStringSubmatch(stripped, -1) {
		record(info.props, propConflicts, m[1], m[2])
	}
	for name := range propConflicts {
		delete(info.props, name)
	}
	caseConflicts := map[string]bool{}
	for _, m := range swiftEnumCaseDeclRe.FindAllStringSubmatch(stripped, -1) {
		payload := strings.TrimSpace(m[2])
		if payload == "" || strings.Contains(payload, ",") {
			continue // no payload, or several: positional binding not worth guessing
		}
		if colon := strings.LastIndex(payload, ":"); colon >= 0 {
			payload = payload[colon+1:] // labeled payload: `case available(buffer: ByteBuffer)`
		}
		record(info.enumPayloads, caseConflicts, m[1], swiftTypeName(payload))
	}
	for name := range caseConflicts {
		delete(info.enumPayloads, name)
	}
	return info
}
