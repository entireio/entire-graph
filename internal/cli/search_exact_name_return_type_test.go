package cli

import (
	"bytes"
	"strings"
	"testing"
)

// A Go function that RETURNS the queried type does not define it. Only `func (<receiver>) Name(`
// (or `Name[`) is a method declaration of Name; a name after a closing parenthesis that is not
// followed by a parameter list is a result type.
func TestExactNameGoResultTypeIsNotADefinition(t *testing.T) {
	t.Parallel()
	cases := []struct {
		line string
		want bool
	}{
		// Result types: the name follows `)` but is not followed by `(` or `[`.
		{"func DefaultOptions() Options {", false},
		{"func (o Options) Merge(other Options) Options {", false},
		{"func G[U any](x U) Options {", false},
		{"func (o *Options) Clone() *Options {", false},
		{"func (o Options) Pair() (Options, error) {", false},
		{"func NewOptions(n int) (Options) {", false},
		{"\tfn := func() Options { return Options{} }", false},
		{"func() Options {", false},
		{"func (r R) X() Options[int] {", false},
		{"func (o Options) Validate() error {", false},
		// Method declarations of the name: one balanced receiver group, then the name and `(`/`[`.
		{"func (r R) Options() Options {", true},
		{"func (r *R) Options(ctx context.Context) error {", true},
		{"func (r *R[K, V]) Options() {", true},
		{"func (r R[T]) Options[U any]() {", true},
		{"func(r R) Options() {", true},
		{"func (r R) Options () {", true},
		{"\tfunc (r R) Options() {", true},
		// Other definition shapes keep their verdicts.
		{"\tOptions := build()", true},
		{"const Options = 1", true},
		{"func Options() {", true},
		{"type Options struct {", true},
		{"var Options = Options{}", true},
		{"\tOptions = other", false},
	}
	for _, c := range cases {
		if got := exactNameLineDefines(c.line, "Options", ".go"); got != c.want {
			t.Errorf("%q: defines = %v, want %v", c.line, got, c.want)
		}
	}
}

// End to end: on a repo whose cfg.go defines Options and also has a constructor and a method that
// return it, the exact answer for `Options` shows the type and never prints DefaultOptions or
// Options.Merge as exact definitions of it.
func TestSearchExactNameDoesNotShowReturnTypeFuncsAsDefinitions(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Tests")
	git(t, repo, "config", "user.email", "tests@entire.local")
	write(t, repo, "go.mod", "module example.com/cfgdemo\n\ngo 1.22\n")
	write(t, repo, "cfg/cfg.go", `package cfg

// Options configures the demo.
type Options struct {
	Name  string
	Limit int
}

// DefaultOptions returns the default configuration.
func DefaultOptions() Options {
	return Options{Name: "default", Limit: 10}
}

// Merge overlays other onto o.
func (o Options) Merge(other Options) Options {
	if other.Name != "" {
		o.Name = other.Name
	}
	if other.Limit != 0 {
		o.Limit = other.Limit
	}
	return o
}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "cfg fixture")

	var out bytes.Buffer
	if err := Run(t.Context(), Options{
		Version: "0.1.0",
		Env:     EntireEnv{RepoRoot: repo},
		Stdout:  &out,
	}, []string{"search", "--repo", repo, "--format", "agent", "--head", "--max-context-bytes", "4096", "Options"}); err != nil {
		t.Fatalf("search: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "exact name:") || !strings.Contains(got, "type Options struct {") {
		t.Fatalf("the exact answer for the type was not taken:\n%s", got)
	}
	for _, header := range strings.Split(got, "\n") {
		if !strings.HasPrefix(header, "1. ") && !strings.HasPrefix(header, "2. ") && !strings.HasPrefix(header, "3. ") {
			continue
		}
		if strings.Contains(header, "DefaultOptions") || strings.Contains(header, "Merge") {
			t.Fatalf("a function returning Options was shown as an exact definition (%q):\n%s", header, got)
		}
	}
	for _, line := range []string{"func DefaultOptions() Options {", "func (o Options) Merge(other Options) Options {"} {
		if strings.Contains(got, line) {
			t.Fatalf("exact answer prints %q as a definition of Options:\n%s", line, got)
		}
	}
}
