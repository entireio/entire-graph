package sem

import (
	"strings"
	"testing"
)

// Signature matching guards the interface-implementation hop, and it has two
// failure directions. Letting `Run(string) error` satisfy `Run() error` invents
// an edge Go rejects; refusing `gocontext.Context` where the interface wrote
// `context.Context` deletes an edge Go accepts. Comparing normalised spellings
// byte for byte only closed the first. Every row below is a decision about ONE
// spelling difference, and both verdicts are asserted from the same table.
func TestGoMethodSignatureIdentityDecidesBothDirections(t *testing.T) {
	t.Parallel()
	cases := []struct {
		why        string
		req, impl  string
		reqImports map[string]string // the requirement file's alias -> import path
		implImport map[string]string // the implementation file's alias -> import path
		match      bool
		permissive bool // folded because neither file's imports decide it
	}{
		// --- same type, different spelling: must match ---
		{why: "identical", req: "Run() error", impl: "func (w *W) Run() error", match: true},
		{
			why: "import alias on the same package", match: true,
			req: "Do(ctx context.Context) error", impl: "func (w *W) Do(c gocontext.Context) error",
		},
		{
			why: "dot-import or same-package spelling drops the qualifier", match: true,
			req: "Do(r io.Reader) error", impl: "func (w *W) Do(r Reader) error",
		},
		{
			why: "qualifier inside a composite type", match: true,
			req: "Map(m map[string]pkg.T, p *pkg.T) error", impl: "func (w *W) Map(m map[string]alias.T, p *alias.T) error",
		},
		{
			why: "byte and uint8 are one type by the spec", match: true,
			req: "Write(p []byte) (int, error)", impl: "func (w *W) Write(p []uint8) (n int, err error)",
		},
		{
			why: "rune and int32 are one type by the spec", match: true,
			req: "Get() rune", impl: "func (w *W) Get() int32",
		},
		{
			why: "any and interface{} are one type by the spec", match: true,
			req: "Handle(v any) error", impl: "func (w *W) Handle(v interface{}) error",
		},
		{
			why: "result names are not part of the signature", match: true,
			req: "Read(p []byte) (n int, err error)", impl: "func (w *W) Read(p []byte) (int, error)",
		},
		{
			why: "names inside a nested func type are not part of it either", match: true,
			req:  "Walk(fn func(p string, i fs.FileInfo, e error) error) error",
			impl: "func (w *W) Walk(fn func(string, os.FileInfo, error) error) error",
		},
		{
			why: "a one-result list is written both ways", match: true,
			req: "F(fn func() (error)) error", impl: "func (w *W) F(fn func() error) error",
		},
		{
			why: "a value receiver still declares the same method", match: true,
			req: "Close() error", impl: "func (w W) Close() error",
		},

		// --- genuinely different types: must NOT match ---
		{
			why: "the arity bug the signature check exists for",
			req: "Run() error", impl: "func (w *W) Run(name string) error",
		},
		{
			why: "dropping a parameter",
			req: "Do(a int, b int) error", impl: "func (w *W) Do(a int) error",
		},
		{
			why: "variadic is not a slice",
			req: "Sum(vals ...int) int", impl: "func (w *W) Sum(vals []int) int",
		},
		{
			why: "pointer-ness is part of the type",
			req: "Do(p *T) error", impl: "func (w *W) Do(p T) error",
		},
		{
			why: "an array is not a slice",
			req: "Do(p []T) error", impl: "func (w *W) Do(p [4]T) error",
		},
		{
			why: "channel direction is part of the type",
			req: "Do(c chan T) error", impl: "func (w *W) Do(c <-chan T) error",
		},
		{
			why: "map key and value are not interchangeable",
			req: "Do(m map[string]int) error", impl: "func (w *W) Do(m map[int]string) error",
		},
		{
			why: "int and int64 are convertible, not identical",
			req: "Do() int", impl: "func (w *W) Do() int64",
		},
		{
			why: "byte folds onto uint8, not onto every one-byte type",
			req: "Do(v byte) error", impl: "func (w *W) Do(v int8) error",
		},
		{
			why: "a nested func type is compared, not skipped",
			req: "F(fn func(int) error) error", impl: "func (w *W) F(fn func(string) error) error",
		},
		{
			why: "nested results are compared too",
			req: "F(fn func() (int, error)) error", impl: "func (w *W) F(fn func() error) error",
		},
		{
			why: "a generic method is declined rather than guessed",
			req: "Gen[T any](v T) error", impl: "func (w *W) Gen[T any](v T) error",
		},

		// --- qualifiers decided by the declaring files' import blocks ---
		{
			why: "two packages exporting the same type name are different types",
			req: "Do(x http.Client) error", impl: "func (w *W) Do(x redis.Client) error",
			reqImports: map[string]string{"http": "net/http"},
			implImport: map[string]string{"redis": "github.com/redis/go-redis/v9"},
		},
		{
			why: "an alias of the same package is the same type", match: true,
			req: "Do(ctx context.Context) error", impl: "func (w *W) Do(c gocontext.Context) error",
			reqImports: map[string]string{"context": "context"},
			implImport: map[string]string{"gocontext": "context"},
		},
		{
			why: "the same import written the same way in both files", match: true,
			req: "Do(x http.Client) error", impl: "func (w *W) Do(x http.Client) error",
			reqImports: map[string]string{"http": "net/http"},
			implImport: map[string]string{"http": "net/http"},
		},
		{
			why: "a qualifier inside a composite type is decided too",
			req: "Map(m map[string]http.Client) error", impl: "func (w *W) Map(m map[string]redis.Client) error",
			reqImports: map[string]string{"http": "net/http"},
			implImport: map[string]string{"redis": "github.com/redis/go-redis/v9"},
		},
		{
			why: "a qualifier nested inside a func type is decided too",
			req: "F(fn func(c http.Client) error) error", impl: "func (w *W) F(fn func(c redis.Client) error) error",
			reqImports: map[string]string{"http": "net/http"},
			implImport: map[string]string{"redis": "github.com/redis/go-redis/v9"},
		},
		{
			why: "a qualifier in a nested func RESULT is decided too",
			req: "F(fn func() http.Client) error", impl: "func (w *W) F(fn func() redis.Client) error",
			reqImports: map[string]string{"http": "net/http"},
			implImport: map[string]string{"redis": "github.com/redis/go-redis/v9"},
		},
		{
			why: "the method result is decided too",
			req: "Get() http.Client", impl: "func (w *W) Get() redis.Client",
			reqImports: map[string]string{"http": "net/http"},
			implImport: map[string]string{"redis": "github.com/redis/go-redis/v9"},
		},

		// --- undecidable even with the import blocks: folded on purpose ---
		{
			why: "with no import evidence at all the comparison stays name-only",
			req: "Do(x http.Client) error", impl: "func (w *W) Do(x redis.Client) error",
			match: true, permissive: true,
		},
		{
			why: "only one side resolving is not proof the packages differ", match: true,
			req: "Do(x http.Client) error", impl: "func (w *W) Do(x redis.Client) error",
			reqImports: map[string]string{"http": "net/http"},
			permissive: true,
		},
		{
			why: "a dot-imported or same-package bare name resolves to nothing", match: true,
			req: "Do(r io.Reader) error", impl: "func (w *W) Do(r Reader) error",
			reqImports: map[string]string{"io": "io"},
			implImport: map[string]string{"bytes": "bytes"},
			permissive: true,
		},
		{
			why: "an alias the import scanner never recorded stays folded", match: true,
			req: "Do(x http.Client) error", impl: "func (w *W) Do(x redis.Client) error",
			reqImports: map[string]string{"http": "net/http"},
			implImport: map[string]string{"other": "example.com/other"},
			permissive: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.why, func(t *testing.T) {
			t.Parallel()
			if got := goMethodSignaturesMatch(tc.req, tc.impl, tc.reqImports, tc.implImport); got != tc.match {
				verdict := "must not match"
				if tc.match {
					verdict = "must match"
				}
				if tc.permissive {
					verdict += " (folded because the import blocks do not decide it)"
				}
				t.Fatalf("%s: %q vs %q matched=%v, %s\n  req key:  %s\n  impl key: %s",
					tc.why, tc.req, tc.impl, got, verdict,
					signatureIdentityKeyForTest(tc.req), signatureIdentityKeyForTest(tc.impl))
			}
		})
	}
}

// signatureIdentityKeyForTest renders what the matcher actually compares, so a
// failure names the spelling difference instead of just reporting a boolean.
func signatureIdentityKeyForTest(signature string) string {
	normalized, ok := goNormalizedMethodSignature(signature)
	if !ok {
		return "<declined>"
	}
	return goTypeIdentityKey(normalized)
}

// The end-to-end shape of the same defect: the interface and its implementations
// live in different packages, so they import the same types under different
// aliases. `GoodWorker` implements `Runner` — Go accepts it — and must receive
// the implementation hop; `BadWorker` takes one argument where the interface
// requires two and must not.
func TestGoInterfaceCallReachesAnAliasImportedImplementation(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.com/aliased\n\ngo 1.21\n")
	writeFile(t, repo, "iface/iface.go", `package iface

import (
	"context"
	"io"
)

type Runner interface {
	Run(ctx context.Context, payload []byte) error
	Stop(w io.Writer) error
}
`)
	// Same types as the interface, spelled through import aliases, through the
	// byte/uint8 alias, and with one parameter name elided entirely.
	writeFile(t, repo, "good/good.go", `package good

import (
	gocontext "context"
	stdio "io"
)

type GoodWorker struct{}

func (w *GoodWorker) Run(c gocontext.Context, payload []uint8) error { return nil }

func (w *GoodWorker) Stop(stdio.Writer) error { return nil }
`)
	// BadWorker.Run drops a parameter, so BadWorker does not implement Runner.
	writeFile(t, repo, "bad/bad.go", `package bad

import (
	"context"
	"io"
)

type BadWorker struct{}

func (w *BadWorker) Run(ctx context.Context) error { return nil }

func (w *BadWorker) Stop(out io.Writer) error { return nil }
`)
	writeFile(t, repo, "consumer/consumer.go", `package consumer

import (
	"context"
	"os"

	"example.com/aliased/iface"
)

func drive(ctx context.Context, r iface.Runner) error {
	if err := r.Run(ctx, nil); err != nil {
		return err
	}
	return r.Stop(os.Stdout)
}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]SymbolRecord{}
	for _, symbol := range snapshot.Symbols {
		byID[symbol.ID] = symbol
	}
	implFiles := map[string]bool{}
	for _, relation := range snapshot.Relations {
		if relation.Type != "CALLS" || relation.Reason != "interface method call carried to the implementing method" {
			continue
		}
		implFiles[byID[relation.ToID].FilePath] = true
	}
	if !implFiles["good/good.go"] {
		t.Fatalf("an implementation Go accepts lost its hop because the interface "+
			"and the implementation spell the same types differently: %v", implFiles)
	}
	if implFiles["bad/bad.go"] {
		t.Fatalf("interface call carried to a method that cannot implement Runner: %v", implFiles)
	}
	for file := range implFiles {
		if !strings.HasPrefix(file, "good/") {
			t.Fatalf("implementation hop landed outside the implementing package: %s", file)
		}
	}
}
