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

	// Apart from the quote-style canonicalization above, explicit proto3 must be
	// parsed as authored. In particular, masking a proto2-only `required` field
	// or group in such a file would hide a genuine syntax error and violate the
	// provider's partial-failure contract.
	legacySyntax := syntaxKind == protobufSyntaxProto2 || syntaxKind == protobufSyntaxOmitted
	if !legacySyntax {
		return string(parseBytes), entitySource, lineOffset
	}

	structure := stripCodeLiteralsAndComments(entitySource)
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

	return string(parseBytes), entitySource, lineOffset
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
