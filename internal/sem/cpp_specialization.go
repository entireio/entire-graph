package sem

import "strings"

// C++ template specializations are named after the PRIMARY template, not after
// what they specialize: `template <typename Char, typename... T> struct
// formatter<tuple_join_view<Char, T...>, Char>` is indexed with the name
// `formatter` — the most generic name in fmtlib/fmt, shared by dozens of
// declarations. No name signal can rank it, so the tuple-join formatter (the gold
// fix site of fmtlib/fmt#2457) was reachable only through body text.
//
// The specialization ARGUMENT is what a caller names when they mean this
// declaration ("the tuple_join_view formatter"), so it is added as a searchable
// ALIAS rather than folded into the name. Aliases are additive: the symbol keeps
// its name, its qualified name and — decisively — its compound-v1 ID, which is
// derived from the qualified name. Renaming the symbol to
// `formatter<tuple_join_view>` would have changed that ID for every existing
// specialization in every indexed repository.
//
// Only the FIRST top-level specialization argument is used. C++ partial
// specializations specialize on the leading argument by convention and carry the
// primary template's own parameters (`Char`, `Allocator`, an `enable_if_t` SFINAE
// guard) in the trailing positions, so the leading argument is the discriminating
// one; taking the rest would alias a declaration to its template parameters.
const cppSpecializationMinAliasLength = 3

// cppSpecializationAliases returns the searchable aliases for a C/C++ symbol
// whose declaration specializes a template: the leading specialization argument's
// head identifier, and the compound `Name<Argument>` form a caller may write
// verbatim. It returns nil for anything that is not a specialization — a primary
// template (`struct formatter { ... }`) names no argument.
func cppSpecializationAliases(language, name, signature string) []string {
	if language != "C++" && language != "C" {
		return nil
	}
	if name == "" || signature == "" {
		return nil
	}
	arguments, ok := cppSpecializationArgumentList(name, signature)
	if !ok {
		return nil
	}
	head := cppTypeHeadIdentifier(firstTopLevelArgument(arguments))
	if len(head) < cppSpecializationMinAliasLength || head == name {
		return nil
	}
	return []string{head, name + "<" + head + ">"}
}

// cppSpecializationArgumentList returns the text inside the `<...>` that directly
// follows the declared name in the signature (`formatter<tuple_join_view<Char,
// T...>, Char>` -> `tuple_join_view<Char, T...>, Char`). Angle brackets are
// matched by depth, so a nested argument list is kept whole.
func cppSpecializationArgumentList(name, signature string) (string, bool) {
	for offset := 0; ; {
		index := strings.Index(signature[offset:], name)
		if index < 0 {
			return "", false
		}
		index += offset
		offset = index + 1
		if index > 0 && identifierByte(signature[index-1]) {
			continue // a longer identifier that merely contains the name
		}
		rest := signature[index+len(name):]
		if !strings.HasPrefix(rest, "<") {
			continue
		}
		depth := 0
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case '<':
				depth++
			case '>':
				depth--
				if depth == 0 {
					return rest[1:i], true
				}
			}
		}
		return "", false
	}
}

// firstTopLevelArgument returns the first comma-separated entry of a template
// argument list, splitting only at bracket depth zero so a nested list
// (`tuple_join_view<Char, T...>`) stays intact.
func firstTopLevelArgument(arguments string) string {
	depth := 0
	for i := 0; i < len(arguments); i++ {
		switch arguments[i] {
		case '<', '(', '[':
			depth++
		case '>', ')', ']':
			depth--
		case ',':
			if depth == 0 {
				return strings.TrimSpace(arguments[:i])
			}
		}
	}
	return strings.TrimSpace(arguments)
}

// cppTypeHeadIdentifier reduces a template argument to the identifier it names:
// pointer/reference/cv qualifiers, namespace qualifiers and its own argument list
// are stripped (`const detail::tuple_join_view<Char, T...>&` -> tuple_join_view).
// A non-identifier argument (a value, an expression) yields "".
func cppTypeHeadIdentifier(argument string) string {
	text := strings.TrimSpace(argument)
	if index := strings.IndexAny(text, "<"); index >= 0 {
		text = text[:index]
	}
	text = strings.TrimRight(text, "*&. \t")
	for {
		trimmed := text
		for _, qualifier := range []string{"const ", "volatile ", "typename ", "struct ", "class ", "unsigned ", "signed "} {
			trimmed = strings.TrimPrefix(strings.TrimSpace(trimmed), qualifier)
		}
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == text {
			break
		}
		text = trimmed
	}
	if index := strings.LastIndex(text, "::"); index >= 0 {
		text = text[index+2:]
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	for i := 0; i < len(text); i++ {
		if !identifierByte(text[i]) || text[i] == '.' {
			return ""
		}
	}
	if text[0] >= '0' && text[0] <= '9' {
		return ""
	}
	return text
}

// applyCppSpecializationAliases attaches specialization aliases to a file's
// symbols in place. A member of a specialization inherits its container's
// aliases: the gold edit of fmtlib/fmt#2457 is `formatter<tuple_join_view<...>>`'s
// own parse/format methods, and each of those is likewise indexed under the bare
// name `formatter`. Existing aliases (registration tables) are preserved.
func applyCppSpecializationAliases(symbols []SymbolRecord) {
	aliasesByContainer := map[string][]string{}
	for i := range symbols {
		symbol := &symbols[i]
		if !typeLikeKind(symbol.Kind) {
			continue
		}
		aliases := cppSpecializationAliases(symbol.Language, symbol.Name, symbol.Signature)
		if len(aliases) == 0 {
			continue
		}
		symbol.Aliases = appendUnique(symbol.Aliases, aliases...)
		aliasesByContainer[symbol.ID] = aliases
	}
	if len(aliasesByContainer) == 0 {
		return
	}
	for i := range symbols {
		symbol := &symbols[i]
		if symbol.ContainerID == "" || typeLikeKind(symbol.Kind) {
			continue
		}
		if aliases := aliasesByContainer[symbol.ContainerID]; len(aliases) > 0 {
			symbol.Aliases = appendUnique(symbol.Aliases, aliases...)
		}
	}
}
