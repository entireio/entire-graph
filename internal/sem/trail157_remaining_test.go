package sem

import (
	"strings"
	"testing"
)

func TestRegistrationCensusSkipsGoCommentAndStringOnlyMentions(t *testing.T) {
	for _, content := range []string{"package p\n// Handle is elsewhere\n", "package p\nvar text = `Handle`\n"} {
		reads := 0
		sc := sourceContext{paths: []string{"noise.go"}, read: func(string) (string, bool) { reads++; return content, true }}
		counts, err := registrationCandidateCounts(t.Context(), sc, resolveProfile(ProfileFull), defaultMaxParseBytes, 1, map[string][]string{"Handle": {"serve"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(counts) != 0 {
			t.Fatalf("false handler: %v", counts)
		}
		if reads != 1 {
			t.Errorf("read %d times; comment/string-only file should skip the parser's second read", reads)
		}
	}
}

func TestGoInterfaceHopRequiresPackageTypeIdentity(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.com/evidence\n\ngo 1.21\n")
	writeFile(t, repo, "contract/api.go", `package contract
 type Request struct{}
 type Runner interface { Run(Request) error; Stop() error }
 type Local struct{}
 func (Local) Run(Request) error { return nil }
 func (Local) Stop() error { return nil }
 func call(r Runner) error { return r.Stop() }
 `)
	writeFile(t, repo, "wrong/impl.go", `package wrong
 type Request struct{}
 type Wrong struct{}
 func (Wrong) Run(Request) error { return nil }
 func (Wrong) Stop() error { return nil }
 `)
	writeFile(t, repo, "good/alias.go", `package good
 import "example.com/evidence/contract"
 type Request = contract.Request
 `)
	writeFile(t, repo, "good/impl.go", `package good
 type Good struct{}
 func (Good) Run(Request) error { return nil }
 func (Good) Stop() error { return nil }
 `)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := callsFromTo(snapshot, "call", "Wrong.Stop"); ok {
		t.Error("invented implementation hop between different packages' Request types")
	}
	for _, name := range []string{"Local.Stop", "Good.Stop"} {
		if _, ok := callsFromTo(snapshot, "call", name); !ok {
			t.Errorf("lost valid implementation hop to %s", name)
		}
	}
}

func TestGoSignatureEvidenceDistinguishesUnknownAndDifferent(t *testing.T) {
	for _, tc := range []struct {
		want, got string
		a, b      map[string]string
		result    goTypeComparison
	}{
		{"Run(p a.T)", "Run(p b.T)", nil, nil, goTypeUnknown},
		{"Run(p a.T)", "Run(p b.T)", map[string]string{"a": "example.com/a"}, map[string]string{"b": "example.com/b"}, goTypeDifferent},
		{"Run(p a.T)", "Run(p b.T)", map[string]string{"a": "example.com/a"}, map[string]string{"b": "example.com/a"}, goTypeMatch},
		{"Run(p []byte)", "Run(p []uint8)", nil, nil, goTypeMatch},
	} {
		got := compareGoSignatures(tc.want, tc.got, &goTypeScope{imports: tc.a}, &goTypeScope{imports: tc.b})
		if got != tc.result {
			t.Errorf("%s / %s: comparison %v, want %v", tc.want, tc.got, got, tc.result)
		}
	}
}

func TestRegistrationPrefilterPreservesCandidatesAndEmbeddedSource(t *testing.T) {
	aliases := map[string][]string{"Handle": {"serve"}}
	for _, tc := range []struct {
		file, text string
		want       bool
	}{
		{"a.go", "package p\nfunc Handle() {}", true},
		{"a.go", "package p\n/* Handle */\nvar s = \"Handle\"", false},
		{"a.go", "package p\nvar s = `Handle`", false},
		{"a.go", "package p\nvar s = \"Handle", true},
		{"a.ts", "const script = `function Handle() {}`", true},
		{"a.py", "# Handle", true},
	} {
		if got := mentionsRegistrationHandlerInFile(tc.file, tc.text, aliases); got != tc.want {
			t.Errorf("%s %q: %v want %v", tc.file, tc.text, got, tc.want)
		}
	}
}

func BenchmarkRegistrationCommentOnlyCandidate(b *testing.B) {
	content := "package p\n" + strings.Repeat("// Handle documents the command elsewhere\n", 100) + "var label = `Handle`\n"
	aliases := map[string][]string{"Handle": {"serve"}}
	sc := sourceContext{read: func(string) (string, bool) { return content, true }}
	for _, lexical := range []bool{false, true} {
		name := "before"
		if lexical {
			name = "after"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				hit := mentionsRegistrationHandler(content, aliases)
				if lexical {
					hit = mentionsRegistrationHandlerInFile("noise.go", content, aliases)
				}
				if hit {
					processProviderFile(b.Context(), sc, resolveProfile(ProfileFull), defaultMaxParseBytes, 0, "noise.go")
				}
			}
		})
	}
}

func TestGoSignaturePackageEvidenceAliasesAndAmbiguity(t *testing.T) {
	sources := map[string]string{
		"api/api.go":        "package api\ntype Request struct{}\n",
		"api/alias.go":      "package api\ntype Alias = Request\n",
		"impl/impl.go":      "package impl\nimport . \"example.com/test/api\"\ntype Local = Request\ntype Byte = byte\n",
		"other/other.go":    "package other\ntype Request struct{}\n",
		"ambiguous/impl.go": "package ambiguous\nimport . \"example.com/test/api\"\nimport . \"example.com/test/other\"\n",
		"cycle/cycle.go":    "package cycle\ntype A = B\ntype B = A\n",
	}
	var files []FileRecord
	for file := range sources {
		files = append(files, FileRecord{Path: file})
	}
	reads := map[string]int{}
	index := newGoFileImports(func(file string) (string, bool) { reads[file]++; s, ok := sources[file]; return s, ok }, files, newGoModuleIndex([]goModuleRoot{{Path: "example.com/test"}}))
	for _, tc := range []struct {
		aFile, a, bFile, b string
		match              bool
	}{
		{"api/api.go", "Run(Request)", "impl/impl.go", "Run(Request)", true},
		{"api/api.go", "Run(Request)", "impl/impl.go", "Run(Local)", true},
		{"api/api.go", "Run(Alias)", "api/api.go", "Run(Request)", true},
		{"impl/impl.go", "Run(Byte)", "api/api.go", "Run(uint8)", true},
		{"api/api.go", "Run(Request)", "other/other.go", "Run(Request)", false},
		{"api/api.go", "Run(Request)", "ambiguous/impl.go", "Run(Request)", false},
		{"cycle/cycle.go", "Run(A)", "cycle/cycle.go", "Run(B)", false},
	} {
		for n := 0; n < 2; n++ {
			got := index.signaturesMatch(SymbolRecord{FilePath: tc.aFile, Signature: tc.a}, SymbolRecord{FilePath: tc.bFile, Signature: tc.b})
			if got != tc.match {
				t.Errorf("%s %s / %s %s matched=%v want %v", tc.aFile, tc.a, tc.bFile, tc.b, got, tc.match)
			}
		}
	}
	for file, count := range reads {
		if count != 1 {
			t.Errorf("%s read %d times, want once", file, count)
		}
	}
}
