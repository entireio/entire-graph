package sem

import (
	"testing"
)

func TestLanguageForShebang(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
		ok      bool
	}{
		{"env bash", "#!/usr/bin/env bash\nset -e\n", "Bash", true},
		{"direct bash", "#!/bin/bash\n", "Bash", true},
		{"posix sh", "#!/bin/sh\n", "Bash", true},
		{"zsh", "#!/usr/bin/env zsh\n", "Zsh", true},
		{"python", "#!/usr/bin/python\n", "Python", true},
		{"versioned python", "#!/usr/bin/env python3\n", "Python", true},
		{"dotted versioned python", "#!/usr/bin/python2.7\n", "Python", true},
		{"perl with flag", "#!/usr/bin/perl -w\n", "Perl", true},
		{"ruby", "#!/usr/bin/env ruby\n", "Ruby", true},
		{"node", "#!/usr/bin/env node\n", "JavaScript", true},
		{"env split-string", "#!/usr/bin/env -S node --harmony\n", "JavaScript", true},
		{"env with assignment", "#!/usr/bin/env FOO=bar bash\n", "Bash", true},
		{"crlf line ending", "#!/usr/bin/env bash\r\necho hi\r\n", "Bash", true},
		{"no trailing newline", "#!/bin/bash", "Bash", true},
		{"unknown interpreter", "#!/usr/bin/env fish\n", "", false},
		{"bare env", "#!/usr/bin/env\n", "", false},
		{"no shebang", "echo hello\n", "", false},
		{"empty", "", "", false},
		{"binary with shebang prefix", "#!/usr/bin/env bash\n\x00\x01\x02", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, ok := languageForShebang(tc.content)
			if ok != tc.ok {
				t.Fatalf("languageForShebang(%q) ok = %v, want %v", tc.content, ok, tc.ok)
			}
			if spec.language != tc.want {
				t.Fatalf("languageForShebang(%q) language = %q, want %q", tc.content, spec.language, tc.want)
			}
			if tc.ok && spec.grammar == nil {
				t.Fatalf("languageForShebang(%q) returned spec without grammar", tc.content)
			}
		})
	}
}

// TestSnapshotRoutesExtensionlessShebangScripts is the regression test for the
// pyenv-style discovery gap: extensionless executables (libexec/pyenv-which and
// friends) were dropped at the parse stage because language routing was
// extension-only, so repositories whose commands are bare shebang scripts
// produced zero file records for them.
func TestSnapshotRoutesExtensionlessShebangScripts(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "libexec/mytool-which", `#!/usr/bin/env bash
set -e

resolve_link() {
  readlink "$1"
}

can_use_cache() {
  [ -d "$CACHE_DIR" ]
}

resolve_link "$0"
`)
	writeFile(t, repo, "bin/pytool", `#!/usr/bin/env python3
def resolve_target(name):
    return name.strip()

if __name__ == "__main__":
    resolve_target("x")
`)
	// Binary file with no extension: the null-byte guard must keep it out.
	writeFile(t, repo, "bin/blob", "\x7fELF\x00\x00\x00\x01binary\x00payload")
	// Skip rules still apply: vendored trees stay skipped even when their
	// files are legitimate shebang scripts.
	writeFile(t, repo, "node_modules/pkg/cli", "#!/usr/bin/env bash\nvendored_fn() { true; }\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	filesByPath := map[string]FileRecord{}
	for _, file := range snapshot.Files {
		filesByPath[file.Path] = file
	}

	shellFile, ok := filesByPath["libexec/mytool-which"]
	if !ok {
		t.Fatalf("extensionless bash script missing from snapshot files: %#v", snapshot.Files)
	}
	if shellFile.Language != "Bash" {
		t.Fatalf("libexec/mytool-which language = %q, want Bash", shellFile.Language)
	}
	for _, want := range []string{"resolve_link", "can_use_cache"} {
		if !fileHasSymbolNamed(snapshot, "libexec/mytool-which", want) {
			t.Fatalf("missing bash function symbol %q for libexec/mytool-which; symbols: %v", want, symbolNamesForPath(snapshot, "libexec/mytool-which"))
		}
	}

	pyFile, ok := filesByPath["bin/pytool"]
	if !ok {
		t.Fatalf("extensionless python script missing from snapshot files: %#v", snapshot.Files)
	}
	if pyFile.Language != "Python" {
		t.Fatalf("bin/pytool language = %q, want Python", pyFile.Language)
	}
	if !fileHasSymbolNamed(snapshot, "bin/pytool", "resolve_target") {
		t.Fatalf("missing python function symbol resolve_target for bin/pytool; symbols: %v", symbolNamesForPath(snapshot, "bin/pytool"))
	}

	if _, ok := filesByPath["bin/blob"]; ok {
		t.Fatalf("binary file bin/blob must not be inventoried")
	}
	if _, ok := filesByPath["node_modules/pkg/cli"]; ok {
		t.Fatalf("vendored node_modules script must stay skipped")
	}
}

func fileHasSymbolNamed(snapshot ProviderSnapshot, path, name string) bool {
	for _, symbol := range snapshot.Symbols {
		if symbol.FilePath == path && symbol.Name == name {
			return true
		}
	}
	return false
}

func symbolNamesForPath(snapshot ProviderSnapshot, path string) []string {
	var names []string
	for _, symbol := range snapshot.Symbols {
		if symbol.FilePath == path {
			names = append(names, symbol.Name)
		}
	}
	return names
}
