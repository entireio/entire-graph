// Command grammar-refresh vendors a tree-sitter grammar into
// internal/sem/grammars/<lang> from its upstream git repository and records
// where it came from in internal/sem/grammars/grammars.json. It is a
// maintainer tool: it needs the network, and its output is committed, so
// building entire-graph never downloads anything.
//
// Usage:
//
//	go run ./cmd/grammar-refresh -lang zig -ref v1.1.2
//	go run ./cmd/grammar-refresh -lang fsharp -ref <commit> -path fsharp -allow-local-changes
//	go run ./cmd/grammar-refresh -lang newlang -ref v1.0.0 -url https://github.com/o/tree-sitter-newlang
//
// The ref must contain generated sources (src/parser.c) built for ABI 13-14,
// the range the go-tree-sitter runtime loads. binding.go is never touched, so
// a new language still needs one. A grammar that changes parse results needs
// an IdentityRevision bump (internal/sem/provider.go) in the same commit.
//
// A manifest entry with local_changes carries edits that upstream does not
// have, and copying upstream verbatim would discard them. The tool refuses
// such a grammar unless -allow-local-changes is passed, and the maintainer then
// reapplies the steps in the entry's note. Notes survive every refresh.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

func main() {
	var (
		lang         = flag.String("lang", "", "grammar directory name under -dir (required)")
		ref          = flag.String("ref", "", "tag, branch or full commit to vendor (required)")
		url          = flag.String("url", "", "git URL; defaults to the manifest entry")
		subdir       = flag.String("path", "", "grammar subdirectory within the repository; defaults to the manifest entry")
		manifestPath = flag.String("manifest", "internal/sem/grammars/grammars.json", "path to the grammar manifest")
		grammarsDir  = flag.String("dir", "internal/sem/grammars", "directory holding vendored grammars")
		allowLocal   = flag.Bool("allow-local-changes", false, "overwrite a grammar whose manifest entry records local changes; they must then be reapplied by hand")
		generateFlag = flag.Bool("generate", false, "regenerate the parser with tree-sitter-cli (via npx) even if the manifest entry records no local build")
		cliFlag      = flag.String("tree-sitter-cli", "", "tree-sitter-cli version to generate with; implies -generate")
	)
	flag.Parse()
	if *lang == "" || *ref == "" {
		flag.Usage()
		os.Exit(2)
	}
	o := options{
		Lang:              *lang,
		Ref:               *ref,
		URL:               *url,
		Subdir:            *subdir,
		ManifestPath:      *manifestPath,
		GrammarsDir:       *grammarsDir,
		AllowLocalChanges: *allowLocal,
		Generate:          *generateFlag,
		TreeSitterCLI:     *cliFlag,
	}
	if err := refresh(o); err != nil {
		fmt.Fprintln(os.Stderr, "grammar-refresh:", err)
		os.Exit(1)
	}
}

var languageNamePattern = regexp.MustCompile(`^[a-z0-9_]+$`)

// options holds one run's settings. An empty URL or Subdir falls back to the
// manifest entry.
type options struct {
	Lang, Ref, URL, Subdir    string
	ManifestPath, GrammarsDir string
	AllowLocalChanges         bool
	// Generate regenerates the parser even when the entry records no
	// tree-sitter-cli; TreeSitterCLI overrides the version chosen.
	Generate      bool
	TreeSitterCLI string
}

func refresh(o options) error {
	lang, ref, url, subdir := o.Lang, o.Ref, o.URL, o.Subdir
	manifestPath, grammarsDir := o.ManifestPath, o.GrammarsDir
	if !languageNamePattern.MatchString(lang) {
		return fmt.Errorf("invalid -lang %q: use the lower-case directory name under %s", lang, grammarsDir)
	}
	if subdir != "" && !filepath.IsLocal(subdir) {
		return fmt.Errorf("invalid -path %q: must be a relative path inside the repository", subdir)
	}
	grammars, err := loadManifest(manifestPath)
	if err != nil {
		return err
	}
	var existing Grammar
	for _, g := range grammars {
		if g.Language == lang {
			existing = g
		}
	}
	if url == "" {
		url = existing.URL
	}
	if url == "" {
		return fmt.Errorf("%s is not in %s; pass -url", lang, manifestPath)
	}
	if subdir == "" {
		subdir = existing.Path
	}
	if subdir != "" && !filepath.IsLocal(subdir) {
		return fmt.Errorf("invalid path %q for %s in %s: must be a relative path inside the repository", subdir, lang, manifestPath)
	}
	if existing.LocalChanges && !o.AllowLocalChanges {
		return fmt.Errorf("%s has local changes that a verbatim refresh would discard (%s); pass -allow-local-changes to overwrite it, then reapply them by hand", lang, existing.Note)
	}
	build := existing.LocalBuild
	var patchFile string
	if build != nil && build.Patch != "" {
		if !filepath.IsLocal(build.Patch) {
			return fmt.Errorf("invalid patch %q for %s in %s: must be a relative path inside %s", build.Patch, lang, manifestPath, grammarsDir)
		}
		if patchFile, err = filepath.Abs(filepath.Join(grammarsDir, build.Patch)); err != nil {
			return err
		}
	}
	generating := wantsGeneration(existing, o)
	if generating {
		if _, err := exec.LookPath("npx"); err != nil {
			return errNoNPX
		}
	}

	tmp, err := os.MkdirTemp("", "grammar-refresh-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := git(tmp, "init", "-q"); err != nil {
		return err
	}
	if err := git(tmp, "fetch", "-q", "--depth", "1", "--", url, ref); err != nil {
		return fmt.Errorf("fetch %s %s: %w", url, ref, err)
	}
	if err := git(tmp, "checkout", "-q", "FETCH_HEAD"); err != nil {
		return err
	}
	revision, err := gitOutput(tmp, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if patchFile != "" {
		if err := applyPatch(tmp, subdir, patchFile); err != nil {
			return fmt.Errorf("%s at %s: %w", url, ref, err)
		}
	}
	var version string
	if generating {
		grammarRoot := filepath.Join(tmp, subdir)
		if version, err = resolveCLI(grammarRoot, cliVersion(existing, revision, o)); err != nil {
			return err
		}
		args := generateArgs(build)
		if err := generate(grammarRoot, version, args); err != nil {
			return err
		}
		next := LocalBuild{}
		if build != nil {
			next = *build
		}
		next.TreeSitterCLI, next.GenerateArgs = version, args
		build = &next
	}

	files, abi, err := vendor(tmp, subdir, filepath.Join(grammarsDir, lang))
	var unsupported abiError
	if generating && errors.As(err, &unsupported) {
		return fmt.Errorf("%s at %s: %w; tree-sitter-cli %s may no longer generate ABI 14, so pass -tree-sitter-cli <older version>", url, ref, err, version)
	}
	if err != nil {
		return fmt.Errorf("%s at %s: %w", url, ref, err)
	}
	grammars = upsert(grammars, Grammar{
		Language:  lang,
		URL:       url,
		Reference: ref,
		Revision:  revision,
		Path:      subdir,
		Files:     files,
		ABI:       abi,
		// Kept across a revision change: that is when a note's manual steps
		// are due. A note the new revision makes obsolete is removed by hand.
		LocalChanges: existing.LocalChanges,
		Note:         existing.Note,
		LocalBuild:   build,
	})
	if err := saveManifest(manifestPath, grammars); err != nil {
		return err
	}
	fmt.Printf("vendored %s %s (%s, ABI %d) into %s\n", lang, ref, revision[:7], abi, filepath.Join(grammarsDir, lang))
	if existing.LocalChanges {
		fmt.Printf("warning: %s now lacks its local changes; reapply them before committing\n", lang)
	}
	if existing.Note != "" {
		fmt.Printf("note: %s\n", existing.Note)
	}
	fmt.Println("next: bump IdentityRevision in internal/sem/provider.go if parse results change, and run scripts/gen-notices.sh")
	return nil
}

func git(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// applyPatch applies patchFile to the grammar at subdir inside checkout; the
// patch's paths are relative to the grammar directory.
func applyPatch(checkout, subdir, patchFile string) error {
	args := []string{"apply"}
	if subdir != "" {
		args = append(args, "--directory="+filepath.ToSlash(subdir))
	}
	args = append(args, "--", patchFile)
	cmd := exec.Command("git", args...)
	cmd.Dir = checkout
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("apply %s: %w\n%s", patchFile, err, strings.TrimSpace(string(out)))
	}
	return nil
}

var errNoNPX = errors.New("tree-sitter generation needs Node.js; npx was not found on PATH")

func npx(dir string, args ...string) (string, error) {
	cmd := exec.Command("npx", append([]string{"--yes"}, args...)...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if errors.Is(err, exec.ErrNotFound) {
		err = errNoNPX
	}
	return strings.TrimSpace(string(out)), err
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
