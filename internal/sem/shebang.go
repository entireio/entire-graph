package sem

import (
	"path"
	"path/filepath"
	"strings"
)

// shebangSniffLimit bounds how much of a file is examined when routing by
// shebang: enough for the `#!` line plus the null-byte binary guard, without
// pulling large binaries into memory.
const shebangSniffLimit = 1024

// shebangInterpreterExtensions maps a shebang interpreter basename (after env
// indirection and version-suffix stripping) to the extension whose
// languageSpec handles the file, so extensionless executables reuse the exact
// grammar routing of their extension-bearing peers (`libexec/pyenv-which`
// parses like a `.sh` file).
var shebangInterpreterExtensions = map[string]string{
	"sh":   ".sh",
	"bash": ".sh",
	"zsh":  ".zsh",

	"python": ".py",
	"perl":   ".pl",
	"ruby":   ".rb",
	"node":   ".js",
	"nodejs": ".js",
}

// languageForShebang routes a file with no extension (or no recognized
// extension) by its `#!` interpreter line. Only a small prefix of the content
// is examined; a null byte in that prefix marks the file as binary and
// disqualifies it.
func languageForShebang(content string) (languageSpec, bool) {
	chunk := content
	if len(chunk) > shebangSniffLimit {
		chunk = chunk[:shebangSniffLimit]
	}
	if strings.IndexByte(chunk, 0) >= 0 {
		return languageSpec{}, false
	}
	if !strings.HasPrefix(chunk, "#!") {
		return languageSpec{}, false
	}
	line := chunk[2:]
	if idx := strings.IndexAny(line, "\r\n"); idx >= 0 {
		line = line[:idx]
	}
	interpreter := shebangInterpreter(line)
	if interpreter == "" {
		return languageSpec{}, false
	}
	ext, ok := shebangInterpreterExtensions[interpreter]
	if !ok {
		return languageSpec{}, false
	}
	return treeSitterLanguages[ext], true
}

// shebangInterpreter extracts the interpreter basename from the body of a
// shebang line (the text after `#!`): the first field's basename, or — when
// that is `env` — the first following argument that is neither an option nor a
// VAR=value assignment (`/usr/bin/env bash`, `/usr/bin/env -S python3`).
// Trailing version suffixes are stripped so python3 / python2.7 resolve to
// python.
func shebangInterpreter(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	name := path.Base(filepath.ToSlash(fields[0]))
	if name == "env" {
		name = ""
		for _, arg := range fields[1:] {
			if strings.HasPrefix(arg, "-") || strings.Contains(arg, "=") {
				continue
			}
			name = path.Base(filepath.ToSlash(arg))
			break
		}
	}
	return strings.TrimRight(strings.ToLower(name), "0123456789.")
}

// shebangRoutable reports whether a file the path alone cannot classify routes
// to a supported language via its shebang line, reading only a bounded prefix
// of the content (so large binaries are never fully read).
func shebangRoutable(readPrefix prefixReader, path string) bool {
	if readPrefix == nil {
		return false
	}
	prefix, ok := readPrefix(path, shebangSniffLimit)
	if !ok {
		return false
	}
	_, ok = languageForShebang(prefix)
	return ok
}

// languageForContent resolves a file's language from its path (extension or
// well-known filename) first, then falls back to shebang routing for files the
// path alone cannot classify (extensionless executables such as pyenv's
// libexec/* scripts).
func languageForContent(filePath, content string) (languageSpec, bool) {
	if spec, ok := languageForPath(filePath); ok {
		return spec, true
	}
	return languageForShebang(content)
}
