package sem

import (
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"
)

const searchVerifyGoNativeMaxProbes = 8

// searchVerifyGoApplies admits Go source directly. Native build inputs require
// positive package evidence: a readable, same-directory non-test Go file, plus
// a real import of C when cgo compiles that input. Neighboring Go files never
// license Python/JS or arbitrary embedded assets as Go verification subjects.
// This consumes only the existing inventory and bounded content reader. It does
// not enumerate directories, run go list, or evaluate platform/build constraints.
// SWIG-generated Go and arbitrary embedded assets are not inferred here.
func searchVerifyGoApplies(filePath string, evidence *searchVerifyEvidence) (applies bool) {
	ext := path.Ext(filePath)
	if ext == ".go" {
		return true
	}
	needsCgo, header := false, false
	switch ext {
	case ".s", ".syso":
	case ".h", ".hh", ".hpp", ".hxx":
		header = true
	case ".c", ".cc", ".cpp", ".cxx", ".m", ".f", ".F", ".for", ".f90", ".S", ".sx":
		needsCgo = true
	default:
		return false
	}
	if evidence == nil {
		return false
	}
	if cached, ok := evidence.goNative[filePath]; ok {
		return cached
	}
	defer func() {
		if evidence.goNative == nil {
			evidence.goNative = make(map[string]bool)
		}
		evidence.goNative[filePath] = applies
	}()
	dir := path.Dir(filePath)
	var goFiles, assemblyFiles []string
	for _, candidate := range evidence.files {
		if path.Dir(candidate) != dir || strings.HasPrefix(path.Base(candidate), ".") || strings.HasPrefix(path.Base(candidate), "_") {
			continue
		}
		switch path.Ext(candidate) {
		case ".go":
			if !strings.HasSuffix(candidate, "_test.go") {
				goFiles = append(goFiles, candidate)
			}
		case ".s":
			assemblyFiles = append(assemblyFiles, candidate)
		}
	}
	// Stable read order makes the shared read budget and provenance deterministic.
	sort.Strings(goFiles)
	sort.Strings(assemblyFiles)
	probes := 0
	readNative := func(candidate string) (string, bool) {
		if probes >= searchVerifyGoNativeMaxProbes {
			return "", false
		}
		probes++
		// Leave manifest/fallback capacity even when earlier derivations have
		// nearly exhausted the shared reader. Cached evidence costs no new read.
		if _, cached := evidence.cache[candidate]; !cached && evidence.reads >= searchVerifyMaxReads-searchVerifyGoNativeMaxProbes {
			return "", false
		}
		return evidence.file(candidate)
	}
	assemblyChecked := false
	for _, candidate := range goFiles {
		if probes >= searchVerifyGoNativeMaxProbes {
			break
		}
		content, ok := readNative(candidate)
		if !ok {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), candidate, content, parser.ImportsOnly)
		if err != nil || parsed.Name.Name == "_" {
			continue
		}
		if !needsCgo && !header {
			return true
		}
		for _, imp := range parsed.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err == nil && importPath == "C" && imp.Name == nil {
				return true
			}
		}
		if header && !assemblyChecked {
			// Once a Go package is established, an assembly witness suffices for
			// a header. Do not scan every Go source for cgo first.
			assemblyChecked = true
			for _, assembly := range assemblyFiles {
				if probes >= searchVerifyGoNativeMaxProbes {
					break
				}
				if _, ok := readNative(assembly); ok {
					return true
				}
			}
		}
	}
	return false
}
