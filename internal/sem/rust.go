package sem

import (
	"regexp"
	"strings"
)

var (
	rustReceiverFactoryAssignRe = regexp.MustCompile(`\b(?:let\s+(?:mut\s+)?)?([A-Za-z_]\w*)\s*(?::[^=\n]+)?=\s*([A-Za-z_]\w*)\s*\.\s*([A-Za-z_]\w*)\s*\(`)
	rustAssociatedPathAssignRe  = regexp.MustCompile(`\b(?:let\s+(?:mut\s+)?)?([a-z_]\w*)\s*(?::[^=\n]+)?=\s*(?:(?:[A-Za-z_]\w*)::)*([A-Z]\w*)::[A-Za-z_]\w*(?:\s*::\s*<[^>\n]*>)?\s*\(`)
	rustTurbofishCallRe         = regexp.MustCompile(`\b([a-z_]\w*)\s*::\s*<[^()\n]+>\s*\(`)
)

func rustReceiverFactoryVarTypes(block string, varTypes map[string]string, methodsByContainer map[string]map[string]SymbolRecord, superContainerByID map[string]string, symbolsByShortName map[string][]SymbolRecord, returnTypesBySymbolNameAndFile map[string]map[string][]string, filePath string) map[string]string {
	stripped := stripCodeLiteralsAndComments(block)
	out := map[string]string{}
	conflicted := map[string]bool{}
	for _, m := range rustReceiverFactoryAssignRe.FindAllStringSubmatch(stripped, -1) {
		if len(m) != 4 {
			continue
		}
		name, receiver, factory := m[1], m[2], m[3]
		receiverType := varTypes[receiver]
		if name == "" || receiverType == "" || factory == "" {
			continue
		}
		typeSym, ok := firstTypeLikeNamedPreferFile(symbolsByShortName[receiverType], receiverType, filePath)
		if !ok {
			continue
		}
		method, _, ok := lookupMethodUpChain(typeSym.ID, factory, methodsByContainer, superContainerByID)
		if !ok {
			continue
		}
		types := returnTypesBySymbolNameAndFile[method.Name][method.FilePath]
		if len(types) == 0 {
			continue
		}
		typeName := types[0]
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

func rustAssociatedPathVarTypes(block string) map[string]string {
	stripped := stripCodeLiteralsAndComments(block)
	out := map[string]string{}
	conflicted := map[string]bool{}
	for _, m := range rustAssociatedPathAssignRe.FindAllStringSubmatch(stripped, -1) {
		if len(m) != 3 {
			continue
		}
		name, typeName := m[1], m[2]
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

func rustTurbofishCallIdentifiers(block string) map[string]struct{} {
	stripped := stripCodeLiteralsAndComments(block)
	out := map[string]struct{}{}
	for _, m := range rustTurbofishCallRe.FindAllStringSubmatch(stripped, -1) {
		if len(m) == 2 {
			out[m[1]] = struct{}{}
		}
	}
	return out
}

func rustModulePathCalls(block string) []rustPathCall {
	stripped := stripCodeLiteralsAndComments(block)
	var out []rustPathCall
	seen := map[string]bool{}
	re := regexp.MustCompile(`\b([a-z_]\w*)\s*::\s*([A-Za-z_]\w*)\s*\(`)
	for _, m := range re.FindAllStringSubmatch(stripped, -1) {
		if len(m) != 3 {
			continue
		}
		key := m[1] + "::" + m[2]
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, rustPathCall{Module: m[1], Function: m[2], Detail: key})
	}
	return out
}

type rustPathCall struct {
	Module   string
	Function string
	Detail   string
}

func rustModulePathCallRelations(from SymbolRecord, calls []rustPathCall, importsByName map[string][]string, symbolsByShortName map[string][]SymbolRecord) []RelationRecord {
	if from.Language != "Rust" || len(calls) == 0 {
		return nil
	}
	var relations []RelationRecord
	seen := map[string]bool{}
	for _, call := range calls {
		if len(importsByName[call.Module]) == 0 {
			continue
		}
		for _, to := range symbolsByShortName[call.Function] {
			if to.ID == from.ID || to.Kind == "field" || to.Name != call.Function || to.Language != "Rust" || !localReachable(from, to) {
				continue
			}
			if !importedNameMatchesFile(importsByName[call.Module], from.FilePath, to.FilePath) {
				continue
			}
			key := to.ID + "|" + call.Detail
			if seen[key] {
				continue
			}
			seen[key] = true
			scope := "file"
			if to.FilePath != from.FilePath {
				scope = "module"
			}
			relations = append(relations, RelationRecord{
				RecordType:    "relation",
				FromID:        from.ID,
				ToID:          to.ID,
				Type:          "CALLS",
				Confidence:    0.84,
				Reason:        "Rust module path call resolved through use binding",
				RelationScope: scope,
				Resolution:    "import_resolved",
				TargetKind:    "symbol",
				Evidence: []Evidence{{
					Kind:      "call_site",
					FilePath:  from.FilePath,
					StartLine: from.StartLine,
					EndLine:   from.EndLine,
					Detail:    call.Detail,
				}},
				WarningCodes: []string{},
			})
		}
	}
	return relations
}

type rustImportBinding struct {
	Local  string
	Module string
}

func importedRustNames(content string) map[string][]string {
	imports := map[string][]string{}
	add := func(local, module string) {
		local = strings.TrimSpace(local)
		module = strings.TrimSpace(module)
		if local == "" || module == "" || local == "_" || local == "*" {
			return
		}
		imports[local] = append(imports[local], module)
	}

	stripped := stripCodeLiteralsAndComments(content)
	var stmt strings.Builder
	collecting := false
	for _, line := range strings.Split(stripped, "\n") {
		trimmed := strings.TrimSpace(line)
		if !collecting && !strings.HasPrefix(trimmed, "use ") && !strings.HasPrefix(trimmed, "pub use ") && !strings.HasPrefix(trimmed, "pub(crate) use ") && !strings.HasPrefix(trimmed, "pub(super) use ") && !strings.HasPrefix(trimmed, "pub(self) use ") {
			continue
		}
		if stmt.Len() > 0 {
			stmt.WriteByte(' ')
		}
		stmt.WriteString(trimmed)
		collecting = true
		if !strings.Contains(trimmed, ";") {
			continue
		}
		text := stmt.String()
		stmt.Reset()
		collecting = false
		if idx := strings.Index(text, ";"); idx >= 0 {
			text = text[:idx]
		}
		expr := strings.TrimSpace(text)
		if strings.HasPrefix(expr, "pub(crate) use ") {
			expr = strings.TrimSpace(strings.TrimPrefix(expr, "pub(crate) use "))
		} else if strings.HasPrefix(expr, "pub(super) use ") {
			expr = strings.TrimSpace(strings.TrimPrefix(expr, "pub(super) use "))
		} else if strings.HasPrefix(expr, "pub(self) use ") {
			expr = strings.TrimSpace(strings.TrimPrefix(expr, "pub(self) use "))
		} else if strings.HasPrefix(expr, "pub use ") {
			expr = strings.TrimSpace(strings.TrimPrefix(expr, "pub use "))
		} else if strings.HasPrefix(expr, "use ") {
			expr = strings.TrimSpace(strings.TrimPrefix(expr, "use "))
		}
		for _, binding := range rustUseImportBindings(expr) {
			add(binding.Local, binding.Module)
		}
	}
	for name, modules := range imports {
		imports[name] = uniqueStrings(modules)
	}
	return imports
}

func rustUseImportBindings(expr string) []rustImportBinding {
	expr = strings.TrimSpace(expr)
	if expr == "" || strings.Contains(expr, "*") {
		return nil
	}
	if open := rustTopLevelByte(expr, '{'); open >= 0 {
		close := matchDelimiter([]byte(expr), open)
		if close < 0 {
			return nil
		}
		prefix := strings.TrimSuffix(strings.TrimSpace(expr[:open]), "::")
		var out []rustImportBinding
		for _, item := range splitTopLevelCommas(expr[open+1 : close]) {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			out = append(out, rustUseImportBindings(rustJoinModule(prefix, item))...)
		}
		return out
	}
	local, module := rustUseTerminalBinding(expr)
	if local == "" || module == "" {
		return nil
	}
	return []rustImportBinding{{Local: local, Module: module}}
}

func rustUseTerminalBinding(expr string) (local, module string) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return "", ""
	}
	alias := ""
	if idx := strings.LastIndex(expr, " as "); idx >= 0 {
		alias = strings.TrimSpace(expr[idx+4:])
		expr = strings.TrimSpace(expr[:idx])
	}
	expr = strings.Trim(expr, ": ")
	if expr == "" {
		return "", ""
	}
	parts := strings.Split(expr, "::")
	if len(parts) > 0 && parts[len(parts)-1] == "self" {
		parts = parts[:len(parts)-1]
		expr = strings.Join(parts, "::")
	}
	if expr == "" {
		return alias, expr
	}
	if alias != "" {
		return alias, expr
	}
	parts = strings.Split(expr, "::")
	return parts[len(parts)-1], expr
}

func rustTopLevelByte(s string, want byte) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			if want == '{' && depth == 0 {
				return i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		}
	}
	return -1
}
