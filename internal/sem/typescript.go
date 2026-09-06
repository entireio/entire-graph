package sem

import (
	"regexp"
	"strings"
)

// TypeScript-specific receiver typing. The generic localVarTypes scanner only
// understands constructor assignments, while TypeScript projects often route
// important receiver calls through explicit annotations or DI field
// initializers (`const router: Router = ...`, `private router = inject(Router)`).
// These helpers are intentionally conservative: only capitalized short type
// names are kept, and conflicting bindings are dropped by the caller.

var (
	typeScriptTypedLocalRe   = regexp.MustCompile(`\b(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*[!?]?\s*:\s*([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)?(?:<[^>\n]*>)?)`)
	typeScriptInjectLocalRe  = regexp.MustCompile(`\b(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*inject\s*(?:<[^>\n]*>)?\(\s*([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)?)\b`)
	typeScriptCtorPropertyRe = regexp.MustCompile(`\b(?:(?:public|protected|private|readonly|override)\s+)+([A-Za-z_$][\w$]*)\s*[!?]?\s*:\s*([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)?(?:<[^>\n]*>)?)`)
)

func typeScriptTypeName(ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "readonly ")
	ref = strings.TrimPrefix(ref, "?")
	ref = strings.TrimSuffix(ref, "?")
	ref = strings.TrimSuffix(ref, "!")
	ref = stripGenerics(ref)
	name := lastTypeSegment(ref)
	if !isCapitalized(name) {
		return ""
	}
	return name
}

func typeScriptLocalVarTypes(block string) map[string]string {
	stripped := stripCodeLiteralsAndComments(block)
	out := map[string]string{}
	conflicted := map[string]bool{}
	record := func(name, typeName string) {
		typeName = typeScriptTypeName(typeName)
		if name == "" || typeName == "" {
			return
		}
		if existing, ok := out[name]; ok && existing != typeName {
			conflicted[name] = true
			return
		}
		out[name] = typeName
	}
	for _, m := range typeScriptTypedLocalRe.FindAllStringSubmatch(stripped, -1) {
		record(m[1], m[2])
	}
	for _, m := range typeScriptInjectLocalRe.FindAllStringSubmatch(stripped, -1) {
		record(m[1], m[2])
	}
	for name := range conflicted {
		delete(out, name)
	}
	return out
}

// typeScriptPropertyTypes infers field -> type from parser-confirmed class
// field symbols plus constructor parameter properties. Restricting initializer
// parsing to emitted field line ranges avoids classifying method-local
// `const x = inject(Foo)` bindings as class properties.
func typeScriptPropertyTypes(content string, fields []SymbolRecord) map[string]string {
	stripped := stripCodeLiteralsAndComments(content)
	lines := strings.Split(stripped, "\n")
	out := map[string]string{}
	conflicted := map[string]bool{}
	record := func(name, typeName string) {
		typeName = typeScriptTypeName(typeName)
		if name == "" || typeName == "" {
			return
		}
		if existing, ok := out[name]; ok && existing != typeName {
			conflicted[name] = true
			return
		}
		out[name] = typeName
	}
	for _, field := range fields {
		if field.Kind != "field" || field.Language != "TypeScript" {
			continue
		}
		if field.Signature != "" && strings.Contains(field.Signature, ":") {
			if _, typ, ok := strings.Cut(field.Signature, ":"); ok {
				record(field.Name, typ)
			}
		}
		start := maxInt(1, field.StartLine)
		end := maxInt(start, field.EndLine)
		if start > len(lines) {
			continue
		}
		if end > len(lines) {
			end = len(lines)
		}
		block := strings.Join(lines[start-1:end], "\n")
		escaped := regexp.QuoteMeta(field.Name)
		typed := regexp.MustCompile(`\b` + escaped + `\s*[!?]?\s*:\s*([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)?(?:<[^>\n]*>)?)`)
		if m := typed.FindStringSubmatch(block); m != nil {
			record(field.Name, m[1])
		}
		inject := regexp.MustCompile(`\b` + escaped + `\s*[!?]?\s*=\s*inject\s*(?:<[^>\n]*>)?\(\s*([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)?)\b`)
		if m := inject.FindStringSubmatch(block); m != nil {
			record(field.Name, m[1])
		}
		ctor := regexp.MustCompile(`\b` + escaped + `\s*[!?]?\s*=\s*new\s+([A-Z][A-Za-z0-9_$]*)\s*\(`)
		if m := ctor.FindStringSubmatch(block); m != nil {
			record(field.Name, m[1])
		}
	}
	for _, m := range typeScriptCtorPropertyRe.FindAllStringSubmatch(stripped, -1) {
		record(m[1], m[2])
	}
	for name := range conflicted {
		delete(out, name)
	}
	return out
}
