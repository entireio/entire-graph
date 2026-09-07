package sem

// The tree-sitter-protobuf grammar bundled by go-tree-sitter is a proto3-only
// grammar: it requires a `syntax` declaration, accepts only the "proto3"
// literal, and rejects the proto2 `required`/`optional` labels and `group`
// declarations. Proto2 remains common in long-lived production repositories,
// where omitting `syntax` means proto2 by specification.
//
// prepareProtocolBuffersParseSource builds a position-preserving proto3 view
// for tree-sitter while keeping a parallel source view containing the original
// bytes. The parser consumes the first string; entity extraction consumes the
// second. Replacements therefore improve grammar compatibility without
// rewriting signatures, body hashes, or source locations. When a synthetic
// syntax line is needed, both views receive it and callers subtract lineOffset
// from extracted locations.

import (
	"regexp"
	"strings"
)

const protobufSyntheticSyntax = "syntax = \"proto3\";\n"

// Proto field numbers are positive integer literals. Keep the alternatives
// lexical (rather than parsing a captured token after the match) so malformed
// group declarations are left untouched for tree-sitter to report. Each
// non-decimal alternative requires at least one non-zero digit, excluding
// zero-valued spellings such as 00 and 0x0.
const protobufPositiveIntLitPattern = `(?:[1-9][0-9]*|0[0-7]*[1-7][0-7]*|0[xX][0-9A-Fa-f]*[1-9A-Fa-f][0-9A-Fa-f]*)`

var (
	protobufFieldLabelPattern = regexp.MustCompile(`\b(?:required|optional)\b`)
	protobufGroupPattern      = regexp.MustCompile(`\b(?:required|optional|repeated)[\t\n\r\f ]+group[\t\n\r\f ]+([A-Z][A-Za-z0-9_]*)[\t\n\r\f ]*=[\t\n\r\f ]*` + protobufPositiveIntLitPattern + `[\t\n\r\f ]*\{`)
)

type protobufSyntaxKind uint8

const (
	protobufSyntaxOmitted protobufSyntaxKind = iota
	protobufSyntaxProto2
	protobufSyntaxProto3
	protobufSyntaxInvalid
)

func prepareProtocolBuffersParseSource(content string) (parseSource, entitySource string, lineOffset int) {
	syntaxKind, literalStart, literalEnd := protocolBuffersSyntax(content)
	entitySource = content
	if syntaxKind == protobufSyntaxOmitted {
		// Keep a UTF-8 BOM at byte zero. The parse view masks it below, while
		// inserting the synthetic declaration after it avoids moving the BOM into
		// the middle of the source and keeps every original byte in order.
		if strings.HasPrefix(content, "\uFEFF") {
			entitySource = content[:len("\uFEFF")] + protobufSyntheticSyntax + content[len("\uFEFF"):]
		} else {
			entitySource = protobufSyntheticSyntax + content
		}
		lineOffset = 1
	}

	parseBytes := []byte(entitySource)
	maskProtocolBuffersBOM(parseBytes)
	// The bundled proto3 grammar accepts only the canonical double-quoted
	// spelling. Protobuf itself permits either quote style, so normalize every
	// valid declaration in the parse view. Both spellings are eight bytes wide;
	// entitySource retains the authored literal for signatures and body hashes.
	if syntaxKind == protobufSyntaxProto2 || syntaxKind == protobufSyntaxProto3 {
		copy(parseBytes[literalStart:literalEnd], `"proto3"`)
	}

	// The bundled grammar does not accept leading underscores as the first
	// byte of an identifier, although protobuf permits them in field names.
	// Keep the authored source untouched and change only the parse view.  The
	// narrow `=` lookahead avoids masking message/type names and leaves malformed
	// declarations visible to tree-sitter.
	structure := stripCodeLiteralsAndComments(entitySource)
	maskProtocolBuffersLeadingUnderscoreFields(parseBytes, structure)
	maskProtocolBuffersReservedStrings(parseBytes, entitySource)
	// Apart from the quote-style canonicalization above, explicit proto3 must be
	// parsed as authored. In particular, masking a proto2-only `required` field
	// or group in such a file would hide a genuine syntax error and violate the
	// provider's partial-failure contract.
	legacySyntax := syntaxKind == protobufSyntaxProto2 || syntaxKind == protobufSyntaxOmitted
	if !legacySyntax {
		// Extension bodies are not part of the provider's protobuf entity/relation
		// contract: fields and extendees are intentionally not emitted.  The
		// bundled grammar does not accept all valid extension declarations, so
		// remove only a narrowly validated, field-only declaration from the parse
		// view.  Unknown or malformed forms remain visible to preserve partial
		// failure reporting.
		maskProtocolBuffersExtensions(parseBytes, entitySource)
		return string(parseBytes), entitySource, lineOffset
	}

	// Proto2 field labels are all eight bytes long, as is `repeated`, so the
	// grammar-compatible substitution leaves every later byte at its original
	// offset. The original label remains visible in entitySource.
	for _, bounds := range protobufFieldLabelPattern.FindAllStringIndex(structure, -1) {
		copy(parseBytes[bounds[0]:bounds[1]], "repeated")
	}

	// A proto2 group is structurally a nested message with an implicit field.
	// Present the declaration as `message <Name> {` while preserving the name
	// and opening-brace offsets. Entity extraction then emits the original group
	// declaration as a message-shaped container rather than dropping the whole
	// file as unparseable.
	for _, bounds := range protobufGroupPattern.FindAllStringSubmatchIndex(structure, -1) {
		start, end := bounds[0], bounds[1]
		nameStart, nameEnd := bounds[2], bounds[3]
		braceRelative := strings.LastIndexByte(structure[start:end], '{')
		if braceRelative < 0 || start+len("message") > nameStart {
			continue
		}
		brace := start + braceRelative
		maskBytes(parseBytes, start, nameStart)
		copy(parseBytes[start:start+len("message")], "message")
		maskBytes(parseBytes, nameEnd, brace)
	}
	// Run after legacy label/group substitutions so a masked extension cannot
	// receive a compatibility label rewrite from the authored source.
	maskProtocolBuffersExtensions(parseBytes, entitySource)

	return string(parseBytes), entitySource, lineOffset
}

// maskProtocolBuffersLeadingUnderscoreFields changes only a leading underscore
// in an identifier that is immediately followed by a field assignment.  The
// structure argument has literals and comments blanked but retains byte
// offsets, so names in comments/default strings cannot be mistaken for fields.
func maskProtocolBuffersLeadingUnderscoreFields(parseBytes []byte, structure string) {
	for index := 0; index < len(structure); index++ {
		if structure[index] != '_' || (index > 0 && isProtocolBuffersIdentifierByte(structure[index-1])) {
			continue
		}
		if index+1 >= len(structure) || !isProtocolBuffersIdentifierByte(structure[index+1]) {
			continue
		}
		if protobufPreviousWord(structure, index) == "group" {
			// The token after `group` is the group type name, not a field
			// name.  Leaving it untouched keeps invalid group declarations
			// visible to the grammar.
			continue
		}
		end := index + 2
		for end < len(structure) && isProtocolBuffersIdentifierByte(structure[end]) {
			end++
		}
		assignment := end
		for assignment < len(structure) {
			switch structure[assignment] {
			case ' ', '\t', '\n', '\r', '\f':
				assignment++
				continue
			}
			break
		}
		if assignment < len(structure) && structure[assignment] == '=' {
			parseBytes[index] = 'x'
		}
		index = end - 1
	}
}

func protobufPreviousWord(structure string, index int) string {
	end := index
	for end > 0 {
		switch structure[end-1] {
		case ' ', '\t', '\n', '\r', '\f':
			end--
			continue
		}
		break
	}
	start := end
	for start > 0 && isProtocolBuffersIdentifierByte(structure[start-1]) {
		start--
	}
	return structure[start:end]
}

// maskProtocolBuffersReservedStrings removes only the delimiters around valid
// identifier-shaped names in message/enum `reserved` declarations.  The
// grammar accepts the bare field-name form but rejects protobuf's quoted form.
// Unterminated or non-identifier strings are left intact so malformed input
// still reports a parse error instead of being made to look valid.
func maskProtocolBuffersReservedStrings(parseBytes []byte, content string) {
	depth := 0
	statementStart := true
	reserved := false
	for index := 0; index < len(content); {
		if content[index] == '/' && index+1 < len(content) {
			switch content[index+1] {
			case '/':
				index += 2
				for index < len(content) && content[index] != '\n' && content[index] != '\r' {
					index++
				}
				continue
			case '*':
				index += 2
				for index+1 < len(content) && !(content[index] == '*' && content[index+1] == '/') {
					index++
				}
				if index+1 < len(content) {
					index += 2
				} else {
					index = len(content)
				}
				continue
			}
		}

		switch content[index] {
		case ' ', '\t', '\n', '\r', '\f':
			index++
			continue
		case '"', '\'':
			end := protocolBuffersStringEnd(content, index)
			if end < 0 {
				return
			}
			if reserved && protocolBuffersReservedName(content[index+1:end]) {
				parseBytes[index] = ' '
				parseBytes[end] = ' '
				if content[index+1] == '_' {
					// The same grammar limitation applies to a quoted reserved
					// name once its delimiters have been removed.
					parseBytes[index+1] = 'x'
				}
			}
			statementStart = false
			index = end + 1
			continue
		case '{':
			depth++
			reserved = false
			statementStart = true
			index++
			continue
		case '}':
			if depth > 0 {
				depth--
			}
			reserved = false
			statementStart = true
			index++
			continue
		case ';':
			reserved = false
			statementStart = true
			index++
			continue
		}

		if isProtocolBuffersIdentifierStartByte(content[index]) {
			end := index + 1
			for end < len(content) && isProtocolBuffersIdentifierByte(content[end]) {
				end++
			}
			if depth > 0 && statementStart && content[index:end] == "reserved" {
				reserved = true
			} else {
				statementStart = false
			}
			index = end
			continue
		}

		statementStart = false
		index++
	}
}

func protocolBuffersStringEnd(content string, start int) int {
	quote := content[start]
	for index := start + 1; index < len(content); index++ {
		if content[index] == '\\' {
			index++
			continue
		}
		if content[index] == quote {
			return index
		}
		if content[index] == '\n' || content[index] == '\r' {
			return -1
		}
	}
	return -1
}

func protocolBuffersReservedName(name string) bool {
	if name == "" || !isProtocolBuffersIdentifierStartByte(name[0]) {
		return false
	}
	for index := 1; index < len(name); index++ {
		if !isProtocolBuffersIdentifierByte(name[index]) {
			return false
		}
	}
	return true
}

func isProtocolBuffersIdentifierStartByte(ch byte) bool {
	return ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

// maskProtocolBuffersExtensions blanks complete, narrowly supported extension
// declarations in the parse view.  It deliberately does not attempt to
// recover arbitrary protobuf grammar: the body must contain one or more plain
// field declarations, without groups, options, or other nested constructs.
// This keeps malformed/broader declarations visible as parser errors while
// allowing the surrounding message/service/RPC entities to be extracted.
func maskProtocolBuffersExtensions(parseBytes []byte, content string) {
	containers := make([]string, 0, 4)
	pendingContainer := ""
	pendingContainerName := false
	for index := 0; index < len(content); {
		switch content[index] {
		case '/':
			if index+1 < len(content) && content[index+1] == '/' {
				index += 2
				for index < len(content) && content[index] != '\n' && content[index] != '\r' {
					index++
				}
				continue
			}
			if index+1 < len(content) && content[index+1] == '*' {
				index += 2
				for index+1 < len(content) && !(content[index] == '*' && content[index+1] == '/') {
					index++
				}
				if index+1 < len(content) {
					index += 2
				} else {
					return
				}
				continue
			}
		case '"', '\'':
			end := protocolBuffersStringEnd(content, index)
			if end < 0 {
				return
			}
			index = end + 1
			continue
		case '{':
			containers = append(containers, pendingContainer)
			pendingContainer = ""
			pendingContainerName = false
			index++
			continue
		case '}':
			if len(containers) > 0 {
				containers = containers[:len(containers)-1]
			}
			pendingContainer = ""
			pendingContainerName = false
			index++
			continue
		case ';':
			pendingContainer = ""
			pendingContainerName = false
			index++
			continue
		}
		if !isProtocolBuffersIdentifierStartByte(content[index]) {
			index++
			continue
		}
		end := index + 1
		for end < len(content) && isProtocolBuffersIdentifierByte(content[end]) {
			end++
		}
		word := content[index:end]
		if word == "extend" && (len(containers) == 0 || containers[len(containers)-1] == "message") {
			if extensionEnd, ok := protocolBuffersExtensionEnd(content, index); ok {
				maskBytes(parseBytes, index, extensionEnd)
				index = extensionEnd
				continue
			}
		}
		if len(containers) == 0 || containers[len(containers)-1] == "message" {
			if container, ok := protocolBuffersContainerDeclaration(content, index, end); ok {
				pendingContainer = container
				pendingContainerName = true
			} else if pendingContainerName {
				// The only token permitted between a recognized container keyword
				// and its opening brace is the declaration name.
				pendingContainerName = false
			} else if pendingContainer != "" {
				pendingContainer = ""
			}
		} else if pendingContainerName {
			pendingContainerName = false
		} else if pendingContainer != "" {
			pendingContainer = ""
		}
		index = end
	}
}

func protocolBuffersContainerDeclaration(content string, start, end int) (string, bool) {
	word := content[start:end]
	if word != "message" && word != "enum" && word != "service" {
		return "", false
	}
	index := skipProtocolBuffersTrivia(content, end)
	index, ok := protocolBuffersIdentifierEnd(content, index)
	if !ok {
		return "", false
	}
	index = skipProtocolBuffersTrivia(content, index)
	if index >= len(content) || content[index] != '{' {
		return "", false
	}
	return word, true
}

func protocolBuffersExtensionEnd(content string, start int) (int, bool) {
	index := start + len("extend")
	index = skipProtocolBuffersTrivia(content, index)
	if index >= len(content) {
		return 0, false
	}
	if content[index] == '.' {
		index = skipProtocolBuffersTrivia(content, index+1)
	}
	var ok bool
	index, ok = protocolBuffersQualifiedNameEnd(content, index)
	if !ok {
		return 0, false
	}
	index = skipProtocolBuffersTrivia(content, index)
	if index >= len(content) || content[index] != '{' {
		return 0, false
	}
	index++
	fieldCount := 0
	for {
		index = skipProtocolBuffersTrivia(content, index)
		if index >= len(content) {
			return 0, false
		}
		if content[index] == '}' {
			if fieldCount == 0 {
				return 0, false
			}
			return index + 1, true
		}
		var fieldOK bool
		index, fieldOK = protocolBuffersExtensionFieldEnd(content, index)
		if !fieldOK {
			return 0, false
		}
		fieldCount++
	}
}

func protocolBuffersQualifiedNameEnd(content string, index int) (int, bool) {
	if index >= len(content) || !isProtocolBuffersIdentifierStartByte(content[index]) {
		return 0, false
	}
	index++
	for index < len(content) && isProtocolBuffersIdentifierByte(content[index]) {
		index++
	}
	for {
		point := skipProtocolBuffersTrivia(content, index)
		if point >= len(content) || content[point] != '.' {
			return point, true
		}
		point = skipProtocolBuffersTrivia(content, point+1)
		if point >= len(content) || !isProtocolBuffersIdentifierStartByte(content[point]) {
			return 0, false
		}
		index = point + 1
		for index < len(content) && isProtocolBuffersIdentifierByte(content[index]) {
			index++
		}
	}
}

func protocolBuffersExtensionFieldEnd(content string, index int) (int, bool) {
	index = skipProtocolBuffersTrivia(content, index)
	if index >= len(content) {
		return 0, false
	}
	// Labels are optional for the narrow proto3-compatible form, but when
	// present only protobuf field labels are admitted.
	if end, ok := protocolBuffersWordEnd(content, index); ok {
		word := content[index:end]
		if word == "required" || word == "optional" || word == "repeated" {
			index = skipProtocolBuffersTrivia(content, end)
		}
	}
	var ok bool
	index, ok = protocolBuffersQualifiedNameEnd(content, index)
	if !ok {
		return 0, false
	}
	index = skipProtocolBuffersTrivia(content, index)
	index, ok = protocolBuffersIdentifierEnd(content, index)
	if !ok {
		return 0, false
	}
	index = skipProtocolBuffersTrivia(content, index)
	if index >= len(content) || content[index] != '=' {
		return 0, false
	}
	index = skipProtocolBuffersTrivia(content, index+1)
	start := index
	for index < len(content) && content[index] >= '0' && content[index] <= '9' {
		index++
	}
	if start == index || !protocolBuffersValidExtensionFieldNumber(content[start:index]) {
		return 0, false
	}
	index = skipProtocolBuffersTrivia(content, index)
	// Options require grammar validation beyond this narrow scanner.  Leave
	// such declarations untouched rather than hiding malformed option syntax.
	if index < len(content) && content[index] == '[' {
		return 0, false
	}
	if index >= len(content) || content[index] != ';' {
		return 0, false
	}
	return index + 1, true
}

func protocolBuffersValidExtensionFieldNumber(number string) bool {
	value := 0
	for index := 0; index < len(number); index++ {
		digit := int(number[index] - '0')
		if value > (536870911-digit)/10 {
			return false
		}
		value = value*10 + digit
	}
	return value > 0 && (value < 19000 || value > 19999)
}

func protocolBuffersWordEnd(content string, index int) (int, bool) {
	if index >= len(content) || !isProtocolBuffersIdentifierStartByte(content[index]) {
		return 0, false
	}
	end := index + 1
	for end < len(content) && isProtocolBuffersIdentifierByte(content[end]) {
		end++
	}
	return end, true
}

func protocolBuffersIdentifierEnd(content string, index int) (int, bool) {
	if index >= len(content) || !isProtocolBuffersIdentifierStartByte(content[index]) {
		return 0, false
	}
	end := index + 1
	for end < len(content) && isProtocolBuffersIdentifierByte(content[end]) {
		end++
	}
	return end, true
}

// protocolBuffersSyntax reads the leading syntax declaration using protobuf
// lexical trivia rules. Comments and newlines may appear between every token;
// a UTF-8 BOM is accepted at byte zero. A malformed leading `syntax` statement
// is distinguished from an omitted declaration so callers do not accidentally
// treat malformed proto3 as legacy proto2 and mask its errors.
func protocolBuffersSyntax(content string) (protobufSyntaxKind, int, int) {
	index := 0
	if strings.HasPrefix(content, "\uFEFF") {
		index = len("\uFEFF")
	}
	index = skipProtocolBuffersTrivia(content, index)
	identifierStart := index
	for index < len(content) && isProtocolBuffersIdentifierByte(content[index]) {
		index++
	}
	if content[identifierStart:index] != "syntax" {
		return protobufSyntaxOmitted, 0, 0
	}

	index = skipProtocolBuffersTrivia(content, index)
	if index >= len(content) || content[index] != '=' {
		return protobufSyntaxInvalid, 0, 0
	}
	index = skipProtocolBuffersTrivia(content, index+1)
	if index >= len(content) || (content[index] != '"' && content[index] != '\'') {
		return protobufSyntaxInvalid, 0, 0
	}
	literalStart := index
	quote := content[index]
	index++
	valueStart := index
	for index < len(content) && content[index] != quote {
		if content[index] == '\\' && index+1 < len(content) {
			index += 2
			continue
		}
		index++
	}
	if index >= len(content) {
		return protobufSyntaxInvalid, 0, 0
	}
	value := content[valueStart:index]
	literalEnd := index + 1
	switch value {
	case "proto2":
		return protobufSyntaxProto2, literalStart, literalEnd
	case "proto3":
		return protobufSyntaxProto3, literalStart, literalEnd
	default:
		return protobufSyntaxInvalid, literalStart, literalEnd
	}
}

func skipProtocolBuffersTrivia(content string, index int) int {
	for index < len(content) {
		switch content[index] {
		case ' ', '\t', '\n', '\r', '\f':
			index++
			continue
		case '/':
			if index+1 >= len(content) {
				return index
			}
			switch content[index+1] {
			case '/':
				index += 2
				for index < len(content) && content[index] != '\n' && content[index] != '\r' {
					index++
				}
				continue
			case '*':
				index += 2
				for index+1 < len(content) && !(content[index] == '*' && content[index+1] == '/') {
					index++
				}
				if index+1 >= len(content) {
					return len(content)
				}
				index += 2
				continue
			}
		}
		return index
	}
	return index
}

func isProtocolBuffersIdentifierByte(ch byte) bool {
	return ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9'
}

func maskProtocolBuffersBOM(content []byte) {
	if len(content) >= len("\uFEFF") && string(content[:len("\uFEFF")]) == "\uFEFF" {
		for index := 0; index < len("\uFEFF"); index++ {
			content[index] = ' '
		}
	}
}
